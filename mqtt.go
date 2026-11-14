package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type mqttStatus struct {
	Connected   bool    `json:"connected"`
	Published   int64   `json:"published"`
	LastSuccess float64 `json:"last_success"`
	LastError   string  `json:"last_error"`
}

type MQTTPublisher struct {
	settings atomic.Pointer[MQTTSettings]
	app      *App
	ctx      context.Context
	cancel   context.CancelFunc
	changed  chan struct{}
	requests chan chan error
	wg       sync.WaitGroup
	mu       sync.Mutex
	state    mqttStatus
}

func newMQTTPublisher(a *App, s MQTTSettings) *MQTTPublisher {
	ctx, cancel := context.WithCancel(context.Background())
	p := &MQTTPublisher{app: a, ctx: ctx, cancel: cancel, changed: make(chan struct{}, 1), requests: make(chan chan error)}
	p.settings.Store(&s)
	p.wg.Add(1)
	go p.run()
	return p
}

func (p *MQTTPublisher) close() { p.cancel(); p.wg.Wait() }
func (p *MQTTPublisher) status() mqttStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}
func (p *MQTTPublisher) setStatus(connected bool, err error, success bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Connected = connected
	p.state.LastError = ""
	if err != nil {
		p.state.LastError = err.Error()
	}
	if success {
		p.state.Published++
		p.state.LastSuccess = unixNow()
	}
}
func (p *MQTTPublisher) configure(s MQTTSettings) {
	p.settings.Store(&s)
	select {
	case p.changed <- struct{}{}:
	default:
	}
}
func (p *MQTTPublisher) publishNow(ctx context.Context) error {
	if !p.settings.Load().Enabled {
		return errors.New("请先启用并保存 MQTT 接入")
	}
	response := make(chan error, 1)
	select {
	case p.requests <- response:
	case <-ctx.Done():
		return errors.New("MQTT 尚未连接，请检查配置和接入状态")
	case <-p.ctx.Done():
		return errors.New("MQTT 接入已停止")
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		return errors.New("MQTT 发布超时，请查看接入状态")
	case <-p.ctx.Done():
		return errors.New("MQTT 接入已停止")
	}
}

func mqttWait(ctx context.Context, token mqtt.Token, message string) error {
	select {
	case <-ctx.Done():
		return errors.New(message)
	case <-token.Done():
		if token.Error() != nil {
			// Broker/library errors may contain an address or credentials.
			return errors.New(message)
		}
		return nil
	}
}

func mqttSend(ctx context.Context, client mqtt.Client, topic string, value any) error {
	var data []byte
	var err error
	if text, ok := value.(string); ok {
		data = []byte(text)
	} else if data, err = json.Marshal(value); err != nil {
		return errors.New("MQTT 数据编码失败")
	}
	return mqttWait(ctx, client.Publish(topic, 1, true, data), "MQTT 发布失败，请检查连接或主题权限")
}

func (p *MQTTPublisher) publish(client mqtt.Client, s MQTTSettings, discovery bool) (result error) {
	ctx, cancel := context.WithTimeout(p.ctx, 12*time.Second)
	defer cancel()
	defer func() {
		if result != nil && client.IsConnectionOpen() {
			offlineCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			_ = mqttSend(offlineCtx, client, mqttBase(s)+"/availability", "offline")
			stop()
		}
	}()
	if discovery {
		for _, entity := range mqttEntities {
			topic, config := mqttDiscovery(s, entity)
			if err := mqttSend(ctx, client, topic, config); err != nil {
				return err
			}
		}
	}
	queryCtx, stopQuery := boundedContext(ctx)
	snapshot, err := p.app.mqttSnapshot(queryCtx, time.Now())
	stopQuery()
	base := mqttBase(s)
	if err != nil {
		return err
	}
	for _, message := range []struct {
		topic string
		value any
	}{
		{base + "/top_sources", map[string]any{"items": snapshot.sources, "suspicious_items": snapshot.suspiciousSources, "business_items": snapshot.businessSources}},
		{base + "/top_ports", map[string]any{"items": snapshot.ports, "suspicious_items": snapshot.suspiciousPorts, "business_items": snapshot.businessPorts}},
		{base + "/recent_events", map[string]any{"items": snapshot.events}},
		{base + "/state", snapshot.state},
		{base + "/availability", "online"},
	} {
		if err := mqttSend(ctx, client, message.topic, message.value); err != nil {
			return err
		}
	}
	return nil
}

func (p *MQTTPublisher) session(s MQTTSettings) error {
	birth, lost := make(chan struct{}, 1), make(chan struct{}, 1)
	opts := mqtt.NewClientOptions().AddBroker(s.Broker).SetClientID("ros-watch-"+s.InstanceID).
		SetUsername(s.Username).SetPassword(s.Password).SetProtocolVersion(4).
		SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false).
		SetConnectTimeout(5*time.Second).SetWriteTimeout(5*time.Second).
		SetKeepAlive(30*time.Second).SetPingTimeout(5*time.Second).
		SetOrderMatters(false).SetWill(mqttBase(s)+"/availability", "offline", 1, true).
		SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	opts.SetConnectionLostHandler(func(mqtt.Client, error) {
		select {
		case lost <- struct{}{}:
		default:
		}
	})
	client := mqtt.NewClient(opts)
	defer func() {
		if client.IsConnectionOpen() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = mqttSend(ctx, client, mqttBase(s)+"/availability", "offline")
			cancel()
		}
		client.Disconnect(100)
		p.setStatus(false, nil, false)
	}()
	ctx, cancel := context.WithTimeout(p.ctx, 6*time.Second)
	err := mqttWait(ctx, client.Connect(), "MQTT 连接失败，请检查地址、账号、密码和证书")
	cancel()
	if err != nil {
		return err
	}
	ctx, cancel = context.WithTimeout(p.ctx, 5*time.Second)
	err = mqttWait(ctx, client.Subscribe(s.DiscoveryPrefix+"/status", 1, func(_ mqtt.Client, message mqtt.Message) {
		if string(message.Payload()) == "online" {
			select {
			case birth <- struct{}{}:
			default:
			}
		}
	}), "无法订阅 HA 启动主题，请检查主题权限")
	cancel()
	if err != nil {
		return err
	}
	err = p.publish(client, s, true)
	p.setStatus(true, err, err == nil)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		discovery := false
		var response chan error
		select {
		case <-p.ctx.Done():
			return nil
		case <-p.changed:
			return nil
		case <-lost:
			return errors.New("MQTT 连接中断，15 秒后重试")
		case response = <-p.requests:
			discovery = true
		case <-birth:
			discovery = true
		case <-ticker.C:
		}
		err = p.publish(client, s, discovery)
		p.setStatus(client.IsConnectionOpen(), err, err == nil)
		if response != nil {
			response <- err
		}
	}
}

func (p *MQTTPublisher) run() {
	defer p.wg.Done()
	for p.ctx.Err() == nil {
		s := *p.settings.Load()
		if !s.Enabled {
			p.setStatus(false, nil, false)
			select {
			case <-p.ctx.Done():
				return
			case <-p.changed:
				continue
			}
		}
		if err := p.session(s); err != nil && p.ctx.Err() == nil {
			p.setStatus(false, err, false)
			timer := time.NewTimer(15 * time.Second)
			select {
			case <-p.ctx.Done():
			case <-p.changed:
			case <-timer.C:
			}
			timer.Stop()
		}
	}
}
