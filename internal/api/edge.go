package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/edge"
	"github.com/turin-dev/aven/internal/store"
)

// 인스턴스(서비스 × 환경)마다 저장하고, 바꾸면 재배포 없이 모든 노드의 keel-proxy에 바로 반영한다.

type edgeView struct {
	edge.Settings
	// Available은 공개 도메인(리다이렉트가 아닌)이 있는지다. 엣지 기능은 공개 도메인으로 들어오는 요청에만 적용된다.
	Available         bool     `json:"available"`
	Domains           []string `json:"domains"`
	UnderAttackActive bool     `json:"under_attack_active"`
}

func edgeDomains(in *store.Instance) []string {
	out := []string{}
	for _, d := range in.Domains {
		if d.Redirect == nil {
			out = append(out, d.Host)
		}
	}
	return out
}

func (s *Server) edgeView(in *store.Instance, settings edge.Settings) edgeView {
	settings.WAF.ClearanceSecret = ""
	doms := edgeDomains(in)
	return edgeView{Settings: settings, Available: len(doms) > 0, Domains: doms, UnderAttackActive: settings.WAF.UnderAttackActive(time.Now())}
}

func (s *Server) getEdgeSettings(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	settings, err := s.Store.GetEdgeSettings(r.Context(), c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.edgeView(c.in, settings))
}

// mergeJSON은 cur 위에 patch의 키만 덮는다(patchInstance와 같은 방식).
func mergeJSON(cur any, patch json.RawMessage, out any) error {
	b, _ := json.Marshal(cur)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	var p map[string]json.RawMessage
	if err := json.Unmarshal(patch, &p); err != nil {
		return err
	}
	for k, v := range p {
		m[k] = v
	}
	b, _ = json.Marshal(m)
	return json.Unmarshal(b, out)
}

// patchEdgeSettings는 cdn / waf의 바꾼 키만 반영한다. purge 세대와 Under Attack 상태는 전용 엔드포인트로만 바뀐다.
func (s *Server) patchEdgeSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		CDN json.RawMessage `json:"cdn"`
		WAF json.RawMessage `json:"waf"`
	}
	if !decode(w, r, &body) {
		return
	}
	old, err := s.Store.GetEdgeSettings(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	next := old
	if len(body.CDN) > 0 && string(body.CDN) != "null" {
		next.CDN = edge.CDN{}
		if err := mergeJSON(old.CDN, body.CDN, &next.CDN); err != nil {
			writeErr(w, 400, "invalid cdn: "+err.Error())
			return
		}
		next.CDN.HTMLGeneration, next.CDN.AllGeneration = old.CDN.HTMLGeneration, old.CDN.AllGeneration
		next.CDN.LastPurgedHTML, next.CDN.LastPurgedAll, next.CDN.LastDeployPurged = old.CDN.LastPurgedHTML, old.CDN.LastPurgedAll, old.CDN.LastDeployPurged
	}
	if len(body.WAF) > 0 && string(body.WAF) != "null" {
		next.WAF = edge.WAF{}
		if err := mergeJSON(old.WAF, body.WAF, &next.WAF); err != nil {
			writeErr(w, 400, "invalid waf: "+err.Error())
			return
		}
		next.WAF.UnderAttack, next.WAF.ClearanceSecret = old.WAF.UnderAttack, old.WAF.ClearanceSecret
		if next.WAF.AllowIPs, err = edge.CanonicalPrefixes(next.WAF.AllowIPs); err != nil {
			writeErr(w, 400, "waf.allow_ips: "+err.Error())
			return
		}
		if next.WAF.DenyIPs, err = edge.CanonicalPrefixes(next.WAF.DenyIPs); err != nil {
			writeErr(w, 400, "waf.deny_ips: "+err.Error())
			return
		}
	}
	next.Normalize()
	if err := next.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if next.CDN.Enabled && !old.CDN.Enabled && len(edgeDomains(c.in)) == 0 {
		writeErr(w, 400, "CDN caching requires a public domain: add a generated or custom domain first")
		return
	}
	s.saveEdge(w, r, c, next, describeEdgeChange(old, next))
}

func describeEdgeChange(old, next edge.Settings) string {
	switch {
	case next.CDN.Enabled && !old.CDN.Enabled:
		return "CDN 캐시를 켰습니다"
	case !next.CDN.Enabled && old.CDN.Enabled:
		return "CDN 캐시를 껐습니다"
	}
	return "엣지 설정을 변경했습니다"
}

