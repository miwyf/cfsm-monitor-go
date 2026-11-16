package main

import (
	"math"
	"strings"
	"time"
)

const gib = 1024 * 1024 * 1024

// Explicit DTO: never forward raw upstream fields (secrets, notes, addresses,
// global statistics, and realtime payloads must not enter the public API).
type server struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Region    string   `json:"region"`
	Group     string   `json:"group"`
	Hidden    bool     `json:"hidden"`
	Online    bool     `json:"online"`
	CPU       *float64 `json:"cpu"`
	RAM       *float64 `json:"ram"`
	Disk      *float64 `json:"disk"`
	SpeedIn   *float64 `json:"speed_in"`
	SpeedOut  *float64 `json:"speed_out"`
	Used      *float64 `json:"used_gib"`
	Limit     *float64 `json:"limit_gib"`
	Remaining *float64 `json:"remaining_gib"`
	RX        *float64 `json:"in_gib"`
	TX        *float64 `json:"out_gib"`
	Mode      string   `json:"mode"`
	Reset     string   `json:"reset"`
	ResetDay  int      `json:"-"`
	Stamp     float64  `json:"updated"`
	Days      []day    `json:"days"`
}

type day struct {
	Date      string  `json:"date"`
	In        float64 `json:"in_bytes"`
	Out       float64 `json:"out_bytes"`
	Used      float64 `json:"used_bytes"`
	Samples   int     `json:"samples"`
	Partial   bool    `json:"partial"`
	Estimated bool    `json:"estimated"`
}
type sample struct {
	Stamp float64 `json:"stamp"`
	RX    float64 `json:"rx"`
	TX    float64 `json:"tx"`
	Mode  string  `json:"mode"`
}
type record struct {
	Last *sample         `json:"last"`
	Days map[string]*day `json:"days"`
}

func normalizeServer(raw map[string]any) (server, bool) {
	s := server{ID: textValue(raw["id"]), Name: textValue(raw["name"]), Region: textValue(raw["region"]), Group: textValue(raw["server_group"]), Mode: textValue(raw["traffic_calc_type"])}
	if s.ID == "" {
		return s, false
	}
	if s.Name == "" {
		s.Name = s.ID
	}
	// Fail closed for missing/null/unrecognized visibility fields.
	s.Hidden = true
	if b, ok := raw["is_hidden"].(bool); ok {
		s.Hidden = b
	} else if n := number(raw["is_hidden"]); n != nil {
		s.Hidden = *n != 0
	} else if v, ok := raw["is_hidden"].(string); ok && strings.EqualFold(v, "false") {
		s.Hidden = false
	}
	s.CPU = number(raw["cpu"])
	s.RAM = ratio(raw["ram_used"], raw["ram_total"])
	s.Disk = ratio(raw["disk_used"], raw["disk_total"])
	s.SpeedIn = number(raw["net_in_speed"])
	s.SpeedOut = number(raw["net_out_speed"])
	if s.Mode != "ul" && s.Mode != "dl" && s.Mode != "max" {
		s.Mode = "total"
	}
	rx, tx := number(raw["net_rx_monthly"]), number(raw["net_tx_monthly"])
	if rx != nil {
		n := *rx / gib
		s.RX = &n
	}
	if tx != nil {
		n := *tx / gib
		s.TX = &n
	}
	if rx != nil && tx != nil {
		n := usage(*rx, *tx, s.Mode) / gib
		s.Used = &n
	}
	s.Limit = number(raw["traffic_limit"])
	if s.Limit != nil && *s.Limit == 0 {
		s.Limit = nil
	}
	if s.Limit != nil && s.Used != nil {
		n := math.Max(0, *s.Limit-*s.Used)
		s.Remaining = &n
	}
	stamp := number(raw["last_updated"])
	if stamp == nil || *stamp == 0 {
		stamp = number(raw["timestamp"])
	}
	if stamp != nil {
		s.Stamp = *stamp
		if s.Stamp > 1e10 {
			s.Stamp /= 1000
		}
	}
	age := float64(time.Now().UnixMilli())/1000 - s.Stamp
	s.Online = s.Stamp > 0 && age >= -60 && age <= 300
	if d := number(raw["reset_day"]); d != nil && *d >= 1 && *d <= 31 {
		s.ResetDay = int(*d)
	}
	return s, true
}

