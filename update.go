package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateExitCode = 42
	maxUpdateBytes = 64 << 20
	releasesAPI    = "https://api.github.com/repos/miwyf/cfsm-monitor-go/releases/latest"
	releases       = "https://github.com/miwyf/cfsm-monitor-go/releases/download/"
)

func updateCompat() string {
	return fmt.Sprintf("runtime1-db%d", sqliteSchemaVersion)
}

type releaseAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type updateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Available  bool   `json:"available"`
	NeedsImage bool   `json:"needs_image,omitempty"`
	asset      releaseAsset
}

type updateProgress struct {
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
}

type updateManager struct {
	ctx          context.Context
	dir          string
	restart      chan<- struct{}
	client       *http.Client
	apiURL       string
	downloadBase string
	mu           sync.Mutex
	progress     updateProgress
}

func newUpdateManager(ctx context.Context, dir string, restart chan<- struct{}) *updateManager {
	return &updateManager{
		ctx: ctx, dir: dir, restart: restart, apiURL: releasesAPI, downloadBase: releases,
		client: &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			switch req.URL.Host {
			case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
				if req.URL.Scheme == "https" && req.URL.User == nil && len(via) < 5 {
					return nil
				}
			}
			return errors.New("unexpected GitHub release redirect")
		}},
	}
}

func releaseVersion(value string) ([3]uint64, bool) {
	var version [3]uint64
	parts := strings.Split(value, ".")
	if len(parts) != len(version) {
		return version, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return version, false
		}
		version[i] = n
	}
	return version, true
}

func newerRelease(candidate, installed string) bool {
	next, ok := releaseVersion(candidate)
	have, valid := releaseVersion(installed)
	if !ok || !valid {
		return false
	}
	for i := range next {
		if next[i] != have[i] {
			return next[i] > have[i]
		}
	}
	return false
}

func (u *updateManager) get(ctx context.Context, address, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "cfsm-monitor-go/"+appVersion())
	return u.client.Do(req)
}

func (u *updateManager) check(ctx context.Context) (updateInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info := updateInfo{Current: appVersion()}
	res, err := u.get(ctx, u.apiURL, "application/vnd.github+json")
	if err != nil {
		return info, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return info, nil
	}
	if res.StatusCode != http.StatusOK {
		return info, fmt.Errorf("GitHub release status %d", res.StatusCode)
	}
	var release struct {
		Tag        string         `json:"tag_name"`
		Draft      bool           `json:"draft"`
		Prerelease bool           `json:"prerelease"`
		Assets     []releaseAsset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&release); err != nil {
		return info, err
	}
	version, hasPrefix := strings.CutPrefix(release.Tag, "v")
	if _, valid := releaseVersion(version); !hasPrefix || !valid || release.Draft || release.Prerelease {
		return info, errors.New("unexpected GitHub release tag")
	}
	info.Latest = version
	if !newerRelease(version, info.Current) {
		return info, nil
	}
	name := "cfsm-monitor-go-linux-" + runtime.GOARCH + "-" + updateCompat()
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		digest, ok := strings.CutPrefix(asset.Digest, "sha256:")
		data, err := hex.DecodeString(digest)
		if !ok || err != nil || len(data) != sha256.Size || asset.Size <= 0 || asset.Size > maxUpdateBytes {
			return info, errors.New("invalid GitHub release asset size or digest")
		}
		info.Available = true
		info.asset = asset
		return info, nil
	}
	info.NeedsImage = true
	return info, nil
}

func (u *updateManager) stage(info updateInfo) error {
	ctx, cancel := context.WithTimeout(u.ctx, 3*time.Minute)
	defer cancel()
	res, err := u.get(ctx, u.downloadBase+"v"+info.Latest+"/"+info.asset.Name, "application/octet-stream")
	if err != nil {
		return errors.New("无法下载升级程序")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("下载升级程序失败（HTTP %d）", res.StatusCode)
	}
	f, err := os.CreateTemp(u.dir, "download-")
	if err != nil {
		return errors.New("无法写入升级目录")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, info.asset.Size+1))
	if err != nil || n != info.asset.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != strings.ToLower(info.asset.Digest) {
		return errors.New("升级程序的大小或 SHA256 校验失败")
	}
	if f.Chmod(0700) != nil || f.Sync() != nil || f.Close() != nil {
		return errors.New("保存升级程序失败")
	}
	probe, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	version, err := exec.CommandContext(probe, f.Name(), "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "cfsm-monitor-go v"+info.Latest {
		return errors.New("升级程序试运行失败，请检查数据卷执行权限")
	}
	compat, err := exec.CommandContext(probe, f.Name(), "--update-compat").Output()
	if err != nil || strings.TrimSpace(string(compat)) != updateCompat() {
		return errors.New("升级程序与当前容器或数据库不兼容")
	}
	if err := os.Rename(f.Name(), filepath.Join(u.dir, "pending")); err != nil {
		return errors.New("无法保存待切换的升级程序")
	}
	return nil
}

func (a *app) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if a.update == nil {
		problem(w, http.StatusConflict, "请使用支持应用内升级的 Docker 镜像")
		return
	}
	info, err := a.update.check(r.Context())
	if err != nil {
		log.Printf("Check update: %v", err)
		problem(w, http.StatusBadGateway, "无法读取 GitHub 发布信息")
		return
	}
	respond(w, http.StatusOK, info)
}

func (a *app) updateStatus(w http.ResponseWriter, r *http.Request) {
	progress := updateProgress{Stage: "idle"}
	if u := a.update; u != nil {
		u.mu.Lock()
		if u.progress.Stage != "" {
			progress = u.progress
		}
		u.mu.Unlock()
	}
	respond(w, http.StatusOK, map[string]any{
		"enabled": a.update != nil, "current": appVersion(), "stage": progress.Stage, "error": progress.Error,
	})
}

func (a *app) startUpdate(w http.ResponseWriter, r *http.Request) {
	u := a.update
	if u == nil {
		problem(w, http.StatusConflict, "请使用支持应用内升级的 Docker 镜像")
		return
	}
	var request struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil || !newerRelease(request.Version, appVersion()) {
		problem(w, http.StatusBadRequest, "升级版本无效")
		return
	}
	u.mu.Lock()
	if u.progress.Stage == "downloading" || u.progress.Stage == "restarting" {
		u.mu.Unlock()
		problem(w, http.StatusConflict, "升级已在运行")
		return
	}
	u.progress = updateProgress{Stage: "downloading"}
	u.mu.Unlock()
	respond(w, http.StatusAccepted, map[string]bool{"ok": true})
	go func() {
		info, err := u.check(u.ctx)
		if err != nil {
			log.Printf("Check update before install: %v", err)
			err = errors.New("无法读取 GitHub 发布信息")
		} else if !info.Available || request.Version != info.Latest {
			err = errors.New("发布版本已变化，请重新检查更新")
		} else {
			err = u.stage(info)
		}
		u.mu.Lock()
		if err != nil {
			u.progress = updateProgress{Stage: "error", Error: err.Error()}
			u.mu.Unlock()
			return
		}
		u.progress = updateProgress{Stage: "restarting"}
		u.mu.Unlock()
		u.restart <- struct{}{}
	}()
}
