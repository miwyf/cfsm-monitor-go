package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"time"
)

type notificationBlock struct {
	NodeID string `json:"node_id,omitempty"`
	Text   string `json:"text"`
}
type notificationPayload struct {
	Source  string              `json:"source,omitempty"`
	Heading string              `json:"heading"`
	Blocks  []notificationBlock `json:"blocks"`
}
type notificationSnapshot struct {
	Source           string
	Ready, Available bool
	Nodes            []server
	Sampled          time.Time
}

func (a *app) notificationSnapshot(now time.Time) notificationSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s := notificationSnapshot{Ready: a.config.URL == "" || a.fetchCount > 0, Available: a.available, Sampled: a.fetchedAt}
	source := sha256.Sum256([]byte(a.config.URL + "\n" + a.config.Key))
	s.Source = hex.EncodeToString(source[:])
	if !s.Available {
		return s
	}
	if now.Sub(a.fetchedAt) > time.Duration(max(120, a.config.PollSeconds*2)+30)*time.Second {
		s.Available = false
		return s
	}
	for _, node := range a.servers {
		node.Days = a.history(node.ID, now)
		s.Nodes = append(s.Nodes, node)
	}
	slices.SortFunc(s.Nodes, func(x, y server) int { return strings.Compare(x.Name, y.Name) })
	return s
}

func channelContains(c notificationChannel, s server) bool {
	switch c.Scope {
	case "public":
		return !s.Hidden
	case "hidden":
		return s.Hidden
	case "all":
		return true
	case "selected":
		return slices.Contains(c.NodeIDs, s.ID)
	}
	return false
}

func safeNodeName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	runes := []rune(name)
	if len(runes) > 64 {
		name = string(runes[:64]) + "…"
	}
	return name
}
func observedGiB(d *day) string {
	if d == nil || d.Samples == 0 {
		return "—"
	}
	s := fmt.Sprintf("%.2f", d.Used/gib)
	if d.Partial {
		s += "*"
	}
	return s
}
func findDay(s server, date string) *day {
	for _, d := range s.Days {
		if d.Date == date {
			v := d
			return &v
		}
	}
	return nil
}
func nodeUsageText(s server, now time.Time, zone *time.Location) string {
	date := now.In(zone).Format("2006-01-02")
	name := safeNodeName(s.Name)
	if s.Hidden {
		name += " [隐藏]"
	}
	used, limit, remaining := "—", "—", "—"
	if s.Used != nil {
		used = fmt.Sprintf("%.2f", *s.Used)
	}
	if s.Limit != nil {
		limit = fmt.Sprintf("%.2f", *s.Limit)
	}
	if s.Remaining != nil {
		remaining = fmt.Sprintf("%.2f", *s.Remaining)
	}
	reset := nextReset(s.ResetDay, zone, now)
	if reset != "" {
		reset = reset[:10]
	} else {
		reset = "—"
	}
	return fmt.Sprintf("%s\n今日 %s GiB · 昨日 %s GiB\n周期 %s / %s GiB · 剩余 %s GiB\n重置 %s", name, observedGiB(findDay(s, date)), observedGiB(findDay(s, now.In(zone).AddDate(0, 0, -1).Format("2006-01-02"))), used, limit, remaining, reset)
}

func priorBaseline(s server, now time.Time, zone *time.Location) (float64, float64) {
	date := now.In(zone).Format("2006-01-02")
	first := now.In(zone).AddDate(0, 0, -7).Format("2006-01-02")
	values := []float64{}
	for _, d := range s.Days {
		if d.Date >= first && d.Date < date && d.Samples > 0 && !d.Partial {
			values = append(values, d.Used/gib)
		}
	}
	if len(values) < 3 {
		return -1, -1
	}
	slices.Sort(values)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	middle := len(values) / 2
	median := values[middle]
	if len(values)%2 == 0 {
		median = (values[middle-1] + median) / 2
	}
	return median, sum / float64(len(values))
}

