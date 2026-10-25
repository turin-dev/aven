package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
)

func TestPermissionBoundaryMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := &Store{db: db, q: db, box: new(*secret.Box)}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER NOT NULL); INSERT INTO schema_version VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	// Version 40 is the deployed multi-workspace schema, before this migration.
	for v := 0; v < 40; v++ {
		if noForeignKeyMigrations[v] {
			if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
		}
		if err := old.Tx(ctx, func(tx *Store) error {
			if err := migrations[v](ctx, tx); err != nil {
				return err
			}
			_, err := tx.q.ExecContext(ctx, `UPDATE schema_version SET version=?`, v+1)
			return err
		}); err != nil {
			t.Fatalf("migration %d: %v", v, err)
		}
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			t.Fatal(err)
		}
	}
	u := &User{Email: "legacy@example.com", Role: RoleAdmin}
	if err := old.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	// Use old columns: the production writer already expects the new schema.
	for _, q := range []string{
		`INSERT INTO api_tokens (id,user_id,name,token_hash,prefix,scope,role,created_at) VALUES ('tok_old','usr_admin','old','hash','keel_old','workspace','admin','2026-01-01T00:00:00Z')`,
		`INSERT INTO access_groups (id,name,role,created_at) VALUES ('ag_old','Legacy','member','2026-01-01T00:00:00Z')`,
		`INSERT INTO access_group_users VALUES ('ag_old','usr_admin')`,
		`UPDATE access_control_settings SET access_groups_enabled=1`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if role, err := s.WorkspaceRole(ctx, DefaultWorkspaceID, u); err != nil || role != RoleAdmin {
		t.Fatalf("legacy membership not preserved: %q %v", role, err)
	}
	u.Role = RoleViewer
	if err := s.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if role, _ := s.WorkspaceRole(ctx, DefaultWorkspaceID, u); role != RoleAdmin {
		t.Fatalf("instance demotion changed workspace membership: %q", role)
	}
	token, err := s.GetAPIToken(ctx, "tok_old")
	if err != nil || token.WorkspaceID == nil || *token.WorkspaceID != DefaultWorkspaceID {
		t.Fatalf("legacy token binding: %+v %v", token, err)
	}
	group, err := s.GetAccessGroup(ctx, "ag_old")
	if err != nil || group.WorkspaceID != DefaultWorkspaceID || len(group.UserIDs) != 1 {
		t.Fatalf("group or child rows lost: %+v %v", group, err)
	}
	if enabled, err := s.WorkspaceAccessGroupsEnabled(ctx, DefaultWorkspaceID); err != nil || !enabled {
		t.Fatalf("enforcement lost: %v %v", enabled, err)
	}
	var foreignKeys int
	if err := s.q.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys: %d %v", foreignKeys, err)
	}
	rows, err := s.q.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a broken foreign key")
	}
}

func TestOwnershipAndMoveAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u := &User{Email: "owner@example.com", Role: RoleMember}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	a, _ := s.CreateWorkspace(ctx, "A", u.ID)
	b, _ := s.CreateWorkspace(ctx, "B", u.ID)
	if err := s.SetWorkspaceMember(ctx, a.ID, u.ID, RoleViewer); !errors.Is(err, ErrLastWorkspaceOwner) {
		t.Fatalf("last owner demotion: %v", err)
	}
	u.Disabled = true
	if err := s.UpdateUser(ctx, u); !errors.Is(err, ErrLastWorkspaceOwner) {
		t.Fatalf("last owner disabled: %v", err)
	}
	if err := s.DeleteUser(ctx, u.ID); !errors.Is(err, ErrLastWorkspaceOwner) {
		t.Fatalf("last owner deleted: %v", err)
	}
	u.Disabled = false
	p := &Project{Name: "same", WorkspaceID: a.ID, GitHubUserID: &u.ID}
	if _, err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, &Project{Name: "same", WorkspaceID: b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMember(ctx, p.ID, u.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	token := &APIToken{UserID: u.ID, Name: "ci", Prefix: "keel_ci", Scope: ScopeProject, ProjectID: &p.ID, Role: RoleMember}
	if err := s.CreateAPIToken(ctx, token, "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveProject(ctx, p.ID, b.ID, u.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting move: %v", err)
	}
	if _, err := s.GetAPIToken(ctx, token.ID); err != nil {
		t.Fatalf("failed move revoked a token: %v", err)
	}
	if role, _ := s.MemberRole(ctx, p.ID, u.ID); role != RoleAdmin {
		t.Fatalf("failed move removed membership: %s", role)
	}
	project, _ := s.GetProject(ctx, p.ID)
	if project.WorkspaceID != a.ID || project.GitHubUserID == nil {
		t.Fatal("failed move changed source project")
	}
}
