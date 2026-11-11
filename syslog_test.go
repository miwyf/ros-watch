package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSystemLogClassificationAndRawPreservation(t *testing.T) {
	for _, tc := range []struct{ message, category, event, username, source, method, severity string }{
		{"<182>Nov  1 09:00:01 RB5009 RW-SYS-SYSTEM user epaper logged in from 192.0.2.15 via rest-api", "login", "success", "epaper", "192.0.2.15", "rest-api", "info"},
		{"<179>Nov  1 09:00:01 RB5009 RW-SYS-SYSTEM login failure for user admin from 2001:db8::5 via winbox", "login", "failure", "admin", "2001:db8::5", "winbox", "error"},
		{"<182>Nov  1 09:00:01 RB5009 RW-SYS-SYSTEM user epaper logged out via api", "login", "logout", "epaper", "", "api", "info"},
		{"<182>Nov  1 09:00:01 RB5009 RW-SYS-SYSTEM filter rule added by owner", "config", "change", "owner", "", "", "info"},
		{"<182>Nov  1 09:00:01 RB5009 RW-SYS-SYSTEM filter rule changed by tcp-msg(winbox):owner@192.0.2.10", "config", "change", "owner", "", "", "info"},
		{"<180>Nov  1 09:00:01 RB5009 RW-SYS-PPP pppoe-out1: disconnected", "network", "state", "", "", "", "warning"},
		{"<179>2026-11-01T09:00:01+08:00 RB5009 RW-SYS-SCRIPT script execution failed", "script", "script", "", "", "", "error"},
		{"system,info,account user owner logged in from 192.0.2.5 via winbox", "login", "success", "owner", "192.0.2.5", "winbox", "info"},
		{"<182>Nov  1 09:00:01 RB5009 RW-SYS-SYSTEM router rebooted", "system", "reboot", "", "", "", "info"},
		{"<182>Nov  1 09:00:01 RB5009 RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 192.0.2.7:12->192.0.2.1:22, len 60", "firewall", "access", "", "", "", "info"},
		{"<182>Nov  1 09:00:01 RB5009 RW-SYS-OTHER <img src=x onerror=alert(1)> 未识别消息", "other", "message", "", "", "", "info"},
	} {
		l := parseSyslog([]byte(tc.message), "192.0.2.1", 123)
		if l.Category != tc.category || l.Event != tc.event || l.Username != tc.username || l.Source != tc.source || l.Method != tc.method || l.Severity != tc.severity || l.Raw != tc.message || l.TS != 123 {
			t.Fatalf("classification mismatch: %+v", l)
		}
	}
	raw := strings.Repeat("中", 1200) + "\r\n"
	if l := parseSyslog([]byte(raw), "192.0.2.1", 123); l.Raw != raw {
		t.Fatal("raw was truncated or normalized")
	}
}

func TestGenericLogsUseTrustedCollectorAndLeaveAccessStatisticsSeparate(t *testing.T) {
	a := testApp(t)
	a.collector.accept([]byte("RW-SYS-SYSTEM user admin logged in from 192.0.2.7 via winbox"), netip.MustParseAddr("192.0.2.88"))
	if a.collector.queueLength() != 0 {
		t.Fatal("untrusted log queued")
	}
	a.collector.accept([]byte("RW-SYS-SYSTEM user admin logged in from 192.0.2.7 via winbox"), netip.MustParseAddr("127.0.0.1"))
	a.collector.accept([]byte("unrecognized but retained"), netip.MustParseAddr("127.0.0.1"))
	a.collector.accept([]byte(sampleEvent(22).Raw), netip.MustParseAddr("127.0.0.1"))
	a.collector.writeBatch(context.Background(), a.collector.pop())
	var events, logs int
	a.store.reader.QueryRow("SELECT count(*) FROM events").Scan(&events)
	a.store.reader.QueryRow("SELECT count(*) FROM logs").Scan(&logs)
	if events != 1 || logs != 3 {
		t.Fatalf("separation broken: %d events %d logs", events, logs)
	}
	s := *a.settings.Load()
	s.RetentionDays = 1
	a.settings.Store(&s)
	if err := a.store.maintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.store.reader.QueryRow("SELECT (SELECT count(*) FROM events)+(SELECT count(*) FROM logs)").Scan(&logs)
	if logs != 4 {
		t.Fatal("unexpired records were removed", logs)
	}
}

