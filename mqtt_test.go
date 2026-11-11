package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func TestMQTTSettingsSecretsAndIdentity(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/mqtt", nil, false); code != 401 {
		t.Fatal("anonymous MQTT settings access")
	}
	if code, _ := b.request(t, "GET", "/api/mqtt/dashboard", nil, false); code != 401 {
		t.Fatal("anonymous dashboard export")
	}
	b.login(t, "admin", "test-account-password")
	s := *a.mqtt.settings.Load()
	id := s.InstanceID
	s.Broker, s.Username, s.Password = "mqtt://127.0.0.1:1883", "fixture-user", "fixture-mqtt-secret"
	if code, _ := b.request(t, "POST", "/api/mqtt", s, false); code != 400 {
		t.Fatal("MQTT settings accepted without CSRF")
	}
	s.InstanceID = "attempted-identity-replacement"
	code, body := b.request(t, "POST", "/api/mqtt", s, true)
	if code != 200 || strings.Contains(string(body), "fixture-mqtt-secret") {
		t.Fatal("secret leaked or settings failed", code)
	}
	if current := a.mqtt.settings.Load(); current.InstanceID != id || current.Broker != "tcp://127.0.0.1:1883" {
		t.Fatal("identity replaced or broker not normalized")
	}
	s.Password = ""
	if code, _ = b.request(t, "POST", "/api/mqtt", s, true); code != 200 || a.mqtt.settings.Load().Password != "fixture-mqtt-secret" {
		t.Fatal("blank password did not preserve secret")
	}
	loaded, err := a.control.loadMQTT(context.Background())
	if err != nil || loaded.InstanceID != id || loaded.Password != "fixture-mqtt-secret" {
		t.Fatal("MQTT persistence failed", err)
	}
	code, body = b.request(t, "GET", "/api/mqtt", nil, false)
	if code != 200 || strings.Contains(string(body), "fixture-mqtt-secret") || !strings.Contains(string(body), `"password_set":true`) {
		t.Fatal("MQTT secret exposed by GET")
	}
	data, _ := json.Marshal(s)
	var clear map[string]any
	json.Unmarshal(data, &clear)
	clear["clear_password"] = true
	if code, _ = b.request(t, "POST", "/api/mqtt", clear, true); code != 200 || a.mqtt.settings.Load().Password != "" {
		t.Fatal("explicit secret removal failed")
	}
	if code, _ = b.request(t, "POST", "/api/mqtt/publish", nil, false); code != 400 {
		t.Fatal("publishing accepted without CSRF")
	}
	if code, _ = b.request(t, "POST", "/api/mqtt/publish", nil, true); code != 502 {
		t.Fatal("disabled publisher succeeded")
	}
	code, body = b.request(t, "GET", "/api/mqtt/dashboard", nil, false)
	if code != 200 || !strings.Contains(string(body), "sensor.ros_watch_"+id+"_access_records_12h") || !strings.Contains(string(body), "custom:button-card") || !strings.Contains(string(body), "graph_span: 12h") || strings.Contains(string(body), "fixture-mqtt-secret") {
		t.Fatal("dashboard export failed or leaked secret")
	}
	code, body = b.request(t, "GET", "/api/mqtt/dashboard?style=basic", nil, false)
	if code != 200 || !strings.Contains(string(body), "type: history-graph") || strings.Contains(string(body), "custom:button-card") || strings.Contains(string(body), "_6h") {
		t.Fatal("basic dashboard export is not compatible with 12-hour entity IDs")
	}
}

