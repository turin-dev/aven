package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ---------------- Users ----------------

// Role names are shared; users.role is an instance role, workspace_members.role is local.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// BuiltinUserID는 KEEL_ADMIN_PASSWORD로 로그인하는 기본 owner 계정이다(비밀번호는 환경변수가 원본이다).
const (
	BuiltinUserID    = "usr_admin"
	BuiltinUserEmail = "admin@local"
)

// ValidWorkspaceRole은 작업 공간 역할 이름인지 본다.
func ValidWorkspaceRole(r string) bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleMember || r == RoleViewer
}

// ValidProjectRole은 프로젝트(및 API 토큰) 역할 이름인지 본다.
func ValidProjectRole(r string) bool {
	return r == RoleAdmin || r == RoleMember || r == RoleViewer
}

type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	TOTPEnabled  bool       `json:"totp_enabled"`
	Builtin      bool       `json:"builtin"`
	Disabled     bool       `json:"disabled"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	PasswordHash string     `json:"-"`
	// TOTPSecret은 복호화된 base32 비밀이다(저장 시 secret box로 암호화). 활성화 전 설정 중인 비밀도 여기 있다.
	TOTPSecret   string `json:"-"`
	TOTPLastStep int64  `json:"-"`
}

// Display는 이벤트·감사 기록에 남기는 사용자 표시다.
func (u *User) Display() string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

const userCols = `id, email, name, password_hash, role, totp_secret, totp_enabled, totp_last_step, builtin, disabled, created_at, last_login_at`

func (s *Store) scanUser(r scanner) (*User, error) {
	var u User
	var secret, created string
	var last sql.NullString
	if err := r.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &secret, &u.TOTPEnabled, &u.TOTPLastStep, &u.Builtin, &u.Disabled, &created, &last); err != nil {
		return nil, notFound(err)
	}
	if secret != "" {
		v, err := s.Unseal(secret)
		if err != nil {
			return nil, err
		}
		u.TOTPSecret = v
	}
	u.CreatedAt, u.LastLoginAt = parseTS(created), parseNullTS(last)
	return &u, nil
}

// NormalizeEmail은 저장·조회에 쓰는 이메일 형식(앞뒤 공백 제거, 소문자)이다.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	u.ID, u.CreatedAt, u.Email = NewID("usr"), time.Now().UTC(), NormalizeEmail(u.Email)
	_, err := s.q.ExecContext(ctx, `INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.Role, s.sealOpt(u.TOTPSecret), u.TOTPEnabled, u.TOTPLastStep, false, u.Disabled, ts(u.CreatedAt), nullTS(u.LastLoginAt))
	return conflict(err)
}

func (s *Store) sealOpt(v string) string {
	if v == "" {
		return ""
	}
	return s.Seal(v)
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.q.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return s.scanUser(s.q.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email=?`, NormalizeEmail(email)))
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser는 이름, 역할, 비활성화, 비밀번호 해시를 저장한다(TOTP는 SetUserTOTP).
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	return s.Tx(ctx, func(tx *Store) error {
		if u.Disabled {
			if err := tx.CheckWorkspaceOwners(ctx, u.ID); err != nil {
				return err
			}
		}
		return affected(tx.q.ExecContext(ctx, `UPDATE users SET name=?, role=?, disabled=?, password_hash=? WHERE id=?`, u.Name, u.Role, u.Disabled, u.PasswordHash, u.ID))
	})
}

// SetUserTOTP는 TOTP 비밀과 활성 상태를 바꾼다. 비활성화(secret 비움)하면 복구 코드도 지운다.
func (s *Store) SetUserTOTP(ctx context.Context, id, secret string, enabled bool) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := affected(tx.q.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_enabled=?, totp_last_step=0 WHERE id=?`, tx.sealOpt(secret), enabled, id)); err != nil {
			return err
		}
		if secret == "" {
			_, err := tx.q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id=?`, id)
			return err
		}
		return nil
	})
}

// UseTOTPStep은 step이 마지막으로 쓴 step보다 클 때만 기록하고 true를 돌려준다(같은 코드 재사용 방지).
func (s *Store) UseTOTPStep(ctx context.Context, id string, step int64) (bool, error) {
	res, err := s.q.ExecContext(ctx, `UPDATE users SET totp_last_step=? WHERE id=? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) TouchUserLogin(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, ts(time.Now()), id)
	return err
}