func ratio(a, b any) *float64 {
	x, y := number(a), number(b)
	if x == nil || y == nil || *y == 0 {
		return nil
	}
	n := math.Min(100, *x / *y * 100)
	return &n
}
func usage(rx, tx float64, mode string) float64 {
	switch mode {
	case "dl":
		return rx
	case "ul":
		return tx
	case "max":
		return math.Max(rx, tx)
	default:
		return rx + tx
	}
}
func instant(stamp float64) time.Time { return time.UnixMilli(int64(stamp * 1000)) }

func nextReset(day int, zone *time.Location, now time.Time) string {
	if day < 1 || day > 31 {
		return ""
	}
	now = now.In(zone)
	y, m, _ := now.Date()
	date := func() time.Time {
		last := time.Date(y, m+1, 0, 0, 0, 0, 0, zone).Day()
		d := min(day, last)
		return time.Date(y, m, d, 0, 0, 0, 0, zone)
	}
	next := date()
	if !next.After(now) {
		m++
		next = date()
	}
	return next.Format(time.RFC3339)
}

func (a *app) ingest(s server, now time.Time) {
	if s.RX == nil || s.TX == nil || s.Stamp <= 0 {
		return
	}
	age := float64(now.UnixMilli())/1000 - s.Stamp
	if age < -60 || age > 300 {
		return
	}
	r := a.ledger[s.ID]
	if r == nil {
		r = &record{Days: map[string]*day{}}
		a.ledger[s.ID] = r
	}
	if r.Days == nil {
		r.Days = map[string]*day{}
	}
	last := r.Last
	if last != nil && s.Stamp <= last.Stamp {
		return
	}
	cur := instant(s.Stamp).In(a.zone)
	rx, tx := *s.RX*gib, *s.TX*gib
	bucket := func(date string, partial bool) *day {
		if a.dirty == nil {
			a.dirty = map[dayKey]bool{}
		}
		a.dirty[dayKey{s.ID, date}] = true
		d := r.Days[date]
		if d == nil {
			d = &day{Date: date}
			r.Days[date] = d
		}
		d.Partial = d.Partial || partial
		return d
	}
	date := cur.Format("2006-01-02")
	if last == nil || rx < last.RX || tx < last.TX || s.Mode != last.Mode {
		bucket(date, true)
	} else {
		before := instant(last.Stamp).In(a.zone)
		elapsed := s.Stamp - last.Stamp
		cross := before.Format("2006-01-02") != date
		if cross && elapsed > float64(max(900, a.config.PollSeconds*3)) {
			bucket(before.Format("2006-01-02"), true)
			bucket(date, true)
		} else {
			for cursor := before; cursor.Before(cur); {
				y, m, d := cursor.Date()
				end := time.Date(y, m, d+1, 0, 0, 0, 0, a.zone)
				if end.After(cur) {
					end = cur
				}
				weight := end.Sub(cursor).Seconds() / elapsed
				item := bucket(cursor.Format("2006-01-02"), false)
				item.In += (rx - last.RX) * weight
				item.Out += (tx - last.TX) * weight
				item.Used += (usage(rx, tx, s.Mode) - usage(last.RX, last.TX, last.Mode)) * weight
				item.Samples++
				item.Estimated = item.Estimated || cross
				cursor = end
			}
		}
	}
	r.Last = &sample{s.Stamp, rx, tx, s.Mode}
	first := a.retentionCutoff(now)
	for date := range r.Days {
		if date < first {
			delete(r.Days, date)
		}
	}
}

func (a *app) history(id string, now time.Time) []day {
	days := make([]day, 0, 30)
	r := a.ledger[id]
	for offset := 29; offset >= 0; offset-- {
		date := now.In(a.zone).AddDate(0, 0, -offset).Format("2006-01-02")
		d := day{Date: date}
		if r != nil && r.Days[date] != nil {
			d = *r.Days[date]
		}
		days = append(days, d)
	}
	return days
}
