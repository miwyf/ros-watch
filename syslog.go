package main

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type LogEvent struct {
	ID         int64   `json:"id"`
	TS         float64 `json:"ts"`
	Sender     string  `json:"sender"`
	Device     string  `json:"device"`
	DeviceTime string  `json:"device_time"`
	Category   string  `json:"category"`
	Severity   string  `json:"severity"`
	Username   string  `json:"username"`
	Source     string  `json:"source"`
	Method     string  `json:"method"`
	Event      string  `json:"event"`
	Message    string  `json:"message"`
	Raw        string  `json:"raw"`
	Topic      string  `json:"topic"`
}

var bsdHeader = regexp.MustCompile(`^<([0-9]{1,3})>([A-Z][a-z]{2}\s+[0-9]{1,2}\s+[0-9:]{8})\s+(\S+)\s+(.*)$`)
var isoHeader = regexp.MustCompile(`^<([0-9]{1,3})>(?:1\s+)?([0-9]{4}-[0-9]{2}-[0-9]{2}T\S+)\s+(\S+)\s+(.*)$`)
var logTag = regexp.MustCompile(`\bRW-SYS-(SYSTEM|INTERFACE|PPP|PPPOE|SCRIPT|OTHER)\s+`)
var topicHeader = regexp.MustCompile(`^([a-z]+(?:,[a-z-]+)+)\s+`)
var loginEvent = regexp.MustCompile(`(?i)\buser\s+(.+?)\s+logged\s+(in|out)(?:\s+from\s+(\S+))?\s+via\s+(\S+)`)
var failedLogin = regexp.MustCompile(`(?i)\blogin failure for user\s+(.+?)\s+from\s+(\S+)(?:\s+via\s+(\S+))?`)
var changedEvent = regexp.MustCompile(`(?i)\b(?:added|changed|removed|enabled|disabled|moved|reset) by\s+(\S+)`)
var changedActor = regexp.MustCompile(`:([^:@\s()]+)@`)
var cliChange = regexp.MustCompile(`(?i)\b(?:configuration|config)\b.*\b(?:changed|modified|reset|imported)\b`)

func cleanSource(value string) string {
	value = strings.Trim(value, "[](),")
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap().String()
	}
	if addr, err := netip.ParseAddrPort(value); err == nil {
		return addr.Addr().Unmap().String()
	}
	return ""
}

func parseSyslog(data []byte, sender string, ts float64) *LogEvent {
	raw := strings.ToValidUTF8(string(data), "\ufffd")
	l := &LogEvent{TS: ts, Sender: sender, Category: "other", Severity: "unknown", Event: "message", Raw: raw}
	message := strings.TrimSpace(raw)
	header := bsdHeader.FindStringSubmatch(message)
	if header == nil {
		header = isoHeader.FindStringSubmatch(message)
	}
	if header != nil {
		pri, _ := strconv.Atoi(header[1])
		if pri <= 191 {
			l.Severity = []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}[pri%8]
		}
		l.DeviceTime, l.Device, message = header[2], header[3], header[4]
	}
	if tag := logTag.FindStringSubmatchIndex(message); tag != nil {
		l.Topic = strings.ToLower(message[tag[2]:tag[3]])
		message = message[tag[1]:]
	}
	if topic := topicHeader.FindStringSubmatch(message); topic != nil {
		l.Topic = topic[1]
		for _, level := range []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"} {
			if strings.Contains(","+l.Topic+",", ","+level+",") {
				l.Severity = level
				break
			}
		}
		message = message[len(topic[0]):]
	}
	l.Message = message
	if m := failedLogin.FindStringSubmatch(message); m != nil {
		l.Category, l.Event, l.Username, l.Source, l.Method = "login", "failure", strings.Trim(m[1], "\"'"), cleanSource(m[2]), m[3]
	} else if m := loginEvent.FindStringSubmatch(message); m != nil {
		l.Category, l.Event, l.Username, l.Source, l.Method = "login", "success", strings.Trim(m[1], "\"'"), cleanSource(m[3]), m[4]
		if strings.EqualFold(m[2], "out") {
			l.Event = "logout"
		}
	} else if m := changedEvent.FindStringSubmatch(message); m != nil && strings.Contains(","+l.Topic+",", ",system,") {
		l.Category, l.Event, l.Username = "config", "change", m[1]
		if actor := changedActor.FindStringSubmatch(m[1]); actor != nil {
			l.Username = actor[1]
		}
	} else if cliChange.MatchString(message) && strings.Contains(","+l.Topic+",", ",system,") {
		l.Category, l.Event = "config", "change"
	} else if chainPattern.MatchString(message) && protoPattern.MatchString(message) && flowPattern.MatchString(message) {
		l.Category, l.Event = "firewall", "access"
	} else {
		for _, topic := range strings.Split(l.Topic, ",") {
			switch topic {
			case "interface", "ppp", "pppoe":
				l.Category, l.Event = "network", "state"
			case "script":
				l.Category, l.Event = "script", "script"
			case "system":
				l.Category = "system"
			}
		}
		lower := strings.ToLower(message)
		if l.Category == "system" && (strings.Contains(lower, "router rebooted") || strings.Contains(lower, "router was rebooted")) {
			l.Event = "reboot"
		}
	}
	return l
}
