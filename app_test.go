package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func itoa(n int) string { return strconv.Itoa(n) }

func testApp(t *testing.T) *App {
	t.Helper()
	cfg := Config{DataDir: t.TempDir(), HTTPHost: "127.0.0.1", HTTPPort: 8080, SyslogHost: "127.0.0.1", SyslogPort: 0}
	control, err := openControl(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.AllowedSenders = "127.0.0.1"
	s.MinFreeMB = 0
	if _, err = control.createAccount(context.Background(), "admin", "test-account-password"); err != nil {
		t.Fatal(err)
	}
	if err = s.validate(); err != nil {
		t.Fatal(err)
	}
	if err = control.saveSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	control.db.Close()
	app, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.close)
	return app
}

type testBrowser struct {
	client *http.Client
	server *httptest.Server
	csrf   string
}

func browser(t *testing.T, a *App) *testBrowser {
	t.Helper()
	server := httptest.NewServer(a.handler())
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	return &testBrowser{client: &http.Client{Jar: jar, Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, server: server}
}
func (b *testBrowser) request(t *testing.T, method, path string, body any, csrf bool) (int, []byte) {
	t.Helper()
	var data io.Reader
	if body != nil {
		v, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.NewReader(v)
	}
	req, _ := http.NewRequest(method, b.server.URL+path, data)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	r, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	v, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, v
}
func (b *testBrowser) login(t *testing.T, username, password string) int {
	t.Helper()
	r, err := b.client.Get(b.server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindSubmatch(data)
	if match == nil {
		t.Fatal("missing form CSRF")
	}
	form := url.Values{"username": {username}, "password": {password}, "csrf": {string(match[1])}}
	r, err = b.client.PostForm(b.server.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode == 303 {
		_, data = b.request(t, "GET", "/", nil, false)
		match = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindSubmatch(data)
		if match == nil {
			t.Fatalf("missing authenticated CSRF: %s", data)
		}
		b.csrf = string(match[1])
	}
	return r.StatusCode
}
func decoded(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("invalid JSON: %s", body)
	}
	return v
}
func (tB *testBrowser) settings(t *testing.T) Settings {
	_, body := tB.request(t, "GET", "/api/settings", nil, false)
	var v struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v.Settings
}

func TestAccountAndSettingsPersist(t *testing.T) {
	a := testApp(t)
	known := "$argon2id$v=19$m=19456,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$IAktY94prga1Oi85mHq+w+rnzr2RjCq0Y2VyhCj1J/4"
	if !verifyPassword(known, "known-test-passphrase") || verifyPassword(known, "wrong") {
		t.Fatal("standard PHC verification failed")
	}
	account, _ := a.control.account(context.Background())
	raw, _ := os.ReadFile(filepath.Join(a.config.DataDir, "control.sqlite3"))
	if !strings.HasPrefix(account.PasswordHash, "$argon2id$") || bytes.Contains(raw, []byte("test-account-password")) {
		t.Fatal("plaintext credential storage")
	}
	second, err := openControl(a.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.db.Close()
	if made, err := second.createAccount(context.Background(), "wrong", "different-test-password"); err != nil || made {
		t.Fatal("existing account overwritten")
	}
	_, ok, err := second.authenticate(context.Background(), "admin", "test-account-password")
	if err != nil || !ok {
		t.Fatal("persisted credentials lost")
	}
}

func TestAuthCSRFAndHeaders(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/summary", nil, false); code != 401 {
		t.Fatal(code)
	}
	if code, _ := b.request(t, "POST", "/login", map[string]string{"password": "anything"}, false); code != 400 {
		t.Fatal(code)
	}
	if b.login(t, "admin", "test-account-password") != 303 {
		t.Fatal("login failed")
	}
	if code, _ := b.request(t, "POST", "/api/settings", defaultSettings(), false); code != 400 {
		t.Fatal("missing CSRF accepted")
	}
	r, err := b.client.Get(b.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.Header.Get("X-Frame-Options") != "DENY" || r.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing security headers")
	}
	if code, _ := b.request(t, "POST", "/logout", nil, true); code != 303 {
		t.Fatal(code)
	}
	if code, _ := b.request(t, "GET", "/api/account", nil, false); code != 401 {
		t.Fatal("logout failed")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	for range 5 {
		if b.login(t, "未知用户", "wrong") != 401 {
			t.Fatal("wrong authentication response")
		}
	}
	if b.login(t, "admin", "test-account-password") != 429 {
		t.Fatal("login throttle not applied")
	}
}

func TestAccountChangeInvalidatesAllSessions(t *testing.T) {
	a := testApp(t)
	b, c := browser(t, a), browser(t, a)
	b.login(t, "admin", "test-account-password")
	c.login(t, "admin", "test-account-password")
	body := map[string]string{"username": "new-owner", "current_password": "wrong", "new_password": "new-account-passphrase", "confirm_password": "new-account-passphrase"}
	if code, _ := b.request(t, "POST", "/api/account", body, true); code != 403 {
		t.Fatal("current password not required")
	}
	body["current_password"] = "test-account-password"
	body["confirm_password"] = "mismatch"
	if code, _ := b.request(t, "POST", "/api/account", body, true); code != 400 {
		t.Fatal("confirmation not enforced")
	}
	body["confirm_password"] = body["new_password"]
	if code, _ := b.request(t, "POST", "/api/account", body, true); code != 200 {
		t.Fatal("account change failed", code)
	}
	for _, client := range []*testBrowser{b, c} {
		if code, _ := client.request(t, "GET", "/api/account", nil, false); code != 401 {
			t.Fatal("old session remained valid")
		}
	}
	if b.login(t, "new-owner", "new-account-passphrase") != 303 {
		t.Fatal("new account not usable")
	}
	if err := a.control.resetPassword(context.Background(), "recovered", "recovered-password-phrase"); err != nil {
		t.Fatal(err)
	}
	if code, _ := b.request(t, "GET", "/api/account", nil, false); code != 401 {
		t.Fatal("recovery did not revoke session")
	}
	if b.login(t, "recovered", "recovered-password-phrase") != 303 {
		t.Fatal("recovery login failed")
	}
}

func TestSettingsHotUpdateAndValidation(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	s := b.settings(t)
	s.AllowedSenders = "203.0.113.1"
	s.WANMap = map[string]string{"pppoe-out1": "主线"}
	s.QueueSize = 1
	s.ScanPorts = 2
	if code, _ := b.request(t, "POST", "/api/settings", s, true); code != 200 {
		t.Fatal("settings rejected", code)
	}
	a.collector.accept([]byte(sampleEvent(80).Raw), netip.MustParseAddr("127.0.0.1"))
	a.collector.accept([]byte(sampleEvent(80).Raw), netip.MustParseAddr("203.0.113.1"))
	a.collector.accept([]byte(sampleEvent(81).Raw), netip.MustParseAddr("203.0.113.1"))
	if a.collector.counters.untrusted.Load() != 1 || a.collector.counters.queueDropped.Load() != 1 {
		t.Fatal("source/queue protection failed")
	}
	if a.collector.pop()[0].WAN != "主线" {
		t.Fatal("WAN mapping not hot-applied")
	}
	stored, err := a.control.loadSettings(context.Background())
	if err != nil || stored.QueueSize != 1 || stored.ScanPorts != 2 {
		t.Fatal("settings not persisted")
	}
	s.AllowedSenders = "0.0.0.0/0"
	if code, _ := b.request(t, "POST", "/api/settings", s, true); code != 400 {
		t.Fatal("unrestricted sources accepted")
	}
	s.AllowedSenders = ""
	if code, _ := b.request(t, "POST", "/api/settings", s, true); code != 200 {
		t.Fatal(code)
	}
	a.collector.accept([]byte(sampleEvent(80).Raw), netip.MustParseAddr("203.0.113.1"))
	if a.collector.queueLength() != 0 {
		t.Fatal("empty source list accepted traffic")
	}
}

func TestFilteredPagingSummaryAndCSV(t *testing.T) {
	a := testApp(t)
	events := make([]Event, 70)
	for i := range events {
		events[i] = *sampleEvent(i)
	}
	if err := a.store.append(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	code, data := b.request(t, "GET", "/api/events", nil, false)
	first := decoded(t, data)
	if code != 200 || len(first["rows"].([]any)) != 50 {
		t.Fatal("page size")
	}
	next := int64(first["next"].(float64))
	_, data = b.request(t, "GET", "/api/events?before="+strconv.FormatInt(next, 10), nil, false)
	if len(decoded(t, data)["rows"].([]any)) != 20 {
		t.Fatal("pagination failed")
	}
	_, data = b.request(t, "GET", "/api/summary", nil, false)
	if decoded(t, data)["totals"].(map[string]any)["observations"] != float64(70) {
		t.Fatal("wrong summary")
	}
	for _, q := range []string{"hours=-1", "hours=NaN", "dport=99999", "src=invalid", "scope=bad", "before=-1"} {
		if code, _ := b.request(t, "GET", "/api/events?"+q, nil, false); code != 400 {
			t.Fatal("invalid filter accepted", q, code)
		}
	}
	if code, data := b.request(t, "GET", "/api/export?dport=6", nil, false); code != 200 || !bytes.Contains(data, []byte("198.51.100.8")) {
		t.Fatal("CSV export failed")
	}
}

func TestRealUDPToSQLiteAndHTTP(t *testing.T) {
	if runtime.GOOS == "windows" {
		probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		sender, err := net.DialUDP("udp4", nil, probe.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = sender.Write([]byte("probe"))
		sender.Close()
		probe.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if _, _, err = probe.ReadFromUDP(make([]byte, 16)); err != nil {
			t.Skip("Windows baseline UDP loopback unavailable; Linux must pass")
		}
	}
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.collector.start(ctx, "127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); a.collector.stop() }()
	conn, err := net.DialUDP("udp4", nil, a.collector.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(sampleEvent(8291).Raw))
	until := time.Now().Add(3 * time.Second)
	for a.collector.counters.written.Load() < 1 && time.Now().Before(until) {
		time.Sleep(20 * time.Millisecond)
	}
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	_, data := b.request(t, "GET", "/api/summary", nil, false)
	if decoded(t, data)["totals"].(map[string]any)["observations"] != float64(1) {
		t.Fatal("UDP receive/write/query failed")
	}
}

func TestTemplatesRender(t *testing.T) {
	a := testApp(t)
	for _, name := range []string{"login.html", "index.html", "setup.html"} {
		var output bytes.Buffer
		if err := a.templates.ExecuteTemplate(&output, name, pageData{CSRF: "test-token", Version: Version}); err != nil {
			t.Fatal(err)
		}
	}
}
