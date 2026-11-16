package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"
)

type notificationChannel struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	Webhook         string   `json:"webhook"`
	Secret          string   `json:"secret"`
	Enabled         bool     `json:"enabled"`
	Scope           string   `json:"scope"`
	NodeIDs         []string `json:"node_ids"`
	Alerts          bool     `json:"alerts"`
	QuotaThresholds []int    `json:"quota_thresholds"`
	DailyGiB        float64  `json:"daily_gib"`
	Spike           bool     `json:"spike"`
	Forecast        bool     `json:"forecast"`
	Daily           bool     `json:"daily"`
	DailyTimes      []string `json:"daily_times"`
}

func validateChannel(c *notificationChannel) error {
	if c.NodeIDs == nil {
		c.NodeIDs = []string{}
	}
	if c.QuotaThresholds == nil {
		c.QuotaThresholds = []int{}
	}
	if c.DailyTimes == nil {
		c.DailyTimes = []string{}
	}
	if c.Scope == "" {
		c.Scope = "public"
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len([]rune(c.Name)) > 64 {
		return errors.New("渠道名称为 1–64 字符")
	}
	u, err := url.Parse(strings.TrimSpace(c.Webhook))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Opaque != "" || len(c.Webhook) > 2048 {
		return errors.New("请填写对应平台的 HTTPS 机器人 Webhook")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("Webhook 参数无效")
	}
	valid := false
	switch c.Provider {
	case "feishu":
		token := strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")
		valid = u.Host == "open.feishu.cn" && strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") && token != "" && !strings.Contains(token, "/") && len(q) == 0
	case "wecom":
		valid = u.Host == "qyapi.weixin.qq.com" && u.Path == "/cgi-bin/webhook/send" && len(q) == 1 && len(q["key"]) == 1 && q.Get("key") != ""
		if c.Secret != "" {
			return errors.New("企业微信群机器人不使用签名密钥")
		}
	case "dingtalk":
		valid = u.Host == "oapi.dingtalk.com" && u.Path == "/robot/send" && len(q) == 1 && len(q["access_token"]) == 1 && q.Get("access_token") != ""
	}
	if !valid {
		return errors.New("Webhook 地址与所选平台不匹配；钉钉只需原始 access_token 地址")
	}
	c.Webhook = u.String()
	if len(c.Secret) > 512 || strings.ContainsAny(c.Secret, "\r\n") {
		return errors.New("签名密钥格式无效")
	}
	if !slices.Contains([]string{"public", "hidden", "all", "selected"}, c.Scope) || len(c.NodeIDs) > 200 {
		return errors.New("请选择节点范围")
	}
	c.NodeIDs = uniqueStrings(c.NodeIDs)
	if c.Scope == "selected" && len(c.NodeIDs) == 0 {
		return errors.New("请至少选择一个节点")
	}
	for _, id := range c.NodeIDs {
		if id == "" || len(id) > 256 {
			return errors.New("节点 ID 无效")
		}
	}
	slices.Sort(c.QuotaThresholds)
	c.QuotaThresholds = slices.Compact(c.QuotaThresholds)
	if len(c.QuotaThresholds) > 10 {
		return errors.New("配额阈值最多 10 个")
	}
	for _, v := range c.QuotaThresholds {
		if v < 1 || v > 100 {
			return errors.New("配额阈值范围为 1–100%")
		}
	}
	if math.IsNaN(c.DailyGiB) || math.IsInf(c.DailyGiB, 0) || c.DailyGiB < 0 || c.DailyGiB > 1e9 {
		return errors.New("每日用量阈值无效")
	}
	if c.Alerts && len(c.QuotaThresholds) == 0 && c.DailyGiB == 0 && !c.Spike && !c.Forecast {
		return errors.New("请至少选择一种流量预警规则")
	}
	c.DailyTimes = uniqueStrings(c.DailyTimes)
	if len(c.DailyTimes) > 4 || (c.Daily && len(c.DailyTimes) == 0) {
		return errors.New("每日播报需设置 1–4 个时间")
	}
	for _, v := range c.DailyTimes {
		t, e := time.Parse("15:04", v)
		if e != nil || t.Format("15:04") != v {
			return errors.New("播报时间使用 HH:MM，例如 09:00")
		}
	}
	return nil
}

func uniqueStrings(v []string) []string { slices.Sort(v); return slices.Compact(v) }

func (a *app) channels() ([]notificationChannel, error) {
	rows, err := a.db.Query(`SELECT config FROM notify_channels ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []notificationChannel{}
	for rows.Next() {
		var raw string
		var c notificationChannel
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

func (a *app) notificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/notifications", a.adminOnly(a.getNotifications))
	mux.HandleFunc("POST /api/admin/notifications/save", a.adminOnly(a.saveNotification))
	mux.HandleFunc("POST /api/admin/notifications/delete", a.adminOnly(a.deleteNotification))
	mux.HandleFunc("POST /api/admin/notifications/test", a.adminOnly(a.testNotification))
	mux.HandleFunc("POST /api/admin/notifications/retry", a.adminOnly(a.retryNotification))
}

func (a *app) getNotifications(w http.ResponseWriter, r *http.Request) {
	channels, err := a.channels()
	if err != nil {
		problem(w, 500, "通知配置读取失败")
		return
	}
	views := []map[string]any{}
	for _, c := range channels {
		webhook, secret := c.Webhook != "", c.Secret != ""
		c.Webhook, c.Secret = "", ""
		views = append(views, map[string]any{"config": c, "webhook_configured": webhook, "secret_configured": secret})
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT j.id,c.config,j.kind,j.status,j.attempts,j.created_at,j.sent_at,j.last_error FROM notify_jobs j JOIN notify_channels c ON c.id=j.channel_id ORDER BY j.id DESC LIMIT 100`)
	if err != nil {
		problem(w, 500, "通知记录读取失败")
		return
	}
	defer rows.Close()
	logs := []map[string]any{}
	for rows.Next() {
		var id, attempts, created, sent int64
		var raw, kind, status, lastError string
		if err = rows.Scan(&id, &raw, &kind, &status, &attempts, &created, &sent, &lastError); err != nil {
			break
		}
		var c notificationChannel
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			break
		}
		logs = append(logs, map[string]any{"id": id, "channel": c.Name, "kind": kind, "status": status, "attempts": attempts, "created_at": created, "sent_at": sent, "last_error": lastError})
	}
	if err != nil || rows.Err() != nil {
		problem(w, 500, "通知记录读取失败")
		return
	}
	respond(w, 200, map[string]any{"channels": views, "logs": logs, "time_zone": a.zone.String()})
}

