package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const aiHistoryPageSize = 30

type aiChatReply struct {
	Conversation string       `json:"conversation,omitempty"`
	Answer       string       `json:"answer"`
	Model        string       `json:"model"`
	Evidence     []aiEvidence `json:"evidence"`
	BlockSources []string     `json:"block_sources"`
}

type aiConversationSummary struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Profile   string  `json:"profile"`
	UpdatedAt float64 `json:"updated_at"`
}

type aiConversation struct {
	aiConversationSummary
	profileHash []byte
	lastTurn    int64
	context     [][]aiMessage
}

type aiSavedTurn struct {
	ID       int64       `json:"id"`
	Question string      `json:"question"`
	Reply    aiChatReply `json:"reply"`
}

type aiHistoryPage[T any] struct {
	Items  []T   `json:"items"`
	Before int64 `json:"before,omitempty"`
}

var errAIConversationChanged = errors.New("AI conversation changed during update")

func aiProfileHash(profile aiProfile) []byte {
	sum := sha256.Sum256([]byte(profile.BaseURL + "\x00" + profile.Model + "\x00" + profile.APIKey))
	return sum[:]
}

func (history aiConversation) matches(profile aiProfile) bool {
	return history.Profile == profile.ID && bytes.Equal(history.profileHash, aiProfileHash(profile))
}

func (history aiConversation) messages() []aiMessage {
	var messages []aiMessage
	for _, turn := range history.context {
		messages = append(messages, turn...)
	}
	return messages
}

func (c *Control) aiConversation(ctx context.Context, id string) (aiConversation, error) {
	var conversation aiConversation
	var raw string
	err := c.db.QueryRowContext(ctx, `SELECT id,title,profile,profile_hash,updated_at,last_turn,context
 FROM ai_conversations WHERE id=?`, id).
		Scan(&conversation.ID, &conversation.Title, &conversation.Profile, &conversation.profileHash,
			&conversation.UpdatedAt, &conversation.lastTurn, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &conversation.context)
	}
	return conversation, err
}

func (c *Control) aiConversations(ctx context.Context, before int64) (aiHistoryPage[aiConversationSummary], error) {
	page := aiHistoryPage[aiConversationSummary]{Items: []aiConversationSummary{}}
	rows, err := c.db.QueryContext(ctx, `SELECT id,title,profile,updated_at,last_turn
 FROM ai_conversations WHERE (?=0 OR last_turn<?) ORDER BY last_turn DESC LIMIT ?`, before, before, aiHistoryPageSize+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var previous int64
	for rows.Next() {
		var item aiConversationSummary
		var last int64
		if err := rows.Scan(&item.ID, &item.Title, &item.Profile, &item.UpdatedAt, &last); err != nil {
			return page, err
		}
		if len(page.Items) == aiHistoryPageSize {
			page.Before = previous
			break
		}
		page.Items = append(page.Items, item)
		previous = last
	}
	return page, rows.Err()
}

func (c *Control) aiSavedTurns(ctx context.Context, id string, before int64) (aiHistoryPage[aiSavedTurn], error) {
	page := aiHistoryPage[aiSavedTurn]{Items: []aiSavedTurn{}}
	rows, err := c.db.QueryContext(ctx, `SELECT id,question,reply FROM ai_turns
 WHERE conversation_id=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?`, id, before, before, aiHistoryPageSize+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == aiHistoryPageSize {
			page.Before = page.Items[len(page.Items)-1].ID
			break
		}
		var item aiSavedTurn
		var raw string
		if err := rows.Scan(&item.ID, &item.Question, &raw); err != nil {
			return page, err
		}
		if err := json.Unmarshal([]byte(raw), &item.Reply); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	slices.Reverse(page.Items)
	return page, rows.Err()
}

func (c *Control) saveAITurn(ctx context.Context, conversation aiConversation, question string, reply aiChatReply, turn []aiMessage) error {
	// Preserve complete tool-call turns while limiting the context sent to the model.
	window := append(conversation.context, turn)
	if len(window) > 6 {
		window = window[len(window)-6:]
	}
	var contextJSON []byte
	for {
		var err error
		contextJSON, err = json.Marshal(window)
		if err != nil {
			return err
		}
		if len(contextJSON) <= 96000 || len(window) == 1 {
			break
		}
		window = window[1:]
	}
	reply.Conversation = ""
	replyJSON, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := unixNow()
	if conversation.lastTurn == 0 {
		title := truncateRunes(strings.Join(strings.Fields(question), " "), 64)
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_conversations(id,title,profile,profile_hash,updated_at,last_turn,context)
 VALUES(?,?,?,?,?,0,'[]')`, conversation.ID, title, conversation.Profile, conversation.profileHash, now); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO ai_turns(conversation_id,question,reply) VALUES(?,?,?)", conversation.ID, question, string(replyJSON))
	if err != nil {
		return err
	}
	last, err := result.LastInsertId()
	if err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, "UPDATE ai_conversations SET last_turn=?,updated_at=?,context=? WHERE id=? AND last_turn=?",
		last, now, string(contextJSON), conversation.ID, conversation.lastTurn)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return errAIConversationChanged
	}
	return tx.Commit()
}
