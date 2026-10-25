package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
)

func TestNoForeignKeyMigrationIndex(t *testing.T) {
	for v := range noForeignKeyMigrations {
		if reflect.ValueOf(migrations[v]).Pointer() != reflect.ValueOf(migrateWorkspaces).Pointer() && reflect.ValueOf(migrations[v]).Pointer() != reflect.ValueOf(migratePermissionBoundaries).Pointer() {
			t.Fatalf("noForeignKeyMigrations[%d] is not migrateWorkspaces", v)
		}
	}
}

// 작업 공간 이전 DB를 올린다: 기존 프로젝트와 그 하위 데이터는 그대로 기본 작업 공간에 남고,
// 전역 GitHub 연결은 연결했던 사용자의 연결이 되며, 외래 키 검사는 다시 켜진다.
func TestMigrateWorkspacesKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "k.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := &Store{db: db, q: db, box: new(*secret.Box)}
	box, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	old.SetSecretBox(box)
	// 작업 공간 마이그레이션 직전까지 올린다.
	var wsIdx int
	for v, migration := range migrations {
		if reflect.ValueOf(migration).Pointer() == reflect.ValueOf(migrateWorkspaces).Pointer() {
			wsIdx = v
			break
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER NOT NULL); INSERT INTO schema_version VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	for v := 0; v < wsIdx; v++ {
		if err := old.Tx(ctx, func(tx *Store) error {
			if err := migrations[v](ctx, tx); err != nil {
				return err
			}
			_, err := tx.q.ExecContext(ctx, `UPDATE schema_version SET version=?`, v+1)
			return err
		}); err != nil {
			t.Fatalf("migration %d: %v", v+1, err)
		}
	}
	prj := &Project{Name: DefaultProjectName}
	if _, err := old.CreateProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	pid := prj.ID
	u := &User{Email: "dev@example.com", Name: "dev", Role: RoleAdmin}
	if err := old.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := old.SetMember(ctx, pid, u.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	sealed := old.Seal("gho_legacy")
	if err := old.SetSetting(ctx, "github_oauth", `{"login":"dev-gh","scopes":"repo","connected_at":"2026-01-02T03:04:05Z","token":"`+sealed+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.q.ExecContext(ctx, `INSERT INTO audit_log (id, actor_id, actor, kind, message, created_at) VALUES ('aud_1', ?, 'dev', 'github.connected', 'x', '2026-01-02T03:04:05.000000000Z')`, u.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetSecretBox(box)
	p, err := s.GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if p.WorkspaceID != DefaultWorkspaceID || p.GitHubUserID == nil || *p.GitHubUserID != u.ID {
		t.Fatalf("migrated project: %+v", p)
	}
	if envs, _ := s.ListEnvironments(ctx, pid); len(envs) != 1 {
		t.Fatalf("environments lost: %+v", envs)
	}
	if role, _ := s.MemberRole(ctx, pid, u.ID); role != RoleAdmin {
		t.Fatalf("project membership lost: %q", role)
	}
	c, err := s.GetGitHubConnection(ctx, u.ID)
	if err != nil || c.Login != "dev-gh" {
		t.Fatalf("github connection: %+v %v", c, err)
	}
	if tok, _ := s.Unseal(c.Token); tok != "gho_legacy" {
		t.Fatalf("token: %q", tok)
	}
	if v, _ := s.GetSetting(ctx, "github_oauth"); v != "" {
		t.Fatal("the global GitHub setting must be removed")
	}
	var fk int
	s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Fatal("foreign keys must be back on")
	}
	// 외래 키가 실제로 동작한다(프로젝트를 지우면 환경도 지워진다).
	if err := s.DeleteProject(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if envs, _ := s.ListEnvironments(ctx, pid); len(envs) != 0 {
		t.Fatal("cascade from the rebuilt projects table")
	}
	var sqlText string
	s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='projects'`).Scan(&sqlText)
	if strings.Contains(sqlText, "name TEXT NOT NULL UNIQUE") || !strings.Contains(sqlText, "UNIQUE (workspace_id, name)") {
		t.Fatalf("projects schema: %s", sqlText)
	}
}

func TestWorkspaceStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := &User{Email: "a@example.com", Name: "", Role: RoleOwner}
	b := &User{Email: "b@example.com", Name: "Bee", Role: RoleViewer}
	for _, u := range []*User{a, b} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	wa, err := s.EnsurePersonalWorkspace(ctx, a)
	if err != nil || !wa.Personal || wa.Name != "a" {
		t.Fatalf("personal: %+v %v", wa, err)
	}
	if again, _ := s.EnsurePersonalWorkspace(ctx, a); again.ID != wa.ID {
		t.Fatal("personal workspace must be created once")
	}
	pa := &Project{Name: "same", WorkspaceID: wa.ID}
	if _, err := s.CreateProject(ctx, pa); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, &Project{Name: "same"}); err != nil {
		t.Fatalf("same name in another workspace: %v", err)
	}
	if _, err := s.CreateProject(ctx, &Project{Name: "same", WorkspaceID: wa.ID}); err != ErrConflict {
		t.Fatalf("duplicate in a workspace: %v", err)
	}
	// 기본 작업 공간 owner(a)는 b의 개인 작업 공간 프로젝트에 역할이 없다.
	wb, _ := s.EnsurePersonalWorkspace(ctx, b)
	pb := &Project{Name: "bee", WorkspaceID: wb.ID}
	s.CreateProject(ctx, pb)
	if role, _ := s.EffectiveProjectRole(ctx, pb.ID, a); role != "" {
		t.Fatalf("instance owner in another person's workspace: %q", role)
	}
	if role, _ := s.EffectiveProjectRole(ctx, pb.ID, b); role != RoleAdmin {
		t.Fatalf("owner of the personal workspace: %q", role)
	}
	if err := s.DeleteUser(ctx, b.ID); err != ErrWorkspaceNotEmpty {
		t.Fatalf("delete user with projects: %v", err)
	}
	s.DeleteProject(ctx, pb.ID)
	if err := s.DeleteUser(ctx, b.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := s.GetWorkspace(ctx, wb.ID); err != ErrNotFound {
		t.Fatal("the empty personal workspace goes with the user")
	}
}
