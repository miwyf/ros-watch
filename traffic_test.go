package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTrafficDirectionKeepsRepliesAndUnconfirmedOutOfScans(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 203.0.113.10:50000->192.0.2.1:22", trafficInbound},
		{"forward: in:pppoe-out1 out:bridge, connection-state:established,snat proto TCP (ACK,PSH), 203.0.113.10:443->192.0.2.20:49152", trafficResponse},
		{"forward: in:pppoe-out1 out:bridge, proto TCP (SYN,ACK), 203.0.113.10:443->192.0.2.20:49152", trafficResponse},
		{"forward: in:pppoe-out1 out:bridge, connection-state:established,snat proto UDP, 203.0.113.10:53->192.0.2.20:49152", trafficResponse},
		{"forward: in:pppoe-out1 out:bridge, proto UDP, 203.0.113.10:53->192.0.2.20:49152", trafficUnknown},
		{"RW-FWD forward: in:pppoe-out1 out:bridge, connection-state:invalid proto TCP (SYN), 203.0.113.10:50000->192.0.2.20:1883", trafficUnknown},
		{"RW-FWD forward: in:pppoe-out1 out:bridge, proto UDP, 203.0.113.10:50000->192.0.2.20:1883", trafficInbound},
	}
	a := testApp(t)
	var events []Event
	for _, c := range cases {
		e := parseMessage([]byte(c.raw), "127.0.0.1", a.settings.Load().WANMap, unixNow())
		if e == nil || e.Direction != c.want {
			t.Fatalf("%s: %+v, want %s", c.raw, e, c.want)
		}
		if c.want != trafficInbound {
			for i := 0; i < 20; i++ {
				copy := *e
				p := 49000 + i
				copy.Dport = &p
				events = append(events, copy)
			}
		}
	}
	if err := a.store.append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	f := Filters{Since: unixNow() - 3600, Until: unixNow() + 1, Fields: map[string]any{}}
	summary, err := a.store.summary(context.Background(), f)
	if err != nil || len(summary["scans"].([]map[string]any)) != 0 {
		t.Fatal("reply/unconfirmed scan false positive", err)
	}
	settings := fixtureNotification("feishu")
	settings.Enabled = true
	settings.Scans = true
	d := notificationDetector{}
	if messages, _, _ := d.process(&settings, a.settings.Load(), events, time.Now()); len(messages) != 0 {
		t.Fatal("reply/unconfirmed generated alert", messages)
	}
	rows, err := a.store.events(context.Background(), f, 200, 0)
	if err != nil || len(rows) != len(events) {
		t.Fatal("history was discarded", len(rows), err)
	}
	f.Fields["direction"] = trafficInbound
	summary, err = a.store.summary(context.Background(), f)
	if err != nil || number(summary["totals"].(map[string]any)["observations"]) != 0 {
		t.Fatal("responses entered inbound totals", err)
	}
	f.Fields["direction"] = trafficResponse
	rows, err = a.store.events(context.Background(), f, 200, 0)
	if err != nil || len(rows) != 60 {
		t.Fatal("response history filter", len(rows), err)
	}
}

func TestTrafficMigrationPreservesHistoricalRowsAndIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE events(id INTEGER PRIMARY KEY,raw TEXT,flags TEXT,proto TEXT,interface TEXT,scope TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	for i := 0; i < 1100; i++ {
		flags, raw := "SYN", "RW-INPUT input:"
		if i%2 == 0 {
			flags = "ACK"
			raw = "forward: connection-state:established"
		}
		if _, err = tx.Exec("INSERT INTO events(raw,flags,proto,interface,scope)VALUES(?,?,'TCP','pppoe-out1','router')", raw, flags); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = migrateTraffic(db); err != nil {
			t.Fatal(err)
		}
	}
	var total, inbound, response int
	if err = db.QueryRow("SELECT COUNT(*),SUM(direction='inbound'),SUM(direction='response') FROM events").Scan(&total, &inbound, &response); err != nil || total != 1100 || inbound != 550 || response != 550 {
		t.Fatal(total, inbound, response, err)
	}
}

