package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

type NotificationSettings struct {
	Enabled       bool   `json:"enabled"`
	Platform      string `json:"platform"`
	Webhook       string `json:"webhook"`
	Secret        string `json:"secret"`
	LoginFailures bool   `json:"login_failures"`
	LoginSuccess  bool   `json:"login_success"`
	Storage       bool   `json:"storage"`
	Scans         bool   `json:"scans"`
	TrustedAdmins string `json:"trusted_admins"`
	networks      []netip.Prefix
}

func defaultNotifications() NotificationSettings {
	return NotificationSettings{Platform: "feishu", LoginFailures: true, Storage: true}
}

func (s *NotificationSettings) validate() error {
	if s.Platform != "feishu" && s.Platform != "wecom" && s.Platform != "dingtalk" {
		return errors.New("请选择飞书、企业微信或钉钉")
	}
	s.Webhook, s.Secret = strings.TrimSpace(s.Webhook), strings.TrimSpace(s.Secret)
	if len(s.Secret) > 512 || strings.ContainsAny(s.Secret, "\r\n\x00") {
		return errors.New("签名密钥格式无效")
	}
	if s.Platform == "wecom" && s.Secret != "" {
		return errors.New("企业微信群机器人不使用此签名密钥，请清除密钥")
	}
	if s.Webhook != "" {
		if _, err := s.endpoint(); err != nil {
			return err
		}
	} else if s.Enabled {
		return errors.New("请填写 Webhook 地址")
	}
	// Reuse the IP/CIDR validation without mixing management clients with syslog senders.
	trusted := defaultSettings()
	trusted.AllowedSenders = s.TrustedAdmins
	if err := trusted.validate(); err != nil {
		return errors.New("可信管理来源须为具体 IP 或网段，不能允许所有地址")
	}
	s.TrustedAdmins, s.networks = trusted.AllowedSenders, trusted.networks
	if s.Enabled && s.LoginSuccess && len(s.networks) == 0 {
		return errors.New("启用非可信来源登录通知前，请填写可信管理来源")
	}
	return nil
}

var notificationToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func (s NotificationSettings) endpoint() (*url.URL, error) {
	u, err := url.Parse(s.Webhook)
	invalid := errors.New("Webhook 须为所选平台的官方 HTTPS 群机器人地址")
	if err != nil || len(s.Webhook) > 2048 || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, invalid
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || u.RawPath != "" {
		return nil, invalid
	}
	switch s.Platform {
	case "feishu":
		token := strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")
		if u.Hostname() != "open.feishu.cn" || token == u.Path || !notificationToken.MatchString(token) || len(query) != 0 {
			return nil, invalid
		}
	case "wecom", "dingtalk":
		host, path, key := "qyapi.weixin.qq.com", "/cgi-bin/webhook/send", "key"
		if s.Platform == "dingtalk" {
			host, path, key = "oapi.dingtalk.com", "/robot/send", "access_token"
		}
		if u.Hostname() != host || u.Path != path || len(query) != 1 || len(query[key]) != 1 || query.Get(key) == "" {
			return nil, invalid
		}
	default:
		return nil, invalid
	}
	return u, nil
}

func (s NotificationSettings) public() map[string]any {
	return map[string]any{"enabled": s.Enabled, "platform": s.Platform, "webhook_set": s.Webhook != "", "secret_set": s.Secret != "", "login_failures": s.LoginFailures, "login_success": s.LoginSuccess, "storage": s.Storage, "scans": s.Scans, "trusted_admins": s.TrustedAdmins}
}

func (c *Control) loadNotifications(ctx context.Context) (NotificationSettings, error) {
	s := defaultNotifications()
	_, err := c.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS notifications(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`)
	if err != nil {
		return s, err
	}
	value, _ := json.Marshal(s)
	if _, err = c.db.ExecContext(ctx, "INSERT OR IGNORE INTO notifications VALUES(1,?)", string(value)); err != nil {
		return s, err
	}
	var text string
	if err = c.db.QueryRowContext(ctx, "SELECT value FROM notifications WHERE id=1").Scan(&text); err == nil {
		err = json.Unmarshal([]byte(text), &s)
	}
	if err == nil {
		err = s.validate()
	}
	return s, err
}

func (c *Control) saveNotifications(ctx context.Context, s NotificationSettings) error {
	value, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, "UPDATE notifications SET value=? WHERE id=1", string(value))
	return err
}
