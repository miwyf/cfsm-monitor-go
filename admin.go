package main

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *app) adminOnly(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			problem(w, 401, "请先登录管理员")
			return
		}
		handler(w, r)
	}
}

func (a *app) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/status", a.adminOnly(a.adminStatus))
	mux.HandleFunc("GET /api/admin/history", a.adminOnly(a.adminHistory))
	mux.HandleFunc("POST /api/admin/collect", a.adminOnly(a.collectNow))
	mux.HandleFunc("POST /api/admin/retention", a.adminOnly(a.saveRetention))
	mux.HandleFunc("POST /api/admin/clear-history", a.adminOnly(a.deleteHistory))
	mux.HandleFunc("POST /api/admin/account", a.adminOnly(a.changeAccount))
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (a *app) adminStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var version string
	var journal string
	var count int
	var first, last sql.NullString
	if err := a.db.QueryRowContext(r.Context(), `SELECT sqlite_version()`).Scan(&version); err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	if err := a.db.QueryRowContext(r.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	if err := a.db.QueryRowContext(r.Context(), `SELECT COUNT(*),MIN(day),MAX(day) FROM daily_usage`).Scan(&count, &first, &last); err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	nodes := []map[string]any{}
	rows, err := a.db.QueryContext(r.Context(), `SELECT n.id,n.name,n.hidden,COUNT(d.day) FROM nodes n LEFT JOIN daily_usage d ON d.node_id=n.id GROUP BY n.id ORDER BY n.name`)
	if err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	for rows.Next() {
		var id, name string
		var hidden bool
		var days int
		if err = rows.Scan(&id, &name, &hidden, &days); err != nil {
			break
		}
		nodes = append(nodes, map[string]any{"id": id, "name": name, "hidden": hidden, "days": days})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	var size int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if st, err := os.Stat(filepath.Join(a.dir, "cfsm.db"+suffix)); err == nil {
			size += st.Size()
		}
	}
	respond(w, 200, map[string]any{"username": a.username, "configured": a.config.URL != "", "available": a.available, "collecting": a.collecting, "started_at": a.startedAt, "last_attempt_at": nullableTime(a.lastAttempt), "last_success_at": nullableTime(a.fetchedAt), "next_poll_at": nullableTime(a.nextPoll), "last_error": a.lastError, "failures": a.failures, "fetch_count": a.fetchCount, "poll_seconds": a.config.PollSeconds, "retention_days": a.config.RetentionDays, "time_zone": a.zone.String(), "database": map[string]any{"engine": "SQLite", "version": version, "journal_mode": journal, "file": "cfsm.db", "bytes": size, "records": count, "first_day": first.String, "last_day": last.String}, "nodes": nodes})
}

func (a *app) adminHistory(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if node == "" || len(node) > 256 {
		problem(w, 400, "请选择节点")
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().In(a.zone).AddDate(0, 0, -29).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().In(a.zone).Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", from); err != nil {
		problem(w, 400, "开始日期无效")
		return
	}
	if _, err := time.Parse("2006-01-02", to); err != nil || from > to {
		problem(w, 400, "日期范围无效")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1000000 {
			problem(w, 400, "分页参数无效")
			return
		}
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT day,in_bytes,out_bytes,used_bytes,samples,partial,estimated FROM daily_usage WHERE node_id=? AND day>=? AND day<=? ORDER BY day DESC LIMIT 101 OFFSET ?`, node, from, to, offset)
	if err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	days := []day{}
	for rows.Next() {
		var d day
		if err = rows.Scan(&d.Date, &d.In, &d.Out, &d.Used, &d.Samples, &d.Partial, &d.Estimated); err != nil {
			break
		}
		days = append(days, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		problem(w, 500, "SQLite 查询失败")
		return
	}
	more := len(days) > 100
	if more {
		days = days[:100]
	}
	respond(w, 200, map[string]any{"days": days, "has_more": more, "offset": offset, "time_zone": a.zone.String()})
}

func (a *app) collectNow(w http.ResponseWriter, r *http.Request) {
	if !a.fetchMu.TryLock() {
		problem(w, 409, "正在采集或保存数据源")
		return
	}
	defer a.fetchMu.Unlock()
	a.mu.RLock()
	configured := a.config.URL != ""
	recent := !a.lastAttempt.IsZero() && time.Since(a.lastAttempt) < 30*time.Second
	a.mu.RUnlock()
	if !configured {
		problem(w, 400, "请先配置数据源")
		return
	}
	if recent {
		w.Header().Set("Retry-After", "30")
		problem(w, 429, "两次采集至少间隔 30 秒")
		return
	}
	if err := a.collectLocked(r.Context()); err != nil {
		problem(w, 502, "采集失败，请查看状态")
		return
	}
	select {
	case a.changed <- struct{}{}:
	default:
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *app) saveRetention(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Days int `json:"retention_days"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if input.Days < 30 || input.Days > 3650 {
		problem(w, 400, "历史保留范围为 30–3650 天")
		return
	}
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.pruneHistory(input.Days); err != nil {
		problem(w, 500, "SQLite 清理失败")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *app) deleteHistory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm string `json:"confirm"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if input.Confirm != "CLEAR" {
		problem(w, 400, "请确认清空历史")
		return
	}
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.clearHistory(); err != nil {
		problem(w, 500, "SQLite 清理失败")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *app) changeAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Current  string `json:"current_password"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if message := accountValidation(input.Username, input.Password); message != "" {
		problem(w, 400, message)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, _ := pbkdf2.Key(sha256.New, input.Current, []byte(a.salt), 600000, 32)
	if subtle.ConstantTimeCompare(current, a.passwordHash) != 1 {
		problem(w, 400, "当前密码错误")
		return
	}
	salt := randomToken()
	hash, _ := pbkdf2.Key(sha256.New, input.Password, []byte(salt), 600000, 32)
	if _, err := a.db.ExecContext(r.Context(), `UPDATE administrator SET username=?,password_hash=?,salt=? WHERE id=1`, input.Username, hash, salt); err != nil {
		problem(w, 500, "账号保存失败")
		return
	}
	a.username = input.Username
	a.passwordHash = hash
	a.salt = salt
	a.sessions = map[string]session{}
	a.cookie(w, "", -1)
	respond(w, 200, map[string]bool{"ok": true})
}
