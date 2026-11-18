package main

import (
	"context"
	"time"
)

// Reclaim only on low space, in bounded batches, with a cooldown on failure.
func (s *Store) ensureDiskSpace(ctx context.Context) bool {
	if s.diskOK() {
		return true
	}
	if s.settings.Load().DiskLowAction != "prune" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.diskOK() {
		return true
	}
	if last := s.lastSpaceAttempt.Load(); last > 0 && time.Since(time.Unix(0, last)) < 30*time.Second {
		return false
	}
	s.lastSpaceAttempt.Store(time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.reclaimPages(ctx)
	for i := 0; i < 16 && ctx.Err() == nil; i++ {
		free, err := s.availableDisk(s.dir)
		if err != nil {
			return false
		}
		minimum := uint64(s.settings.Load().MinFreeMB) * 1024 * 1024
		if free >= minimum {
			return true
		}
		// If the entire log database cannot restore the reserve, preserve history.
		size := s.diskUsage()
		if !s.reclaimSupported || size <= 0 || uint64(size) <= minimum-free {
			return false
		}
		records := s.coverage().records()
		if records == 0 {
			return false
		}
		batch := int64(float64(minimum-free)*float64(records)/float64(size)*1.1) + 1
		if batch < 1000 {
			batch = 1000
		}
		if batch > 50000 {
			batch = 50000
		}
		removed, err := s.deleteOldestForSpace(ctx, batch)
		if err != nil || removed == 0 {
			return false
		}
		s.reclaimPages(ctx)
		after, err := s.availableDisk(s.dir)
		if err != nil {
			return false
		}
		if after <= free {
			break
		}
	}
	return s.diskOK()
}

func (s *Store) reclaimPages(ctx context.Context) {
	// Exhaust the incremental-vacuum result so every requested page is reclaimed.
	if rows, err := s.writer.QueryContext(ctx, "PRAGMA incremental_vacuum"); err == nil {
		for rows.Next() {
		}
		rows.Close()
	}
	_, _ = s.writer.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
}

func (s *Store) deleteOldestForSpace(ctx context.Context, limit int64) (int64, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS space_rows(kind TEXT,id INTEGER); DELETE FROM space_rows;`); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO space_rows SELECT kind,id FROM (
 SELECT 'events' kind,id,ts FROM (SELECT id,ts FROM events ORDER BY ts,id LIMIT ?)
 UNION ALL SELECT 'logs',id,ts FROM (SELECT id,ts FROM logs ORDER BY ts,id LIMIT ?)
 ) ORDER BY ts,id,kind LIMIT ?`, limit, limit, limit)
	if err != nil {
		return 0, err
	}
	coverage := s.coverage()
	var removed, systemRemoved int64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM logs WHERE id IN (SELECT id FROM space_rows WHERE kind='logs') AND category != 'firewall'").Scan(&systemRemoved); err != nil {
		return 0, err
	}
	for _, target := range []struct {
		name  string
		table *tableCoverage
	}{{"events", &coverage.events}, {"logs", &coverage.logs}} {
		r, e := tx.ExecContext(ctx, "DELETE FROM "+target.name+" WHERE id IN (SELECT id FROM space_rows WHERE kind=?)", target.name)
		if e != nil {
			return 0, e
		}
		n, _ := r.RowsAffected()
		removed += n
		target.table.records -= n
	}
	now := unixNow()
	if removed > 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE storage_state SET space_pruned=space_pruned+?,last_cleanup=? WHERE id=1", removed, now); err != nil {
			return 0, err
		}
	}
	coverage.systemLogs -= systemRemoved
	if err = coverage.bounds(ctx, tx); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	s.setCoverage(coverage)
	if removed > 0 {
		s.spacePruned.Add(removed)
		s.lastSpaceCleanup.Store(int64(now * 1e9))
	}
	return removed, nil
}
