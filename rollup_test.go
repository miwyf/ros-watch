package main

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestMinuteRollupMatchesRawDuringBackfillAndPruning(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	a.store.stopRollupBackfill()
	<-a.store.rollupBackfillDone

	base := math.Floor(unixNow()/60)*60 - 3600
	covered := int64(base) + 120
	if _, err := a.store.writer.Exec("UPDATE minute_rollup_state SET covered_since=? WHERE id=1", covered); err != nil {
		t.Fatal(err)
	}
	a.store.coveredSince.Store(covered)

	add := func(offset float64, category, direction, outcome string, port int) {
		t.Helper()
		event := *sampleEvent(port)
		event.TS, event.Direction, event.Outcome = base+offset, direction, outcome
		event.Log = &LogEvent{TS: event.TS, Category: category, Severity: "warning", Event: "failure", Username: "admin", Raw: "system notice"}
		if err := a.store.append(ctx, []Event{event}); err != nil {
			t.Fatal(err)
		}
	}
	add(59.25, "other", "response", "blocked", 22)
	add(59.5, "login", "inbound", "attempt", 22)
	add(60, "login", "inbound", "blocked", 23)
	add(119.75, "other", "response", "attempt", 23)
	add(120, "config", "response", "blocked", 443)
	add(179.75, "config", "inbound", "attempt", 443)
	add(180, "other", "inbound", "attempt", 22)
	add(180.5, "login", "inbound", "blocked", 22)
	add(3000, "other", "inbound", "attempt", 443)

	compare := func() {
		t.Helper()
		floor := a.store.coveredSince.Load()
		defer a.store.coveredSince.Store(floor)
		for _, window := range [][2]float64{{59.5, 180}, {60, 179.75}, {60.5, 120}, {150, 1400}, {480, 660}} {
			f := Filters{Since: base + window[0], Until: base + window[1], Fields: map[string]any{"direction": "inbound", "wan": "WAN1"}}
			lf := LogFilters{Since: f.Since, Until: f.Until, Fields: map[string]string{"severity": "warning"}}
			a.store.coveredSince.Store(floor)
			withEvents, err := a.store.summary(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			withLogs, err := a.store.logSummary(ctx, lf)
			if err != nil {
				t.Fatal(err)
			}
			a.store.coveredSince.Store(int64(base) + 4000)
			rawEvents, err := a.store.summary(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			rawLogs, err := a.store.logSummary(ctx, lf)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(withEvents, rawEvents) || !reflect.DeepEqual(withLogs, rawLogs) {
				t.Fatalf("rollup disagrees with raw for %v: events %v / %v; logs %v / %v", window, withEvents, rawEvents, withLogs, rawLogs)
			}
		}
	}

	compare()
	if err := a.store.backfillMinutes(ctx); err != nil {
		t.Fatal(err)
	}
	if start := a.store.coveredSince.Load(); start == covered || start == 0 {
		t.Fatal("backfill progress was not committed", start)
	}
	compare()

	a.store.close()
	store, err := openStore(a.config.DataDir, &a.settings)
	if err != nil {
		t.Fatal(err)
	}
	a.store, a.collector.store = store, store
	deadline := time.Now().Add(3 * time.Second)
	for store.coveredSince.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.coveredSince.Load() != 0 {
		t.Fatal("backfill did not resume")
	}
	compare()
	add(120.5, "login", "inbound", "blocked", 443)
	compare()
	if n, err := a.store.deleteOldestForSpace(ctx, 5); err != nil || n != 5 {
		t.Fatal("space prune", n, err)
	}
	compare()
	add(-8*86400, "login", "inbound", "blocked", 22)
	if err := a.store.maintenance(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range []minuteRollup{eventMinutes, logMinutes} {
		var match bool
		if err := a.store.reader.QueryRow("SELECT (SELECT COALESCE(SUM(weight),0) FROM " + m.table + ")=(SELECT COUNT(*) FROM " + m.raw + ")").Scan(&match); err != nil || !match {
			t.Fatal("row counts diverged after backfill and pruning", m.raw, err)
		}
	}
}

func TestSummaryRankTotalsCountAllSourcesAndDistinctNonNullPorts(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	now := unixNow()
	for i := 0; i < 20; i++ {
		e := *sampleEvent(i % 10)
		e.TS = now - 90
		e.Src = fmt.Sprintf("198.51.100.%d", i+1)
		if i >= 10 {
			e.Proto = "UDP"
		}
		if i == 19 {
			e.Dport = nil
		}
		if err := a.store.append(ctx, []Event{e}); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := a.store.saveBusiness(ctx, BusinessRule{
				Name: "service", Sender: e.Sender, Src: e.Src, Dst: e.Dst, Proto: e.Proto,
				Dport: *e.Dport, Scope: e.Scope, Interface: e.Interface,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, fields := range []map[string]any{nil, {"dport": 1}, {"proto": "UDP"}, {"src": sampleEvent(2).Src}} {
		f := Filters{Since: now - 1000, Until: now, Fields: fields}
		result, err := a.store.summary(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		where, args := f.where()
		raw, err := queryMaps(ctx, a.store.reader, "SELECT COUNT(*) observations,COUNT(DISTINCT src) sources,COUNT(DISTINCT dport) ports,COALESCE(SUM("+businessExistsSQL+"),0) business FROM events WHERE "+where, args...)
		if err != nil {
			t.Fatal(err)
		}
		totals := result["totals"].(map[string]any)
		for k, want := range raw[0] {
			if totals[k] != want {
				t.Fatalf("wrong exact total for %s with %v: got %v want %v", k, fields, totals[k], want)
			}
		}
	}
}
