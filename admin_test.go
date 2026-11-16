package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAdminRoutesRequireAuthentication(t *testing.T) {
	a := testApp(t)
	for _, path := range []string{"/api/admin/status", "/api/admin/history?node=a"} {
		if w := call(a, "GET", path, "", nil); w.Code != 401 {
			t.Fatalf("%s %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/admin/collect", "/api/admin/retention", "/api/admin/clear-history", "/api/admin/account"} {
		if w := call(a, "POST", path, `{}`, nil); w.Code != 401 {
			t.Fatalf("%s %d", path, w.Code)
		}
	}
	c := loginTest(t, a)
	w := call(a, "GET", "/api/admin/status", "", c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"engine":"SQLite"`) {
		t.Fatal(w.Body.String())
	}
	for _, secret := range []string{testPassword, testKey, a.salt, "password_hash"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret in status")
		}
	}
}

func TestAdminAccountPersistsAndRevokesSessions(t *testing.T) {
	a := testApp(t)
	c := loginTest(t, a)
	other := loginTest(t, a)
	bad := `{"username":"owner","current_password":"wrong","password":"new123"}`
	if w := call(a, "POST", "/api/admin/account", bad, c); w.Code != 400 {
		t.Fatal("wrong current password accepted")
	}
	body := `{"username":"owner","current_password":"` + testPassword + `","password":"new123"}`
	w := call(a, "POST", "/api/admin/account", body, c)
	if w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("account update failed", w.Body.String())
	}
	for _, cookie := range []*http.Cookie{c, other} {
		if w := call(a, "GET", "/api/admin/status", "", cookie); w.Code != 401 {
			t.Fatal("old session survived account change")
		}
	}
	if w := call(a, "POST", "/api/login", `{"username":"admin","password":"`+testPassword+`"}`, nil); w.Code != 401 {
		t.Fatal("old login accepted")
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newApp(a.dir, false, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.db.Close()
	if w := call(restarted, "POST", "/api/login", `{"username":"owner","password":"new123"}`, nil); w.Code != 200 {
		t.Fatal("changed account lost on restart", w.Body.String())
	}
	if w := call(restarted, "POST", "/api/login", `{"username":"admin","password":"`+testPassword+`"}`, nil); w.Code != 401 {
		t.Fatal("changed account lost on restart")
	}
}

func TestAdminManualCollectionAndCooldown(t *testing.T) {
	a := testApp(t)
	calls := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"servers":[{"id":"a","name":"Example","is_hidden":false}]}`)
	}))
	defer u.Close()
	c := loginTest(t, a)
	if w := call(a, "POST", "/api/admin/collect", `{}`, c); w.Code != 400 {
		t.Fatal("unconfigured collection accepted")
	}
	body, _ := json.Marshal(settings{u.URL, testKey, 300, 90})
	if w := call(a, "POST", "/api/settings", string(body), c); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(a, "POST", "/api/admin/collect", `{}`, c); w.Code != 429 {
		t.Fatal("collection cooldown bypassed", w.Code)
	}
	a.mu.Lock()
	a.lastAttempt = time.Now().Add(-31 * time.Second)
	a.mu.Unlock()
	if w := call(a, "POST", "/api/admin/collect", `{}`, c); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if calls != 2 {
		t.Fatal("unexpected upstream calls", calls)
	}
	a.fetchMu.Lock()
	w := call(a, "POST", "/api/admin/collect", `{}`, c)
	a.fetchMu.Unlock()
	if w.Code != 409 {
		t.Fatal("concurrent collection not rejected")
	}
}

func TestAdminHistoryPagingAndBoundParameters(t *testing.T) {
	a := testApp(t)
	now := time.Now().UTC().Truncate(time.Second)
	saveSample(t, a, now, 100, 200)
	for i := 1; i <= 105; i++ {
		date := now.AddDate(0, 0, -i).Format("2006-01-02")
		if _, err := a.db.Exec(`INSERT INTO daily_usage VALUES('node',?,10,20,30,1,0,0)`, date); err != nil {
			t.Fatal(err)
		}
	}
	c := loginTest(t, a)
	from := now.AddDate(0, 0, -110).Format("2006-01-02")
	to := now.Format("2006-01-02")
	path := "/api/admin/history?node=node&from=" + from + "&to=" + to
	w := call(a, "GET", path, "", c)
	var result struct {
		Days []day `json:"days"`
		More bool  `json:"has_more"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Days) != 100 || !result.More {
		t.Fatal("first page", w.Body.String())
	}
	w = call(a, "GET", path+"&offset=100", "", c)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Days) != 6 || result.More {
		t.Fatal("second page", w.Body.String())
	}
	w = call(a, "GET", "/api/admin/history?node="+url.QueryEscape(`' OR 1=1 --`), "", c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"days":[]`) {
		t.Fatal("unbound history query", w.Body.String())
	}
	if w := call(a, "GET", "/api/admin/history?node=node&from=invalid", "", c); w.Code != 400 {
		t.Fatal("invalid date accepted")
	}
}
