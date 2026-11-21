package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type aiCustomIntelSettings struct {
	Enabled  bool              `json:"enabled"`
	URL      string            `json:"url"`
	Auth     string            `json:"auth"`
	APIKey   string            `json:"api_key,omitempty"`
	KeySet   bool              `json:"key_set,omitempty"`
	ClearKey bool              `json:"clear_key,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

const aiIntelIPPlaceholder = "{ip}"

var aiIntelFieldPath = regexp.MustCompile(`^[a-zA-Z0-9_-]+(\.[a-zA-Z0-9_-]+){0,7}$`)

func validAIIntelURL(raw string) bool {
	const marker = "roswatch-ip-placeholder"
	if len(raw) > 1024 || strings.Count(raw, aiIntelIPPlaceholder) != 1 || strings.Contains(raw, marker) {
		return false
	}
	u, err := url.Parse(strings.Replace(raw, aiIntelIPPlaceholder, marker, 1))
	if err != nil || u.User != nil || u.Fragment != "" || strings.Contains(u.Host, marker) {
		return false
	}
	if _, err := normalizeAIBase(u.Scheme + "://" + u.Host); err != nil {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	if strings.Contains(u.Path, marker) {
		return true
	}
	for _, values := range query {
		for _, value := range values {
			if strings.Contains(value, marker) {
				return true
			}
		}
	}
	return false
}

func (c *aiCustomIntelSettings) validate(previous aiCustomIntelSettings) error {
	c.URL, c.APIKey = strings.TrimSpace(c.URL), strings.TrimSpace(c.APIKey)
	switch c.Auth {
	case "", "none":
		c.Auth = "none"
	case "bearer", "x-api-key", "key":
	default:
		return errors.New("自定义情报认证方式无效")
	}
	if c.ClearKey || c.Auth == "none" {
		c.APIKey = ""
	} else if c.APIKey == "" && c.URL == previous.URL && c.Auth == previous.Auth {
		c.APIKey = previous.APIKey
	}
	c.ClearKey, c.KeySet = false, false
	if !validAIKey(c.APIKey) {
		return errors.New("自定义情报 API Key 格式无效")
	}
	if (c.Enabled || c.URL != "") && !validAIIntelURL(c.URL) {
		return errors.New("自定义情报地址须在路径或查询值中包含一个 {ip}，公网地址使用 HTTPS")
	}
	if c.Enabled && c.Auth != "none" && c.APIKey == "" {
		return errors.New("启用该自定义情报认证方式需要 API Key")
	}
	for field, path := range c.Fields {
		path = strings.TrimSpace(path)
		if path == "" {
			delete(c.Fields, field)
			continue
		}
		switch field {
		case "classification", "listed", "confidence", "reported_count", "last_seen":
		default:
			return errors.New("自定义情报包含未知返回字段")
		}
		if len(path) > 120 || !aiIntelFieldPath.MatchString(path) {
			return errors.New("自定义情报字段路径格式无效，例如 data.score")
		}
		c.Fields[field] = path
	}
	if c.Enabled && len(c.Fields) == 0 {
		return errors.New("请至少填写一个自定义情报返回字段")
	}
	return nil
}

func aiIntelMappedValue(data map[string]any, path string) any {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		data, _ = data[part].(map[string]any)
	}
	return data[parts[len(parts)-1]]
}

func aiIntelTypedValue(field string, value any, key string) any {
	switch field {
	case "classification":
		if v, ok := value.(string); ok && (key == "" || !strings.Contains(v, key)) {
			if v = strings.TrimSpace(v); v != "" {
				return truncateRunes(v, 48)
			}
		}
	case "last_seen":
		if v, ok := value.(string); ok {
			for _, format := range []string{time.RFC3339, time.DateOnly} {
				if _, err := time.Parse(format, v); err == nil {
					return v
				}
			}
		}
	case "listed":
		if v, ok := value.(bool); ok {
			return v
		}
	case "confidence":
		if v, ok := value.(float64); ok && v >= 0 && v <= 100 {
			return v
		}
	case "reported_count":
		if v, ok := value.(float64); ok && v >= 0 && v == math.Trunc(v) {
			return v
		}
	}
	return nil
}

func (s *aiService) customIntel(ctx context.Context, ip netip.Addr, c aiCustomIntelSettings) aiIntelValue {
	result := aiIntelValue{Service: "自定义 IP 情报", Status: "invalid_response", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	headers := map[string]string{}
	switch c.Auth {
	case "bearer":
		headers["Authorization"] = "Bearer " + c.APIKey
	case "x-api-key":
		headers["X-API-Key"] = c.APIKey
	case "key":
		headers["Key"] = c.APIKey
	}
	endpoint := strings.Replace(c.URL, aiIntelIPPlaceholder, url.QueryEscape(ip.String()), 1)
	raw, err := s.getIntel(ctx, endpoint, headers)
	var status aiHTTPError
	if errors.As(err, &status) && status.status == http.StatusNotFound {
		result.Status = "no_record"
		return result
	}
	if err != nil {
		result.Status = aiIntelStatus(err)
		return result
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return result
	}
	values := map[string]any{}
	for field, path := range c.Fields {
		if v := aiIntelTypedValue(field, aiIntelMappedValue(data, path), c.APIKey); v != nil {
			values[field] = v
		}
	}
	if len(values) > 0 {
		result.Result = values
		result.Status = "queried"
		if len(values) != len(c.Fields) {
			result.Status = "partial"
		}
	}
	return result
}
