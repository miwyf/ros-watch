package main

import (
	"context"
	"database/sql"
)

const minimumFreeDiskBytes uint64 = 1024 * 1024 * 1024

type tableCoverage struct {
	records        int64
	oldest, newest sql.NullFloat64
}

type storageCoverage struct {
	events, logs tableCoverage
	systemLogs   int64
}

func (c *tableCoverage) append(ts float64) {
	c.records++
	if !c.oldest.Valid || ts < c.oldest.Float64 {
		c.oldest = sql.NullFloat64{Float64: ts, Valid: true}
	}
	if !c.newest.Valid || ts > c.newest.Float64 {
		c.newest = sql.NullFloat64{Float64: ts, Valid: true}
	}
}

func (c tableCoverage) report() map[string]any {
	var oldest, newest any
	if c.oldest.Valid {
		oldest = c.oldest.Float64
	}
	if c.newest.Valid {
		newest = c.newest.Float64
	}
	return map[string]any{"stored": c.records, "oldest": oldest, "newest": newest}
}

func (c storageCoverage) logReport() map[string]any {
	result := c.logs.report()
	result["system_logs"] = c.systemLogs
	return result
}

func (c storageCoverage) records() int64 {
	return c.events.records + c.logs.records
}

func (c *storageCoverage) bounds(ctx context.Context, tx *sql.Tx) error {
	return tx.QueryRowContext(ctx, `SELECT
 (SELECT MIN(ts) FROM events),(SELECT MAX(ts) FROM events),
 (SELECT MIN(ts) FROM logs),(SELECT MAX(ts) FROM logs)`).
		Scan(&c.events.oldest, &c.events.newest, &c.logs.oldest, &c.logs.newest)
}

func (s *Store) coverage() storageCoverage {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	return s.storage
}

func (s *Store) setCoverage(coverage storageCoverage) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	s.storage = coverage
}

func (s *Store) recordStorageAppend(events []Event) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	for _, e := range events {
		if e.Log != nil {
			s.storage.logs.append(e.Log.TS)
			if e.Log.Category != "firewall" {
				s.storage.systemLogs++
			}
		}
		if e.Scope != "" {
			s.storage.events.append(e.TS)
		}
	}
}

func (s *Store) storageStatus() map[string]any {
	coverage := s.coverage()
	free, err := s.availableDisk(s.dir)
	state := "normal"
	var freeValue any = free
	minimum := uint64(s.settings.Load().MinFreeMB) * 1024 * 1024
	if err != nil {
		state = "unknown"
		freeValue = nil
	} else if free < minimum {
		state = "low_space"
	}
	merged := coverage.events
	if t := coverage.logs.oldest; t.Valid && (!merged.oldest.Valid || t.Float64 < merged.oldest.Float64) {
		merged.oldest = t
	}
	if t := coverage.logs.newest; t.Valid && (!merged.newest.Valid || t.Float64 > merged.newest.Float64) {
		merged.newest = t
	}
	bounds := merged.report()
	return map[string]any{"database_bytes": s.diskUsage(), "disk_free_bytes": freeValue, "minimum_free_bytes": minimum, "space_state": state, "records": coverage.records(), "oldest": bounds["oldest"], "newest": bounds["newest"], "retention_days": s.settings.Load().RetentionDays, "disk_low_action": s.settings.Load().DiskLowAction, "space_pruned": s.spacePruned.Load(), "last_space_cleanup": nullableTime(s.lastSpaceCleanup.Load()), "last_space_attempt": nullableTime(s.lastSpaceAttempt.Load()), "space_reclaim_supported": s.reclaimSupported}
}
