package main

import (
	"context"
	"strings"
	"testing"
)

func TestRouterOSCommandsUseReviewedTemplate(t *testing.T) {
	commands, err := buildRouterOSCommands("192.0.2.21", 15514)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commands.Install, `:local watchServer "192.0.2.21"`) || !strings.Contains(commands.Install, ":local watchPort 15514") {
		t.Fatal("address or UDP port not rendered")
	}
	if strings.Count(commands.Install, "action=log log-prefix=") != 4 || strings.Count(commands.Install, "limit=10/1s,20:packet") != 4 {
		t.Fatal("logging-only rules changed")
	}
	if strings.Count(commands.Install, "connection-nat-state=dstnat") != 2 || !strings.Contains(commands.Install, "ROSWatch already exists") {
		t.Fatal("NAT scope or duplicate guard missing")
	}
	for _, forbidden := range []string{"/ip service", "action=accept", "action=drop", "/ip firewall filter remove"} {
		if strings.Contains(commands.Install, forbidden) {
			t.Fatal("unexpected management or firewall policy change")
		}
	}
	if strings.Contains(commands.Check, "remove") || !strings.Contains(commands.Check, "print stats") {
		t.Fatal("check commands modify state")
	}
	if strings.Count(commands.Remove, "remove [find") != 5 || !strings.Contains(commands.Remove, `comment~"^ROS-WATCH "`) || !strings.Contains(commands.Remove, `name="ROSWatch"`) {
		t.Fatal("removal scope changed")
	}
	if !strings.Contains(commands.Upgrade, "ROSWatchSystem") || !strings.Contains(commands.Upgrade, "syslog-severity=auto") || strings.Contains(commands.Upgrade, "/ip firewall") || strings.Count(commands.Upgrade, "/system logging add") != 6 || !strings.Contains(commands.RemoveSystem, `name="ROSWatchSystem"`) {
		t.Fatal("system log upgrade or rollback missing")
	}
}
func TestRouterOSGeneratorAuthValidationAndNoSettingsMutation(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	body := map[string]any{"server_address": "192.0.2.21", "syslog_port": 5514}
	if code, _ := b.request(t, "POST", "/api/routeros-script", body, false); code != 401 {
		t.Fatal("anonymous script generation accepted", code)
	}
	b.login(t, "admin", "test-account-password")
	if code, _ := b.request(t, "POST", "/api/routeros-script", body, false); code != 400 {
		t.Fatal("missing CSRF accepted", code)
	}
	for _, server := range []string{"", "127.0.0.1", "0.0.0.0", "192.0.2.10", "255.255.255.255", "224.0.0.1", "169.254.1.1", "::1", "2001:db8::1", "example.com", "192.0.2.21\"; /system reboot", "192.0.2.21\n/system reboot"} {
		body["server_address"] = server
		if code, _ := b.request(t, "POST", "/api/routeros-script", body, true); code != 400 {
			t.Fatal("invalid server accepted", code)
		}
	}
	body["server_address"] = "192.0.2.21"
	for _, port := range []any{0, -1, 65536, true, 12.5} {
		body["syslog_port"] = port
		if code, _ := b.request(t, "POST", "/api/routeros-script", body, true); code != 400 {
			t.Fatal("invalid port accepted", code)
		}
	}
	beforeAccount, _ := a.control.account(context.Background())
	beforeSettings := b.settings(t)
	body["syslog_port"] = 5514
	code, data := b.request(t, "POST", "/api/routeros-script", body, true)
	if code != 200 || decoded(t, data)["install"] == "" {
		t.Fatal("script generation failed", code)
	}
	afterAccount, _ := a.control.account(context.Background())
	afterSettings := b.settings(t)
	if beforeAccount != afterAccount || beforeSettings.AllowedSenders != afterSettings.AllowedSenders || beforeSettings.MinFreeMB != afterSettings.MinFreeMB {
		t.Fatal("generation changed account or collector settings")
	}
	code, page := b.request(t, "GET", "/", nil, false)
	if code != 200 || !strings.Contains(string(page), "RouterOS 接入指令") || !strings.Contains(string(page), "/static/routeros.js") || !strings.Contains(string(page), "复制撤销指令") {
		t.Fatal("instructions not available in settings page")
	}
	if code, _ := b.request(t, "GET", "/static/routeros.js", nil, false); code != 200 {
		t.Fatal("script UI asset missing")
	}
}
