package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/secret"
	"github.com/turin-dev/aven/internal/store"
)

// fakeGitHub는 OAuth 토큰 교환, /user, /user/repos, 저장소 hooks, grant 취소를 흉내 낸다.
type fakeGitHub struct {
	mu      sync.Mutex
	hooks   []map[string]any
	patched int
	revoked string
	tokens  []string // Authorization으로 받은 토큰
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "cid" || r.Form.Get("client_secret") != "csecret" || r.Form.Get("code") != "good-code" {
			json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_oauthtoken", "scope": "admin:repo_hook,repo", "token_type": "bearer"})
	})
	auth := func(r *http.Request) {
		f.mu.Lock()
		f.tokens = append(f.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		auth(r)
		json.NewEncoder(w).Encode(map[string]string{"login": "turin-dev"})
	})
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		auth(r)
		json.NewEncoder(w).Encode([]map[string]any{
			{"full_name": "example-org/sample-repo", "private": true, "default_branch": "main", "permissions": map[string]bool{"admin": true}},
			{"full_name": "example-org/blog", "private": false, "default_branch": "main"},
		})
	})
	mux.HandleFunc("GET /repos/example-org/sample-repo/hooks", func(w http.ResponseWriter, r *http.Request) {
		auth(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.hooks)
	})
	mux.HandleFunc("POST /repos/example-org/sample-repo/hooks", func(w http.ResponseWriter, r *http.Request) {
		auth(r)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		in["id"] = 7
		f.hooks = append(f.hooks, in)
		f.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(in)
	})
	mux.HandleFunc("PATCH /repos/example-org/sample-repo/hooks/7", func(w http.ResponseWriter, r *http.Request) {
		auth(r)
		f.mu.Lock()
		f.patched++
		f.mu.Unlock()
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /repos/o/private/hooks", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("DELETE /applications/cid/grant", func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		b, _ := io.ReadAll(r.Body)
		if u == "cid" && p == "csecret" {
			f.mu.Lock()
			f.revoked = string(b)
			f.mu.Unlock()
		}
		w.WriteHeader(204)
	})
	return mux
}

type ghOAuthFixture struct {
	ownerU, otherU *store.User
	s              *Server
	h              http.Handler
	gh             *fakeGitHub
	owner          func(*http.Request)
	other          func(*http.Request)
}

func newGHOAuthFixture(t *testing.T) *ghOAuthFixture {
	st := testStore(t)
	box, err := secret.LoadOrCreate(t.TempDir() + "/master.key")
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretBox(box)
	gh := &fakeGitHub{}
	srv := httptest.NewServer(gh.handler(t))
	t.Cleanup(srv.Close)
	s := &Server{Store: st, AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Deploy: &deploy.Manager{Store: st}, PublicURL: "https://aven.example.com", GitHubSecret: "hooksecret",
		GitHubClientID: "cid", GitHubClientSecret: "csecret", GitHubOAuthBase: srv.URL, GitHubAPIBase: srv.URL}
	owner := testUser(t, st, "owner@example.com", store.RoleOwner)
	other := testUser(t, st, "admin2@example.com", store.RoleAdmin)
	return &ghOAuthFixture{ownerU: owner, otherU: other, s: s, h: s.Handler(), gh: gh, owner: withCookie(testSessionCookie(t, s, owner)), other: withCookie(testSessionCookie(t, s, other))}
}

func (f *ghOAuthFixture) start(t *testing.T) string {
	t.Helper()
	w := req(f.h, "GET", "/api/github/oauth/start", "", f.owner)
	if w.Code != 302 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	q := loc.Query()
	if q.Get("client_id") != "cid" || q.Get("redirect_uri") != "https://aven.example.com/api/github/oauth/callback" ||
		q.Get("scope") != githubScopes || q.Get("state") == "" {
		t.Fatalf("authorize redirect: %s", loc)
	}
	return q.Get("state")
}

