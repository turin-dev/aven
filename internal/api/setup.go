package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 초기 설정 체크리스트: 새로 설치한 서버의 owner에게 남은 준비 작업을 알려 준다.
// 항목의 완료 여부는 매번 서버 설정과 DB에서 계산한다. 문구는 웹 UI가 id로 고른다.
const settingSetup = "owner.setup"

type setupSetting struct {
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	DismissedBy string     `json:"dismissed_by,omitempty"`
}

type setupItem struct {
	ID       string `json:"id"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional"`
}

func (s *Server) setupItems(r *http.Request) ([]setupItem, error) {
	ctx := r.Context()
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	personalOwner := false
	for _, u := range users {
		if !u.Builtin && !u.Disabled && (u.Role == store.RoleOwner || s.SuperAdmins[store.NormalizeEmail(u.Email)]) {
			personalOwner = true
		}
	}
	mfa := false
	if p := principalFrom(r); p != nil && p.User != nil {
		mfa = p.User.TOTPEnabled
		if !mfa {
			n, err := s.Store.CountPasskeys(ctx, p.User.ID)
			if err != nil {
				return nil, err
			}
			mfa = n > 0
		}
	}
	nodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	remoteNode := false
	for _, n := range nodes {
		if !n.IsLocal {
			remoteNode = true
		}
	}
	dests, err := s.Store.ListBackupDestinations(ctx)
	if err != nil {
		return nil, err
	}
	// 첫 실행 때 빈 "default" 프로젝트가 생기므로 프로젝트가 아니라 서비스(환경별 인스턴스)가 있는지 본다.
	// Aven 프로젝트(self 서비스)는 첫 프로젝트로 치지 않는다.
	all, err := s.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var instances []*store.Instance
	for _, in := range all {
		if s.Deploy == nil || !s.Deploy.IsSelfService(in.ServiceID) {
			instances = append(instances, in)
		}
	}
	return []setupItem{
		{ID: "public_url", Done: strings.HasPrefix(s.PublicURL, "https://")},
		{ID: "base_domain", Done: s.baseDomain() != ""},
		{ID: "owner_account", Done: personalOwner},
		{ID: "mfa", Done: mfa},
		{ID: "github", Done: s.GitHubClientID != "" && s.GitHubClientSecret != "" && s.GitHubSecret != "", Optional: true},
		{ID: "smtp", Done: s.mailer() != nil, Optional: true},
		{ID: "backup_destination", Done: len(dests) > 0, Optional: true},
		{ID: "node", Done: remoteNode, Optional: true},
		{ID: "self_service", Done: s.selfServiceView(ctx).Enabled, Optional: true},
		{ID: "first_project", Done: len(instances) > 0},
	}, nil
}

func (s *Server) baseDomain() string {
	if s.Deploy == nil {
		return ""
	}
	return s.Deploy.Cfg.BaseDomain
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	items, err := s.setupItems(r)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var st setupSetting
	s.loadOwnerSetting(r.Context(), settingSetup, &st)
	required, done := 0, 0
	for _, it := range items {
		if !it.Optional {
			required++
			if it.Done {
				done++
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"items": items, "required": required, "required_done": done, "complete": done == required,
		"dismissed": st.DismissedAt != nil, "dismissed_at": st.DismissedAt,
		"server": map[string]any{
			"public_url": s.PublicURL, "base_domain": s.baseDomain(),
			"github_callback_url": strings.TrimRight(s.PublicURL, "/") + "/api/github/oauth/callback",
			"github_webhook_url":  strings.TrimRight(s.PublicURL, "/") + "/api/webhooks/github",
		},
	})
}

// putSetup은 첫 로그인 안내를 닫거나(dismissed=true) 다시 켠다. 체크리스트 자체는 어드민에서 계속 볼 수 있다.
func (s *Server) putSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dismissed bool `json:"dismissed"`
	}
	if !decode(w, r, &body) {
		return
	}
	var st setupSetting
	msg := "초기 설정 안내를 다시 켰습니다"
	if body.Dismissed {
		now := time.Now().UTC()
		st.DismissedAt = &now
		if p := principalFrom(r); p != nil && p.User != nil {
			st.DismissedBy = p.User.ID
		}
		msg = "초기 설정 안내를 닫았습니다"
	}
	if err := s.saveOwnerSetting(r.Context(), settingSetup, st); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "owner.setup", "instance", msg)
	writeJSON(w, 200, map[string]any{"dismissed": body.Dismissed})
}
