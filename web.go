package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/securecookie"
)

//go:embed web/templates/*.html web/static/* routeros/ros-watch.rsc.example
var assets embed.FS

const cookieName = "ros_watch_session"

type session struct {
	Revision string `json:"revision"`
	CSRF     string `json:"csrf"`
}
type pageData struct {
	CSRF, Username, Version, Error string
	Changed                        bool
}
type authAttempt struct {
	times []time.Time
	last  time.Time
}
type cachedSummary struct {
	value map[string]any
	at    time.Time
}

type App struct {
	config        Config
	settings      atomic.Pointer[Settings]
	control       *Control
	store         *Store
	collector     *Collector
	geo           *GeoLookup
	templates     *template.Template
	cookies       *securecookie.SecureCookie
	authMu        sync.Mutex
	attempts      map[string]authAttempt
	totalAttempts []time.Time
	settingsMu    sync.Mutex
	queryMu       sync.Mutex
	cache         map[string]cachedSummary
	requests      chan struct{}
	expensive     chan struct{}
}

func newApp(config Config) (*App, error) {
	control, err := openControl(config.DataDir)
	if err != nil {
		return nil, err
	}
	settings, err := control.loadSettings(context.Background())
	if err != nil {
		control.db.Close()
		return nil, err
	}
	a := &App{config: config, control: control, attempts: map[string]authAttempt{}, cache: map[string]cachedSummary{}, requests: make(chan struct{}, 32), expensive: make(chan struct{}, 2)}
	a.settings.Store(&settings)
	a.store, err = openStore(config.DataDir, &a.settings)
	if err != nil {
		control.db.Close()
		return nil, err
	}
	a.collector = newCollector(a.store, &a.settings, config.Demo)
	a.templates, err = template.New("").ParseFS(assets, "web/templates/*.html")
	if err != nil {
		a.close()
		return nil, err
	}
	keyPath := filepath.Join(config.DataDir, "session.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = securecookie.GenerateRandomKey(32)
		if key == nil {
			a.close()
			return nil, errors.New("session randomness unavailable")
		}
		var f *os.File
		f, err = os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(key)
			_ = f.Close()
		}
	}
	if err != nil || len(key) < 32 {
		a.close()
		return nil, errors.New("session key unavailable or invalid")
	}
	a.cookies = securecookie.New(key, nil).MaxAge(43200).MaxLength(2048).SetSerializer(securecookie.JSONEncoder{})
	a.geo, err = newGeoLookup("geo")
	if err != nil {
		log.Print("IP location database unavailable; collection remains active")
	}
	return a, nil
}

func (a *App) close() {
	a.geo.close()
	if a.store != nil {
		a.store.close()
	}
	if a.control != nil {
		_ = a.control.db.Close()
	}
}
func jsonResponse(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, code int, message string) {
	jsonResponse(w, code, map[string]string{"error": message})
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /setup", a.setup)
	mux.HandleFunc("POST /setup", a.setup)
	mux.HandleFunc("GET /login", a.login)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.logout)
	mux.HandleFunc("GET /", a.index)
	mux.HandleFunc("GET /api/account", a.account)
	mux.HandleFunc("POST /api/account", a.account)
	mux.HandleFunc("GET /api/settings", a.runtimeSettings)
	mux.HandleFunc("POST /api/settings", a.runtimeSettings)
	mux.HandleFunc("POST /api/routeros-script", a.routerOSScript)
	mux.HandleFunc("GET /api/summary", a.summary)
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/export", a.export)
	static, _ := fs.Sub(assets, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		select {
		case a.requests <- struct{}{}:
			defer func() { <-a.requests }()
		default:
			apiError(w, 503, "服务繁忙，请稍后重试")
			return
		}
		defer func() {
			if recover() != nil {
				apiError(w, 500, "请求暂时无法处理")
			}
		}()
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		mux.ServeHTTP(w, r)
	})
}

