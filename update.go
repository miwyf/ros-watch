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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"ros-watch/internal/appupdate"
)

const (
	updateAPI      = "https://api.github.com/repos/miwyf/ros-watch/releases/latest"
	updateDownload = "https://github.com/miwyf/ros-watch/releases/download/"
	maxUpdateSize  = 64 << 20
)

type updateAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type updateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Available  bool   `json:"available"`
	NeedsImage bool   `json:"needs_image,omitempty"`
	asset      updateAsset
}

type updateProgress struct {
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
}

type systemUpdate struct {
	ctx         context.Context
	dir         string
	restart     chan<- struct{}
	client      *http.Client
	apiURL      string
	downloadURL string
	mu          sync.Mutex
	progress    updateProgress
}

func newSystemUpdate(ctx context.Context, dir string, restart chan<- struct{}) *systemUpdate {
	return &systemUpdate{
		ctx: ctx, dir: dir, restart: restart, apiURL: updateAPI, downloadURL: updateDownload,
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

func (u *systemUpdate) get(ctx context.Context, address, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "ros-watch/"+Version)
	return u.client.Do(req)
}

func (u *systemUpdate) check(ctx context.Context) (updateInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info := updateInfo{Current: Version}
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
		Tag        string        `json:"tag_name"`
		Draft      bool          `json:"draft"`
		Prerelease bool          `json:"prerelease"`
		Assets     []updateAsset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&release); err != nil {
		return info, err
	}
	version, hasPrefix := strings.CutPrefix(release.Tag, "v")
	if _, valid := appupdate.ParseVersion(version); !hasPrefix || !valid || release.Draft || release.Prerelease {
		return info, errors.New("unexpected GitHub release tag")
	}
	info.Latest = version
	if !appupdate.Newer(version, Version) {
		return info, nil
	}
	name := "ros-watch-linux-" + runtime.GOARCH + "-" + appupdate.Compatibility
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		digest, ok := strings.CutPrefix(asset.Digest, "sha256:")
		data, err := hex.DecodeString(digest)
		if !ok || err != nil || len(data) != sha256.Size || asset.Size <= 0 || asset.Size > maxUpdateSize {
			return info, errors.New("invalid GitHub release asset size or digest")
		}
		info.Available, info.asset = true, asset
		return info, nil
	}
	info.NeedsImage = true
	return info, nil
}

func (u *systemUpdate) stage(info updateInfo) error {
	ctx, cancel := context.WithTimeout(u.ctx, 3*time.Minute)
	defer cancel()
	res, err := u.get(ctx, u.downloadURL+"v"+info.Latest+"/"+info.asset.Name, "application/octet-stream")
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
	version, compatibility, err := appupdate.Inspect(ctx, f.Name())
	if err != nil {
		return errors.New("升级程序试运行失败，请检查数据卷允许执行文件")
	}
	if version != info.Latest || compatibility != appupdate.Compatibility {
		return errors.New("升级程序版本或兼容性校验失败")
	}
	if err := os.Rename(f.Name(), filepath.Join(u.dir, "pending")); err != nil {
		return errors.New("无法保存待切换的升级程序")
	}
	return nil
}

func (a *App) updateCheck(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	if a.update == nil {
		apiError(w, http.StatusConflict, "当前运行方式不支持应用内升级")
		return
	}
	info, err := a.update.check(r.Context())
	if err != nil {
		log.Printf("Check update: %v", err)
		apiError(w, http.StatusBadGateway, "无法读取 GitHub 发布信息")
		return
	}
	jsonResponse(w, http.StatusOK, info)
}

func (a *App) updateStatus(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authorized(w, r); !ok {
		return
	}
	progress := updateProgress{Stage: "idle"}
	if u := a.update; u != nil {
		u.mu.Lock()
		if u.progress.Stage != "" {
			progress = u.progress
		}
		u.mu.Unlock()
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"enabled": a.update != nil, "current": Version, "stage": progress.Stage, "error": progress.Error,
	})
}

func (a *App) updateStart(w http.ResponseWriter, r *http.Request) {
	_, s, ok := a.authorized(w, r)
	if !ok {
		return
	}
	if !validCSRF(r, s) {
		apiError(w, http.StatusBadRequest, "页面验证已过期")
		return
	}
	u := a.update
	if u == nil {
		apiError(w, http.StatusConflict, "当前运行方式不支持应用内升级")
		return
	}
	var request struct {
		Version string `json:"version"`
	}
	if err := readJSON(r, &request); err != nil || !appupdate.Newer(request.Version, Version) {
		apiError(w, http.StatusBadRequest, "升级版本无效")
		return
	}
	u.mu.Lock()
	if u.progress.Stage == "downloading" || u.progress.Stage == "restarting" {
		u.mu.Unlock()
		apiError(w, http.StatusConflict, "升级已在运行")
		return
	}
	u.progress = updateProgress{Stage: "downloading"}
	u.mu.Unlock()
	jsonResponse(w, http.StatusAccepted, map[string]bool{"ok": true})
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
