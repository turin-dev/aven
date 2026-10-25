package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ---------------- Workspaces ----------------
//
// All workspaces use explicit workspace_members, including the shared default.
// Instance roles never imply membership. Workspace owners/admins administer all
// projects in their workspace; other members need direct or group project grants.

const DefaultWorkspaceID = "ws_default"

// ErrWorkspaceNotEmpty는 프로젝트가 남아 있는 작업 공간을 지우려 할 때다.
var ErrWorkspaceNotEmpty = errors.New("workspace still has projects")

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Personal  bool      `json:"personal"`
	OwnerID   *string   `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
	// Default는 기본(공유) 작업 공간인지다.
	Default bool `json:"default"`
}

type WorkspaceMember struct {
	WorkspaceID string    `json:"workspace_id"`
	UserID      string    `json:"user_id"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Disabled    bool      `json:"disabled"`
}

// migrateWorkspaces는 작업 공간·멤버 테이블과 사용자별 GitHub 연결을 만들고, projects를 작업 공간에 묶는다.
// projects.name의 전역 UNIQUE를 (workspace_id, name)으로 바꾸려면 테이블을 다시 만들어야 해서 외래 키 검사를 끈 채로 돈다
// (noForeignKeyMigrations). 기존 프로젝트는 모두 기본 작업 공간에 들어가므로 접근 권한은 그대로다.
func migrateWorkspaces(ctx context.Context, tx *Store) error {
	now := ts(time.Now())
	for _, q := range []string{
		`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			personal INTEGER NOT NULL DEFAULT 0,
			owner_id TEXT REFERENCES users(id) ON DELETE SET NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX workspaces_personal_owner ON workspaces(owner_id) WHERE personal = 1`,
		`CREATE TABLE workspace_members (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (workspace_id, user_id)
		)`,
		`CREATE INDEX workspace_members_user ON workspace_members(user_id)`,
		`CREATE TABLE github_connections (
			user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			login TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT '',
			token TEXT NOT NULL,
			connected_at TEXT NOT NULL
		)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if _, err := tx.q.ExecContext(ctx, `INSERT INTO workspaces (id, name, personal, owner_id, created_at) VALUES (?,?,0,NULL,?)`,
		DefaultWorkspaceID, "Team", now); err != nil {
		return err
	}

	// projects 재생성: 원래 CREATE 문(뒤에 ALTER로 붙은 컬럼 포함)에서 name의 UNIQUE만 빼고 새 컬럼을 붙인다.
	var orig string
	if err := tx.q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&orig); err != nil {
		return err
	}
	cols, err := tx.tableColumns(ctx, "projects")
	if err != nil {
		return err
	}
	body := strings.Replace(orig, "name TEXT NOT NULL UNIQUE", "name TEXT NOT NULL", 1)
	if body == orig {
		return fmt.Errorf("projects schema: unexpected definition %q", orig)
	}
	i := strings.LastIndex(body, ")")
	if i < 0 {
		return fmt.Errorf("projects schema: unexpected definition %q", orig)
	}
	body = body[:i] + `,
	workspace_id TEXT NOT NULL DEFAULT '` + DefaultWorkspaceID + `' REFERENCES workspaces(id),
	github_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
	UNIQUE (workspace_id, name)
)`
	body = strings.Replace(body, "CREATE TABLE projects", "CREATE TABLE projects_new", 1)
	colList := strings.Join(cols, ", ")
	for _, q := range []string{
		body,
		`INSERT INTO projects_new (` + colList + `) SELECT ` + colList + ` FROM projects`,
		`DROP TABLE projects`,
		`ALTER TABLE projects_new RENAME TO projects`,
		`CREATE INDEX projects_workspace ON projects(workspace_id)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("rebuild projects: %w", err)
		}
	}

	// 예전 전역 GitHub 연결(settings.github_oauth)은 그것을 연결한 사용자의 연결이 되고, 기존 프로젝트는 그 계정으로 계속 배포한다.
	var raw string
	err = tx.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='github_oauth'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var legacy struct {
		Login       string    `json:"login"`
		Scopes      string    `json:"scopes"`
		ConnectedAt time.Time `json:"connected_at"`
		Token       string    `json:"token"`
	}
	if raw != "" && json.Unmarshal([]byte(raw), &legacy) == nil && legacy.Token != "" {
		uid := BuiltinUserID
		var actor string
		if tx.q.QueryRowContext(ctx, `SELECT a.actor_id FROM audit_log a JOIN users u ON u.id = a.actor_id
			WHERE a.kind='github.connected' ORDER BY a.created_at DESC LIMIT 1`).Scan(&actor) == nil && actor != "" {
			uid = actor
		}
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=?`, uid).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			if legacy.ConnectedAt.IsZero() {
				legacy.ConnectedAt = time.Now()
			}
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO github_connections (user_id, login, scopes, token, connected_at) VALUES (?,?,?,?,?)`,
				uid, legacy.Login, legacy.Scopes, legacy.Token, ts(legacy.ConnectedAt)); err != nil {
				return err
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE projects SET github_user_id=?`, uid); err != nil {
				return err
			}
		}
		if _, err := tx.q.ExecContext(ctx, `DELETE FROM settings WHERE key='github_oauth'`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) tableColumns(ctx context.Context, table string) ([]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

const workspaceCols = `id, name, personal, owner_id, created_at`

func scanWorkspace(r scanner) (*Workspace, error) {
	var w Workspace
	var owner sql.NullString
	var created string
	if err := r.Scan(&w.ID, &w.Name, &w.Personal, &owner, &created); err != nil {
		return nil, notFound(err)
	}
	w.OwnerID, w.CreatedAt, w.Default = nullStr(owner), parseTS(created), w.ID == DefaultWorkspaceID
	return &w, nil
}

func (s *Store) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	return scanWorkspace(s.q.QueryRowContext(ctx, `SELECT `+workspaceCols+` FROM workspaces WHERE id=?`, id))
}

// ListWorkspaces는 모든 작업 공간이다(기본, 개인, 팀 순).
func (s *Store) ListWorkspaces(ctx context.Context) ([]*Workspace, error) {
	return s.queryWorkspaces(ctx, `SELECT `+workspaceCols+` FROM workspaces ORDER BY id != ?, personal DESC, created_at, id`, DefaultWorkspaceID)
}

// UserWorkspaces는 사용자가 명시적으로 멤버인 작업 공간이다.
func (s *Store) UserWorkspaces(ctx context.Context, userID string) ([]*Workspace, error) {
	return s.queryWorkspaces(ctx, `SELECT `+prefixCols("w.", workspaceCols)+` FROM workspaces w
		WHERE w.id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)
		ORDER BY w.id != ?, NOT (w.personal = 1 AND w.owner_id = ?), w.created_at, w.id`, userID, DefaultWorkspaceID, userID)
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func (s *Store) queryWorkspaces(ctx context.Context, q string, args ...any) ([]*Workspace, error) {
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Workspace{}
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CreateWorkspace는 팀 작업 공간을 만들고 만든 사람을 owner로 넣는다.
func (s *Store) CreateWorkspace(ctx context.Context, name, ownerID string) (*Workspace, error) {
	w := &Workspace{ID: NewID("ws"), Name: name, CreatedAt: time.Now().UTC(), OwnerID: &ownerID}
	err := s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO workspaces (`+workspaceCols+`) VALUES (?,?,0,?,?)`, w.ID, w.Name, ownerID, ts(w.CreatedAt)); err != nil {
			return err
		}
		return tx.SetWorkspaceMember(ctx, w.ID, ownerID, RoleOwner)
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// EnsurePersonalWorkspace는 사용자의 개인 작업 공간을 돌려준다. 없으면 만든다(사용자가 처음 작업 공간 목록을 볼 때).
func (s *Store) EnsurePersonalWorkspace(ctx context.Context, u *User) (*Workspace, error) {
	w, err := s.PersonalWorkspace(ctx, u.ID)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return w, err
	}
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name, _, _ = strings.Cut(u.Email, "@")
	}
	w = &Workspace{ID: NewID("ws"), Name: name, Personal: true, OwnerID: &u.ID, CreatedAt: time.Now().UTC()}
	err = s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO workspaces (`+workspaceCols+`) VALUES (?,?,1,?,?)`, w.ID, w.Name, u.ID, ts(w.CreatedAt)); err != nil {
			return err
		}
		return tx.SetWorkspaceMember(ctx, w.ID, u.ID, RoleOwner)
	})
	if err != nil {
		// 동시에 두 요청이 만들었으면 먼저 만든 쪽을 쓴다.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return s.PersonalWorkspace(ctx, u.ID)
		}
		return nil, err
	}
	return w, nil
}

func (s *Store) PersonalWorkspace(ctx context.Context, userID string) (*Workspace, error) {
	return scanWorkspace(s.q.QueryRowContext(ctx, `SELECT `+workspaceCols+` FROM workspaces WHERE personal=1 AND owner_id=?`, userID))
}

func (s *Store) RenameWorkspace(ctx context.Context, id, name string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE workspaces SET name=? WHERE id=?`, name, id))
}

