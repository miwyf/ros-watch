package main

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const eventColumns = "ts,sender,src,sport,dst,dport,proto,flags,wan,interface,scope,outcome,raw"

type Store struct {
	writer, reader   *sql.DB
	dir              string
	mu               sync.Mutex
	settings         *atomic.Pointer[Settings]
	pruned           atomic.Int64
	availableDisk    func(string) (uint64, error)
	storageMu        sync.RWMutex
	storage          storageCoverage
	spacePruned      atomic.Int64
	lastSpaceCleanup atomic.Int64
	lastSpaceAttempt atomic.Int64
	reclaimSupported bool
}

func sqliteDSN(path, query string) string {
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	u := url.URL{Scheme: "file", Path: slash}
	return u.String() + "?" + query
}

func openStore(dir string, settings *atomic.Pointer[Settings]) (*Store, error) {
	path := filepath.Join(dir, "events.sqlite3")
	writer, err := sql.Open("sqlite", sqliteDSN(path, "_pragma=busy_timeout(3000)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(256)"))
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	_, err = writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA auto_vacuum=INCREMENTAL;
 CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY,ts REAL NOT NULL,sender TEXT NOT NULL,src TEXT NOT NULL,sport INTEGER,dst TEXT NOT NULL,dport INTEGER,proto TEXT NOT NULL,flags TEXT NOT NULL,wan TEXT NOT NULL,interface TEXT NOT NULL,scope TEXT NOT NULL,outcome TEXT NOT NULL,raw TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS events_time ON events(ts);CREATE INDEX IF NOT EXISTS events_source_time ON events(src,ts);`)
	if err != nil {
		writer.Close()
		return nil, err
	}
	if _, err = writer.Exec(logSchema); err != nil {
		writer.Close()
		return nil, err
	}
	if _, err = writer.Exec(`CREATE TABLE IF NOT EXISTS storage_state(id INTEGER PRIMARY KEY CHECK(id=1),space_pruned INTEGER NOT NULL,last_cleanup REAL NOT NULL); INSERT OR IGNORE INTO storage_state VALUES(1,0,0);`); err != nil {
		writer.Close()
		return nil, err
	}
	var vacuumMode int
	if err = writer.QueryRow("PRAGMA auto_vacuum").Scan(&vacuumMode); err != nil {
		writer.Close()
		return nil, err
	}
	if vacuumMode == 0 {
		info, statErr := os.Stat(path)
		free, freeErr := availableDisk(dir)
		if statErr == nil && freeErr == nil && free > uint64(info.Size())*2+uint64(settings.Load().MinFreeMB)*1024*1024 {
			if _, err = writer.Exec("PRAGMA auto_vacuum=INCREMENTAL; VACUUM;"); err != nil {
				writer.Close()
				return nil, err
			}
			vacuumMode = 2
		}
	}
	reader, err := sql.Open("sqlite", sqliteDSN(path, "mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(1)"))
	if err != nil {
		writer.Close()
		return nil, err
	}
	reader.SetMaxOpenConns(2)
	reader.SetMaxIdleConns(2)
	s := &Store{writer: writer, reader: reader, dir: dir, settings: settings, availableDisk: availableDisk, reclaimSupported: vacuumMode != 0}
	var count int64
	var last float64
	if err = writer.QueryRow("SELECT space_pruned,last_cleanup FROM storage_state WHERE id=1").Scan(&count, &last); err != nil {
		s.close()
		return nil, err
	}
	s.spacePruned.Store(count)
	s.lastSpaceCleanup.Store(int64(last * 1e9))
	if err = s.maintenance(context.Background()); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *Store) close() { _ = s.reader.Close(); _ = s.writer.Close() }
func (s *Store) append(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT INTO events ("+eventColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	logs, err := tx.PrepareContext(ctx, "INSERT INTO logs ("+logColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer logs.Close()
	for _, e := range events {
		if e.Log != nil {
			l := e.Log
			if _, err = logs.ExecContext(ctx, l.TS, l.Sender, l.Device, l.DeviceTime, l.Category, l.Severity, l.Username, l.Source, l.Method, l.Event, l.Message, l.Raw, l.Topic); err != nil {
				return err
			}
		}
		if e.Scope == "" {
			continue
		}
		if _, err = statement.ExecContext(ctx, e.TS, e.Sender, e.Src, e.Sport, e.Dst, e.Dport, e.Proto, e.Flags, e.WAN, e.Interface, e.Scope, e.Outcome, e.Raw); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.recordStorageAppend(events)
	return nil
}

func (s *Store) maintenance(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := s.settings.Load()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var removed int64
	for _, table := range []string{"events", "logs"} {
		result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE ts < ?", unixNow()-float64(settings.RetentionDays*86400))
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		removed += n
	}
	var coverage storageCoverage
	if err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM events)+(SELECT COUNT(*) FROM logs),
 (SELECT MIN(t) FROM (SELECT MIN(ts) t FROM events UNION ALL SELECT MIN(ts) FROM logs)),
 (SELECT MAX(t) FROM (SELECT MAX(ts) t FROM events UNION ALL SELECT MAX(ts) FROM logs))`).Scan(&coverage.Records, &coverage.oldest, &coverage.newest); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.pruned.Add(removed)
	s.storageMu.Lock()
	s.storage = coverage
	s.storageMu.Unlock()
	for _, q := range []string{"PRAGMA wal_checkpoint(TRUNCATE)", "PRAGMA incremental_vacuum(256)"} {
		if _, err = s.writer.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) diskUsage() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(filepath.Join(s.dir, "events.sqlite3"+suffix)); err == nil {
			total += info.Size()
		}
	}
	return total
}
func (s *Store) diskOK() bool {
	free, err := s.availableDisk(s.dir)
	return err == nil && free >= uint64(s.settings.Load().MinFreeMB)*1024*1024
}

