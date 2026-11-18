package main

import (
	"context"
	"strings"
)

const logColumns = "ts,sender,device,device_time,category,severity,username,source,method,event,message,raw,topic"
const logSchema = `CREATE TABLE IF NOT EXISTS logs(id INTEGER PRIMARY KEY,ts REAL NOT NULL,sender TEXT NOT NULL,device TEXT NOT NULL,device_time TEXT NOT NULL,category TEXT NOT NULL,severity TEXT NOT NULL,username TEXT NOT NULL,source TEXT NOT NULL,method TEXT NOT NULL,event TEXT NOT NULL,message TEXT NOT NULL,raw TEXT NOT NULL,topic TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS logs_time ON logs(ts);
CREATE INDEX IF NOT EXISTS logs_time_summary ON logs(ts,event,category,severity);
CREATE INDEX IF NOT EXISTS logs_category_time ON logs(category,ts);
CREATE INDEX IF NOT EXISTS logs_category_id ON logs(category,id);
CREATE INDEX IF NOT EXISTS logs_login_source ON logs(source,username,ts) WHERE category='login';
CREATE INDEX IF NOT EXISTS logs_attention_time ON logs(ts) WHERE (category='config' OR event='reboot' OR severity IN ('warning','error','critical','alert','emergency')) AND NOT (category='login' AND event='failure');`

type LogFilters struct {
	Since, Until float64
	Hours        float64
	Fields       map[string]string
	Search       string
}

func (f LogFilters) where() (string, []any) {
	terms := []string{"ts>=?", "ts<=?"}
	args := []any{f.Since, f.Until}
	for _, key := range []string{"category", "severity", "username", "source", "sender", "device", "event"} {
		if v := f.Fields[key]; v != "" {
			terms = append(terms, key+"=?")
			args = append(args, v)
		}
	}
	if f.Search != "" {
		terms = append(terms, "instr(lower(raw),lower(?))>0")
		args = append(args, f.Search)
	}
	return strings.Join(terms, " AND "), args
}
func (s *Store) logs(ctx context.Context, f LogFilters, limit int, before int64) ([]LogEvent, error) {
	where, args := f.where()
	if before > 0 {
		where += " AND id<?"
		args = append(args, before)
	}
	from := "logs"
	// The unfiltered newest page can walk row IDs backwards and stop at LIMIT.
	// A time-index range scan instead sorts every matching log before returning it.
	if len(f.Fields) == 0 && f.Search == "" {
		var exists bool
		if err := s.reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM logs WHERE "+where+")", args...).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return []LogEvent{}, nil
		}
		from += " NOT INDEXED"
	} else if f.Fields["category"] != "" && f.Search == "" {
		from += " INDEXED BY logs_category_id"
	}
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, "SELECT id,"+logColumns+" FROM "+from+" WHERE "+where+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LogEvent, 0)
	for rows.Next() {
		l := LogEvent{}
		if err = rows.Scan(&l.ID, &l.TS, &l.Sender, &l.Device, &l.DeviceTime, &l.Category, &l.Severity, &l.Username, &l.Source, &l.Method, &l.Event, &l.Message, &l.Raw, &l.Topic); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func (s *Store) logSummary(ctx context.Context, f LogFilters) (map[string]any, error) {
	w, a := f.where()
	eligible := f.Search == "" && f.Fields["username"] == "" && f.Fields["source"] == "" &&
		f.Fields["sender"] == "" && f.Fields["device"] == ""
	from, rangeArgs := s.countInput(logMinutes, logMinuteFields, f.Since, f.Until, eligible)
	totals, err := queryMaps(ctx, s.reader, `SELECT COALESCE(SUM(weight),0) total,
 COALESCE(SUM(weight*(event='failure')),0) failures,
 COALESCE(SUM(weight*(event='success')),0) successes,
 COALESCE(SUM(weight*(category='config')),0) changes,
 COALESCE(SUM(weight*(severity IN ('warning','error','critical','alert','emergency'))),0) warnings
 FROM `+from+" WHERE "+w, append(rangeArgs, a...)...)
	if err != nil {
		return nil, err
	}
	// Fixed five-minute windows; only messages carrying an explicit source qualify.
	failures, err := queryMaps(ctx, s.reader, `SELECT source,username,COUNT(*) attempts,MIN(ts) first,MAX(ts) last FROM logs WHERE `+w+` AND category='login' AND event='failure' AND source<>'' GROUP BY source,username,CAST(ts/300 AS INTEGER) HAVING COUNT(*)>=5 ORDER BY last DESC LIMIT 20`, a...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"totals": totals[0], "failures": failures, "coverage": s.coverage().logReport()}, nil
}
