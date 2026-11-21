package main

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
)

var aiAddressToken = regexp.MustCompile(`[a-zA-Z0-9_:%./-]+`)

func aiWantsBlockCommands(question string) bool {
	if aiContainsPhrase(question, "解封", "解锁", "解除封", "取消封", "撤销", "不要封", "不想封", "别封") {
		return false
	}
	return aiContainsPhrase(question, "封禁", "封锁", "封堵", "拉黑") &&
		aiContainsPhrase(question, "命令", "指令", "脚本", "一键")
}

func aiContainsPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func aiBlockCommands(question string, history []aiMessage, demo bool) (string, []string) {
	const single = "ROS Watch 的一键封禁接受单个 IPv4 地址。请给出要封禁的具体 IP。"
	if aiContainsPhrase(question, "整段", "整个网段", "按网段") {
		return single, nil
	}
	addresses := []string{}
	seen := map[string]bool{}
	add := func(address string) {
		if !seen[address] {
			addresses = append(addresses, address)
			seen[address] = true
		}
	}
	for _, token := range aiAddressToken.FindAllString(question, -1) {
		token = strings.Trim(token, ".")
		if strings.Count(token, ".") < 3 && strings.Count(token, ":") < 2 {
			continue
		}
		if strings.Count(token, ":") == 1 {
			prefix, address, _ := strings.Cut(token, ":")
			if prefix == "" || strings.EqualFold(prefix, "ip") || strings.EqualFold(prefix, "ipv4") {
				token = address
			}
		}
		ip, err := netip.ParseAddr(token)
		if err != nil || !ip.Is4() {
			return single, nil
		}
		add(ip.String())
	}
	if len(addresses) == 0 {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role != "tool" {
				continue
			}
			var scan struct {
				Windows *int `json:"qualifying_windows"`
				Sources []struct {
					IP string `json:"ip"`
				} `json:"sources"`
			}
			if json.Unmarshal([]byte(history[i].Content), &scan) != nil || scan.Windows == nil {
				continue
			}
			for _, source := range scan.Sources {
				if ip, err := netip.ParseAddr(source.IP); err == nil && ip.Is4() {
					add(ip.String())
				}
			}
			break
		}
	}
	if len(addresses) == 0 {
		return "请提供要封禁的 IPv4 地址，或先查询疑似扫描来源。", nil
	}
	answer := "已生成 ROS Watch 的一键封禁指令，使用 blocked-sources 地址列表。请核对来源后复制到 RouterOS 终端。"
	if demo {
		answer = "当前为演示实例。请用真实日志核对来源。\n" + answer
	}
	return answer, addresses
}
