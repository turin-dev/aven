package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 앱에서 토큰 만료를 켜면 GitHub는 8시간짜리 access token과 refresh token을 준다. aven은 refresh token을 저장해 두고
// 만료 전에 갱신해야 한다(예전에는 access token만 저장해서 연결이 8시간 뒤에 끊겼다).
func TestGitHubExpiringTokenRefresh(t *testing.T) {
	f := newGHOAuthFixture(t)
	f.gh.expiring = true
	f.s.Deploy.Cfg.GitHubClientID, f.s.Deploy.Cfg.GitHubClientSecret, f.s.Deploy.Cfg.GitHubOAuthBase = "cid", "csecret", f.s.GitHubOAuthBase
	ctx := context.Background()

	st := f.start(t)
	if w := req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+st, "", f.owner); w.Code != 302 {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	c, err := f.s.Store.GetGitHubConnection(ctx, f.ownerU.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(c.ExpiresAt); d < 7*time.Hour || d > 9*time.Hour {
		t.Fatalf("expires_at should be ~8h ahead: %v", c.ExpiresAt)
	}
	if c.RefreshToken == "" || c.RefreshToken == "ghr_1" {
		t.Fatalf("the refresh token must be stored encrypted: %q", c.RefreshToken)
	}
	if c.RefreshExpiresAt.IsZero() {
		t.Fatal("refresh_expires_at missing")
	}
	// 아직 유효하면 갱신하지 않는다
	if got := f.s.Deploy.UserGitHubToken(ctx, f.ownerU.ID); got != "gho_oauthtoken" || f.gh.refreshes != 0 {
		t.Fatalf("fresh token: %q, refreshes %d", got, f.gh.refreshes)
	}

	// 만료가 가까워지면 한 번만 갱신한다(동시에 여러 배포가 토큰을 찾아도 refresh token은 한 번만 쓴다)
	expire := func(at time.Time) {
		t.Helper()
		cur, _ := f.s.Store.GetGitHubConnection(ctx, f.ownerU.ID)
		access, _ := f.s.Store.Unseal(cur.Token)
		refresh, _ := f.s.Store.Unseal(cur.RefreshToken)
		if ok, err := f.s.Store.RefreshGitHubTokens(ctx, f.ownerU.ID, cur.RefreshToken,
			store.GitHubTokens{Access: access, Refresh: refresh, ExpiresAt: at, RefreshExpiresAt: cur.RefreshExpiresAt}); !ok || err != nil {
			t.Fatalf("expire: %v %v", ok, err)
		}
	}
	expire(time.Now().Add(2 * time.Minute))
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = f.s.Deploy.UserGitHubToken(ctx, f.ownerU.ID)
		}(i)
	}
	wg.Wait()
	for _, g := range got {
		if g != "gho_refreshed_1" {
			t.Fatalf("refreshed tokens: %v", got)
		}
	}
	if f.gh.refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", f.gh.refreshes)
	}
	c2, _ := f.s.Store.GetGitHubConnection(ctx, f.ownerU.ID)
	if !c2.ConnectedAt.Equal(c.ConnectedAt) || time.Until(c2.ExpiresAt) < 7*time.Hour {
		t.Fatalf("after refresh: connected_at %v→%v, expires_at %v", c.ConnectedAt, c2.ConnectedAt, c2.ExpiresAt)
	}
	if w := req(f.h, "GET", "/api/github/connection", "", f.owner); decodeBody[map[string]any](t, w)["expired"] != false {
		t.Fatalf("connection after refresh: %s", w.Body.String())
	}

	// GitHub가 refresh token을 거부하면(취소·만료) 기존 토큰을 그대로 돌려주고, 만료된 뒤에는 연결 상태가 expired다
	f.gh.mu.Lock()
	f.gh.refresh = "ghr_revoked"
	f.gh.mu.Unlock()
	expire(time.Now().Add(-time.Minute))
	if got := f.s.Deploy.UserGitHubToken(ctx, f.ownerU.ID); got != "gho_refreshed_1" {
		t.Fatalf("on refresh failure the old token stays: %q", got)
	}
	cur, _ := f.s.Store.GetGitHubConnection(ctx, f.ownerU.ID)
	access, _ := f.s.Store.Unseal(cur.Token)
	f.s.Store.RefreshGitHubTokens(ctx, f.ownerU.ID, cur.RefreshToken,
		store.GitHubTokens{Access: access, Refresh: "ghr_x", ExpiresAt: time.Now().Add(-time.Minute), RefreshExpiresAt: time.Now().Add(-time.Second)})
	if w := req(f.h, "GET", "/api/github/connection", "", f.owner); decodeBody[map[string]any](t, w)["expired"] != true {
		t.Fatalf("connection with a dead refresh token: %s", w.Body.String())
	}
}

// 만료를 끈 앱의 토큰(refresh token 없음)은 갱신하지 않고 expires_at도 null이다.
func TestGitHubNonExpiringTokenUnchanged(t *testing.T) {
	f := newGHOAuthFixture(t)
	f.s.Deploy.Cfg.GitHubClientID, f.s.Deploy.Cfg.GitHubClientSecret, f.s.Deploy.Cfg.GitHubOAuthBase = "cid", "csecret", f.s.GitHubOAuthBase
	st := f.start(t)
	req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+st, "", f.owner)
	if got := f.s.Deploy.UserGitHubToken(context.Background(), f.ownerU.ID); got != "gho_oauthtoken" || f.gh.refreshes != 0 {
		t.Fatalf("token %q, refreshes %d", got, f.gh.refreshes)
	}
	out := decodeBody[map[string]any](t, req(f.h, "GET", "/api/github/connection", "", f.owner))
	if out["connected"] != true || out["expires_at"] != nil || out["expired"] != false {
		t.Fatalf("connection: %v", out)
	}
}
