package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPreviewEnvAndPRSettings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &Project{Name: "p"}
	prod, err := s.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if prod.Kind != EnvStandard || !p.PREnvironments.AutoDelete || !p.PREnvironments.Domain {
		t.Fatalf("defaults: %+v %+v", prod, p.PREnvironments)
	}
	got, _ := s.GetProject(ctx, p.ID)
	if got.PREnvironments.Enabled || !got.PREnvironments.AutoDelete || !got.PREnvironments.Domain || got.PRFromForks || got.PRFromBots {
		t.Fatalf("stored defaults: %+v", got.PREnvironments)
	}
	got.PREnvironments = PRSettings{Enabled: true, BaseEnvironmentID: &prod.ID, AutoDelete: false, Domain: false, Focused: true}
	got.PRFromForks = true
	got.PRFromBots = true
	if err := s.UpdateProject(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetProject(ctx, p.ID)
	if !got.PREnvironments.Enabled || got.PREnvironments.AutoDelete || got.PREnvironments.Domain || !got.PREnvironments.Focused || !got.PRFromForks || !got.PRFromBots || *got.PREnvironments.BaseEnvironmentID != prod.ID {
		t.Fatalf("round trip: %+v fork=%v bot=%v", got.PREnvironments, got.PRFromForks, got.PRFromBots)
	}

	n, url, br, repo := 12, "https://github.com/a/b/pull/12", "feat", "github.com/a/b"
	baseID := prod.ID
	e := &Environment{ProjectID: p.ID, Name: "pr-12", Kind: EnvPreview, PRNumber: &n, PRURL: &url, PRBranch: &br, PRRepo: &repo, BaseEnvironmentID: &baseID}
	if err := s.CreateEnvironment(ctx, e); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListPreviewEnvironments(ctx, repo, 12)
	if len(list) != 1 || list[0].ID != e.ID || *list[0].PRNumber != 12 || *list[0].PRBranch != "feat" || list[0].BaseEnvironmentID == nil || *list[0].BaseEnvironmentID != prod.ID {
		t.Fatalf("preview lookup: %+v", list)
	}
	if l, _ := s.ListPreviewEnvironments(ctx, repo, 13); len(l) != 0 {
		t.Error("wrong number matched")
	}
	std, _ := s.GetEnvironment(ctx, prod.ID)
	if std.Kind != EnvStandard || std.PRNumber != nil || std.PRURL != nil {
		t.Errorf("standard env: %+v", std)
	}
}

func TestWaitingDeployments(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &Project{Name: "p"}
	env, _ := s.CreateProject(ctx, p)
	sv := &Service{ProjectID: p.ID, Name: "web", SourceType: "repo"}
	s.CreateService(ctx, sv)
	in := &Instance{ServiceID: sv.ID, EnvironmentID: env.ID}
	s.CreateInstance(ctx, in)
	sha := "abc"
	d := &Deployment{ServiceID: sv.ID, EnvironmentID: env.ID, InstanceID: in.ID, Trigger: "github", Status: DepWaiting, CommitSHA: &sha}
	s.CreateDeployment(ctx, d)
	if un, _ := s.ListUnfinished(ctx); len(un) != 0 {
		t.Fatalf("waiting counted as unfinished: %d", len(un))
	}
	if w, _ := s.ListWaitingBySHA(ctx, sha); len(w) != 1 {
		t.Fatal("waiting by sha")
	}
	msg := "CI 실패"
	if ok, _ := s.TransitionDeployment(ctx, d.ID, DepWaiting, DepSkipped, &msg); !ok {
		t.Fatal("transition")
	}
	if ok, _ := s.TransitionDeployment(ctx, d.ID, DepWaiting, DepQueued, nil); ok {
		t.Fatal("second transition must not apply")
	}
	got, _ := s.GetDeployment(ctx, d.ID)
	if got.Status != DepSkipped || got.FinishedAt == nil || *got.Error != msg {
		t.Fatalf("after transition: %+v", got)
	}
	if DepWaiting.Terminal() {
		t.Error("waiting is not terminal")
	}
}
