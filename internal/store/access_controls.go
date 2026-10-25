package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ---- Enterprise access controls: access groups, environment RBAC, guardrails ----
//
// 작업 공간 하나에 설정 행 하나(access_control_settings id=1)가 있다. access group은 사람 집합 + 프로젝트 집합 + 프로젝트 역할 하나다.
// 그룹은 꺼진 상태에서도 만들고 고칠 수 있고, 켜져 있을 때만(access_groups_enabled) 권한을 준다(직접 부여한 프로젝트 권한에 더해진다).

// ErrDeploymentSourceRestricted는 "승인된 출처에서만 배포" guardrail이 막은 배포·소스 변경이다(API는 403).
var ErrDeploymentSourceRestricted = errors.New("deployment source is not approved by workspace policy")

// AccessControlSettings는 작업 공간의 접근 그룹 적용 여부와 guardrail 정책이다.
type AccessControlSettings struct {
	AccessGroupsEnabled       bool     `json:"access_groups_enabled"`
	RestrictGeneratedDomains  bool     `json:"restrict_generated_domains"`
	RestrictTCPProxy          bool     `json:"restrict_tcp_proxy"`
	RestrictDeploymentSources bool     `json:"restrict_deployment_sources"`
	ApprovedGitHubOrgs        []string `json:"approved_github_orgs"`
}

// AccessGroup은 사람(UserIDs)에게 프로젝트(ProjectIDs)의 역할 Role을 준다.
type AccessGroup struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	UserIDs     []string  `json:"user_ids"`
	ProjectIDs  []string  `json:"project_ids"`
}

