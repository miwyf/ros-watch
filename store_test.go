package main

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestRetentionBudgetAndAccountIsolation(t *testing.T) {
	a := testApp(t)
	s := *a.settings.Load()
	s.MaxRows = 10
	s.MaxDBMB = 8
	a.settings.Store(&s)
	items := make([]Event, 21)
	for i := range items {
		items[i] = *sampleEvent(i)
	}
	items[0].TS = unixNow() - 8*86400
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	if err := a.store.maintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := Filters{Since: unixNow() - 86400, Until: unixNow() + 1, Fields: map[string]any{}}
	events, err := a.store.events(context.Background(), f, 50, 0)
	if err != nil || len(events) != 10 || a.store.pruned.Load() != 11 {
		t.Fatal("retention/row cap failed", len(events), err)
	}
	if _, ok, err := a.control.authenticate(context.Background(), "admin", "test-account-password"); err != nil || !ok {
		t.Fatal("log cleanup affected account")
	}
}

func TestFixedScanWindowsAndCacheRefresh(t *testing.T) {
	a := testApp(t)
	start := float64(int64(unixNow()/300) * 300)
	items := make([]Event, 20)
	for i := range items {
		items[i] = *sampleEvent(i)
		items[i].TS = start - 1
		if i >= 10 {
			items[i].TS = start + 1
		}
	}
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	summary, err := a.store.summary(context.Background(), Filters{Since: start - 10, Until: start + 10, Fields: map[string]any{}})
	if err != nil || len(summary["scans"].([]map[string]any)) != 0 {
		t.Fatal("cross-window scan incorrectly merged")
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	b.request(t, "GET", "/api/summary", nil, false)
	s := b.settings(t)
	s.ScanPorts = 2
	if code, _ := b.request(t, "POST", "/api/settings", s, true); code != 200 {
		t.Fatal(code)
	}
	if len(a.cache) != 0 {
		t.Fatal("summary cache not cleared")
	}
}

func TestQueryCancellationDoesNotBreakNextRead(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := queryMaps(ctx, a.store.reader, "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n"); err == nil {
		t.Fatal("cancelled query executed")
	}
	rows, err := queryMaps(context.Background(), a.store.reader, "SELECT COUNT(*) n FROM events")
	if err != nil || number(rows[0]["n"]) != 0 {
		t.Fatal("connection not recovered", err)
	}
}

func TestSettingsRejectInvalidAndIgnoreBusinessEnvironment(t *testing.T) {
	t.Setenv("APP_PASSWORD", "ignored-environment-password")
	t.Setenv("ALLOWED_SENDERS", "0.0.0.0/0")
	t.Setenv("BIND_IP", "192.0.2.88")
	s := defaultSettings()
	data, _ := json.Marshal(s)
	if _, err := decodeSettings(data); err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	json.Unmarshal(data, &values)
	for _, field := range []string{"scan_ports", "queue_size", "max_db_mb"} {
		copy := map[string]any{}
		for k, v := range values {
			copy[k] = v
		}
		copy[field] = true
		raw, _ := json.Marshal(copy)
		if _, err := decodeSettings(raw); err == nil {
			t.Fatal("boolean accepted for numeric setting")
		}
	}
	if _, err := decodeSettings([]byte(`{"allowed_senders":""}`)); err == nil {
		t.Fatal("incomplete settings accepted")
	}
	a := testApp(t)
	if a.settings.Load().AllowedSenders == "0.0.0.0/0" {
		t.Fatal("environment overrode persisted settings")
	}
}

func TestCollectorOversizeRateAndDiskProtection(t *testing.T) {
	a := testApp(t)
	s := *a.settings.Load()
	s.IngestPerSecond = 1
	s.QueueSize = 8
	a.settings.Store(&s)
	a.collector.tokens = 1
	a.collector.accept([]byte(strings.Repeat("x", 4097)), mustLoopback())
	a.collector.accept([]byte(sampleEvent(1).Raw), mustLoopback())
	a.collector.accept([]byte(sampleEvent(2).Raw), mustLoopback())
	if a.collector.counters.oversized.Load() != 1 || a.collector.counters.rateDropped.Load() != 1 {
		t.Fatal("receiver protection missing")
	}
	s.MinFreeMB = 1048576
	a.settings.Store(&s)
	a.collector.writeBatch(context.Background(), a.collector.pop())
	if a.collector.counters.storageDropped.Load() != 1 || a.collector.getError() != "disk_low" {
		t.Fatal("low disk not visible")
	}
}

func mustLoopback() netip.Addr { return netip.MustParseAddr("127.0.0.1") }
