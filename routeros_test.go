package main

import (
	"context"
	"strings"
	"testing"
)

func requireSingleRouterOSTerminalBlock(t *testing.T, script string) {
	t.Helper()
	script = strings.TrimSpace(strings.ReplaceAll(script, "\r\n", "\n"))
	if !strings.HasPrefix(script, "{\n") || !strings.HasSuffix(script, "\n}") {
		t.Fatal("terminal commands must share one outer block; separate lines lose local variables")
	}
	depth, quoted, escaped, comment := 0, false, false, false
	for i, r := range script {
		if comment {
			comment = r != '\n'
			continue
		}
		if quoted {
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				quoted = false
			}
			continue
		}
		switch r {
		case '#':
			comment = true
		case '"':
			quoted = true
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 || depth == 0 && i != len(script)-1 {
				t.Fatal("script ends its terminal scope before all commands finish")
			}
		}
	}
	if depth != 0 || quoted {
		t.Fatal("unbalanced RouterOS block or string")
	}
}

func TestRouterOSGeneratedScriptsShareTerminalScope(t *testing.T) {
	commands, err := buildRouterOSCommands("192.0.2.21", 15514)
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"install": commands.Install, "upgrade": commands.Upgrade, "repair": commands.Repair} {
		t.Run(name, func(t *testing.T) { requireSingleRouterOSTerminalBlock(t, script) })
	}
	for _, path := range []string{"routeros/ros-watch.rsc.example", "routeros/ros-watch-system.rsc.example", "routeros/ros-watch-repair.rsc.example"} {
		data, err := assets.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		block, _, _ := strings.Cut(string(data), "# Rollback ONLY")
		t.Run(path, func(t *testing.T) { requireSingleRouterOSTerminalBlock(t, block) })
	}
}

func TestRouterOSRepairPreservesObservationRulesAndExistingTargets(t *testing.T) {
	commands, err := buildRouterOSCommands("192.0.2.21", 15514)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`:local watchServer "192.0.2.21"`, ":local watchPort 15514", `remote-port=$watchPort`, "ROS-WATCH observation rules are missing", "ROSWatch target is not remote", "ROSWatch server differs", "ROSWatch port differs", "ROS Watch log forwarding repair completed"} {
		if !strings.Contains(commands.Repair, expected) {
			t.Fatal("repair guard or rendered target missing", expected)
		}
	}
	for _, forbidden := range []string{"/ip firewall filter add", "/ip firewall filter set", "/ip firewall filter remove", "/ip firewall filter move", "/interface list add", "/system logging action set", "/system logging action remove", "/system logging remove", ":global"} {
		if strings.Contains(commands.Repair, forbidden) {
			t.Fatal("repair changes existing observation or logging configuration", forbidden)
		}
	}
	if strings.Count(commands.Repair, "/system logging add") != 7 || !strings.Contains(commands.Repair, `action="ROSWatchSystem" and prefix="RW-SYS-OTHER "`) {
		t.Fatal("firewall/system log repair coverage changed")
	}
	firstWrite := strings.Index(commands.Repair, "/system logging action add")
	if strings.Index(commands.Repair, "ROSWatch port differs") > firstWrite {
		t.Fatal("repair must check the existing target before adding any logging configuration")
	}
	firstInstallWrite := strings.Index(commands.Install, "/system logging action add")
	if strings.Index(commands.Install, "ROS-WATCH observation rules already exist") < 0 || strings.Index(commands.Install, "ROS-WATCH observation rules already exist") > firstInstallWrite {
		t.Fatal("partial-install guard must run before any installation write")
	}
}

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
	if code != 200 || decoded(t, data)["install"] == "" || decoded(t, data)["repair"] == "" {
		t.Fatal("script generation failed", code)
	}
	afterAccount, _ := a.control.account(context.Background())
	afterSettings := b.settings(t)
	if beforeAccount != afterAccount || beforeSettings.AllowedSenders != afterSettings.AllowedSenders {
		t.Fatal("generation changed account or collector settings")
	}
	code, page := b.request(t, "GET", "/", nil, false)
	if code != 200 || !strings.Contains(string(page), "一键接入指令") || !strings.Contains(string(page), "/static/routeros.js") || !strings.Contains(string(page), `id="routeros-dialog"`) || strings.Contains(string(page), `id="ros-repair"`) {
		t.Fatal("instructions not available in settings page")
	}
	if code, _ := b.request(t, "GET", "/static/routeros.js", nil, false); code != 200 {
		t.Fatal("script UI asset missing")
	}
}

func TestRouterOSAuxiliaryCommandsWithoutServer(t *testing.T) {
	a := testApp(t)
	b := browser(t, a)
	body := map[string]any{"command": "check"}
	if code, _ := b.request(t, "POST", "/api/routeros-script", body, false); code != 401 {
		t.Fatal("anonymous auxiliary command accepted", code)
	}
	b.login(t, "admin", "test-account-password")
	if code, _ := b.request(t, "POST", "/api/routeros-script", body, false); code != 400 {
		t.Fatal("missing CSRF accepted", code)
	}
	for _, command := range []string{"check", "remove", "upgrade", "remove_system"} {
		body["command"] = command
		code, data := b.request(t, "POST", "/api/routeros-script", body, true)
		if code != 200 || decoded(t, data)[command] == "" {
			t.Fatal("auxiliary command requires an irrelevant server address", command, code)
		}
		if command == "check" && strings.Contains(decoded(t, data)[command].(string), "remove") {
			t.Fatal("status command modifies configuration")
		}
	}
	for _, command := range []string{"install", "unknown", "repair"} {
		body["command"] = command
		if code, _ := b.request(t, "POST", "/api/routeros-script", body, true); code != 400 {
			t.Fatal("invalid or incomplete command accepted", command, code)
		}
	}
}
