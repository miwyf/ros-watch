package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLegacyStorageLimitsMigrateWithoutChangingOtherSettings(t *testing.T) {
	a := testApp(t)
	before, err := a.control.account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(a.settings.Load())
	var legacy map[string]any
	json.Unmarshal(value, &legacy)
	delete(legacy, "disk_low_action")
	legacy["max_rows"] = 1
	legacy["max_db_mb"] = 8
	legacy["min_free_mb"] = 256
	legacy["retention_days"] = 14
	value, _ = json.Marshal(legacy)
	if _, err = a.control.db.Exec("UPDATE settings SET value=? WHERE id=1", string(value)); err != nil {
		t.Fatal(err)
	}
	s, err := a.control.loadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.MinFreeMB != 256 || s.DiskLowAction != "prune" || s.RetentionDays != 14 || s.AllowedSenders != a.settings.Load().AllowedSenders || s.QueueSize != a.settings.Load().QueueSize {
		t.Fatal("non-storage settings changed")
	}
	var stored string
	a.control.db.QueryRow("SELECT value FROM settings WHERE id=1").Scan(&stored)
	for _, key := range []string{"max_rows", "max_db_mb"} {
		if strings.Contains(stored, key) {
			t.Fatal("obsolete limit persisted")
		}
	}
	after, _ := a.control.account(context.Background())
	if before != after {
		t.Fatal("account changed")
	}
	a.settings.Store(&s)
	if err = a.store.append(context.Background(), []Event{*sampleEvent(1), *sampleEvent(2)}); err != nil {
		t.Fatal(err)
	}
	if err = a.store.maintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	a.store.reader.QueryRow("SELECT count(*) FROM events").Scan(&n)
	if n != 2 {
		t.Fatal("old row cap still applies")
	}
	if _, err = decodeSettings(value); err == nil {
		t.Fatal("old browser payload accepted without refresh")
	}
}

func TestSpaceStatusPauseRecoveryAndHistoryProtection(t *testing.T) {
	a := testApp(t)
	old := Event{Log: parseSyslog([]byte("ordinary old log"), "127.0.0.1", unixNow()-8*86400)}
	current := Event{Log: parseSyslog([]byte("ordinary current log"), "127.0.0.1", unixNow())}
	if err := a.store.append(context.Background(), []Event{old, current, *sampleEvent(22)}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.maintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := a.store.storageStatus()
	if status["records"] != int64(2) || status["space_state"] != "normal" || status["oldest"] == nil || status["database_bytes"].(int64) <= 0 {
		t.Fatal("storage status", status)
	}
	s := *a.settings.Load()
	s.DiskLowAction = "pause"
	a.settings.Store(&s)
	a.store.availableDisk = func(string) (uint64, error) { return minimumFreeDiskBytes - 1, nil }
	if a.store.storageStatus()["space_state"] != "low_space" {
		t.Fatal("low space state")
	}
	a.collector.writeBatch(context.Background(), []Event{current})
	if a.collector.getError() != "disk_low" || a.collector.counters.storageDropped.Load() != 1 {
		t.Fatal("low disk did not pause ingestion")
	}
	if err := a.store.maintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.store.storageStatus()["records"] != int64(2) {
		t.Fatal("low disk deleted unexpired records")
	}
	a.store.availableDisk = func(string) (uint64, error) { return 10 * minimumFreeDiskBytes, nil }
	a.collector.writeBatch(context.Background(), []Event{current})
	if a.collector.getError() != "" || a.collector.counters.written.Load() != 1 || a.store.storageStatus()["records"] != int64(3) {
		t.Fatal("did not recover")
	}
	a.store.availableDisk = func(string) (uint64, error) { return 0, errors.New("statfs failed") }
	if a.store.diskOK() || a.store.storageStatus()["space_state"] != "unknown" {
		t.Fatal("unreadable disk treated as safe")
	}
}

func TestSettingsExposeDaysAndReadOnlyStorage(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	code, data := b.request(t, "GET", "/api/settings", nil, false)
	v := decoded(t, data)
	if code != 200 || v["storage"] == nil {
		t.Fatal("storage status absent")
	}
	s := v["settings"].(map[string]any)
	for _, key := range []string{"max_rows", "max_db_mb"} {
		if _, ok := s[key]; ok {
			t.Fatal("obsolete setting exposed", key)
		}
	}
	s["retention_days"] = 10
	if code, _ = b.request(t, "POST", "/api/settings", s, true); code != 200 {
		t.Fatal("simplified settings rejected", code)
	}
	if a.settings.Load().RetentionDays != 10 {
		t.Fatal("retention did not save")
	}
	_, html := b.request(t, "GET", "/", nil, false)
	for _, old := range []string{"id=\"set-max_rows\"", "id=\"set-max_db_mb\""} {
		if strings.Contains(string(html), old) {
			t.Fatal("obsolete field in UI")
		}
	}
	if !strings.Contains(string(html), "settings-storage") {
		t.Fatal("storage status panel missing")
	}
}
