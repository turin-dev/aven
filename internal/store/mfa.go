package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// migrateMFA는 패스키(WebAuthn credential)와 한 번만 쓰는 WebAuthn 요청(challenge) 테이블을 만든다.
func migrateMFA(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE passkeys (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			rp_id TEXT NOT NULL,
			credential_id TEXT NOT NULL,
			name TEXT NOT NULL,
			credential_data TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_used_at TEXT,
			UNIQUE (rp_id, credential_id)
		)`,
		`CREATE INDEX passkeys_user ON passkeys(user_id, created_at)`,
		`CREATE TABLE webauthn_challenges (
			challenge_hash TEXT PRIMARY KEY,
			user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
			session_id TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			origin TEXT NOT NULL,
			rp_id TEXT NOT NULL,
			session_data TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX webauthn_challenges_expiry ON webauthn_challenges(expires_at)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

const workspace2FASetting = "workspace_require_2fa"

// Workspace2FAEnforced는 작업 공간이 모든 멤버에게 2단계 인증을 요구하는지다(기본 꺼짐).
func (s *Store) Workspace2FAEnforced(ctx context.Context) (bool, error) {
	v, err := s.GetSetting(ctx, workspace2FASetting)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return v == "1", err
}

func (s *Store) SetWorkspace2FAEnforced(ctx context.Context, required bool) error {
	v := "0"
	if required {
		v = "1"
	}
	return s.SetSetting(ctx, workspace2FASetting, v)
}

// Passkey는 계정에 등록된 패스키다. CredentialJSON(공개 키, 서명 카운터)은 내부에서만 쓰고 암호화해서 저장한다.
type Passkey struct {
	ID             string     `json:"id"`
	UserID         string     `json:"-"`
	RPID           string     `json:"rp_id"`
	Name           string     `json:"name"`
	CredentialID   string     `json:"-"`
	CredentialJSON string     `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at"`
}

func scanPasskey(r scanner, s *Store, withCredential bool) (*Passkey, error) {
	var p Passkey
	var created string
	var last sql.NullString
	if withCredential {
		if err := r.Scan(&p.ID, &p.UserID, &p.RPID, &p.Name, &p.CredentialID, &p.CredentialJSON, &created, &last); err != nil {
			return nil, notFound(err)
		}
		v, err := s.Unseal(p.CredentialJSON)
		if err != nil {
			return nil, err
		}
		p.CredentialJSON = v
	} else if err := r.Scan(&p.ID, &p.UserID, &p.RPID, &p.Name, &created, &last); err != nil {
		return nil, notFound(err)
	}
	p.CreatedAt, p.LastUsedAt = parseTS(created), parseNullTS(last)
	return &p, nil
}

func (s *Store) CountPasskeys(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

// PasskeyCounts는 사용자별 passkey 수다(작업 공간 사용자 목록용).
func (s *Store) PasskeyCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT user_id, COUNT(*) FROM passkeys GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Store) ListPasskeys(ctx context.Context, userID string) ([]*Passkey, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id,user_id,rp_id,name,created_at,last_used_at FROM passkeys WHERE user_id=? ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Passkey{}
	for rows.Next() {
		p, err := scanPasskey(rows, s, false)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PasskeyRecords(ctx context.Context, userID, rpID string) ([]*Passkey, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id,user_id,rp_id,name,credential_id,credential_data,created_at,last_used_at FROM passkeys WHERE user_id=? AND rp_id=? ORDER BY created_at,id`, userID, rpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Passkey{}
	for rows.Next() {
		p, err := scanPasskey(rows, s, true)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PasskeyByCredentialID(ctx context.Context, rpID, credentialID string) (*Passkey, error) {
	return scanPasskey(s.q.QueryRowContext(ctx, `SELECT id,user_id,rp_id,name,credential_id,credential_data,created_at,last_used_at FROM passkeys WHERE rp_id=? AND credential_id=?`, rpID, credentialID), s, true)
}

func (s *Store) CreatePasskey(ctx context.Context, p *Passkey) error {
	p.ID, p.CreatedAt = NewID("pk"), time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO passkeys (id,user_id,rp_id,credential_id,name,credential_data,created_at,last_used_at) VALUES (?,?,?,?,?,?,?,NULL)`,
		p.ID, p.UserID, p.RPID, p.CredentialID, p.Name, s.Seal(p.CredentialJSON), ts(p.CreatedAt))
	return conflict(err)
}

func (s *Store) UpdatePasskeyCredential(ctx context.Context, userID, id, credentialJSON string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE passkeys SET credential_data=?,last_used_at=? WHERE id=? AND user_id=?`, s.Seal(credentialJSON), ts(time.Now().UTC()), id, userID))
}

func (s *Store) DeletePasskey(ctx context.Context, userID, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM passkeys WHERE id=? AND user_id=?`, id, userID))
}

// ClearUserTOTP는 TOTP만 끄고 복구 코드는 남긴다(패스키와 함께 쓰는 수단이다).
func (s *Store) ClearUserTOTP(ctx context.Context, userID string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE users SET totp_secret='', totp_enabled=0, totp_last_step=0 WHERE id=?`, userID))
}

// ResetUserMFA는 사용자의 두 번째 인증 수단(TOTP, 패스키, 복구 코드, 진행 중인 WebAuthn 요청)을 모두 지운다.
func (s *Store) ResetUserMFA(ctx context.Context, userID string) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := affected(tx.q.ExecContext(ctx, `UPDATE users SET totp_secret='', totp_enabled=0, totp_last_step=0 WHERE id=?`, userID)); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM passkeys WHERE user_id=?`,
			`DELETE FROM recovery_codes WHERE user_id=?`,
			`DELETE FROM webauthn_challenges WHERE user_id=?`,
		} {
			if _, err := tx.q.ExecContext(ctx, q, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

// WebAuthnChallenge는 WebAuthn 라이브러리의 session data를 서버에 두고 origin/RP ID와 브라우저 세션에 묶는다.
type WebAuthnChallenge struct {
	UserID      string
	SessionID   string
	Purpose     string
	Name        string
	Origin      string
	RPID        string
	SessionData string
	ExpiresAt   time.Time
}

// MaxPendingWebAuthnChallenges는 동시에 진행할 수 있는 WebAuthn 요청 수다(만료 5분).
const MaxPendingWebAuthnChallenges = 10000

// ErrTooManyChallenges는 진행 중인 WebAuthn 요청이 너무 많을 때다.
var ErrTooManyChallenges = errors.New("too many pending webauthn challenges")

func (s *Store) CreateWebAuthnChallenge(ctx context.Context, challengeHash string, c *WebAuthnChallenge) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM webauthn_challenges WHERE expires_at<=?`, ts(time.Now().UTC())); err != nil {
			return err
		}
		// 인증 없이 시작할 수 있는 로그인 요청으로 테이블을 채우지 못하게 진행 중인 요청 수를 제한한다.
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM webauthn_challenges`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxPendingWebAuthnChallenges {
			return ErrTooManyChallenges
		}
		var userID any
		if c.UserID != "" {
			userID = c.UserID
		}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO webauthn_challenges (challenge_hash,user_id,session_id,purpose,name,origin,rp_id,session_data,expires_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			challengeHash, userID, c.SessionID, c.Purpose, c.Name, c.Origin, c.RPID, c.SessionData, ts(c.ExpiresAt), ts(time.Now().UTC()))
		return err
	})
}

// ConsumeWebAuthnChallenge는 challenge를 한 문장으로 꺼내며 지운다(동시에 두 번 써도 하나만 성공한다).
// 목적, 브라우저 세션, 만료가 맞지 않으면 ErrNotFound이고 그대로 둔다(만료된 것은 다음 생성 때 정리된다).
func (s *Store) ConsumeWebAuthnChallenge(ctx context.Context, challengeHash, purpose, sessionID string) (*WebAuthnChallenge, error) {
	var c WebAuthnChallenge
	var userID sql.NullString
	var expires string
	err := s.q.QueryRowContext(ctx, `DELETE FROM webauthn_challenges WHERE challenge_hash=? AND purpose=? AND session_id=? AND expires_at>?
		RETURNING user_id,session_id,purpose,name,origin,rp_id,session_data,expires_at`, challengeHash, purpose, sessionID, ts(time.Now().UTC())).
		Scan(&userID, &c.SessionID, &c.Purpose, &c.Name, &c.Origin, &c.RPID, &c.SessionData, &expires)
	if err != nil {
		return nil, notFound(err)
	}
	c.UserID, c.ExpiresAt = userID.String, parseTS(expires)
	return &c, nil
}