// DeleteWorkspace는 빈 팀 작업 공간을 지운다(기본·개인 작업 공간은 호출하는 쪽이 막는다).
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	return s.Tx(ctx, func(tx *Store) error {
		var n int
		if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE workspace_id=?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrWorkspaceNotEmpty
		}
		return affected(tx.q.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, id))
	})
}

func (s *Store) SetWorkspaceMember(ctx context.Context, wsID, userID, role string) error {
	if !ValidWorkspaceRole(role) {
		return fmt.Errorf("invalid workspace role: %s", role)
	}
	return s.Tx(ctx, func(tx *Store) error {
		if role != RoleOwner {
			if err := tx.checkWorkspaceMemberRemoval(ctx, wsID, userID); err != nil {
				return err
			}
		}
		_, err := tx.q.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?,?,?,?)
			ON CONFLICT(workspace_id, user_id) DO UPDATE SET role=excluded.role`, wsID, userID, role, ts(time.Now()))
		return err
	})
}

func (s *Store) RemoveWorkspaceMember(ctx context.Context, wsID, userID string) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := tx.checkWorkspaceMemberRemoval(ctx, wsID, userID); err != nil {
			return err
		}
		return tx.removeWorkspaceMemberRows(ctx, wsID, userID)
	})
}

// removeWorkspaceMemberRows는 규칙 검사 없이 멤버와 그 작업 공간 안의 프로젝트 멤버십·그룹·토큰·초대를 지운다(트랜잭션 안에서).
func (s *Store) removeWorkspaceMemberRows(ctx context.Context, wsID, userID string) error {
	if err := s.RemoveWorkspaceProjectMemberships(ctx, wsID, userID); err != nil {
		return err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM access_group_users WHERE user_id=? AND group_id IN (SELECT id FROM access_groups WHERE workspace_id=?)`, userID, wsID); err != nil {
		return err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id=? AND workspace_id=?`, userID, wsID); err != nil {
		return err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM invites WHERE created_by=? AND workspace_id=?`, userID, wsID); err != nil {
		return err
	}
	return affected(s.q.ExecContext(ctx, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, wsID, userID))
}

