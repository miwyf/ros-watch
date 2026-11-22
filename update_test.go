package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ros-watch/internal/appupdate"
)

func TestSystemUpdateAssetCompatibilityAndDigest(t *testing.T) {
	const next = "999.1.0"
	program := []byte("#!/bin/sh\n[ \"$1\" = update-info ] || exit 1\nprintf '%s\\n' '" + next + " " + appupdate.Compatibility + "'\n")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(program))
	name := "ros-watch-linux-" + runtime.GOARCH + "-" + appupdate.Compatibility
	for _, tc := range []struct {
		label, asset, digest                      string
		available, needsImage, checkErr, stageErr bool
	}{
		{label: "valid", asset: name, digest: digest, available: true},
		{label: "other runtime", asset: name + "-new", digest: digest, needsImage: true},
		{label: "missing digest", asset: name, checkErr: true},
		{label: "bad digest", asset: name, digest: "sha256:" + strings.Repeat("0", 64), available: true, stageErr: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest":
					jsonResponse(w, 200, map[string]any{
						"tag_name": "v" + next,
						"assets":   []updateAsset{{Name: tc.asset, Size: int64(len(program)), Digest: tc.digest}},
					})
				case "/download/v" + next + "/" + name:
					_, _ = w.Write(program)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			manager := newSystemUpdate(t.Context(), dir, nil)
			manager.apiURL, manager.downloadURL = server.URL+"/latest", server.URL+"/download/"
			info, err := manager.check(t.Context())
			if (err != nil) != tc.checkErr || info.Available != tc.available || info.NeedsImage != tc.needsImage {
				t.Fatalf("check = %+v, %v", info, err)
			}
			if !info.Available || (!tc.stageErr && runtime.GOOS != "linux") {
				return
			}
			if err := manager.stage(info); (err != nil) != tc.stageErr {
				t.Fatalf("stage = %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "pending"))
			if tc.stageErr {
				if !os.IsNotExist(err) {
					t.Fatal("invalid program was staged")
				}
				return
			}
			if err != nil || string(got) != string(program) {
				t.Fatalf("pending contents mismatch: %v", err)
			}
		})
	}
}

func TestSystemUpdateUsesNumericVersions(t *testing.T) {
	for _, version := range []string{"1.2.3", "1.1.9", "1.02.4", "1.3.0-rc.1", "1.3.0/path"} {
		if appupdate.Newer(version, "1.2.3") {
			t.Errorf("unexpected update from %q", version)
		}
	}
	if !appupdate.Newer("1.10.0", "1.2.3") {
		t.Fatal("numeric version comparison failed")
	}
}

func TestSystemUpdateRequiresAuthentication(t *testing.T) {
	a := &App{requests: make(chan struct{}, 1)}
	for _, request := range []*http.Request{
		httptest.NewRequest("GET", "/api/update", nil),
		httptest.NewRequest("GET", "/api/update/status", nil),
		httptest.NewRequest("POST", "/api/update", strings.NewReader(`{"version":"999.1.0"}`)),
	} {
		rec := httptest.NewRecorder()
		a.handler().ServeHTTP(rec, request)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d", request.Method, request.URL, rec.Code)
		}
	}
}
