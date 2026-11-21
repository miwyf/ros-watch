package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAISettingsPreserveKeysOnlyForSameEndpoint(t *testing.T) {
	previous := aiSettings{
		Profiles:   []aiProfile{{ID: "gateway", Name: "Gateway", BaseURL: "https://api.example.test/v1", Model: "test", APIKey: "saved-model-key"}},
		Reputation: aiReputationSettings{AbuseKey: "saved-intel-key"},
	}
	next := aiSettings{
		Profiles:   []aiProfile{{ID: "gateway", Name: "Gateway", BaseURL: "https://api.example.test/v1/chat/completions/", Model: "test"}},
		Reputation: aiReputationSettings{AbuseIPDB: true},
	}
	if err := next.validate(previous); err != nil {
		t.Fatal(err)
	}
	if p := next.Profiles[0]; p.APIKey != "saved-model-key" || p.BaseURL != "https://api.example.test/v1" || next.Default != "gateway" {
		t.Fatal("saved profile was not retained")
	}
	public, _ := json.Marshal(next.public())
	if bytes.Contains(public, []byte("saved-model-key")) || bytes.Contains(public, []byte("saved-intel-key")) {
		t.Fatal("secret exposed")
	}
	next = next.public()
	next.Profiles[0].BaseURL = "https://gateway.other.test/v1"
	if err := next.validate(previous); err != nil || next.Profiles[0].APIKey != "" {
		t.Fatalf("changed endpoint inherited an API key: %v", err)
	}
	if _, err := normalizeAIBase("http://169.254.169.254/v1"); err == nil {
		t.Fatal("accepted insecure link-local endpoint")
	}
}

