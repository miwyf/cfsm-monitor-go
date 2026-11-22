package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"
)

//go:embed web/*
var assets embed.FS

type settings struct {
	URL           string `json:"url"`
	Key           string `json:"key"`
	PollSeconds   int    `json:"poll_seconds"`
	RetentionDays int    `json:"retention_days"`
}

type session struct{ Expires time.Time }
type loginWindow struct {
	Started time.Time
	Count   int
}

type app struct {
	mu           sync.RWMutex
	fetchMu      sync.Mutex
	config       settings
	servers      []server
	fetchedAt    time.Time
	available    bool
	ledger       map[string]*record
	sessions     map[string]session
	attempts     map[string]loginWindow
	username     string
	passwordHash []byte
	salt         string
	secure       bool
	dir          string
	zone         *time.Location
	client       *http.Client
	changed      chan struct{}
	db           *sql.DB
	dirty        map[dayKey]bool
	startedAt    time.Time
	lastAttempt  time.Time
	nextPoll     time.Time
	lastError    string
	failures     int
	fetchCount   int
	collecting   bool
	notifyMu     sync.Mutex
	notifyWake   chan struct{}
	notifyClient *http.Client
	update       *updateManager
}

func newApp(dir string, secure bool, zone *time.Location) (*app, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	a := &app{dir: dir, secure: secure, zone: zone, config: settings{PollSeconds: 300, RetentionDays: 90},
		ledger: map[string]*record{}, sessions: map[string]session{}, attempts: map[string]loginWindow{},
		changed: make(chan struct{}, 1), notifyWake: make(chan struct{}, 1), dirty: map[dayKey]bool{}, startedAt: time.Now(),
		notifyClient: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		client:       &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := a.openStore(); err != nil {
		if a.db != nil {
			a.db.Close()
		}
		return nil, err
	}
	return a, nil
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func validateSettings(c *settings) error {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return errors.New("CFSM 地址必须是 http/https 站点根地址，不能带路径、账号或参数")
	}
	c.URL = strings.TrimRight(u.String(), "/")
	if len(c.Key) < 16 || len(c.Key) > 32 || strings.ContainsAny(c.Key, "\r\n") {
		return errors.New("App 访问口令应为 16–32 位")
	}
	if c.RetentionDays < 30 || c.RetentionDays > 3650 {
		return errors.New("历史保留范围为 30–3650 天")
	}
	if c.PollSeconds < 60 || c.PollSeconds > 86400 {
		return errors.New("采集间隔应为 60–86400 秒")
	}
	return nil
}

func (a *app) fetch(ctx context.Context, c settings) ([]server, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", c.URL+"/api/servers", nil)
	if err != nil {
		return nil, errors.New("无法创建 CFSM 请求")
	}
	r.Header.Set("X-App-Password", c.Key)
	r.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(r)
	if err != nil {
		return nil, errors.New("无法连接 CFSM，请检查地址及网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CFSM 返回 HTTP %d，请检查 App 访问口令或站点验证设置", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
	if err != nil || len(b) > 16*1024*1024 {
		return nil, errors.New("CFSM 响应读取失败或过大")
	}
	var payload struct {
		Servers *[]map[string]any `json:"servers"`
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if err := d.Decode(&payload); err != nil || payload.Servers == nil {
		return nil, errors.New("CFSM 响应不是有效的节点列表，请检查 App 口令和 Cloudflare 验证")
	}
	items := make([]server, 0, len(*payload.Servers))
	for _, raw := range *payload.Servers {
		if s, ok := normalizeServer(raw); ok {
			items = append(items, s)
		}
	}
	return items, nil
}

// One upstream batch for all browser clients. A failed poll closes public access
// instead of continuing to expose a node whose visibility may have changed.
func (a *app) refresh(ctx context.Context) {
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	_ = a.collectLocked(ctx)
}

func (a *app) collectLocked(ctx context.Context) error {
	a.mu.Lock()
	c := a.config
	if c.URL == "" {
		a.mu.Unlock()
		return errors.New("请先配置数据源")
	}
	a.collecting = true
	a.lastAttempt = time.Now()
	a.mu.Unlock()
	items, err := a.fetch(ctx, c)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.collecting = false
	a.fetchCount++
	if err == nil {
		err = a.accept(items, time.Now())
	}
	if err != nil {
		a.available = false
		a.failures++
		a.lastError = err.Error()
		log.Print("CFSM collection failed")
		return err
	}
	a.failures = 0
	a.lastError = ""
	return nil
}

// The snapshot and daily counters become visible only after SQLite commits.
func (a *app) accept(items []server, now time.Time) error {
	return a.commitSnapshot(items, now, nil)
}

func (a *app) poll(ctx context.Context) {
	a.refresh(ctx)
	for {
		a.mu.RLock()
		seconds := a.config.PollSeconds
		a.mu.RUnlock()
		a.mu.Lock()
		a.nextPoll = time.Now().Add(time.Duration(seconds) * time.Second)
		a.mu.Unlock()
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.changed:
			timer.Stop()
		case <-timer.C:
			a.refresh(ctx)
		}
	}
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func (a *app) authenticated(r *http.Request) bool {
	c, err := r.Cookie("cfsm_session")
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	if ok && time.Now().Before(s.Expires) {
		return true
	}
	delete(a.sessions, c.Value)
	return false
}

func (a *app) cookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "cfsm_session", Value: value, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"version": appVersion()})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			problem(w, 503, "SQLite unavailable")
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]bool{"admin": a.authenticated(r), "setup_required": a.setupRequired()})
	})
	mux.HandleFunc("POST /api/setup", a.setup)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("cfsm_session"); err == nil {
			a.mu.Lock()
			delete(a.sessions, c.Value)
			a.mu.Unlock()
		}
		a.cookie(w, "", -1)
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/servers", a.list)
	mux.HandleFunc("GET /api/settings", a.getSettings)
	a.adminRoutes(mux)
	a.notificationRoutes(mux)
	mux.HandleFunc("POST /api/settings", a.saveSettings)
	web, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(web))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && a.setupRequired() {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/admin" {
			clone := r.Clone(r.Context())
			u := *r.URL
			u.Path = "/admin.html"
			clone.URL = &u
			files.ServeHTTP(w, clone)
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/traffic.mjs" && r.URL.Path != "/theme.mjs" && r.URL.Path != "/style.css" && r.URL.Path != "/theme.css" && r.URL.Path != "/admin.js" && r.URL.Path != "/admin.css" && r.URL.Path != "/appearance.js" && r.URL.Path != "/notifications.js" {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method == "POST" {
			// Custom header prevents simple cross-origin requests, including login
			// CSRF. No CORS is enabled. Origin check is independent of proxy headers.
			if r.Header.Get("X-CFSM-Request") != "1" {
				problem(w, 403, "请求校验失败")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
					problem(w, 403, "请求来源不匹配")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problem(w, 400, "请求格式错误")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		problem(w, 400, "请求格式错误")
		return false
	}
	return true
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if a.setupRequired() {
		problem(w, 409, "请先创建管理员账号")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	a.mu.Lock()
	for key, win := range a.attempts {
		if now.Sub(win.Started) > 5*time.Minute {
			delete(a.attempts, key)
		}
	}
	blocked := false
	for _, key := range []string{"global", "ip:" + ip} {
		win := a.attempts[key]
		limit := 10
		if key == "global" {
			limit = 100
		}
		if win.Count == 0 {
			win.Started = now
		}
		win.Count++
		a.attempts[key] = win
		if win.Count > limit {
			blocked = true
			break
		}
	}
	a.mu.Unlock()
	if blocked {
		w.Header().Set("Retry-After", "300")
		problem(w, 429, "尝试过于频繁，请稍后再试")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	a.mu.RLock()
	salt := a.salt
	hash, _ := pbkdf2.Key(sha256.New, input.Password, []byte(salt), 600000, 32)
	userHash, expectedUser := sha256.Sum256([]byte(input.Username)), sha256.Sum256([]byte(a.username))
	valid := subtle.ConstantTimeCompare(hash, a.passwordHash)&subtle.ConstantTimeCompare(userHash[:], expectedUser[:]) == 1
	a.mu.RUnlock()
	if !valid {
		problem(w, 401, "账号或密码错误")
		return
	}
	token := randomToken()
	a.mu.Lock()
	if a.salt != salt {
		a.mu.Unlock()
		problem(w, 401, "账号已更新，请重新登录")
		return
	}
	for key, s := range a.sessions {
		if now.After(s.Expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 32 {
		a.sessions = map[string]session{}
	}
	a.sessions[token] = session{now.Add(12 * time.Hour)}
	a.mu.Unlock()
	a.cookie(w, token, 12*60*60)
	respond(w, 200, map[string]bool{"admin": true})
}

func (a *app) list(w http.ResponseWriter, r *http.Request) {
	admin := a.authenticated(r)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.available {
		message := "数据暂不可用，请稍后重试"
		if a.config.URL == "" {
			message = "等待管理员配置 CFSM 数据源"
		}
		problem(w, 503, message)
		return
	}
	items := make([]server, 0, len(a.servers))
	for _, s := range a.servers {
		if s.Hidden && !admin {
			continue
		}
		s.Days = a.history(s.ID, time.Now())
		s.Reset = nextReset(s.ResetDay, a.zone, time.Now())
		items = append(items, s)
	}
	respond(w, 200, map[string]any{"servers": items, "admin": admin, "fetched_at": a.fetchedAt, "poll_seconds": a.config.PollSeconds, "time_zone": a.zone.String()})
}

func (a *app) getSettings(w http.ResponseWriter, r *http.Request) {
	if !a.authenticated(r) {
		problem(w, 401, "请先登录管理员")
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	respond(w, 200, map[string]any{"url": a.config.URL, "key_configured": a.config.Key != "", "poll_seconds": a.config.PollSeconds, "retention_days": a.config.RetentionDays})
}

func (a *app) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !a.authenticated(r) {
		problem(w, 401, "请先登录管理员")
		return
	}
	var input settings
	if !decodeBody(w, r, &input) {
		return
	}
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	a.mu.RLock()
	previous := a.config
	a.mu.RUnlock()
	if input.Key == "" {
		input.Key = previous.Key
	}
	if input.RetentionDays == 0 {
		input.RetentionDays = previous.RetentionDays
	}
	if err := validateSettings(&input); err != nil {
		problem(w, 400, err.Error())
		return
	}
	items, err := a.fetch(r.Context(), input)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	a.mu.Lock()
	err = a.commitSnapshot(items, time.Now(), &input)
	if err == nil {
		a.lastAttempt = time.Now()
		a.failures = 0
		a.lastError = ""
		a.fetchCount++
	}
	a.mu.Unlock()
	if err != nil {
		problem(w, 500, "SQLite 写入失败，请检查数据卷")
		return
	}
	select {
	case a.changed <- struct{}{}:
	default:
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func main() {
	os.Exit(run())
}

func run() int {
	showVersion := flag.Bool("version", false, "print version and exit")
	showCompat := flag.Bool("update-compat", false, "print container update compatibility and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("cfsm-monitor-go v%s\n", appVersion())
		return 0
	}
	if *showCompat {
		fmt.Println(updateCompat())
		return 0
	}
	zoneName := os.Getenv("TZ")
	if zoneName == "" {
		zoneName = "Asia/Shanghai"
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		log.Fatal("Invalid TZ")
	}
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "./data"
	}
	secure := os.Getenv("COOKIE_SECURE") == "true"
	a, err := newApp(dir, secure, zone)
	if err != nil {
		log.Fatal(err)
	}
	defer a.db.Close()
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restart := make(chan struct{}, 1)
	if updateDir := os.Getenv("CFSM_UPDATE_DIR"); updateDir != "" && runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		a.update = newUpdateManager(ctx, updateDir, restart)
	}
	srv := &http.Server{Addr: addr, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Print(err)
		return 1
	}
	defer listener.Close()
	if ready := os.Getenv("CFSM_UPDATE_READY"); ready != "" {
		if err := a.db.PingContext(ctx); err != nil {
			log.Printf("Upgrade database check: %v", err)
			return 1
		}
		if err := os.WriteFile(ready, nil, 0600); err != nil {
			log.Printf("Upgrade readiness: %v", err)
			return 1
		}
	}
	var workers sync.WaitGroup
	workers.Go(func() { a.poll(ctx) })
	workers.Go(func() { a.notificationLoop(ctx) })
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	log.Printf("CFSM Monitor v%s listening on %s", appVersion(), addr)
	exit := 0
	select {
	case <-ctx.Done():
	case <-restart:
		exit = updateExitCode
	case err := <-serveErr:
		log.Printf("HTTP server: %v", err)
		exit = 1
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
	}
	workers.Wait()
	return exit
}

func number(v any) *float64 {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case json.Number:
		s = x.String()
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f != f || f > 1e30 {
		return nil
	}
	return &f
}

func textValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}
