package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type MQTTSettings struct {
	Enabled         bool   `json:"enabled"`
	Broker          string `json:"broker"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	DeviceName      string `json:"device_name"`
	DiscoveryPrefix string `json:"discovery_prefix"`
	InstanceID      string `json:"instance_id"`
}

func (s *MQTTSettings) validate() error {
	s.Broker = strings.TrimSpace(s.Broker)
	s.Username = strings.TrimSpace(s.Username)
	s.DeviceName = strings.TrimSpace(s.DeviceName)
	s.DiscoveryPrefix = strings.Trim(s.DiscoveryPrefix, " /")
	if s.DeviceName == "" || len(s.DeviceName) > 96 || strings.ContainsAny(s.DeviceName, "\x00\r\n") {
		return errors.New("设备名称须为 1–96 字节的单行文本")
	}
	if len(s.Username) > 256 || len(s.Password) > 1024 || strings.ContainsAny(s.Username+s.Password, "\x00\r\n") {
		return errors.New("MQTT 用户名或密码格式无效")
	}
	if len(s.DiscoveryPrefix) == 0 || len(s.DiscoveryPrefix) > 128 || strings.ContainsAny(s.DiscoveryPrefix, "+#\x00\r\n ") {
		return errors.New("自动发现前缀不能为空或包含通配符、空格")
	}
	if len(s.Broker) > 512 || strings.ContainsAny(s.Broker, "\x00\r\n") {
		return errors.New("Broker 地址过长或包含无效字符")
	}
	if s.Broker == "" && !s.Enabled {
		return nil
	}
	u, err := url.Parse(s.Broker)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("Broker 地址格式为 tcp://主机:1883 或 ssl://主机:8883，不包含账号密码")
	}
	switch u.Scheme {
	case "mqtt":
		u.Scheme = "tcp"
	case "mqtts":
		u.Scheme = "ssl"
	case "tcp", "ssl":
	default:
		return errors.New("Broker 仅支持 tcp://、mqtt://、ssl:// 或 mqtts://")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("Broker 地址须包含有效端口")
	}
	u.Path = ""
	s.Broker = u.String()
	return nil
}

func (s MQTTSettings) public() map[string]any {
	return map[string]any{"enabled": s.Enabled, "broker": s.Broker, "username": s.Username, "password_set": s.Password != "", "device_name": s.DeviceName, "discovery_prefix": s.DiscoveryPrefix, "instance_id": s.InstanceID, "interval_seconds": 60, "hours": mqttWindowHours}
}

func (c *Control) loadMQTT(ctx context.Context) (MQTTSettings, error) {
	s := MQTTSettings{DeviceName: "ROS Watch", DiscoveryPrefix: "homeassistant"}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return s, err
	}
	s.InstanceID = hex.EncodeToString(id)
	if _, err := c.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mqtt(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`); err != nil {
		return s, err
	}
	data, _ := json.Marshal(s)
	if _, err := c.db.ExecContext(ctx, "INSERT OR IGNORE INTO mqtt VALUES(1,?)", string(data)); err != nil {
		return s, err
	}
	var value string
	if err := c.db.QueryRowContext(ctx, "SELECT value FROM mqtt WHERE id=1").Scan(&value); err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(value), &s); err != nil {
		return s, err
	}
	if raw, err := hex.DecodeString(s.InstanceID); err != nil || len(raw) != 8 {
		return s, errors.New("invalid MQTT instance identity")
	}
	return s, s.validate()
}

func (c *Control) saveMQTT(ctx context.Context, s MQTTSettings) error {
	value, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, "UPDATE mqtt SET value=? WHERE id=1", string(value))
	return err
}