func TestAICustomIntelSavedLookup(t *testing.T) {
	a := testApp(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/lookup" || r.Header.Get("X-API-Key") != "custom-fixture-key" || len(r.URL.Query()) != 1 {
			t.Errorf("unexpected custom request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Query().Get("ip") {
		case "79.124.62.10":
			_, _ = w.Write([]byte(`{"data":{"confidence":81,"found":false,"classification":"suspicious"},"raw":"raw-provider-private","token":"custom-fixture-key"}`))
		case "79.124.49.146":
			_, _ = w.Write([]byte(`{"data":{"confidence":0,"found":"false","classification":"echo custom-fixture-key"}}`))
		case "2606:4700:4700::1111":
			w.WriteHeader(http.StatusNotFound)
		case "93.184.215.14":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			t.Error("unexpected source reached the provider")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer api.Close()
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	custom := aiCustomIntelSettings{
		Enabled: true, URL: api.URL + "/lookup?ip={ip}", Auth: "x-api-key", APIKey: "custom-fixture-key",
		Fields: map[string]string{"confidence": "data.confidence", "listed": "data.found", "classification": "data.classification"},
	}
	settings := aiSettings{Reputation: aiReputationSettings{Custom: custom}}
	if code, body := b.request(t, "POST", "/api/ai/settings", settings, true); code != 200 ||
		bytes.Contains(body, []byte(custom.APIKey)) || !bytes.Contains(body, []byte(`"key_set":true`)) {
		t.Fatalf("custom settings response: %d %s", code, body)
	}
	for _, item := range []struct {
		ip       string
		code     int
		expected []string
	}{
		{"10.1.2.3", 400, nil},
		{"79.124.62.10", 200, []string{`"status":"queried"`, `"confidence":81`, `"listed":false`, `"classification":"suspicious"`}},
		{"79.124.49.146", 200, []string{`"status":"partial"`, `"result":{"confidence":0}`}},
		{"2606:4700:4700::1111", 200, []string{`"status":"no_record"`}},
		{"93.184.215.14", 200, []string{`"status":"rate_limited"`}},
	} {
		code, body := b.request(t, "POST", "/api/ai/reputation/custom/test", map[string]string{"ip": item.ip}, true)
		if code != item.code || bytes.Contains(body, []byte(custom.APIKey)) || bytes.Contains(body, []byte("raw-provider-private")) {
			t.Fatalf("custom query: %d %s", code, body)
		}
		for _, expected := range item.expected {
			if !bytes.Contains(body, []byte(expected)) {
				t.Fatalf("missing %s in %s", expected, body)
			}
		}
	}
	if code, _ := b.request(t, "POST", "/api/ai/reputation/custom/test", map[string]string{"ip": "79.124.62.10"}, false); code != 400 {
		t.Fatalf("custom lookup accepted a request without CSRF: %d", code)
	}
	saved, err := a.control.loadAI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	toolData, _ := a.aiInvoke(context.Background(), aiFunction{"lookup_reputation", `{"sources":["79.124.62.10"]}`}, saved.Reputation, unixNow(), time.UTC)
	encoded, _ := json.Marshal(toolData)
	if !bytes.Contains(encoded, []byte(`"service":"自定义 IP 情报"`)) || !bytes.Contains(encoded, []byte(`"confidence":81`)) || bytes.Contains(encoded, []byte(custom.APIKey)) {
		t.Fatalf("wrong custom tool result: %s", encoded)
	}
	draft := saved.public()
	draft.Reputation.Custom.URL += "&v=2"
	if err := draft.validate(saved); err == nil {
		t.Fatal("changing lookup URL retained the previous API key")
	}
	if !validAIIntelURL("https://example.test/check/{ip}?period=7d") || validAIIntelURL("https://{ip}.example.test/check") ||
		validAIIntelURL("https://example.test/check?{ip}=query") || validAIIntelURL("https://secret@example.test/{ip}") {
		t.Fatal("custom URL did not restrict the placeholder to the path or a query value")
	}
}

func TestAIChatFromScanToReputationAndLogin(t *testing.T) {
	a := testApp(t)
	settings := *a.settings.Load()
	settings.ScanPorts = 3
	a.settings.Store(&settings)
	const source = "79.124.62.10"
	ts := float64(time.Now().Unix()/300*300 - 150)
	dst := "192.168.5.22"
	rows := []Event{}
	for n := 0; n < 4; n++ {
		port := 1500 + n
		rows = append(rows, Event{TS: ts, Sender: "10.0.0.1", Src: source, Dst: dst, Dport: &port, Proto: "TCP", Scope: "router", Outcome: "attempt", Direction: "inbound", Raw: "fixture-private-firewall-log"})
	}
	for n := 0; n < 2; n++ {
		port := 1600 + n
		rows = append(rows, Event{TS: ts, Sender: "10.0.0.2", Src: source, Dst: dst, Dport: &port, Proto: "TCP", Scope: "router", Outcome: "attempt", Direction: "inbound"})
	}
	rows = append(rows, Event{Log: &LogEvent{TS: ts, Sender: "10.0.0.1", Category: "login", Username: "fixture-private-account", Source: source, Event: "failure", Raw: "fixture-private-login-log"}})
	if err := a.store.append(context.Background(), rows); err != nil {
		t.Fatal(err)
	}

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []aiMessage `json:"messages"`
			Tools    []aiTool    `json:"tools"`
		}
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(r.Body)
		for _, secret := range []string{"fixture-private", "saved-model-key", "saved-intel-key", dst} {
			if strings.Contains(raw.String(), secret) {
				t.Error("raw or private content reached model", secret)
			}
		}
		if err := json.Unmarshal(raw.Bytes(), &req); err != nil || len(req.Messages) < 2 || r.URL.Path != "/prefix/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer saved-model-key" {
			t.Errorf("unexpected model request: %v", err)
			w.WriteHeader(400)
			return
		}
		last := req.Messages[len(req.Messages)-1]
		answer := aiMessage{}
		if last.Role == "tool" {
			switch last.CallID {
			case "test":
				answer.Content = last.Content
			case "scan":
				if !strings.Contains(last.Content, `"qualifying_windows":1`) || !strings.Contains(last.Content, `"peak_distinct_ports":4`) || !strings.Contains(last.Content, source) {
					t.Error("wrong scan data", last.Content)
				}
				answer.Content = "一个保加利亚扫描窗口。"
			case "intel":
				if !strings.Contains(last.Content, `"announcing_asns":[207812]`) || !strings.Contains(last.Content, `"cidr":"79.124.62.0/24"`) ||
					!strings.Contains(last.Content, `"status":"rate_limited"`) {
					t.Error("wrong intelligence payload", last.Content)
				}
				answer.Content = "该来源有网段名单记录。"
			case "login":
				if !strings.Contains(last.Content, `"failure":1`) || !strings.Contains(last.Content, `"success":0`) {
					t.Error("wrong login data", last.Content)
				}
				answer.Content = "记录到一次登录失败。"
			}
		} else {
			call := aiCall{Type: "function"}
			switch last.Content {
			case "Run the tool connectivity test.":
				call.ID, call.Function = "test", aiFunction{"ros_watch_connectivity", "{}"}
			case "保加利亚扫描":
				call.ID, call.Function = "scan", aiFunction{"analyze_scans", `{"hours":168,"country":"BG"}`}
			case "这些 IP 有黑记录吗":
				if !strings.Contains(raw.String(), source) {
					t.Error("follow-up lost previous source")
				}
				call.ID, call.Function = "intel", aiFunction{"lookup_reputation", `{"sources":["79.124.62.10"]}`}
			case "有没有登录成功":
				call.ID, call.Function = "login", aiFunction{"check_logins", `{"hours":168,"sources":["79.124.62.10"]}`}
			}
			answer.Calls = []aiCall{call}
		}
		jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": answer}}})
	}))
	defer model.Close()
	a.ai.client.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.String(), model.URL) {
			return http.DefaultTransport.RoundTrip(r)
		}
		status, body := 200, ""
		switch {
		case strings.HasPrefix(r.URL.String(), aiGreyEndpoint):
			status = 429
		case strings.HasPrefix(r.URL.String(), aiAbuseEndpoint):
			if r.Header.Get("Key") != "saved-intel-key" || r.URL.Query().Get("ipAddress") != source {
				t.Error("wrong AbuseIPDB request")
			}
			body = `{"data":{"ipAddress":"79.124.62.10","abuseConfidenceScore":90,"totalReports":5}}`
		case r.URL.String() == aiDropEndpoint+"drop_v4.json":
			body = `{"cidr":"79.124.62.0/24","sblid":"SBL-fixture"}`
		case r.URL.String() == aiDropEndpoint+"asndrop.json":
			body = `{"asn":207812}`
		case strings.HasPrefix(r.URL.String(), aiRIPEEndpoint):
			body = `{"data":{"asns":["207812"]}}`
		default:
			t.Error("unexpected external API", r.URL.Host)
			status = 500
		}
		resp := httptest.NewRecorder()
		resp.WriteHeader(status)
		_, _ = resp.WriteString(body)
		return resp.Result(), nil
	})
	b := browser(t, a)
	if code, _ := b.request(t, "GET", "/api/ai/settings", nil, false); code != 401 {
		t.Fatalf("unauthenticated settings read: %d", code)
	}
	b.login(t, "admin", "test-account-password")
	config := aiSettings{Profiles: []aiProfile{{ID: "test", Name: "本地测试", Model: "fixture", BaseURL: model.URL + "/prefix/v1", APIKey: "saved-model-key"}},
		Reputation: aiReputationSettings{GreyNoise: true, AbuseIPDB: true, AbuseKey: "saved-intel-key", Spamhaus: true}}
	if code, _ := b.request(t, "POST", "/api/ai/settings", config, false); code != 400 {
		t.Fatalf("unauthenticated mutation: %d", code)
	}
	if code, body := b.request(t, "POST", "/api/ai/settings", config, true); code != 200 || strings.Contains(string(body), "saved-") {
		t.Fatalf("settings save: %d %s", code, body)
	}
	if code, body := b.request(t, "POST", "/api/ai/test", map[string]string{"profile": "test"}, true); code != 200 {
		t.Fatalf("tool round trip: %d %s", code, body)
	}
	conversation := ""
	for _, item := range []struct{ question, answer, link string }{
		{"保加利亚扫描", "一个保加利亚扫描窗口。", "#events"},
		{"这些 IP 有黑记录吗", "该来源有网段名单记录。", "spamhaus.org"},
		{"有没有登录成功", "记录到一次登录失败。", "#logs"},
	} {
		code, body := b.request(t, "POST", "/api/ai/chat", map[string]any{"question": item.question, "conversation": conversation, "timezone_offset_minutes": 480}, true)
		if code != 200 || !strings.Contains(string(body), item.link) {
			t.Fatalf("chat error: %d %s", code, body)
		}
		reply := decoded(t, body)
		id := reply["conversation"].(string)
		if reply["answer"] != item.answer || (conversation != "" && id != conversation) {
			t.Fatal("unexpected answer or conversation", reply)
		}
		conversation = id
	}
	for _, item := range []struct{ question, sources string }{
		{"给我ros的封锁指令", `"block_sources":["79.124.62.10"]`},
		{"给我 IP:79.124.49.146 的封禁命令", `"block_sources":["79.124.49.146"]`},
		{"给我 79.124.62.0/24 的封禁命令", `"block_sources":null`},
	} {
		code, body := b.request(t, "POST", "/api/ai/chat", map[string]string{"question": item.question, "conversation": conversation}, true)
		if code != 200 || !strings.Contains(string(body), item.sources) || !strings.Contains(string(body), `"model":"ROS Watch"`) {
			t.Fatalf("built-in block commands: %d %s", code, body)
		}
	}
	other := browser(t, a)
	path := "/api/ai/conversations/" + conversation
	if code, _ := other.request(t, "GET", path, nil, false); code != 401 {
		t.Fatalf("unauthenticated history read: %d", code)
	}
	other.login(t, "admin", "test-account-password")
	if code, body := other.request(t, "GET", "/api/ai/conversations", nil, false); code != 200 ||
		!strings.Contains(string(body), conversation) || !strings.Contains(string(body), `"title":"保加利亚扫描"`) {
		t.Fatalf("history list: %d %s", code, body)
	}
	if code, body := other.request(t, "GET", path, nil, false); code != 200 || !strings.Contains(string(body), `"can_resume":true`) ||
		!strings.Contains(string(body), "spamhaus.org") || !strings.Contains(string(body), `"block_sources":["79.124.62.10"]`) ||
		strings.Contains(string(body), "tool_calls") || strings.Contains(string(body), "saved-model-key") {
		t.Fatalf("restored history: %d %s", code, body)
	}
	continuation := map[string]string{"question": "给我ros的封锁指令", "conversation": conversation}
	if code, body := other.request(t, "POST", "/api/ai/chat", continuation, true); code != 200 ||
		!strings.Contains(string(body), `"block_sources":["79.124.62.10"]`) {
		t.Fatalf("resume after signing in again: %d %s", code, body)
	}
	config.Profiles[0].Model = "different-model"
	if code, body := b.request(t, "POST", "/api/ai/settings", config, true); code != 200 {
		t.Fatalf("change model: %d %s", code, body)
	}
	if code, body := b.request(t, "GET", path, nil, false); code != 200 || !strings.Contains(string(body), `"can_resume":false`) {
		t.Fatalf("archived history after model change: %d %s", code, body)
	}
	if code, _ := b.request(t, "POST", "/api/ai/chat", continuation, true); code != 409 {
		t.Fatalf("conversation was forwarded to a changed connection: %d", code)
	}
	if code, _ := b.request(t, "DELETE", path, nil, false); code != 400 {
		t.Fatalf("history delete without CSRF: %d", code)
	}
	if code, _ := b.request(t, "DELETE", path, nil, true); code != 200 {
		t.Fatalf("history delete: %d", code)
	}
	if code, _ := other.request(t, "GET", path, nil, false); code != 404 {
		t.Fatalf("deleted history still readable: %d", code)
	}
	var count int
	if err := a.control.db.QueryRow("SELECT count(*) FROM ai_turns WHERE conversation_id=?", conversation).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleting the conversation left saved turns: %d %v", count, err)
	}
	if _, _, err := a.ai.ipReputation(context.Background(), aiArguments{Sources: []string{"192.168.1.10"}}, config.Reputation); err == nil {
		t.Fatal("private IP allowed in external lookup")
	}
}

