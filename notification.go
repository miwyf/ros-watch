package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const notificationCooldown = 10 * time.Minute
const notificationInterval = 5 * time.Second

type notification struct {
	settings *NotificationSettings
	text     string
}
type observedBatch struct {
	settings *NotificationSettings
	events   []Event
}
type notificationStatus struct {
	Sent            int64   `json:"sent"`
	Failed          int64   `json:"failed"`
	Dropped         int64   `json:"dropped"`
	Suppressed      int64   `json:"suppressed"`
	ObservedDropped int64   `json:"observed_dropped"`
	LastAttempt     float64 `json:"last_attempt"`
	LastSuccess     float64 `json:"last_success"`
	LastError       string  `json:"last_error"`
}
type Notifier struct {
	settings  atomic.Pointer[NotificationSettings]
	collector *Collector
	geo       *GeoLookup
	client    *http.Client
	batches   chan observedBatch
	queue     chan notification
	gate      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.Mutex
	state     notificationStatus
	nextSend  time.Time // guarded by gate
}

func newNotifier(s NotificationSettings, collector *Collector, geo *GeoLookup) *Notifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{collector: collector, geo: geo, batches: make(chan observedBatch, 8), queue: make(chan notification, 32), gate: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
		client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	n.settings.Store(&s)
	n.wg.Add(2)
	go n.monitor()
	go n.sender()
	return n
}

func (n *Notifier) close() {
	n.cancel()
	n.wg.Wait()
	n.client.CloseIdleConnections()
}
func (n *Notifier) status() notificationStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}
func (n *Notifier) counts(dropped, suppressed int64) {
	n.mu.Lock()
	n.state.ObservedDropped += dropped
	n.state.Suppressed += suppressed
	n.mu.Unlock()
}
func (n *Notifier) observe(events []Event) {
	s := n.settings.Load()
	if !s.Enabled {
		return
	}
	select {
	case n.batches <- observedBatch{s, events}:
	default:
		n.counts(int64(len(events)), 0)
	}
}
func (n *Notifier) offer(s *NotificationSettings, text string) {
	select {
	case n.queue <- notification{s, text}:
	default:
		n.mu.Lock()
		n.state.Dropped++
		n.mu.Unlock()
	}
}
func (n *Notifier) monitor() {
	defer n.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	detector := notificationDetector{geo: n.geo}
	var businessRules []BusinessRule
	var businessRevision uint64
	businessLoaded := false
	var previous *NotificationSettings
	storageState := ""
	for {
		select {
		case <-n.ctx.Done():
			return
		case batch := <-n.batches:
			if batch.settings != n.settings.Load() || !batch.settings.Enabled {
				continue
			}
			if batch.settings.Scans {
				revision := n.collector.store.businessRevision.Load()
				if !businessLoaded || revision != businessRevision {
					ctx, cancel := boundedContext(n.ctx)
					rules, err := n.collector.store.businessRules(ctx)
					cancel()
					if err != nil {
						n.counts(int64(len(batch.events)), 0)
						continue
					}
					businessRules, businessRevision, businessLoaded = rules, revision, true
					detector.scans, detector.ports = map[string]*notificationBucket{}, 0
				}
				// Never mutate the collector's batch; other consumers may still use it.
				batch.events = append([]Event(nil), batch.events...)
				for i := range batch.events {
					for _, rule := range businessRules {
						if rule.matches(batch.events[i]) {
							batch.events[i].Business = rule.Name
							break
						}
					}
				}
			}
			messages, dropped, suppressed := detector.process(batch.settings, n.collector.settings.Load(), batch.events, time.Now())
			n.counts(dropped, suppressed)
			for _, message := range messages {
				n.offer(batch.settings, message)
			}
		case <-ticker.C:
			s := n.settings.Load()
			if s != previous {
				storageState, previous = "", s
			}
			if !s.Enabled || !s.Storage {
				continue
			}
			state := n.collector.getError()
			space := n.collector.store.storageStatus()["space_state"].(string)
			if state == "" && space != "normal" {
				state = space
			}
			if message := storageNotification(storageState, state); message != "" {
				n.offer(s, message)
			}
			storageState = state
		}
	}
}

