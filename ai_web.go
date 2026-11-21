package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *App) aiConfiguration(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	if r.Method == http.MethodGet {
		settings, err := a.control.loadAI(ctx)
		if err != nil {
			apiError(w, 503, "AI 设置暂不可用")
			return
		}
		jsonResponse(w, 200, settings.public())
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var next aiSettings
	if err := readJSON(r, &next); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	previous, err := a.control.loadAI(ctx)
	if err != nil {
		apiError(w, 503, "AI 设置暂不可用")
		return
	}
	if err = next.validate(previous); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	if err = a.control.saveAI(ctx, next); err != nil {
		apiError(w, 503, "AI 设置保存失败")
		return
	}
	jsonResponse(w, 200, next.public())
}

func (a *App) aiModelList(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body aiProfile
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	base, err := normalizeAIBase(body.BaseURL)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	key := strings.TrimSpace(body.APIKey)
	if body.ClearKey {
		key = ""
	} else if key == "" && body.ID != "" {
		settings, err := a.control.loadAI(ctx)
		if err != nil {
			apiError(w, 503, "AI 设置暂不可用")
			return
		}
		if saved, ok := settings.profile(body.ID); ok && saved.BaseURL == base {
			key = saved.APIKey
		}
	}
	if !validAIKey(key) {
		apiError(w, 400, "模型 API Key 格式无效")
		return
	}
	select {
	case a.ai.inflight <- struct{}{}:
		defer func() { <-a.ai.inflight }()
	default:
		apiError(w, 503, "AI 服务繁忙，请稍后再试")
		return
	}
	headers := map[string]string{}
	if key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	raw, err := a.ai.readHTTP(ctx, http.MethodGet, base+"/models", nil, headers, 4<<20)
	if err != nil {
		apiError(w, 502, "模型列表读取失败（"+err.Error()+"），可继续手动填写模型 ID")
		return
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Data == nil {
		apiError(w, 502, "服务未返回兼容的模型列表，可继续手动填写模型 ID")
		return
	}
	models := []string{}
	for _, entry := range result.Data {
		id := strings.TrimSpace(entry.ID)
		if id != "" && len(id) <= 256 && !strings.ContainsAny(id, "\r\n\x00") {
			models = append(models, id)
		}
	}
	slices.Sort(models)
	models = slices.Compact(models)
	jsonResponse(w, 200, map[string]any{"models": models[:min(len(models), 2000)], "total": len(models)})
}

func (a *App) aiConnectionTest(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body struct {
		Profile string `json:"profile"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	settings, err := a.control.loadAI(r.Context())
	if err != nil {
		apiError(w, 503, "AI 设置暂不可用")
		return
	}
	profile, ok := settings.profile(body.Profile)
	if !ok {
		apiError(w, 400, "请先保存模型连接")
		return
	}
	select {
	case a.ai.inflight <- struct{}{}:
		defer func() { <-a.ai.inflight }()
	default:
		apiError(w, 503, "AI 分析繁忙，请稍后再试")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(aiRequestBudget + 5*time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), aiRequestBudget)
	defer cancel()
	if err := a.ai.test(ctx, profile); err != nil {
		apiError(w, 502, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]string{"message": "工具调用与结果回传测试通过"})
}

func (a *App) aiCustomIntelTest(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body struct {
		IP string `json:"ip"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	ip, err := aiAddress(body.IP)
	if err != nil || !aiPublicIP(ip) {
		apiError(w, 400, "请输入一个有效的公网 IP")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
	defer cancel()
	settings, err := a.control.loadAI(ctx)
	if err != nil {
		apiError(w, 503, "AI 设置暂不可用")
		return
	}
	custom := settings.Reputation.Custom
	if !custom.Enabled {
		apiError(w, 400, "请先启用并保存自定义情报接口")
		return
	}
	select {
	case a.ai.inflight <- struct{}{}:
		defer func() { <-a.ai.inflight }()
	default:
		apiError(w, 503, "AI 服务繁忙，请稍后再试")
		return
	}
	jsonResponse(w, 200, a.ai.customIntel(ctx, ip, custom))
}

func (a *App) aiChat(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, session) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var body struct {
		Profile      string `json:"profile"`
		Conversation string `json:"conversation"`
		Question     string `json:"question"`
		Offset       int    `json:"timezone_offset_minutes"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	body.Question = strings.TrimSpace(body.Question)
	if body.Question == "" || utf8.RuneCountInString(body.Question) > 2000 || len(body.Conversation) > 64 || body.Offset < -840 || body.Offset > 840 {
		apiError(w, 400, "请输入 1 至 2000 个字符的分析问题")
		return
	}
	settings, err := a.control.loadAI(r.Context())
	if err != nil {
		apiError(w, 503, "AI 设置暂不可用")
		return
	}
	profile, ok := settings.profile(body.Profile)
	if !ok {
		apiError(w, 400, "请先保存模型连接")
		return
	}
	select {
	case a.ai.inflight <- struct{}{}:
		defer func() { <-a.ai.inflight }()
	default:
		apiError(w, 503, "AI 分析繁忙，请稍后再试")
		return
	}
	id, conversation, history, err := a.ai.begin(session.CSRF, body.Conversation, profile)
	if err != nil {
		apiError(w, 409, err.Error())
		return
	}
	var turn []aiMessage
	defer func() { a.ai.end(conversation, turn) }()
	if aiWantsBlockCommands(body.Question) {
		answer, sources := aiBlockCommands(body.Question, history, a.config.Demo)
		turn = []aiMessage{{Role: "user", Content: body.Question}, {Role: "assistant", Content: answer}}
		jsonResponse(w, 200, map[string]any{
			"conversation": id, "answer": answer, "block_sources": sources, "model": "ROS Watch",
		})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(aiRequestBudget + 5*time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), aiRequestBudget)
	defer cancel()
	zone := time.FixedZone("", body.Offset*60)
	result, answer, evidence, err := a.aiRun(ctx, profile, settings.Reputation, history, body.Question, zone)
	if err != nil {
		apiError(w, 502, err.Error())
		return
	}
	turn = result
	jsonResponse(w, 200, map[string]any{"conversation": id, "answer": answer, "evidence": evidence, "model": profile.Name})
}
