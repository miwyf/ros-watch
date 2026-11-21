package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	aiGreyEndpoint  = "https://api.greynoise.io/v3/community/"
	aiAbuseEndpoint = "https://api.abuseipdb.com/api/v2/check"
	aiDropEndpoint  = "https://www.spamhaus.org/drop/"
	aiRIPEEndpoint  = "https://stat.ripe.net/data/network-info/data.json"
)

type aiHTTPError struct{ status int }

func (e aiHTTPError) Error() string { return "HTTP " + strconv.Itoa(e.status) }

func (s *aiService) readHTTP(ctx context.Context, method, endpoint string, body io.Reader, headers map[string]string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("无法构造 API 请求")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("API 连接失败或超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, aiHTTPError{resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return nil, errors.New("API 响应过大或读取失败")
	}
	return raw, nil
}

func (s *aiService) getIntel(ctx context.Context, path string, headers map[string]string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.readHTTP(ctx, http.MethodGet, path, nil, headers, 1<<20)
}

func aiIntelStatus(err error) string {
	var h aiHTTPError
	if errors.As(err, &h) {
		if h.status == http.StatusTooManyRequests {
			return "rate_limited"
		}
		if h.status == http.StatusUnauthorized || h.status == http.StatusForbidden {
			return "authentication_failed"
		}
	}
	return "unavailable"
}

type aiIntelValue struct {
	Service   string `json:"service"`
	Status    string `json:"status"`
	CheckedAt string `json:"checked_at"`
	Result    any    `json:"result,omitempty"`
}

