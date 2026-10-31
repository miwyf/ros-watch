package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestGeoIPv4IPv6AndSpecialAddresses(t *testing.T) {
	g, err := newGeoLookup("geo")
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	for _, item := range []struct{ ip, kind string }{
		{"1.2.3.4", "located"}, {"2606:4700:4700::1111", "located"},
		{"::ffff:1.2.3.4", "located"}, {"192.168.1.1", "special"},
		{"127.0.0.1", "special"}, {"100.64.0.1", "special"}, {"169.254.1.1", "special"},
		{"203.0.113.1", "special"}, {"2001:db8::1", "special"}, {"fd00::1", "special"},
		{"0.0.0.0", "special"}, {"invalid", "unknown"},
	} {
		got := g.location(item.ip)
		if got.Kind != item.kind || got.Label == "" {
			t.Fatalf("location kind mismatch: %s", got.Kind)
		}
	}
	if got := g.location("1.2.3.4"); got.CountryCode != "AU" {
		t.Fatal("bundled IPv4 database mismatch")
	}
	if g.location("::ffff:1.2.3.4") != g.location("1.2.3.4") {
		t.Fatal("mapped IPv4 lookup differs")
	}
	got := parseGeoLocation("中国|上海|上海|电信|CN")
	if got.Label != "中国 · 上海" || got.ISP != "电信" {
		t.Fatal("duplicate location fields")
	}
	got = parseGeoLocation("0|0|0|0|0")
	if got.Kind != "unknown" || got.Label != "未知地区" {
		t.Fatal("unknown region exposed as zero")
	}
}
func TestGeoCacheBoundedAndConcurrent(t *testing.T) {
	g, err := newGeoLookup("geo")
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < geoCacheLimit+100; i += 8 {
				g.location(fmt.Sprintf("1.1.%d.%d", i/256, i%256))
			}
		}(worker)
	}
	wg.Wait()
	if len(g.cache) != geoCacheLimit || len(g.order) != geoCacheLimit {
		t.Fatal("unbounded location cache", len(g.cache))
	}
}
func TestGeoEnrichesHistoricalSummaryEventsAndExport(t *testing.T) {
	a := testApp(t)
	if a.geo == nil {
		t.Fatal("bundled geolocation database unavailable")
	}
	items := make([]Event, 16)
	for i := range items {
		items[i] = *sampleEvent(1000 + i)
		items[i].Src = "1.2.3.4"
		items[i].TS = float64(int64(unixNow()/300)*300) + .01
	}
	if err := a.store.append(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	code, data := b.request(t, "GET", "/api/summary", nil, false)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	s := decoded(t, data)
	if s["geo"].(map[string]any)["available"] != true {
		t.Fatal("geolocation not available")
	}
	locations := s["locations"].(map[string]any)
	if locations["1.2.3.4"].(map[string]any)["country_code"] != "AU" {
		t.Fatal("rank or scan locations missing")
	}
	if len(s["scans"].([]any)) != 1 {
		t.Fatal("historical scan not found")
	}
	code, data = b.request(t, "GET", "/api/events", nil, false)
	if code != 200 {
		t.Fatal(code)
	}
	rows := decoded(t, data)["rows"].([]any)
	if rows[0].(map[string]any)["src_location"].(map[string]any)["country_code"] != "AU" {
		t.Fatal("event location missing")
	}
	code, data = b.request(t, "GET", "/api/export", nil, false)
	if code != 200 || !strings.Contains(string(data), "src_location,src_isp") || !strings.Contains(string(data), "Australia") {
		t.Fatal("export location missing")
	}
}
func TestGeoUnavailableDoesNotBlockCollection(t *testing.T) {
	if _, err := newGeoLookup(t.TempDir()); err == nil {
		t.Fatal("missing database not detected")
	}
	a := testApp(t)
	a.geo.close()
	a.geo = nil
	if got := a.geo.location("1.2.3.4"); got.Kind != "unavailable" {
		t.Fatal("database unavailability hidden")
	}
	a.collector.accept([]byte(sampleEvent(8291).Raw), mustLoopback())
	a.collector.writeBatch(context.Background(), a.collector.pop())
	if a.collector.counters.written.Load() != 1 {
		t.Fatal("location failure blocked collection")
	}
}
