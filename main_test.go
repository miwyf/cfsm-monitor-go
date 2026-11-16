package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPassword = "test-admin-password-123"
const testKey = "test-app-key-12345678"

func testApp(t *testing.T) *app {
	t.Helper()
	a, err := newApp(t.TempDir(), false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	w := call(a, "POST", "/api/setup", `{"username":"admin","password":"`+testPassword+`","confirm_password":"`+testPassword+`"}`, nil)
	if w.Code != 201 {
		t.Fatalf("setup %d %s", w.Code, w.Body.String())
	}
	return a
}

func call(a *app, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == "POST" {
		r.Header.Set("X-CFSM-Request", "1")
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	return w
}

func loginTest(t *testing.T, a *app) *http.Cookie {
	t.Helper()
	w := call(a, "POST", "/api/login", `{"username":"admin","password":"`+testPassword+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body.String())
	}
	c := w.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 43200 {
		t.Fatal("unsafe cookie attributes")
	}
	return c
}

func TestVisibilityAndLogout(t *testing.T) {
	a := testApp(t)
	raws := []map[string]any{
		{"id": "public", "name": "Public", "is_hidden": "0", "secret": "never-forward", "note": "private-note", "ip_v4": "192.0.2.10"},
		{"id": "hidden1", "name": "PrivateOne", "is_hidden": "1"},
		{"id": "hidden2", "name": "PrivateTwo", "is_hidden": true},
		{"id": "missing", "name": "MissingVisibility"},
		{"id": "null", "name": "NullVisibility", "is_hidden": nil},
		{"id": "unknown", "name": "UnknownVisibility", "is_hidden": "unexpected"},
	}
	for _, raw := range raws {
		s, _ := normalizeServer(raw)
		a.servers = append(a.servers, s)
	}
	a.available = true
	w := call(a, "GET", "/api/servers", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Private") || strings.Contains(w.Body.String(), "Visibility") || strings.Contains(w.Body.String(), "hidden1") {
		t.Fatalf("public visibility: %s", w.Body.String())
	}
	for _, secret := range []string{"never-forward", "private-note", "192.0.2.10"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("raw private field escaped DTO")
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("data could be cached")
	}
	c := loginTest(t, a)
	w = call(a, "GET", "/api/servers", "", c)
	if !strings.Contains(w.Body.String(), "PrivateOne") || !strings.Contains(w.Body.String(), "MissingVisibility") {
		t.Fatal("admin cannot see private servers")
	}
	w = call(a, "POST", "/api/logout", `{}`, c)
	if w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("cookie not cleared")
	}
	w = call(a, "GET", "/api/servers", "", c)
	if strings.Contains(w.Body.String(), "PrivateOne") {
		t.Fatal("logged-out cookie still authorized")
	}
	for _, path := range []string{"/api/settings", "/data/settings.json", "/data/traffic.json", "/api/server?id=hidden1"} {
		w = call(a, "GET", path, "", nil)
		if w.Code == 200 {
			t.Fatalf("unexpected exposed endpoint: %s", path)
		}
	}
}

func TestLoginGuards(t *testing.T) {
	a := testApp(t)
	wrong := `{"username":"admin","password":"wrong"}`
	if w := call(a, "POST", "/api/login", wrong, nil); w.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(wrong))
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("simple cross-site request accepted")
	}
	r = httptest.NewRequest("POST", "/api/login", strings.NewReader(wrong))
	r.Header.Set("X-CFSM-Request", "1")
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin request accepted")
	}
	a.mu.Lock()
	a.attempts["ip:192.0.2.1"] = loginWindow{time.Now(), 10}
	a.mu.Unlock()
	if w := call(a, "POST", "/api/login", wrong, nil); w.Code != 429 {
		t.Fatalf("rate limit %d", w.Code)
	}
	a.mu.Lock()
	a.attempts = map[string]loginWindow{}
	a.mu.Unlock()
	a.secure = true
	c := loginTest(t, a)
	if !c.Secure {
		t.Fatal("HTTPS cookie lacks secure flag")
	}
	a.mu.Lock()
	a.sessions[c.Value] = session{time.Now().Add(-time.Second)}
	a.mu.Unlock()
	if w := call(a, "GET", "/api/settings", "", c); w.Code != 401 {
		t.Fatal("expired cookie accepted")
	}
}

