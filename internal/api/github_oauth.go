package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 받은 토큰은 마스터 키로 암호화해 저장한다(store.github_connections). 저장소 목록·웹훅 등록은 요청한 사용자의 토큰을 쓰고,
// 배포 중의 비공개 저장소 클론·커밋 상태·PR 파일 조회는 프로젝트에 지정된 사용자의 토큰을 쓴다(deploy.Manager.ProjectGitHubToken,
// PUT /api/projects/{pid}/github). KEEL_GITHUB_TOKEN은 프로젝트에 연결이 없을 때의 대체값으로 남는다.
//
// GET    /api/github/oauth/start      → 302 GitHub 승인 화면 (state는 10분, 시작한 사용자에게 묶인다)
// GET    /api/github/oauth/callback   → 토큰 교환 후 302 /settings/?section=integrations&github=connected|error
// GET    /api/github/connection       → 내 연결 {app_configured, connected, source, login, scopes, connected_at, callback_url}
// DELETE /api/github/connection       → 내 연결 해제(GitHub에서도 승인을 취소한다)
// GET    /api/github/repos?q=         → 내 계정이 볼 수 있는 저장소(최근 push 순, 최대 300)
// POST   /api/github/webhooks {repo}  → 저장소에 aven 웹훅을 만들거나 맞춘다(멱등)

// githubScopes: 비공개 저장소 읽기·커밋 상태(repo), 저장소 웹훅 관리(admin:repo_hook).
const githubScopes = "repo admin:repo_hook"

// githubHookEvents는 aven이 처리하는 웹훅 이벤트다(githubWebhook).
var githubHookEvents = []string{"push", "pull_request", "check_suite", "workflow_run"}

type oauthState struct {
	user    string
	back    string // 승인 뒤 돌아갈 화면("/settings/" 또는 "/account/")
	expires time.Time
}

type githubStates struct {
	mu sync.Mutex
	m  map[string]oauthState
}

func (g *githubStates) put(user, back string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	st := hex.EncodeToString(b)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]oauthState{}
	}
	now := time.Now()
	for k, v := range g.m {
		if now.After(v.expires) {
			delete(g.m, k)
		}
	}
	if len(g.m) > 100 {
		return "", errors.New("too many pending GitHub authorizations")
	}
	g.m[st] = oauthState{user: user, back: back, expires: now.Add(10 * time.Minute)}
	return st, nil
}

// take는 state를 한 번만 쓴다(같은 사용자, 만료 전). 돌아갈 화면도 돌려준다.
func (g *githubStates) take(st, user string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[st]
	delete(g.m, st)
	return v.back, ok && v.user == user && time.Now().Before(v.expires)
}

// githubBackPath는 ?from=account면 계정 화면, 아니면 설정 화면이다(열린 리다이렉트가 되지 않게 두 값만 받는다).
func githubBackPath(from string) string {
	if from == "account" {
		return "/account/"
	}
	return "/settings/"
}

func (s *Server) githubOAuthBase() string {
	if s.GitHubOAuthBase != "" {
		return strings.TrimRight(s.GitHubOAuthBase, "/")
	}
	return "https://github.com"
}

func (s *Server) githubAPI() string {
	if s.GitHubAPIBase != "" {
		return strings.TrimRight(s.GitHubAPIBase, "/")
	}
	return "https://api.github.com"
}

func (s *Server) githubCallbackURL(r *http.Request) string {
	return s.publicBase(r) + "/api/github/oauth/callback"
}

func principalID(r *http.Request) string {
	if p := principalFrom(r); p != nil && p.User != nil {
		return p.User.ID
	}
	return ""
}