func storageNotification(previous, current string) string {
	if previous == current {
		return ""
	}
	text := map[string]string{"disk_low": "日志写入已暂停：可用磁盘空间不足或磁盘状态无法读取。", "low_space": "可用磁盘空间低于设置阈值，请检查磁盘不足处理状态。", "unknown": "无法读取磁盘空间状态，请检查存储。", "storage_failed": "日志写入失败，请检查存储。", "maintenance_failed": "日志保留或空间清理失败，请检查存储。", "receiver_failed": "日志接收器已停止，请检查服务。"}[current]
	if current == "" && previous != "" {
		text = "日志接收与存储检查已恢复正常。"
	}
	if text == "" {
		return ""
	}
	return "ROS Watch 告警 · 接收与存储\n" + text
}

func (n *Notifier) sender() {
	defer n.wg.Done()
	for {
		select {
		case <-n.ctx.Done():
			return
		case message := <-n.queue:
			for attempt := 0; attempt < 3; attempt++ {
				if n.ctx.Err() != nil || n.settings.Load() != message.settings || !message.settings.Enabled {
					break
				}
				if n.deliver(n.ctx, message.settings, message.text, false) == nil {
					break
				}
			}
		}
	}
}

// One gate applies to automatic alerts, retries and user-triggered tests alike.
func (n *Notifier) deliver(ctx context.Context, s *NotificationSettings, text string, test bool) error {
	select {
	case n.gate <- struct{}{}:
		defer func() { <-n.gate }()
	case <-ctx.Done():
		return errors.New("通知发送等待超时，请稍后重试")
	}
	if wait := time.Until(n.nextSend); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return errors.New("通知发送等待超时，请稍后重试")
		}
	}
	if (!test && (!s.Enabled || n.settings.Load() != s)) || ctx.Err() != nil {
		return errors.New("通知已取消")
	}
	now := time.Now()
	n.nextSend = now.Add(notificationInterval)
	err := sendNotification(ctx, n.client, *s, text, now)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state.LastAttempt = float64(now.UnixNano()) / 1e9
	if err != nil {
		n.state.Failed++
		n.state.LastError = err.Error()
	} else {
		n.state.Sent++
		n.state.LastSuccess = unixNow()
		n.state.LastError = ""
	}
	return err
}

func sendNotification(ctx context.Context, client *http.Client, s NotificationSettings, text string, now time.Time) error {
	u, err := s.endpoint()
	if err != nil {
		return err
	}
	content := map[string]string{"content": text}
	payload := map[string]any{"msgtype": "text", "text": content}
	if s.Platform == "feishu" {
		payload = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		if s.Secret != "" {
			timestamp := strconv.FormatInt(now.Unix(), 10)
			mac := hmac.New(sha256.New, []byte(timestamp+"\n"+s.Secret))
			payload["timestamp"], payload["sign"] = timestamp, base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	} else if s.Platform == "dingtalk" && s.Secret != "" {
		timestamp := strconv.FormatInt(now.UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(s.Secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + s.Secret))
		query := u.Query()
		query.Set("timestamp", timestamp)
		query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		u.RawQuery = query.Encode()
	}
	data, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return errors.New("通知请求创建失败")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		// Never expose URL errors, as their query/path contains the bot credential.
		return errors.New("通知请求失败，请检查网络、证书或平台可用性")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("通知平台返回 HTTP %d", response.StatusCode)
	}
	var result map[string]json.RawMessage
	if err = json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result); err != nil {
		return errors.New("通知平台响应格式无效")
	}
	keys := []string{"errcode"}
	if s.Platform == "feishu" {
		keys = []string{"code", "StatusCode"}
	}
	found := false
	for _, key := range keys {
		if raw, exists := result[key]; exists {
			var code int
			if string(raw) == "null" || json.Unmarshal(raw, &code) != nil {
				return errors.New("通知平台响应格式无效")
			}
			found = true
			if code != 0 {
				return fmt.Errorf("通知平台拒绝请求（错误码 %d），请检查机器人安全设置", code)
			}
		}
	}
	if !found {
		return errors.New("通知平台未返回有效的成功标记")
	}
	return nil
}