// DeleteUser는 사용자와 그의 빈 개인 작업 공간을 지운다. 개인 작업 공간에 프로젝트가 남아 있으면 ErrWorkspaceNotEmpty다.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	if id == BuiltinUserID {
		return ErrNotFound
	}
	return s.Tx(ctx, func(tx *Store) error {
		if err := tx.CheckWorkspaceOwners(ctx, id); err != nil {
			return err
		}
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM projects p JOIN workspaces w ON w.id = p.workspace_id
			WHERE w.personal=1 AND w.owner_id=?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrWorkspaceNotEmpty
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM workspaces WHERE personal=1 AND owner_id=?`, id); err != nil {
			return err
		}
		return affected(tx.q.ExecContext(ctx, `DELETE FROM users WHERE id=? AND builtin=0`, id))
	})
}

// CountActiveOwners는 비활성화되지 않은 owner 수다(마지막 owner를 지우거나 강등하지 못하게).
func (s *Store) CountActiveOwners(ctx context.Context) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role=? AND disabled=0`, RoleOwner).Scan(&n)
	return n, err
}

// ---------------- Sessions ----------------

type Session struct {
	ID                string    `json:"id"`
	UserID            string    `json:"user_id"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	LastSeen          time.Time `json:"last_seen"`
	IP                string    `json:"ip"`
	UserAgent         string    `json:"user_agent"`
	SAMLAuthenticated bool      `json:"saml_authenticated"`
	// AuthTag는 기본 owner 계정 세션이 만들어질 때의 KEEL_ADMIN_PASSWORD 지문이다(바뀌면 세션이 무효가 된다).
	AuthTag string `json:"-"`
}

const sessionCols = `id, user_id, auth_tag, created_at, expires_at, last_seen, ip, user_agent, saml_authenticated`

func scanSession(r scanner) (*Session, error) {
	var se Session
	var created, exp, seen string
	if err := r.Scan(&se.ID, &se.UserID, &se.AuthTag, &created, &exp, &seen, &se.IP, &se.UserAgent, &se.SAMLAuthenticated); err != nil {
		return nil, notFound(err)
	}
	se.CreatedAt, se.ExpiresAt, se.LastSeen = parseTS(created), parseTS(exp), parseTS(seen)
	return &se, nil
}

// CreateSession은 토큰 해시(SHA-256 hex)로 세션을 저장한다. 원래 토큰은 저장하지 않는다.
func (s *Store) CreateSession(ctx context.Context, se *Session, tokenHash string) error {
	now := time.Now().UTC()
	se.ID, se.CreatedAt, se.LastSeen = NewID("ses"), now, now
	_, err := s.q.ExecContext(ctx, `INSERT INTO sessions (id, token_hash, user_id, auth_tag, created_at, expires_at, last_seen, ip, user_agent, saml_authenticated) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		se.ID, tokenHash, se.UserID, se.AuthTag, ts(se.CreatedAt), ts(se.ExpiresAt), ts(se.LastSeen), se.IP, se.UserAgent, se.SAMLAuthenticated)
	return err
}

// GetSessionByHash는 만료되지 않은 세션을 찾는다.
func (s *Store) GetSessionByHash(ctx context.Context, tokenHash string) (*Session, error) {
	return scanSession(s.q.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE token_hash=? AND expires_at > ?`, tokenHash, ts(time.Now())))
}

// TouchSession은 마지막 사용 시각과 (sliding) 만료 시각을 늦춘다.
func (s *Store) TouchSession(ctx context.Context, id string, seen, exp time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE sessions SET last_seen=?, expires_at=? WHERE id=?`, ts(seen), ts(exp), id)
	return err
}

func (s *Store) ListSessions(ctx context.Context, userID string) ([]*Session, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE user_id=? AND expires_at > ? ORDER BY last_seen DESC`, userID, ts(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		se, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteSession은 사용자 본인의 세션 하나를 지운다.
func (s *Store) DeleteSession(ctx context.Context, userID, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM sessions WHERE id=? AND user_id=?`, id, userID))
}

// DeleteSessionByHash는 로그아웃한 세션을 지운다.
func (s *Store) DeleteSessionByHash(ctx context.Context, tokenHash string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash))
}

