package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDailySummaryTenSelectedNodesOneMessage(t *testing.T) {
	a := testApp(t)
	a.zone, _ = time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, a.zone)
	notificationFixture(a, now)
	template := a.servers[0]
	a.servers = nil
	c := exampleChannel()
	c.Scope, c.Alerts, c.Daily, c.DailyTimes = "selected", false, true, []string{"09:00"}
	for i := 0; i < 12; i++ {
		s := template
		s.ID, s.Name, s.Hidden = fmt.Sprint(i), fmt.Sprintf("节点%02d", i+1), i%2 == 1
		if i == 9 {
			s.Name = "US-EXAMPLE.LONG.NODE-NAME"
		}
		a.servers = append(a.servers, s)
		days := map[string]*day{}
		for date, d := range a.ledger["pub"].Days {
			copy := *d
			days[date] = &copy
		}
		days["2026-11-16"].Used = 0.25 * gib
		days["2026-11-15"].Used = 18.2 * gib
		if i == 0 {
			days["2026-11-15"].Used = 96.5 * gib
		}
		a.ledger[s.ID] = &record{Days: days}
		if i < 10 {
			c.NodeIDs = append(c.NodeIDs, s.ID)
		}
	}
	storeChannel(t, a, c)
	var messages []string
	a.notifyClient.Transport = notificationTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, body.Content.Text)
		return robotResponse(`{"code":0}`), nil
	})
	if err := a.runNotifications(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("10 nodes should share one message, got %d", len(messages))
	}
	text := messages[0]
	if len(text) > 1800 || strings.Contains(text, "/1000.00") || strings.Contains(text, "节点11") || strings.Contains(text, "节点12") {
		t.Fatal("report length, date or scope", text)
	}
	for _, want := range []string{
		"昨日 11/15",
		"节点01（11/22）\n今日 0.25｜昨日 96.50 ⚠突增｜剩余 50.00",
		"节点02（11/22）\n今日 0.25｜昨日 18.20｜剩余 50.00",
		"US-EXAMPLE.LONG.NODE-NAME（11/22）\n今日 0.25｜昨日 18.20｜剩余 50.00",
	} {
		if !strings.Contains(text, want) {
			t.Fatal("missing report field", want, text)
		}
	}
	blocks := strings.Split(text, "\n\n")
	if len(blocks) != 11 {
		t.Fatalf("heading and 10 separate node blocks expected, got %d", len(blocks))
	}
	for _, block := range blocks[1:] {
		lines := strings.Split(block, "\n")
		if len(lines) != 2 || !strings.HasSuffix(lines[0], "（11/22）") || !strings.HasPrefix(lines[1], "今日 ") || !strings.Contains(lines[1], "｜昨日 ") || !strings.HasSuffix(lines[1], "｜剩余 50.00") {
			t.Fatal("expected the node and reset date on the first line and usage on the second", block)
		}
	}
	if err := a.runNotifications(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatal("report repeated")
	}
	t.Logf("Ten selected nodes: one delivery, %d UTF-8 bytes", len(text))
}

func TestDailySummaryMissingPartialAndPastBaseline(t *testing.T) {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2027, 1, 1, 9, 0, 0, 0, zone)
	s := server{Name: "跨年\n节点", Days: []day{{Date: "2027-01-01", Samples: 1, Used: 999 * gib}}}
	text := dailyNodeText(s, now.UTC(), zone)
	if text != "跨年 节点\n今日 999.00｜昨日 —｜剩余 —" {
		t.Fatal("missing data invented", text)
	}
	s.Days = append(s.Days, day{Date: "2026-12-31", Samples: 2, Partial: true, Used: 80 * gib})
	for i := 2; i <= 5; i++ {
		s.Days = append(s.Days, day{Date: now.AddDate(0, 0, -i).Format("2006-01-02"), Samples: 4, Used: 10 * gib})
	}
	text = dailyNodeText(s, now, zone)
	if !strings.Contains(text, "昨日 80.00*") || strings.Contains(text, "突增") {
		t.Fatal("partial record treated as complete", text)
	}
	s.Days[1].Partial = false
	if !strings.Contains(dailyNodeText(s, now, zone), "⚠突增") {
		t.Fatal("yesterday spike missing")
	}
	s.Days = s.Days[:2]
	if strings.Contains(dailyNodeText(s, now, zone), "突增") {
		t.Fatal("missing baseline invented")
	}
}

func TestCompactAlertsOnlyIncludeTriggeredNodes(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	notificationFixture(a, now)
	c := exampleChannel()
	c.Scope = "all"
	storeChannel(t, a, c)
	low := 10.0
	a.servers[1].Used = &low
	var text string
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
	if !strings.Contains(text, "Public Node\n配额已用 95.0%") || !strings.Contains(text, "剩余 50.00/1000.00｜重置 11/22") || strings.Contains(text, "Hidden Node") || strings.Contains(text, "昨日") {
		t.Fatal("compact alert fields or filtering", text)
	}
}
