package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type aiEvidence struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type aiArguments struct {
	Hours   int      `json:"hours"`
	Country string   `json:"country"`
	Source  string   `json:"source"`
	Sources []string `json:"sources"`
}

const aiMaxHours = 168

func aiDecodeArguments(raw string) (aiArguments, error) {
	var args aiArguments
	if len(raw) > 4096 {
		return args, errors.New("查询参数过长")
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || args.Hours < 0 || args.Hours > aiMaxHours || len(args.Sources) > 8 {
		return args, errors.New("查询参数无效。每次最多查 8 个 IP，时间范围最长 168 小时")
	}
	args.Country = strings.ToUpper(strings.TrimSpace(args.Country))
	args.Source = strings.TrimSpace(args.Source)
	if args.Country != "" {
		if _, ok := geoCountryNames()[args.Country]; !ok {
			return args, errors.New("国家使用 ISO 两字母代码，例如 BG")
		}
	}
	return args, nil
}

func aiPublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, special := range specialGeoRanges {
		if special.prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func aiAddress(value string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, errors.New("请提供有效的来源 IP")
	}
	return ip.Unmap(), nil
}

func aiSourceFilter(value string) (netip.Addr, netip.Prefix, error) {
	if value == "" {
		return netip.Addr{}, netip.Prefix{}, nil
	}
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() {
			return netip.Addr{}, netip.Prefix{}, errors.New("请提供有效的 IPv4 或 IPv6 网段")
		}
		return netip.Addr{}, prefix.Masked(), nil
	}
	ip, err := aiAddress(value)
	return ip, netip.Prefix{}, err
}

func (a *App) aiRange(hours int, now float64) Filters {
	if hours == 0 {
		hours = 12
	}
	h := min(hours, a.settings.Load().RetentionDays*24)
	return Filters{Since: now - float64(h)*3600, Until: now, Hours: float64(h), Fields: map[string]any{}}
}

func aiStamp(ts float64, zone *time.Location) string {
	return time.Unix(int64(ts), 0).In(zone).Format(time.RFC3339)
}

func aiQueryScope(f Filters, c tableCoverage, zone *time.Location) map[string]any {
	scope := map[string]any{"hours": f.Hours, "from": aiStamp(f.Since, zone), "through": aiStamp(f.Until, zone)}
	if c.oldest.Valid {
		scope["oldest_retained"] = aiStamp(c.oldest.Float64, zone)
	}
	if c.newest.Valid {
		scope["newest_retained"] = aiStamp(c.newest.Float64, zone)
	}
	return scope
}

func aiLocalEvidence(kind string, hours float64, source string) aiEvidence {
	q := url.Values{}
	label := "访问记录"
	if kind == "logs" {
		label = "登录日志"
		q.Set("logs_hours", strconv.FormatFloat(hours, 'f', -1, 64))
		q.Set("logs_category", "login")
		if source != "" {
			q.Set("logs_source", source)
		}
	} else {
		q.Set("hours", strconv.FormatFloat(hours, 'f', -1, 64))
		q.Set("direction", "inbound")
		if source != "" {
			q.Set("src", source)
		}
	}
	if source != "" {
		label += " · " + source
	}
	return aiEvidence{label, "/?" + q.Encode() + "#" + kind}
}

const aiSourceDisplayLimit = 16

type aiScanSource struct {
	IP        string `json:"ip"`
	Network   string `json:"network,omitempty"`
	Country   string `json:"country,omitempty"`
	Location  string `json:"location,omitempty"`
	Windows   int    `json:"windows"`
	Records   int    `json:"records"`
	PeakPorts int    `json:"peak_ports"`
	Last      string `json:"last"`
	lastTS    float64
}

