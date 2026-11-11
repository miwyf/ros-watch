package main

import (
	"net/netip"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/service"
)

const geoDataVersion = "v3.18.0 / 2026-09-08"
const geoCacheLimit = 4096

type IPLocation struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Region      string `json:"region"`
	City        string `json:"city"`
	ISP         string `json:"isp"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
}

type GeoLookup struct {
	searcher *service.Ip2Region
	mu       sync.Mutex
	cache    map[string]IPLocation
	order    []string
	next     int
}

func newGeoLookup(dir string) (*GeoLookup, error) {
	v4, err := service.NewV4Config(service.VIndexCache, filepath.Join(dir, "ip2region_v4.xdb"), 2)
	if err != nil {
		return nil, err
	}
	v6, err := service.NewV6Config(service.VIndexCache, filepath.Join(dir, "ip2region_v6.xdb"), 2)
	if err != nil {
		return nil, err
	}
	searcher, err := service.NewIp2Region(v4, v6)
	if err != nil {
		return nil, err
	}
	return &GeoLookup{searcher: searcher, cache: make(map[string]IPLocation), order: make([]string, 0, geoCacheLimit)}, nil
}
func (g *GeoLookup) close() {
	if g != nil {
		g.searcher.Close()
	}
}

var specialGeoRanges = []struct {
	prefix netip.Prefix
	label  string
}{
	{netip.MustParsePrefix("100.64.0.0/10"), "共享地址（CGNAT）"},
	{netip.MustParsePrefix("192.0.2.0/24"), "文档示例地址"},
	{netip.MustParsePrefix("198.51.100.0/24"), "文档示例地址"},
	{netip.MustParsePrefix("203.0.113.0/24"), "文档示例地址"},
	{netip.MustParsePrefix("2001:db8::/32"), "文档示例地址"},
	{netip.MustParsePrefix("198.18.0.0/15"), "网络测试地址"},
	{netip.MustParsePrefix("0.0.0.0/8"), "特殊用途地址"},
	{netip.MustParsePrefix("240.0.0.0/4"), "保留地址"},
}

func (g *GeoLookup) location(ip string) IPLocation {
	unknown := IPLocation{Label: "未知地区", Kind: "unknown"}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return unknown
	}
	addr = addr.Unmap()
	label := ""
	switch {
	case addr.IsUnspecified():
		label = "未指定地址"
	case addr.IsLoopback():
		label = "本机回环"
	case addr.IsPrivate():
		label = "内网地址"
	case addr.IsLinkLocalUnicast():
		label = "链路本地地址"
	case addr.IsMulticast():
		label = "组播地址"
	default:
		for _, item := range specialGeoRanges {
			if item.prefix.Contains(addr) {
				label = item.label
				break
			}
		}
	}
	if label != "" {
		return IPLocation{Label: label, Kind: "special"}
	}
	if g == nil {
		return IPLocation{Label: "归属地库暂不可用", Kind: "unavailable"}
	}
	key := addr.String()
	g.mu.Lock()
	cached, ok := g.cache[key]
	g.mu.Unlock()
	if ok {
		return cached
	}
	region, err := g.searcher.Search(key)
	result := unknown
	if err == nil {
		result = parseGeoLocation(region)
	}
	g.mu.Lock()
	if _, exists := g.cache[key]; !exists {
		if len(g.order) < geoCacheLimit {
			g.order = append(g.order, key)
		} else {
			delete(g.cache, g.order[g.next])
			g.order[g.next] = key
			g.next = (g.next + 1) % geoCacheLimit
		}
		g.cache[key] = result
	}
	g.mu.Unlock()
	return result
}
func parseGeoLocation(value string) IPLocation {
	result := IPLocation{Label: "未知地区", Kind: "unknown"}
	fields := strings.Split(value, "|")
	if len(fields) != 5 {
		return result
	}
	for i, field := range fields {
		field = strings.TrimSpace(field)
		if field == "0" {
			field = ""
		}
		fields[i] = field
	}
	result.Country, result.Region, result.City, result.ISP = fields[0], fields[1], fields[2], fields[3]
	if len(fields[4]) == 2 && fields[4][0] >= 'A' && fields[4][0] <= 'Z' && fields[4][1] >= 'A' && fields[4][1] <= 'Z' {
		result.CountryCode = fields[4]
	}
	switch result.CountryCode {
	case "CN":
		result.Country = "中国"
	case "TW":
		result.Country, result.Region = "中国", "台湾"
	case "HK":
		result.Country, result.Region = "中国", "香港"
	case "MO":
		result.Country, result.Region = "中国", "澳门"
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{result.Country, result.Region, result.City} {
		if part != "" && (len(parts) == 0 || parts[len(parts)-1] != part) {
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 {
		result.Label = strings.Join(parts, " · ")
		result.Kind = "located"
	}
	return result
}
func (a *App) locations(rows ...[]map[string]any) map[string]IPLocation {
	result := make(map[string]IPLocation)
	for _, list := range rows {
		for _, row := range list {
			if src, ok := row["src"].(string); ok {
				if _, exists := result[src]; !exists {
					result[src] = a.geo.location(src)
				}
			}
		}
	}
	return result
}