func TestMQTTValidationAndDiscovery(t *testing.T) {
	base := MQTTSettings{DeviceName: "观察台", DiscoveryPrefix: "homeassistant", InstanceID: "0123456789abcdef"}
	for _, broker := range []string{"http://example.com:80", "tcp://user:password@example.com:1883", "tcp://example.com", "tcp://example.com:0", "ssl://example.com:8883/path", "tcp://example.com:1883?secret=1"} {
		s := base
		s.Broker = broker
		if err := s.validate(); err == nil {
			t.Fatal("invalid MQTT broker accepted")
		}
	}
	for _, prefix := range []string{"", "home/#", "home/+", "home\nassistant"} {
		s := base
		s.DiscoveryPrefix = prefix
		if err := s.validate(); err == nil {
			t.Fatal("invalid discovery prefix accepted")
		}
	}
	seen := map[string]bool{}
	for _, e := range mqttEntities {
		topic, config := mqttDiscovery(base, e)
		if seen[topic] || !strings.HasSuffix(topic, "/config") || config["expire_after"] != 180 {
			t.Fatal("unstable or duplicated discovery identity")
		}
		seen[topic] = true
		if config["state_class"] == "total_increasing" {
			t.Fatal("rolling count incorrectly marked cumulative")
		}
		if e.key == "scan_sources_6h" && config["state_class"] != nil {
			t.Fatal("capped scan count must support text 20+")
		}
	}
	if len(seen) != 11 {
		t.Fatal("wrong entity count")
	}
	_, discovery := mqttDiscovery(base, mqttEntities[0])
	if discovery["unique_id"] != mqttObjectID(base, "access_records_6h") || discovery["default_entity_id"] != "sensor."+mqttObjectID(base, "access_records_12h") || discovery["value_template"] != "{{ value_json.access_records_12h }}" || base.public()["hours"] != 12 {
		t.Fatal("12-hour window changed entity identity or retained old payload mapping")
	}
	dashboard := mqttDashboardYAML(base)
	for _, e := range mqttEntities {
		if !strings.Contains(dashboard, mqttObjectID(base, mqttStateKey(e.key))) {
			t.Fatal("dashboard missing entity", e.key)
		}
	}
}

