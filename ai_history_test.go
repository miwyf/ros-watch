package main

import (
	"context"
	"errors"
	"testing"
)

func TestAIHistoryPaginationAndAtomicTurns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	c, err := openControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.db.Close()
	profile := aiProfile{ID: "fixture", BaseURL: "https://model.example.test/v1", Model: "fixture"}
	var oldest string
	reply := aiChatReply{Answer: "fixture reply", Model: "fixture"}
	turn := []aiMessage{{Role: "user", Content: "question"}, {Role: "assistant", Content: reply.Answer}}
	for i := 0; i <= aiHistoryPageSize; i++ {
		id := "fixture-" + itoa(i)
		conversation := aiConversation{
			aiConversationSummary: aiConversationSummary{ID: id, Profile: profile.ID},
			profileHash:           aiProfileHash(profile),
		}
		if err := c.saveAITurn(ctx, conversation, "question", reply, turn); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = id
		}
	}
	page, err := c.aiConversations(ctx, 0)
	if err != nil || page.Before == 0 || len(page.Items) != aiHistoryPageSize {
		t.Fatalf("first list page: %v %v", page, err)
	}
	tail, err := c.aiConversations(ctx, page.Before)
	if err != nil || len(tail.Items) != 1 || tail.Items[0].ID != oldest || tail.Before != 0 {
		t.Fatalf("last list page: %v %v", tail, err)
	}
	beforeUpdates, err := c.aiConversation(ctx, oldest)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < aiHistoryPageSize+1; i++ {
		conversation, err := c.aiConversation(ctx, oldest)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.saveAITurn(ctx, conversation, "follow-up", reply, turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.saveAITurn(ctx, beforeUpdates, "stale update", reply, turn); !errors.Is(err, errAIConversationChanged) {
		t.Fatalf("concurrent stale snapshot was accepted: %v", err)
	}
	if err := c.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	current, err := reopened.aiConversation(ctx, oldest)
	if err != nil || !current.matches(profile) || len(current.context) != 6 {
		t.Fatalf("context on reopen: %v %v", current.context, err)
	}
	recent, err := reopened.aiSavedTurns(ctx, oldest, 0)
	if err != nil || recent.Before == 0 || len(recent.Items) != aiHistoryPageSize {
		t.Fatalf("recent turns: %v %v", recent, err)
	}
	if recent.Items[0].Question == "stale update" || recent.Items[len(recent.Items)-1].Question != "follow-up" {
		t.Fatalf("unexpected order or stale turn: %v", recent.Items)
	}
	prior, err := reopened.aiSavedTurns(ctx, oldest, recent.Before)
	if err != nil || len(prior.Items) != 2 || prior.Items[0].Question != "question" || prior.Before != 0 {
		t.Fatalf("older turns after restart: %v %v", prior, err)
	}
}
