package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const mqttWindowHours = 12

// Existing discovery IDs remain stable when the observation window changes.
func mqttStateKey(key string) string {
	if strings.HasSuffix(key, "_6h") {
		return strings.TrimSuffix(key, "_6h") + "_12h"
	}
	return key
}

type mqttEntity struct {
	key, name, icon, unit, deviceClass string
	binary, numeric, attributes        bool
}

var mqttEntities = []mqttEntity{
	{key: "access_records_6h", name: "访问记录 · 12 小时", icon: "mdi:shield-eye", unit: "条", numeric: true},
	{key: "unique_sources_6h", name: "独立来源 · 12 小时", icon: "mdi:ip-network", unit: "个", numeric: true},
	{key: "target_ports_6h", name: "目标端口 · 12 小时", icon: "mdi:ethernet", unit: "个", numeric: true},
	{key: "scan_sources_6h", name: "疑似扫描来源 · 12 小时", icon: "mdi:radar"},
	{key: "access_records_minute", name: "每分钟访问记录", icon: "mdi:chart-line", unit: "条", numeric: true},
	{key: "receiver_state", name: "日志接收状态", icon: "mdi:access-point-network"},
	{key: "last_received", name: "最后收到日志", deviceClass: "timestamp"},
	{key: "updated_at", name: "统计更新时间", deviceClass: "timestamp"},
	{key: "top_sources", name: "来源排行 · 12 小时", icon: "mdi:format-list-numbered", attributes: true},
	{key: "top_ports", name: "端口排行 · 12 小时", icon: "mdi:format-list-numbered", attributes: true},
	{key: "receiver_problem", name: "接收与存储异常", deviceClass: "problem", binary: true},
}

func mqttBase(s MQTTSettings) string { return "ros_watch/" + s.InstanceID }
func mqttObjectID(s MQTTSettings, key string) string {
	return "ros_watch_" + s.InstanceID + "_" + key
}

func mqttDiscovery(s MQTTSettings, e mqttEntity) (string, map[string]any) {
	domain := "sensor"
	if e.binary {
		domain = "binary_sensor"
	}
	id, base := mqttObjectID(s, e.key), mqttBase(s)
	config := map[string]any{
		"name": e.name, "unique_id": id, "default_entity_id": domain + "." + mqttObjectID(s, mqttStateKey(e.key)),
		"state_topic": base + "/state", "availability_topic": base + "/availability",
		"value_template": "{{ value_json." + mqttStateKey(e.key) + " }}", "expire_after": 180,
		"device": map[string]any{"identifiers": []string{"ros_watch_" + s.InstanceID}, "name": s.DeviceName, "manufacturer": "ROS Watch", "model": "RouterOS Log Observer", "sw_version": Version},
		"origin": map[string]any{"name": "ROS Watch", "sw_version": Version},
	}
	if e.icon != "" {
		config["icon"] = e.icon
	}
	if e.unit != "" {
		config["unit_of_measurement"] = e.unit
	}
	if e.numeric {
		config["state_class"] = "measurement"
	}
	if e.deviceClass != "" {
		config["device_class"] = e.deviceClass
	}
	if e.deviceClass == "timestamp" {
		config["value_template"] = "{{ value_json." + e.key + " | default(none, true) }}"
	}
	if e.attributes {
		config["json_attributes_topic"] = base + "/" + e.key
	}
	return s.DiscoveryPrefix + "/" + domain + "/" + id + "/config", config
}

type mqttSnapshot struct {
	state          map[string]any
	sources, ports []map[string]any
}

func (a *App) mqttSnapshot(ctx context.Context, now time.Time) (mqttSnapshot, error) {
	select {
	case a.expensive <- struct{}{}:
		defer func() { <-a.expensive }()
	default:
		return mqttSnapshot{}, fmt.Errorf("统计查询繁忙，等待下次发布")
	}
	until := float64(now.UnixNano()) / 1e9
	summary, err := a.store.summary(ctx, Filters{Since: until - float64(mqttWindowHours*3600), Until: until, Fields: map[string]any{}})
	if err != nil {
		return mqttSnapshot{}, fmt.Errorf("12 小时统计暂不可用，等待下次发布")
	}
	// Use a complete calendar minute, rather than a rolling total or a resettable counter.
	minuteEnd := now.Truncate(time.Minute).Unix()
	var minuteCount int64
	if err = a.store.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE ts>=? AND ts<?", minuteEnd-60, minuteEnd).Scan(&minuteCount); err != nil {
		return mqttSnapshot{}, fmt.Errorf("分钟统计暂不可用，等待下次发布")
	}
	totals := summary["totals"].(map[string]any)
	scans := summary["scans"].([]map[string]any)
	var scanCount any = len(scans)
	if len(scans) >= 20 {
		scanCount = "20+"
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
	state := map[string]any{
		"access_records_12h": totals["observations"], "unique_sources_12h": totals["sources"], "target_ports_12h": totals["ports"],
		"scan_sources_12h": scanCount, "access_records_minute": minuteCount, "minute_end": minuteEnd,
		"receiver_state": receiver, "receiver_problem": "OFF", "last_received": lastReceived,
		"window_hours": mqttWindowHours, "updated_at": now.UTC().Format(time.RFC3339), "demo": a.config.Demo, "top_sources": "暂无", "top_ports": "暂无",
	}
	if problem {
		state["receiver_problem"] = "ON"
	}
	sources := summary["sources"].([]map[string]any)
	ports := summary["ports"].([]map[string]any)
	if len(sources) > 5 {
		sources = sources[:5]
	}
	if len(ports) > 5 {
		ports = ports[:5]
	}
	ranked := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		location := a.geo.location(source["src"].(string))
		ranked = append(ranked, map[string]any{"source": source["src"], "count": source["n"], "ports": source["ports"], "location": location.Label})
	}
	if len(sources) > 0 {
		state["top_sources"] = sources[0]["src"]
	}
	if len(ports) > 0 {
		portLabel := "—"
		if ports[0]["dport"] != nil {
			portLabel = fmt.Sprint(ports[0]["dport"])
		}
		state["top_ports"] = portLabel + " / " + fmt.Sprint(ports[0]["proto"])
	}
	return mqttSnapshot{state: state, sources: ranked, ports: ports}, nil
}