// 서버 owner가 소유권을 강제로 바꿀 때 기존 owner를 어떻게 할지다.
const (
	PrevOwnerKeep   = "keep"   // 그대로 owner로 둔다
	PrevOwnerDemote = "demote" // admin으로 낮춘다
	PrevOwnerRemove = "remove" // 작업 공간에서 내보낸다
)

// ForceWorkspaceOwner는 서버 owner의 소유권 강제 변경이다: newOwnerID를 owner로 만들고 기존 owner를 prev대로 처리한다.
// 개인 작업 공간 owner 고정·마지막 owner 유지 규칙을 우회한다. 개인 작업 공간은 팀 작업 공간으로 바뀐다(기존 주인의 개인
// 작업 공간은 다음 로그인 때 새로 만들어진다). 처리한 기존 owner ID와 팀으로 바꿨는지를 돌려준다.
func (s *Store) ForceWorkspaceOwner(ctx context.Context, wsID, newOwnerID, prev string) (prevOwners []string, converted bool, err error) {
	if prev != PrevOwnerKeep && prev != PrevOwnerDemote && prev != PrevOwnerRemove {
		return nil, false, fmt.Errorf("invalid previous owner action: %s", prev)
	}
	err = s.Tx(ctx, func(tx *Store) error {
		w, err := tx.GetWorkspace(ctx, wsID)
		if err != nil {
			return err
		}
		rows, err := tx.q.QueryContext(ctx, `SELECT user_id FROM workspace_members WHERE workspace_id=? AND role='owner' AND user_id!=?`, wsID, newOwnerID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			prevOwners = append(prevOwners, id)
		}
		rows.Close()
		if w.Personal && w.OwnerID != nil && *w.OwnerID != newOwnerID {
			if !slices.Contains(prevOwners, *w.OwnerID) {
				prevOwners = append(prevOwners, *w.OwnerID)
			}
			if _, err := tx.q.ExecContext(ctx, `UPDATE workspaces SET personal=0, owner_id=NULL WHERE id=?`, wsID); err != nil {
				return err
			}
			converted = true
		}
		if _, err := tx.q.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?,?,?,?)
			ON CONFLICT(workspace_id, user_id) DO UPDATE SET role=excluded.role`, wsID, newOwnerID, RoleOwner, ts(time.Now())); err != nil {
			return err
		}
		for _, id := range prevOwners {
			switch prev {
			case PrevOwnerDemote:
				if _, err := tx.q.ExecContext(ctx, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, RoleAdmin, wsID, id); err != nil {
					return err
				}
			case PrevOwnerRemove:
				if err := tx.removeWorkspaceMemberRows(ctx, wsID, id); err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
			}
		}
		return nil
	})
	return prevOwners, converted, err
}

func (s *Store) checkWorkspaceMemberRemoval(ctx context.Context, wsID, userID string) error {
	w, err := s.GetWorkspace(ctx, wsID)
	if err != nil {
		return err
	}
	if w.Personal && w.OwnerID != nil && *w.OwnerID == userID {
		return ErrPersonalOwner
	}
	var role string
	err = s.q.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, wsID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if role == RoleOwner {
		var others int
		if err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members m JOIN users u ON u.id=m.user_id
		 WHERE m.workspace_id=? AND m.user_id!=? AND m.role='owner' AND u.disabled=0`, wsID, userID).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return ErrLastWorkspaceOwner
		}
	}
	return nil
}

