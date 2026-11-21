package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

type aiProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key,omitempty"`
	KeySet   bool   `json:"key_set,omitempty"`
	ClearKey bool   `json:"clear_key,omitempty"`
}

type aiReputationSettings struct {
	GreyNoise   bool                  `json:"greynoise"`
	GreyKey     string                `json:"grey_key,omitempty"`
	GreyKeySet  bool                  `json:"grey_key_set,omitempty"`
	ClearGrey   bool                  `json:"clear_grey,omitempty"`
	AbuseIPDB   bool                  `json:"abuseipdb"`
	AbuseKey    string                `json:"abuse_key,omitempty"`
	AbuseKeySet bool                  `json:"abuse_key_set,omitempty"`
	ClearAbuse  bool                  `json:"clear_abuse,omitempty"`
	Spamhaus    bool                  `json:"spamhaus"`
	Custom      aiCustomIntelSettings `json:"custom"`
}

func (s aiReputationSettings) enabled() bool {
	return s.GreyNoise || s.AbuseIPDB || s.Spamhaus || s.Custom.Enabled
}

type aiSettings struct {
	Default    string               `json:"default_profile"`
	Profiles   []aiProfile          `json:"profiles"`
	Reputation aiReputationSettings `json:"reputation"`
}

var aiProfileID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (c *Control) loadAI(ctx context.Context) (aiSettings, error) {
	var raw string
	var value aiSettings
	err := c.db.QueryRowContext(ctx, "SELECT value FROM ai_settings WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return aiSettings{Profiles: []aiProfile{}}, nil
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal([]byte(raw), &value)
	return value, err
}

func (c *Control) saveAI(ctx context.Context, value aiSettings) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, "INSERT INTO ai_settings(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", string(raw))
	return err
}

func (s aiSettings) public() aiSettings {
	profiles := make([]aiProfile, len(s.Profiles))
	for i, p := range s.Profiles {
		p.KeySet, p.APIKey = p.APIKey != "", ""
		profiles[i] = p
	}
	s.Profiles = profiles
	r := &s.Reputation
	r.GreyKeySet, r.GreyKey = r.GreyKey != "", ""
	r.AbuseKeySet, r.AbuseKey = r.AbuseKey != "", ""
	r.Custom.KeySet, r.Custom.APIKey = r.Custom.APIKey != "", ""
	return s
}

func (s aiSettings) profile(id string) (aiProfile, bool) {
	if id == "" {
		id = s.Default
	}
	for _, p := range s.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return aiProfile{}, false
}

func normalizeAIBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || len(raw) > 1024 || u == nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("模型 API 地址应为不带凭据或参数的 HTTP(S) Base URL")
	}
	if u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		ip, err := netip.ParseAddr(host)
		if host != "localhost" && host != "host.docker.internal" &&
			(err != nil || (!ip.Unmap().IsPrivate() && !ip.Unmap().IsLoopback())) {
			return "", errors.New("公网模型 API 地址请使用 HTTPS")
		}
	}
	base := strings.TrimRight(u.String(), "/")
	return strings.TrimSuffix(base, "/chat/completions"), nil
}

func validAIKey(key string) bool {
	return len(key) <= 2048 && !strings.ContainsAny(key, "\r\n\x00")
}

func (s *aiSettings) validate(previous aiSettings) error {
	if len(s.Profiles) > 10 {
		return errors.New("最多保存 10 个模型连接")
	}
	if s.Profiles == nil {
		s.Profiles = []aiProfile{}
	}
	seen := map[string]bool{}
	for i := range s.Profiles {
		p := &s.Profiles[i]
		p.Name, p.Model, p.APIKey = strings.TrimSpace(p.Name), strings.TrimSpace(p.Model), strings.TrimSpace(p.APIKey)
		if !aiProfileID.MatchString(p.ID) || seen[p.ID] || p.Name == "" || len(p.Name) > 120 ||
			p.Model == "" || len(p.Model) > 256 || strings.ContainsAny(p.Model, "\r\n\x00") {
			return errors.New("模型连接需要唯一标识、名称及有效的模型 ID")
		}
		seen[p.ID] = true
		base, err := normalizeAIBase(p.BaseURL)
		if err != nil {
			return err
		}
		p.BaseURL = base
		old, _ := previous.profile(p.ID)
		if p.ClearKey {
			p.APIKey = ""
		} else if p.APIKey == "" && old.BaseURL == p.BaseURL {
			p.APIKey = old.APIKey
		}
		if !validAIKey(p.APIKey) {
			return errors.New("模型 API Key 格式无效")
		}
		p.KeySet, p.ClearKey = false, false
	}
	if len(s.Profiles) == 0 {
		s.Default = ""
	} else if s.Default == "" {
		s.Default = s.Profiles[0].ID
	} else if !seen[s.Default] {
		return errors.New("默认模型连接不存在")
	}
	r := &s.Reputation
	r.GreyKey, r.AbuseKey = strings.TrimSpace(r.GreyKey), strings.TrimSpace(r.AbuseKey)
	if r.ClearGrey {
		r.GreyKey = ""
	} else if r.GreyKey == "" {
		r.GreyKey = previous.Reputation.GreyKey
	}
	if r.ClearAbuse {
		r.AbuseKey = ""
	} else if r.AbuseKey == "" {
		r.AbuseKey = previous.Reputation.AbuseKey
	}
	if !validAIKey(r.GreyKey) || !validAIKey(r.AbuseKey) {
		return errors.New("情报 API Key 格式无效")
	}
	if r.AbuseIPDB && r.AbuseKey == "" {
		return errors.New("启用 AbuseIPDB 需要 API Key")
	}
	r.GreyKeySet, r.AbuseKeySet, r.ClearGrey, r.ClearAbuse = false, false, false, false
	return r.Custom.validate(previous.Reputation.Custom)
}
