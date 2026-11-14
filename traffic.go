package main

import (
	"database/sql"
	"regexp"
	"strings"
)

const (
	trafficInbound  = "inbound"
	trafficResponse = "response"
	trafficUnknown  = "unknown"
)

var connectionStatePattern = regexp.MustCompile(`(?i)\bconnection-state[:=]\s*([a-z,-]+)`)

// Packet direction is separate from whether the activity is suspicious.
func classifyTraffic(e Event) string {
	state := connectionStatePattern.FindStringSubmatch(e.Raw)
	if len(state) > 1 {
		for _, value := range strings.Split(strings.ToLower(state[1]), ",") {
			if value == "established" || value == "related" {
				return trafficResponse
			}
			if value == "invalid" || value == "untracked" {
				return trafficUnknown
			}
		}
	}
	if e.Proto == "TCP" {
		syn := false
		for _, flag := range strings.Split(strings.ToUpper(e.Flags), ",") {
			switch strings.TrimSpace(flag) {
			case "ACK", "FIN", "RST", "PSH":
				return trafficResponse
			case "SYN":
				syn = true
			}
		}
		if syn && e.Interface != "" {
			return trafficInbound
		}
	}
	// Dedicated observation prefixes have a new-connection contract. Generic
	// WAN logs without state or TCP initiation evidence remain unconfirmed.
	prefix := prefixPattern.FindString(e.Raw)
	dedicated := (prefix == "RW-INPUT" && e.Scope == "router" || prefix == "RW-FWD" && e.Scope == "forward") && (e.Proto == "TCP" || e.Proto == "UDP")
	if dedicated || len(state) > 1 && strings.Contains(","+strings.ToLower(state[1])+",", ",new,") && e.Interface != "" {
		return trafficInbound
	}
	return trafficUnknown
}

func scanEligible(e Event) bool {
	direction := e.Direction
	if direction == "" {
		direction = classifyTraffic(e)
	}
	return direction == trafficInbound && e.Business == ""
}

// All scan summaries use per-device fixed windows and exclude exact business matches.
func (s *Store) scanQuery(f Filters) (string, []any) {
	where, args := f.where()
	settings := s.settings.Load()
	params := append([]any{settings.ScanWindow}, args...)
	params = append(params, settings.ScanPorts)
	return `SELECT sender,src,MAX(ports) ports,SUM(n) n,MIN(first) first,MAX(last) last,COUNT(*) OVER() total FROM (
 SELECT sender,src,CAST(ts / ? AS INTEGER) window,COUNT(DISTINCT dport) ports,COUNT(*) n,MIN(ts) first,MAX(ts) last FROM events WHERE ` + where + ` AND direction='inbound' AND dport IS NOT NULL AND NOT ` + businessExistsSQL + ` GROUP BY sender,src,window HAVING ports >= ?
 ) GROUP BY sender,src`, params
}

// Upgrade existing records once, without discarding raw history. Batches bound
// memory while the transaction keeps the schema and classification atomic.
func migrateTraffic(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(events)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var id, notNull, primary int
		var name, kind string
		var def any
		if err = rows.Scan(&id, &name, &kind, &notNull, &def, &primary); err != nil {
			rows.Close()
			return err
		}
		found = found || name == "direction"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("ALTER TABLE events ADD COLUMN direction TEXT NOT NULL DEFAULT 'unknown'"); err != nil {
		return err
	}
	var after int64
	for {
		rows, err := tx.Query("SELECT id,raw,flags,proto,interface,scope FROM events WHERE id>? ORDER BY id LIMIT 500", after)
		if err != nil {
			return err
		}
		type oldEvent struct {
			id    int64
			event Event
		}
		batch := make([]oldEvent, 0, 500)
		for rows.Next() {
			var item oldEvent
			if err = rows.Scan(&item.id, &item.event.Raw, &item.event.Flags, &item.event.Proto, &item.event.Interface, &item.event.Scope); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if _, err = tx.Exec("UPDATE events SET direction=? WHERE id=?", classifyTraffic(item.event), item.id); err != nil {
				return err
			}
			after = item.id
		}
	}
	return tx.Commit()
}
