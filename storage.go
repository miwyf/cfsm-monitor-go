package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type dayKey struct{ ID, Date string }

func (a *app) openStore() error {
	path := filepath.Join(a.dir, "cfsm.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	a.db, err = sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	a.db.SetMaxOpenConns(1)
	a.db.SetMaxIdleConns(1)
	if _, err = a.db.Exec(`PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;`); err != nil {
		return err
	}
	var version int
	if err = a.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return errors.New("unsupported SQLite schema version")
	}
	_, err = a.db.Exec(`
CREATE TABLE IF NOT EXISTS app_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), url TEXT NOT NULL DEFAULT '', app_key TEXT NOT NULL DEFAULT '',
 poll_seconds INTEGER NOT NULL CHECK(poll_seconds BETWEEN 60 AND 86400),
 retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 30 AND 3650));
INSERT OR IGNORE INTO app_settings VALUES(1,'','',300,90);
CREATE TABLE IF NOT EXISTS administrator (
 id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL, password_hash BLOB NOT NULL, salt TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS nodes (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, region TEXT NOT NULL, group_name TEXT NOT NULL,
 hidden INTEGER NOT NULL CHECK(hidden IN (0,1)), last_seen REAL NOT NULL, active INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS counters (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE, stamp REAL NOT NULL,
 rx REAL NOT NULL, tx REAL NOT NULL, mode TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS daily_usage (
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, day TEXT NOT NULL,
 in_bytes REAL NOT NULL CHECK(in_bytes>=0), out_bytes REAL NOT NULL CHECK(out_bytes>=0),
 used_bytes REAL NOT NULL CHECK(used_bytes>=0), samples INTEGER NOT NULL CHECK(samples>=0),
 partial INTEGER NOT NULL CHECK(partial IN (0,1)), estimated INTEGER NOT NULL CHECK(estimated IN (0,1)),
 PRIMARY KEY(node_id,day));
CREATE INDEX IF NOT EXISTS daily_usage_day ON daily_usage(day);
CREATE TABLE IF NOT EXISTS notify_channels (id TEXT PRIMARY KEY, config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS notify_marks (
 channel_id TEXT NOT NULL REFERENCES notify_channels(id) ON DELETE CASCADE,
 event_key TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(channel_id,event_key));
CREATE TABLE IF NOT EXISTS notify_jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, channel_id TEXT NOT NULL REFERENCES notify_channels(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, event_key TEXT NOT NULL, payload TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
 next_at INTEGER NOT NULL, created_at INTEGER NOT NULL, sent_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', UNIQUE(channel_id,event_key));
CREATE INDEX IF NOT EXISTS notify_jobs_pending ON notify_jobs(status,next_at);
PRAGMA user_version=2;`)
	if err != nil {
		return err
	}
	err = a.db.QueryRow(`SELECT username,password_hash,salt FROM administrator WHERE id=1`).Scan(&a.username, &a.passwordHash, &a.salt)
	if err == sql.ErrNoRows {
		err = nil // First visit creates the administrator through /api/setup.
	}
	if err != nil {
		return err
	}
	if err = a.db.QueryRow(`SELECT url,app_key,poll_seconds,retention_days FROM app_settings WHERE id=1`).Scan(&a.config.URL, &a.config.Key, &a.config.PollSeconds, &a.config.RetentionDays); err != nil {
		return err
	}
	if a.config.URL != "" {
		if err = validateSettings(&a.config); err != nil {
			return err
		}
	}
	return a.loadLedger()
}

