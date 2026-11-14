package main

import "strings"

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
	{key: "scan_sources_6h", name: "疑似扫描来源 · 12 小时", icon: "mdi:radar", unit: "个", numeric: true},
	{key: "access_records_minute", name: "每分钟访问记录", icon: "mdi:chart-line", unit: "条", numeric: true},
	{key: "receiver_state", name: "日志接收状态", icon: "mdi:access-point-network"},
	{key: "last_received", name: "最后收到日志", deviceClass: "timestamp"},
	{key: "updated_at", name: "统计更新时间", deviceClass: "timestamp"},
	{key: "top_sources", name: "来源排行 · 12 小时", icon: "mdi:format-list-numbered", attributes: true},
	{key: "top_ports", name: "端口排行 · 12 小时", icon: "mdi:format-list-numbered", attributes: true},
	{key: "receiver_problem", name: "接收与存储异常", deviceClass: "problem", binary: true},
	{key: "inbound_records_12h", name: "外部新连接 · 12 小时", icon: "mdi:arrow-down-bold", unit: "条", numeric: true},
	{key: "response_records_12h", name: "已建立与响应 · 12 小时", icon: "mdi:swap-horizontal", unit: "条", numeric: true},
	{key: "unknown_records_12h", name: "方向未确认 · 12 小时", icon: "mdi:help-circle-outline", unit: "条", numeric: true},
	{key: "business_records_12h", name: "已标记正常业务 · 12 小时", icon: "mdi:check-network-outline", unit: "条", numeric: true},
	{key: "recent_events", name: "近期关注事件 · 12 小时", icon: "mdi:alert-circle-outline", attributes: true},
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
