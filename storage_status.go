package main

import "database/sql"

const minimumFreeDiskBytes uint64 = 1024 * 1024 * 1024

type storageCoverage struct {
	Records        int64
	oldest, newest sql.NullFloat64
}

func (s *Store) recordStorageAppend(events []Event) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	add := func(ts float64) {
		s.storage.Records++
		if !s.storage.oldest.Valid || ts < s.storage.oldest.Float64 {
			s.storage.oldest = sql.NullFloat64{Float64: ts, Valid: true}
		}
		if !s.storage.newest.Valid || ts > s.storage.newest.Float64 {
			s.storage.newest = sql.NullFloat64{Float64: ts, Valid: true}
		}
	}
	for _, e := range events {
		if e.Log != nil {
			add(e.Log.TS)
		}
		if e.Scope != "" {
			add(e.TS)
		}
	}
}

func (s *Store) storageStatus() map[string]any {
	s.storageMu.RLock()
	coverage := s.storage
	s.storageMu.RUnlock()
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
	var oldest, newest any
	if coverage.oldest.Valid {
		oldest = coverage.oldest.Float64
	}
	if coverage.newest.Valid {
		newest = coverage.newest.Float64
	}
	return map[string]any{"database_bytes": s.diskUsage(), "disk_free_bytes": freeValue, "minimum_free_bytes": minimum, "space_state": state, "records": coverage.Records, "oldest": oldest, "newest": newest, "retention_days": s.settings.Load().RetentionDays, "disk_low_action": s.settings.Load().DiskLowAction, "space_pruned": s.spacePruned.Load(), "last_space_cleanup": nullableTime(s.lastSpaceCleanup.Load()), "last_space_attempt": nullableTime(s.lastSpaceAttempt.Load()), "space_reclaim_supported": s.reclaimSupported}
}
