package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateChecksCompatibilityAndDigest(t *testing.T) {
	const candidate = "999.1.0"
	artifact := []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo 'cfsm-monitor-go v" + candidate +
		"' ;;\n--update-compat) echo '" + updateCompat() + "' ;;\n*) exit 1 ;;\nesac\n")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(artifact))
	assetName := "cfsm-monitor-go-linux-" + runtime.GOARCH + "-" + updateCompat()
	for _, tc := range []struct {
		name      string
		asset     string
		digest    string
		available bool
		needsPull bool
		checkFail bool
		stageFail bool
	}{
		{name: "valid", asset: assetName, digest: digest, available: true},
		{name: "different runtime", asset: "incompatible", digest: digest, needsPull: true},
		{name: "missing digest", asset: assetName, checkFail: true},
		{name: "wrong SHA256", asset: assetName, digest: "sha256:" + strings.Repeat("0", 64), available: true, stageFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest":
					respond(w, 200, map[string]any{
						"tag_name": "v" + candidate,
						"assets":   []releaseAsset{{Name: tc.asset, Size: int64(len(artifact)), Digest: tc.digest}},
					})
				case "/download/v" + candidate + "/" + assetName:
					w.Write(artifact)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			u := newUpdateManager(t.Context(), dir, nil)
			u.apiURL, u.downloadBase = server.URL+"/latest", server.URL+"/download/"
			info, err := u.check(t.Context())
			if (err != nil) != tc.checkFail || info.Available != tc.available || info.NeedsImage != tc.needsPull {
				t.Fatalf("check = %+v, %v", info, err)
			}
			if !info.Available || (!tc.stageFail && runtime.GOOS != "linux") {
				return
			}
			if err := u.stage(info); (err != nil) != tc.stageFail {
				t.Fatalf("stage = %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.stageFail {
				if len(entries) != 0 {
					t.Fatalf("failed asset was staged: %v", entries)
				}
				return
			}
			staged, err := os.ReadFile(filepath.Join(dir, "pending"))
			if err != nil || string(staged) != string(artifact) {
				t.Fatalf("staged content does not match: %v", err)
			}
		})
	}
}

func TestUpdateRejectsOldOrNonNumericVersions(t *testing.T) {
	for _, candidate := range []string{"1.2.3", "1.1.9", "1.2.4-rc.1", "1.02.4", "1.2.4/other"} {
		if newerRelease(candidate, "1.2.3") {
			t.Errorf("%q was accepted", candidate)
		}
	}
	if !newerRelease("1.10.0", "1.2.3") {
		t.Fatal("numeric version ordering failed")
	}
}

func TestUpdateRoutesRequireAdmin(t *testing.T) {
	a := &app{update: newUpdateManager(context.Background(), t.TempDir(), nil)}
	mux := http.NewServeMux()
	a.adminRoutes(mux)
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/api/admin/update", nil),
		httptest.NewRequest("GET", "/api/admin/update/status", nil),
		httptest.NewRequest("POST", "/api/admin/update", strings.NewReader(`{"version":"999.1.0"}`)),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d", req.Method, req.URL, rec.Code)
		}
	}
}
