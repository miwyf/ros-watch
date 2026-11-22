package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ros-watch/internal/appupdate"
)

const imageProgram = "/ros-watch"

func startApplication(dataDir string, args []string) error {
	_ = os.Unsetenv(appupdate.DirEnv)
	execProgram := func(path string) error {
		return syscall.Exec(path, append([]string{path}, args...), os.Environ())
	}
	if os.Geteuid() == 0 {
		return execProgram(imageProgram)
	}
	dir := filepath.Join(dataDir, appupdate.DirName)
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		log.Printf("ROS Watch app updates disabled: %v", err)
		return execProgram(imageProgram)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || syscall.Access(dir, 3) != nil {
		log.Print("ROS Watch app updates disabled: update directory must be writable")
		return execProgram(imageProgram)
	}
	_ = os.Setenv(appupdate.DirEnv, dir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	imageVersion, compatibility, err := appupdate.Inspect(ctx, imageProgram)
	if err != nil || compatibility != appupdate.Compatibility {
		return fmt.Errorf("built-in program metadata unavailable: %v", err)
	}
	current := filepath.Join(dir, "current")
	active, version := imageProgram, imageVersion
	if newer := candidateVersion(ctx, current, imageVersion); newer != "" {
		active, version = current, newer
	}
	if len(args) > 0 {
		return execProgram(active)
	}
	return supervise(ctx, dir, active, version)
}

func candidateVersion(ctx context.Context, path, installed string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
		return ""
	}
	version, compatibility, err := appupdate.Inspect(ctx, path)
	if err != nil || compatibility != appupdate.Compatibility || !appupdate.Newer(version, installed) {
		return ""
	}
	return version
}

type childProcess struct {
	process *os.Process
	done    <-chan error
}

func launch(path string) (childProcess, error) {
	cmd := exec.Command(path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return childProcess{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return childProcess{process: cmd.Process, done: done}, nil
}

func (c childProcess) wait(ctx context.Context) error {
	select {
	case err := <-c.done:
		return err
	case <-ctx.Done():
		_ = c.process.Signal(syscall.SIGTERM)
		select {
		case <-c.done:
		case <-time.After(12 * time.Second):
			_ = c.process.Kill()
			<-c.done
		}
		return nil
	}
}

func supervise(ctx context.Context, dir, active, version string) error {
	current := filepath.Join(dir, "current")
	pending := filepath.Join(dir, "pending")
	trial := filepath.Join(dir, "trial")
	_ = os.Remove(trial)
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "download-") && entry.Type().IsRegular() {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	for ctx.Err() == nil {
		if next := candidateVersion(ctx, pending, version); next != "" {
			if err := os.Rename(pending, trial); err != nil {
				log.Printf("ROS Watch could not stage upgrade: %v", err)
			} else {
				child, err := launch(trial)
				if err == nil {
					if confirmTrial(ctx, child, trial, current, next) {
						active, version = current, next
						log.Printf("ROS Watch upgrade confirmed: %s", version)
						err = child.wait(ctx)
						if restartRequested(err) {
							continue
						}
						return err
					}
				}
				_ = os.Remove(trial)
				log.Printf("ROS Watch upgrade failed; restoring %s", version)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		_ = os.Remove(pending)
		child, err := launch(active)
		if err != nil {
			return err
		}
		err = child.wait(ctx)
		if !restartRequested(err) {
			return err
		}
	}
	return nil
}

func restartRequested(err error) bool {
	var status *exec.ExitError
	return errors.As(err, &status) && status.ExitCode() == appupdate.RestartCode
}

func confirmTrial(ctx context.Context, child childProcess, trial, current, version string) bool {
	deadline, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	host := os.Getenv("HTTP_HOST")
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	address := "http://" + net.JoinHostPort(host, port) + "/healthz"
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	healthy := 0
	for {
		select {
		case <-child.done:
			return false
		case <-deadline.Done():
			if ctx.Err() != nil {
				_ = child.wait(ctx)
			} else {
				_ = child.process.Kill()
				<-child.done
			}
			return false
		case <-ticker.C:
			if healthyVersion(deadline, client, address, version) {
				healthy++
			} else {
				healthy = 0
			}
			if healthy < 3 {
				continue
			}
			select {
			case <-child.done:
				return false
			default:
			}
			if deadline.Err() == nil && os.Rename(trial, current) == nil {
				return true
			}
			_ = child.process.Kill()
			<-child.done
			return false
		}
	}
}

func healthyVersion(ctx context.Context, client *http.Client, address, version string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	return res.StatusCode == http.StatusOK &&
		json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&health) == nil &&
		health.Status == "ok" && health.Version == version
}