func (s *aiService) greyNoise(ctx context.Context, ip string, settings aiReputationSettings) aiIntelValue {
	result := aiIntelValue{Service: "GreyNoise", Status: "disabled", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if !settings.GreyNoise {
		return result
	}
	headers := map[string]string{}
	if settings.GreyKey != "" {
		headers["key"] = settings.GreyKey
	}
	raw, err := s.getIntel(ctx, aiGreyEndpoint+url.PathEscape(ip), headers)
	var status aiHTTPError
	if errors.As(err, &status) && status.status == http.StatusNotFound {
		result.Status = "no_record"
		return result
	}
	if err != nil {
		result.Status = aiIntelStatus(err)
		return result
	}
	var data struct {
		IP             string `json:"ip"`
		Noise          *bool  `json:"noise"`
		Classification string `json:"classification"`
		LastSeen       string `json:"last_seen"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Noise == nil || data.IP != ip {
		result.Status = "invalid_response"
		return result
	}
	result.Status = "queried"
	result.Result = map[string]any{"observed_scanning": *data.Noise, "classification": truncateRunes(data.Classification, 24), "last_seen": truncateRunes(data.LastSeen, 24)}
	return result
}

func (s *aiService) abuseIPDB(ctx context.Context, ip string, settings aiReputationSettings) aiIntelValue {
	result := aiIntelValue{Service: "AbuseIPDB", Status: "disabled", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if !settings.AbuseIPDB {
		return result
	}
	q := url.Values{"ipAddress": {ip}, "maxAgeInDays": {"90"}}
	raw, err := s.getIntel(ctx, aiAbuseEndpoint+"?"+q.Encode(), map[string]string{"Key": settings.AbuseKey})
	if err != nil {
		result.Status = aiIntelStatus(err)
		return result
	}
	var response struct {
		Data struct {
			IP      string `json:"ipAddress"`
			Score   int    `json:"abuseConfidenceScore"`
			Reports int    `json:"totalReports"`
			Last    string `json:"lastReportedAt"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Data.IP != ip {
		result.Status = "invalid_response"
		return result
	}
	result.Status = "queried"
	result.Result = map[string]any{"lookback_days": 90, "reported_count": response.Data.Reports, "confidence": response.Data.Score, "last_report": truncateRunes(response.Data.Last, 32)}
	return result
}

type aiDropRow struct {
	CIDR string `json:"cidr"`
	SBL  string `json:"sblid"`
	ASN  uint32 `json:"asn"`
}

type aiDropFeed struct {
	rows    []aiDropRow
	checked time.Time
}

func (s *aiService) dropFeed(ctx context.Context, file string) (aiDropFeed, error) {
	s.feedMu.Lock()
	cached := s.feeds[file]
	s.feedMu.Unlock()
	if !cached.checked.IsZero() && time.Since(cached.checked) < 6*time.Hour {
		return cached, nil
	}
	body, err := s.getIntel(ctx, aiDropEndpoint+file, nil)
	if err != nil {
		return aiDropFeed{}, err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	var data aiDropFeed
	for {
		var row aiDropRow
		err = d.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return aiDropFeed{}, errors.New("Spamhaus 名单格式无效")
		}
		if row.CIDR != "" || row.ASN != 0 {
			data.rows = append(data.rows, row)
		}
	}
	if len(data.rows) == 0 {
		return aiDropFeed{}, errors.New("Spamhaus 名单为空")
	}
	data.checked = time.Now().UTC()
	s.feedMu.Lock()
	s.feeds[file] = data
	s.feedMu.Unlock()
	return data, nil
}

func (s *aiService) ripeASNs(ctx context.Context, ip string) ([]uint32, error) {
	params := url.Values{"resource": {ip}}
	raw, err := s.getIntel(ctx, aiRIPEEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			ASNs []json.RawMessage `json:"asns"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, errors.New("RIPEstat 数据格式无效")
	}
	asns := []uint32{}
	for _, raw := range response.Data.ASNs {
		value, err := strconv.ParseUint(strings.Trim(string(raw), `"`), 10, 32)
		if err == nil && value != 0 {
			asns = append(asns, uint32(value))
		}
	}
	return asns, nil
}

func (s *aiService) spamhaus(ctx context.Context, ip netip.Addr, allow bool) aiIntelValue {
	value := aiIntelValue{Service: "Spamhaus DROP", Status: "disabled", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if !allow {
		return value
	}
	file := "drop_v4.json"
	if ip.Is6() {
		file = "drop_v6.json"
	}
	var ipFeed, asnFeed aiDropFeed
	var ipErr, asnErr, routeErr error
	var asns []uint32
	var work sync.WaitGroup
	work.Go(func() { ipFeed, ipErr = s.dropFeed(ctx, file) })
	work.Go(func() { asnFeed, asnErr = s.dropFeed(ctx, "asndrop.json") })
	work.Go(func() { asns, routeErr = s.ripeASNs(ctx, ip.String()) })
	work.Wait()
	matches := []aiDropRow{}
	for _, row := range ipFeed.rows {
		prefix, err := netip.ParsePrefix(row.CIDR)
		if err == nil && prefix.Contains(ip) {
			matches = append(matches, row)
		}
	}
	asnMatches := []uint32{}
	for _, row := range asnFeed.rows {
		for _, asn := range asns {
			if asn == row.ASN {
				asnMatches = append(asnMatches, asn)
			}
		}
	}
	ipStatus, asnStatus := "queried", "queried"
	if ipErr != nil {
		ipStatus = aiIntelStatus(ipErr)
	}
	if asnErr != nil {
		asnStatus = aiIntelStatus(asnErr)
	} else if routeErr != nil {
		asnStatus = aiIntelStatus(routeErr)
	} else if len(asns) == 0 {
		asnStatus = "no_route"
	}
	value.Status = "queried"
	if ipStatus != "queried" || asnStatus != "queried" {
		value.Status = "partial"
	}
	value.Result = map[string]any{"prefix_matches": matches, "asn_matches": asnMatches, "announcing_asns": asns,
		"prefix_feed_status": ipStatus, "asn_feed_status": asnStatus, "prefix_checked_at": ipFeed.checked, "asn_checked_at": asnFeed.checked}
	return value
}

func (s *aiService) ipReputation(ctx context.Context, args aiArguments, settings aiReputationSettings) (any, []aiEvidence, error) {
	if args.Source != "" {
		args.Sources = append(args.Sources, args.Source)
	}
	if len(args.Sources) < 1 || len(args.Sources) > 8 {
		return nil, nil, errors.New("每次查询 1 至 8 个公网来源 IP")
	}
	ips := []netip.Addr{}
	seen := map[netip.Addr]bool{}
	for _, raw := range args.Sources {
		ip, err := aiAddress(raw)
		if err != nil || !aiPublicIP(ip) {
			return nil, nil, errors.New("情报查询只接受有效的公网 IP")
		}
		if !seen[ip] {
			ips = append(ips, ip)
			seen[ip] = true
		}
	}
	type row struct {
		IP        string         `json:"ip"`
		Providers []aiIntelValue `json:"providers"`
	}
	result := make([]row, len(ips))
	ev := []aiEvidence{}
	providers := 3
	if settings.Custom.Enabled {
		providers++
	}
	for i, ip := range ips {
		source := ip.String()
		result[i] = row{IP: source, Providers: make([]aiIntelValue, providers)}
		if settings.GreyNoise {
			ev = append(ev, aiEvidence{"GreyNoise · " + source, "https://viz.greynoise.io/ip/" + url.PathEscape(source)})
		}
		if settings.AbuseIPDB {
			ev = append(ev, aiEvidence{"AbuseIPDB · " + source, "https://www.abuseipdb.com/check/" + url.PathEscape(source)})
		}
	}
	if settings.Spamhaus {
		ev = append(ev, aiEvidence{"Spamhaus DROP 官方名单", "https://www.spamhaus.org/blocklists/do-not-route-or-peer/"})
	}
	var work sync.WaitGroup
	// Providers run independently. Keep each provider's requests sequential.
	work.Go(func() {
		for i, ip := range ips {
			result[i].Providers[0] = s.greyNoise(ctx, ip.String(), settings)
		}
	})
	work.Go(func() {
		for i, ip := range ips {
			result[i].Providers[1] = s.abuseIPDB(ctx, ip.String(), settings)
		}
	})
	work.Go(func() {
		for i, ip := range ips {
			result[i].Providers[2] = s.spamhaus(ctx, ip, settings.Spamhaus)
		}
	})
	if settings.Custom.Enabled {
		work.Go(func() {
			for i, ip := range ips {
				result[i].Providers[3] = s.customIntel(ctx, ip, settings.Custom)
			}
		})
	}
	work.Wait()
	return result, ev, nil
}