type Filters struct {
	Since, Until float64
	Fields       map[string]any
}

func (f Filters) where() (string, []any) {
	terms := []string{"ts >= ?", "ts <= ?"}
	values := []any{f.Since, f.Until}
	for _, key := range []string{"wan", "proto", "scope", "outcome", "src", "dport"} {
		if value, ok := f.Fields[key]; ok {
			terms = append(terms, key+" = ?")
			values = append(values, value)
		}
	}
	return strings.Join(terms, " AND "), values
}

func (s *Store) events(ctx context.Context, f Filters, limit int, before int64) ([]Event, error) {
	where, args := f.where()
	if before > 0 {
		where += " AND id < ?"
		args = append(args, before)
	}
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, "SELECT id,"+eventColumns+" FROM events WHERE "+where+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Event, 0)
	for rows.Next() {
		e := Event{}
		var sport, dport sql.NullInt64
		if err = rows.Scan(&e.ID, &e.TS, &e.Sender, &e.Src, &sport, &e.Dst, &dport, &e.Proto, &e.Flags, &e.WAN, &e.Interface, &e.Scope, &e.Outcome, &e.Raw); err != nil {
			return nil, err
		}
		if sport.Valid {
			p := int(sport.Int64)
			e.Sport = &p
		}
		if dport.Valid {
			p := int(dport.Int64)
			e.Dport = &p
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func queryMaps(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func number(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}

func (s *Store) summary(ctx context.Context, f Filters) (map[string]any, error) {
	where, args := f.where()
	settings := s.settings.Load()
	bucket := int64(math.Max(60, math.Ceil((f.Until-f.Since)/3600)*60))
	query := func(sql string, extra ...any) ([]map[string]any, error) {
		params := append([]any{}, extra...)
		params = append(params, args...)
		return queryMaps(ctx, s.reader, sql, params...)
	}
	totals, err := query("SELECT COUNT(*) observations,COUNT(DISTINCT src) sources,COUNT(DISTINCT dport) ports,COALESCE(SUM(outcome='blocked'),0) blocked FROM events WHERE " + where)
	if err != nil {
		return nil, err
	}
	trend, err := query("SELECT CAST(ts / ? AS INTEGER) * ? t,COUNT(*) n FROM events WHERE "+where+" GROUP BY t ORDER BY t", bucket, bucket)
	if err != nil {
		return nil, err
	}
	sources, err := query("SELECT src,COUNT(*) n,COUNT(DISTINCT dport) ports,MAX(ts) last FROM events WHERE " + where + " GROUP BY src ORDER BY n DESC LIMIT 10")
	if err != nil {
		return nil, err
	}
	ports, err := query("SELECT dport,proto,COUNT(*) n FROM events WHERE " + where + " GROUP BY dport,proto ORDER BY n DESC LIMIT 8")
	if err != nil {
		return nil, err
	}
	wans, err := query("SELECT wan,COUNT(*) n FROM events WHERE " + where + " GROUP BY wan ORDER BY n DESC")
	if err != nil {
		return nil, err
	}
	scanArgs := append([]any{settings.ScanWindow}, args...)
	scanArgs = append(scanArgs, settings.ScanPorts)
	scans, err := queryMaps(ctx, s.reader, `SELECT src,MAX(ports) ports,SUM(n) n,MAX(last) last FROM (
 SELECT src,CAST(ts / ? AS INTEGER) window,COUNT(DISTINCT dport) ports,COUNT(*) n,MAX(ts) last FROM events WHERE `+where+` AND dport IS NOT NULL GROUP BY src,window HAVING ports >= ?
 ) GROUP BY src ORDER BY ports DESC,n DESC LIMIT 20`, scanArgs...)
	if err != nil {
		return nil, err
	}
	coverage, err := queryMaps(ctx, s.reader, "SELECT MIN(ts) oldest,MAX(ts) newest,COUNT(*) stored FROM events")
	if err != nil {
		return nil, err
	}
	indexed := map[int64]int64{}
	for _, row := range trend {
		indexed[int64(number(row["t"]))] = int64(number(row["n"]))
	}
	filled := make([]map[string]any, 0)
	for t := int64(f.Since/float64(bucket)) * bucket; t <= int64(f.Until/float64(bucket))*bucket; t += bucket {
		filled = append(filled, map[string]any{"t": t, "n": indexed[t]})
	}
	if len(totals) != 1 || len(coverage) != 1 {
		return nil, errors.New("invalid aggregate result")
	}
	return map[string]any{"totals": totals[0], "trend": filled, "sources": sources, "ports": ports, "wans": wans, "scans": scans, "coverage": coverage[0], "bucket_seconds": bucket}, nil
}

func boundedContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 4*time.Second)
}
