package store

import (
	"context"
	"errors"
)

// Roles are ranked identically at each boundary, but a role at one boundary
// never grants a role at another. users.role is the instance role only.
func RoleLevel(role string) int {
	switch role {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

var (
	ErrLastWorkspaceOwner  = errors.New("작업 공간에는 활성 owner가 한 명 이상 있어야 합니다")
	ErrPersonalOwner       = errors.New("개인 작업 공간 주인의 멤버십은 바꿀 수 없습니다")
	ErrWorkspaceMembership = errors.New("먼저 해당 작업 공간에 멤버로 추가하세요")
)

// Snapshot legacy membership once. Future instance role changes and new
// accounts do not change membership of the shared workspace.
func migratePermissionBoundaries(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`INSERT INTO workspace_members (workspace_id, user_id, role, created_at)
		 SELECT 'ws_default', id, role, created_at FROM users`,
		`INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, created_at)
		 SELECT p.workspace_id, m.user_id, 'viewer', m.created_at FROM project_members m JOIN projects p ON p.id=m.project_id`,
		`ALTER TABLE api_tokens ADD COLUMN workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE`,
		`UPDATE api_tokens SET workspace_id = CASE WHEN scope='workspace' THEN 'ws_default'
		 ELSE (SELECT workspace_id FROM projects WHERE id=api_tokens.project_id) END`,
		`ALTER TABLE invites ADD COLUMN workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE`,
		`ALTER TABLE invites ADD COLUMN workspace_role TEXT`,
		`UPDATE invites SET workspace_id=COALESCE((SELECT workspace_id FROM projects WHERE id=invites.project_id), 'ws_default'),
		 workspace_role=CASE WHEN project_id IS NULL THEN role ELSE 'viewer' END`,
		`ALTER TABLE workspaces ADD COLUMN access_groups_enabled INTEGER NOT NULL DEFAULT 0`,
		`UPDATE workspaces SET access_groups_enabled=(SELECT access_groups_enabled FROM access_control_settings WHERE id=1) WHERE id='ws_default'`,
		`CREATE TABLE access_groups_new (
		 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		 name TEXT NOT NULL COLLATE NOCASE, role TEXT NOT NULL CHECK(role IN ('admin','member','viewer')),
		 created_at TEXT NOT NULL, UNIQUE(workspace_id, name))`,
		`INSERT INTO access_groups_new SELECT id, 'ws_default', name, role, created_at FROM access_groups`,
		`DROP TABLE access_groups`,
		`ALTER TABLE access_groups_new RENAME TO access_groups`,
		`ALTER TABLE templates ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_default' REFERENCES workspaces(id) ON DELETE CASCADE`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// CheckWorkspaceOwners protects ownership even when an instance administrator
// disables or deletes an account. Transfer ownership in each workspace first.
func (s *Store) CheckWorkspaceOwners(ctx context.Context, userID string) error {
	var count int
	err := s.q.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members m
	 JOIN workspaces w ON w.id=m.workspace_id
	 WHERE m.user_id=? AND m.role='owner' AND w.personal=0
	 AND NOT EXISTS (SELECT 1 FROM workspace_members other JOIN users u ON u.id=other.user_id
	 WHERE other.workspace_id=m.workspace_id AND other.user_id!=m.user_id AND other.role='owner' AND u.disabled=0)`, userID).Scan(&count)
	if err == nil && count > 0 {
		return ErrLastWorkspaceOwner
	}
	return err
}
