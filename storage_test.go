package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveSample(t *testing.T, a *app, when time.Time, rx, tx float64) {
	t.Helper()
	s := sampleServer(when, rx, tx, "total")
	s.Name = "Example"
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.accept([]server{s}, when); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteStartsFreshAndIgnoresJSON(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"settings.json", "traffic.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`invalid old JSON must not be read`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := newApp(dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	if a.config.URL != "" || len(a.ledger) != 0 || a.config.RetentionDays != 90 {
		t.Fatal("legacy JSON was imported")
	}
	var journal string
	if err := a.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatal("WAL not enabled", err, journal)
	}
	b, err := os.ReadFile(filepath.Join(dir, "cfsm.db"))
	if err != nil || !strings.HasPrefix(string(b), "SQLite format 3") {
		t.Fatal("not SQLite", err)
	}
	for _, path := range []string{"/cfsm.db", "/data/cfsm.db", "/admin.html", "/api/admin/status"} {
		if w := call(a, "GET", path, "", nil); w.Code == 200 {
			t.Fatalf("exposed %s", path)
		}
	}
	if w := call(a, "GET", "/admin", "", nil); w.Code != 200 {
		t.Fatal("admin shell unavailable", w.Code)
	}
}

func TestSQLiteLedgerRoundTrip(t *testing.T) {
	a := testApp(t)
	start := time.Now().UTC().Truncate(time.Second)
	saveSample(t, a, start, 100, 200)
	saveSample(t, a, start.Add(time.Minute), 160, 240)
	var incoming, outgoing, used float64
	var samples int
	var partial bool
	date := start.Format("2006-01-02")
	if err := a.db.QueryRow(`SELECT in_bytes,out_bytes,used_bytes,samples,partial FROM daily_usage WHERE node_id='node' AND day=?`, date).Scan(&incoming, &outgoing, &used, &samples, &partial); err != nil {
		t.Fatal(err)
	}
	if incoming != 60 || outgoing != 40 || used != 100 || samples != 1 || !partial {
		t.Fatal("incorrect persisted delta", incoming, outgoing, used, samples, partial)
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newApp(a.dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.db.Close()
	if restarted.available {
		t.Fatal("cached visibility exposed before fresh collection")
	}
	r := restarted.ledger["node"]
	if r == nil || r.Last == nil || r.Days[date].Used != 100 {
		t.Fatal("ledger not restored")
	}
	saveSample(t, restarted, start.Add(2*time.Minute), 180, 250)
	if restarted.ledger["node"].Days[date].Used != 130 {
		t.Fatal("restart double-counted or lost baseline")
	}
}

func TestSQLiteTransactionFailureRestoresBaseline(t *testing.T) {
	a := testApp(t)
	start := time.Now().UTC().Truncate(time.Second)
	saveSample(t, a, start, 100, 200)
	if _, err := a.db.Exec(`CREATE TRIGGER reject_daily BEFORE INSERT ON daily_usage BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END;`); err != nil {
		t.Fatal(err)
	}
	s := sampleServer(start.Add(time.Minute), 160, 240, "total")
	s.Name = "Changed name"
	a.mu.Lock()
	err := a.accept([]server{s}, start.Add(time.Minute))
	a.mu.Unlock()
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	if a.ledger["node"].Last.Stamp != float64(start.UnixMilli())/1000 || a.ledger["node"].Days[start.Format("2006-01-02")].Used != 0 {
		t.Fatal("in-memory baseline advanced after failed transaction")
	}
	var name string
	var rx float64
	if err := a.db.QueryRow(`SELECT name FROM nodes WHERE id='node'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT rx FROM counters WHERE node_id='node'`).Scan(&rx); err != nil {
		t.Fatal(err)
	}
	if name != "Example" || rx != 100 {
		t.Fatal("partial SQL write was committed", name, rx)
	}
	if _, err := a.db.Exec(`DROP TRIGGER reject_daily`); err != nil {
		t.Fatal(err)
	}
	saveSample(t, a, start.Add(time.Minute), 160, 240)
	if a.ledger["node"].Days[start.Format("2006-01-02")].Used != 100 {
		t.Fatal("retry lost traffic")
	}
}

func TestSQLiteRetentionAndClear(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC().Truncate(time.Second)
	saveSample(t, a, now, 100, 200)
	old := now.AddDate(0, 0, -45).Format("2006-01-02")
	recent := now.AddDate(0, 0, -10).Format("2006-01-02")
	for _, date := range []string{old, recent} {
		if _, err := a.db.Exec(`INSERT INTO daily_usage VALUES('node',?,10,20,30,1,0,0)`, date); err != nil {
			t.Fatal(err)
		}
	}
	c := loginTest(t, a)
	if w := call(a, "POST", "/api/admin/retention", `{"retention_days":29}`, c); w.Code != 400 {
		t.Fatal("invalid retention accepted")
	}
	if w := call(a, "POST", "/api/admin/retention", `{"retention_days":30}`, c); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM daily_usage WHERE day=?`, old).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired records retained", err, count)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM daily_usage WHERE day=?`, recent).Scan(&count); err != nil || count != 1 {
		t.Fatal("recent records removed", err, count)
	}
	if w := call(a, "POST", "/api/admin/clear-history", `{"confirm":"NO"}`, c); w.Code != 400 {
		t.Fatal("unconfirmed clear accepted")
	}
	if w := call(a, "POST", "/api/admin/clear-history", `{"confirm":"CLEAR"}`, c); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM daily_usage)+(SELECT COUNT(*) FROM counters)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("history not cleared", err, count)
	}
	if len(a.ledger) != 0 {
		t.Fatal("in-memory history not cleared")
	}
	if w := call(a, "GET", "/api/settings", "", c); w.Code != 200 {
		t.Fatal("clear removed account")
	}
	if a.config.RetentionDays != 30 {
		t.Fatal("clear removed configuration")
	}
}