// WorkspaceRole은 사용자의 작업 공간 역할이다(비활성 계정 또는 멤버가 아니면 "").
func (s *Store) WorkspaceRole(ctx context.Context, wsID string, u *User) (string, error) {
	if u == nil || u.Disabled {
		return "", nil
	}
	var role string
	err := s.q.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, wsID, u.ID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Store) ListWorkspaceMembers(ctx context.Context, wsID string) ([]*WorkspaceMember, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT m.workspace_id, m.user_id, m.role, m.created_at, u.email, u.name, u.disabled
		FROM workspace_members m JOIN users u ON u.id = m.user_id WHERE m.workspace_id=? ORDER BY m.created_at, u.email`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WorkspaceMember{}
	for rows.Next() {
		var m WorkspaceMember
		var created string
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &m.Role, &created, &m.Email, &m.Name, &m.Disabled); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(created)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// CountWorkspaceOwners는 작업 공간의 owner 수다(마지막 owner를 빼지 못하게).
func (s *Store) CountWorkspaceOwners(ctx context.Context, wsID string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id=? AND m.role=? AND u.disabled=0`, wsID, RoleOwner).Scan(&n)
	return n, err
}

// MoveProject는 프로젝트를 다른 작업 공간으로 옮긴다. 옮긴 공간에 같은 이름이 있으면 ErrConflict다.
func (s *Store) MoveProject(ctx context.Context, pid, wsID string, actorIDs ...string) error {
	return s.Tx(ctx, func(tx *Store) error {
		p, err := tx.GetProject(ctx, pid)
		if err != nil {
			return err
		}
		if p.WorkspaceID == wsID {
			return nil
		}
		if err := conflict(affected(tx.q.ExecContext(ctx, `UPDATE projects SET workspace_id=?, github_user_id=NULL WHERE id=?`, wsID, pid))); err != nil {
			return err
		}
		if err := tx.checkWorkspaceResources(ctx, wsID); err != nil {
			return err
		}
		// A move is an access boundary: old credentials and grants cannot follow it.
		for _, q := range []string{
			`DELETE FROM project_members WHERE project_id=?`,
			`DELETE FROM access_group_projects WHERE project_id=?`,
			`DELETE FROM api_tokens WHERE project_id=?`,
			`DELETE FROM invites WHERE project_id=?`,
		} {
			if _, err := tx.q.ExecContext(ctx, q, pid); err != nil {
				return err
			}
		}
		if len(actorIDs) > 0 && actorIDs[0] != "" {
			return tx.SetMember(ctx, pid, actorIDs[0], RoleAdmin)
		}
		return nil
	})
}

// SetProjectGitHubUser는 프로젝트가 GitHub 저장소 접근(클론, 커밋 상태, PR 파일)에 쓸 사용자 연결을 바꾼다(nil이면 없음).
func (s *Store) SetProjectGitHubUser(ctx context.Context, pid string, userID *string) error {
	return affected(s.q.ExecContext(ctx, `UPDATE projects SET github_user_id=? WHERE id=?`, userID, pid))
}

