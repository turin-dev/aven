package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestImageUpdateHistoryAndAppliedDigest(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "image-updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projects, _ := s.ListProjects(ctx)
	envs, _ := s.ListEnvironments(ctx, projects[0].ID)
	svc := &Service{ProjectID: projects[0].ID, Name: "web", SourceType: "image"}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &Instance{ServiceID: svc.ID, EnvironmentID: envs[0].ID, Variables: map[string]string{}, Status: InstOnline}
	in.Image = ptr("ghcr.io/acme/web:latest")
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	u := &ImageUpdate{InstanceID: in.ID, CurrentImage: *in.Image, TargetImage: *in.Image, Digest: "sha256:new", DigestImage: "ghcr.io/acme/web@sha256:new"}
	first, err := s.SaveAvailableImageUpdate(ctx, u)
	if err != nil || first.ID == "" || first.Status != "available" {
		t.Fatalf("save update: %+v, %v", first, err)
	}
	second, err := s.SaveAvailableImageUpdate(ctx, &ImageUpdate{InstanceID: in.ID, CurrentImage: *in.Image, TargetImage: *in.Image,
		Digest: "sha256:new", DigestImage: "ghcr.io/acme/web@sha256:new"})
	if err != nil || second.ID != first.ID {
		t.Fatalf("duplicate digest created another history row: %+v, %v", second, err)
	}
	if ok, err := s.ClaimImageUpdate(ctx, first.ID, true); err != nil || !ok {
		t.Fatalf("claim: %t, %v", ok, err)
	}
	d := &Deployment{ServiceID: svc.ID, EnvironmentID: envs[0].ID, InstanceID: in.ID, Trigger: "image_update", Image: ptr(first.DigestImage), BuilderUsed: ptr("image"), Spec: in.Spec, ReqSpec: in.Spec}
	if err := s.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImageUpdateDeployment(ctx, first.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := s.CompleteImageUpdate(ctx, d.ID, "applied", "")
	if err != nil || completed.Status != "applied" {
		t.Fatalf("complete: %+v, %v", completed, err)
	}
	state, err := s.GetImageUpdateState(ctx, in.ID)
	if err != nil || state.LastDigest != "sha256:new" {
		t.Fatalf("state: %+v, %v", state, err)
	}
	history, err := s.ListImageUpdates(ctx, in.ID, 50)
	if err != nil || len(history) != 1 || history[0].Status != "applied" {
		t.Fatalf("history: %+v, %v", history, err)
	}
}