// DeleteUserSessions는 사용자의 세션을 모두(exceptID 제외) 지운다.
func (s *Store) DeleteUserSessions(ctx context.Context, userID, exceptID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND id<>?`, userID, exceptID)
	return err
}

func (s *Store) PruneSessions(ctx context.Context) error {
	now := ts(time.Now())
	if _, err := s.q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	_, err := s.q.ExecContext(ctx, `DELETE FROM invites WHERE expires_at <= ?`, now)
	return err
}

// ---------------- Project members ----------------

type Member struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	// WorkspaceRole은 사용자의 작업 공간 역할이다(owner/admin은 멤버 여부와 관계없이 모든 프로젝트에 접근한다).
	WorkspaceRole string `json:"workspace_role"`
}

// SetMember는 멤버를 추가하거나 역할을 바꾼다.
func (s *Store) SetMember(ctx context.Context, projectID, userID, role string) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?,?,?,?)
		ON CONFLICT(project_id, user_id) DO UPDATE SET role=excluded.role`, projectID, userID, role, ts(time.Now()))
	return err
}

func (s *Store) RemoveMember(ctx context.Context, projectID, userID string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM project_members WHERE project_id=? AND user_id=?`, projectID, userID))
}

// MemberRole은 사용자의 프로젝트 역할이다(멤버가 아니면 "").
func (s *Store) MemberRole(ctx context.Context, projectID, userID string) (string, error) {
	var role string
	err := s.q.QueryRowContext(ctx, `SELECT role FROM project_members WHERE project_id=? AND user_id=?`, projectID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}

// UserProjectRoles는 사용자의 프로젝트 → 역할이다.
func (s *Store) UserProjectRoles(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT project_id, role FROM project_members WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, r string
		if err := rows.Scan(&p, &r); err != nil {
			return nil, err
		}
		out[p] = r
	}
	return out, rows.Err()
}

func (s *Store) ListMembers(ctx context.Context, projectID string) ([]*Member, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT m.project_id, m.user_id, m.role, m.created_at, u.email, u.name, COALESCE(wm.role, '')
		FROM project_members m JOIN users u ON u.id = m.user_id
		JOIN projects p ON p.id=m.project_id
		LEFT JOIN workspace_members wm ON wm.workspace_id=p.workspace_id AND wm.user_id=m.user_id WHERE m.project_id=? ORDER BY m.created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Member{}
	for rows.Next() {
		var m Member
		var created string
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Role, &created, &m.Email, &m.Name, &m.WorkspaceRole); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(created)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ---------------- Invites ----------------

type Invite struct {
	ID            string    `json:"id"`
	WorkspaceID   *string   `json:"workspace_id"`
	WorkspaceRole *string   `json:"workspace_role"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	ProjectID     *string   `json:"project_id"`
	ProjectRole   *string   `json:"project_role"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

const inviteCols = `id, email, role, project_id, project_role, created_by, created_at, expires_at, workspace_id, workspace_role`

func scanInvite(r scanner) (*Invite, error) {
	var in Invite
	var pid, prole, wsid, wsrole sql.NullString
	var created, exp string
	if err := r.Scan(&in.ID, &in.Email, &in.Role, &pid, &prole, &in.CreatedBy, &created, &exp, &wsid, &wsrole); err != nil {
		return nil, notFound(err)
	}
	in.ProjectID, in.ProjectRole = nullStr(pid), nullStr(prole)
	in.WorkspaceID, in.WorkspaceRole = nullStr(wsid), nullStr(wsrole)
	in.CreatedAt, in.ExpiresAt = parseTS(created), parseTS(exp)
	return &in, nil
}

func (s *Store) CreateInvite(ctx context.Context, in *Invite, tokenHash string) error {
	in.ID, in.CreatedAt, in.Email = NewID("inv"), time.Now().UTC(), NormalizeEmail(in.Email)
	_, err := s.q.ExecContext(ctx, `INSERT INTO invites (id, token_hash, email, role, project_id, project_role, created_by, created_at, expires_at, workspace_id, workspace_role) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		in.ID, tokenHash, in.Email, in.Role, in.ProjectID, in.ProjectRole, in.CreatedBy, ts(in.CreatedAt), ts(in.ExpiresAt), in.WorkspaceID, in.WorkspaceRole)
	return err
}

func (s *Store) ListInvites(ctx context.Context) ([]*Invite, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE expires_at > ? ORDER BY created_at DESC`, ts(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Invite{}
	for rows.Next() {
		in, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) GetInvite(ctx context.Context, id string) (*Invite, error) {
	return scanInvite(s.q.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE id=?`, id))
}