// EffectiveProjectRole은 사용자가 프로젝트에서 갖는 역할이다(admin|member|viewer|"").
// 작업 공간 멤버십을 먼저 확인한 뒤 그 공간의 역할과 프로젝트/그룹 역할을 합친다.
func (s *Store) EffectiveProjectRole(ctx context.Context, pid string, u *User) (string, error) {
	var wsID string
	if err := s.q.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE id=?`, pid).Scan(&wsID); err != nil {
		return "", notFound(err)
	}
	wsRole, err := s.WorkspaceRole(ctx, wsID, u)
	if err != nil {
		return "", err
	}
	if wsRole == RoleOwner || wsRole == RoleAdmin {
		return RoleAdmin, nil
	}
	if wsRole == "" {
		return "", nil
	}
	return s.ProjectRole(ctx, pid, u.ID)
}

// ---------------- GitHub connections (사용자별) ----------------

// GitHubConnection은 사용자가 대시보드에서 연결한 GitHub 계정이다. Token은 암호문이다(밖으로 내보내지 않는다).
type GitHubConnection struct {
	UserID      string    `json:"user_id"`
	Login       string    `json:"login"`
	Scopes      string    `json:"scopes"`
	ConnectedAt time.Time `json:"connected_at"`
	Token       string    `json:"-"`
}

func (s *Store) GetGitHubConnection(ctx context.Context, userID string) (*GitHubConnection, error) {
	var c GitHubConnection
	var at string
	err := s.q.QueryRowContext(ctx, `SELECT user_id, login, scopes, token, connected_at FROM github_connections WHERE user_id=?`, userID).
		Scan(&c.UserID, &c.Login, &c.Scopes, &c.Token, &at)
	if err != nil {
		return nil, notFound(err)
	}
	c.ConnectedAt = parseTS(at)
	return &c, nil
}

// SetGitHubConnection은 토큰(평문)을 암호화해 사용자 연결을 저장한다(이전 연결을 바꾼다).
func (s *Store) SetGitHubConnection(ctx context.Context, userID, token, login, scopes string) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO github_connections (user_id, login, scopes, token, connected_at) VALUES (?,?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET login=excluded.login, scopes=excluded.scopes, token=excluded.token, connected_at=excluded.connected_at`,
		userID, login, scopes, s.Seal(token), ts(time.Now()))
	return err
}

// GitHubConnectionOwner는 GitHub를 연결한 활성 사용자의 공개 정보다(토큰 없음).
type GitHubConnectionOwner struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Email    string `json:"email"`
	Login    string `json:"login"`
}

// ListGitHubConnectionOwners는 GitHub를 연결한 활성 사용자 목록이다(서버 owner의 프로젝트 연결 지정용).
func (s *Store) ListGitHubConnectionOwners(ctx context.Context) ([]GitHubConnectionOwner, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT u.id, u.name, u.email, g.login FROM github_connections g JOIN users u ON u.id=g.user_id
		WHERE u.disabled=0 ORDER BY lower(u.name), u.email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitHubConnectionOwner{}
	for rows.Next() {
		var c GitHubConnectionOwner
		if err := rows.Scan(&c.UserID, &c.UserName, &c.Email, &c.Login); err != nil {
			return nil, err
		}
		if c.UserName == "" {
			c.UserName = c.Email
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteGitHubConnection(ctx context.Context, userID string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM github_connections WHERE user_id=?`, userID))
}

// ProjectGitHubUser는 프로젝트의 GitHub 연결 사용자다(없으면 "").
func (s *Store) ProjectGitHubUser(ctx context.Context, pid string) (string, error) {
	var uid sql.NullString
	if err := s.q.QueryRowContext(ctx, `SELECT github_user_id FROM projects WHERE id=?`, pid).Scan(&uid); err != nil {
		return "", notFound(err)
	}
	return uid.String, nil
}

// RemoveWorkspaceProjectMemberships는 작업 공간에서 나간 사용자의 그 공간 프로젝트 직접 멤버십을 지운다.
func (s *Store) RemoveWorkspaceProjectMemberships(ctx context.Context, wsID, userID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM project_members WHERE user_id=? AND project_id IN (SELECT id FROM projects WHERE workspace_id=?)`, userID, wsID)
	return err
}
