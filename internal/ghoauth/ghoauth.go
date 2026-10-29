// Package ghoauth는 GitHub OAuth 토큰 엔드포인트(/login/oauth/access_token)를 부른다: 승인 코드 교환과 refresh token 갱신.
//
// OAuth App이나 GitHub App에서 토큰 만료(expiring user tokens)를 켜면 access token은 8시간, refresh token은 6개월 뒤에
// 만료되고 refresh token은 한 번만 쓸 수 있다. 만료를 끈 앱은 refresh_token/expires_in 없이 만료되지 않는 토큰을 준다.
package ghoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBase는 github.com이다(GitHub Enterprise나 테스트는 다른 base를 넘긴다).
const DefaultBase = "https://github.com"

// Token은 토큰 엔드포인트 응답이다. 만료가 없는 토큰이면 Refresh와 두 만료 시각이 비어 있다.
type Token struct {
	Access           string
	Refresh          string
	Scope            string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

// Error는 GitHub가 돌려준 OAuth 오류다(bad_verification_code, bad_refresh_token 등).
type Error struct {
	Code        string
	Description string
}

func (e *Error) Error() string { return strings.TrimSpace(e.Code + " " + e.Description) }

// Exchange는 승인 코드를 토큰으로 바꾼다.
func Exchange(ctx context.Context, base, clientID, clientSecret, code, redirect string) (*Token, error) {
	return call(ctx, base, url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "code": {code}, "redirect_uri": {redirect}})
}

// Refresh는 refresh token으로 새 토큰을 받는다. 성공하면 이전 refresh token은 더 쓸 수 없다.
func Refresh(ctx context.Context, base, clientID, clientSecret, refresh string) (*Token, error) {
	return call(ctx, base, url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

func call(ctx context.Context, base string, form url.Values) (*Token, error) {
	if base == "" {
		base = DefaultBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		Scope                 string `json:"scope"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
		Description           string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("GitHub token response (HTTP %d): %w", resp.StatusCode, err)
	}
	if out.Error != "" || out.AccessToken == "" {
		return nil, &Error{Code: out.Error, Description: out.Description}
	}
	now := time.Now()
	t := &Token{Access: out.AccessToken, Refresh: out.RefreshToken, Scope: out.Scope}
	if out.ExpiresIn > 0 {
		t.ExpiresAt = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.RefreshTokenExpiresIn > 0 {
		t.RefreshExpiresAt = now.Add(time.Duration(out.RefreshTokenExpiresIn) * time.Second)
	}
	return t, nil
}