func (a *app) saveNotification(w http.ResponseWriter, r *http.Request) {
	var input struct {
		notificationChannel
		ClearSecret bool `json:"clear_secret"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	channels, err := a.channels()
	if err != nil {
		problem(w, 500, "通知配置读取失败")
		return
	}
	c := input.notificationChannel
	var previous *notificationChannel
	if c.ID == "" {
		if len(channels) >= 20 {
			problem(w, 400, "最多添加 20 个通知渠道")
			return
		}
		c.ID = randomToken()
	} else {
		index := slices.IndexFunc(channels, func(old notificationChannel) bool { return old.ID == c.ID })
		if index < 0 {
			problem(w, 404, "渠道不存在")
			return
		}
		old := channels[index]
		previous = &old
		if c.Webhook == "" && c.Provider == old.Provider {
			c.Webhook = old.Webhook
		}
		if c.Secret == "" && !input.ClearSecret && c.Provider == old.Provider {
			c.Secret = old.Secret
		}
	}
	if input.ClearSecret {
		c.Secret = ""
	}
	if err = validateChannel(&c); err != nil {
		problem(w, 400, err.Error())
		return
	}
	recheck := previous == nil
	if previous != nil {
		next := c
		next.Name = previous.Name
		recheck = !reflect.DeepEqual(next, *previous)
	}
	raw, _ := json.Marshal(c)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 500, "通知配置保存失败")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO notify_channels(id,config) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, c.ID, string(raw))
	if err == nil && recheck {
		_, err = tx.Exec(`DELETE FROM notify_marks WHERE channel_id=? AND (event_key NOT LIKE 'daily:%' OR EXISTS (SELECT 1 FROM notify_jobs j WHERE j.channel_id=notify_marks.channel_id AND j.kind='daily' AND j.status IN ('pending','failed') AND j.event_key LIKE notify_marks.event_key || ':%'))`, c.ID)
	}
	if err == nil && recheck {
		_, err = tx.Exec(`UPDATE notify_jobs SET status='cancelled' WHERE channel_id=? AND status IN ('pending','failed')`, c.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 500, "通知配置保存失败")
		return
	}
	a.wakeNotifications()
	respond(w, 200, map[string]any{"ok": true, "id": c.ID})
}

func (a *app) deleteNotification(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	result, err := a.db.ExecContext(r.Context(), `DELETE FROM notify_channels WHERE id=?`, input.ID)
	if err != nil {
		problem(w, 500, "删除失败")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		problem(w, 404, "渠道不存在")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *app) testNotification(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM notify_channels WHERE id=?`, input.ID).Scan(&count); err != nil || count == 0 {
		problem(w, 404, "渠道不存在")
		return
	}
	var recent int64
	if err := a.db.QueryRow(`SELECT COALESCE(MAX(created_at),0) FROM notify_jobs WHERE channel_id=? AND kind='test'`, input.ID).Scan(&recent); err != nil {
		problem(w, 500, "通知记录读取失败")
		return
	}
	if time.Now().Unix()-recent < 30 {
		problem(w, 429, "测试发送至少间隔 30 秒")
		return
	}
	payload, _ := json.Marshal(notificationPayload{Heading: "CFSM 测试通知\n" + time.Now().In(a.zone).Format("2006-01-02 15:04:05 MST"), Blocks: []notificationBlock{{Text: "通知渠道测试"}}})
	now := time.Now().Unix()
	_, err := a.db.Exec(`INSERT INTO notify_jobs(channel_id,kind,event_key,payload,next_at,created_at) VALUES(?,'test',?,?,?,?)`, input.ID, randomToken(), string(payload), now, now)
	if err != nil {
		problem(w, 500, "测试通知排队失败")
		return
	}
	a.wakeNotifications()
	respond(w, 202, map[string]bool{"ok": true})
}

func (a *app) retryNotification(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID int64 `json:"id"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	result, err := a.db.Exec(`UPDATE notify_jobs SET status='pending',attempts=0,next_at=?,last_error='' WHERE id=? AND status='failed'`, time.Now().Unix(), input.ID)
	if err != nil {
		problem(w, 500, "重试排队失败")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		problem(w, 400, "仅失败记录可重试")
		return
	}
	a.wakeNotifications()
	respond(w, 202, map[string]bool{"ok": true})
}

func (a *app) wakeNotifications() {
	select {
	case a.notifyWake <- struct{}{}:
	default:
	}
}

func markNotification(tx *sql.Tx, channel, key string, now time.Time) (bool, error) {
	result, err := tx.Exec(`INSERT OR IGNORE INTO notify_marks(channel_id,event_key,created_at) VALUES(?,?,?)`, channel, key, now.Unix())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func notificationError(code int) error {
	return fmt.Errorf("平台返回错误码 %d，请检查机器人配置、签名和关键词", code)
}
