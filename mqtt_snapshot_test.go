package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMQTTClassificationsBusinessBoundaryRankingsAndEvents(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	runtime := *a.settings.Load()
	runtime.ScanPorts = 3
	a.settings.Store(&runtime)
	now := time.Unix(1800000030, 0)
	makeEvent := func(port int) Event {
		e := *sampleEvent(port)
		e.Src = "1.2.3.4"
		e.TS = float64(now.Unix() - 2)
		return e
	}
	business := makeEvent(1883)
	rule := BusinessRule{Name: "MQTT 上报", Sender: business.Sender, Src: business.Src, Dst: business.Dst, Proto: business.Proto, Dport: 1883, Scope: business.Scope, Interface: business.Interface}
	if err := a.store.saveBusiness(ctx, rule); err != nil {
		t.Fatal(err)
	}
	events := []Event{business, makeEvent(80), makeEvent(81)}
	for i := 0; i < 10; i++ {
		events = append(events, business)
	}
	response, unknown := makeEvent(49000), makeEvent(49001)
	response.Direction, unknown.Direction = trafficResponse, trafficUnknown
	events = append(events, response, unknown)
	for i := 0; i < 5; i++ {
		e := notificationLogin("failure", "1.2.3.4", float64(now.Unix()-10))
		e.Log.Raw = "PRIVATE RAW LOG"
		events = append(events, e)
	}
	events = append(events, Event{Log: &LogEvent{TS: float64(now.Unix() - 1), Device: "fixture-router", Category: "config", Event: "change", Severity: "info", Message: "PRIVATE SYSTEM MESSAGE", Raw: "PRIVATE RAW LOG"}})
	if err := a.store.append(ctx, events); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.mqttSnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int{"access_records_12h": 15, "inbound_records_12h": 13, "response_records_12h": 1, "unknown_records_12h": 1, "business_records_12h": 11, "scan_sources_12h": 0} {
		if number(snapshot.state[key]) != float64(want) {
			t.Fatalf("%s=%v want %d", key, snapshot.state[key], want)
		}
	}
	if len(snapshot.suspiciousSources) != 0 || len(snapshot.businessSources) != 1 || number(snapshot.businessSources[0]["count"]) != 11 {
		t.Fatal("business polluted suspicious rank", snapshot)
	}
	if !strings.Contains(snapshot.sources[0]["location"].(string), "澳大利亚") || snapshot.sources[0]["country_code"] != "AU" {
		t.Fatal("missing localized source", snapshot.sources)
	}
	if len(snapshot.events) != 2 || snapshot.events[0]["kind"] != "system" || snapshot.events[1]["kind"] != "login_failure" {
		t.Fatal("recent attention missing", snapshot.events)
	}
	data, _ := json.Marshal(snapshot.events)
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal("raw system or login content leaked into MQTT")
	}
	if err = a.store.append(ctx, []Event{makeEvent(82)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = a.mqttSnapshot(ctx, now)
	if err != nil || number(snapshot.state["scan_sources_12h"]) != 1 || len(snapshot.suspiciousSources) != 1 || number(snapshot.suspiciousSources[0]["ports"]) != 3 {
		t.Fatal("normal marker exempted unrelated services", snapshot, err)
	}
	for _, port := range snapshot.suspiciousPorts {
		if number(port["dport"]) == 1883 {
			t.Fatal("normal service in suspicious ports")
		}
	}
	f := Filters{Since: float64(now.Unix() - 12*3600), Until: float64(now.Unix()), Fields: map[string]any{}}
	summary, err := a.store.summary(ctx, f)
	if err != nil || number(summary["scan_count"]) != number(snapshot.state["scan_sources_12h"]) {
		t.Fatal("web and MQTT scan counts diverged", err)
	}
}

func TestMQTTScanWindowsDoNotCombineDevicesOrPortsAcrossWindows(t *testing.T) {
	a := testApp(t)
	s := *a.settings.Load()
	s.ScanPorts = 3
	a.settings.Store(&s)
	now := time.Unix(1800000330, 0)
	events := []Event{}
	for device := 0; device < 2; device++ {
		for window := 0; window < 2; window++ {
			for port := 0; port < 2; port++ {
				e := *sampleEvent(1000 + device*10 + window*2 + port)
				e.Src = "1.2.3.4"
				e.TS = float64(now.Unix() - int64(window*300))
				if device == 1 {
					e.Sender = "127.0.0.2"
				}
				events = append(events, e)
			}
		}
	}
	if err := a.store.append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.mqttSnapshot(context.Background(), now)
	if err != nil || number(snapshot.state["scan_sources_12h"]) != 0 {
		t.Fatal("ports from different device/windows combined", snapshot.state, err)
	}
}
