package main

import (
	"context"
	"sort"
)

func (s *Store) homeAttention(ctx context.Context, f Filters, scans []map[string]any) ([]map[string]any, []map[string]any, error) {
	items := make([]map[string]any, 0, len(scans)+8)
	for _, scan := range scans {
		items = append(items, map[string]any{"kind": "scan", "src": scan["src"], "sender": scan["sender"], "last": scan["last"], "first": scan["first"], "n": scan["n"], "ports": scan["ports"]})
	}
	failures, err := queryMaps(ctx, s.reader, `SELECT sender,source src,username,MAX(ts) last,MIN(ts) first,COUNT(*) n FROM logs
 WHERE ts>=? AND ts<=? AND category='login' AND event='failure' AND source<>''
 GROUP BY sender,source,username,CAST(ts/300 AS INTEGER) HAVING COUNT(*)>=5 ORDER BY last DESC,sender,src LIMIT 8`, f.Since, f.Until)
	if err != nil {
		return nil, nil, err
	}
	for _, failure := range failures {
		failure["kind"] = "login_failure"
		items = append(items, failure)
	}
	sort.SliceStable(items, func(i, j int) bool { return number(items[i]["last"]) > number(items[j]["last"]) })
	system, err := queryMaps(ctx, s.reader, `SELECT id,ts last,device,category,event,severity,message FROM logs WHERE ts>=? AND ts<=?
 AND (category='config' OR event='reboot' OR severity IN ('warning','error','critical','alert','emergency'))
 AND NOT (category='login' AND event='failure') ORDER BY ts DESC,id DESC LIMIT 8`, f.Since, f.Until)
	return items, system, err
}