func (a *app) loadLedger() error {
	loaded := map[string]*record{}
	rows, err := a.db.Query(`SELECT node_id,stamp,rx,tx,mode FROM counters`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var s sample
		if err = rows.Scan(&id, &s.Stamp, &s.RX, &s.TX, &s.Mode); err != nil {
			rows.Close()
			return err
		}
		loaded[id] = &record{Last: &s, Days: map[string]*day{}}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = a.db.Query(`SELECT node_id,day,in_bytes,out_bytes,used_bytes,samples,partial,estimated FROM daily_usage WHERE day>=?`, a.retentionCutoff(time.Now()))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var d day
		if err = rows.Scan(&id, &d.Date, &d.In, &d.Out, &d.Used, &d.Samples, &d.Partial, &d.Estimated); err != nil {
			rows.Close()
			return err
		}
		if loaded[id] == nil {
			loaded[id] = &record{Days: map[string]*day{}}
		}
		loaded[id].Days[d.Date] = &d
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	a.ledger = loaded
	return nil
}

func cloneLedger(source map[string]*record) map[string]*record {
	copy := make(map[string]*record, len(source))
	for id, r := range source {
		if r == nil {
			continue
		}
		next := &record{Days: make(map[string]*day, len(r.Days))}
		if r.Last != nil {
			s := *r.Last
			next.Last = &s
		}
		for date, d := range r.Days {
			if d != nil {
				v := *d
				next.Days[date] = &v
			}
		}
		copy[id] = next
	}
	return copy
}

func (a *app) retentionCutoff(now time.Time) string {
	days := a.config.RetentionDays
	if days < 30 {
		days = 90
	}
	return now.In(a.zone).AddDate(0, 0, 1-days).Format("2006-01-02")
}

// mu and fetchMu serialize publication and source changes. Failed transactions
// restore the in-memory accounting baseline so the next poll can retry safely.
func (a *app) commitSnapshot(items []server, now time.Time, next *settings) (err error) {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, previousLedger := a.config, a.ledger
	a.ledger = cloneLedger(a.ledger)
	a.dirty = map[dayKey]bool{}
	defer func() {
		if err != nil {
			a.config = previous
			a.ledger = previousLedger
		}
		a.dirty = map[dayKey]bool{}
	}()
	if next != nil {
		a.config = *next
		if previous.URL != next.URL || previous.Key != next.Key {
			if _, err = tx.Exec(`DELETE FROM notify_jobs; DELETE FROM notify_marks;`); err != nil {
				return err
			}
			if _, err = tx.Exec(`DELETE FROM nodes`); err != nil {
				return err
			}
			a.ledger = map[string]*record{}
		}
		if _, err = tx.Exec(`UPDATE app_settings SET url=?,app_key=?,poll_seconds=?,retention_days=? WHERE id=1`, next.URL, next.Key, next.PollSeconds, next.RetentionDays); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE nodes SET active=0`); err != nil {
		return err
	}
	active := map[string]bool{}
	for _, s := range items {
		active[s.ID] = true
		_, err = tx.Exec(`INSERT INTO nodes(id,name,region,group_name,hidden,last_seen,active) VALUES(?,?,?,?,?,?,1)
ON CONFLICT(id) DO UPDATE SET name=excluded.name,region=excluded.region,group_name=excluded.group_name,hidden=excluded.hidden,last_seen=excluded.last_seen,active=1`, s.ID, s.Name, s.Region, s.Group, s.Hidden, s.Stamp)
		if err != nil {
			return err
		}
		a.ingest(s, now)
		if r := a.ledger[s.ID]; r != nil && r.Last != nil {
			v := r.Last
			_, err = tx.Exec(`INSERT INTO counters(node_id,stamp,rx,tx,mode) VALUES(?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET stamp=excluded.stamp,rx=excluded.rx,tx=excluded.tx,mode=excluded.mode WHERE counters.stamp!=excluded.stamp OR counters.rx!=excluded.rx OR counters.tx!=excluded.tx OR counters.mode!=excluded.mode`, s.ID, v.Stamp, v.RX, v.TX, v.Mode)
			if err != nil {
				return err
			}
		}
	}
	for key := range a.dirty {
		r := a.ledger[key.ID]
		if r == nil {
			continue
		}
		d := r.Days[key.Date]
		if d == nil {
			continue
		}
		_, err = tx.Exec(`INSERT INTO daily_usage(node_id,day,in_bytes,out_bytes,used_bytes,samples,partial,estimated) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(node_id,day) DO UPDATE SET in_bytes=excluded.in_bytes,out_bytes=excluded.out_bytes,used_bytes=excluded.used_bytes,samples=excluded.samples,partial=excluded.partial,estimated=excluded.estimated`, key.ID, d.Date, d.In, d.Out, d.Used, d.Samples, d.Partial, d.Estimated)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM nodes WHERE active=0`); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM daily_usage WHERE day<?`, a.retentionCutoff(now)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for id := range a.ledger {
		if !active[id] {
			delete(a.ledger, id)
		}
	}
	a.servers = items
	a.fetchedAt = now
	a.available = true
	return nil
}

func (a *app) pruneHistory(days int) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := time.Now().In(a.zone).AddDate(0, 0, 1-days).Format("2006-01-02")
	if _, err = tx.Exec(`UPDATE app_settings SET retention_days=? WHERE id=1`, days); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM daily_usage WHERE day<?`, cutoff); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.config.RetentionDays = days
	for _, r := range a.ledger {
		for date := range r.Days {
			if date < cutoff {
				delete(r.Days, date)
			}
		}
	}
	return nil
}

func (a *app) clearHistory() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM daily_usage; DELETE FROM counters;`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.ledger = map[string]*record{}
	return nil
}
