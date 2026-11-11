package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureNotification(platform string) NotificationSettings {
	s := defaultNotifications()
	s.Platform = platform
	s.Webhook = map[string]string{"feishu": "https://open.feishu.cn/open-apis/bot/v2/hook/fixture-token", "wecom": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fixture-token", "dingtalk": "https://oapi.dingtalk.com/robot/send?access_token=fixture-token"}[platform]
	return s
}

func TestNotificationPlatformPayloadAndSignature(t *testing.T) {
	for _, platform := range []string{"feishu", "wecom", "dingtalk"} {
		t.Run(platform, func(t *testing.T) {
			s := fixtureNotification(platform)
			if platform != "wecom" {
				s.Secret = "fixture-secret"
			}
			client := &http.Client{Transport: notificationTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("wrong request method or content type")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if platform == "feishu" {
					if body["msg_type"] != "text" || body["content"].(map[string]any)["text"] != "告警测试" || body["timestamp"] != "1700000000" || body["sign"] != "eSSBpPPdWfBl7avPl9BWSBfQOnLGZ91Xn8G5oZis0sc=" {
						t.Fatal("invalid Feishu payload or independent signature vector")
					}
				} else {
					if body["msgtype"] != "text" || body["text"].(map[string]any)["content"] != "告警测试" {
						t.Fatal("invalid text payload")
					}
					if platform == "dingtalk" && (r.URL.Query().Get("timestamp") != "1700000000000" || r.URL.Query().Get("sign") != "gIUqOEU7dEruH5nj2xv88HPqxCLcD6EYqLsd6yP4eoA=") {
						t.Fatal("invalid DingTalk signature vector")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0,"errcode":0}`)), Header: http.Header{}}, nil
			})}
			if err := sendNotification(context.Background(), client, s, "告警测试", time.Unix(1700000000, 0)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNotificationRejectsUnsafeURLsAndBadResponses(t *testing.T) {
	for _, endpoint := range []string{"http://open.feishu.cn/open-apis/bot/v2/hook/test", "https://127.0.0.1/test", "https://open.feishu.cn.attacker.invalid/open-apis/bot/v2/hook/test", "https://name:pass@open.feishu.cn/open-apis/bot/v2/hook/test", "https://open.feishu.cn:444/open-apis/bot/v2/hook/test", "https://open.feishu.cn/open-apis/bot/v2/hook/test?x=y", "https://open.feishu.cn/open-apis/bot/v2/hook/test#fragment"} {
		s := fixtureNotification("feishu")
		s.Webhook = endpoint
		if s.validate() == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	for _, body := range []string{`{"errcode":93000}`, `{"errcode":null}`, `{"errcode":"0"}`, `{"ok":true}`, `<html>failure</html>`} {
		client := &http.Client{Transport: notificationTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		if sendNotification(context.Background(), client, fixtureNotification("wecom"), "test", time.Now()) == nil {
			t.Fatal("platform error treated as success")
		}
	}
	client := &http.Client{Transport: notificationTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network failure fixture-token fixture-secret")
	})}
	if err := sendNotification(context.Background(), client, fixtureNotification("feishu"), "test", time.Now()); err == nil || strings.Contains(err.Error(), "fixture") {
		t.Fatal("transport credential leaked")
	}
	if storageNotification("storage_failed", "") == "" || storageNotification("low_space", "low_space") != "" {
		t.Fatal("storage transition notifications invalid")
	}
}

func TestNotificationSettingsAuthenticationPersistenceAndSecretMask(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/notifications", nil, false); code == 200 {
		t.Fatal("anonymous notification access")
	}
	b.login(t, "admin", "test-account-password")
	s := fixtureNotification("feishu")
	s.Secret = "fixture-secret"
	if code, _ := b.request(t, "POST", "/api/notifications", s, false); code != 400 {
		t.Fatal("missing CSRF accepted")
	}
	if code, body := b.request(t, "POST", "/api/notifications", s, true); code != 200 || strings.Contains(string(body), "fixture") {
		t.Fatalf("save or mask failed: %d", code)
	}
	if code, body := b.request(t, "GET", "/api/notifications", nil, false); code != 200 || strings.Contains(string(body), "fixture") || !strings.Contains(string(body), `"secret_set":true`) {
		t.Fatal("credentials exposed or lost")
	}
	s.Webhook, s.Secret = "", ""
	if code, _ := b.request(t, "POST", "/api/notifications", s, true); code != 200 {
		t.Fatal("keep saved credentials failed")
	}
	stored, err := a.control.loadNotifications(context.Background())
	if err != nil || stored.Webhook == "" || stored.Secret != "fixture-secret" {
		t.Fatal("notification persistence failed")
	}
	runtime, err := a.control.loadSettings(context.Background())
	if err != nil || runtime.AllowedSenders != a.settings.Load().AllowedSenders {
		t.Fatal("runtime settings changed")
	}
	s.Enabled, s.LoginSuccess = true, true
	if code, _ := b.request(t, "POST", "/api/notifications", s, true); code != 400 {
		t.Fatal("untrusted success enabled without trusted networks")
	}
	if code, _ := b.request(t, "POST", "/api/notifications/test", map[string]any{}, false); code != 400 {
		t.Fatal("test send missing CSRF accepted")
	}
}

func notificationLogin(event, source string, ts float64) Event {
	return Event{Log: &LogEvent{TS: ts, Sender: "192.0.2.1", Device: "fixture-router", Category: "login", Event: event, Username: "fixture-user", Source: source, Method: "winbox"}}
}

func TestNotificationThresholdsCooldownWindowsAndTrustedSources(t *testing.T) {
	s := fixtureNotification("feishu")
	s.Enabled, s.LoginSuccess, s.Scans = true, true, true
	s.TrustedAdmins = "192.0.2.0/24"
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	runtime := defaultSettings()
	runtime.ScanPorts = 3
	d := notificationDetector{}
	now := time.Unix(1800000000, 0)
	failure := notificationLogin("failure", "198.51.100.20", float64(now.Unix()))
	for i := 0; i < 4; i++ {
		if messages, _, _ := d.process(&s, &runtime, []Event{failure}, now); len(messages) != 0 {
			t.Fatal("premature failure notification")
		}
	}
	if messages, _, _ := d.process(&s, &runtime, []Event{failure}, now); len(messages) != 1 || !strings.Contains(messages[0], "连续登录失败") {
		t.Fatal("missing threshold notification")
	}
	if messages, _, suppressed := d.process(&s, &runtime, []Event{failure}, now.Add(time.Minute)); len(messages) != 0 || suppressed != 1 {
		t.Fatal("cooldown failed")
	}
	trusted := notificationLogin("success", "192.0.2.44", float64(now.Unix()))
	unknown := notificationLogin("success", "", float64(now.Unix()))
	foreign := notificationLogin("success", "198.51.100.20", float64(now.Unix()))
	if messages, _, _ := d.process(&s, &runtime, []Event{trusted, unknown, foreign, foreign}, now); len(messages) != 1 || !strings.Contains(messages[0], "非可信") {
		t.Fatal("trusted/unknown sources or success dedup failed")
	}
	port := 80
	e := Event{TS: float64(now.Unix()), Sender: "192.0.2.1", Src: "198.51.100.30", Scope: "router", Dport: &port}
	if messages, _, _ := d.process(&s, &runtime, []Event{e, e, e}, now); len(messages) != 0 {
		t.Fatal("duplicate ports counted")
	}
	for _, p := range []int{81, 82} {
		e.Dport = &p
		messages, _, _ := d.process(&s, &runtime, []Event{e}, now)
		if (p == 82) != (len(messages) == 1) {
			t.Fatal("scan threshold invalid")
		}
	}
	d = notificationDetector{}
	for i := 0; i < 4; i++ {
		d.process(&s, &runtime, []Event{failure}, now)
	}
	failure.Log.TS += 300
	if messages, _, _ := d.process(&s, &runtime, []Event{failure}, now.Add(5*time.Minute)); len(messages) != 0 {
		t.Fatal("failure windows merged")
	}
	disabled := s
	disabled.Enabled = false
	if messages, _, _ := d.process(&disabled, &runtime, []Event{foreign}, now); len(messages) != 0 {
		t.Fatal("disabled notifier emitted")
	}
}

func TestNotificationHTTPSIntegrationAndNonBlockingCollection(t *testing.T) {
	a := testApp(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	a.notifier.client.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		endpoint := *r.URL
		endpoint.Scheme, endpoint.Host = target.Scheme, target.Host
		clone.URL, clone.Host = &endpoint, target.Host
		return transport.RoundTrip(clone)
	})
	s := fixtureNotification("feishu")
	s.Enabled, s.Storage = true, false
	a.notifier.settings.Store(&s)
	now := unixNow()
	events := make([]Event, 5)
	for i := range events {
		events[i] = notificationLogin("failure", "198.51.100.22", now)
	}
	a.collector.writeBatch(context.Background(), events)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("background notification not sent")
	}
	finished := make(chan struct{})
	go func() {
		a.collector.writeBatch(context.Background(), []Event{notificationLogin("success", "192.0.2.5", now)})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("slow webhook blocked log writes")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for a.notifier.status().Sent == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.notifier.status().Sent != 1 || a.collector.counters.written.Load() != 6 || calls.Load() != 1 {
		t.Fatal("HTTPS send or collection counters invalid")
	}
}

func TestNotificationRejectsRedirectAndBoundsObservationQueue(t *testing.T) {
	a := testApp(t)
	var requests int
	a.notifier.client.Transport = notificationTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.invalid/collect"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := sendNotification(context.Background(), a.notifier.client, fixtureNotification("feishu"), "test", time.Now()); err == nil || requests != 1 {
		t.Fatal("redirect followed or treated as success")
	}
	n := &Notifier{batches: make(chan observedBatch, 1)}
	s := fixtureNotification("feishu")
	s.Enabled = true
	n.settings.Store(&s)
	n.observe([]Event{{}})
	n.observe([]Event{{}, {}})
	if len(n.batches) != 1 || n.status().ObservedDropped != 2 {
		t.Fatal("notification backpressure not bounded/countable")
	}
}

func TestNotificationTestAPIAndCredentialClear(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	if code, _ := b.request(t, "POST", "/api/notifications/test", map[string]any{}, true); code != 400 {
		t.Fatal("unconfigured channel test accepted")
	}
	s := fixtureNotification("feishu")
	s.Secret = "fixture-secret"
	if code, _ := b.request(t, "POST", "/api/notifications", s, true); code != 200 {
		t.Fatal("channel save failed")
	}
	var calls int
	a.notifier.client.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Hostname() != "open.feishu.cn" || !strings.HasSuffix(r.URL.Path, "fixture-token") {
			t.Fatal("saved webhook not used")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})
	if code, body := b.request(t, "POST", "/api/notifications/test", map[string]any{}, true); code != 200 || strings.Contains(string(body), "fixture") {
		t.Fatal("disabled channel test failed or secret exposed")
	}
	if calls != 1 || a.notifier.status().Sent != 1 || a.notifier.status().LastSuccess == 0 {
		t.Fatal("test delivery status missing")
	}
	data, _ := json.Marshal(s)
	var body map[string]any
	json.Unmarshal(data, &body)
	body["webhook"], body["secret"], body["clear_secret"] = "", "", true
	if code, _ := b.request(t, "POST", "/api/notifications", body, true); code != 200 {
		t.Fatal("credential clear failed")
	}
	other, err := openControl(a.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.db.Close()
	loaded, err := other.loadNotifications(context.Background())
	if err != nil || loaded.Secret != "" || loaded.Webhook == "" || loaded.Enabled {
		t.Fatal("saved channel did not survive reopening control database")
	}
}

func TestNotificationLabelNeutralizesMentions(t *testing.T) {
	label := notificationLabel("<at user_id=\"all\">@all</at> &lt;at&gt;\n")
	if strings.ContainsAny(label, "<>@&\r\n") || !strings.Contains(label, "＠all") {
		t.Fatal("log-controlled account text could trigger bot mentions")
	}
	if len([]rune(notificationLabel(strings.Repeat("界", 100)))) != 81 {
		t.Fatal("notification label length is not bounded")
	}
}