func TestAIModelListUsesDraftAndEndpointSpecificKey(t *testing.T) {
	a := testApp(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := map[string]string{"/draft/v1/models": "Bearer draft-key", "/saved/v1/models": "Bearer saved-key", "/changed/v1/models": ""}
		auth, ok := expected[r.URL.Path]
		if !ok || r.Method != http.MethodGet || r.Header.Get("Authorization") != auth {
			t.Errorf("unexpected model-list request to %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"a-model"},{"id":""}]}`))
	}))
	defer model.Close()
	b := browser(t, a)
	b.login(t, "admin", "test-account-password")
	config := aiSettings{Profiles: []aiProfile{{ID: "saved", Name: "Saved", Model: "existing", BaseURL: model.URL + "/saved/v1", APIKey: "saved-key"}}}
	if code, body := b.request(t, "POST", "/api/ai/settings", config, true); code != 200 {
		t.Fatalf("save model connection: %d %s", code, body)
	}
	if code, _ := b.request(t, "POST", "/api/ai/models", aiProfile{BaseURL: model.URL + "/changed/v1"}, false); code != 400 {
		t.Fatalf("model discovery without CSRF: %d", code)
	}
	for _, draft := range []aiProfile{
		{ID: "unsaved", BaseURL: model.URL + "/draft/v1", APIKey: "draft-key"},
		{ID: "saved", BaseURL: model.URL + "/saved/v1/chat/completions/"},
		{ID: "saved", BaseURL: model.URL + "/changed/v1"},
	} {
		code, body := b.request(t, "POST", "/api/ai/models", draft, true)
		if code != 200 || !strings.Contains(string(body), `"models":["a-model","z-model"]`) {
			t.Fatalf("list models: %d %s", code, body)
		}
	}
	code, body := b.request(t, "GET", "/api/ai/settings", nil, false)
	if code != 200 || !strings.Contains(string(body), `"model":"existing"`) || strings.Contains(string(body), "draft-key") {
		t.Fatalf("discovery changed saved settings: %d %s", code, body)
	}
}
