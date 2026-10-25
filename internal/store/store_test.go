package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDefaultProjectAndCRUD(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ps, _ := s.ListProjects(ctx)
	if len(ps) != 1 || ps[0].Name != DefaultProjectName {
		t.Fatalf("default project: %+v", ps)
	}
	envs, _ := s.ListEnvironments(ctx, ps[0].ID)
	if len(envs) != 1 || envs[0].Name != "production" || ps[0].DefaultEnvironmentID != envs[0].ID {
		t.Fatalf("default env: %+v", envs)
	}
	svc := &Service{ProjectID: ps[0].ID, Name: "web", SourceType: "repo"}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateService(ctx, &Service{ProjectID: ps[0].ID, Name: "web", SourceType: "repo"}); err != ErrConflict {
		t.Fatalf("duplicate name: %v", err)
	}
	in := &Instance{ServiceID: svc.ID, EnvironmentID: envs[0].ID, Variables: map[string]string{"A": "1"}}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstanceFor(ctx, svc.ID, envs[0].ID)
	if err != nil || got.Replicas != 1 || got.Builder != "auto" || got.Variables["A"] != "1" {
		t.Fatalf("instance: %+v %v", got, err)
	}
	d := &Deployment{ServiceID: svc.ID, EnvironmentID: envs[0].ID, InstanceID: in.ID, Trigger: "manual", VarsRaw: in.Variables, Spec: in.Spec}
	if err := s.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	un, _ := s.ListUnfinished(ctx)
	if len(un) != 1 {
		t.Fatalf("unfinished: %d", len(un))
	}
	// 서비스를 지우면 인스턴스·배포도 지워진다
	if err := s.DeleteService(ctx, svc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDeployment(ctx, d.ID); err != ErrNotFound {
		t.Fatalf("cascade: %v", err)
	}
}

func TestDefaultProjectNotRecreated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "k.db")
	s, _ := Open(path)
	ps, _ := s.ListProjects(ctx)
	s.DeleteProject(ctx, ps[0].ID)
	s.Close()
	s, _ = Open(path)
	defer s.Close()
	if ps, _ := s.ListProjects(ctx); len(ps) != 0 {
		t.Fatal("deleted default project was recreated")
	}
}

func TestMigrateFromV0(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v0.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE apps (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, repo_url TEXT NOT NULL, branch TEXT NOT NULL, dockerfile TEXT NOT NULL,
	build_context TEXT NOT NULL, port INTEGER NOT NULL, healthcheck_path TEXT NOT NULL, domains TEXT NOT NULL, env TEXT NOT NULL,
	status TEXT NOT NULL, current_deployment_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE deployments (id TEXT PRIMARY KEY, app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, status TEXT NOT NULL,
	commit_sha TEXT, image TEXT, container TEXT, error TEXT, trigger TEXT NOT NULL, created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT);
INSERT INTO apps VALUES ('app_1','hello','file:///tmp/good','main','Dockerfile','.',8000,'/','["hello.example.com"]','{"GREETING":"hi"}','running','dep_aaaaaaaaaaaaa','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO deployments VALUES ('dep_aaaaaaaaaaaaa','app_1','live','abc','keel/hello:dep_a','keel-hello-aaaaaaaa',NULL,'manual','2026-01-01T00:00:00Z',NULL,NULL);
INSERT INTO deployments VALUES ('dep_bbbbbbbbbbbbb','app_1','superseded','abd','keel/hello:dep_b','keel-hello-bbbbbbbb',NULL,'manual','2025-12-01T00:00:00Z',NULL,NULL);
`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ps, _ := s.ListProjects(ctx)
	if len(ps) != 1 {
		t.Fatalf("projects: %d", len(ps))
	}
	svcs, _ := s.ListServices(ctx, ps[0].ID)
	if len(svcs) != 1 || svcs[0].Name != "hello" {
		t.Fatalf("services: %+v", svcs)
	}
	in, err := s.GetInstanceFor(ctx, svcs[0].ID, ps[0].DefaultEnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != InstOnline || in.CurrentDeploymentID == nil || *in.CurrentDeploymentID != "dep_aaaaaaaaaaaaa" ||
		*in.Port != 8000 || in.Domains[0].Host != "hello.example.com" || in.Variables["GREETING"] != "hi" {
		t.Fatalf("instance: %+v", in)
	}
	deps, _ := s.ListDeployments(ctx, in.ID, 10)
	if len(deps) != 2 || deps[0].Status != DepLive || deps[1].Status != DepRemoved || deps[0].Containers[0] != "keel-hello-aaaaaaaa" {
		t.Fatalf("deployments: %+v", deps)
	}
	if s.tableExists(ctx, "apps") {
		t.Error("legacy table should be dropped")
	}
}