func (a *App) aiScans(ctx context.Context, args aiArguments, now float64, zone *time.Location) (any, []aiEvidence, error) {
	exact, prefix, err := aiSourceFilter(args.Source)
	if err != nil {
		return nil, nil, err
	}
	if args.Country != "" && a.geo == nil {
		return nil, nil, errors.New("IP 归属库不可用，暂时无法按国家统计")
	}
	f := a.aiRange(args.Hours, now)
	if exact.IsValid() {
		f.Fields["src"] = exact.String()
	}
	settings := a.settings.Load()
	query, params := scanWindowsQuery(f, settings)
	rows, err := a.store.reader.QueryContext(ctx, "SELECT src,window,ports,n,last FROM ("+query+")", params...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	sources := map[string]*aiScanSource{}
	daily := map[string]int{}
	networks := map[string]int{}
	total, records, peak := 0, 0, 0
	for rows.Next() {
		var source string
		var bucket int64
		var ports, n int
		var last float64
		if err := rows.Scan(&source, &bucket, &ports, &n, &last); err != nil {
			return nil, nil, err
		}
		ip, err := aiAddress(source)
		if err != nil || (prefix.IsValid() && !prefix.Contains(ip)) {
			continue
		}
		s := sources[source]
		if s == nil {
			loc := a.geo.location(source)
			if args.Country != "" && loc.CountryCode != args.Country {
				continue
			}
			s = &aiScanSource{IP: "内网或特殊地址", Country: loc.CountryCode, Location: localizedLocation(loc)}
			if aiPublicIP(ip) {
				bits := 24
				if ip.Is6() {
					bits = 48
				}
				s.IP, s.Network = ip.String(), netip.PrefixFrom(ip, bits).Masked().String()
			}
			sources[source] = s
		}
		s.Windows++
		s.Records += n
		s.PeakPorts = max(s.PeakPorts, ports)
		if last > s.lastTS {
			s.lastTS, s.Last = last, aiStamp(last, zone)
		}
		total++
		records += n
		peak = max(peak, ports)
		daily[time.Unix(bucket*int64(settings.ScanWindow), 0).In(zone).Format("2006-01-02")]++
		if s.Network != "" {
			networks[s.Network]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	ranked := make([]*aiScanSource, 0, len(sources))
	for _, source := range sources {
		ranked = append(ranked, source)
	}
	slices.SortFunc(ranked, func(a, b *aiScanSource) int {
		if a.Windows != b.Windows {
			return b.Windows - a.Windows
		}
		return strings.Compare(a.IP, b.IP)
	})
	type row struct {
		Network string `json:"network"`
		Windows int    `json:"windows"`
	}
	grouped := make([]row, 0, len(networks))
	for network, n := range networks {
		grouped = append(grouped, row{network, n})
	}
	slices.SortFunc(grouped, func(a, b row) int {
		if a.Windows != b.Windows {
			return b.Windows - a.Windows
		}
		return strings.Compare(a.Network, b.Network)
	})
	evidence := []aiEvidence{}
	for _, source := range ranked[:min(len(ranked), 5)] {
		if source.Network != "" {
			evidence = append(evidence, aiLocalEvidence("events", f.Hours, source.IP))
		}
	}
	if len(evidence) == 0 {
		evidence = append(evidence, aiLocalEvidence("events", f.Hours, ""))
	}
	return map[string]any{
		"range": aiQueryScope(f, a.store.coverage().events, zone), "country": args.Country, "qualifying_windows": total,
		"scan_source_count": len(ranked), "qualifying_window_records": records, "peak_distinct_ports": peak,
		"scan_window_seconds": settings.ScanWindow, "scan_threshold_ports": settings.ScanPorts, "sampling": settings.Sampling,
		"windows_by_day": daily, "sources": ranked[:min(len(ranked), aiSourceDisplayLimit)],
		"source_list_truncated": len(ranked) > aiSourceDisplayLimit, "networks": grouped[:min(len(grouped), aiSourceDisplayLimit)],
	}, evidence, nil
}

func (a *App) aiAccess(ctx context.Context, args aiArguments, now float64, zone *time.Location) (any, []aiEvidence, error) {
	ip, err := aiAddress(args.Source)
	if err != nil {
		return nil, nil, err
	}
	f := a.aiRange(args.Hours, now)
	f.Fields["src"] = ip.String()
	where, params := f.where()
	stats, err := queryMaps(ctx, a.store.reader, `SELECT direction,scope,outcome,COUNT(*) records
 FROM events WHERE `+where+` GROUP BY direction,scope,outcome`, params...)
	if err != nil {
		return nil, nil, err
	}
	ports, err := queryMaps(ctx, a.store.reader, `SELECT dport,proto,COUNT(*) records FROM events WHERE `+where+
		` AND dport IS NOT NULL AND direction='inbound' GROUP BY dport,proto ORDER BY records DESC,dport LIMIT 15`, params...)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"range": aiQueryScope(f, a.store.coverage().events, zone), "records_by_type": stats,
		"top_inbound_ports": ports}, []aiEvidence{aiLocalEvidence("events", f.Hours, ip.String())}, nil
}

func (a *App) aiLogins(ctx context.Context, args aiArguments, now float64, zone *time.Location) (any, []aiEvidence, error) {
	if args.Country != "" && a.geo == nil {
		return nil, nil, errors.New("IP 归属库不可用，暂时无法按国家统计")
	}
	f := a.aiRange(args.Hours, now)
	filters := []any{f.Since, f.Until}
	where := "ts>=? AND ts<=? AND category='login'"
	evidence := []aiEvidence{}
	if args.Source != "" {
		if len(args.Sources) >= 8 {
			return nil, nil, errors.New("每次最多查 8 个来源")
		}
		args.Sources = append(args.Sources, args.Source)
	}
	seen := map[netip.Addr]bool{}
	for _, item := range args.Sources {
		ip, err := aiAddress(item)
		if err != nil {
			return nil, nil, err
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		filters = append(filters, ip.String())
		evidence = append(evidence, aiLocalEvidence("logs", f.Hours, ip.String()))
	}
	if len(seen) > 0 {
		where += " AND source IN (" + strings.TrimSuffix(strings.Repeat("?,", len(seen)), ",") + ")"
	}
	rows, err := a.store.reader.QueryContext(ctx, `SELECT source,event,COUNT(*) FROM logs WHERE `+where+` GROUP BY source,event`, filters...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	totals := map[string]int{"success": 0, "failure": 0, "logout": 0}
	bySource := map[string]map[string]int{}
	unknown := 0
	for rows.Next() {
		var source, event string
		var n int
		if err := rows.Scan(&source, &event, &n); err != nil {
			return nil, nil, err
		}
		if source == "" {
			unknown += n
		}
		if args.Country != "" && a.geo.location(source).CountryCode != args.Country {
			continue
		}
		totals[event] += n
		if len(seen) == 0 {
			continue
		}
		ip, _ := aiAddress(source)
		if !aiPublicIP(ip) {
			source = "内网或特殊地址"
		}
		if bySource[source] == nil {
			bySource[source] = map[string]int{}
		}
		bySource[source][event] += n
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(evidence) == 0 {
		evidence = append(evidence, aiLocalEvidence("logs", f.Hours, ""))
	}
	coverage := a.store.coverage()
	return map[string]any{"range": aiQueryScope(f, coverage.logs, zone), "login_log_records": totals,
		"by_source": bySource, "scope_country": args.Country, "records_without_explicit_source": unknown,
		"explicit_source_filter": len(seen) > 0, "system_log_records_retained": coverage.systemLogs}, evidence, nil
}
