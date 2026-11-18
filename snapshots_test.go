package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundStatisticsLeaveDetailsAvailable(t *testing.T) {
	a := testApp(t)
	if err := a.store.append(context.Background(), []Event{
		*sampleEvent(22),
		{Log: parseSyslog([]byte("test log"), "127.0.0.1", unixNow())},
	}); err != nil {
		t.Fatal(err)
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")

	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var runs atomic.Int64
	build := func(ctx context.Context, now float64) (map[string]any, error) {
		runs.Add(1)
		close(started)
		select {
		case <-release:
			return map[string]any{"sample_at": now}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	const key = "blocked"
	_, ready, _ := a.getSnapshot("test", key, build)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker never started")
	}
	for _, path := range []string{"/api/summary?hours=168&async=1", "/api/logs/summary?hours=168&async=1"} {
		if code, body := b.request(t, "GET", path, nil, false); code != http.StatusAccepted || decoded(t, body)["pending"] != true {
			t.Fatal(path, code, string(body))
		}
	}
	for _, path := range []string{"/api/events?hours=168", "/api/logs?hours=168&details=1", "/api/status"} {
		code, body := b.request(t, "GET", path, nil, false)
		if code != http.StatusOK {
			t.Fatal(path, code, string(body))
		}
		data := decoded(t, body)
		if path == "/api/status" {
			if data["access"].(map[string]any)["stored"] != float64(1) || data["logs"].(map[string]any)["stored"] != float64(1) {
				t.Fatal("wrong store coverage", data)
			}
		} else if len(data["rows"].([]any)) != 1 || data["totals"] != nil {
			t.Fatal("unexpected details", data)
		}
	}

	if value, secondReady, _ := a.getSnapshot("test", key, build); value != nil || secondReady != ready {
		t.Fatal("duplicate refresh was scheduled")
	}
	unblock.Do(func() { close(release) })
	<-ready
	value, _, _ := a.getSnapshot("test", key, build)
	if value["sample_at"] != value["generated_at"] || runs.Load() != 1 {
		t.Fatal("snapshot time or deduplication", value, runs.Load())
	}
	for _, path := range []string{"/api/summary?hours=168", "/api/logs/summary?hours=168"} {
		if code, body := b.request(t, "GET", path, nil, false); code != http.StatusOK || decoded(t, body)["generated_at"] == nil {
			t.Fatal(path, code, string(body))
		}
	}
	a.queryMu.Lock()
	a.cache[key].updated = time.Now().Add(-snapshotAge)
	a.queryMu.Unlock()
	failingBuild := func(context.Context, float64) (map[string]any, error) {
		return nil, errors.New("test query failure")
	}
	stale, retry, _ := a.getSnapshot("test", key, failingBuild)
	if stale["generated_at"] != value["generated_at"] || retry == nil {
		t.Fatal("missing stale response during refresh")
	}
	<-retry
	stale, retry, failed := a.getSnapshot("test", key, failingBuild)
	if !failed || retry != nil || stale["generated_at"] != value["generated_at"] {
		t.Fatal("failed refresh discarded or retimed the last good result")
	}
}
