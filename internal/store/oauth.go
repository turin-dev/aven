package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// ---------------- OAuth 2.0 / OpenID Connect provider ("Login with Aven") ----------------
//
// oauth_apps는 작업 공간에 등록된 OAuth 클라이언트다(작업 공간 admin이 관리한다).
// 비밀 값(client secret, 인가 요청, 인가 코드, access/refresh token)은 모두 SHA-256 해시만 저장한다.
// 한 사용자는 앱마다 활성 승인(oauth_grants)을 하나만 가진다. 다시 동의하면 그 승인의 scope와 프로젝트를 바꾸고,
// 승인을 폐기하면 거기서 발급된 모든 토큰이 함께 무효가 된다.

// ErrOAuthCodeReplay는 이미 쓴 인가 코드를 다시 제출한 경우다. 그 코드로 만든 승인은 폐기된다(RFC 6749 4.1.2).
var ErrOAuthCodeReplay = errors.New("oauth authorization code reused")

// OAuthMaxRefreshTokens는 승인 하나가 가질 수 있는 활성 refresh token 수다. 넘으면 가장 오래된 것부터 폐기한다.
const OAuthMaxRefreshTokens = 100

func migrateOAuthProvider(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE oauth_apps (
			id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL UNIQUE,
			created_by TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			client_type TEXT NOT NULL,
			redirect_uris TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE oauth_client_secrets (
			id TEXT PRIMARY KEY,
			app_id TEXT NOT NULL REFERENCES oauth_apps(id) ON DELETE CASCADE,
			secret_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			revoked_at TEXT
		)`,
		`CREATE INDEX oauth_client_secrets_app ON oauth_client_secrets(app_id, created_at DESC)`,
		`CREATE TABLE oauth_pending_requests (
			id TEXT PRIMARY KEY,
			request_hash TEXT NOT NULL,
			app_id TEXT NOT NULL REFERENCES oauth_apps(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			redirect_uri TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT '',
			scopes TEXT NOT NULL,
			code_challenge TEXT NOT NULL DEFAULT '',
			nonce TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX oauth_pending_expiry ON oauth_pending_requests(expires_at)`,
		`CREATE TABLE oauth_grants (
			id TEXT PRIMARY KEY,
			app_id TEXT NOT NULL REFERENCES oauth_apps(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			scopes TEXT NOT NULL,
			project_ids TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_used_at TEXT,
			revoked_at TEXT
		)`,
		`CREATE INDEX oauth_grants_user ON oauth_grants(user_id, updated_at DESC)`,
		`CREATE UNIQUE INDEX oauth_grants_active ON oauth_grants(app_id, user_id) WHERE revoked_at IS NULL`,
		`CREATE TABLE oauth_authorization_codes (
			id TEXT PRIMARY KEY,
			code_hash TEXT NOT NULL,
			app_id TEXT NOT NULL REFERENCES oauth_apps(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			grant_id TEXT REFERENCES oauth_grants(id) ON DELETE SET NULL,
			redirect_uri TEXT NOT NULL,
			scopes TEXT NOT NULL,
			project_ids TEXT NOT NULL DEFAULT '[]',
			code_challenge TEXT NOT NULL DEFAULT '',
			nonce TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL,
			used_at TEXT
		)`,
		`CREATE INDEX oauth_codes_expiry ON oauth_authorization_codes(expires_at)`,
		`CREATE TABLE oauth_access_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL,
			grant_id TEXT NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX oauth_access_grant ON oauth_access_tokens(grant_id)`,
		`CREATE INDEX oauth_access_expiry ON oauth_access_tokens(expires_at)`,
		`CREATE TABLE oauth_refresh_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL,
			grant_id TEXT NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			revoked_at TEXT
		)`,
		`CREATE INDEX oauth_refresh_grant ON oauth_refresh_tokens(grant_id, created_at)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

type OAuthApp struct {
	ID           string    `json:"id"`
	ClientID     string    `json:"client_id"`
	CreatedBy    string    `json:"created_by"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	ClientType   string    `json:"client_type"` // confidential | public
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type OAuthClientSecret struct {
	ID         string     `json:"id"`
	AppID      string     `json:"app_id"`
	SecretHash string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// OAuthPendingRequest는 동의 화면에서 사용자의 결정을 기다리는 인가 요청이다(검증을 마친 파라미터만 저장한다).
type OAuthPendingRequest struct {
	ID            string
	RequestHash   string
	AppID         string
	UserID        string
	RedirectURI   string
	State         string
	Scopes        []string
	CodeChallenge string // 비어 있으면 PKCE 없이 시작한 요청(confidential 앱만 허용)
	Nonce         string
	ExpiresAt     time.Time
}

type OAuthAuthorizationCode struct {
	ID            string
	CodeHash      string
	AppID         string
	UserID        string
	GrantID       string // 자동 동의로 발급한 코드는 기존 승인에 토큰을 붙인다. 교환한 뒤에는 만든 승인을 가리킨다
	RedirectURI   string
	Scopes        []string
	ProjectIDs    []string
	CodeChallenge string
	Nonce         string
	ExpiresAt     time.Time
	UsedAt        *time.Time
}

type OAuthGrant struct {
	ID         string     `json:"id"`
	AppID      string     `json:"app_id"`
	UserID     string     `json:"user_id"`
	Scopes     []string   `json:"scopes"`
	ProjectIDs []string   `json:"project_ids"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type OAuthAccessToken struct {
	ID        string
	TokenHash string
	GrantID   string
	ExpiresAt time.Time
}

type OAuthRefreshToken struct {
	ID        string
	TokenHash string
	GrantID   string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// OAuthAuthorizedApp은 사용자의 "승인된 앱" 목록 항목이다.
type OAuthAuthorizedApp struct {
	GrantID     string     `json:"grant_id"`
	AppID       string     `json:"app_id"`
	ClientID    string     `json:"client_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Scopes      []string   `json:"scopes"`
	ProjectIDs  []string   `json:"project_ids"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

func hashEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func unmarshalList(raw string, out *[]string) error {
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return err
	}
	if *out == nil {
		*out = []string{}
	}
	return nil
}

// ---- apps ----

const oauthAppCols = `id, client_id, created_by, name, description, client_type, redirect_uris, created_at, updated_at`

func scanOAuthApp(r scanner) (*OAuthApp, error) {
	var a OAuthApp
	var redirects, created, updated string
	if err := r.Scan(&a.ID, &a.ClientID, &a.CreatedBy, &a.Name, &a.Description, &a.ClientType, &redirects, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	if err := unmarshalList(redirects, &a.RedirectURIs); err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = parseTS(created), parseTS(updated)
	return &a, nil
}

func (s *Store) CreateOAuthApp(ctx context.Context, a *OAuthApp) error {
	now := time.Now().UTC()
	a.ID, a.CreatedAt, a.UpdatedAt = NewID("oapp"), now, now
	_, err := s.q.ExecContext(ctx, `INSERT INTO oauth_apps (`+oauthAppCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ClientID, a.CreatedBy, a.Name, a.Description, a.ClientType, jsonStr(a.RedirectURIs), ts(now), ts(now))
	return conflict(err)
}

func (s *Store) GetOAuthApp(ctx context.Context, id string) (*OAuthApp, error) {
	return scanOAuthApp(s.q.QueryRowContext(ctx, `SELECT `+oauthAppCols+` FROM oauth_apps WHERE id=?`, id))
}

func (s *Store) GetOAuthAppByClientID(ctx context.Context, clientID string) (*OAuthApp, error) {
	return scanOAuthApp(s.q.QueryRowContext(ctx, `SELECT `+oauthAppCols+` FROM oauth_apps WHERE client_id=?`, clientID))
}

func (s *Store) ListOAuthApps(ctx context.Context) ([]*OAuthApp, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+oauthAppCols+` FROM oauth_apps ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OAuthApp{}
	for rows.Next() {
		a, err := scanOAuthApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateOAuthApp은 이름, 설명, redirect URI만 바꾼다(client_id와 앱 유형은 바꿀 수 없다).
func (s *Store) UpdateOAuthApp(ctx context.Context, a *OAuthApp) error {
	a.UpdatedAt = time.Now().UTC()
	return affected(s.q.ExecContext(ctx, `UPDATE oauth_apps SET name=?, description=?, redirect_uris=?, updated_at=? WHERE id=?`,
		a.Name, a.Description, jsonStr(a.RedirectURIs), ts(a.UpdatedAt), a.ID))
}

// DeleteOAuthApp은 앱과 거기서 발급한 모든 코드, 승인, 토큰을 지운다(외래 키 CASCADE).
func (s *Store) DeleteOAuthApp(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM oauth_apps WHERE id=?`, id))
}

// ---- client secrets ----

const oauthSecretCols = `id, app_id, secret_hash, created_at, revoked_at`

func scanOAuthClientSecret(r scanner) (*OAuthClientSecret, error) {
	var v OAuthClientSecret
	var created string
	var revoked sql.NullString
	if err := r.Scan(&v.ID, &v.AppID, &v.SecretHash, &created, &revoked); err != nil {
		return nil, notFound(err)
	}
	v.CreatedAt, v.RevokedAt = parseTS(created), parseNullTS(revoked)
	return &v, nil
}

func (s *Store) CreateOAuthClientSecret(ctx context.Context, v *OAuthClientSecret) error {
	if v.ID == "" {
		v.ID = NewID("osec")
	}
	v.CreatedAt, v.RevokedAt = time.Now().UTC(), nil
	_, err := s.q.ExecContext(ctx, `INSERT INTO oauth_client_secrets (`+oauthSecretCols+`) VALUES (?,?,?,?,NULL)`,
		v.ID, v.AppID, v.SecretHash, ts(v.CreatedAt))
	return err
}

// GetOAuthClientSecret은 폐기되지 않은 secret만 돌려준다.
func (s *Store) GetOAuthClientSecret(ctx context.Context, id string) (*OAuthClientSecret, error) {
	return scanOAuthClientSecret(s.q.QueryRowContext(ctx, `SELECT `+oauthSecretCols+` FROM oauth_client_secrets WHERE id=? AND revoked_at IS NULL`, id))
}

func (s *Store) ListOAuthClientSecrets(ctx context.Context, appID string) ([]*OAuthClientSecret, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+oauthSecretCols+` FROM oauth_client_secrets WHERE app_id=? ORDER BY created_at DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OAuthClientSecret{}
	for rows.Next() {
		v, err := scanOAuthClientSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RevokeOAuthClientSecret(ctx context.Context, appID, secretID string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE oauth_client_secrets SET revoked_at=? WHERE app_id=? AND id=? AND revoked_at IS NULL`,
		ts(time.Now()), appID, secretID))
}

// ---- pending authorization requests ----

const oauthPendingCols = `id, request_hash, app_id, user_id, redirect_uri, state, scopes, code_challenge, nonce, expires_at`

func scanOAuthPending(r scanner) (*OAuthPendingRequest, error) {
	var v OAuthPendingRequest
	var scopes, exp string
	if err := r.Scan(&v.ID, &v.RequestHash, &v.AppID, &v.UserID, &v.RedirectURI, &v.State, &scopes, &v.CodeChallenge, &v.Nonce, &exp); err != nil {
		return nil, notFound(err)
	}
	if err := unmarshalList(scopes, &v.Scopes); err != nil {
		return nil, err
	}
	v.ExpiresAt = parseTS(exp)
	return &v, nil
}

func (s *Store) CreateOAuthPendingRequest(ctx context.Context, v *OAuthPendingRequest) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO oauth_pending_requests (`+oauthPendingCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.RequestHash, v.AppID, v.UserID, v.RedirectURI, v.State, jsonStr(v.Scopes), v.CodeChallenge, v.Nonce, ts(v.ExpiresAt))
	return err
}

// GetOAuthPendingRequest는 해시가 맞고 만료되지 않은, userID의 요청만 돌려준다.
func (s *Store) GetOAuthPendingRequest(ctx context.Context, id, requestHash, userID string) (*OAuthPendingRequest, error) {
	p, err := scanOAuthPending(s.q.QueryRowContext(ctx, `SELECT `+oauthPendingCols+` FROM oauth_pending_requests WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if !hashEqual(p.RequestHash, requestHash) || p.UserID != userID || !time.Now().Before(p.ExpiresAt) {
		return nil, ErrNotFound
	}
	return p, nil
}

// ConsumeOAuthPendingRequest는 요청을 한 번만 꺼낸다(거부 또는 승인 처리). 동시에 두 번 처리되지 않는다.
func (s *Store) ConsumeOAuthPendingRequest(ctx context.Context, id, requestHash, userID string) (*OAuthPendingRequest, error) {
	var out *OAuthPendingRequest
	err := s.Tx(ctx, func(tx *Store) error {
		p, err := tx.GetOAuthPendingRequest(ctx, id, requestHash, userID)
		if err != nil {
			return err
		}
		if err := affected(tx.q.ExecContext(ctx, `DELETE FROM oauth_pending_requests WHERE id=?`, id)); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// ---- authorization codes ----

const oauthCodeCols = `id, code_hash, app_id, user_id, grant_id, redirect_uri, scopes, project_ids, code_challenge, nonce, expires_at, used_at`

func scanOAuthCode(r scanner) (*OAuthAuthorizationCode, error) {
	var v OAuthAuthorizationCode
	var grantID, used sql.NullString
	var scopes, projectIDs, exp string
	if err := r.Scan(&v.ID, &v.CodeHash, &v.AppID, &v.UserID, &grantID, &v.RedirectURI, &scopes, &projectIDs, &v.CodeChallenge, &v.Nonce, &exp, &used); err != nil {
		return nil, notFound(err)
	}
	if err := unmarshalList(scopes, &v.Scopes); err != nil {
		return nil, err
	}
	if err := unmarshalList(projectIDs, &v.ProjectIDs); err != nil {
		return nil, err
	}
	v.GrantID, v.ExpiresAt, v.UsedAt = grantID.String, parseTS(exp), parseNullTS(used)
	return &v, nil
}

func (s *Store) CreateOAuthAuthorizationCode(ctx context.Context, c *OAuthAuthorizationCode) error {
	var grantID any
	if c.GrantID != "" {
		grantID = c.GrantID
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO oauth_authorization_codes (`+oauthCodeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)`,
		c.ID, c.CodeHash, c.AppID, c.UserID, grantID, c.RedirectURI, jsonStr(c.Scopes), jsonStr(c.ProjectIDs), c.CodeChallenge, c.Nonce, ts(c.ExpiresAt))
	return err
}

func (s *Store) GetOAuthAuthorizationCode(ctx context.Context, id string) (*OAuthAuthorizationCode, error) {
	return scanOAuthCode(s.q.QueryRowContext(ctx, `SELECT `+oauthCodeCols+` FROM oauth_authorization_codes WHERE id=?`, id))
}

// IssueOAuthAuthorizationCode는 동의한 요청을 꺼내고 그 파라미터로 인가 코드를 만든다(한 트랜잭션).
// code에는 ID, CodeHash, ExpiresAt만 채워 넘기면 된다.
func (s *Store) IssueOAuthAuthorizationCode(ctx context.Context, pendingID, requestHash, userID string, scopes, projectIDs []string, code *OAuthAuthorizationCode) (*OAuthPendingRequest, error) {
	var out *OAuthPendingRequest
	err := s.Tx(ctx, func(tx *Store) error {
		p, err := tx.ConsumeOAuthPendingRequest(ctx, pendingID, requestHash, userID)
		if err != nil {
			return err
		}
		code.AppID, code.UserID, code.GrantID = p.AppID, p.UserID, ""
		code.RedirectURI, code.Scopes, code.ProjectIDs = p.RedirectURI, scopes, projectIDs
		code.CodeChallenge, code.Nonce = p.CodeChallenge, p.Nonce
		out = p
		return tx.CreateOAuthAuthorizationCode(ctx, code)
	})
	return out, err
}

// ExchangeOAuthAuthorizationCode는 인가 코드를 한 번만 토큰으로 바꾼다.
// 코드 해시, 앱, redirect URI가 맞아야 하고 verify(PKCE 검사)가 true여야 한다. 실패하면 ErrNotFound.
// 이미 쓴 코드를 다시 내면 그 코드로 만든 승인을 폐기하고 ErrOAuthCodeReplay를 돌려준다.
// verify가 실패하면 코드를 버린다(같은 코드로 다시 추측할 수 없게).
func (s *Store) ExchangeOAuthAuthorizationCode(ctx context.Context, codeID, codeHash, appID, redirectURI string,
	verify func(*OAuthAuthorizationCode) bool, access *OAuthAccessToken, refresh *OAuthRefreshToken) (*OAuthAuthorizationCode, *OAuthGrant, error) {
	var code *OAuthAuthorizationCode
	var grant *OAuthGrant
	var outcome error
	err := s.Tx(ctx, func(tx *Store) error {
		var err error
		code, err = tx.GetOAuthAuthorizationCode(ctx, codeID)
		if err != nil {
			return err
		}
		if !hashEqual(code.CodeHash, codeHash) || code.AppID != appID {
			return ErrNotFound
		}
		now := time.Now().UTC()
		if code.UsedAt != nil {
			if code.GrantID != "" {
				if err := tx.revokeGrant(ctx, code.GrantID, now); err != nil {
					return err
				}
			}
			outcome = ErrOAuthCodeReplay // 폐기는 커밋해야 하므로 트랜잭션은 성공으로 끝낸다
			return nil
		}
		if err := affected(tx.q.ExecContext(ctx, `UPDATE oauth_authorization_codes SET used_at=? WHERE id=? AND used_at IS NULL`, ts(now), codeID)); err != nil {
			return err
		}
		if !now.Before(code.ExpiresAt) || code.RedirectURI != redirectURI || !verify(code) {
			outcome = ErrNotFound // 코드는 쓴 것으로 남긴다
			return nil
		}
		if code.GrantID != "" {
			// 자동 동의: 기존 승인에 토큰만 붙인다(승인이 그사이 폐기되었으면 실패).
			grant, err = tx.activeGrantByID(ctx, code.GrantID)
			if err != nil {
				outcome = ErrNotFound
				return nil
			}
		} else {
			grant, err = tx.upsertGrant(ctx, appID, code.UserID, code.Scopes, code.ProjectIDs, now)
			if err != nil {
				return err
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE oauth_authorization_codes SET grant_id=? WHERE id=?`, grant.ID, codeID); err != nil {
				return err
			}
			code.GrantID = grant.ID
		}
		if err := tx.insertAccessToken(ctx, grant.ID, access, now); err != nil {
			return err
		}
		if refresh != nil && slices.Contains(code.Scopes, "offline_access") {
			if err := tx.insertRefreshToken(ctx, grant.ID, refresh, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if outcome != nil {
		return nil, nil, outcome
	}
	return code, grant, nil
}

// ---- grants ----

const oauthGrantCols = `id, app_id, user_id, scopes, project_ids, created_at, updated_at, last_used_at, revoked_at`

func scanOAuthGrant(r scanner) (*OAuthGrant, error) {
	var g OAuthGrant
	var scopes, projectIDs, created, updated string
	var used, revoked sql.NullString
	if err := r.Scan(&g.ID, &g.AppID, &g.UserID, &scopes, &projectIDs, &created, &updated, &used, &revoked); err != nil {
		return nil, notFound(err)
	}
	if err := unmarshalList(scopes, &g.Scopes); err != nil {
		return nil, err
	}
	if err := unmarshalList(projectIDs, &g.ProjectIDs); err != nil {
		return nil, err
	}
	g.CreatedAt, g.UpdatedAt, g.LastUsedAt, g.RevokedAt = parseTS(created), parseTS(updated), parseNullTS(used), parseNullTS(revoked)
	return &g, nil
}

// ActiveOAuthGrant는 사용자가 앱에 준 활성 승인이다(자동 동의 판단용).
func (s *Store) ActiveOAuthGrant(ctx context.Context, appID, userID string) (*OAuthGrant, error) {
	return scanOAuthGrant(s.q.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE app_id=? AND user_id=? AND revoked_at IS NULL`, appID, userID))
}

func (s *Store) activeGrantByID(ctx context.Context, id string) (*OAuthGrant, error) {
	return scanOAuthGrant(s.q.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE id=? AND revoked_at IS NULL`, id))
}

// upsertGrant는 새 동의를 기록한다. 활성 승인이 있으면 그 scope와 프로젝트를 바꾸고(기존 토큰도 새 범위를 따른다), 없으면 만든다.
func (s *Store) upsertGrant(ctx context.Context, appID, userID string, scopes, projectIDs []string, now time.Time) (*OAuthGrant, error) {
	g, err := s.ActiveOAuthGrant(ctx, appID, userID)
	switch {
	case err == nil:
		g.Scopes, g.ProjectIDs, g.UpdatedAt = scopes, projectIDs, now
		_, err = s.q.ExecContext(ctx, `UPDATE oauth_grants SET scopes=?, project_ids=?, updated_at=? WHERE id=?`, jsonStr(scopes), jsonStr(projectIDs), ts(now), g.ID)
		return g, err
	case errors.Is(err, ErrNotFound):
		g = &OAuthGrant{ID: NewID("ogrt"), AppID: appID, UserID: userID, Scopes: scopes, ProjectIDs: projectIDs, CreatedAt: now, UpdatedAt: now}
		_, err = s.q.ExecContext(ctx, `INSERT INTO oauth_grants (`+oauthGrantCols+`) VALUES (?,?,?,?,?,?,?,NULL,NULL)`,
			g.ID, appID, userID, jsonStr(scopes), jsonStr(projectIDs), ts(now), ts(now))
		return g, conflict(err)
	}
	return nil, err
}

// revokeGrant는 승인을 폐기하고 그 토큰을 지운다(access token은 바로 삭제, refresh token은 폐기 표시).
func (s *Store) revokeGrant(ctx context.Context, grantID string, now time.Time) error {
	if _, err := s.q.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, ts(now), grantID); err != nil {
		return err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM oauth_access_tokens WHERE grant_id=?`, grantID); err != nil {
		return err
	}
	_, err := s.q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at=? WHERE grant_id=? AND revoked_at IS NULL`, ts(now), grantID)
	return err
}

func (s *Store) ListOAuthAuthorizedApps(ctx context.Context, userID string) ([]*OAuthAuthorizedApp, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT g.id, a.id, a.client_id, a.name, a.description, g.scopes, g.project_ids, g.created_at, g.updated_at, g.last_used_at
		FROM oauth_grants g JOIN oauth_apps a ON a.id=g.app_id WHERE g.user_id=? AND g.revoked_at IS NULL ORDER BY g.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OAuthAuthorizedApp{}
	for rows.Next() {
		var v OAuthAuthorizedApp
		var scopes, projects, created, updated string
		var used sql.NullString
		if err := rows.Scan(&v.GrantID, &v.AppID, &v.ClientID, &v.Name, &v.Description, &scopes, &projects, &created, &updated, &used); err != nil {
			return nil, err
		}
		if err := unmarshalList(scopes, &v.Scopes); err != nil {
			return nil, err
		}
		if err := unmarshalList(projects, &v.ProjectIDs); err != nil {
			return nil, err
		}
		v.CreatedAt, v.UpdatedAt, v.LastUsedAt = parseTS(created), parseTS(updated), parseNullTS(used)
		out = append(out, &v)
	}
	return out, rows.Err()
}

// GetUserOAuthGrant는 userID의 활성 승인 하나를 돌려준다(남의 승인은 ErrNotFound).
func (s *Store) GetUserOAuthGrant(ctx context.Context, userID, grantID string) (*OAuthGrant, error) {
	return scanOAuthGrant(s.q.QueryRowContext(ctx, `SELECT `+oauthGrantCols+` FROM oauth_grants WHERE id=? AND user_id=? AND revoked_at IS NULL`, grantID, userID))
}

// SetOAuthGrantProjects는 승인된 앱에 공유한 프로젝트 목록을 바꾼다.
func (s *Store) SetOAuthGrantProjects(ctx context.Context, userID, grantID string, projectIDs []string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE oauth_grants SET project_ids=?, updated_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL`,
		jsonStr(projectIDs), ts(time.Now()), grantID, userID))
}

// RevokeOAuthGrant는 사용자가 "승인된 앱"에서 접근을 폐기한다. 그 승인의 모든 토큰이 즉시 무효가 된다.
func (s *Store) RevokeOAuthGrant(ctx context.Context, userID, grantID string) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.GetUserOAuthGrant(ctx, userID, grantID); err != nil {
			return err
		}
		return tx.revokeGrant(ctx, grantID, time.Now())
	})
}

// TouchOAuthGrant는 승인의 마지막 사용 시각을 기록한다.
func (s *Store) TouchOAuthGrant(ctx context.Context, grantID string) {
	s.q.ExecContext(ctx, `UPDATE oauth_grants SET last_used_at=? WHERE id=?`, ts(time.Now()), grantID)
}

// ---- tokens ----

func (s *Store) insertAccessToken(ctx context.Context, grantID string, t *OAuthAccessToken, now time.Time) error {
	t.GrantID = grantID
	_, err := s.q.ExecContext(ctx, `INSERT INTO oauth_access_tokens (id, token_hash, grant_id, created_at, expires_at) VALUES (?,?,?,?,?)`,
		t.ID, t.TokenHash, grantID, ts(now), ts(t.ExpiresAt))
	return err
}

// insertRefreshToken은 refresh token을 추가하고, 승인당 OAuthMaxRefreshTokens개를 넘으면 가장 오래된 것을 폐기한다.
func (s *Store) insertRefreshToken(ctx context.Context, grantID string, t *OAuthRefreshToken, now time.Time) error {
	t.GrantID, t.CreatedAt = grantID, now
	if _, err := s.q.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens (id, token_hash, grant_id, created_at, expires_at, revoked_at) VALUES (?,?,?,?,?,NULL)`,
		t.ID, t.TokenHash, grantID, ts(now), ts(t.ExpiresAt)); err != nil {
		return err
	}
	_, err := s.q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at=? WHERE id IN (
			SELECT id FROM oauth_refresh_tokens WHERE grant_id=? AND revoked_at IS NULL ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)`,
		ts(now), grantID, OAuthMaxRefreshTokens)
	return err
}

// RotateOAuthRefreshToken은 refresh token을 한 번만 쓰고 새 access/refresh token으로 바꾼다.
// allow가 false면 아무것도 바꾸지 않는다. 이미 바뀐(폐기된) 토큰, 만료된 토큰, 다른 앱의 토큰, 폐기된 승인의 토큰은 ErrNotFound다.
func (s *Store) RotateOAuthRefreshToken(ctx context.Context, tokenID, tokenHash, appID string, allow func(*OAuthGrant) bool, access *OAuthAccessToken, refresh *OAuthRefreshToken) (*OAuthGrant, error) {
	var grant *OAuthGrant
	err := s.Tx(ctx, func(tx *Store) error {
		var storedHash, grantID, exp string
		var revoked sql.NullString
		err := tx.q.QueryRowContext(ctx, `SELECT token_hash, grant_id, expires_at, revoked_at FROM oauth_refresh_tokens WHERE id=?`, tokenID).
			Scan(&storedHash, &grantID, &exp, &revoked)
		if err != nil {
			return notFound(err)
		}
		now := time.Now().UTC()
		if !hashEqual(storedHash, tokenHash) || revoked.Valid || !now.Before(parseTS(exp)) {
			return ErrNotFound
		}
		g, err := tx.activeGrantByID(ctx, grantID)
		if err != nil {
			return err
		}
		if g.AppID != appID || !allow(g) {
			return ErrNotFound
		}
		if err := affected(tx.q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, ts(now), tokenID)); err != nil {
			return err
		}
		if err := tx.insertAccessToken(ctx, g.ID, access, now); err != nil {
			return err
		}
		if err := tx.insertRefreshToken(ctx, g.ID, refresh, now); err != nil {
			return err
		}
		grant = g
		return nil
	})
	return grant, err
}

// GetOAuthAccessToken은 만료되지 않은 access token과 그 활성 승인을 돌려준다(해시 비교는 호출자가 한다).
func (s *Store) GetOAuthAccessToken(ctx context.Context, tokenID string) (*OAuthAccessToken, *OAuthGrant, error) {
	var t OAuthAccessToken
	var exp string
	err := s.q.QueryRowContext(ctx, `SELECT id, token_hash, grant_id, expires_at FROM oauth_access_tokens WHERE id=? AND expires_at>?`, tokenID, ts(time.Now())).
		Scan(&t.ID, &t.TokenHash, &t.GrantID, &exp)
	if err != nil {
		return nil, nil, notFound(err)
	}
	t.ExpiresAt = parseTS(exp)
	g, err := s.activeGrantByID(ctx, t.GrantID)
	if err != nil {
		return nil, nil, err
	}
	return &t, g, nil
}

// RevokeOAuthToken은 RFC 7009 토큰 폐기다. 모르는 토큰, 다른 앱의 토큰은 조용히 무시한다.
// refresh token을 폐기하면 같은 승인의 access token도 함께 지운다. access token은 그 토큰만 지운다.
func (s *Store) RevokeOAuthToken(ctx context.Context, tokenID, tokenHash, appID string, refresh bool) error {
	return s.Tx(ctx, func(tx *Store) error {
		table := "oauth_access_tokens"
		if refresh {
			table = "oauth_refresh_tokens"
		}
		var storedHash, grantID, storedAppID string
		err := tx.q.QueryRowContext(ctx, `SELECT t.token_hash, t.grant_id, g.app_id FROM `+table+` t JOIN oauth_grants g ON g.id=t.grant_id WHERE t.id=?`, tokenID).
			Scan(&storedHash, &grantID, &storedAppID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if storedAppID != appID || !hashEqual(storedHash, tokenHash) {
			return nil
		}
		if !refresh {
			_, err = tx.q.ExecContext(ctx, `DELETE FROM oauth_access_tokens WHERE id=?`, tokenID)
			return err
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, ts(time.Now()), tokenID); err != nil {
			return err
		}
		_, err = tx.q.ExecContext(ctx, `DELETE FROM oauth_access_tokens WHERE grant_id=?`, grantID)
		return err
	})
}

// PruneOAuth는 만료된 인가 요청, 코드, access token과 오래전에 끝난 refresh token, 승인을 지운다.
func (s *Store) PruneOAuth(ctx context.Context) error {
	now := time.Now()
	nowTS, old := ts(now), ts(now.Add(-30*24*time.Hour))
	// 쓴 코드는 재사용 감지를 위해 만료 뒤 하루 더 남긴다.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM oauth_pending_requests WHERE expires_at <= ?`, []any{nowTS}},
		{`DELETE FROM oauth_authorization_codes WHERE expires_at <= ?`, []any{ts(now.Add(-24 * time.Hour))}},
		{`DELETE FROM oauth_access_tokens WHERE expires_at <= ?`, []any{nowTS}},
		{`DELETE FROM oauth_refresh_tokens WHERE expires_at <= ? OR revoked_at <= ?`, []any{nowTS, old}},
		{`DELETE FROM oauth_grants WHERE revoked_at <= ?`, []any{old}},
	} {
		if _, err := s.q.ExecContext(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	return nil
}