func TestGitHubOAuthConnectFlow(t *testing.T) {
	f := newGHOAuthFixture(t)
	st := f.start(t)
	// 쿠키 없이 돌아오면(SameSite=Strict, cross-site) 같은 origin에서 한 번 다시 이동시키고, 두 번째도 없으면 로그인으로 보낸다
	w0 := req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+st, "", nil)
	if w0.Code != 200 || !strings.Contains(w0.Body.String(), "bounced=1") {
		t.Fatalf("bounce: %d %s", w0.Code, w0.Body.String())
	}
	w0 = req(f.h, "GET", "/api/github/oauth/callback?bounced=1&code=good-code&state="+st, "", nil)
	if w0.Code != 303 || !strings.HasPrefix(w0.Header().Get("Location"), "/login/") {
		t.Fatalf("no session after bounce: %d %s", w0.Code, w0.Header().Get("Location"))
	}
	// 다른 사용자가 같은 state로 돌아오면 거부한다(그리고 state는 한 번만 쓴다)
	w := req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+st, "", f.other)
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "github=error") {
		t.Fatalf("foreign state: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+st, "", f.owner)
	if !strings.Contains(w.Header().Get("Location"), "github=error") {
		t.Fatal("a state must not be reusable")
	}
	st = f.start(t)
	w = req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+st, "", f.owner)
	if w.Code != 302 || w.Header().Get("Location") != "/settings/?github=connected&section=integrations" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	ctx := context.Background()
	if got := f.s.Deploy.UserGitHubToken(ctx, f.ownerU.ID); got != "gho_oauthtoken" {
		t.Fatalf("token %q", got)
	}
	if c, err := f.s.Store.GetGitHubConnection(ctx, f.ownerU.ID); err != nil || strings.Contains(c.Token, "gho_oauthtoken") {
		t.Fatalf("the token must be stored encrypted: %v", err)
	}
	// 연결은 사용자별이다: 다른 사용자에게는 연결이 없고 저장소 목록도 볼 수 없다
	if f.s.Deploy.UserGitHubToken(ctx, f.otherU.ID) != "" {
		t.Fatal("another user must not get the owner's token")
	}
	if w := req(f.h, "GET", "/api/github/connection", "", f.other); decodeBody[map[string]any](t, w)["connected"] != false {
		t.Fatalf("other connection: %s", w.Body.String())
	}
	if w := req(f.h, "GET", "/api/github/repos", "", f.other); w.Code != 409 {
		t.Fatalf("other repos: %d", w.Code)
	}
	w = req(f.h, "GET", "/api/github/connection", "", f.owner)
	conn := decodeBody[map[string]any](t, w)
	if conn["connected"] != true || conn["login"] != "turin-dev" || conn["source"] != "oauth" || strings.Contains(w.Body.String(), "gho_") {
		t.Fatalf("connection: %s", w.Body.String())
	}
	// 저장소 목록
	w = req(f.h, "GET", "/api/github/repos?q=sample", "", f.owner)
	repos := decodeBody[[]map[string]any](t, w)
	if len(repos) != 1 || repos[0]["url"] != "https://github.com/example-org/sample-repo" {
		t.Fatalf("repos: %s", w.Body.String())
	}
	// 해제: aven에서 지우고 GitHub 승인도 취소한다
	if w := req(f.h, "DELETE", "/api/github/connection", "", f.owner); w.Code != 204 {
		t.Fatalf("disconnect: %d", w.Code)
	}
	if f.s.Deploy.UserGitHubToken(ctx, f.ownerU.ID) != "" || !strings.Contains(f.gh.revoked, "gho_oauthtoken") {
		t.Fatalf("after disconnect: token=%q revoked=%q", f.s.Deploy.UserGitHubToken(ctx, f.ownerU.ID), f.gh.revoked)
	}
}

// 누구나(인스턴스 viewer도) 자기 GitHub 계정을 연결한다.
func TestGitHubOAuthAnyUserConnectsOwnAccount(t *testing.T) {
	f := newGHOAuthFixture(t)
	vu := testUser(t, f.s.Store, "v@example.com", store.RoleViewer)
	viewer := withCookie(testSessionCookie(t, f.s, vu))
	// 계정 화면(가입 직후 안내)에서 시작하면 계정 화면으로 돌아온다.
	w := req(f.h, "GET", "/api/github/oauth/start?from=account", "", viewer)
	if w.Code != 302 {
		t.Fatalf("viewer start: %d", w.Code)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	w = req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+loc.Query().Get("state"), "", viewer)
	if got := w.Header().Get("Location"); got != "/account/?github=connected&section=integrations" {
		t.Fatalf("viewer callback: %s", w.Header().Get("Location"))
	}
	if f.s.Deploy.UserGitHubToken(context.Background(), vu.ID) != "gho_oauthtoken" || f.s.Deploy.UserGitHubConnection(context.Background(), f.ownerU.ID) != nil {
		t.Fatal("the connection must belong to the viewer only")
	}
}

func TestGitHubOAuthBadCode(t *testing.T) {
	f := newGHOAuthFixture(t)
	st := f.start(t)
	w := req(f.h, "GET", "/api/github/oauth/callback?code=bad&state="+st, "", f.owner)
	if !strings.Contains(w.Header().Get("Location"), "github=error") || f.s.Deploy.UserGitHubConnection(context.Background(), f.ownerU.ID) != nil {
		t.Fatalf("bad code must not connect: %s", w.Header().Get("Location"))
	}
	w = req(f.h, "GET", "/api/github/oauth/callback?error=access_denied&error_description=user+said+no", "", f.owner)
	if !strings.Contains(w.Header().Get("Location"), "access_denied") {
		t.Fatalf("denied: %s", w.Header().Get("Location"))
	}
}