// migrateAccessControls는 access group, 환경 제한(restricted), 작업 공간 guardrail 정책을 추가한다.
func migrateAccessControls(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE environments ADD COLUMN restricted INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE access_control_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			access_groups_enabled INTEGER NOT NULL DEFAULT 0,
			restrict_generated_domains INTEGER NOT NULL DEFAULT 0,
			restrict_tcp_proxy INTEGER NOT NULL DEFAULT 0,
			restrict_deployment_sources INTEGER NOT NULL DEFAULT 0,
			approved_github_orgs TEXT NOT NULL DEFAULT '[]'
		)`,
		`INSERT INTO access_control_settings (id) VALUES (1)`,
		`CREATE TABLE access_groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			role TEXT NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE access_group_users (
			group_id TEXT NOT NULL REFERENCES access_groups(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			PRIMARY KEY (group_id, user_id)
		)`,
		`CREATE TABLE access_group_projects (
			group_id TEXT NOT NULL REFERENCES access_groups(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			PRIMARY KEY (group_id, project_id)
		)`,
		`CREATE INDEX access_group_users_user ON access_group_users(user_id, group_id)`,
		`CREATE INDEX access_group_projects_project ON access_group_projects(project_id, group_id)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AccessControlSettings(ctx context.Context) (*AccessControlSettings, error) {
	var groups, domains, tcp, sources int
	var orgs string
	err := s.q.QueryRowContext(ctx, `SELECT access_groups_enabled, restrict_generated_domains, restrict_tcp_proxy,
		restrict_deployment_sources, approved_github_orgs FROM access_control_settings WHERE id=1`).
		Scan(&groups, &domains, &tcp, &sources, &orgs)
	if err == sql.ErrNoRows {
		return &AccessControlSettings{ApprovedGitHubOrgs: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	out := &AccessControlSettings{
		AccessGroupsEnabled: groups != 0, RestrictGeneratedDomains: domains != 0,
		RestrictTCPProxy: tcp != 0, RestrictDeploymentSources: sources != 0,
	}
	json.Unmarshal([]byte(orgs), &out.ApprovedGitHubOrgs)
	if out.ApprovedGitHubOrgs == nil {
		out.ApprovedGitHubOrgs = []string{}
	}
	out.AccessGroupsEnabled, err = s.WorkspaceAccessGroupsEnabled(ctx, DefaultWorkspaceID)
	return out, err
}

func (s *Store) SetAccessControlSettings(ctx context.Context, in *AccessControlSettings) error {
	if in.ApprovedGitHubOrgs == nil {
		in.ApprovedGitHubOrgs = []string{}
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO access_control_settings (id, access_groups_enabled, restrict_generated_domains,
		restrict_tcp_proxy, restrict_deployment_sources, approved_github_orgs) VALUES (1,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET access_groups_enabled=excluded.access_groups_enabled,
		restrict_generated_domains=excluded.restrict_generated_domains, restrict_tcp_proxy=excluded.restrict_tcp_proxy,
		restrict_deployment_sources=excluded.restrict_deployment_sources, approved_github_orgs=excluded.approved_github_orgs`,
		in.AccessGroupsEnabled, in.RestrictGeneratedDomains, in.RestrictTCPProxy,
		in.RestrictDeploymentSources, jsonStr(in.ApprovedGitHubOrgs))
	if err == nil {
		err = s.SetWorkspaceAccessGroupsEnabled(ctx, DefaultWorkspaceID, in.AccessGroupsEnabled)
	}
	return err
}

func (s *Store) ListAccessGroups(ctx context.Context) ([]*AccessGroup, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT id, name, role, created_at, workspace_id FROM access_groups ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	out := []*AccessGroup{}
	byID := map[string]*AccessGroup{}
	for rows.Next() {
		g := &AccessGroup{UserIDs: []string{}, ProjectIDs: []string{}}
		var created string
		if err := rows.Scan(&g.ID, &g.Name, &g.Role, &created, &g.WorkspaceID); err != nil {
			rows.Close()
			return nil, err
		}
		g.CreatedAt = parseTS(created)
		out = append(out, g)
		byID[g.ID] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, q := range []struct {
		sql string
		add func(g *AccessGroup, id string)
	}{
		{`SELECT group_id, user_id FROM access_group_users ORDER BY user_id`, func(g *AccessGroup, id string) { g.UserIDs = append(g.UserIDs, id) }},
		{`SELECT group_id, project_id FROM access_group_projects ORDER BY project_id`, func(g *AccessGroup, id string) { g.ProjectIDs = append(g.ProjectIDs, id) }},
	} {
		rows, err := s.q.QueryContext(ctx, q.sql)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var gid, id string
			if err := rows.Scan(&gid, &id); err != nil {
				rows.Close()
				return nil, err
			}
			if g := byID[gid]; g != nil {
				q.add(g, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) GetAccessGroup(ctx context.Context, id string) (*AccessGroup, error) {
	gs, err := s.ListAccessGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, ErrNotFound
}

// SaveAccessGroup은 그룹을 만들거나(ID가 비었으면) 이름·역할·사람·프로젝트를 통째로 바꾼다.
func (s *Store) SaveAccessGroup(ctx context.Context, g *AccessGroup) error {
	if g.WorkspaceID == "" {
		g.WorkspaceID = DefaultWorkspaceID
	}
	if g.ID == "" {
		g.ID = NewID("ag")
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	g.UserIDs, g.ProjectIDs = uniqueIDs(g.UserIDs), uniqueIDs(g.ProjectIDs)
	return s.Tx(ctx, func(tx *Store) error {
		for _, uid := range g.UserIDs {
			var n int
			if err := tx.q.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, g.WorkspaceID, uid).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrWorkspaceMembership
			}
		}
		for _, pid := range g.ProjectIDs {
			p, err := tx.GetProject(ctx, pid)
			if err != nil {
				return err
			}
			if p.WorkspaceID != g.WorkspaceID {
				return ErrWorkspaceMembership
			}
		}
		if existing, err := tx.GetAccessGroup(ctx, g.ID); err == nil && existing.WorkspaceID != g.WorkspaceID {
			return ErrWorkspaceMembership
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}

		_, err := tx.q.ExecContext(ctx, `INSERT INTO access_groups (id, name, role, created_at, workspace_id) VALUES (?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, role=excluded.role`, g.ID, g.Name, g.Role, ts(g.CreatedAt), g.WorkspaceID)
		if err != nil {
			return conflict(err)
		}
		for _, table := range []string{"access_group_users", "access_group_projects"} {
			if _, err := tx.q.ExecContext(ctx, `DELETE FROM `+table+` WHERE group_id=?`, g.ID); err != nil {
				return err
			}
		}
		for _, id := range g.UserIDs {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO access_group_users (group_id, user_id) VALUES (?,?)`, g.ID, id); err != nil {
				return err
			}
		}
		for _, id := range g.ProjectIDs {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO access_group_projects (group_id, project_id) VALUES (?,?)`, g.ID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func uniqueIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) DeleteAccessGroup(ctx context.Context, id string) error {
	return affected(s.q.ExecContext(ctx, `DELETE FROM access_groups WHERE id=?`, id))
}

// SetProjectAccessGroups는 프로젝트가 속한 그룹을 groupIDs로 바꾼다(프로젝트 설정의 "그룹에 추가").
func (s *Store) SetProjectAccessGroups(ctx context.Context, projectID string, groupIDs []string) error {
	return s.Tx(ctx, func(tx *Store) error {
		p, err := tx.GetProject(ctx, projectID)
		if err != nil {
			return err
		}
		for _, gid := range uniqueIDs(groupIDs) {
			g, err := tx.GetAccessGroup(ctx, gid)
			if err != nil {
				return err
			}
			if g.WorkspaceID != p.WorkspaceID {
				return ErrWorkspaceMembership
			}
		}

		if _, err := tx.q.ExecContext(ctx, `DELETE FROM access_group_projects WHERE project_id=?`, projectID); err != nil {
			return err
		}
		for _, gid := range uniqueIDs(groupIDs) {
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO access_group_projects (group_id, project_id) VALUES (?,?)`, gid, projectID); err != nil {
				return err
			}
		}
		return nil
	})
}

// AccessGroupRole은 사용자의 그룹들이 프로젝트에 주는 가장 높은 역할이다(그룹 적용 여부와 무관, 없으면 "").
func (s *Store) AccessGroupRole(ctx context.Context, projectID, userID string) (string, error) {
	var role string
	err := s.q.QueryRowContext(ctx, `SELECT g.role FROM access_groups g
		JOIN access_group_users u ON u.group_id=g.id
		JOIN access_group_projects p ON p.group_id=g.id
		JOIN projects pr ON pr.id=p.project_id AND pr.workspace_id=g.workspace_id
		JOIN workspace_members wm ON wm.workspace_id=g.workspace_id AND wm.user_id=u.user_id
		WHERE p.project_id=? AND u.user_id=?
		ORDER BY CASE g.role WHEN 'admin' THEN 3 WHEN 'member' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END DESC LIMIT 1`,
		projectID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}

// ProjectRole은 사용자가 프로젝트에서 받은 가장 높은 역할이다: 직접 부여(project_members)와,
// access group이 켜져 있으면 그룹에서 받은 역할 중 높은 것. 작업 공간 역할은 보지 않는다(호출하는 쪽이 처리).
func (s *Store) ProjectRole(ctx context.Context, projectID, userID string) (string, error) {
	role, err := s.MemberRole(ctx, projectID, userID)
	if err != nil {
		return "", err
	}
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	enabled, err := s.WorkspaceAccessGroupsEnabled(ctx, project.WorkspaceID)
	if err != nil || !enabled {
		return role, err
	}
	g, err := s.AccessGroupRole(ctx, projectID, userID)
	if err != nil {
		return "", err
	}
	if roleRank(g) > roleRank(role) {
		role = g
	}
	return role, nil
}

func roleRank(role string) int { return RoleLevel(role) }

// GroupGrant는 그룹이 프로젝트에서 사용자에게 준 역할이다(프로젝트 멤버 화면의 "접근 경로").
type GroupGrant struct {
	UserID    string `json:"user_id"`
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name"`
	Role      string `json:"role"`
}

// ProjectGroupGrants는 프로젝트가 속한 그룹들이 주는 사용자별 역할이다.
func (s *Store) ProjectGroupGrants(ctx context.Context, projectID string) ([]GroupGrant, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT u.user_id, g.id, g.name, g.role FROM access_groups g
		JOIN access_group_projects p ON p.group_id=g.id
		JOIN access_group_users u ON u.group_id=g.id
		WHERE p.project_id=? ORDER BY g.name COLLATE NOCASE, u.user_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupGrant{}
	for rows.Next() {
		var g GroupGrant
		if err := rows.Scan(&g.UserID, &g.GroupID, &g.GroupName, &g.Role); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SetEnvironmentRestricted(ctx context.Context, id string, restricted bool) error {
	return affected(s.q.ExecContext(ctx, `UPDATE environments SET restricted=? WHERE id=?`, restricted, id))
}

func (s *Store) WorkspaceAccessGroupsEnabled(ctx context.Context, wsID string) (bool, error) {
	var enabled bool
	err := s.q.QueryRowContext(ctx, `SELECT access_groups_enabled FROM workspaces WHERE id=?`, wsID).Scan(&enabled)
	return enabled, notFound(err)
}
func (s *Store) SetWorkspaceAccessGroupsEnabled(ctx context.Context, wsID string, enabled bool) error {
	return affected(s.q.ExecContext(ctx, `UPDATE workspaces SET access_groups_enabled=? WHERE id=?`, enabled, wsID))
}
