package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func oauthTestStore(t *testing.T) (*Store, *OAuthApp, *User) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	u := &User{Email: "u@example.com", Role: RoleMember}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	app := &OAuthApp{ClientID: "keel_client_x", Name: "x", ClientType: "confidential", RedirectURIs: []string{"https://a.example/cb"}}
	if err := s.CreateOAuthApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthApp(ctx, &OAuthApp{ClientID: "keel_client_x", Name: "dup", ClientType: "public", RedirectURIs: []string{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate client_id: %v", err)
	}
	return s, app, u
}

func exchangeTestCode(t *testing.T, s *Store, app *OAuthApp, u *User, n int, scopes []string) (*OAuthGrant, *OAuthRefreshToken) {
	t.Helper()
	ctx := context.Background()
	code := &OAuthAuthorizationCode{ID: fmt.Sprintf("ocod_%d", n), CodeHash: "h", AppID: app.ID, UserID: u.ID, RedirectURI: "https://a.example/cb",
		Scopes: scopes, ProjectIDs: []string{}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := s.CreateOAuthAuthorizationCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	access := &OAuthAccessToken{ID: fmt.Sprintf("oat_%d", n), TokenHash: "a", ExpiresAt: time.Now().Add(time.Hour)}
	refresh := &OAuthRefreshToken{ID: fmt.Sprintf("ort_%d", n), TokenHash: "r", ExpiresAt: time.Now().Add(time.Hour)}
	_, g, err := s.ExchangeOAuthAuthorizationCode(ctx, code.ID, "h", app.ID, code.RedirectURI, func(*OAuthAuthorizationCode) bool { return true }, access, refresh)
	if err != nil {
		t.Fatal(err)
	}
	return g, refresh
}

func TestOAuthGrantLifecycle(t *testing.T) {
	ctx := context.Background()
	s, app, u := oauthTestStore(t)
	g1, _ := exchangeTestCode(t, s, app, u, 1, []string{"offline_access", "openid"})
	// 다시 동의하면 같은 승인을 갱신한다(앱+사용자당 활성 승인 하나).
	g2, _ := exchangeTestCode(t, s, app, u, 2, []string{"email", "offline_access", "openid"})
	if g1.ID != g2.ID {
		t.Fatalf("second consent created a new grant: %s vs %s", g1.ID, g2.ID)
	}
	if g, err := s.ActiveOAuthGrant(ctx, app.ID, u.ID); err != nil || len(g.Scopes) != 3 {
		t.Fatalf("grant scopes: %+v %v", g, err)
	}
	// offline_access가 없으면 refresh token을 저장하지 않는다.
	g3, rt := exchangeTestCode(t, s, app, u, 3, []string{"openid"})
	if _, err := s.RotateOAuthRefreshToken(ctx, rt.ID, "r", app.ID, func(*OAuthGrant) bool { return true },
		&OAuthAccessToken{ID: "oat_x", TokenHash: "a", ExpiresAt: time.Now().Add(time.Hour)},
		&OAuthRefreshToken{ID: "ort_x", TokenHash: "r", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresh token stored without offline_access: %v", err)
	}
	// 코드 재사용은 승인을 폐기한다.
	_, _, err := s.ExchangeOAuthAuthorizationCode(ctx, "ocod_3", "h", app.ID, "https://a.example/cb", func(*OAuthAuthorizationCode) bool { return true },
		&OAuthAccessToken{ID: "oat_y", TokenHash: "a", ExpiresAt: time.Now().Add(time.Hour)}, nil)
	if !errors.Is(err, ErrOAuthCodeReplay) {
		t.Fatalf("replay: %v", err)
	}
	if _, _, err := s.GetOAuthAccessToken(ctx, "oat_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("access token survives replay revocation: %v", err)
	}
	if _, err := s.ActiveOAuthGrant(ctx, app.ID, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("grant %s survives replay: %v", g3.ID, err)
	}
	// 폐기 뒤 새 동의는 새 승인이다.
	g4, _ := exchangeTestCode(t, s, app, u, 4, []string{"openid"})
	if g4.ID == g1.ID {
		t.Fatal("revoked grant was reused")
	}
	if apps, _ := s.ListOAuthAuthorizedApps(ctx, u.ID); len(apps) != 1 || apps[0].GrantID != g4.ID {
		t.Fatalf("authorized apps: %+v", apps)
	}
}

func TestOAuthRefreshTokenCap(t *testing.T) {
	ctx := context.Background()
	s, app, u := oauthTestStore(t)
	g, first := exchangeTestCode(t, s, app, u, 0, []string{"offline_access", "openid"})
	var last *OAuthRefreshToken
	for i := 1; i <= OAuthMaxRefreshTokens; i++ {
		last = &OAuthRefreshToken{ID: fmt.Sprintf("ort_cap_%03d", i), TokenHash: "r", ExpiresAt: time.Now().Add(time.Hour)}
		if err := s.insertRefreshToken(ctx, g.ID, last, time.Now().Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	var active int
	s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_refresh_tokens WHERE grant_id=? AND revoked_at IS NULL`, g.ID).Scan(&active)
	if active != OAuthMaxRefreshTokens {
		t.Fatalf("active refresh tokens = %d", active)
	}
	allow := func(*OAuthGrant) bool { return true }
	newPair := func(n string) (*OAuthAccessToken, *OAuthRefreshToken) {
		return &OAuthAccessToken{ID: "oat_" + n, TokenHash: "a", ExpiresAt: time.Now().Add(time.Hour)},
			&OAuthRefreshToken{ID: "ort_" + n, TokenHash: "r", ExpiresAt: time.Now().Add(time.Hour)}
	}
	a, r := newPair("a")
	if _, err := s.RotateOAuthRefreshToken(ctx, first.ID, "r", app.ID, allow, a, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest refresh token not revoked by the cap: %v", err)
	}
	a, r = newPair("b")
	if _, err := s.RotateOAuthRefreshToken(ctx, last.ID, "wrong", app.ID, allow, a, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong hash: %v", err)
	}
	if _, err := s.RotateOAuthRefreshToken(ctx, last.ID, "r", app.ID, func(*OAuthGrant) bool { return false }, a, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disallowed: %v", err)
	}
	if _, err := s.RotateOAuthRefreshToken(ctx, last.ID, "r", app.ID, allow, a, r); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	a, r = newPair("c")
	if _, err := s.RotateOAuthRefreshToken(ctx, last.ID, "r", app.ID, allow, a, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotated token reused: %v", err)
	}
	if err := s.PruneOAuth(ctx); err != nil {
		t.Fatal(err)
	}
	// 앱을 지우면 승인과 토큰이 모두 사라진다.
	if err := s.DeleteOAuthApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM oauth_grants) + (SELECT COUNT(*) FROM oauth_refresh_tokens) + (SELECT COUNT(*) FROM oauth_access_tokens)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after deleting the app", n)
	}
}