func (s *Store) DeleteInvite(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM invites WHERE id=?`, id))
}

// AcceptInvite는 초대 하나로 사용자를 만든다(같은 트랜잭션에서 초대를 지우고, 프로젝트 초대면 멤버로 넣는다).
// 초대가 없거나 만료됐으면 ErrNotFound, 같은 이메일의 사용자가 있으면 ErrConflict(초대는 남는다)다.
func (s *Store) AcceptInvite(ctx context.Context, tokenHash string, u *User) (*Invite, error) {
	var out *Invite
	err := s.Tx(ctx, func(tx *Store) error {
		in, err := scanInvite(tx.q.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE token_hash=? AND expires_at > ?`, tokenHash, ts(time.Now())))
		if err != nil {
			return err
		}
		if err := tx.checkInviteAuthority(ctx, in); err != nil {
			return err
		}
		u.Email, u.Role = in.Email, in.Role
		if err := tx.CreateUser(ctx, u); err != nil {
			return err
		}
		if in.WorkspaceID != nil && in.WorkspaceRole != nil {
			if err := tx.SetWorkspaceMember(ctx, *in.WorkspaceID, u.ID, *in.WorkspaceRole); err != nil {
				return err
			}
		}
		if in.ProjectID != nil && in.ProjectRole != nil {
			prj, err := tx.GetProject(ctx, *in.ProjectID)
			if err != nil {
				return err
			}
			if in.WorkspaceID == nil {
				if err := tx.SetWorkspaceMember(ctx, prj.WorkspaceID, u.ID, RoleViewer); err != nil {
					return err
				}
			} else if *in.WorkspaceID != prj.WorkspaceID {
				return ErrWorkspaceMembership
			}
			if err := tx.SetMember(ctx, *in.ProjectID, u.ID, *in.ProjectRole); err != nil {
				return err
			}
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM invites WHERE id=?`, in.ID); err != nil {
			return err
		}
		out = in
		return nil
	})
	return out, err
}

// Recheck the inviter's workspace/project grants at redemption time. An old
// link cannot restore authority after its creator is disabled or demoted.
func (s *Store) checkInviteAuthority(ctx context.Context, in *Invite) error {
	if in.CreatedBy == "" { // compatibility for trusted, directly seeded invitations
		return nil
	}
	u, err := s.GetUser(ctx, in.CreatedBy)
	if err != nil {
		return err
	}
	if u.Disabled {
		return ErrNotFound
	}
	if in.WorkspaceID != nil {
		role, err := s.WorkspaceRole(ctx, *in.WorkspaceID, u)
		if err != nil {
			return err
		}
		if role == "" {
			return ErrNotFound
		}
		// A project admin can invite a workspace viewer into that project.
		projectOnly := in.ProjectID != nil && in.WorkspaceRole != nil && *in.WorkspaceRole == RoleViewer
		if !projectOnly && (RoleLevel(role) < RoleLevel(RoleAdmin) || in.WorkspaceRole == nil || RoleLevel(role) < RoleLevel(*in.WorkspaceRole)) {
			return ErrNotFound
		}
	}
	if in.ProjectID != nil {
		role, err := s.EffectiveProjectRole(ctx, *in.ProjectID, u)
		if err != nil {
			return err
		}
		if role != RoleAdmin {
			return ErrNotFound
		}
	}
	return nil
}

// ---------------- API tokens ----------------

// API 토큰 범위
const (
	ScopeInstance    = "instance"
	ScopeWorkspace   = "workspace"
	ScopeProject     = "project"
	ScopeEnvironment = "environment"
)

type APIToken struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	Name          string     `json:"name"`
	Prefix        string     `json:"prefix"`
	Scope         string     `json:"scope"`
	WorkspaceID   *string    `json:"workspace_id"`
	ProjectID     *string    `json:"project_id"`
	EnvironmentID *string    `json:"environment_id"`
	Role          string     `json:"role"`
	ExpiresAt     *time.Time `json:"expires_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

const tokenCols = `id, user_id, name, prefix, scope, project_id, environment_id, role, expires_at, last_used_at, created_at, workspace_id`

func scanToken(r scanner) (*APIToken, error) {
	var t APIToken
	var pid, eid, exp, used, wsid sql.NullString
	var created string
	if err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Scope, &pid, &eid, &t.Role, &exp, &used, &created, &wsid); err != nil {
		return nil, notFound(err)
	}
	t.ProjectID, t.EnvironmentID, t.WorkspaceID = nullStr(pid), nullStr(eid), nullStr(wsid)
	t.ExpiresAt, t.LastUsedAt, t.CreatedAt = parseNullTS(exp), parseNullTS(used), parseTS(created)
	return &t, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken, tokenHash string) error {
	t.ID, t.CreatedAt = NewID("tok"), time.Now().UTC()
	if t.Scope != ScopeInstance && t.WorkspaceID == nil {
		wsID := DefaultWorkspaceID
		if t.ProjectID != nil {
			p, err := s.GetProject(ctx, *t.ProjectID)
			if err != nil {
				return err
			}
			wsID = p.WorkspaceID
		}
		t.WorkspaceID = &wsID
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, scope, project_id, environment_id, role, expires_at, last_used_at, created_at, workspace_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.UserID, t.Name, tokenHash, t.Prefix, t.Scope, t.ProjectID, t.EnvironmentID, t.Role, nullTS(t.ExpiresAt), nil, ts(t.CreatedAt), t.WorkspaceID)
	return conflict(err)
}

