package main

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type notificationBucket struct {
	window int64
	count  int
	ports  map[int]bool
}
type notificationDetector struct {
	settings *NotificationSettings
	failures map[string]*notificationBucket
	scans    map[string]*notificationBucket
	cooldown map[string]time.Time
	pruneAt  time.Time
	ports    int
}

func (d *notificationDetector) process(s *NotificationSettings, runtime *Settings, events []Event, now time.Time) ([]string, int64, int64) {
	if d.settings != s {
		*d = notificationDetector{settings: s, failures: map[string]*notificationBucket{}, scans: map[string]*notificationBucket{}, cooldown: map[string]time.Time{}}
	}
	if !s.Enabled {
		return nil, 0, 0
	}
	if !now.Before(d.pruneAt) {
		for key, bucket := range d.failures {
			if bucket.window < now.Unix()/300-1 {
				delete(d.failures, key)
			}
		}
		for key, bucket := range d.scans {
			if bucket.window < now.Unix()/int64(runtime.ScanWindow)-1 {
				d.ports -= len(bucket.ports)
				delete(d.scans, key)
			}
		}
		for key, until := range d.cooldown {
			if !now.Before(until) {
				delete(d.cooldown, key)
			}
		}
		d.pruneAt = now.Add(time.Minute)
	}
	messages := []string{}
	var dropped, suppressed int64
	emit := func(key, text string) {
		if until := d.cooldown[key]; now.Before(until) {
			suppressed++
			return
		}
		if len(d.cooldown) >= 4096 {
			dropped++
			return
		}
		d.cooldown[key] = now.Add(notificationCooldown)
		messages = append(messages, "ROS Watch 告警 · "+text)
	}
	for _, event := range events {
		if l := event.Log; l != nil && l.Category == "login" && l.Source != "" {
			key := strings.Join([]string{l.Sender, l.Device, l.Source, l.Username}, "\x00")
			detail := fmt.Sprintf("\n设备：%s\n账号：%s\n来源：%s\n方式：%s", notificationLabel(l.Device), notificationLabel(l.Username), l.Source, notificationLabel(l.Method))
			if s.LoginFailures && l.Event == "failure" {
				window := int64(l.TS) / 300
				bucket := d.failures[key]
				if bucket == nil || bucket.window != window {
					if bucket == nil && len(d.failures) >= 1024 {
						dropped++
						continue
					}
					bucket = &notificationBucket{window: window}
					d.failures[key] = bucket
				}
				bucket.count++
				if bucket.count >= 5 {
					emit("failure\x00"+key, "连续登录失败"+detail+fmt.Sprintf("\n同一 5 分钟固定窗口已观察到 %d 次失败。", bucket.count))
				}
			}
			if s.LoginSuccess && l.Event == "success" {
				address, err := netip.ParseAddr(l.Source)
				if err != nil {
					continue
				}
				trusted := false
				for _, prefix := range s.networks {
					if prefix.Contains(address.Unmap()) {
						trusted = true
						break
					}
				}
				if !trusted {
					emit("success\x00"+key, "非可信来源登录成功"+detail)
				}
			}
		}
		if s.Scans && event.Scope != "" && event.Src != "" && event.Dport != nil {
			key := event.Sender + "\x00" + event.Src
			window := int64(event.TS) / int64(runtime.ScanWindow)
			bucket := d.scans[key]
			if bucket == nil || bucket.window != window {
				if bucket == nil && len(d.scans) >= 1024 {
					dropped++
					continue
				}
				if bucket != nil {
					d.ports -= len(bucket.ports)
				}
				bucket = &notificationBucket{window: window, ports: map[int]bool{}}
				d.scans[key] = bucket
			}
			if !bucket.ports[*event.Dport] && len(bucket.ports) < runtime.ScanPorts {
				if d.ports >= 65536 {
					dropped++
					continue
				}
				bucket.ports[*event.Dport] = true
				d.ports++
			}
			if len(bucket.ports) >= runtime.ScanPorts {
				emit("scan\x00"+key, "疑似端口扫描\n来源："+event.Src+"\n设备日志来源："+event.Sender+"\n在 "+strconv.Itoa(runtime.ScanWindow)+" 秒固定窗口内观察到至少 "+strconv.Itoa(len(bucket.ports))+" 个不同目标端口。")
			}
		}
	}
	return messages, dropped, suppressed
}

func notificationLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return ' '
		}
		switch r {
		case '<':
			return '＜'
		case '>':
			return '＞'
		case '@':
			return '＠'
		case '&':
			return '＆'
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80]) + "…"
	}
	if value == "" {
		return "未提供"
	}
	return value
}
