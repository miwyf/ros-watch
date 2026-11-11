package main

import (
	"strings"
	"testing"
)

func sampleEvent(port int) *Event {
	return parseMessage([]byte("RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 198.51.100.8:45000->192.0.2.1:"+itoa(port)+", len 60"), "127.0.0.1", map[string]string{"pppoe-out1": "WAN1"}, unixNow())
}

func TestParseFirewall(t *testing.T) {
	cases := []struct {
		name, raw, source, proto, scope string
		port                            int
		valid                           bool
	}{
		{"bsd", "<134>Nov 1 12:00:00 router RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 198.51.100.8:45000->192.0.2.1:8291, len 60", "198.51.100.8", "TCP", "router", 8291, true},
		{"forward UDP", "RW-FWD forward: in:pppoe-out1 out:bridge, proto UDP, 198.51.100.8:99->192.0.2.1:1883, len 60", "198.51.100.8", "UDP", "forward", 1883, true},
		{"IPv6", "RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), [2001:db8::1]:55->[2001:db8::2]:443, len 80", "2001:db8::1", "TCP", "router", 443, true},
		{"icmp", "input: in:pppoe-out1 out:(none), proto ICMP (type 8, code 0), 198.51.100.8->192.0.2.1, len 60", "198.51.100.8", "ICMP", "router", -1, true},
		{"bad message", "login failure", "", "", "", 0, false},
		{"bad port", "RW-INPUT input: in:pppoe-out1 out:(none), proto TCP, 198.51.100.8:5->192.0.2.1:99999", "", "", "", 0, false},
		{"LAN excluded", "input: in:bridge out:(none), proto TCP, 198.51.100.8:5->192.0.2.1:80", "", "", "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := parseMessage([]byte(c.raw), "127.0.0.1", map[string]string{"pppoe-out1": "WAN1"}, 10)
			if (e != nil) != c.valid {
				t.Fatal("wrong parse outcome")
			}
			if !c.valid {
				return
			}
			if e.Src != c.source || e.Proto != c.proto || e.Scope != c.scope || e.Outcome != "attempt" {
				t.Fatalf("wrong event classification: %+v", e)
			}
			if c.port < 0 {
				if e.Dport != nil {
					t.Fatal("ICMP has port")
				}
			} else if e.Dport == nil || *e.Dport != c.port {
				t.Fatal("wrong port")
			}
		})
	}
}

func TestPrefixRawAndNATSuffix(t *testing.T) {
	raw := strings.Replace(sampleEvent(8291).Raw, "RW-INPUT", "RW-DROP", 1) + " NAT 192.0.2.1:11883->192.0.2.2:1883\x00\n" + strings.Repeat("中", 1400)
	e := parseMessage([]byte(raw), "127.0.0.1", map[string]string{}, 10)
	if e == nil || e.Outcome != "blocked" || e.WAN != "pppoe-out1" || *e.Dport != 8291 || len([]rune(e.Raw)) != 1200 || strings.ContainsAny(e.Raw, "\x00\n") {
		t.Fatal("prefix/NAT/sanitization failed")
	}
}
