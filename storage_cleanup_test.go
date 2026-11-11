package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoSpaceCleanupReclaimsFileAndPersistsAudit(t *testing.T) {
	a := testApp(t)
	items := make([]Event, 5000)
	now := unixNow()
	for i := range items {
		items[i] = Event{Log: parseSyslog([]byte(fmt.Sprintf("message-%04d-", i)+strings.Repeat("x", 3900)), "127.0.0.1", now-float64(5000-i))}
	}
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	a.store.reclaimPages(context.Background())
	path := filepath.Join(a.config.DataDir, "events.sqlite3")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	initial := info.Size()
	a.store.availableDisk = func(string) (uint64, error) {
		info, err := os.Stat(path)
		if err != nil {
			return 0, err
		}
		reclaimed := initial - info.Size()
		if reclaimed < 0 {
			reclaimed = 0
		}
		return minimumFreeDiskBytes - 4*1024*1024 + uint64(reclaimed), nil
	}
	if !a.store.ensureDiskSpace(context.Background()) {
		t.Fatal("automatic cleanup failed to restore threshold", a.store.storageStatus())
	}
	status := a.store.storageStatus()
	if status["space_pruned"].(int64) <= 0 || status["records"].(int64) <= 0 || status["last_space_cleanup"] == nil {
		t.Fatal("cleanup audit missing", status)
	}
	var oldest string
	if err := a.store.reader.QueryRow("SELECT message FROM logs ORDER BY ts LIMIT 1").Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(oldest, "message-0000-") {
		t.Fatal("oldest records not removed")
	}
	count := status["space_pruned"]
	a.store.close()
	a.store, err = openStore(a.config.DataDir, &a.settings)
	if err != nil {
		t.Fatal(err)
	}
	a.collector.store = a.store
	if a.store.storageStatus()["space_pruned"] != count {
		t.Fatal("cleanup audit lost on restart")
	}
}

func TestAutoSpaceCleanupPreservesHistoryWhenLogsCannotRestoreReserve(t *testing.T) {
	a := testApp(t)
	if err := a.store.append(context.Background(), []Event{*sampleEvent(22)}); err != nil {
		t.Fatal(err)
	}
	a.store.availableDisk = func(string) (uint64, error) { return 0, nil }
	if a.store.ensureDiskSpace(context.Background()) {
		t.Fatal("insufficient disk accepted")
	}
	if a.store.storageStatus()["records"] != int64(1) || a.store.spacePruned.Load() != 0 {
		t.Fatal("history deleted despite insufficient reclaimable space")
	}
}