func TestMQTTSnapshotWindowAndFailure(t *testing.T) {
	a := testApp(t)
	now := time.Now().Truncate(time.Minute).Add(20 * time.Second)
	minute := now.Truncate(time.Minute).Unix()
	since := float64(now.Unix() - 12*3600)
	times := []float64{float64(minute - 60), float64(minute) - 0.001, float64(minute), float64(now.Unix() - 7*3600), since, since - 0.001}
	for i, ts := range times {
		e := *sampleEvent(i)
		e.TS = ts
		if err := a.store.append(context.Background(), []Event{e}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := a.mqttSnapshot(context.Background(), now)
	if err != nil || number(snapshot.state["access_records_12h"]) != 5 || snapshot.state["access_records_minute"] != int64(2) || snapshot.state["window_hours"] != 12 {
		t.Fatal("wrong 12h/minute boundaries", snapshot.state, err)
	}
	if snapshot.state["receiver_state"] != "等待日志" || snapshot.state["last_received"] != nil || snapshot.state["receiver_problem"] != "OFF" {
		t.Fatal("empty receive state was fabricated")
	}
	a.collector.setError("storage_failed")
	snapshot, err = a.mqttSnapshot(context.Background(), now)
	if err != nil || snapshot.state["receiver_problem"] != "ON" {
		t.Fatal("storage failure was hidden")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = a.mqttSnapshot(ctx, now); err == nil {
		t.Fatal("cancelled statistics fabricated successful data")
	}
	a.expensive <- struct{}{}
	a.expensive <- struct{}{}
	if _, err = a.mqttSnapshot(context.Background(), now); err == nil {
		t.Fatal("publisher ignored query concurrency limit")
	}
	<-a.expensive
	<-a.expensive
}

func TestMQTTSnapshotCappedScansAndPortlessTraffic(t *testing.T) {
	a := testApp(t)
	settings := *a.settings.Load()
	settings.ScanPorts = 2
	a.settings.Store(&settings)
	now := time.Now().Truncate(5 * time.Minute).Add(30 * time.Second)
	var events []Event
	for source := 1; source <= 21; source++ {
		for _, port := range []int{80, 443} {
			e := *sampleEvent(port)
			e.Src, e.TS = fmt.Sprintf("198.51.100.%d", source), float64(now.Unix()-1)
			events = append(events, e)
		}
	}
	for i := 0; i < 30; i++ {
		e := *sampleEvent(0)
		e.Proto, e.Dport, e.TS = "ICMP", nil, float64(now.Unix()-1)
		events = append(events, e)
	}
	if err := a.store.append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.mqttSnapshot(context.Background(), now)
	if err != nil || snapshot.state["scan_sources_12h"] != "20+" || snapshot.state["top_ports"] != "— / ICMP" {
		t.Fatal("capped scan count or portless ranking misleading", snapshot.state, err)
	}
}

// Set ROS_WATCH_MQTT_TEST_BROKER to run against an isolated real broker.
func TestMQTTBrokerLifecycle(t *testing.T) {
	broker := os.Getenv("ROS_WATCH_MQTT_TEST_BROKER")
	if broker == "" {
		t.Skip("real MQTT broker not configured")
	}
	a := testApp(t)
	s := *a.mqtt.settings.Load()
	s.Enabled, s.Broker = true, broker
	base := mqttBase(s)
	type observed struct{ topic, payload string }
	messages := make(chan observed, 256)
	monitor := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).SetClientID("ros-watch-test-" + s.InstanceID))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := mqttWait(ctx, monitor.Connect(), "monitor connect failed"); err != nil {
		t.Fatal(err)
	}
	defer monitor.Disconnect(100)
	if err := mqttWait(ctx, monitor.Subscribe("#", 1, func(_ mqtt.Client, m mqtt.Message) {
		select {
		case messages <- observed{m.Topic(), string(m.Payload())}:
		default:
		}
	}), "monitor subscribe failed"); err != nil {
		t.Fatal(err)
	}
	wait := func(topic, payload string) observed {
		t.Helper()
		for {
			select {
			case m := <-messages:
				if m.topic == topic && (payload == "" || m.payload == payload) {
					return m
				}
			case <-ctx.Done():
				t.Fatalf("missing MQTT message %s", topic)
			}
		}
	}
	a.mqtt.configure(s)
	topic, _ := mqttDiscovery(s, mqttEntities[0])
	message := wait(topic, "")
	if !strings.Contains(message.payload, mqttObjectID(s, mqttEntities[0].key)) {
		t.Fatal("bad discovery identity")
	}
	wait(base+"/state", "")
	wait(base+"/availability", "online")
	if err := a.mqtt.publishNow(ctx); err != nil {
		t.Fatal(err)
	}
	// Drain previous publication before asking HA birth to trigger rediscovery.
	for len(messages) > 0 {
		<-messages
	}
	if err := mqttWait(ctx, monitor.Publish(s.DiscoveryPrefix+"/status", 1, false, "online"), "birth failed"); err != nil {
		t.Fatal(err)
	}
	wait(topic, "")
	wait(base+"/availability", "online")
	// Query failure marks stale data unavailable; a later successful round recovers.
	a.expensive <- struct{}{}
	a.expensive <- struct{}{}
	if err := a.mqtt.publishNow(ctx); err == nil {
		t.Fatal("busy statistics published as successful")
	}
	wait(base+"/availability", "offline")
	<-a.expensive
	<-a.expensive
	if err := a.mqtt.publishNow(ctx); err != nil {
		t.Fatal(err)
	}
	wait(base+"/availability", "online")
	// A duplicate client ID forces a genuine transport disconnect and broker LWT.
	replacement := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).SetClientID("ros-watch-" + s.InstanceID).SetAutoReconnect(false))
	if err := mqttWait(ctx, replacement.Connect(), "replacement connect failed"); err != nil {
		t.Fatal(err)
	}
	defer replacement.Disconnect(100)
	wait(base+"/availability", "offline")
	wait(base+"/availability", "online")
	// A fresh subscriber must receive retained discovery and state.
	if err := mqttWait(ctx, monitor.Unsubscribe(base+"/state"), "unsubscribe failed"); err != nil {
		t.Fatal(err)
	}
	if err := mqttWait(ctx, monitor.Subscribe(base+"/state", 1, func(_ mqtt.Client, m mqtt.Message) {
		if m.Retained() {
			messages <- observed{m.Topic(), "retained"}
		}
	}), "resubscribe failed"); err != nil {
		t.Fatal(err)
	}
	wait(base+"/state", "retained")
	s.Enabled = false
	a.mqtt.configure(s)
	wait(base+"/availability", "offline")
	if current := a.mqtt.settings.Load(); current.Enabled {
		t.Fatal("disable did not apply")
	}
	t.Log(fmt.Sprintf("real broker verified %d discovery entities, retained state, HA birth, LWT/reconnect, query failure/recovery and disable", len(mqttEntities)))
}
