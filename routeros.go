package main

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

type RouterOSCommands struct {
	Install      string `json:"install"`
	Repair       string `json:"repair"`
	Upgrade      string `json:"upgrade"`
	Check        string `json:"check"`
	Remove       string `json:"remove"`
	RemoveSystem string `json:"remove_system"`
}

func routerOSTemplateBody(path string) (string, error) {
	data, err := assets.ReadFile(path)
	if err != nil {
		return "", errors.New("接入指令暂不可用")
	}
	text, _, _ := strings.Cut(string(data), "# Rollback ONLY")
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") || !strings.HasSuffix(text, "}") {
		return "", errors.New("接入指令模板无效")
	}
	return strings.TrimSpace(text[1 : len(text)-1]), nil
}

func routerOSTerminalBlock(body, completion string) string {
	lines := []string{"{"}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if text := strings.TrimSpace(line); text != "" && !strings.HasPrefix(text, "#") {
			lines = append(lines, strings.TrimRight(line, "\r"))
		}
	}
	return strings.Join(lines, "\n") + "\n:put \"" + completion + "\"\n}"
}

func routerOSAuxiliaryCommands() (RouterOSCommands, error) {
	result := RouterOSCommands{}
	upgrade, err := routerOSTemplateBody("routeros/ros-watch-system.rsc.example")
	if err != nil {
		return result, errors.New("系统日志指令暂不可用")
	}
	result.Upgrade = routerOSTerminalBlock(upgrade, "ROS Watch system logs ready")
	result.Check = `/system logging action print detail where name="ROSWatch"
/system logging action print detail where name="ROSWatchSystem"
/system logging print detail where action="ROSWatch"
/system logging print detail where action="ROSWatchSystem"
/ip firewall filter print stats where comment~"^ROS-WATCH "
/log print where message~"RW-|login failure|logged in|logged out"`
	result.RemoveSystem = `/system logging remove [find where action="ROSWatchSystem"]
/system logging action remove [find where name="ROSWatchSystem"]`
	result.Remove = `# 仅移除 ROS Watch 标记的规则及日志转发，执行后停止向看板发送这些日志。
/ip firewall filter remove [find where comment~"^ROS-WATCH "]
/system logging remove [find where action="ROSWatch"]
/system logging action remove [find where name="ROSWatch"]` + "\n" + result.RemoveSystem
	return result, nil
}

func buildRouterOSCommands(server string, port int) (RouterOSCommands, error) {
	result := RouterOSCommands{}
	address, err := netip.ParseAddr(strings.TrimSpace(server))
	if err != nil || !address.Is4() || address.IsLoopback() || !address.IsGlobalUnicast() || address.IsLinkLocalUnicast() || address.String() == "192.0.2.10" {
		return result, errors.New("请填写 ROS 能访问的日志服务器 IPv4 地址，不能使用回环地址或模板占位地址")
	}
	if port < 1 || port > 65535 {
		return result, errors.New("UDP 端口须为 1–65535")
	}
	result, err = routerOSAuxiliaryCommands()
	if err != nil {
		return result, err
	}
	upgrade, err := routerOSTemplateBody("routeros/ros-watch-system.rsc.example")
	if err != nil {
		return result, errors.New("系统日志指令暂不可用")
	}
	install, err := routerOSTemplateBody("routeros/ros-watch.rsc.example")
	if err != nil {
		return result, errors.New("接入指令暂不可用")
	}
	repair, err := routerOSTemplateBody("routeros/ros-watch-repair.rsc.example")
	if err != nil {
		return result, err
	}
	renderTarget := func(body string) string {
		body = strings.Replace(body, `:local watchServer "192.0.2.10"`, `:local watchServer "`+address.String()+`"`, 1)
		return strings.Replace(body, ":local watchPort 5514", ":local watchPort "+strconv.Itoa(port), 1)
	}
	result.Install = routerOSTerminalBlock(renderTarget(install)+"\n\n"+upgrade, "ROS Watch installation completed")
	result.Repair = routerOSTerminalBlock(renderTarget(repair)+"\n\n"+upgrade, "ROS Watch log forwarding repair completed")

	return result, nil
}

func (a *App) routerOSScript(w http.ResponseWriter, r *http.Request) {
	_, s, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, s) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body struct {
		Server  string `json:"server_address"`
		Port    int    `json:"syslog_port"`
		Command string `json:"command"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	var commands RouterOSCommands
	var err error
	switch body.Command {
	case "", "install":
		commands, err = buildRouterOSCommands(body.Server, body.Port)
	case "check", "remove", "upgrade", "remove_system":
		commands, err = routerOSAuxiliaryCommands()
	default:
		err = errors.New("未知接入指令")
	}
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	jsonResponse(w, 200, commands)
}
