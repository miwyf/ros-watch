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
	Upgrade      string `json:"upgrade"`
	Check        string `json:"check"`
	Remove       string `json:"remove"`
	RemoveSystem string `json:"remove_system"`
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
	data, err := assets.ReadFile("routeros/ros-watch.rsc.example")
	if err != nil {
		return result, errors.New("接入指令暂不可用")
	}
	install, _, found := strings.Cut(string(data), "# Rollback ONLY")
	if !found {
		return result, errors.New("接入指令模板无效")
	}
	install = strings.Replace(install, `:local watchServer "192.0.2.10"`, `:local watchServer "`+address.String()+`"`, 1)
	install = strings.Replace(install, ":local watchPort 5514", ":local watchPort "+strconv.Itoa(port), 1)
	upgrade, err := assets.ReadFile("routeros/ros-watch-system.rsc.example")
	if err != nil {
		return result, errors.New("系统日志指令暂不可用")
	}
	result.Upgrade = strings.TrimSpace(string(upgrade))
	result.Install = strings.TrimSpace(install) + "\n\n" + result.Upgrade
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
		Server string `json:"server_address"`
		Port   int    `json:"syslog_port"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	commands, err := buildRouterOSCommands(body.Server, body.Port)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	jsonResponse(w, 200, commands)
}
