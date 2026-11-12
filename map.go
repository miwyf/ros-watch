package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"
)

const mapSourceLimit = 500

type MapSettings struct {
	Name      string   `json:"name"`
	Longitude *float64 `json:"longitude"`
	Latitude  *float64 `json:"latitude"`
}

func (c *Control) loadMapSettings(ctx context.Context) (MapSettings, error) {
	s := MapSettings{Name: "我的 RouterOS"}
	var value string
	err := c.db.QueryRowContext(ctx, "SELECT value FROM map_settings WHERE id=1").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(value), &s)
	}
	return s, err
}

func (a *App) mapSettings(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	if r.Method == http.MethodGet {
		s, err := a.control.loadMapSettings(ctx)
		if err != nil {
			apiError(w, 503, "地图位置暂不可用")
			return
		}
		jsonResponse(w, 200, s)
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var s MapSettings
	if err := readJSON(r, &s); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || utf8.RuneCountInString(s.Name) > 60 || (s.Longitude == nil) != (s.Latitude == nil) {
		apiError(w, 400, "请填写位置名称及完整的经纬度")
		return
	}
	if s.Longitude != nil && (math.IsNaN(*s.Longitude) || math.IsInf(*s.Longitude, 0) || *s.Longitude < -180 || *s.Longitude > 180 || math.IsNaN(*s.Latitude) || math.IsInf(*s.Latitude, 0) || *s.Latitude < -90 || *s.Latitude > 90) {
		apiError(w, 400, "经度范围为 -180～180，纬度范围为 -90～90")
		return
	}
	value, _ := json.Marshal(s)
	if _, err := a.control.db.ExecContext(ctx, "INSERT INTO map_settings(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", string(value)); err != nil {
		apiError(w, 503, "地图位置保存失败")
		return
	}
	jsonResponse(w, 200, s)
}

func (a *App) accessMap(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	f, err := a.filters(r)
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
	where, args := f.where()
	settings := a.settings.Load()
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, mapSourceLimit+1, settings.ScanWindow)
	queryArgs = append(queryArgs, args...)
	// Keep the same per-device fixed windows as scan detection. A busy service,
	// replies, or many unrelated sources in one region must not become a scan.
	rows, err := queryMaps(ctx, a.store.reader, `WITH selected AS (
 SELECT src,COUNT(*) n,SUM(outcome='blocked') blocked FROM events WHERE `+where+` GROUP BY src ORDER BY n DESC,src LIMIT ?
), peaks AS (
 SELECT src,MAX(ports) peak_ports FROM (
  SELECT sender,src,CAST(ts / ? AS INTEGER) window,COUNT(DISTINCT dport) ports FROM events
  WHERE `+where+` AND src IN (SELECT src FROM selected) AND direction='inbound' AND dport IS NOT NULL AND NOT `+businessExistsSQL+`
  GROUP BY sender,src,window
 ) GROUP BY src
)
SELECT selected.*,COALESCE(peaks.peak_ports,0) peak_ports FROM selected LEFT JOIN peaks USING(src) ORDER BY n DESC,src`, queryArgs...)
	if err != nil {
		queryError(w)
		return
	}
	truncated := len(rows) > mapSourceLimit
	if truncated {
		rows = rows[:mapSourceLimit]
	}
	locations := a.locations(rows)
	if a.config.Demo {
		// Demo IPs are reserved documentation addresses. These locations are
		// explicitly synthetic and are never used for collected production logs.
		samples := []IPLocation{
			{CountryCode: "US", City: "New York"}, {CountryCode: "JP", City: "Tokyo"},
			{CountryCode: "DE", City: "Frankfurt"}, {CountryCode: "SG", City: "Singapore"},
			{CountryCode: "AU", City: "Sydney"}, {CountryCode: "GB", City: "London"},
			{CountryCode: "CN", City: "Beijing"}, {CountryCode: "BR", City: "São Paulo"},
		}
		for i, row := range rows {
			location := samples[i%len(samples)]
			location.Kind, location.Label = "located", "演示地区 · "+location.City
			locations[row["src"].(string)] = location
		}
	}
	jsonResponse(w, 200, map[string]any{"sources": rows, "locations": locations, "scan_ports": settings.ScanPorts, "scan_window": settings.ScanWindow, "demo": a.config.Demo, "truncated": truncated, "source_limit": mapSourceLimit, "generated_at": unixNow()})
}
