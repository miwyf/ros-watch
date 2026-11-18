package main

import (
	"context"
	"database/sql"
	"log"
	"math"
	"strings"
	"time"
)

const minuteSeconds = 60
const backfillMinutes = 15
const backfillRows = 20000

type minuteRollup struct {
	raw, table string
	dimensions []string
}

var eventMinutes = minuteRollup{"events", "event_minutes", strings.Split(eventMinuteFields, ",")}
var logMinutes = minuteRollup{"logs", "log_minutes", strings.Split(logMinuteFields, ",")}

const eventMinuteFields = "wan,proto,scope,outcome,direction"
const logMinuteFields = "category,severity,event"

func (m minuteRollup) schema() string {
	fields := strings.Join(m.dimensions, ",")
	definitions := make([]string, len(m.dimensions))
	keys := make([]string, len(m.dimensions))
	newValues := make([]string, len(m.dimensions))
	for i, field := range m.dimensions {
		definitions[i] = field + " TEXT NOT NULL"
		keys[i] = field + "=OLD." + field
		newValues[i] = "NEW." + field
	}
	return strings.NewReplacer(
		"@raw", m.raw, "@table", m.table,
		"@definitions", strings.Join(definitions, ","), "@fields", fields,
		"@new", strings.Join(newValues, ","), "@old", strings.Join(keys, " AND "),
	).Replace(`
CREATE TABLE IF NOT EXISTS @table(
 minute INTEGER NOT NULL,@definitions,weight INTEGER NOT NULL,
 PRIMARY KEY(minute,@fields)
) WITHOUT ROWID;
CREATE TRIGGER IF NOT EXISTS @table_insert AFTER INSERT ON @raw
 WHEN NEW.ts >= (SELECT covered_since FROM minute_rollup_state WHERE id=1)
 BEGIN
  INSERT INTO @table(minute,@fields,weight) VALUES(CAST(NEW.ts/60 AS INTEGER)*60,@new,1)
  ON CONFLICT(minute,@fields) DO UPDATE SET weight=weight+1;
 END;
CREATE TRIGGER IF NOT EXISTS @table_delete AFTER DELETE ON @raw
 WHEN OLD.ts >= (SELECT covered_since FROM minute_rollup_state WHERE id=1)
 BEGIN
  UPDATE @table SET weight=weight-1 WHERE minute=CAST(OLD.ts/60 AS INTEGER)*60 AND @old;
  DELETE FROM @table WHERE minute=CAST(OLD.ts/60 AS INTEGER)*60 AND @old AND weight=0;
 END;
`)
}

func (s *Store) initRollups() error {
	ctx := context.Background()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS minute_rollup_state(
 id INTEGER PRIMARY KEY CHECK(id=1), covered_since INTEGER NOT NULL
)`); err != nil {
		return err
	}
	// Cover preexisting rows even if they were received before a clock correction.
	var newest float64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(?,COALESCE((SELECT MAX(ts) FROM events),0),
 COALESCE((SELECT MAX(ts) FROM logs),0))`, unixNow()).Scan(&newest); err != nil {
		return err
	}
	start := (int64(newest/minuteSeconds) + 1) * minuteSeconds
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO minute_rollup_state VALUES(1,?)", start); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, eventMinutes.schema()+logMinutes.schema()); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT covered_since FROM minute_rollup_state WHERE id=1").Scan(&start); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.coveredSince.Store(start)
	return nil
}

func (s *Store) startRollupBackfill() {
	ctx, cancel := context.WithCancel(context.Background())
	s.stopRollupBackfill = cancel
	s.rollupBackfillDone = make(chan struct{})
	go func() {
		defer close(s.rollupBackfillDone)
		for ctx.Err() == nil && s.coveredSince.Load() > 0 {
			delay := 30 * time.Millisecond
			free, err := availableDisk(s.dir)
			if err != nil || free < uint64(s.settings.Load().MinFreeMB)*1024*1024 {
				delay = 5 * time.Second
			} else if err := s.backfillMinutes(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("minute statistics backfill: %v", err)
				delay = 5 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}()
}

func (s *Store) backfillMinutes(ctx context.Context) error {
	// Ingestion and retention use this same writer. Commit each batch before
	// releasing it so changes cannot fall between the historical and live counts.
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var end int64
	if err = tx.QueryRowContext(ctx, "SELECT covered_since FROM minute_rollup_state WHERE id=1").Scan(&end); err != nil {
		return err
	}
	if end == 0 {
		s.coveredSince.Store(0)
		return nil
	}
	var newest, capped sql.NullFloat64
	for _, m := range []minuteRollup{eventMinutes, logMinutes} {
		var last, limit sql.NullFloat64
		selectTime := "SELECT ts FROM " + m.raw + " WHERE ts>=0 AND ts<? ORDER BY ts DESC LIMIT 1"
		if err = tx.QueryRowContext(ctx, "SELECT ("+selectTime+"),("+selectTime+" OFFSET ?)", end, end, backfillRows-1).Scan(&last, &limit); err != nil {
			return err
		}
		if last.Valid && (!newest.Valid || last.Float64 > newest.Float64) {
			newest = last
		}
		if limit.Valid && (!capped.Valid || limit.Float64 > capped.Float64) {
			capped = limit
		}
	}
	var start int64
	if newest.Valid {
		start = max(0, int64(newest.Float64/minuteSeconds)*minuteSeconds-(backfillMinutes-1)*minuteSeconds)
		if capped.Valid {
			start = max(start, int64(capped.Float64/minuteSeconds)*minuteSeconds)
		}
		for _, m := range []minuteRollup{eventMinutes, logMinutes} {
			fields := strings.Join(m.dimensions, ",")
			q := "INSERT INTO " + m.table + "(minute," + fields + ",weight)" +
				" SELECT CAST(ts/60 AS INTEGER)*60 AS minute," + fields + ",COUNT(*) FROM " + m.raw +
				" WHERE ts>=? AND ts<? GROUP BY minute," + fields
			if _, err = tx.ExecContext(ctx, q, start, end); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE minute_rollup_state SET covered_since=? WHERE id=1", start); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.coveredSince.Store(start)
	return nil
}

// countInput mixes full, covered minutes with raw records at both endpoints
// and in any remaining historical gap. The inclusive upper bound stays raw
// whenever it falls exactly at the start of a new minute.
func (s *Store) countInput(m minuteRollup, columns string, since, until float64, eligible bool) (string, []any) {
	// The backfill publishes a lower limit only after committing it. An older
	// limit is still safe and only means reading a larger raw interval.
	from := max(math.Ceil(since/minuteSeconds)*minuteSeconds, float64(s.coveredSince.Load()))
	to := math.Floor(until/minuteSeconds) * minuteSeconds
	raw := "SELECT ts," + columns + ",1 AS weight FROM " + m.raw
	if !eligible || from >= to {
		return "(SELECT *,1 AS weight FROM " + m.raw + ")", nil
	}
	return "(SELECT minute AS ts," + columns + ",weight FROM " + m.table + " WHERE minute>=? AND minute<?" +
			" UNION ALL " + raw + " WHERE ts>=? AND ts<?" +
			" UNION ALL " + raw + " WHERE ts>=? AND ts<=?)",
		[]any{from, to, since, from, to, until}
}