func (s *Server) putEdgeRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var rs edge.Ruleset
	if !decode(w, r, &rs) {
		return
	}
	old, err := s.Store.GetEdgeSettings(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	rs.Normalize(func() string { return store.NewID("rul") })
	next := old
	next.Rules = rs
	if err := next.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	for _, rule := range rs.Rules {
		if rule.IsEnabled() && rule.Then.Action == edge.ActCacheOverride && !next.CDN.Enabled {
			writeErr(w, 400, fmt.Sprintf("rule %q: cache_override needs CDN caching to be enabled", rule.Description))
			return
		}
	}
	if len(rs.Rules) > 0 && len(edgeDomains(c.in)) == 0 {
		writeErr(w, 400, "edge rules require a public domain: add a generated or custom domain first")
		return
	}
	s.saveEdge(w, r, c, next, fmt.Sprintf("엣지 규칙 %d개를 저장했습니다", len(rs.Rules)))
}

func (s *Server) purgeEdgeCache(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		Scope string `json:"scope"`
	}
	if !decode(w, r, &body) {
		return
	}
	settings, err := s.Store.GetEdgeSettings(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	now := time.Now().UTC()
	switch body.Scope {
	case "html":
		settings.CDN.HTMLGeneration++
		settings.CDN.LastPurgedHTML = &now
	case "all":
		settings.CDN.HTMLGeneration++
		settings.CDN.AllGeneration++
		settings.CDN.LastPurgedHTML, settings.CDN.LastPurgedAll = &now, &now
	default:
		writeErr(w, 400, "scope must be html or all")
		return
	}
	msg := "CDN 캐시(HTML)를 비웠습니다"
	if body.Scope == "all" {
		msg = "CDN 캐시 전체를 비웠습니다"
	}
	s.saveEdge(w, r, c, settings, msg)
}

var underAttackDurations = map[string]time.Duration{"1h": time.Hour, "3h": 3 * time.Hour, "12h": 12 * time.Hour, "24h": 24 * time.Hour}

func (s *Server) enableUnderAttack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		Duration string `json:"duration"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(edgeDomains(c.in)) == 0 {
		writeErr(w, 400, "Under Attack Mode requires a public domain: add a generated or custom domain first")
		return
	}
	now := time.Now().UTC()
	ua := &edge.UnderAttack{Since: now}
	switch d := strings.TrimSpace(strings.ToLower(body.Duration)); d {
	case "", "forever":
	default:
		dur, ok := underAttackDurations[d]
		if !ok {
			writeErr(w, 400, "duration must be forever, 1h, 3h, 12h or 24h")
			return
		}
		until := now.Add(dur)
		ua.Until = &until
	}
	settings, err := s.Store.GetEdgeSettings(ctx, c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if settings.WAF.UnderAttackActive(now) {
		ua.Since = settings.WAF.UnderAttack.Since // 기간만 바꾼다
	}
	settings.WAF.UnderAttack = ua
	msg := "Under Attack Mode를 켰습니다(끌 때까지)"
	if ua.Until != nil {
		msg = fmt.Sprintf("Under Attack Mode를 %s 동안 켰습니다", body.Duration)
	}
	s.saveEdge(w, r, c, settings, msg)
}

func (s *Server) disableUnderAttack(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	settings, err := s.Store.GetEdgeSettings(r.Context(), c.in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	settings.WAF.UnderAttack = nil
	s.saveEdge(w, r, c, settings, "Under Attack Mode를 껐습니다")
}

// saveEdge는 설정을 저장하고, 이벤트를 남기고, 모든 노드의 proxy에 반영한다.
func (s *Server) saveEdge(w http.ResponseWriter, r *http.Request, c *instCtx, settings edge.Settings, msg string) {
	ctx := r.Context()
	if settings.WAF.ClearanceSecret == "" {
		settings.WAF.ClearanceSecret = store.RandomSecret(40)
	}
	if err := s.Store.SaveEdgeSettings(ctx, c.in.ID, settings); err != nil {
		s.storeErr(w, err)
		return
	}
	if s.Events != nil {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "service.updated", fmt.Sprintf("%s (%s): %s", c.svc.Name, c.env.Name, msg))
	}
	s.syncEdgeProxy(ctx)
	writeJSON(w, http.StatusOK, s.edgeView(c.in, settings))
}

func (s *Server) syncEdgeProxy(ctx context.Context) {
	if s.Deploy == nil {
		return
	}
	if err := s.Deploy.SyncProxy(context.WithoutCancel(ctx)); err != nil && s.Log != nil {
		s.Log.Warn("proxy sync", "err", err)
	}
}
