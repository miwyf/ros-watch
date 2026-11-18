package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
)

const (
	snapshotAge      = 30 * time.Second
	snapshotRetry    = 15 * time.Second
	snapshotBudget   = 75 * time.Second
	snapshotFastWait = 100 * time.Millisecond
	snapshotCapacity = 24
)

type cachedSummary struct {
	value    map[string]any
	updated  time.Time
	lastUsed time.Time
	failed   time.Time
	ready    chan struct{}
	cancel   context.CancelFunc
}

type snapshotJob struct {
	kind, key string
	entry     *cachedSummary
	ctx       context.Context
	build     func(context.Context, float64) (map[string]any, error)
}

func snapshotKey(kind string, hours float64, fields any, extra string) string {
	encoded, _ := json.Marshal(fields)
	return kind + ":" + strconv.FormatFloat(hours, 'g', -1, 64) + ":" + string(encoded) + ":" + extra
}

func (a *App) startSnapshots() {
	a.snapshotCtx, a.snapshotCancel = context.WithCancel(context.Background())
	a.snapshotWork = make(chan snapshotJob, 2)
	a.snapshotWG.Add(1)
	go func() {
		defer a.snapshotWG.Done()
		for {
			select {
			case <-a.snapshotCtx.Done():
				return
			case job := <-a.snapshotWork:
				a.buildSnapshot(job)
			}
		}
	}()
}

func (a *App) buildSnapshot(job snapshotJob) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(job.ctx, snapshotBudget)
	defer cancel()
	var value map[string]any
	err := ctx.Err()
	if err == nil {
		value, err = job.build(ctx, float64(started.UnixNano())/1e9)
	}
	a.queryMu.Lock()
	defer a.queryMu.Unlock()
	defer job.entry.cancel()
	defer func() {
		close(job.entry.ready)
		job.entry.ready = nil
	}()
	if a.cache[job.key] != job.entry {
		return
	}
	if err == nil {
		value["generated_at"] = float64(started.UnixNano()) / 1e9
		job.entry.value = value
		job.entry.updated = time.Now()
		job.entry.failed = time.Time{}
	} else if !errors.Is(err, context.Canceled) {
		job.entry.failed = time.Now()
		log.Printf("%s statistics failed after %s: %v", job.kind, time.Since(started).Round(time.Millisecond), err)
	}
}

func (a *App) clearSnapshots() {
	a.queryMu.Lock()
	defer a.queryMu.Unlock()
	for _, entry := range a.cache {
		if entry.ready != nil {
			entry.cancel()
		}
	}
	clear(a.cache)
}

func (a *App) getSnapshot(kind, key string, build func(context.Context, float64) (map[string]any, error)) (map[string]any, <-chan struct{}, bool) {
	a.queryMu.Lock()
	defer a.queryMu.Unlock()
	now := time.Now()
	entry := a.cache[key]
	if entry == nil {
		if len(a.cache) >= snapshotCapacity {
			var oldestKey string
			oldest := now
			for candidate, cached := range a.cache {
				if cached.ready == nil && cached.lastUsed.Before(oldest) {
					oldestKey, oldest = candidate, cached.lastUsed
				}
			}
			if oldestKey == "" {
				return nil, nil, false
			}
			delete(a.cache, oldestKey)
		}
		entry = &cachedSummary{}
		a.cache[key] = entry
	}
	entry.lastUsed = now
	recentFailure := now.Sub(entry.failed) < snapshotRetry
	if entry.ready == nil && now.Sub(entry.updated) >= snapshotAge && !recentFailure && a.snapshotCtx.Err() == nil {
		ctx, cancel := context.WithCancel(a.snapshotCtx)
		select {
		case a.snapshotWork <- snapshotJob{kind: kind, key: key, entry: entry, ctx: ctx, build: build}:
			entry.ready = make(chan struct{})
			entry.cancel = cancel
		default:
			cancel()
		}
	}
	return entry.value, entry.ready, recentFailure
}

func (a *App) serveSnapshot(w http.ResponseWriter, r *http.Request, kind, key string, build func(context.Context, float64) (map[string]any, error)) map[string]any {
	value, ready, failed := a.getSnapshot(kind, key, build)
	if value != nil {
		return value
	}
	async := r.URL.Query().Get("async") == "1"
	if ready != nil {
		wait := queryBudget
		if async {
			wait = snapshotFastWait
		}
		ctx, cancel := context.WithTimeout(r.Context(), wait)
		defer cancel()
		select {
		case <-ready:
			a.queryMu.Lock()
			if entry := a.cache[key]; entry != nil {
				value = entry.value
				failed = !entry.failed.IsZero()
			}
			a.queryMu.Unlock()
		case <-ctx.Done():
		}
	}
	if value != nil {
		return value
	}
	if !async {
		queryError(w)
	} else if failed {
		apiError(w, http.StatusServiceUnavailable, "统计暂不可用，稍后自动重试")
	} else {
		jsonResponse(w, http.StatusAccepted, map[string]bool{"pending": true})
	}
	return nil
}
