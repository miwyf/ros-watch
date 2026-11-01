package main

import (
	"encoding/csv"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

func (a *App) logFilters(r *http.Request) (LogFilters, int64, error) {
	q := r.URL.Query()
	h, err := strconv.ParseFloat(valueOr(q.Get("hours"), "24"), 64)
	f := LogFilters{Until: unixNow(), Fields: map[string]string{}}
	if err != nil || math.IsNaN(h) || math.IsInf(h, 0) || h <= 0 || h > float64(a.settings.Load().RetentionDays*24) {
		return f, 0, errors.New("invalid hours")
	}
	f.Since = f.Until - h*3600
	choices := map[string]string{"category": "|login|config|network|script|system|other|firewall|", "severity": "|emergency|alert|critical|error|warning|notice|info|debug|unknown|", "event": "|success|failure|logout|change|state|script|reboot|access|message|"}
	for _, key := range []string{"category", "severity", "username", "source", "sender", "device", "event"} {
		v := strings.TrimSpace(q.Get(key))
		if v == "" {
			continue
		}
		if len(v) > 256 {
			return f, 0, errors.New("filter too long")
		}
		if key == "source" || key == "sender" {
			addr, e := netip.ParseAddr(v)
			if e != nil {
				return f, 0, e
			}
			v = addr.Unmap().String()
		}
		if allowed, ok := choices[key]; ok && !strings.Contains(allowed, "|"+v+"|") {
			return f, 0, errors.New("invalid filter")
		}
		f.Fields[key] = v
	}
	f.Search = strings.TrimSpace(q.Get("q"))
	if len(f.Search) > 256 {
		return f, 0, errors.New("search too long")
	}
	var before int64
	if q.Get("before") != "" {
		before, err = strconv.ParseInt(q.Get("before"), 10, 64)
		if err != nil || before <= 0 {
			return f, 0, errors.New("invalid cursor")
		}
	}
	return f, before, nil
}

func (a *App) logs(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	f, before, err := a.logFilters(r)
	if err != nil {
		filterError(w)
		return
	}
	if !a.acquireQuery(w) {
		return
	}
	defer func() { <-a.expensive }()
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	rows, err := a.store.logs(ctx, f, 51, before)
	if err != nil {
		queryError(w)
		return
	}
	summary, err := a.store.logSummary(ctx, f)
	if err != nil {
		queryError(w)
		return
	}
	var next any
	if len(rows) > 50 {
		next = rows[49].ID
		rows = rows[:50]
	}
	summary["rows"], summary["next"], summary["status"] = rows, next, a.collector.status()
	jsonResponse(w, 200, summary)
}

func (a *App) exportLogs(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	f, _, err := a.logFilters(r)
	if err != nil {
		filterError(w)
		return
	}
	if !a.acquireQuery(w) {
		return
	}
	defer func() { <-a.expensive }()
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	rows, err := a.store.logs(ctx, f, 10000, 0)
	if err != nil {
		queryError(w)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=ros-watch-system-logs.csv")
	_, _ = io.WriteString(w, "\ufeff")
	out := csv.NewWriter(w)
	_ = out.Write([]string{"received_ts", "sender", "device", "device_time", "category", "severity", "username", "source", "method", "event", "message", "raw"})
	for _, l := range rows {
		values := []string{strconv.FormatFloat(l.TS, 'f', 6, 64), l.Sender, l.Device, l.DeviceTime, l.Category, l.Severity, l.Username, l.Source, l.Method, l.Event, l.Message, l.Raw}
		for i, v := range values {
			clean := strings.TrimLeft(v, " \t\r\n")
			if clean != "" && strings.ContainsRune("=+-@", rune(clean[0])) {
				values[i] = "'" + v
			}
		}
		if out.Write(values) != nil {
			return
		}
	}
	out.Flush()
}
