package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// ---- OAuth 앱 관리(작업 공간 admin), 승인된 앱(사용자 본인), 동의 화면 API ----

const (
	maxOAuthRedirectURIs  = 10
	maxOAuthClientSecrets = 10
)

func (s *Server) oauthAppFrom(w http.ResponseWriter, r *http.Request) (*store.OAuthApp, bool) {
	a, err := s.Store.GetOAuthApp(r.Context(), r.PathValue("appid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return a, true
}

func (s *Server) listOAuthApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListOAuthApps(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) createOAuthApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		ClientType   string   `json:"client_type"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if !validName(in.Name) || len(in.Description) > 1000 {
		writeErr(w, http.StatusBadRequest, "앱 이름은 1..100자, 설명은 1000자 이하여야 합니다")
		return
	}
	if in.ClientType != "confidential" && in.ClientType != "public" {
		writeErr(w, http.StatusBadRequest, "client_type은 confidential 또는 public이어야 합니다")
		return
	}
	if err := validateOAuthRedirectURIs(in.RedirectURIs, in.ClientType); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a := &store.OAuthApp{
		ClientID: oauthClientIDPrefix + randomToken(18), CreatedBy: principalFrom(r).User.ID,
		Name: in.Name, Description: in.Description, ClientType: in.ClientType, RedirectURIs: in.RedirectURIs,
	}
	var secret string
	var record *store.OAuthClientSecret
	err := s.Store.Tx(r.Context(), func(tx *store.Store) error {
		if err := tx.CreateOAuthApp(r.Context(), a); err != nil {
			return err
		}
		if a.ClientType == "confidential" {
			var err error
			secret, record, err = issueOAuthClientSecret(r, tx, a)
			return err
		}
		return nil
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.app.created", a.ID, "OAuth 앱 "+a.Name+" 생성")
	out := map[string]any{"app": a, "client_secret": nilIfEmpty(secret)}
	if record != nil {
		out["client_secret_record"] = record
	}
	writeJSON(w, http.StatusCreated, out)
}

// validateOAuthRedirectURIs: 절대 URI, fragment 없음(RFC 6749 3.1.2). HTTPS, loopback HTTP(RFC 8252 7.3),
// public 앱은 사용자 지정 scheme(RFC 8252 7.1)도 허용한다. 와일드카드는 없고 요청과 문자열이 정확히 같아야 한다.
func validateOAuthRedirectURIs(uris []string, clientType string) error {
	if len(uris) == 0 || len(uris) > maxOAuthRedirectURIs {
		return fmt.Errorf("redirect URI는 1..%d개 등록해야 합니다", maxOAuthRedirectURIs)
	}
	seen := make(map[string]bool, len(uris))
	for _, raw := range uris {
		if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, " \t\r\n\x00#*") || seen[raw] {
			return fmt.Errorf("redirect URI가 비어 있거나 중복되었거나 형식이 올바르지 않습니다: %.200q", raw)
		}
		seen[raw] = true
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() || u.User != nil || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("올바르지 않은 redirect URI: %.200s", raw)
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			if u.Hostname() == "" {
				return fmt.Errorf("HTTPS redirect URI에 호스트가 필요합니다: %.200s", raw)
			}
		case "http":
			host := strings.ToLower(u.Hostname())
			if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return fmt.Errorf("HTTP redirect URI는 localhost 또는 loopback 주소만 쓸 수 있습니다: %.200s", raw)
			}
		case "javascript", "data", "file", "vbscript", "blob", "about":
			return fmt.Errorf("허용하지 않는 redirect scheme입니다: %.200s", raw)
		default:
			if clientType != "public" || !validCustomOAuthScheme(u.Scheme) || !strings.Contains(u.Scheme, ".") {
				return fmt.Errorf("사용자 지정 scheme은 네이티브 앱에서 역도메인 형식(com.example.app:/callback)만 쓸 수 있습니다: %.200s", raw)
			}
		}
	}
	return nil
}

func validCustomOAuthScheme(scheme string) bool {
	if scheme == "" || !((scheme[0] >= 'a' && scheme[0] <= 'z') || (scheme[0] >= 'A' && scheme[0] <= 'Z')) {
		return false
	}
	for _, ch := range scheme[1:] {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '+' && ch != '-' && ch != '.' {
			return false
		}
	}
	return true
}

// registeredRedirectURI는 등록된 URI와 정확히(바이트 단위로) 같은지 본다. 정규화하지 않는다.
func registeredRedirectURI(app *store.OAuthApp, uri string) bool {
	match := false
	for _, registered := range app.RedirectURIs {
		if ctEqual(registered, uri) {
			match = true
		}
	}
	return match
}

func (s *Server) getOAuthApp(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.oauthAppFrom(w, r); ok {
		writeJSON(w, http.StatusOK, a)
	}
}

func (s *Server) updateOAuthApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.oauthAppFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Name         *string   `json:"name"`
		Description  *string   `json:"description"`
		RedirectURIs *[]string `json:"redirect_uris"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name != nil {
		if a.Name = strings.TrimSpace(*in.Name); !validName(a.Name) {
			writeErr(w, http.StatusBadRequest, "앱 이름은 1..100자여야 합니다")
			return
		}
	}
	if in.Description != nil {
		if a.Description = strings.TrimSpace(*in.Description); len(a.Description) > 1000 {
			writeErr(w, http.StatusBadRequest, "설명은 1000자 이하여야 합니다")
			return
		}
	}
	if in.RedirectURIs != nil {
		if err := validateOAuthRedirectURIs(*in.RedirectURIs, a.ClientType); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.RedirectURIs = *in.RedirectURIs
	}
	if err := s.Store.UpdateOAuthApp(r.Context(), a); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.app.updated", a.ID, "OAuth 앱 "+a.Name+" 수정")
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteOAuthApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.oauthAppFrom(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteOAuthApp(r.Context(), a.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.app.deleted", a.ID, "OAuth 앱 "+a.Name+" 삭제(발급된 토큰 모두 폐기)")
	w.WriteHeader(http.StatusNoContent)
}

func issueOAuthClientSecret(r *http.Request, st *store.Store, app *store.OAuthApp) (string, *store.OAuthClientSecret, error) {
	id, raw, digest := newOAuthOpaque(oauthClientSecretPrefix, "osec")
	rec := &store.OAuthClientSecret{ID: id, AppID: app.ID, SecretHash: digest}
	if err := st.CreateOAuthClientSecret(r.Context(), rec); err != nil {
		return "", nil, err
	}
	return raw, rec, nil
}

func (s *Server) listOAuthClientSecrets(w http.ResponseWriter, r *http.Request) {
	a, ok := s.oauthAppFrom(w, r)
	if !ok {
		return
	}
	secrets, err := s.Store.ListOAuthClientSecrets(r.Context(), a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, secrets)
}

func (s *Server) createOAuthClientSecret(w http.ResponseWriter, r *http.Request) {
	a, ok := s.oauthAppFrom(w, r)
	if !ok {
		return
	}
	if a.ClientType != "confidential" {
		writeErr(w, http.StatusBadRequest, "네이티브(public) 앱은 client secret을 쓰지 않습니다")
		return
	}
	var raw string
	var rec *store.OAuthClientSecret
	err := s.Store.Tx(r.Context(), func(tx *store.Store) error {
		secrets, err := tx.ListOAuthClientSecrets(r.Context(), a.ID)
		if err != nil {
			return err
		}
		active := 0
		for _, v := range secrets {
			if v.RevokedAt == nil {
				active++
			}
		}
		if active >= maxOAuthClientSecrets {
			return errTooManySecrets
		}
		raw, rec, err = issueOAuthClientSecret(r, tx, a)
		return err
	})
	if errors.Is(err, errTooManySecrets) {
		writeErr(w, http.StatusConflict, fmt.Sprintf("활성 client secret은 최대 %d개까지 만들 수 있습니다", maxOAuthClientSecrets))
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.secret.created", a.ID, "OAuth 앱 "+a.Name+" client secret 생성")
	writeJSON(w, http.StatusCreated, map[string]any{"secret": raw, "record": rec})
}

var errTooManySecrets = errors.New("too many client secrets")

func (s *Server) revokeOAuthClientSecret(w http.ResponseWriter, r *http.Request) {
	a, ok := s.oauthAppFrom(w, r)
	if !ok {
		return
	}
	if err := s.Store.RevokeOAuthClientSecret(r.Context(), a.ID, r.PathValue("secretid")); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.secret.revoked", a.ID, "OAuth 앱 "+a.Name+" client secret 폐기")
	w.WriteHeader(http.StatusNoContent)
}

// ---- 승인된 앱(본인) ----

func (s *Server) listOAuthAuthorizedApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListOAuthAuthorizedApps(r.Context(), principalFrom(r).User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

// patchOAuthGrant는 승인된 앱에 공유한 프로젝트를 바꾼다(project scope가 있는 승인만).
func (s *Server) patchOAuthGrant(w http.ResponseWriter, r *http.Request) {
	u := principalFrom(r).User
	g, err := s.Store.GetUserOAuthGrant(r.Context(), u.ID, r.PathValue("grantid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		ProjectIDs []string `json:"project_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if oauthScopeLevel(g.Scopes, "project:") == lvNone {
		writeErr(w, http.StatusBadRequest, "이 앱은 프로젝트 scope를 승인받지 않았습니다")
		return
	}
	ids, msg := s.validOAuthProjects(r, u, in.ProjectIDs)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.Store.SetOAuthGrantProjects(r.Context(), u.ID, g.ID, ids); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.grant.updated", g.ID, fmt.Sprintf("OAuth 앱에 공유한 프로젝트 변경(%d개)", len(ids)))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeOAuthGrant(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeOAuthGrant(r.Context(), principalFrom(r).User.ID, r.PathValue("grantid")); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.grant.revoked", r.PathValue("grantid"), "OAuth 앱 접근 권한 폐기")
	w.WriteHeader(http.StatusNoContent)
}

// oauthShareableProjects는 사용자가 앱에 공유할 수 있는 프로젝트(볼 수 있는 프로젝트)다.
func (s *Server) oauthShareableProjects(r *http.Request, u *store.User) ([]map[string]string, error) {
	all, err := s.Store.ListProjects(r.Context())
	if err != nil {
		return nil, err
	}
	out := []map[string]string{}
	for _, p := range all {
		lv, err := s.userProjectLevel(r.Context(), u, p.ID)
		if err != nil {
			return nil, err
		}
		if lv > lvNone {
			out = append(out, map[string]string{"id": p.ID, "name": p.Name, "role": levelRole(lv)})
		}
	}
	return out, nil
}

// validOAuthProjects는 선택한 프로젝트가 1개 이상, 중복 없이, 모두 사용자가 볼 수 있는 것인지 본다.
func (s *Server) validOAuthProjects(r *http.Request, u *store.User, ids []string) ([]string, string) {
	if len(ids) == 0 {
		return nil, "프로젝트를 하나 이상 선택하세요"
	}
	projects, err := s.oauthShareableProjects(r, u)
	if err != nil {
		return nil, "프로젝트 목록을 읽지 못했습니다"
	}
	ok := map[string]bool{}
	for _, p := range projects {
		ok[p["id"]] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !ok[id] || slices.Contains(out, id) {
			return nil, "선택할 수 없는 프로젝트가 있습니다"
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, ""
}

// ---- 동의 화면 ----

// consentRequest는 경로의 인가 요청이 세션 사용자의 것인지 확인한다(API 토큰, OAuth 토큰으로는 동의할 수 없다).
func (s *Server) consentRequest(r *http.Request) (*store.OAuthPendingRequest, *store.OAuthApp, string, bool) {
	p := principalFrom(r)
	if p == nil || p.Via != viaSession {
		return nil, nil, "", false
	}
	id, secret, ok := parseOAuthOpaque(r.PathValue("requestid"), oauthRequestPrefix)
	if !ok {
		return nil, nil, "", false
	}
	hash := hashSecret(secret)
	pending, err := s.Store.GetOAuthPendingRequest(r.Context(), id, hash, p.User.ID)
	if err != nil {
		return nil, nil, "", false
	}
	app, err := s.Store.GetOAuthApp(r.Context(), pending.AppID)
	if err != nil {
		return nil, nil, "", false
	}
	return pending, app, hash, true
}

func (s *Server) getOAuthConsentRequest(w http.ResponseWriter, r *http.Request) {
	pending, app, _, ok := s.consentRequest(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "요청이 만료되었거나 찾을 수 없습니다")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	u := principalFrom(r).User
	projects := []map[string]string{}
	if oauthScopeLevel(pending.Scopes, "project:") > lvNone {
		var err error
		if projects, err = s.oauthShareableProjects(r, u); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	var previous []string
	if g, err := s.Store.ActiveOAuthGrant(r.Context(), app.ID, u.ID); err == nil {
		previous = g.ProjectIDs
	}
	scopes := make([]map[string]string, 0, len(pending.Scopes))
	for _, scope := range pending.Scopes {
		scopes = append(scopes, map[string]string{"name": scope, "description": oauthScopeDescriptions[scope]})
	}
	redirect, _ := url.Parse(pending.RedirectURI)
	host := ""
	if redirect != nil {
		host = redirect.Host
		if host == "" {
			host = redirect.Scheme + ":"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app":                  map[string]string{"name": app.Name, "description": app.Description, "client_id": app.ClientID, "redirect_host": host},
		"user":                 map[string]string{"email": u.Email, "name": u.Display(), "role": u.Role},
		"scopes":               scopes,
		"projects":             projects,
		"previous_project_ids": nonNil(previous),
		"expires_at":           pending.ExpiresAt,
	})
}

// finishOAuthConsent는 동의 화면의 결정을 처리하고 앱으로 돌아갈 URL(코드 또는 access_denied)을 돌려준다.
// 사용자는 요청받은 scope 중 일부(openid는 필수)만 승인할 수 있다.
func (s *Server) finishOAuthConsent(w http.ResponseWriter, r *http.Request) {
	pending, _, hash, ok := s.consentRequest(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "요청이 만료되었거나 이미 처리되었습니다")
		return
	}
	var in struct {
		Decision   string   `json:"decision"`
		Scopes     []string `json:"scopes"`
		ProjectIDs []string `json:"project_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	issuer := s.oauthIssuer(r)
	u := principalFrom(r).User
	respond := func(p *store.OAuthPendingRequest, params url.Values) {
		target, err := oauthAuthorizationRedirect(p.RedirectURI, issuer, params, p.State)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "등록된 redirect URI가 올바르지 않습니다")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"redirect_uri": target})
	}
	switch in.Decision {
	case "deny":
		p, err := s.Store.ConsumeOAuthPendingRequest(r.Context(), pending.ID, hash, u.ID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "요청이 만료되었거나 이미 처리되었습니다")
			return
		}
		respond(p, url.Values{"error": {"access_denied"}, "error_description": {"The user denied the request"}})
		return
	case "approve":
	default:
		writeErr(w, http.StatusBadRequest, "decision은 approve 또는 deny여야 합니다")
		return
	}
	scopes := []string{}
	for _, scope := range in.Scopes {
		if !hasOAuthScope(pending.Scopes, scope) {
			writeErr(w, http.StatusBadRequest, "요청하지 않은 scope는 승인할 수 없습니다")
			return
		}
		if !hasOAuthScope(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	if !hasOAuthScope(scopes, "openid") {
		writeErr(w, http.StatusBadRequest, "openid scope는 승인해야 합니다")
		return
	}
	slices.Sort(scopes)
	projectIDs := []string{}
	if oauthScopeLevel(scopes, "project:") > lvNone {
		ids, msg := s.validOAuthProjects(r, u, in.ProjectIDs)
		if msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		projectIDs = ids
	} else if len(in.ProjectIDs) > 0 {
		writeErr(w, http.StatusBadRequest, "프로젝트 scope 없이 프로젝트를 선택할 수 없습니다")
		return
	}
	codeID, codeValue, codeHash := newOAuthOpaque(oauthCodePrefix, "ocod")
	code := &store.OAuthAuthorizationCode{ID: codeID, CodeHash: codeHash, ExpiresAt: time.Now().Add(oauthCodeTTL)}
	p, err := s.Store.IssueOAuthAuthorizationCode(r.Context(), pending.ID, hash, u.ID, scopes, projectIDs, code)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "요청이 만료되었거나 이미 처리되었습니다")
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "oauth.consent", p.AppID, "OAuth 앱 승인: "+strings.Join(scopes, " "))
	respond(p, url.Values{"code": {codeValue}})
}
