package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/cloudflare"
	"github.com/turin-dev/aven/internal/deploy"
)

// Cloudflare 연동(owner 전용). 토큰은 마스터 키로 암호화해 저장하고 API로 내보내지 않는다(has_token, token_hint만).

type cloudflareView struct {
	Enabled   bool                    `json:"enabled"`
	HasToken  bool                    `json:"has_token"`
	TokenHint string                  `json:"token_hint"` // 토큰 끝 4자
	Mode      string                  `json:"mode"`
	Proxied   bool                    `json:"proxied"`
	Wildcards bool                    `json:"wildcards"`
	Zones     []string                `json:"zones"`
	Status    deploy.CloudflareStatus `json:"status"`
}

func tokenHint(t string) string {
	if len(t) < 8 {
		return ""
	}
	return "…" + t[len(t)-4:]
}

func (s *Server) cloudflareView(ctx context.Context) (cloudflareView, error) {
	cfg, err := s.Store.CloudflareSettings(ctx)
	if err != nil {
		return cloudflareView{}, err
	}
	v := cloudflareView{Enabled: cfg.Enabled, HasToken: cfg.Token != "", TokenHint: tokenHint(cfg.Token), Mode: cfg.Mode, Proxied: cfg.Proxied, Wildcards: cfg.Wildcards, Zones: cfg.Zones}
	if s.Deploy != nil {
		v.Status = s.Deploy.CloudflareStatus()
	}
	if v.Status.Zones == nil {
		v.Status.Zones, v.Status.Hosts, v.Status.Pools = []deploy.CloudflareZone{}, []deploy.CloudflareHost{}, []deploy.CloudflarePool{}
	}
	return v, nil
}

func (s *Server) getCloudflare(w http.ResponseWriter, r *http.Request) {
	v, err := s.cloudflareView(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// cleanZones는 존 이름 목록을 검사한다(소문자, 중복 제거).
func cleanZones(in []string) ([]string, bool) {
	out := []string{}
	for _, z := range in {
		z = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(z), "."))
		if z == "" {
			continue
		}
		if !domainRe.MatchString(z) || strings.HasPrefix(z, "*.") || !strings.Contains(z, ".") {
			return nil, false
		}
		if !slices.Contains(out, z) {
			out = append(out, z)
		}
	}
	return out, len(out) <= 100
}

func (s *Server) putCloudflare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled   *bool     `json:"enabled"`
		Token     *string   `json:"token"` // 없으면 저장된 토큰을 유지, ""이면 지운다
		Mode      *string   `json:"mode"`
		Proxied   *bool     `json:"proxied"`
		Wildcards *bool     `json:"wildcards"`
		Zones     *[]string `json:"zones"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	cfg, err := s.Store.CloudflareSettings(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var changes []string
	if body.Token != nil {
		t := strings.TrimSpace(*body.Token)
		if len(t) > 200 || strings.ContainsAny(t, " \t\r\n") {
			writeErr(w, 400, "the API token looks invalid")
			return
		}
		if t != "" {
			if err := s.Deploy.CloudflareClient(t).VerifyToken(ctx); err != nil {
				writeErr(w, 400, "Cloudflare rejected the token: "+err.Error())
				return
			}
		}
		cfg.Token = t
		changes = append(changes, "token")
	}
	if body.Mode != nil {
		switch *body.Mode {
		case "auto", "load_balancer", "dns":
			cfg.Mode = *body.Mode
		default:
			writeErr(w, 400, "mode must be auto, load_balancer or dns")
			return
		}
		changes = append(changes, "mode="+cfg.Mode)
	}
	if body.Proxied != nil {
		cfg.Proxied = *body.Proxied
		changes = append(changes, "proxied="+yesNo(cfg.Proxied))
	}
	if body.Wildcards != nil {
		cfg.Wildcards = *body.Wildcards
		changes = append(changes, "wildcards="+yesNo(cfg.Wildcards))
	}
	if body.Zones != nil {
		z, ok := cleanZones(*body.Zones)
		if !ok {
			writeErr(w, 400, "zones must be domain names (at most 100)")
			return
		}
		cfg.Zones = z
		changes = append(changes, "zones="+strings.Join(z, ","))
	}
	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
		changes = append(changes, "enabled="+yesNo(cfg.Enabled))
	}
	if cfg.Enabled && cfg.Token == "" {
		writeErr(w, 400, "save an API token before turning the integration on")
		return
	}
	if cfg.Enabled && len(cfg.Zones) == 0 {
		writeErr(w, 400, "choose at least one zone to manage")
		return
	}
	if err := s.Store.SetCloudflareSettings(ctx, cfg); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "owner.cloudflare", "instance", "Cloudflare 연동 설정을 바꿨습니다: "+strings.Join(changes, ", "))
	s.Deploy.SyncCloudflareAsync()
	v, err := s.cloudflareView(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type cloudflareTestResult struct {
	OK            bool                    `json:"ok"`
	Error         string                  `json:"error,omitempty"`
	Zones         []deploy.CloudflareZone `json:"zones"`
	LoadBalancing string                  `json:"load_balancing"` // available | unavailable
	LBError       string                  `json:"load_balancing_error,omitempty"`
}

// testCloudflare는 토큰(본문에 있으면 그 토큰, 없으면 저장된 토큰)으로 존 목록과 Load Balancing 사용 여부를 확인한다. 아무것도 바꾸지 않는다.
func (s *Server) testCloudflare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	token := strings.TrimSpace(body.Token)
	if token == "" {
		cfg, err := s.Store.CloudflareSettings(ctx)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		token = cfg.Token
	}
	res := cloudflareTestResult{Zones: []deploy.CloudflareZone{}}
	if token == "" {
		res.Error = "no API token"
		writeJSON(w, http.StatusOK, res)
		return
	}
	c := s.Deploy.CloudflareClient(token)
	if err := c.VerifyToken(ctx); err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.OK = true
	res.LoadBalancing = "unavailable"
	checked := map[string]bool{}
	for _, z := range zones {
		res.Zones = append(res.Zones, deploy.CloudflareZone{ID: z.ID, Name: z.Name, Account: z.Account.Name, Plan: z.Plan.Name})
		if checked[z.Account.ID] || res.LoadBalancing == "available" {
			continue
		}
		checked[z.Account.ID] = true
		if _, err := c.Pools(ctx, z.Account.ID); err == nil {
			res.LoadBalancing, res.LBError = "available", ""
		} else if res.LBError == "" {
			res.LBError = err.Error()
			if cloudflare.Forbidden(err) {
				res.LBError = "Load Balancing is not available for this account or token (subscription or Load Balancing permissions missing): " + err.Error()
			}
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// syncCloudflareNow는 지금 동기화하고 결과를 돌려준다.
func (s *Server) syncCloudflareNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.Deploy.SyncCloudflare(ctx); err != nil && ctx.Err() != nil {
		writeErr(w, 504, err.Error())
		return
	}
	v, err := s.cloudflareView(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// cleanupCloudflare는 이 설치가 Cloudflare에 만든 것을 모두 지운다. 연동이 켜져 있으면 409(다음 동기화가 다시 만든다).
func (s *Server) cleanupCloudflare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := s.Store.CloudflareSettings(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if cfg.Enabled {
		writeErr(w, 409, "turn the integration off first; the next sync would create everything again")
		return
	}
	n, err := s.Deploy.RemoveCloudflareResources(ctx)
	s.audit(r, "owner.cloudflare", "instance", "Aven이 만든 Cloudflare 레코드와 로드밸런서를 지웠습니다")
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": n})
}
