package main

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type Event struct {
	Log       *LogEvent `json:"-"`
	ID        int64     `json:"id"`
	TS        float64   `json:"ts"`
	Sender    string    `json:"sender"`
	Src       string    `json:"src"`
	Sport     *int      `json:"sport"`
	Dst       string    `json:"dst"`
	Dport     *int      `json:"dport"`
	Proto     string    `json:"proto"`
	Flags     string    `json:"flags"`
	WAN       string    `json:"wan"`
	Interface string    `json:"interface"`
	Scope     string    `json:"scope"`
	Outcome   string    `json:"outcome"`
	Direction string    `json:"direction"`
	Business  string    `json:"business,omitempty"`
	Raw       string    `json:"raw"`
}

var chainPattern = regexp.MustCompile(`(?i)\b(input|forward):\s+in:([^\s]+)\s+out:([^,\s]+)`)
var protoPattern = regexp.MustCompile(`(?i)\bproto\s+([\w-]+)(?:\s+\(([^)]*)\))?`)
var flowPattern = regexp.MustCompile(`(\[[^\]]+\](?::\d+)?|[^\s,]+)->(\[[^\]]+\](?::\d+)?|[^\s,]+)`)
var prefixPattern = regexp.MustCompile(`\b(RW-DROP|RW-INPUT|RW-FWD)\b`)

func endpoint(value string) (string, *int, bool) {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String(), nil, true
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		if address, err := netip.ParseAddr(strings.Trim(value, "[]")); err == nil {
			return address.String(), nil, true
		}
	}
	// ParseAddrPort rejects non-numeric and overflowing ports.
	if address, err := netip.ParseAddrPort(value); err == nil {
		port := int(address.Port())
		return address.Addr().String(), &port, true
	}
	return "", nil, false
}

func parseMessage(data []byte, sender string, mapping map[string]string, ts float64) *Event {
	text := strings.ToValidUTF8(string(data), "\ufffd")
	text = strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, text)
	text = truncateRunes(text, 4096)
	chain := chainPattern.FindStringSubmatchIndex(text)
	proto := protoPattern.FindStringSubmatch(text)
	flow := flowPattern.FindStringSubmatch(text)
	if chain == nil || proto == nil || flow == nil {
		return nil
	}
	src, sport, ok := endpoint(flow[1])
	if !ok {
		return nil
	}
	dst, dport, ok := endpoint(flow[2])
	if !ok {
		return nil
	}
	iface := text[chain[4]:chain[5]]
	prefix := prefixPattern.FindString(text[:chain[0]])
	wan, mapped := mapping[iface]
	if !mapped && prefix == "" {
		return nil
	}
	if !mapped {
		wan = iface
	}
	scope := "router"
	if strings.EqualFold(text[chain[2]:chain[3]], "forward") {
		scope = "forward"
	}
	outcome := "attempt"
	if prefix == "RW-DROP" {
		outcome = "blocked"
	}
	event := &Event{TS: ts, Sender: sender, Src: src, Sport: sport, Dst: dst, Dport: dport, Proto: strings.ToUpper(proto[1]), Flags: proto[2], WAN: wan, Interface: iface, Scope: scope, Outcome: outcome, Raw: truncateRunes(text, 1200)}
	event.Direction = classifyTraffic(*event)
	return event
}

func truncateRunes(text string, max int) string {
	count := 0
	for i := range text {
		if count == max {
			return text[:i]
		}
		count++
	}
	return text
}
func portText(port *int) string {
	if port == nil {
		return ""
	}
	return strconv.Itoa(*port)
}