func TestLogsAPIAuthFiltersFailuresAndCSV(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/logs", nil, false); code != 401 {
		t.Fatal("anonymous logs read", code)
	}
	if code, _ := b.request(t, "GET", "/api/logs/export", nil, false); code != 401 {
		t.Fatal("anonymous logs export", code)
	}
	items := []Event{}
	start := float64(int64(unixNow()/300)*300) - 300
	for i := 0; i < 6; i++ {
		items = append(items, Event{Log: parseSyslog([]byte("RW-SYS-SYSTEM login failure for user admin from 192.0.2.9 via ssh"), "127.0.0.1", start+float64(i))})
	}
	items = append(items, Event{Log: parseSyslog([]byte("=HYPERLINK(\"bad\")"), "127.0.0.1", unixNow())})
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	b.login(t, "admin", "test-account-password")
	code, body := b.request(t, "GET", "/api/logs?category=login&source=192.0.2.9", nil, false)
	v := decoded(t, body)
	if code != 200 || len(v["rows"].([]any)) != 6 || len(v["failures"].([]any)) != 1 {
		t.Fatal("logs or failure aggregation", code, string(body))
	}
	code, body = b.request(t, "GET", "/api/logs?q=HYPERLINK", nil, false)
	if code != 200 || len(decoded(t, body)["rows"].([]any)) != 1 {
		t.Fatal("keyword search failed")
	}
	code, body = b.request(t, "GET", "/api/logs/export?q=HYPERLINK", nil, false)
	if code != 200 || !strings.Contains(string(body), "'=HYPERLINK") {
		t.Fatal("unsafe export", code, string(body))
	}
	for _, query := range []string{"hours=NaN", "hours=Infinity", "category=invalid", "source=bad", "before=-1", "event=drop", "severity=bad"} {
		if code, _ = b.request(t, "GET", "/api/logs?"+query, nil, false); code != 400 {
			t.Fatal(query, code)
		}
	}
	code, body = b.request(t, "GET", "/api/logs?username=%27%20OR%201%3D1--", nil, false)
	if code != 200 || len(decoded(t, body)["rows"].([]any)) != 0 {
		t.Fatal("filter injection")
	}
}

func TestSystemLogsRealUDPToAuthenticatedAPI(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.collector.start(ctx, "127.0.0.1", 0); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); a.collector.stop() }()
	conn, err := net.DialUDP("udp4", nil, a.collector.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, msg := range []string{"<179>Nov  1 10:00:00 test-router RW-SYS-SYSTEM login failure for user admin from 192.0.2.9 via ssh", "<182>Nov  1 10:00:01 test-router RW-SYS-SYSTEM interface changed by owner", "<180>Nov  1 10:00:02 test-router RW-SYS-INTERFACE ether1 link down"} {
		if _, err = conn.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	until := time.Now().Add(3 * time.Second)
	for a.collector.counters.written.Load() < 3 && time.Now().Before(until) {
		time.Sleep(20 * time.Millisecond)
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	code, body := b.request(t, "GET", "/api/logs", nil, false)
	if code != 200 || len(decoded(t, body)["rows"].([]any)) != 3 {
		t.Fatal("UDP logs missing", code, string(body))
	}
	var n int
	a.store.reader.QueryRow("SELECT count(*) FROM events").Scan(&n)
	if n != 0 {
		t.Fatal("system logs entered access statistics")
	}
}

func TestLogsPaginationAndOldStoreReopen(t *testing.T) {
	a := testApp(t)
	items := make([]Event, 55)
	for i := range items {
		items[i] = Event{Log: parseSyslog([]byte("ordinary message"), "127.0.0.1", unixNow())}
	}
	items = append(items, *sampleEvent(8291))
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	a.store.close()
	var err error
	a.store, err = openStore(a.config.DataDir, &a.settings)
	if err != nil {
		t.Fatal(err)
	}
	a.collector.store = a.store
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	code, body := b.request(t, "GET", "/api/logs", nil, false)
	v := decoded(t, body)
	if code != 200 || len(v["rows"].([]any)) != 50 || v["next"] == nil {
		t.Fatal("pagination")
	}
	code, body = b.request(t, "GET", "/api/logs?before=6", nil, false)
	if code != 200 || len(decoded(t, body)["rows"].([]any)) != 5 {
		t.Fatal("pagination boundary")
	}
	var count int
	a.store.reader.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if count != 1 {
		t.Fatal("old access data lost")
	}
}

func TestLegacyDatabaseMigrationPreservesAccessAndAccount(t *testing.T) {
	a := testApp(t)
	if err := a.store.append(context.Background(), []Event{*sampleEvent(8291)}); err != nil {
		t.Fatal(err)
	}
	// A 0.4.x database has only the access-events table.
	if _, err := a.store.writer.Exec("DROP TABLE logs"); err != nil {
		t.Fatal(err)
	}
	a.store.close()
	var err error
	a.store, err = openStore(a.config.DataDir, &a.settings)
	if err != nil {
		t.Fatal(err)
	}
	a.collector.store = a.store
	var oldEvents, newLogs int
	a.store.reader.QueryRow("SELECT count(*) FROM events").Scan(&oldEvents)
	a.store.reader.QueryRow("SELECT count(*) FROM logs").Scan(&newLogs)
	if oldEvents != 1 || newLogs != 0 {
		t.Fatal("legacy data changed")
	}
	if _, ok, err := a.control.authenticate(context.Background(), "admin", "test-account-password"); err != nil || !ok {
		t.Fatal("account changed during migration")
	}
}
