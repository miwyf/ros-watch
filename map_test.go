package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestMapSettingsAuthorizationValidationAndPersistence(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/map", nil, false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated map: %d", code)
	}
	b.login(t, "admin", "test-account-password")
	value := map[string]any{"name": "测试城市", "longitude": 114.06, "latitude": 22.55}
	if code, _ := b.request(t, "POST", "/api/map/settings", value, false); code != 400 {
		t.Fatalf("missing CSRF: %d", code)
	}
	for _, invalid := range []map[string]any{
		{"name": "", "longitude": 0, "latitude": 0},
		{"name": "x", "longitude": 181, "latitude": 0},
		{"name": "x", "longitude": 0, "latitude": -91},
		{"name": "x", "longitude": nil, "latitude": 0},
		{"name": "x", "longitude": "114", "latitude": 22},
	} {
		if code, _ := b.request(t, "POST", "/api/map/settings", invalid, true); code != 400 {
			t.Fatalf("invalid coordinates accepted: %v, status %d", invalid, code)
		}
	}
	if code, body := b.request(t, "POST", "/api/map/settings", value, true); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	// Read through a new control connection, as after application restart.
	other, err := openControl(a.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.db.Close()
	s, err := other.loadMapSettings(context.Background())
	if err != nil || s.Name != "测试城市" || s.Longitude == nil || *s.Longitude != 114.06 || s.Latitude == nil || *s.Latitude != 22.55 {
		t.Fatalf("persisted settings: %+v, %v", s, err)
	}
	value["longitude"], value["latitude"] = 0, 0
	if code, _ := b.request(t, "POST", "/api/map/settings", value, true); code != 200 {
		t.Fatal("zero coordinates must be valid")
	}
	value["longitude"], value["latitude"] = nil, nil
	if code, _ := b.request(t, "POST", "/api/map/settings", value, true); code != 200 {
		t.Fatal("clear failed")
	}
	s, err = other.loadMapSettings(context.Background())
	if err != nil || s.Longitude != nil || s.Latitude != nil {
		t.Fatalf("clear: %+v %v", s, err)
	}
}

func TestAccessMapUsesAllSourcesFiltersAndExplicitLimit(t *testing.T) {
	a := testApp(t)
	now := unixNow()
	tx, err := a.store.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < mapSourceLimit+3; i++ {
		src := fmt.Sprintf("8.8.%d.%d", i/256, i%256)
		if _, err := tx.Exec("INSERT INTO events(ts,sender,src,dst,proto,flags,wan,interface,scope,outcome,raw) VALUES(?,'demo',?,'192.0.2.1','TCP','','WAN1','eth1','router','attempt','demo')", now-1, src); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := tx.Exec("INSERT INTO events(ts,sender,src,dst,proto,flags,wan,interface,scope,outcome,raw) VALUES(?,'demo','1.1.1.1','192.0.2.1','UDP','','WAN2','eth2','forward','blocked','demo')", now-1); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	var result struct {
		Sources []struct {
			Src        string `json:"src"`
			N, Blocked int
		}
		Locations   map[string]IPLocation
		Truncated   bool
		SourceLimit int `json:"source_limit"`
	}
	code, body := b.request(t, "GET", "/api/map", nil, false)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if code != 200 || len(result.Sources) != mapSourceLimit || !result.Truncated || result.SourceLimit != mapSourceLimit || len(result.Locations) != mapSourceLimit {
		t.Fatalf("unfiltered map: status=%d rows=%d locations=%d truncated=%v", code, len(result.Sources), len(result.Locations), result.Truncated)
	}
	if result.Sources[0].Src != "1.1.1.1" || result.Sources[0].N != 3 || result.Sources[0].Blocked != 3 {
		t.Fatalf("aggregation: %+v", result.Sources[0])
	}
	code, body = b.request(t, "GET", "/api/map?wan=WAN2&proto=UDP&scope=forward&outcome=blocked", nil, false)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if code != 200 || len(result.Sources) != 1 || result.Truncated || result.Sources[0].N != 3 {
		t.Fatalf("filtered map: %d %s", code, body)
	}
	if code, _ := b.request(t, "GET", "/api/map?hours=NaN", nil, false); code != 400 {
		t.Fatal("invalid filter accepted")
	}
}
