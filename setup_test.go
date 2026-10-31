package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func freshApp(t *testing.T) *App {
	t.Helper()
	a, err := newApp(Config{DataDir: t.TempDir(), HTTPHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	return a
}
func (b *testBrowser) setupForm(t *testing.T) string {
	t.Helper()
	code, data := b.request(t, "GET", "/setup", nil, false)
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindSubmatch(data)
	if code != 200 || match == nil {
		t.Fatal("setup form unavailable", code)
	}
	return string(match[1])
}
func (b *testBrowser) submitSetup(t *testing.T, username, password, confirm, token string) (int, []byte) {
	t.Helper()
	r, err := b.client.PostForm(b.server.URL+"/setup", url.Values{"username": {username}, "password": {password}, "confirm_password": {confirm}, "csrf": {token}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return r.StatusCode, data
}
func TestFirstVisitRequiresAccountCreation(t *testing.T) {
	a := freshApp(t)
	b := browser(t, a)
	if _, err := a.control.account(context.Background()); err != sql.ErrNoRows {
		t.Fatal("account created automatically", err)
	}
	for _, path := range []string{"/", "/login"} {
		r, err := b.client.Get(b.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 303 || r.Header.Get("Location") != "/setup" {
			t.Fatal("first visit did not reach setup", path, r.StatusCode)
		}
	}
	b.setupForm(t)
	for _, name := range []string{"initial-admin.txt"} {
		if _, err := os.Stat(filepath.Join(a.config.DataDir, name)); !os.IsNotExist(err) {
			t.Fatal("plaintext credential file exists")
		}
	}
	if code, _ := b.request(t, "GET", "/api/settings", nil, false); code != 401 {
		t.Fatal("API accessible before setup")
	}
	if a.settings.Load().AllowedSenders != "" {
		t.Fatal("collector not paused before configuration")
	}
}
func TestSetupValidatesCSRFAndCredentials(t *testing.T) {
	a := freshApp(t)
	b := browser(t, a)
	other := browser(t, a)
	token := b.setupForm(t)
	other.setupForm(t)
	for _, v := range []struct{ u, p, c, token string }{
		{"owner", "test-long-passphrase", "test-long-passphrase", ""},
		{"owner", "short", "short", token},
		{"owner", "test-long-passphrase", "another-long-passphrase", token},
		{"invalid name", "test-long-passphrase", "test-long-passphrase", token},
	} {
		code, data := b.submitSetup(t, v.u, v.p, v.c, v.token)
		if code != 400 {
			t.Fatal("invalid setup accepted", code)
		}
		if bytes.Contains(data, []byte(v.p)) {
			t.Fatal("submitted password echoed")
		}
	}
	if code, _ := other.submitSetup(t, "owner", "test-long-passphrase", "test-long-passphrase", token); code != 400 {
		t.Fatal("cross-browser CSRF accepted")
	}
	if exists, _ := a.control.initialized(context.Background()); exists {
		t.Fatal("invalid requests created account")
	}
}
func TestSetupSignsInAndCannotBeReopened(t *testing.T) {
	a := freshApp(t)
	b := browser(t, a)
	other := browser(t, a)
	token := b.setupForm(t)
	otherToken := other.setupForm(t)
	if code, _ := b.submitSetup(t, "chosen-owner", "chosen-long-passphrase", "chosen-long-passphrase", token); code != 303 {
		t.Fatal(code)
	}
	if code, data := b.request(t, "GET", "/api/account", nil, false); code != 200 || decoded(t, data)["username"] != "chosen-owner" {
		t.Fatal("setup did not sign in", code)
	}
	_, html := b.request(t, "GET", "/", nil, false)
	match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindSubmatch(html)
	if match == nil || string(match[1]) == token {
		t.Fatal("session token not rotated")
	}
	before, _ := a.control.account(context.Background())
	if code, _ := other.submitSetup(t, "attacker", "replacement-passphrase", "replacement-passphrase", otherToken); code != 303 {
		t.Fatal(code)
	}
	if code, _ := other.request(t, "GET", "/api/account", nil, false); code != 401 {
		t.Fatal("second initializer signed in")
	}
	after, _ := a.control.account(context.Background())
	if before != after {
		t.Fatal("account overwritten")
	}
	for _, path := range []string{"/setup", "/setup?reset=1"} {
		r, err := other.client.Get(other.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 303 || r.Header.Get("Location") != "/login" {
			t.Fatal("setup reopened")
		}
	}
	raw, _ := os.ReadFile(filepath.Join(a.config.DataDir, "control.sqlite3"))
	if bytes.Contains(raw, []byte("chosen-long-passphrase")) || !strings.HasPrefix(after.PasswordHash, "$argon2id$") {
		t.Fatal("plaintext password storage")
	}
}
func TestSetupPersistsAcrossStartup(t *testing.T) {
	a := freshApp(t)
	b := browser(t, a)
	token := b.setupForm(t)
	if code, _ := b.submitSetup(t, "owner", "persistent-passphrase", "persistent-passphrase", token); code != 303 {
		t.Fatal(code)
	}
	s := *a.settings.Load()
	s.ScanPorts = 23
	if err := a.control.saveSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := a.store.append(context.Background(), []Event{*sampleEvent(8291)}); err != nil {
		t.Fatal(err)
	}
	second, err := newApp(a.config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	if second.settings.Load().ScanPorts != 23 {
		t.Fatal("settings reset")
	}
	c := browser(t, second)
	if c.login(t, "owner", "persistent-passphrase") != http.StatusSeeOther {
		t.Fatal("chosen credentials lost")
	}
	if code, data := c.request(t, "GET", "/api/events", nil, false); code != 200 || len(decoded(t, data)["rows"].([]any)) != 1 {
		t.Fatal("logs lost")
	}
}
func TestConcurrentInitializersCreateOnlyOneAccount(t *testing.T) {
	a := freshApp(t)
	second, err := openControl(a.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.db.Close()
	start := make(chan struct{})
	out := make(chan bool, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for i, c := range []*Control{a.control, second} {
		wg.Add(1)
		go func(i int, c *Control) {
			defer wg.Done()
			<-start
			created, err := c.createAccount(context.Background(), []string{"first-owner", "second-owner"}[i], "concurrent-passphrase")
			out <- created
			failures <- err
		}(i, c)
	}
	close(start)
	wg.Wait()
	close(out)
	close(failures)
	n := 0
	for made := range out {
		if made {
			n++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatal("multiple initializers won", n)
	}
	var count int
	a.control.db.QueryRow("SELECT COUNT(*) FROM account").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
