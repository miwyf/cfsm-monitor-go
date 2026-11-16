package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const setupBody = `{"username":"owner","password":"owner6","confirm_password":"owner6"}`

func freshApp(t *testing.T) *app {
	t.Helper()
	a, err := newApp(t.TempDir(), false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

func TestFirstVisitSetupAndRestart(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "unwanted-env-user")
	t.Setenv("ADMIN_PASSWORD", testPassword)
	a := freshApp(t)
	if !a.setupRequired() || a.username != "" {
		t.Fatal("fresh database has a default or environment account")
	}
	if w := call(a, "GET", "/", "", nil); w.Code != 303 || w.Header().Get("Location") != "/admin" {
		t.Fatal("first visit did not redirect to setup")
	}
	if w := call(a, "POST", "/api/login", `{"username":"admin","password":"admin"}`, nil); w.Code != 409 {
		t.Fatal("login enabled before setup")
	}
	w := call(a, "GET", "/api/session", "", nil)
	var state map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state["setup_required"] || state["admin"] {
		t.Fatal("wrong initialization state", w.Body.String())
	}
	w = call(a, "POST", "/api/setup", setupBody, nil)
	if w.Code != 201 || len(w.Result().Cookies()) != 1 || a.setupRequired() {
		t.Fatal("setup failed", w.Code, w.Body.String())
	}
	c := w.Result().Cookies()[0]
	if w := call(a, "GET", "/api/admin/status", "", c); w.Code != 200 {
		t.Fatal("setup did not log in")
	}
	if w := call(a, "POST", "/api/setup", setupBody, c); w.Code != 409 {
		t.Fatal("second setup accepted")
	}
	var username string
	var hash []byte
	if err := a.db.QueryRow(`SELECT username,password_hash FROM administrator`).Scan(&username, &hash); err != nil || username != "owner" || string(hash) == "owner6" {
		t.Fatal("account not hashed and saved", err)
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newApp(a.dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.db.Close()
	if restarted.setupRequired() || call(restarted, "POST", "/api/setup", setupBody, nil).Code != 409 {
		t.Fatal("setup reopened after restart")
	}
	if w := call(restarted, "POST", "/api/login", `{"username":"owner","password":"owner6"}`, nil); w.Code != 200 {
		t.Fatal("created account lost after restart", w.Body.String())
	}
	if w := call(restarted, "GET", "/", "", nil); w.Code != 200 {
		t.Fatal("initialized public dashboard unavailable")
	}
}

func TestSetupValidationAndRequestGuards(t *testing.T) {
	a := freshApp(t)
	for _, body := range []string{
		`{}`, `{"username":" ","password":"long-password-123","confirm_password":"long-password-123"}`,
		`{"username":"owner","password":"short","confirm_password":"short"}`,
		`{"username":"owner","password":"long-password-123","confirm_password":"different-password"}`,
		`{"username":"owner","password":"long-password-123","confirm_password":"long-password-123","extra":true}`,
	} {
		if w := call(a, "POST", "/api/setup", body, nil); w.Code != 400 {
			t.Fatal("invalid setup accepted", w.Code, body)
		}
	}
	for _, origin := range []string{"", "https://evil.example"} {
		r := httptest.NewRequest("POST", "/api/setup", strings.NewReader(setupBody))
		if origin != "" {
			r.Header.Set("X-CFSM-Request", "1")
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("unguarded setup request accepted")
		}
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM administrator`).Scan(&count); err != nil || count != 0 || !a.setupRequired() {
		t.Fatal("invalid setup wrote account", err)
	}
	for _, path := range []string{"/api/admin/status", "/api/settings"} {
		if call(a, "GET", path, "", nil).Code != 401 {
			t.Fatal("admin data accessible before setup")
		}
	}
}

func TestConcurrentSetupCreatesOneAdministrator(t *testing.T) {
	a := freshApp(t)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- call(a, "POST", "/api/setup", setupBody, nil).Code
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	for code := range results {
		if code == 201 {
			created++
		} else if code != 409 {
			t.Fatalf("unexpected setup response: %d", code)
		}
	}
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM administrator`).Scan(&count); err != nil || count != 1 || created != 1 {
		t.Fatal("setup was not exclusive", err, count, created)
	}
}

func TestSetupFailedWriteCanRetry(t *testing.T) {
	a := freshApp(t)
	if _, err := a.db.Exec(`CREATE TRIGGER reject_admin BEFORE INSERT ON administrator BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if w := call(a, "POST", "/api/setup", setupBody, nil); w.Code != 500 || !a.setupRequired() || len(a.sessions) != 0 {
		t.Fatal("failed setup published account or session", w.Code)
	}
	if _, err := a.db.Exec(`DROP TRIGGER reject_admin`); err != nil {
		t.Fatal(err)
	}
	if w := call(a, "POST", "/api/setup", setupBody, nil); w.Code != 201 {
		t.Fatal("setup retry failed", w.Code, w.Body.String())
	}
}
