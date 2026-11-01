// A finite, synthetic UDP/HTTP probe for isolated test instances only.
package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type probe struct {
	client     *http.Client
	base, csrf string
}

func (p *probe) get(path string) (map[string]any, error) {
	r, err := p.client.Get(p.base + path)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, errors.New("HTTP query failed")
	}
	var result map[string]any
	err = json.NewDecoder(r.Body).Decode(&result)
	return result, err
}
func (p *probe) setup() error {
	r, err := p.client.Get(p.base + "/setup")
	if err != nil {
		return err
	}
	if r.Request.URL.Path != "/setup" {
		r.Body.Close()
		return errors.New("probe requires an uninitialized isolated instance")
	}
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	token := regexp.MustCompile(`name="csrf"\s+value="([^"]+)"`).FindSubmatch(data)
	if token == nil {
		return errors.New("missing form token")
	}
	password := rand.Text()
	r, err = p.client.PostForm(p.base+"/setup", url.Values{"username": {"probe-owner"}, "password": {password}, "confirm_password": {password}, "csrf": {string(token[1])}})
	if err != nil {
		return err
	}
	data, _ = io.ReadAll(r.Body)
	r.Body.Close()
	token = regexp.MustCompile(`name="csrf-token"\s+content="([^"]+)"`).FindSubmatch(data)
	if r.StatusCode != 200 || token == nil {
		return errors.New("test account creation failed")
	}
	p.csrf = string(token[1])
	return nil
}
func (p *probe) settings() error {
	r, err := p.get("/api/settings")
	if err != nil {
		return err
	}
	s := r["settings"].(map[string]any)
	s["allowed_senders"] = "127.0.0.1"
	data, _ := json.Marshal(s)
	request, _ := http.NewRequest("POST", p.base+"/api/settings", strings.NewReader(string(data)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", p.csrf)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 {
		return errors.New("settings update failed")
	}
	return nil
}
func (p *probe) status() (map[string]any, error) {
	r, err := p.get("/api/summary?hours=1")
	if err != nil {
		return nil, err
	}
	return r["status"].(map[string]any), nil
}

type resource struct {
	CPU float64
	RSS int64
}

func processResource(executable string) func() *resource {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		data, err := os.ReadFile(filepath.Join(root, "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(data), "\x00")
		name := filepath.Base(args[0])
		if name != executable {
			continue
		}
		return func() *resource {
			stat, err := os.ReadFile(filepath.Join(root, "stat"))
			if err != nil {
				return nil
			}
			at := strings.LastIndexByte(string(stat), ')')
			fields := strings.Fields(string(stat[at+1:]))
			if len(fields) < 13 {
				return nil
			}
			u, _ := strconv.ParseFloat(fields[11], 64)
			s, _ := strconv.ParseFloat(fields[12], 64)
			status, err := os.ReadFile(filepath.Join(root, "status"))
			if err != nil {
				return nil
			}
			m := regexp.MustCompile(`VmRSS:\s+(\d+)`).FindSubmatch(status)
			if m == nil {
				return nil
			}
			rss, _ := strconv.ParseInt(string(m[1]), 10, 64)
			return &resource{CPU: (u + s) / 100, RSS: rss * 1024}
		}
	}
	return func() *resource { return nil }
}

func run() error {
	base := flag.String("url", "http://127.0.0.1:8080", "Isolated dashboard URL")
	udp := flag.String("udp", "127.0.0.1:5514", "Isolated UDP destination")
	seconds := flag.Int("seconds", 15, "Duration, 1–60 seconds")
	rate := flag.Int("rate", 100, "Rate, 1–1000 records/s")
	executable := flag.String("process", "ros-watch", "Executable basename for Linux process resource sampling")
	flag.Parse()
	if *seconds < 1 || *seconds > 60 || *rate < 1 || *rate > 1000 {
		return errors.New("invalid finite probe bounds")
	}
	jar, _ := cookiejar.New(nil)
	p := probe{client: &http.Client{Jar: jar, Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil}}, base: *base}
	if err := p.setup(); err != nil {
		return err
	}
	if err := p.settings(); err != nil {
		return err
	}
	initial, err := p.status()
	if err != nil {
		return err
	}
	reader := processResource(*executable)
	first := reader()
	var peak int64
	if first != nil {
		peak = first.RSS
	}
	conn, err := net.Dial("udp4", *udp)
	if err != nil {
		return err
	}
	defer conn.Close()
	started := time.Now()
	var queryMax time.Duration
	for i := 0; i < (*seconds)*(*rate); i++ {
		message := fmt.Sprintf("RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 198.51.100.%d:%d->192.0.2.1:%d, len 60", i%20+1, 40000+i%20000, 1000+i%64)
		if _, err = conn.Write([]byte(message)); err != nil {
			return err
		}
		if i%(*rate*2) == 0 {
			tick := time.Now()
			if _, err = p.status(); err != nil {
				return err
			}
			queryMax = max(queryMax, time.Since(tick))
			if r := reader(); r != nil {
				peak = max(peak, r.RSS)
			}
		}
		time.Sleep(time.Until(started.Add(time.Duration(i+1) * time.Second / time.Duration(*rate))))
	}
	initialCounts := initial["counters"].(map[string]any)
	var final map[string]any
	deadline := time.Now().Add(4 * time.Second)
	sent := *seconds * *rate
	for time.Now().Before(deadline) {
		final, err = p.status()
		if err != nil {
			return err
		}
		counts := final["counters"].(map[string]any)
		if numeric(counts["written"])-numeric(initialCounts["written"]) >= float64(sent) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	elapsed := time.Since(started).Seconds()
	last := reader()
	counts := final["counters"].(map[string]any)
	delta := map[string]float64{}
	for k, v := range counts {
		delta[k] = numeric(v) - numeric(initialCounts[k])
	}
	report := map[string]any{"sent": sent, "duration_seconds": elapsed, "counters_delta": delta, "summary_max_ms": float64(queryMax) / float64(time.Millisecond), "db_bytes": final["db_bytes"]}
	if first != nil && last != nil {
		report["peak_rss_mb"] = float64(peak) / 1048576
		report["cpu_percent_one_core"] = (last.CPU - first.CPU) / elapsed * 100
	}
	if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return err
	}
	if delta["written"] != float64(sent) {
		return errors.New("not all synthetic messages reached storage")
	}
	return nil
}
func numeric(v any) float64 { n, _ := v.(float64); return n }
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Probe failed:", err)
		os.Exit(1)
	}
}
