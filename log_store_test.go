package main

import (
	"context"
	"testing"
)

func TestNewestLogsKeepIDPaginationWithOutOfOrderTimes(t *testing.T) {
	a := testApp(t)
	items := []Event{}
	for _, ts := range []float64{100, 200, 90, 150, 250, 180} {
		items = append(items, Event{Log: parseSyslog([]byte("ordinary message"), "127.0.0.1", ts)})
	}
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	f := LogFilters{Since: 100, Until: 200, Fields: map[string]string{}}
	first, err := a.store.logs(context.Background(), f, 2, 0)
	if err != nil || len(first) != 2 || first[0].ID != 6 || first[1].ID != 4 {
		t.Fatalf("newest IDs with time bounds: %+v, %v", first, err)
	}
	second, err := a.store.logs(context.Background(), f, 2, first[1].ID)
	if err != nil || len(second) != 2 || second[0].ID != 2 || second[1].ID != 1 {
		t.Fatalf("older IDs with time bounds: %+v, %v", second, err)
	}
	f.Fields["category"] = "other"
	filtered, err := a.store.logs(context.Background(), f, 2, first[1].ID)
	if err != nil || len(filtered) != 2 || filtered[0].ID != 2 || filtered[1].ID != 1 {
		t.Fatalf("category pagination with time bounds: %+v, %v", filtered, err)
	}
	f = LogFilters{Since: 300, Until: 400}
	empty, err := a.store.logs(context.Background(), f, 2, 0)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty time range: %+v, %v", empty, err)
	}
}

func TestLogSummaryPreservesFilteredTotalsAndGlobalCoverage(t *testing.T) {
	a := testApp(t)
	items := []Event{
		{Log: parseSyslog([]byte("ordinary message"), "127.0.0.1", 50)},
		{Log: parseSyslog([]byte("RW-SYS-SYSTEM login failure for user admin from 192.0.2.9 via ssh"), "127.0.0.1", 100)},
		{Log: parseSyslog([]byte("RW-SYS-SYSTEM user admin logged in from 192.0.2.9 via api"), "127.0.0.1", 110)},
		{Log: parseSyslog([]byte("RW-SYS-SYSTEM filter rule added by owner"), "127.0.0.1", 120)},
		{Log: parseSyslog([]byte("<180>Nov 1 10:00:00 test RW-SYS-INTERFACE ether1 link down"), "127.0.0.1", 130)},
		{Log: parseSyslog([]byte("RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 192.0.2.7:12->192.0.2.1:22, len 60"), "127.0.0.1", 140)},
	}
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []map[string]string{{}, {"category": "login"}} {
		summary, err := a.store.logSummary(context.Background(), LogFilters{Since: 100, Until: 140, Fields: fields})
		if err != nil {
			t.Fatal(err)
		}
		totals := summary["totals"].(map[string]any)
		wantTotal, wantChanges, wantWarnings := 5.0, 1.0, 1.0
		if fields["category"] == "login" {
			wantTotal, wantChanges, wantWarnings = 2, 0, 0
		}
		if number(totals["total"]) != wantTotal || number(totals["failures"]) != 1 || number(totals["successes"]) != 1 || number(totals["changes"]) != wantChanges || number(totals["warnings"]) != wantWarnings {
			t.Fatalf("filtered totals: %+v", totals)
		}
		coverage := summary["coverage"].(map[string]any)
		if number(coverage["stored"]) != 6 || number(coverage["system_logs"]) != 5 || number(coverage["oldest"]) != 50 || number(coverage["newest"]) != 140 {
			t.Fatalf("global coverage: %+v", coverage)
		}
	}
}

func TestEmptyLogCoverage(t *testing.T) {
	a := testApp(t)
	summary, err := a.store.logSummary(context.Background(), LogFilters{Since: 100, Until: 200})
	if err != nil {
		t.Fatal(err)
	}
	coverage := summary["coverage"].(map[string]any)
	if number(coverage["stored"]) != 0 || number(coverage["system_logs"]) != 0 || coverage["oldest"] != nil || coverage["newest"] != nil {
		t.Fatalf("empty coverage: %+v", coverage)
	}
}
