package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func robotResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func exampleChannel() notificationChannel {
	return notificationChannel{Name: "测试群", Provider: "feishu", Webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/synthetic-hook", Secret: "SEC-test-secret", Enabled: true, Scope: "public", Alerts: true, QuotaThresholds: []int{80, 90, 100}}
}
func storeChannel(t *testing.T, a *app, c notificationChannel) notificationChannel {
	t.Helper()
	if c.ID == "" {
		c.ID = randomToken()
	}
	if err := validateChannel(&c); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	if _, err := a.db.Exec(`INSERT INTO notify_channels(id,config) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, c.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	return c
}
func notificationFixture(a *app, now time.Time) {
	limit, used, remaining := 1000.0, 950.0, 50.0
	a.servers = []server{{ID: "pub", Name: "Public Node", Limit: &limit, Used: &used, Remaining: &remaining, ResetDay: 22}, {ID: "hidden", Name: "Hidden Node", Hidden: true, Limit: &limit, Used: &used, Remaining: &remaining, ResetDay: 22}}
	a.available = true
	a.fetchedAt = now
	for _, s := range a.servers {
		days := map[string]*day{}
		for i := 0; i < 8; i++ {
			date := now.In(a.zone).AddDate(0, 0, -i).Format("2006-01-02")
			days[date] = &day{Date: date, Samples: 4, Used: 10 * gib}
		}
		days[now.In(a.zone).Format("2006-01-02")].Used = 40 * gib
		a.ledger[s.ID] = &record{Days: days}
	}
}

func TestNotificationProtocolSigningAndResponse(t *testing.T) {
	a := testApp(t)
	now := time.Unix(1700000000, 0)
	for _, provider := range []string{"feishu", "wecom", "dingtalk"} {
		t.Run(provider, func(t *testing.T) {
			c := exampleChannel()
			c.Provider = provider
			if provider == "wecom" {
				c.Webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic-key"
				c.Secret = ""
			}
			if provider == "dingtalk" {
				c.Webhook = "https://oapi.dingtalk.com/robot/send?access_token=synthetic-token"
			}
			endpoint, body, err := robotRequest(c, "CFSM 测试", now)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err = json.Unmarshal(body, &raw); err != nil {
				t.Fatal(err)
			}
			if provider == "feishu" {
				if raw["timestamp"] != "1700000000" || raw["sign"] != "N/YfEei+5/EskDDCjSL3nvdh2x/Igb36CEA5Cc6MdHk=" || raw["msg_type"] != "text" {
					t.Fatal("feishu signing", raw)
				}
			}
			if provider == "dingtalk" {
				r, _ := http.NewRequest("POST", endpoint, nil)
				if r.URL.Query().Get("timestamp") != "1700000000000" || r.URL.Query().Get("sign") != "Mrhy389nC5p7fqYHgciQpNWFiBNjbTx4Rn5BqEH45jY=" {
					t.Fatal("dingtalk signing")
				}
			}
			for _, reply := range []string{`{}`, `{"code":19021,"errcode":310000}`, `invalid`} {
				a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) { return robotResponse(reply), nil })
				if a.deliverNotification(context.Background(), c, "CFSM 测试", now) == nil {
					t.Fatal("bad robot response accepted", reply)
				}
			}
			good := `{"errcode":0}`
			if provider == "feishu" {
				good = `{"code":0}`
			}
			a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) { return robotResponse(good), nil })
			if err = a.deliverNotification(context.Background(), c, "CFSM 测试", now); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNotificationValidationAndNoRedirect(t *testing.T) {
	a := testApp(t)
	c := exampleChannel()
	for _, endpoint := range []string{"http://open.feishu.cn/open-apis/bot/v2/hook/token", "https://open.feishu.cn.evil.example/open-apis/bot/v2/hook/token", "https://127.0.0.1/private", "https://user:pass@open.feishu.cn/open-apis/bot/v2/hook/token", "https://open.feishu.cn:444/open-apis/bot/v2/hook/token", "https://open.feishu.cn/open-apis/bot/v2/hook/token?x=y"} {
		bad := c
		bad.Webhook = endpoint
		if validateChannel(&bad) == nil {
			t.Fatal("invalid webhook accepted", endpoint)
		}
	}
	for _, scope := range []string{"all", "public", "hidden", "selected"} {
		cfg := c
		cfg.Scope = scope
		cfg.NodeIDs = []string{"hidden"}
		if validateChannel(&cfg) != nil {
			t.Fatal("valid scope rejected")
		}
		for _, hidden := range []bool{false, true} {
			s := server{ID: "pub", Hidden: hidden}
			want := scope == "all" || (scope == "public" && !hidden) || (scope == "hidden" && hidden)
			if channelContains(cfg, s) != want {
				t.Fatal("scope filter", scope, hidden)
			}
		}
	}
	requests := 0
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/collect"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if a.deliverNotification(context.Background(), c, "CFSM 测试", time.Now()) == nil || requests != 1 {
		t.Fatal("robot redirect followed")
	}
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("sensitive %s", c.Webhook) })
	if err := a.deliverNotification(context.Background(), c, "CFSM 测试", time.Now()); err == nil || strings.Contains(err.Error(), "synthetic-hook") {
		t.Fatal("request error leaked webhook")
	}
}

func TestNotificationAdminAPIAndPersistence(t *testing.T) {
	a := testApp(t)
	for _, path := range []string{"save", "delete", "test", "retry"} {
		if call(a, "POST", "/api/admin/notifications/"+path, `{}`, nil).Code != 401 {
			t.Fatal("anonymous notification mutation")
		}
	}
	if call(a, "GET", "/api/admin/notifications", "", nil).Code != 401 {
		t.Fatal("anonymous channels exposed")
	}
	cookie := loginTest(t, a)
	c := exampleChannel()
	c.Scope, c.NodeIDs = "selected", []string{"pub", "hidden"}
	raw, _ := json.Marshal(c)
	w := call(a, "POST", "/api/admin/notifications/save", string(raw), cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	channels, err := a.channels()
	if err != nil || len(channels) != 1 {
		t.Fatal("channel not saved", err)
	}
	c = channels[0]
	if c.Scope != "selected" || len(c.NodeIDs) != 2 {
		t.Fatal("multiple node selection not saved")
	}
	w = call(a, "GET", "/api/admin/notifications", "", cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), c.Webhook) || strings.Contains(w.Body.String(), c.Secret) {
		t.Fatal("notification credentials exposed", w.Code)
	}
	c.Webhook = ""
	c.Secret = ""
	c.Name = "已修改"
	raw, _ = json.Marshal(c)
	if w = call(a, "POST", "/api/admin/notifications/save", string(raw), cookie); w.Code != 200 {
		t.Fatal("blank credentials update failed", w.Body.String())
	}
	channels, _ = a.channels()
	if channels[0].Secret != "SEC-test-secret" || !strings.Contains(channels[0].Webhook, "synthetic-hook") {
		t.Fatal("credentials not preserved")
	}
	if w = call(a, "POST", "/api/admin/notifications/test", `{"id":"`+c.ID+`"}`, cookie); w.Code != 202 {
		t.Fatal("test not queued", w.Body.String())
	}
	if call(a, "POST", "/api/admin/notifications/test", `{"id":"`+c.ID+`"}`, cookie).Code != 429 {
		t.Fatal("test send not limited")
	}
	if err = a.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newApp(a.dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.db.Close()
	channels, _ = restarted.channels()
	if len(channels) != 1 || channels[0].Name != "已修改" || len(channels[0].NodeIDs) != 2 {
		t.Fatal("channel lost at restart")
	}
	cookie = loginTest(t, restarted)
	if w = call(restarted, "POST", "/api/admin/notifications/delete", `{"id":"`+c.ID+`"}`, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var jobs int
	if err = restarted.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("delete did not cascade", err)
	}
}

func TestNotificationQuotaDedupeRestartAndReset(t *testing.T) {
	a := testApp(t)
	now := time.Now().Truncate(time.Second)
	notificationFixture(a, now)
	storeChannel(t, a, exampleChannel())
	sent := []string{}
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		sent = append(sent, string(body))
		return robotResponse(`{"code":0}`), nil
	})
	for i := 0; i < 2; i++ {
		if err := a.runNotifications(context.Background(), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != 1 || strings.Contains(sent[0], "Hidden Node") || !strings.Contains(sent[0], "90%") {
		t.Fatal("quota dedupe/scope failed", len(sent))
	}
	dir := a.dir
	a.db.Close()
	b, err := newApp(dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer b.db.Close()
	notificationFixture(b, now)
	b.notifyClient.Transport = a.notifyClient.Transport
	if err = b.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatal("restart repeated alert")
	}
	used, remaining := 1000.0, 0.0
	b.servers[0].Used = &used
	b.servers[0].Remaining = &remaining
	if err = b.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatal("100 percent alert missing")
	}
	used = 100
	if err = b.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	used = 850
	if err = b.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 3 {
		t.Fatal("reset crossing did not alert")
	}
}

func TestNotificationDailyScopesScheduleAndUTF8(t *testing.T) {
	a := testApp(t)
	zone, _ := time.LoadLocation("Asia/Shanghai")
	a.zone = zone
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, zone)
	notificationFixture(a, now)
	for _, scope := range []string{"public", "hidden", "all", "selected"} {
		c := exampleChannel()
		c.Scope = scope
		c.NodeIDs = []string{"hidden"}
		c.Alerts = false
		c.Daily = true
		c.DailyTimes = []string{"09:00", "18:00"}
		c.Webhook = "https://open.feishu.cn/open-apis/bot/v2/hook/" + scope
		storeChannel(t, a, c)
	}
	messages := map[string]string{}
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Content.Text) > 1800 {
			t.Fatal("message limit")
		}
		messages[r.URL.Path] = body.Content.Text
		return robotResponse(`{"code":0}`), nil
	})
	if err := a.runNotifications(context.Background(), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatal("report before due time")
	}
	if err := a.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatal("scheduled report count", len(messages))
	}
	for path, message := range messages {
		scope := path[strings.LastIndex(path, "/")+1:]
		pub, hidden := strings.Contains(message, "Public Node"), strings.Contains(message, "Hidden Node")
		if pub != (scope == "public" || scope == "all") || hidden != (scope != "public") {
			t.Fatal("daily scope", scope)
		}
		if !strings.Contains(message, "昨日") || !strings.Contains(message, "重置") {
			t.Fatal("report fields missing")
		}
	}
	var before, after int
	a.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs`).Scan(&before)
	if err := a.runNotifications(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs`).Scan(&after)
	if before != after {
		t.Fatal("daily report duplicated")
	}
	c := exampleChannel()
	c.ID = randomToken()
	storeChannel(t, a, c)
	tx, _ := a.db.Begin()
	blocks := []notificationBlock{}
	for i := 0; i < 30; i++ {
		blocks = append(blocks, notificationBlock{Text: strings.Repeat("流量节点", 30)})
	}
	if err := enqueueParts(tx, c, "daily", "chunks", "", "CFSM 每日播报", blocks, now); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	rows, _ := a.db.Query(`SELECT payload FROM notify_jobs WHERE channel_id=?`, c.ID)
	defer rows.Close()
	parts := 0
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var p notificationPayload
		json.Unmarshal([]byte(raw), &p)
		text := p.Heading
		for _, b := range p.Blocks {
			text += "\n\n" + b.Text
		}
		if len(text) > 1800 {
			t.Fatal("UTF8 chunk too big")
		}
		parts++
	}
	if parts < 2 {
		t.Fatal("long report not split")
	}
}

func TestNotificationVisibilitySourceAndRetry(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	notificationFixture(a, now)
	c := storeChannel(t, a, exampleChannel())
	sent := 0
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) { sent++; return robotResponse(`{"code":0}`), nil })
	if err := a.scheduleChannel(c, a.notificationSnapshot(now), now); err != nil {
		t.Fatal(err)
	}
	a.servers[0].Hidden = true
	if err := a.processNotificationJobs(context.Background(), []notificationChannel{c}, now); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("newly hidden node leaked from queued message")
	}
	a.servers[0].Hidden = false
	a.db.Exec(`DELETE FROM notify_marks; DELETE FROM notify_jobs`)
	if err := a.scheduleChannel(c, a.notificationSnapshot(now), now); err != nil {
		t.Fatal(err)
	}
	a.config.Key = "changed-source"
	if err := a.processNotificationJobs(context.Background(), []notificationChannel{c}, now); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("old source notification delivered")
	}
	cookie := loginTest(t, a)
	if w := call(a, "POST", "/api/admin/notifications/test", `{"id":"`+c.ID+`"}`, cookie); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		sent++
		return robotResponse(`{"code":19021,"msg":"synthetic-hook SEC-test-secret"}`), nil
	})
	base := time.Now().Add(time.Second)
	for _, delta := range []time.Duration{0, 30 * time.Second, time.Minute, 3 * time.Minute} {
		if err := a.runNotifications(context.Background(), base.Add(delta)); err != nil {
			t.Fatal(err)
		}
	}
	var status, lastError string
	var attempts int
	var id int64
	if err := a.db.QueryRow(`SELECT id,status,attempts,last_error FROM notify_jobs WHERE kind='test'`).Scan(&id, &status, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 3 || strings.Contains(lastError, "synthetic-hook") || strings.Contains(lastError, "SEC-test-secret") {
		t.Fatal("retry or error redaction", status, attempts)
	}
	if w := call(a, "POST", "/api/admin/notifications/retry", fmt.Sprintf(`{"id":%d}`, id), cookie); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
}

func TestNotificationSchemaUpgradePreservesAccount(t *testing.T) {
	a := testApp(t)
	if _, err := a.db.Exec(`DROP TABLE notify_jobs; DROP TABLE notify_marks; DROP TABLE notify_channels; PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	a.db.Close()
	b, err := newApp(a.dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer b.db.Close()
	if b.setupRequired() {
		t.Fatal("schema upgrade removed account")
	}
	loginTest(t, b)
	var version int
	if err = b.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatal("schema not upgraded", err, version)
	}
}

func TestNotificationUsageRulesAndPartialData(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	notificationFixture(a, now)
	c := exampleChannel()
	c.QuotaThresholds = nil
	c.DailyGiB = 30
	c.Spike = true
	c.Forecast = true
	c = storeChannel(t, a, c)
	text := ""
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		text = body.Content.Text
		return robotResponse(`{"code":0}`), nil
	})
	if err := a.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"今日用量 40.00 GiB（阈值 30.00 GiB）", "今日用量突增", "近7日中位数 10.00 GiB（约 4.0 倍）", "可能提前用尽"} {
		if !strings.Contains(text, want) {
			t.Fatal("usage rule missing", want)
		}
	}
	a.db.Exec(`DELETE FROM notify_jobs; DELETE FROM notify_marks`)
	a.ledger["pub"].Days[now.Format("2006-01-02")].Partial = true
	text = ""
	if err := a.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "今日用量突增") || !strings.Contains(text, "40.00*") {
		t.Fatal("partial day anomaly/marker", text)
	}
	a.db.Exec(`DELETE FROM notify_jobs; DELETE FROM notify_marks`)
	c.Alerts = false
	c.Daily = true
	c.DailyTimes = []string{"09:00"}
	storeChannel(t, a, c)
	if err := a.runNotifications(context.Background(), now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	a.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs`).Scan(&jobs)
	if jobs != 0 {
		t.Fatal("late daily catchup exceeded window")
	}
}

func TestNotificationChannelChangesReevaluatePendingWork(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	notificationFixture(a, now)
	c := exampleChannel()
	c.Daily = true
	c.DailyTimes = []string{now.In(a.zone).Format("15:04")}
	c = storeChannel(t, a, c)
	if err := a.scheduleChannel(c, a.notificationSnapshot(now), now); err != nil {
		t.Fatal(err)
	}
	cookie := loginTest(t, a)
	c.Name = "只改名称"
	raw, _ := json.Marshal(c)
	if w := call(a, "POST", "/api/admin/notifications/save", string(raw), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var pending int
	a.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs WHERE status='pending'`).Scan(&pending)
	if pending != 2 {
		t.Fatal("renaming cancelled pending messages", pending)
	}
	c.QuotaThresholds = []int{50, 80, 90, 100}
	raw, _ = json.Marshal(c)
	if w := call(a, "POST", "/api/admin/notifications/save", string(raw), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	a.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs WHERE status='pending'`).Scan(&pending)
	if pending != 0 {
		t.Fatal("old pending work not cancelled")
	}
	if err := a.scheduleChannel(c, a.notificationSnapshot(now), now); err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow(`SELECT COUNT(*) FROM notify_jobs WHERE status='pending'`).Scan(&pending)
	if pending != 2 {
		t.Fatal("changed rules did not regenerate alert and daily", pending)
	}
}
