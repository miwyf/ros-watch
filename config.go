package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const Version = "0.7.2"
const MinPasswordLength = 8

type Config struct {
	DataDir      string
	HTTPHost     string
	HTTPPort     int
	SyslogHost   string
	SyslogPort   int
	CookieSecure bool
	Demo         bool
}

func loadConfig() (Config, error) {
	c := Config{DataDir: env("DATA_DIR", "data"), HTTPHost: env("HTTP_HOST", "0.0.0.0"), HTTPPort: 8080, SyslogHost: env("SYSLOG_HOST", "0.0.0.0"), SyslogPort: 5514, CookieSecure: os.Getenv("COOKIE_SECURE") == "true", Demo: os.Getenv("DEMO_MODE") == "true"}
	var err error
	if c.HTTPPort, err = strconv.Atoi(env("HTTP_PORT", "8080")); err != nil || c.HTTPPort < 1 || c.HTTPPort > 65535 {
		return c, errors.New("invalid HTTP port")
	}
	if c.SyslogPort, err = strconv.Atoi(env("SYSLOG_PORT", "5514")); err != nil || c.SyslogPort < 0 || c.SyslogPort > 65535 {
		return c, errors.New("invalid syslog port")
	}
	if c.Demo && c.HTTPHost != "127.0.0.1" && c.HTTPHost != "::1" && c.HTTPHost != "localhost" {
		return c, errors.New("demo mode requires loopback HTTP listener")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	return c, err
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type Settings struct {
	AllowedSenders  string            `json:"allowed_senders"`
	WANMap          map[string]string `json:"wan_map"`
	RetentionDays   int               `json:"retention_days"`
	MinFreeMB       int               `json:"min_free_mb"`
	DiskLowAction   string            `json:"disk_low_action"`
	QueueSize       int               `json:"queue_size"`
	IngestPerSecond int               `json:"ingest_per_second"`
	ScanPorts       int               `json:"scan_ports"`
	ScanWindow      int               `json:"scan_window"`
	Sampling        string            `json:"sampling"`
	networks        []netip.Prefix
}

func defaultSettings() Settings {
	return Settings{WANMap: map[string]string{"pppoe-out1": "WAN1", "pppoe-out2": "WAN2"}, RetentionDays: 7, MinFreeMB: 1024, DiskLowAction: "prune", QueueSize: 4096, IngestPerSecond: 500, ScanPorts: 15, ScanWindow: 300, Sampling: "unknown"}
}

var sourceSeparator = regexp.MustCompile(`[,\s]+`)

func (s *Settings) validate() error {
	if len(s.AllowedSenders) > 4096 {
		return errors.New("来源白名单过长")
	}
	unique := map[string]bool{}
	networks := []netip.Prefix{}
	values := []string{}
	for _, value := range sourceSeparator.Split(strings.TrimSpace(s.AllowedSenders), -1) {
		if value == "" {
			continue
		}
		var prefix netip.Prefix
		var err error
		if strings.Contains(value, "/") {
			prefix, err = netip.ParsePrefix(value)
		} else {
			var address netip.Addr
			address, err = netip.ParseAddr(value)
			if err == nil {
				prefix = netip.PrefixFrom(address, address.BitLen())
			}
		}
		if err != nil {
			return errors.New("白名单须填写有效的 IP 或网段")
		}
		prefix = prefix.Masked()
		if prefix.Bits() == 0 {
			return errors.New("请指定可信来源，不能允许所有地址")
		}
		if !unique[prefix.String()] {
			unique[prefix.String()] = true
			values = append(values, prefix.String())
			networks = append(networks, prefix)
		}
	}
	if s.WANMap == nil || len(s.WANMap) > 32 {
		return errors.New("WAN 映射无效或超过 32 项")
	}
	for k, v := range s.WANMap {
		if !validLabel(k) || !validLabel(v) {
			return errors.New("WAN 接口和显示名称须为 1–64 位有效文字")
		}
	}
	limits := []struct {
		name             string
		value, low, high int
	}{
		{"retention_days", s.RetentionDays, 1, 31}, {"min_free_mb", s.MinFreeMB, 0, 1048576},
		{"queue_size", s.QueueSize, 1, 8192}, {"ingest_per_second", s.IngestPerSecond, 1, 5000}, {"scan_ports", s.ScanPorts, 2, 1000}, {"scan_window", s.ScanWindow, 60, 3600},
	}
	for _, limit := range limits {
		if limit.value < limit.low || limit.value > limit.high {
			return fmt.Errorf("%s 的范围应为 %d–%d", limit.name, limit.low, limit.high)
		}
	}
	if s.Sampling != "unknown" && s.Sampling != "limited" && s.Sampling != "full" {
		return errors.New("采样模式无效")
	}
	if s.DiskLowAction != "prune" && s.DiskLowAction != "pause" {
		return errors.New("磁盘不足处理方式无效")
	}
	s.AllowedSenders = strings.Join(values, ",")
	s.networks = networks
	return nil
}

func validLabel(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 64 {
		return false
	}
	for _, r := range value {
		if r < ' ' || r == 127 {
			return false
		}
	}
	return true
}

func decodeSettings(data []byte) (Settings, error) {
	var raw map[string]json.RawMessage
	var s Settings
	if err := json.Unmarshal(data, &raw); err != nil {
		return s, errors.New("请提交完整的运行设置")
	}
	for _, key := range []string{"max_rows", "max_db_mb"} {
		if _, ok := raw[key]; ok {
			return s, errors.New("日志设置已简化，请刷新页面后重新保存")
		}
	}
	if len(raw) != 10 {
		return s, errors.New("日志设置已更新，请刷新页面后重新保存完整设置")
	}
	for _, value := range raw {
		if string(value) == "null" {
			return s, errors.New("运行设置不能为 null")
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return s, errors.New("运行设置的字段或类型无效")
	}
	return s, s.validate()
}