func TestGitHubOAuthNotConfigured(t *testing.T) {
	f := newGHOAuthFixture(t)
	f.s.GitHubClientSecret = ""
	if w := req(f.h, "GET", "/api/github/oauth/start", "", f.owner); w.Code != 503 {
		t.Fatalf("start without app: %d", w.Code)
	}
}

func TestGitHubEnsureWebhook(t *testing.T) {
	f := newGHOAuthFixture(t)
	if w := req(f.h, "POST", "/api/github/webhooks", `{"repo":"example-org/sample-repo"}`, f.owner); w.Code != 409 {
		t.Fatalf("not connected: %d", w.Code)
	}
	f.s.Store.SetGitHubConnection(context.Background(), f.ownerU.ID, "gho_oauthtoken", "turin-dev", "repo")
	if w := req(f.h, "POST", "/api/github/webhooks", `{"repo":"example-org/sample-repo"}`, f.other); w.Code != 409 {
		t.Fatalf("another user's connection must not be used: %d", w.Code)
	}
	w := req(f.h, "POST", "/api/github/webhooks", `{"repo":"git@github.com:example-org/sample-repo.git"}`, f.owner)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	hook := f.gh.hooks[0]
	cfg := hook["config"].(map[string]any)
	if cfg["url"] != "https://aven.example.com/api/webhooks/github" || cfg["secret"] != "hooksecret" || cfg["content_type"] != "json" {
		t.Fatalf("hook config: %v", cfg)
	}
	if ev := hook["events"].([]any); len(ev) != len(githubHookEvents) {
		t.Fatalf("events: %v", ev)
	}
	// 두 번째는 만들지 않고 맞춘다
	w = req(f.h, "POST", "/api/github/webhooks", `{"repo":"https://github.com/example-org/sample-repo"}`, f.owner)
	if w.Code != 200 || len(f.gh.hooks) != 1 || f.gh.patched != 1 {
		t.Fatalf("idempotent: %d hooks=%d patched=%d", w.Code, len(f.gh.hooks), f.gh.patched)
	}
	if w := req(f.h, "POST", "/api/github/webhooks", `{"repo":"o/private"}`, f.owner); w.Code != 404 {
		t.Fatalf("GitHub 404 should pass through: %d", w.Code)
	}
	if w := req(f.h, "POST", "/api/github/webhooks", `{"repo":"gitlab.com/a/b"}`, f.owner); w.Code != 400 {
		t.Fatalf("non-GitHub repo: %d", w.Code)
	}
	for _, tok := range f.gh.tokens {
		if tok != "gho_oauthtoken" {
			t.Fatalf("GitHub was called with %q", tok)
		}
	}
}

// 배포는 프로젝트에 지정된 사용자의 연결을 쓰고, 없으면 KEEL_GITHUB_TOKEN만 쓴다(다른 사용자의 연결로 넘어가지 않는다).
func TestProjectGitHubTokenPerUser(t *testing.T) {
	st := testStore(t)
	m := &deploy.Manager{Store: st, Cfg: deploy.Config{GitHubToken: "env-token"}}
	ctx := context.Background()
	alice := testUser(t, st, "alice@example.com", store.RoleMember)
	bob := testUser(t, st, "bob@example.com", store.RoleMember)
	st.SetGitHubConnection(ctx, alice.ID, "gho_alice", "alice", "repo")
	st.SetGitHubConnection(ctx, bob.ID, "gho_bob", "bob", "repo")
	pa := &store.Project{Name: "a", GitHubUserID: &alice.ID}
	if _, err := st.CreateProject(ctx, pa); err != nil {
		t.Fatal(err)
	}
	pn := &store.Project{Name: "n"}
	if _, err := st.CreateProject(ctx, pn); err != nil {
		t.Fatal(err)
	}
	if got := m.ProjectGitHubToken(ctx, pa.ID); got != "gho_alice" {
		t.Fatalf("alice project: %q", got)
	}
	if got := m.ProjectGitHubToken(ctx, pn.ID); got != "env-token" {
		t.Fatalf("project without a connection: %q", got)
	}
	st.DeleteGitHubConnection(ctx, alice.ID)
	if got := m.ProjectGitHubToken(ctx, pa.ID); got != "env-token" {
		t.Fatalf("after alice disconnects: %q", got)
	}
}

func TestGitHubOwnerRepo(t *testing.T) {
	for in, want := range map[string]string{
		"example-org/sample-repo": "example-org/sample-repo", "git@github.com:example-org/sample-repo.git": "example-org/sample-repo",
		"https://github.com/turin-dev/blog": "turin-dev/blog", "ssh://git@github.com/o/r.git": "o/r", "https://x:tok@github.com/o/r/": "o/r",
	} {
		if got, ok := githubOwnerRepo(in); !ok || got != want {
			t.Errorf("%s → %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"gitlab.com/a/b", "a/b/c", "a", "../x/y", "o/r?x", "../x", "o/.."} {
		if _, ok := githubOwnerRepo(bad); ok {
			t.Errorf("%s should be rejected", bad)
		}
	}
}