func (a *App) readSession(r *http.Request) (session, bool) {
	value := session{}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return value, false
	}
	err = a.cookies.Decode(cookieName, cookie.Value, &value)
	return value, err == nil && len(value.CSRF) > 0 && len(value.CSRF) <= 128
}
func (a *App) writeSession(w http.ResponseWriter, s session) error {
	value, err := a.cookies.Encode(cookieName, s)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", MaxAge: 43200, Expires: time.Now().Add(12 * time.Hour), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.config.CookieSecure})
	return nil
}
func (a *App) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.config.CookieSecure})
}
func validCSRF(r *http.Request, s session) bool {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.PostFormValue("csrf")
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRF)) == 1
}

func (a *App) authorized(w http.ResponseWriter, r *http.Request) (Account, session, bool) {
	account := Account{}
	s, ok := a.readSession(r)
	if ok && s.Revision != "" {
		var err error
		ctx, cancel := boundedContext(r.Context())
		defer cancel()
		account, err = a.control.account(ctx)
		if err != nil {
			apiError(w, 503, "账号暂不可用")
			return account, s, false
		}
		ok = subtle.ConstantTimeCompare([]byte(s.Revision), []byte(account.Revision)) == 1
	} else {
		ok = false
	}
	if !ok {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiError(w, 401, "请先登录")
		} else {
			destination := "/login"
			ctx, cancel := boundedContext(r.Context())
			defer cancel()
			initialized, err := a.control.initialized(ctx)
			if err != nil {
				apiError(w, 503, "账号暂不可用")
				return account, s, false
			}
			if !initialized {
				destination = "/setup"
			}
			http.Redirect(w, r, destination, http.StatusSeeOther)
		}
	}
	return account, s, ok
}

func remoteKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "local"
	}
	return host
}
func recent(times []time.Time, after time.Time) []time.Time {
	filtered := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(after) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
func (a *App) reserveAttempt(r *http.Request) bool {
	now := time.Now()
	key := remoteKey(r)
	a.authMu.Lock()
	defer a.authMu.Unlock()
	a.totalAttempts = recent(a.totalAttempts, now.Add(-time.Minute))
	entry := a.attempts[key]
	entry.times = recent(entry.times, now.Add(-5*time.Minute))
	if len(entry.times) >= 5 || len(a.totalAttempts) >= 20 {
		return false
	}
	if len(a.attempts) >= 1024 {
		oldest := ""
		at := now
		for k, v := range a.attempts {
			if v.last.Before(at) {
				oldest = k
				at = v.last
			}
		}
		delete(a.attempts, oldest)
	}
	entry.times = append(entry.times, now)
	entry.last = now
	a.attempts[key] = entry
	a.totalAttempts = append(a.totalAttempts, now)
	return true
}
func (a *App) clearAttempts(r *http.Request) {
	a.authMu.Lock()
	delete(a.attempts, remoteKey(r))
	a.authMu.Unlock()
}

func (a *App) render(w http.ResponseWriter, name string, code int, data pageData) {
	var output strings.Builder
	if err := a.templates.ExecuteTemplate(&output, name, data); err != nil {
		apiError(w, 500, "页面暂不可用")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, output.String())
}

func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	initialized, err := a.control.initialized(ctx)
	if err != nil {
		apiError(w, 503, "账号暂不可用")
		return
	}
	if initialized {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s, valid := a.readSession(r)
	message, username, code := "", "admin", http.StatusOK
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || !valid || !validCSRF(r, s) {
			apiError(w, 400, "请从创建账号页面提交")
			return
		}
		username = strings.TrimSpace(r.PostFormValue("username"))
		password := r.PostFormValue("password")
		switch {
		case !usernamePattern.MatchString(username):
			message, code = "用户名须为 3–32 位字母、数字、点、下划线或短横线", 400
		case !validPassword(password, MinPasswordLength):
			message, code = "密码须为 8–128 位，可以使用长密码或口令", 400
		case password != r.PostFormValue("confirm_password"):
			message, code = "两次密码不一致", 400
		case !a.reserveAttempt(r):
			message, code = "尝试过于频繁，请稍后重试", 429
		default:
			created, err := a.control.createAccount(ctx, username, password)
			if err != nil {
				apiError(w, 503, "账号创建暂不可用，请重试")
				return
			}
			if !created {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			account, err := a.control.account(ctx)
			if err != nil {
				apiError(w, 503, "账号已创建，请前往登录页面登录")
				return
			}
			token, err := randomToken(24)
			if err != nil {
				apiError(w, 500, "账号已创建，请前往登录页面登录")
				return
			}
			if err = a.writeSession(w, session{Revision: account.Revision, CSRF: token}); err != nil {
				apiError(w, 500, "账号已创建，请前往登录页面登录")
				return
			}
			a.clearAttempts(r)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	if !valid {
		token, err := randomToken(24)
		if err != nil {
			apiError(w, 500, "创建账号页面暂不可用")
			return
		}
		s = session{CSRF: token}
	}
	if err := a.writeSession(w, s); err != nil {
		apiError(w, 500, "创建账号页面暂不可用")
		return
	}
	a.render(w, "setup.html", code, pageData{CSRF: s.CSRF, Username: username, Version: Version, Error: message})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := boundedContext(r.Context())
	initialized, err := a.control.initialized(ctx)
	cancel()
	if err != nil {
		apiError(w, 503, "账号暂不可用")
		return
	}
	if !initialized {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s, valid := a.readSession(r)
	message := ""
	code := 200
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || !valid || !validCSRF(r, s) {
			apiError(w, 400, "请从登录页面提交")
			return
		}
		if !a.reserveAttempt(r) {
			message = "尝试过于频繁，请稍后重试"
			code = 429
		} else {
			ctx, cancel := boundedContext(r.Context())
			account, ok, err := a.control.authenticate(ctx, strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password"))
			cancel()
			if err != nil {
				apiError(w, 503, "账号暂不可用")
				return
			}
			if ok {
				csrf, err := randomToken(24)
				if err != nil {
					apiError(w, 500, "登录暂不可用")
					return
				}
				if err = a.writeSession(w, session{Revision: account.Revision, CSRF: csrf}); err != nil {
					apiError(w, 500, "登录暂不可用")
					return
				}
				a.clearAttempts(r)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			message = "用户名或密码不正确"
			code = 401
		}
	}
	if !valid {
		csrf, err := randomToken(24)
		if err != nil {
			apiError(w, 500, "登录暂不可用")
			return
		}
		s = session{CSRF: csrf}
	}
	if err := a.writeSession(w, s); err != nil {
		apiError(w, 500, "登录暂不可用")
		return
	}
	a.render(w, "login.html", code, pageData{CSRF: s.CSRF, Version: Version, Error: message, Changed: r.URL.Query().Get("changed") == "1"})
}

func (a *App) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	account, s, ok := a.authorized(w, r)
	if !ok {
		return
	}
	a.render(w, "index.html", 200, pageData{CSRF: s.CSRF, Username: account.Username, Version: Version})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	_, s, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, s) {
		apiError(w, 400, "页面验证已过期")
		return
	}
	a.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	state, code := "ok", 200
	if a.collector.getError() != "" {
		state, code = "degraded", 503
	}
	jsonResponse(w, code, map[string]string{"status": state, "version": Version, "runtime": "go"})
}

func readJSON(r *http.Request, dest any) error {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return errors.New("请提交 JSON 数据")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dest); err != nil {
		return errors.New("提交数据的格式或字段无效")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("提交数据包含多余内容")
	}
	return nil
}

func (a *App) account(w http.ResponseWriter, r *http.Request) {
	account, s, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		jsonResponse(w, 200, map[string]string{"username": account.Username})
		return
	}
	if !validCSRF(r, s) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	if !a.reserveAttempt(r) {
		apiError(w, 429, "尝试过于频繁，请稍后重试")
		return
	}
	var body struct {
		Username string `json:"username"`
		Current  string `json:"current_password"`
		New      string `json:"new_password"`
		Confirm  string `json:"confirm_password"`
	}
	if err := readJSON(r, &body); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	if body.New != body.Confirm {
		apiError(w, 400, "两次新密码不一致")
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	changed, err := a.control.changeAccount(ctx, account.Revision, body.Current, strings.TrimSpace(body.Username), body.New)
	if err != nil {
		if !usernamePattern.MatchString(strings.TrimSpace(body.Username)) || body.New != "" && !validPassword(body.New, MinPasswordLength) || body.New == "" && strings.TrimSpace(body.Username) == account.Username {
			apiError(w, 400, err.Error())
		} else {
			apiError(w, 503, "账号更新暂不可用")
		}
		return
	}
	if !changed {
		apiError(w, 403, "当前密码不正确，请重新输入")
		return
	}
	a.clearAttempts(r)
	a.clearSession(w)
	jsonResponse(w, 200, map[string]string{"message": "账号已更新，请使用新账号信息重新登录", "redirect": "/login?changed=1"})
}

func (a *App) runtimeSettings(w http.ResponseWriter, r *http.Request) {
	_, s, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		jsonResponse(w, 200, map[string]any{"settings": a.settings.Load()})
		return
	}
	if !validCSRF(r, s) {
		apiError(w, 400, "页面验证已过期，请刷新后重试")
		return
	}
	var raw json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	settings, err := decodeSettings(raw)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	a.settingsMu.Lock()
	err = a.control.saveSettings(ctx, settings)
	if err == nil {
		a.settings.Store(&settings)
		a.queryMu.Lock()
		clear(a.cache)
		a.queryMu.Unlock()
		select {
		case a.collector.maintenance <- struct{}{}:
		default:
		}
	}
	a.settingsMu.Unlock()
	if err != nil {
		apiError(w, 503, "设置保存暂不可用")
		return
	}
	jsonResponse(w, 200, map[string]any{"settings": settings, "message": "设置已保存并生效；保留策略由后台清理任务执行"})
}

func (a *App) filters(r *http.Request) (Filters, error) {
	q := r.URL.Query()
	hours, err := strconv.ParseFloat(valueOr(q.Get("hours"), "24"), 64)
	limit := math.Min(float64(a.settings.Load().RetentionDays*24), 744)
	if err != nil || math.IsNaN(hours) || math.IsInf(hours, 0) || hours <= 0 || hours > limit {
		return Filters{}, errors.New("时间范围超出保留期限")
	}
	now := unixNow()
	f := Filters{Since: now - hours*3600, Until: now, Fields: map[string]any{}}
	choices := map[string]string{"scope": "|router|forward|", "outcome": "|blocked|attempt|", "proto": "|TCP|UDP|ICMP|ICMPV6|IPV6-ICMP|GRE|ESP|"}
	for _, key := range []string{"src", "dport", "wan", "scope", "outcome", "proto"} {
		value := strings.TrimSpace(q.Get(key))
		if value == "" {
			continue
		}
		switch key {
		case "src":
			address, err := netip.ParseAddr(value)
			if err != nil {
				return f, err
			}
			f.Fields[key] = address.String()
		case "dport":
			port, err := strconv.Atoi(value)
			if err != nil || port < 0 || port > 65535 {
				return f, errors.New("invalid port")
			}
			f.Fields[key] = port
		case "wan":
			if len(value) > 256 {
				return f, errors.New("invalid WAN")
			}
			f.Fields[key] = value
		default:
			if !strings.Contains(choices[key], "|"+value+"|") {
				return f, errors.New("invalid filter")
			}
			f.Fields[key] = value
		}
	}
	return f, nil
}
func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
func (a *App) acquireQuery(w http.ResponseWriter) bool {
	select {
	case a.expensive <- struct{}{}:
		return true
	default:
		apiError(w, 503, "查询繁忙，请稍后重试")
		return false
	}
}
func filterError(w http.ResponseWriter) {
	apiError(w, 400, "筛选条件无效，请检查 IP、端口和时间范围")
}
func queryError(w http.ResponseWriter) {
	apiError(w, 503, "查询暂不可用，请缩小时间范围后重试")
}

func (a *App) summary(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	f, err := a.filters(r)
	if err != nil {
		filterError(w)
		return
	}
	if !a.acquireQuery(w) {
		return
	}
	defer func() { <-a.expensive }()
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	fields, _ := json.Marshal(f.Fields)
	key := strconv.FormatInt(int64(f.Since)/10, 10) + ":" + strconv.FormatInt(int64(f.Until)/10, 10) + string(fields)
	a.queryMu.Lock()
	cached, ok := a.cache[key]
	var data map[string]any
	if ok && time.Since(cached.at) < 10*time.Second {
		data = cached.value
	} else {
		if ctx.Err() == nil {
			data, err = a.store.summary(ctx, f)
		} else {
			err = ctx.Err()
		}
		if err == nil {
			if len(a.cache) >= 32 {
				oldKey := ""
				oldAt := time.Now()
				for k, v := range a.cache {
					if v.at.Before(oldAt) {
						oldKey = k
						oldAt = v.at
					}
				}
				delete(a.cache, oldKey)
			}
			a.cache[key] = cachedSummary{data, time.Now()}
		}
	}
	a.queryMu.Unlock()
	if err != nil {
		queryError(w)
		return
	}
	response := make(map[string]any, len(data)+2)
	for k, v := range data {
		response[k] = v
	}
	response["status"] = a.collector.status()
	response["generated_at"] = unixNow()
	response["locations"] = a.locations(data["sources"].([]map[string]any), data["scans"].([]map[string]any))
	response["geo"] = map[string]any{"available": a.geo != nil, "source": "ip2region", "version": geoDataVersion}
	jsonResponse(w, 200, response)
}

func (a *App) events(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	f, err := a.filters(r)
	if err != nil {
		filterError(w)
		return
	}
	var before int64
	if value := r.URL.Query().Get("before"); value != "" {
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before <= 0 {
			filterError(w)
			return
		}
	}
	if !a.acquireQuery(w) {
		return
	}
	defer func() { <-a.expensive }()
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	rows, err := a.store.events(ctx, f, 51, before)
	if err != nil {
		queryError(w)
		return
	}
	var next any
	if len(rows) > 50 {
		next = rows[49].ID
		rows = rows[:50]
	}
	type locatedEvent struct {
		Event
		Location IPLocation `json:"src_location"`
	}
	views := make([]locatedEvent, 0, len(rows))
	for _, row := range rows {
		views = append(views, locatedEvent{row, a.geo.location(row.Src)})
	}
	jsonResponse(w, 200, map[string]any{"rows": views, "next": next})
}

func (a *App) export(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	f, err := a.filters(r)
	if err != nil {
		filterError(w)
		return
	}
	if !a.acquireQuery(w) {
		return
	}
	defer func() { <-a.expensive }()
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	rows, err := a.store.events(ctx, f, 10000, 0)
	if err != nil {
		queryError(w)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=ros-watch.csv")
	_, _ = io.WriteString(w, "\ufeff")
	csvWriter := csv.NewWriter(w)
	_ = csvWriter.Write([]string{"ts", "src", "sport", "dst", "dport", "proto", "wan", "scope", "outcome", "src_location", "src_isp"})
	for _, e := range rows {
		location := a.geo.location(e.Src)
		record := []string{strconv.FormatFloat(e.TS, 'f', 6, 64), e.Src, portText(e.Sport), e.Dst, portText(e.Dport), e.Proto, e.WAN, e.Scope, e.Outcome, location.Label, location.ISP}
		for i, value := range record {
			if len(value) > 0 && strings.ContainsRune("=+-@", rune(value[0])) {
				record[i] = "'" + value
			}
		}
		if csvWriter.Write(record) != nil {
			return
		}
	}
	csvWriter.Flush()
}
