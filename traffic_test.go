package main

import (
	"testing"
	"time"
)

func sampleServer(t time.Time, rx, tx float64, mode string) server {
	rx /= gib
	tx /= gib
	return server{ID: "node", RX: &rx, TX: &tx, Mode: mode, Stamp: float64(t.UnixMilli()) / 1000}
}

func TestDailyCounterAccounting(t *testing.T) {
	a := testApp(t)
	start := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	a.ingest(sampleServer(start, 100, 200, "total"), start)
	if a.ledger["node"].Days["2026-11-16"].Samples != 0 {
		t.Fatal("first sample invents usage")
	}
	a.ingest(sampleServer(start.Add(time.Minute), 160, 240, "total"), start.Add(time.Minute))
	d := a.ledger["node"].Days["2026-11-16"]
	if d.Used != 100 || d.In != 60 || d.Out != 40 || d.Samples != 1 || !d.Partial {
		t.Fatalf("bad delta: %+v", d)
	}
	a.ingest(sampleServer(start.Add(time.Minute), 999, 999, "total"), start.Add(time.Minute))
	if d.Used != 100 {
		t.Fatal("duplicate report counted")
	}
	a.ingest(sampleServer(start.Add(2*time.Minute), 0, 0, "total"), start.Add(2*time.Minute))
	if d.Used != 100 {
		t.Fatal("counter reset created usage")
	}
	a.ingest(sampleServer(start.Add(3*time.Minute), 50, 40, "max"), start.Add(3*time.Minute))
	if d.Used != 100 {
		t.Fatal("mode change created usage")
	}
	a.ingest(sampleServer(start.Add(4*time.Minute), 60, 80, "max"), start.Add(4*time.Minute))
	if d.Used != 130 {
		t.Fatal("max must use difference of cumulative maximums")
	}
}

func TestMidnightAndMissingDays(t *testing.T) {
	a := testApp(t)
	start := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)
	a.ingest(sampleServer(start, 100, 100, "total"), start)
	end := start.Add(2 * time.Minute)
	a.ingest(sampleServer(end, 200, 200, "total"), end)
	for _, date := range []string{"2026-11-15", "2026-11-16"} {
		d := a.ledger["node"].Days[date]
		if d.Used != 100 || !d.Estimated {
			t.Fatal("midnight split", d)
		}
	}
	rows := a.history("node", end)
	if len(rows) != 30 || rows[0].Samples != 0 {
		t.Fatal("missing days must have zero samples")
	}
	// A long outage crossing midnight has no evidence for assigning daily usage.
	a = testApp(t)
	a.ingest(sampleServer(start, 100, 100, "total"), start)
	end = start.Add(2 * time.Hour)
	a.ingest(sampleServer(end, 9000, 9000, "total"), end)
	if d := a.ledger["node"].Days["2026-11-16"]; d.Samples != 0 || !d.Partial {
		t.Fatal("outage interpolated", d)
	}
}

func TestStaleReportsAndMonthEnd(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	a.ingest(sampleServer(now.Add(-6*time.Minute), 100, 100, "total"), now)
	a.ingest(sampleServer(now.Add(2*time.Minute), 100, 100, "total"), now)
	if len(a.ledger) != 0 {
		t.Fatal("stale/future report accepted")
	}
	date := nextReset(31, time.UTC, time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC))
	if date != "2026-02-28T00:00:00Z" {
		t.Fatal("bad month end", date)
	}
	date = nextReset(31, time.UTC, time.Date(2026, 12, 31, 1, 0, 0, 0, time.UTC))
	if date != "2027-01-31T00:00:00Z" {
		t.Fatal("bad year rollover", date)
	}
}