func (s *Server) githubOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.GitHubClientID == "" || s.GitHubClientSecret == "" {
		writeErr(w, 503, "GitHub OAuth App is not configured (KEEL_GITHUB_CLIENT_ID / KEEL_GITHUB_CLIENT_SECRET)")
		return
	}
	st, err := s.ghStates.put(principalID(r), githubBackPath(r.URL.Query().Get("from")))
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	q := url.Values{"client_id": {s.GitHubClientID}, "redirect_uri": {s.githubCallbackURL(r)}, "scope": {githubScopes},
		"state": {st}, "allow_signup": {"false"}}
	http.Redirect(w, r, s.githubOAuthBase()+"/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

// githubOAuthReturn은 GitHub 승인 화면에서 돌아오는 브라우저 이동을 받는다. 인증 미들웨어 밖에 있으므로 여기서
// 세션 쿠키를 확인한다: 없고 다른 사이트에서 왔으면 같은 origin 문서로 한 번 다시 이동시켜 쿠키가 붙게 한다.
func (s *Server) githubOAuthReturn(w http.ResponseWriter, r *http.Request) {
	p := s.oauthSession(w, r)
	if p == nil {
		if r.URL.Query().Get("bounced") == "" {
			q := r.URL.Query()
			q.Set("bounced", "1")
			oauthBounce(w, r.URL.Path+"?"+q.Encode())
			return
		}
		http.Redirect(w, r, "/login/?"+url.Values{"next": {"/settings/?section=integrations"}}.Encode(), http.StatusSeeOther)
		return
	}
	s.githubOAuthCallback(w, withPrincipal(r, p))
}

func (s *Server) githubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	path, stateOK := s.ghStates.take(r.URL.Query().Get("state"), principalID(r))
	if path == "" {
		path = "/settings/"
	}
	back := func(result, msg string) {
		q := url.Values{"section": {"integrations"}, "github": {result}}
		if msg != "" {
			q.Set("message", msg)
		}
		http.Redirect(w, r, path+"?"+q.Encode(), http.StatusFound)
	}
	if e := r.URL.Query().Get("error"); e != "" {
		back("error", firstLine(r.URL.Query().Get("error_description")+" ("+e+")"))
		return
	}
	if !stateOK {
		back("error", "승인 요청이 만료되었거나 다른 사용자가 시작했습니다. 다시 연결하세요.")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		back("error", "GitHub가 승인 코드를 보내지 않았습니다.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, scopes, err := s.githubExchange(ctx, code, s.githubCallbackURL(r))
	if err != nil {
		back("error", err.Error())
		return
	}
	var me struct {
		Login string `json:"login"`
	}
	if _, err := s.githubCall(ctx, tok, http.MethodGet, "/user", nil, &me); err != nil {
		back("error", "GitHub 계정을 읽지 못했습니다: "+err.Error())
		return
	}
	if err := s.Store.SetGitHubConnection(ctx, principalID(r), tok, me.Login, scopes); err != nil {
		back("error", err.Error())
		return
	}
	s.audit(r, "github.connected", "user", fmt.Sprintf("GitHub 계정 %s를 연결했습니다", me.Login))
	back("connected", "")
}

// githubExchange는 승인 코드를 access token으로 바꾼다.
func (s *Server) githubExchange(ctx context.Context, code, redirect string) (token, scopes string, err error) {
	form := url.Values{"client_id": {s.GitHubClientID}, "client_secret": {s.GitHubClientSecret}, "code": {code}, "redirect_uri": {redirect}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.githubOAuthBase()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GitHub 토큰 교환 실패: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", "", fmt.Errorf("GitHub 토큰 교환 응답을 읽지 못했습니다: %w", err)
	}
	if out.Error != "" || out.AccessToken == "" {
		return "", "", fmt.Errorf("GitHub 토큰 교환 실패: %s %s", out.Error, out.Description)
	}
	return out.AccessToken, out.Scope, nil
}

// githubCall은 GitHub REST API를 token으로 부른다. 2xx가 아니면 GitHub의 message를 담은 오류다.
func (s *Server) githubCall(ctx context.Context, token, method, path string, body, out any) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, s.githubAPI()+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &e)
		return resp.Header, &githubError{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, err
		}
	}
	return resp.Header, nil
}

