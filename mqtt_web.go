package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (a *App) mqttSettings(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		jsonResponse(w, 200, map[string]any{"settings": a.mqtt.settings.Load().public(), "status": a.mqtt.status()})
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body struct {
		MQTTSettings
		ClearPassword bool `json:"clear_password"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	current := a.mqtt.settings.Load()
	s := body.MQTTSettings
	s.InstanceID = current.InstanceID
	if s.Password == "" && !body.ClearPassword {
		s.Password = current.Password
	}
	if body.ClearPassword {
		s.Password = ""
	}
	if err := s.validate(); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	if err := a.control.saveMQTT(ctx, s); err != nil {
		apiError(w, 503, "MQTT 设置保存失败")
		return
	}
	a.mqtt.configure(s)
	jsonResponse(w, 200, map[string]any{"message": "MQTT 设置已保存，连接与发布在后台进行", "settings": s.public()})
}

func (a *App) publishMQTT(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := a.mqtt.publishNow(ctx); err != nil {
		apiError(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]string{"message": "Broker 已确认接收发现配置和数据，请在 HA 的 MQTT 设备中查看"})
}

func (a *App) mqttDashboard(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	s := *a.mqtt.settings.Load()
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ros-watch-dashboard.yaml"`)
	if r.URL.Query().Get("style") == "basic" {
		_, _ = w.Write([]byte(mqttBasicDashboardYAML(s)))
		return
	}
	_, _ = w.Write([]byte(mqttDashboardYAML(s)))
}

func mqttDashboardYAML(s MQTTSettings) string {
	// This file is embedded at build time and is also the maintained style example.
	data, _ := assets.ReadFile("docs/home-assistant-observatory.yaml")
	return strings.ReplaceAll(string(data), "INSTANCE", s.InstanceID)
}

func mqttBasicDashboardYAML(s MQTTSettings) string {
	// Entity identifiers are deterministic and never include a user-supplied device name.
	return strings.ReplaceAll(`title: 网络边界观察
views:
  - title: 网络边界
    path: ros-watch
    icon: mdi:shield-eye
    cards:
      - type: markdown
        content: |
          # 网络边界观察
          最近 12 小时 · 每分钟更新 · {{ states('sensor.INSTANCE_receiver_state') }}
          {% if is_state('sensor.INSTANCE_receiver_state', '演示数据') %}
          **演示数据 · 与实际网络无关**
          {% endif %}
          全部访问记录包含外部新连接、响应通信及方向未确认记录，不等于成功攻击或已封禁。
      - type: grid
        columns: 2
        square: false
        cards:
          - type: tile
            entity: sensor.INSTANCE_access_records_12h
            name: 访问记录 · 12 小时
          - type: tile
            entity: sensor.INSTANCE_unique_sources_12h
            name: 独立来源 · 12 小时
          - type: tile
            entity: sensor.INSTANCE_target_ports_12h
            name: 目标端口 · 12 小时
          - type: tile
            entity: sensor.INSTANCE_scan_sources_12h
            name: 疑似扫描来源 · 12 小时
            color: amber
      - type: entities
        title: 访问分类 · 12 小时
        entities:
          - entity: sensor.INSTANCE_inbound_records_12h
            name: 外部新连接
          - entity: sensor.INSTANCE_response_records_12h
            name: 已建立 / 响应
          - entity: sensor.INSTANCE_unknown_records_12h
            name: 方向未确认
          - entity: sensor.INSTANCE_business_records_12h
            name: 已标记正常业务（与方向分类重叠）
      - type: history-graph
        title: 每分钟新增访问记录
        hours_to_show: 12
        entities:
          - entity: sensor.INSTANCE_access_records_minute
            name: 上一分钟记录数
      - type: markdown
        title: 来源观察 · 12 小时
        content: |
          {% for key, title in [('suspicious_items','近期可疑来源'),('business_items','正常业务来源'),('items','全部访问排行')] %}
          ### {{ title }}
          {% for item in state_attr('sensor.INSTANCE_top_sources', key) or [] %}
          **{{ loop.index }}. {{ item.source }}** · {{ item.location }} · {{ item.count }} 条

          {% else %}暂无排行{% endfor %}
          {% endfor %}
      - type: markdown
        title: 目标端口观察 · 12 小时
        content: |
          {% for key, title in [('suspicious_items','近期可疑来源的未标记服务'),('business_items','正常业务服务'),('items','全部目标端口排行')] %}
          ### {{ title }}
          {% for item in state_attr('sensor.INSTANCE_top_ports', key) or [] %}
          **{{ loop.index }}. {{ item.dport if item.dport is not none else '—' }} / {{ item.proto }}** · {{ item.n }} 条

          {% else %}暂无排行{% endfor %}
          {% endfor %}
      - type: markdown
        title: 近期关注事件
        content: |
          {% if states('sensor.INSTANCE_recent_events') in ['unknown','unavailable'] %}
          等待事件数据同步
          {% else %}
          {% for item in state_attr('sensor.INSTANCE_recent_events', 'items') or [] %}
          - {{ item.last | timestamp_custom('%m-%d %H:%M:%S') }} · **{{ item.label }}** · {{ item.source | default(item.device, true) }} {{ item.location | default('') }}
          {% else %}当前范围暂无关注事件{% endfor %}
          {% endif %}
      - type: entities
        title: 接收与数据状态
        entities:
          - entity: sensor.INSTANCE_receiver_state
            name: 日志接收状态
          - entity: binary_sensor.INSTANCE_receiver_problem
            name: 接收与存储异常
          - entity: sensor.INSTANCE_last_received
            name: 最后收到日志
          - entity: sensor.INSTANCE_updated_at
            name: 统计更新时间
`, "INSTANCE", "ros_watch_"+s.InstanceID)
}
