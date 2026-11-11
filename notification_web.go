package main

import (
	"context"
	"net/http"
	"time"
)

func (a *App) notificationSettings(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		jsonResponse(w, 200, map[string]any{"settings": a.notifier.settings.Load().public(), "status": a.notifier.status()})
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body struct {
		NotificationSettings
		ClearSecret bool `json:"clear_secret"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	current := a.notifier.settings.Load()
	s := body.NotificationSettings
	if s.Platform == current.Platform {
		if s.Webhook == "" {
			s.Webhook = current.Webhook
		}
		if s.Secret == "" && !body.ClearSecret {
			s.Secret = current.Secret
		}
	}
	if body.ClearSecret {
		s.Secret = ""
	}
	if err := s.validate(); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	if err := a.control.saveNotifications(ctx, s); err != nil {
		apiError(w, 503, "通知设置保存失败")
		return
	}
	a.notifier.settings.Store(&s)
	jsonResponse(w, 200, map[string]any{"message": "通知设置已保存", "settings": s.public()})
}

func (a *App) testNotification(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	s := a.notifier.settings.Load()
	if _, err := s.endpoint(); err != nil {
		apiError(w, 400, "请先保存有效的 Webhook 地址")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := a.notifier.deliver(ctx, s, "ROS Watch 告警 · 测试通知\n通知通道连接测试。", true); err != nil {
		apiError(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]string{"message": "平台已接受测试通知，请到群内确认"})
}
