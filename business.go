package main

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

const businessSchema = `CREATE TABLE IF NOT EXISTS business_rules(
 id INTEGER PRIMARY KEY,name TEXT NOT NULL,sender TEXT NOT NULL,src TEXT NOT NULL,dst TEXT NOT NULL,
 proto TEXT NOT NULL,dport INTEGER NOT NULL,scope TEXT NOT NULL,interface TEXT NOT NULL,
 UNIQUE(sender,src,dst,proto,dport,scope,interface));`

const businessMatchSQL = `b.sender=events.sender AND b.src=events.src AND b.dst=events.dst AND b.proto=events.proto AND b.dport=events.dport AND b.scope=events.scope AND b.interface=events.interface`
const businessExistsSQL = `EXISTS(SELECT 1 FROM business_rules b WHERE ` + businessMatchSQL + `)`
const businessNameSQL = `COALESCE((SELECT name FROM business_rules b WHERE ` + businessMatchSQL + `),'')`

var errBusinessLimit = errors.New("最多保留 100 条正常业务标记，请先取消不再使用的标记")

type BusinessRule struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Sender    string `json:"sender"`
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Proto     string `json:"proto"`
	Dport     int    `json:"dport"`
	Scope     string `json:"scope"`
	Interface string `json:"interface"`
	Remove    bool   `json:"remove,omitempty"`
}

func (r BusinessRule) matches(e Event) bool {
	return e.Dport != nil && r.Sender == e.Sender && r.Src == e.Src && r.Dst == e.Dst && r.Proto == e.Proto && r.Dport == *e.Dport && r.Scope == e.Scope && r.Interface == e.Interface
}

func (r *BusinessRule) validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if !validLabel(r.Name) || !validLabel(r.Interface) || (r.Scope != "router" && r.Scope != "forward") || (r.Proto != "TCP" && r.Proto != "UDP") || r.Dport < 1 || r.Dport > 65535 {
		return errors.New("请填写业务名称及准确的设备、来源、目标、协议和端口")
	}
	for _, value := range []*string{&r.Sender, &r.Src, &r.Dst} {
		ip, err := netip.ParseAddr(strings.TrimSpace(*value))
		if err != nil {
			return errors.New("业务标记须使用准确的来源、目标和日志设备 IP")
		}
		*value = ip.String()
	}
	return nil
}

func (s *Store) businessRules(ctx context.Context) ([]BusinessRule, error) {
	rows, err := s.reader.QueryContext(ctx, "SELECT id,name,sender,src,dst,proto,dport,scope,interface FROM business_rules ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]BusinessRule, 0)
	for rows.Next() {
		var r BusinessRule
		if err = rows.Scan(&r.ID, &r.Name, &r.Sender, &r.Src, &r.Dst, &r.Proto, &r.Dport, &r.Scope, &r.Interface); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) saveBusiness(ctx context.Context, r BusinessRule) (result error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if result == nil {
			s.businessRevision.Add(1)
		}
	}()
	if r.Remove {
		_, err := s.writer.ExecContext(ctx, "DELETE FROM business_rules WHERE id=?", r.ID)
		return err
	}
	var count int
	if err := s.writer.QueryRowContext(ctx, "SELECT COUNT(*) FROM business_rules").Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		var present int
		if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM business_rules WHERE sender=? AND src=? AND dst=? AND proto=? AND dport=? AND scope=? AND interface=?`, r.Sender, r.Src, r.Dst, r.Proto, r.Dport, r.Scope, r.Interface).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			return errBusinessLimit
		}
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO business_rules(name,sender,src,dst,proto,dport,scope,interface) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(sender,src,dst,proto,dport,scope,interface) DO UPDATE SET name=excluded.name`, r.Name, r.Sender, r.Src, r.Dst, r.Proto, r.Dport, r.Scope, r.Interface)
	return err
}

func (a *App) businessRules(w http.ResponseWriter, r *http.Request) {
	_, session, ok := a.authorized(w, r)
	if !ok {
		return
	}
	ctx, cancel := boundedContext(r.Context())
	defer cancel()
	if r.Method == http.MethodPost {
		if !validCSRF(r, session) {
			apiError(w, 400, "页面验证已过期，请刷新后重试")
			return
		}
		var rule BusinessRule
		if err := readJSON(r, &rule); err != nil {
			apiError(w, 400, err.Error())
			return
		}
		if rule.Remove {
			if rule.ID <= 0 {
				apiError(w, 400, "业务标记无效")
				return
			}
		} else if err := rule.validate(); err != nil {
			apiError(w, 400, err.Error())
			return
		}
		if err := a.store.saveBusiness(ctx, rule); err != nil {
			if errors.Is(err, errBusinessLimit) {
				apiError(w, 400, err.Error())
			} else {
				apiError(w, 503, "业务标记保存失败")
			}
			return
		}
		a.queryMu.Lock()
		clear(a.cache)
		a.queryMu.Unlock()
	}
	rules, err := a.store.businessRules(ctx)
	if err != nil {
		queryError(w)
		return
	}
	jsonResponse(w, 200, map[string]any{"rules": rules})
}
