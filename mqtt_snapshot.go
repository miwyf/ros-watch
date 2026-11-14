package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type mqttSnapshot struct {
	state                              map[string]any
	sources, ports                     []map[string]any
	suspiciousSources, suspiciousPorts []map[string]any
	businessSources, businessPorts     []map[string]any
	events                             []map[string]any
}

// Only query published data. In particular, MQTT does not build the web trend,
// WAN distribution or full database coverage on every minute tick.
func (a *App) mqttSnapshot(ctx context.Context, now time.Time) (mqttSnapshot, error) {
	select {
	case a.expensive <- struct{}{}:
		defer func() { <-a.expensive }()
	default:
		return mqttSnapshot{}, fmt.Errorf("统计查询繁忙，等待下次发布")
	}
	snapshot, err := a.mqttStatistics(ctx, now)
	if err != nil {
		return mqttSnapshot{}, fmt.Errorf("12 小时统计暂不可用，等待下次发布")
	}
	status := a.collector.status()
	problem := status["error"] != "" || status["storage"].(map[string]any)["space_state"] != "normal"
	receiver := "等待日志"
	if last := a.collector.lastReceived.Load(); last > 0 && now.Sub(time.Unix(0, last)) < 2*time.Minute {
		receiver = "正在接收"
	}
	if problem {
		receiver = "接收或存储异常"
	}
	if a.config.Demo {
		receiver = "演示数据"
	}
	var lastReceived any
	if last := a.collector.lastReceived.Load(); last > 0 {
		lastReceived = time.Unix(0, last).UTC().Format(time.RFC3339)
	}
	snapshot.state["receiver_state"], snapshot.state["receiver_problem"] = receiver, "OFF"
	if problem {
		snapshot.state["receiver_problem"] = "ON"
	}
	snapshot.state["last_received"] = lastReceived
	snapshot.state["window_hours"], snapshot.state["updated_at"], snapshot.state["demo"] = mqttWindowHours, now.UTC().Format(time.RFC3339), a.config.Demo
	return snapshot, nil
}

