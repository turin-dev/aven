package store

import (
	"context"
	"errors"
	"time"
)

// ErrOwnership은 소유권 이전이 owner 규칙(활성 대상, 활성 owner 1명 이상)을 어길 때다.
var ErrOwnership = errors.New("소유권을 넘길 수 없습니다")

// ---------------- 서버 owner 전용 관리(api/owner.go) ----------------

// SessionWithUser는 owner의 전체 세션 목록 항목이다(토큰 해시는 내보내지 않는다).
type SessionWithUser struct {
	Session
	Email    string `json:"email"`
	Name     string `json:"name"`
	UserRole string `json:"user_role"`
	Builtin  bool   `json:"builtin"`
	Disabled bool   `json:"disabled"`
}

// ListAllSessions는 만료되지 않은 모든 사용자의 세션이다(최근 사용 순).
func (s *Store) ListAllSessions(ctx context.Context) ([]*SessionWithUser, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+prefixCols("se.", sessionCols)+`, u.email, u.name, u.role, u.builtin, u.disabled
		FROM sessions se JOIN users u ON u.id=se.user_id WHERE se.expires_at > ? ORDER BY se.last_seen DESC`, ts(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionWithUser{}
	for rows.Next() {
		var se SessionWithUser
		var created, exp, seen string
		if err := rows.Scan(&se.ID, &se.UserID, &se.AuthTag, &created, &exp, &seen, &se.IP, &se.UserAgent, &se.SAMLAuthenticated,
			&se.Email, &se.Name, &se.UserRole, &se.Builtin, &se.Disabled); err != nil {
			return nil, err
		}
		se.CreatedAt, se.ExpiresAt, se.LastSeen = parseTS(created), parseTS(exp), parseTS(seen)
		out = append(out, &se)
	}
	return out, rows.Err()
}

// DeleteSessionByID는 누구의 것이든 세션 하나를 지운다.
func (s *Store) DeleteSessionByID(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id))
}

// DeleteSessionsExceptOwners는 owner가 아닌(builtin 포함 owner는 남긴다) 모든 사용자의 세션을 지우고 지운 개수를 돌려준다.
// keep에 있는 사용자(KEEL_SUPERADMINS 같은 환경변수 owner)의 세션도 남긴다.
func (s *Store) DeleteSessionsExceptOwners(ctx context.Context, keep []string) (int64, error) {
	q, args := `DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE role<>? AND builtin=0`, []any{RoleOwner}
	q, args = notInIDs(q, args, keep)
	res, err := s.q.ExecContext(ctx, q+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAPITokensExceptOwners는 owner가 아닌 사용자의 API 토큰을 모두 지우고 지운 개수를 돌려준다.
func (s *Store) DeleteAPITokensExceptOwners(ctx context.Context, keep []string) (int64, error) {
	q, args := `DELETE FROM api_tokens WHERE user_id IN (SELECT id FROM users WHERE role<>? AND builtin=0`, []any{RoleOwner}
	q, args = notInIDs(q, args, keep)
	res, err := s.q.ExecContext(ctx, q+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountUserAPITokens는 사용자의 API 토큰 수다(userID가 비면 전체).
func (s *Store) CountUserAPITokens(ctx context.Context, userID string) (int, error) {
	var n int
	var err error
	if userID == "" {
		err = s.q.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens`).Scan(&n)
	} else {
		err = s.q.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens WHERE user_id=?`, userID).Scan(&n)
	}
	return n, err
}

func notInIDs(q string, args []any, ids []string) (string, []any) {
	for _, id := range ids {
		q += ` AND id<>?`
		args = append(args, id)
	}
	return q, args
}

// ListOwners는 users.role=owner인 계정이다(비활성 포함, 기본 계정 먼저).
func (s *Store) ListOwners(ctx context.Context) ([]*User, error) {
	all, err := s.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := []*User{}
	for _, u := range all {
		if u.Role == RoleOwner || u.Builtin {
			out = append(out, u)
		}
	}
	return out, nil
}

// TransferOwnership은 to를 owner로 올리고, demoteFrom이 비어 있지 않으면 그 계정을 admin으로 내린다(한 트랜잭션).
// 기본 계정(builtin)은 항상 owner라 내리지 않는다. 결과적으로 활성 owner가 없으면 되돌린다.
func (s *Store) TransferOwnership(ctx context.Context, to, demoteFrom string) error {
	return s.Tx(ctx, func(tx *Store) error {
		target, err := tx.GetUser(ctx, to)
		if err != nil {
			return err
		}
		if target.Disabled {
			return ErrOwnership
		}
		if _, err := tx.q.ExecContext(ctx, `UPDATE users SET role=? WHERE id=?`, RoleOwner, to); err != nil {
			return err
		}
		if demoteFrom != "" && demoteFrom != to {
			if _, err := tx.q.ExecContext(ctx, `UPDATE users SET role=? WHERE id=? AND builtin=0`, RoleAdmin, demoteFrom); err != nil {
				return err
			}
		}
		n, err := tx.CountActiveOwners(ctx)
		if err != nil {
			return err
		}
		if n < 1 {
			return ErrOwnership
		}
		return nil
	})
}

// BackupTo는 실행 중인 SQLite DB를 path에 일관된 사본으로 쓴다(VACUUM INTO, 쓰기를 막지 않는다).
func (s *Store) BackupTo(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