func TestLatestScansAreSelectedBeforeLimitAndBusinessDoesNotExemptSource(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	now := unixNow()
	var events []Event
	for source := 1; source <= 25; source++ {
		ports := 45 - source
		if source == 25 {
			ports = 15
		}
		for port := 0; port < ports; port++ {
			e := *sampleEvent(1000 + port)
			e.Src = fmt.Sprintf("198.51.100.%d", source)
			e.TS = now - 3600 + float64(source)
			if source == 25 {
				e.TS = now - 2
			}
			events = append(events, e)
		}
	}
	mqtt := *sampleEvent(1883)
	mqtt.Src = "203.0.113.80"
	mqtt.Scope = "forward"
	for i := 0; i < 1000; i++ {
		events = append(events, mqtt)
	}
	if err := a.store.append(ctx, events); err != nil {
		t.Fatal(err)
	}
	rule := BusinessRule{Name: "MQTT 上报", Sender: mqtt.Sender, Src: mqtt.Src, Dst: mqtt.Dst, Proto: mqtt.Proto, Dport: 1883, Scope: mqtt.Scope, Interface: mqtt.Interface}
	if err := rule.validate(); err != nil {
		t.Fatal(err)
	}
	if err := a.store.saveBusiness(ctx, rule); err != nil {
		t.Fatal(err)
	}
	f := Filters{Since: now - 7200, Until: now + 1, Fields: map[string]any{"direction": "inbound"}, ScanOrder: "latest"}
	summary, err := a.store.summary(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	latest := summary["scans"].([]map[string]any)
	if len(latest) != 20 || latest[0]["src"] != "198.51.100.25" || number(summary["scan_count"]) != 25 {
		t.Fatal("latest scanner hidden by old high-volume sources", latest)
	}
	if summary["sources"].([]map[string]any)[0]["src"] != mqtt.Src || number(summary["totals"].(map[string]any)["business"]) != 1000 {
		t.Fatal("business history/count lost")
	}
	f.ScanOrder = "ports"
	summary, err = a.store.summary(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if summary["scans"].([]map[string]any)[0]["src"] != "198.51.100.1" || summary["latest_scans"].([]map[string]any)[0]["src"] != "198.51.100.25" {
		t.Fatal("sort mode changed latest feed")
	}
	for port := 2000; port < 2016; port++ {
		e := mqtt
		e.Dport = &port
		events = append([]Event{}, e)
		if err = a.store.append(ctx, events); err != nil {
			t.Fatal(err)
		}
	}
	f.ScanOrder = "latest"
	summary, err = a.store.summary(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scan := range summary["scans"].([]map[string]any) {
		found = found || scan["src"] == mqtt.Src
	}
	if !found {
		t.Fatal("business marker exempted whole source")
	}
	rows, err := a.store.events(ctx, Filters{Since: now - 7200, Until: now + 1, Fields: map[string]any{"src": mqtt.Src}}, 2000, 0)
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, e := range rows {
		if e.Business != "" {
			marked++
			if *e.Dport != 1883 {
				t.Fatal("unrelated service marked normal")
			}
		}
	}
	if marked != 1000 {
		t.Fatal("precise business matching failed", marked)
	}
	for _, change := range []string{"sender", "target", "protocol", "interface"} {
		e := mqtt
		switch change {
		case "sender":
			e.Sender = "127.0.0.2"
		case "target":
			e.Dst = "192.0.2.99"
		case "protocol":
			e.Proto = "UDP"
		case "interface":
			e.Interface = "pppoe-out2"
		}
		if err = a.store.append(ctx, []Event{e}); err != nil {
			t.Fatal(err)
		}
		rows, err = a.store.events(ctx, Filters{Since: now - 7200, Until: now + 1, Fields: map[string]any{"src": mqtt.Src}}, 1, 0)
		if err != nil || len(rows) != 1 || rows[0].Business != "" {
			t.Fatal("business marker matched a different "+change, err)
		}
	}
}

func TestHomeAttentionUsesTimeScopeForSystemEvents(t *testing.T) {
	a := testApp(t)
	now := unixNow()
	var events []Event
	for i := 0; i < 6; i++ {
		events = append(events, notificationLogin("failure", "203.0.113.76", float64(int64(now/300)*300)+float64(i)))
	}
	events = append(events, Event{Log: &LogEvent{TS: now - 5, Sender: "127.0.0.1", Device: "fixture", Category: "config", Event: "change", Severity: "info", Message: "fixture config update"}})
	if err := a.store.append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	f := Filters{Since: now - 600, Until: now + 10, Fields: map[string]any{"wan": "WAN2", "proto": "UDP", "direction": "response"}}
	summary, err := a.store.summary(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if number(summary["scan_count"]) != 0 || len(summary["attention"].([]map[string]any)) != 1 || len(summary["system_events"].([]map[string]any)) != 1 {
		t.Fatal("access filters incorrectly hid system events")
	}
}

func TestBusinessAPIRequiresLoginCSRFAndPersistsRemoval(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/business", nil, false); code != 401 {
		t.Fatal(code)
	}
	b.login(t, "admin", "test-account-password")
	e := sampleEvent(1883)
	rule := BusinessRule{Name: "MQTT", Sender: e.Sender, Src: e.Src, Dst: e.Dst, Proto: e.Proto, Dport: 1883, Scope: e.Scope, Interface: e.Interface}
	if code, _ := b.request(t, "POST", "/api/business", rule, false); code != 400 {
		t.Fatal("missing CSRF accepted", code)
	}
	if code, _ := b.request(t, "POST", "/api/business", rule, true); code != 200 {
		t.Fatal(code)
	}
	if code, _ := b.request(t, "POST", "/api/business", rule, true); code != 200 {
		t.Fatal(code)
	}
	rules, err := a.store.businessRules(context.Background())
	if err != nil || len(rules) != 1 {
		t.Fatal(rules, err)
	}
	reopened, err := openStore(a.config.DataDir, &a.settings)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.businessRules(context.Background())
	reopened.close()
	if err != nil || len(persisted) != 1 || persisted[0].Name != "MQTT" {
		t.Fatal("business marker did not survive reopen", persisted, err)
	}
	bad := rule
	bad.Src = "0.0.0.0/0"
	if code, _ := b.request(t, "POST", "/api/business", bad, true); code != 400 {
		t.Fatal("broad normal marker accepted")
	}
	if code, _ := b.request(t, "POST", "/api/business", BusinessRule{ID: rules[0].ID, Remove: true}, true); code != 200 {
		t.Fatal(code)
	}
	rules, _ = a.store.businessRules(context.Background())
	if len(rules) != 0 {
		t.Fatal("marker not removed")
	}
	if code, data := b.request(t, "GET", "/api/summary?direction=bogus", nil, false); code != 400 || !strings.Contains(string(data), "筛选") {
		t.Fatal(code, string(data))
	}
}