func enqueueParts(tx *sql.Tx, c notificationChannel, kind, key, source, heading string, blocks []notificationBlock, now time.Time) error {
	parts := [][]notificationBlock{}
	part := []notificationBlock{}
	size := len(heading) + 40
	for _, block := range blocks {
		if size+len(block.Text)+2 > 1700 && len(part) > 0 {
			parts = append(parts, part)
			part = nil
			size = len(heading) + 40
		}
		part = append(part, block)
		size += len(block.Text) + 2
	}
	if len(part) > 0 {
		parts = append(parts, part)
	}
	for i, blocks := range parts {
		title := heading
		if len(parts) > 1 {
			title += fmt.Sprintf(" (%d/%d)", i+1, len(parts))
		}
		raw, err := json.Marshal(notificationPayload{Source: source, Heading: title, Blocks: blocks})
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT OR IGNORE INTO notify_jobs(channel_id,kind,event_key,payload,next_at,created_at) VALUES(?,?,?,?,?,?)`, c.ID, kind, fmt.Sprintf("%s:%d", key, i), string(raw), now.Unix(), now.Unix())
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *app) scheduleChannel(c notificationChannel, snap notificationSnapshot, now time.Time) error {
	if !c.Enabled || !snap.Ready {
		return nil
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	local := now.In(a.zone)
	date := local.Format("2006-01-02")
	blocks := []notificationBlock{}
	keys := []string{}
	for _, s := range snap.Nodes {
		if !channelContains(c, s) {
			continue
		}
		reasons := []string{}
		add := func(key, label string) error {
			fresh, e := markNotification(tx, c.ID, key, now)
			if e != nil {
				return e
			}
			if fresh {
				keys = append(keys, key)
				reasons = append(reasons, label)
			}
			return nil
		}
		cycle := nextReset(s.ResetDay, a.zone, now)
		if cycle == "" {
			cycle = local.Format("2006-01")
		}
		if c.Alerts {
			if s.Used != nil && s.Limit != nil && *s.Limit > 0 {
				percent := *s.Used / *s.Limit * 100
				highest := 0
				for _, threshold := range c.QuotaThresholds {
					key := fmt.Sprintf("quota:%s:%s:%d", s.ID, cycle, threshold)
					if percent >= float64(threshold) {
						fresh, e := markNotification(tx, c.ID, key, now)
						if e != nil {
							return e
						}
						if fresh {
							keys = append(keys, key)
							highest = threshold
						}
					} else {
						if _, e := tx.Exec(`DELETE FROM notify_marks WHERE channel_id=? AND event_key=?`, c.ID, key); e != nil {
							return e
						}
					}
				}
				if highest > 0 {
					reasons = append(reasons, fmt.Sprintf("配额已用 %.1f%%（阈值 %d%%）", percent, highest))
				}
			}
			today := findDay(s, date)
			median, average := priorBaseline(s, now, a.zone)
			if today != nil && today.Samples > 0 {
				if c.DailyGiB > 0 && today.Used/gib >= c.DailyGiB {
					if err := add(fmt.Sprintf("daily-limit:%s:%s:%g", s.ID, date, c.DailyGiB), fmt.Sprintf("今日用量达到 %.2f GiB", c.DailyGiB)); err != nil {
						return err
					}
				}
				if c.Spike && !today.Partial && median >= 0 && today.Used/gib >= 1 && today.Used/gib > median*3 {
					if err := add("spike:"+s.ID+":"+date, "今日用量突增（超过前 7 天完整记录中位数的 3 倍）"); err != nil {
						return err
					}
				}
			}
			if c.Forecast && average > 0 && s.Remaining != nil && *s.Remaining > 0 {
				reset := nextReset(s.ResetDay, a.zone, now)
				if reset != "" {
					t, _ := time.Parse(time.RFC3339, reset)
					days := math.Ceil(t.Sub(now).Hours() / 24)
					if days > 0 && *s.Remaining < average*days {
						if err := add("forecast:"+s.ID+":"+date, "按近 7 日均量可能提前用尽"); err != nil {
							return err
						}
					}
				}
			}
		}
		if len(reasons) > 0 {
			blocks = append(blocks, notificationBlock{s.ID, strings.Join(reasons, "；") + "\n" + nodeUsageText(s, now, a.zone)})
		}
	}
	if len(blocks) > 0 {
		slices.Sort(keys)
		sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
		if err = enqueueParts(tx, c, "alert", hex.EncodeToString(sum[:])+":"+randomToken(), snap.Source, "CFSM 流量预警\n"+local.Format("2006-01-02 15:04 MST"), blocks, now); err != nil {
			return err
		}
	}
	if c.Daily {
		for _, clock := range c.DailyTimes {
			t, _ := time.Parse("15:04", clock)
			due := time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, a.zone)
			if now.Before(due) || now.Sub(due) > 10*time.Minute {
				continue
			}
			key := "daily:" + date + ":" + clock
			fresh, e := markNotification(tx, c.ID, key, now)
			if e != nil {
				return e
			}
			if !fresh {
				continue
			}
			items := []notificationBlock{}
			if !snap.Available {
				items = append(items, notificationBlock{Text: "数据源不可用，未生成节点用量"})
			} else {
				for _, s := range snap.Nodes {
					if channelContains(c, s) {
						items = append(items, notificationBlock{s.ID, nodeUsageText(s, now, a.zone)})
					}
				}
				if len(items) == 0 {
					items = append(items, notificationBlock{Text: "所选范围暂无节点"})
				}
			}
			heading := "CFSM 每日播报\n" + date + " " + clock + " · " + a.zone.String() + "\n* 部分记录；— 无样本"
			if err = enqueueParts(tx, c, "daily", key+":"+randomToken(), snap.Source, heading, items, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (a *app) processNotificationJobs(ctx context.Context, channels []notificationChannel, now time.Time) error {
	rows, err := a.db.Query(`SELECT id,channel_id,kind,payload,attempts,created_at FROM notify_jobs WHERE status='pending' AND next_at<=? ORDER BY id LIMIT 4`, now.Unix())
	if err != nil {
		return err
	}
	type job struct {
		id                 int64
		channel, kind, raw string
		attempts           int
		created            int64
	}
	jobs := []job{}
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.id, &j.channel, &j.kind, &j.raw, &j.attempts, &j.created); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		index := slices.IndexFunc(channels, func(c notificationChannel) bool { return c.ID == j.channel })
		if index < 0 {
			continue
		}
		c := channels[index]
		var payload notificationPayload
		if json.Unmarshal([]byte(j.raw), &payload) != nil {
			return fmt.Errorf("invalid notification payload")
		}
		state := a.notificationSnapshot(now)
		if (j.kind != "test" && payload.Source != state.Source) || now.Unix()-j.created > 86400 {
			if _, err = a.db.Exec(`UPDATE notify_jobs SET status='cancelled',last_error='记录已过期或数据源已变更' WHERE id=?`, j.id); err != nil {
				return err
			}
			continue
		}
		blocks := []string{}
		if j.kind == "test" {
			for _, b := range payload.Blocks {
				blocks = append(blocks, b.Text)
			}
		} else if c.Enabled {
			if !state.Available {
				if j.kind == "alert" {
					if _, err = a.db.Exec(`UPDATE notify_jobs SET next_at=? WHERE id=?`, now.Add(time.Minute).Unix(), j.id); err != nil {
						return err
					}
					continue
				}
				blocks = append(blocks, "数据源不可用，未生成节点用量")
			} else {
				for _, b := range payload.Blocks {
					if b.NodeID == "" {
						blocks = append(blocks, b.Text)
						continue
					}
					if slices.ContainsFunc(state.Nodes, func(s server) bool { return s.ID == b.NodeID && channelContains(c, s) }) {
						blocks = append(blocks, b.Text)
					}
				}
			}
		}
		if len(blocks) == 0 {
			if _, err = a.db.Exec(`UPDATE notify_jobs SET status='cancelled' WHERE id=?`, j.id); err != nil {
				return err
			}
			continue
		}
		message := payload.Heading + "\n\n" + strings.Join(blocks, "\n\n")
		sendErr := a.deliverNotification(ctx, c, message, now)
		status, lastError, sentAt, next := "sent", "", now.Unix(), now.Unix()
		if sendErr != nil {
			status = "pending"
			lastError = sendErr.Error()
			sentAt = 0
			next = now.Add(time.Duration(1<<j.attempts) * time.Minute).Unix()
			if j.attempts+1 >= 3 {
				status = "failed"
			}
		}
		_, err = a.db.Exec(`UPDATE notify_jobs SET status=?,attempts=attempts+1,last_error=?,sent_at=?,next_at=? WHERE id=?`, status, lastError, sentAt, next, j.id)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *app) runNotifications(ctx context.Context, now time.Time) error {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	channels, err := a.channels()
	if err != nil {
		return err
	}
	snap := a.notificationSnapshot(now)
	for _, c := range channels {
		if err = a.scheduleChannel(c, snap, now); err != nil {
			return err
		}
	}
	if err = a.processNotificationJobs(ctx, channels, now); err != nil {
		return err
	}
	_, err = a.db.Exec(`DELETE FROM notify_marks WHERE created_at<?`, now.AddDate(0, 0, -90).Unix())
	if err == nil {
		_, err = a.db.Exec(`DELETE FROM notify_jobs WHERE status<>'pending' AND created_at<?`, now.AddDate(0, 0, -30).Unix())
	}
	return err
}

func (a *app) notificationLoop(ctx context.Context) {
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		if err := a.runNotifications(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Print("notification processing failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-a.notifyWake:
		}
	}
}