func TestSettingsPersistenceAndRedaction(t *testing.T) {
	a := testApp(t)
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/servers" || r.Header.Get("X-App-Password") != testKey {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"servers":[{"id":"node","name":"Example","is_hidden":"0","cpu":"12.5","traffic_limit":"1000","net_rx_monthly":"1073741824","net_tx_monthly":"2147483648","secret":"bad"}],"stats":{"secret":"not-forwarded"}}`)
	}))
	defer upstream.Close()
	if w := call(a, "GET", "/api/servers", "", nil); w.Code != 503 {
		t.Fatal("unconfigured server must be unavailable")
	}
	if w := call(a, "POST", "/api/settings", `{}`, nil); w.Code != 401 {
		t.Fatal("unauthenticated settings write")
	}
	c := loginTest(t, a)
	body, _ := json.Marshal(settings{upstream.URL, testKey, 300, 90})
	w := call(a, "POST", "/api/settings", string(body), c)
	if w.Code != 200 {
		t.Fatalf("save %d: %s", w.Code, w.Body.String())
	}
	w = call(a, "GET", "/api/settings", "", c)
	if strings.Contains(w.Body.String(), testKey) || !strings.Contains(w.Body.String(), `"key_configured":true`) {
		t.Fatal("key redaction failed")
	}
	for i := 0; i < 5; i++ {
		w = call(a, "GET", "/api/servers", "", nil)
	}
	if calls != 1 {
		t.Fatal("page refresh caused upstream calls")
	}
	if strings.Contains(w.Body.String(), testKey) || strings.Contains(w.Body.String(), upstream.URL) {
		t.Fatal("public config leak")
	}
	if !strings.Contains(w.Body.String(), `"used_gib":3`) {
		t.Fatal("traffic units not normalized")
	}
	// Empty key preserves it; invalid settings never overwrite a working config.
	body, _ = json.Marshal(settings{upstream.URL, "", 600, 90})
	w = call(a, "POST", "/api/settings", string(body), c)
	if w.Code != 200 || a.config.Key != testKey {
		t.Fatal("blank key not preserved")
	}
	body, _ = json.Marshal(settings{upstream.URL, "incorrect-key-1234", 600, 90})
	w = call(a, "POST", "/api/settings", string(body), c)
	if w.Code != 400 || a.config.Key != testKey {
		t.Fatal("failed validation replaced working settings")
	}
	for _, file := range []string{"cfsm.db"} {
		if _, err := os.Stat(filepath.Join(a.dir, file)); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := newApp(a.dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.db.Close() })
	if restarted.config.URL != upstream.URL || restarted.config.Key != testKey || restarted.config.PollSeconds != 600 {
		t.Fatal("settings not restored")
	}
	if restarted.available {
		t.Fatal("cached data must not be exposed before first poll")
	}
	restarted.refresh(context.Background())
	if w := call(restarted, "GET", "/api/servers", "", nil); w.Code != 200 {
		t.Fatal("restart poll failed")
	}
}

func TestPollFailureAndVisibilityChanges(t *testing.T) {
	a := testApp(t)
	var mu sync.Mutex
	hidden := false
	fail := false
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(500)
			return
		}
		fmt.Fprintf(w, `{"servers":[{"id":"node","name":"Flip","is_hidden":%t}]}`, hidden)
	}))
	defer u.Close()
	a.config = settings{u.URL, testKey, 300, 90}
	a.refresh(context.Background())
	if w := call(a, "GET", "/api/servers", "", nil); !strings.Contains(w.Body.String(), "Flip") {
		t.Fatal("public missing")
	}
	mu.Lock()
	hidden = true
	mu.Unlock()
	a.refresh(context.Background())
	if w := call(a, "GET", "/api/servers", "", nil); strings.Contains(w.Body.String(), "Flip") {
		t.Fatal("newly hidden node remained public")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	a.refresh(context.Background())
	if w := call(a, "GET", "/api/servers", "", nil); w.Code != 503 || strings.Contains(w.Body.String(), "Flip") {
		t.Fatal("failed poll exposed stale visibility")
	}
}

func TestRedirectDoesNotForwardAppKey(t *testing.T) {
	a := testApp(t)
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	_, err := a.fetch(context.Background(), settings{redirect.URL, testKey, 300, 90})
	if err == nil || leaked {
		t.Fatal("redirect followed with credentials")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, url := range []string{"file:///tmp/test", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=x", "https://example.com/#fragment"} {
		c := settings{url, testKey, 300, 90}
		if validateSettings(&c) == nil {
			t.Errorf("accepted %s", url)
		}
	}
}

func TestConcurrentReadsDuringPoll(t *testing.T) {
	a := testApp(t)
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"servers":[{"id":"a","is_hidden":false}]}`)
	}))
	defer u.Close()
	a.config = settings{u.URL, testKey, 60, 90}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				a.refresh(context.Background())
				call(a, "GET", "/api/servers", "", nil)
			}
		}()
	}
	wg.Wait()
}