type githubError struct {
	Status  int
	Message string
}

func (e *githubError) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Message) }

func (s *Server) githubConnection(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"app_configured":    s.GitHubClientID != "" && s.GitHubClientSecret != "",
		"connected":         false,
		"source":            "",
		"env_token":         s.Deploy.EnvGitHubToken() != "",
		"callback_url":      s.githubCallbackURL(r),
		"webhook_url":       s.publicBase(r) + "/api/webhooks/github",
		"secret_configured": s.GitHubSecret != "",
	}
	if c := s.Deploy.UserGitHubConnection(r.Context(), principalID(r)); c != nil {
		out["connected"], out["login"], out["scopes"], out["connected_at"], out["source"] = true, c.Login, c.Scopes, c.ConnectedAt, "oauth"
	} else if s.Deploy.EnvGitHubToken() != "" {
		out["source"] = "env"
	}
	writeJSON(w, 200, out)
}

func (s *Server) githubDisconnect(w http.ResponseWriter, r *http.Request) {
	uid := principalID(r)
	c := s.Deploy.UserGitHubConnection(r.Context(), uid)
	tok := s.Deploy.UserGitHubToken(r.Context(), uid)
	if c != nil {
		if err := s.Store.DeleteGitHubConnection(r.Context(), uid); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	// GitHub 쪽 승인도 취소한다(실패해도 aven에서는 이미 지웠다).
	if tok != "" && s.GitHubClientID != "" && s.GitHubClientSecret != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		b, _ := json.Marshal(map[string]string{"access_token": tok})
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.githubAPI()+"/applications/"+url.PathEscape(s.GitHubClientID)+"/grant", strings.NewReader(string(b)))
		if err == nil {
			req.SetBasicAuth(s.GitHubClientID, s.GitHubClientSecret)
			req.Header.Set("Accept", "application/vnd.github+json")
			if resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	if c != nil {
		s.audit(r, "github.disconnected", "user", fmt.Sprintf("GitHub 계정 %s 연결을 해제했습니다", c.Login))
	}
	w.WriteHeader(204)
}

type githubRepo struct {
	FullName      string    `json:"full_name"`
	Private       bool      `json:"private"`
	DefaultBranch string    `json:"default_branch"`
	CloneURL      string    `json:"clone_url"`
	PushedAt      time.Time `json:"pushed_at"`
	Permissions   struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

func (s *Server) githubRepos(w http.ResponseWriter, r *http.Request) {
	tok := s.Deploy.UserGitHubToken(r.Context(), principalID(r))
	if tok == "" {
		writeErr(w, 409, "GitHub is not connected")
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	var all []githubRepo
	for page := 1; page <= 3; page++ {
		var batch []githubRepo
		path := fmt.Sprintf("/user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member&page=%d", page)
		if _, err := s.githubCall(r.Context(), tok, http.MethodGet, path, nil, &batch); err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	out := []map[string]any{}
	for _, g := range all {
		if q != "" && !strings.Contains(strings.ToLower(g.FullName), q) {
			continue
		}
		out = append(out, map[string]any{"full_name": g.FullName, "private": g.Private, "default_branch": g.DefaultBranch,
			"url": "https://github.com/" + g.FullName, "pushed_at": g.PushedAt, "admin": g.Permissions.Admin})
	}
	writeJSON(w, 200, out)
}

// githubOwnerRepo는 "owner/repo" 또는 GitHub URL(https/ssh/git@)에서 owner/repo를 꺼낸다(대소문자는 그대로 둔다).
func githubOwnerRepo(v string) (string, bool) {
	v = strings.TrimSpace(v)
	for _, p := range []string{"https://", "http://", "ssh://"} {
		v = strings.TrimPrefix(v, p)
	}
	v = strings.TrimPrefix(v, "git@")
	if at := strings.Index(v, "@"); at >= 0 && at < strings.Index(v+"/", "/") {
		v = v[at+1:]
	}
	v = strings.Replace(v, "github.com:", "github.com/", 1)
	if host, rest, ok := strings.Cut(v, "/"); ok && strings.Contains(host, ".") {
		if !strings.EqualFold(host, "github.com") {
			return "", false
		}
		v = rest
	}
	v = strings.TrimSuffix(strings.TrimRight(v, "/"), ".git")
	parts := strings.Split(v, "/")
	if len(parts) != 2 || !ghNameRe.MatchString(parts[0]) || !ghNameRe.MatchString(parts[1]) {
		return "", false
	}
	for _, p := range parts {
		if strings.Trim(p, ".") == "" {
			return "", false // "." / ".."
		}
	}
	return v, true
}

var ghNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

type githubHook struct {
	ID     int64    `json:"id"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// githubEnsureWebhook은 저장소에 aven 웹훅이 있게 한다. 같은 URL의 훅이 있으면 이벤트·시크릿을 맞추고, 없으면 만든다.
func (s *Server) githubEnsureWebhook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo string `json:"repo"`
	}
	if !decode(w, r, &in) {
		return
	}
	repo, ok := githubOwnerRepo(in.Repo)
	if !ok {
		writeErr(w, 400, "repo must be owner/name or a github.com URL")
		return
	}
	if s.GitHubSecret == "" {
		writeErr(w, 409, "KEEL_GITHUB_WEBHOOK_SECRET is not configured; aven could not verify the deliveries")
		return
	}
	tok := s.Deploy.UserGitHubToken(r.Context(), principalID(r))
	if tok == "" {
		writeErr(w, 409, "GitHub is not connected")
		return
	}
	hookURL := s.publicBase(r) + "/api/webhooks/github"
	if !strings.HasPrefix(hookURL, "https://") {
		writeErr(w, 409, "KEEL_PUBLIC_URL must be an https:// URL that GitHub can reach")
		return
	}
	cfg := map[string]any{"url": hookURL, "content_type": "json", "secret": s.GitHubSecret, "insecure_ssl": "0"}
	events := append([]string(nil), githubHookEvents...)
	sort.Strings(events)
	var hooks []githubHook
	if _, err := s.githubCall(r.Context(), tok, http.MethodGet, "/repos/"+repo+"/hooks?per_page=100", nil, &hooks); err != nil {
		writeErr(w, githubStatus(err), err.Error())
		return
	}
	for _, h := range hooks {
		if h.Config.URL != hookURL {
			continue
		}
		// 시크릿은 GitHub가 돌려주지 않으므로 항상 다시 쓴다.
		if _, err := s.githubCall(r.Context(), tok, http.MethodPatch, fmt.Sprintf("/repos/%s/hooks/%d", repo, h.ID),
			map[string]any{"active": true, "events": events, "config": cfg}, nil); err != nil {
			writeErr(w, githubStatus(err), err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"repo": repo, "hook_id": h.ID, "created": false, "url": hookURL})
		return
	}
	var created githubHook
	if _, err := s.githubCall(r.Context(), tok, http.MethodPost, "/repos/"+repo+"/hooks",
		map[string]any{"name": "web", "active": true, "events": events, "config": cfg}, &created); err != nil {
		writeErr(w, githubStatus(err), err.Error())
		return
	}
	s.audit(r, "github.webhook_created", repo, fmt.Sprintf("%s 저장소에 aven 웹훅을 만들었습니다", repo))
	writeJSON(w, 201, map[string]any{"repo": repo, "hook_id": created.ID, "created": true, "url": hookURL})
}

// githubStatus는 GitHub 오류를 aven 응답 코드로 바꾼다(권한/없음은 그대로, 나머지는 502).
func githubStatus(err error) int {
	var ge *githubError
	if errors.As(err, &ge) {
		switch ge.Status {
		case 401, 403, 404, 422:
			return ge.Status
		}
	}
	return 502
}