func (a *App) mqttStatistics(ctx context.Context, now time.Time) (mqttSnapshot, error) {
	result := mqttSnapshot{state: map[string]any{}, events: []map[string]any{}}
	until := float64(now.UnixNano()) / 1e9
	f := Filters{Since: until - float64(mqttWindowHours*3600), Until: until, Fields: map[string]any{}}
	where, args := f.where()
	minuteEnd := now.Truncate(time.Minute).Unix()
	params := append([]any{minuteEnd - 60, minuteEnd}, args...)
	totals, err := queryMaps(ctx, a.store.reader, `SELECT COUNT(*) observations,COUNT(DISTINCT src) sources,COUNT(DISTINCT dport) ports,
 COALESCE(SUM(direction='inbound'),0) inbound,COALESCE(SUM(direction='response'),0) response,COALESCE(SUM(direction='unknown'),0) unknown,
 COALESCE(SUM(`+businessExistsSQL+`),0) business,COALESCE(SUM(ts>=? AND ts<?),0) minute FROM events WHERE `+where, params...)
	if err != nil {
		return result, err
	}
	total := totals[0]
	for _, item := range [][2]string{{"access_records_12h", "observations"}, {"unique_sources_12h", "sources"}, {"target_ports_12h", "ports"}, {"inbound_records_12h", "inbound"}, {"response_records_12h", "response"}, {"unknown_records_12h", "unknown"}, {"business_records_12h", "business"}, {"access_records_minute", "minute"}} {
		result.state[item[0]] = total[item[1]]
	}
	result.state["minute_end"] = minuteEnd
	result.state["scan_window"], result.state["scan_ports"] = a.settings.Load().ScanWindow, a.settings.Load().ScanPorts
	scanSQL, scanArgs := a.store.scanQuery(f)
	scans, err := queryMaps(ctx, a.store.reader, scanSQL+" ORDER BY last DESC,sender,src LIMIT 20", scanArgs...)
	if err != nil {
		return result, err
	}
	result.state["scan_sources_12h"] = int64(0)
	if len(scans) > 0 {
		result.state["scan_sources_12h"] = scans[0]["total"]
	}
	// Match the web's recent scan selection, merging devices by source only for display.
	suspicious := []map[string]any{}
	bySource := map[string]map[string]any{}
	for _, scan := range scans {
		src := scan["src"].(string)
		if row, ok := bySource[src]; ok {
			row["n"] = number(row["n"]) + number(scan["n"])
			if number(scan["ports"]) > number(row["ports"]) {
				row["ports"] = scan["ports"]
			}
		} else {
			row := map[string]any{"src": src, "n": scan["n"], "ports": scan["ports"], "last": scan["last"]}
			bySource[src] = row
			suspicious = append(suspicious, row)
		}
	}
	if len(suspicious) > 5 {
		suspicious = suspicious[:5]
	}
	result.suspiciousSources = a.mqttSourceRows(suspicious)
	for _, mode := range []string{"all", "business", "suspicious"} {
		filter, rankArgs := where, append([]any{}, args...)
		if mode == "business" {
			filter += " AND " + businessExistsSQL
		}
		if mode == "suspicious" {
			result.suspiciousPorts = []map[string]any{}
			if len(scans) == 0 {
				continue
			}
			terms := []string{}
			for _, scan := range scans {
				terms = append(terms, "(sender=? AND src=?)")
				rankArgs = append(rankArgs, scan["sender"], scan["src"])
			}
			filter += " AND direction='inbound' AND NOT " + businessExistsSQL + " AND (" + strings.Join(terms, " OR ") + ")"
		} else {
			rows, err := queryMaps(ctx, a.store.reader, "SELECT src,COUNT(*) n,COUNT(DISTINCT dport) ports,MAX(ts) last FROM events WHERE "+filter+" GROUP BY src ORDER BY n DESC,src LIMIT 5", rankArgs...)
			if err != nil {
				return result, err
			}
			if mode == "all" {
				result.sources = a.mqttSourceRows(rows)
			} else {
				result.businessSources = a.mqttSourceRows(rows)
			}
		}
		ports, err := queryMaps(ctx, a.store.reader, "SELECT dport,proto,COUNT(*) n FROM events WHERE "+filter+" GROUP BY dport,proto ORDER BY n DESC,dport,proto LIMIT 5", rankArgs...)
		if err != nil {
			return result, err
		}
		switch mode {
		case "all":
			result.ports = ports
		case "business":
			result.businessPorts = ports
		case "suspicious":
			result.suspiciousPorts = ports
		}
	}
	attention, system, err := a.store.homeAttention(ctx, f, scans)
	if err != nil {
		return result, err
	}
	for _, row := range attention {
		location := a.geo.location(fmt.Sprint(row["src"]))
		label := "疑似端口扫描"
		if row["kind"] == "login_failure" {
			label = "连续登录失败"
		}
		result.events = append(result.events, map[string]any{"kind": row["kind"], "label": label, "source": row["src"], "sender": row["sender"], "last": row["last"], "count": row["n"], "ports": row["ports"], "location": localizedLocation(location)})
	}
	for _, row := range system {
		result.events = append(result.events, map[string]any{"kind": "system", "label": "系统事件", "last": row["last"], "device": notificationLabel(fmt.Sprint(row["device"])), "category": row["category"], "event": row["event"], "severity": row["severity"]})
	}
	sort.SliceStable(result.events, func(i, j int) bool { return number(result.events[i]["last"]) > number(result.events[j]["last"]) })
	if len(result.events) > 8 {
		result.events = result.events[:8]
	}
	result.state["top_sources"], result.state["top_ports"], result.state["recent_events"] = "暂无", "暂无", "暂无关注事件"
	if len(result.sources) > 0 {
		result.state["top_sources"] = result.sources[0]["source"]
	}
	if len(result.ports) > 0 {
		label := "—"
		if result.ports[0]["dport"] != nil {
			label = fmt.Sprint(result.ports[0]["dport"])
		}
		result.state["top_ports"] = label + " / " + fmt.Sprint(result.ports[0]["proto"])
	}
	if len(result.events) > 0 {
		result.state["recent_events"] = result.events[0]["label"]
	}
	return result, nil
}

func (a *App) mqttSourceRows(rows []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		location := a.geo.location(row["src"].(string))
		result = append(result, map[string]any{"source": row["src"], "count": row["n"], "ports": row["ports"], "last": row["last"], "location": localizedLocation(location), "country_code": location.CountryCode, "isp": location.ISP})
	}
	return result
}