// GetAPITokenByHash는 만료되지 않은 토큰을 찾는다.
func (s *Store) GetAPITokenByHash(ctx context.Context, tokenHash string) (*APIToken, error) {
	return scanToken(s.q.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE token_hash=? AND (expires_at IS NULL OR expires_at > ?)`, tokenHash, ts(time.Now())))
}

func (s *Store) GetAPIToken(ctx context.Context, id string) (*APIToken, error) {
	return scanToken(s.q.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id=?`, id))
}

// ListAPITokens는 사용자의 토큰을 돌려준다(userID가 비면 전체).
func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]*APIToken, error) {
	q, args := `SELECT `+tokenCols+` FROM api_tokens`, []any{}
	if userID != "" {
		q += ` WHERE user_id=?`
		args = append(args, userID)
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TouchAPIToken(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, ts(time.Now()), id)
	return err
}

func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=?`, id))
}

func (s *Store) DeleteUserAPITokens(ctx context.Context, userID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id=?`, userID)
	return err
}

// ---------------- Recovery codes ----------------

// SetRecoveryCodes는 사용자의 복구 코드(해시)를 모두 바꾼다.
func (s *Store) SetRecoveryCodes(ctx context.Context, userID string, hashes []string) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id=?`, userID); err != nil {
			return err
		}
		for _, h := range hashes {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?,?)`, userID, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// UseRecoveryCode는 코드가 있으면 지우고 true를 돌려준다(한 번만 쓸 수 있다).
func (s *Store) UseRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	res, err := s.q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id=? AND code_hash=?`, userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) CountRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_codes WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

// ---------------- Audit log ----------------

// AuditEntry는 프로젝트에 속하지 않는 작업 공간 수준 기록이다(사용자, 토큰, 로그인).
type AuditEntry struct {
	ID        string    `json:"id"`
	ActorID   string    `json:"actor_id"`
	Actor     string    `json:"actor"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	Message   string    `json:"message"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) CreateAudit(ctx context.Context, a *AuditEntry) error {
	a.ID, a.CreatedAt = NewID("aud"), time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `INSERT INTO audit_log (id, actor_id, actor, kind, target, message, ip, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.ActorID, a.Actor, a.Kind, a.Target, a.Message, a.IP, ts(a.CreatedAt))
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]*AuditEntry, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id, actor_id, actor, kind, target, message, ip, created_at FROM audit_log ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		var created string
		if err := rows.Scan(&a.ID, &a.ActorID, &a.Actor, &a.Kind, &a.Target, &a.Message, &a.IP, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTS(created)
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (s *Store) PruneAudit(ctx context.Context, before time.Time) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM audit_log WHERE created_at < ?`, ts(before))
	return err
}

// ListAuditForTargets는 target이 targets 중 하나인 감사 기록이다(최신순).
func (s *Store) ListAuditForTargets(ctx context.Context, targets []string, limit int) ([]*AuditEntry, error) {
	out := []*AuditEntry{}
	if len(targets) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(targets)+1)
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, limit)
	rows, err := s.q.QueryContext(ctx, `SELECT id, actor_id, actor, kind, target, message, ip, created_at FROM audit_log
		WHERE target IN (?`+strings.Repeat(",?", len(targets)-1)+`) ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AuditEntry
		var created string
		if err := rows.Scan(&a.ID, &a.ActorID, &a.Actor, &a.Kind, &a.Target, &a.Message, &a.IP, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTS(created)
		out = append(out, &a)
	}
	return out, rows.Err()
}
