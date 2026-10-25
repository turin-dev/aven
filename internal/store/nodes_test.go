package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLocalNodeAndPlacementPersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nodes.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.LocalNode(ctx)
	if err != nil || local.ID != LocalNodeID || !local.Schedulable || !local.IsLocal {
		t.Fatalf("local node: %+v, %v", local, err)
	}
	local.Name = "test-node"
	local.Schedulable = false
	local.Labels["zone"] = "test"
	if err := s.UpdateLocalNode(ctx, local); err != nil {
		t.Fatal(err)
	}
	ps, err := s.ListProjects(ctx)
	if err != nil || len(ps) == 0 {
		t.Fatalf("projects: %v, %v", ps, err)
	}
	envs, err := s.ListEnvironments(ctx, ps[0].ID)
	if err != nil || len(envs) == 0 {
		t.Fatalf("environments: %v, %v", envs, err)
	}
	svc := &Service{ProjectID: ps[0].ID, Name: "node-test", SourceType: "image"}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &Instance{ServiceID: svc.ID, EnvironmentID: envs[0].ID}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	dep := &Deployment{ServiceID: svc.ID, EnvironmentID: envs[0].ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec}
	if err := s.CreateDeployment(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err = s.LocalNode(ctx)
	if err != nil || local.Name != "test-node" || local.Schedulable || local.Labels["zone"] != "test" {
		t.Fatalf("local after restart: %+v, %v", local, err)
	}
	loaded, err := s.GetInstance(ctx, in.ID)
	if err != nil || loaded.NodeID != LocalNodeID {
		t.Fatalf("instance node: %+v, %v", loaded, err)
	}
	deployment, err := s.GetDeployment(ctx, dep.ID)
	if err != nil || deployment.NodeID != LocalNodeID {
		t.Fatalf("deployment node: %+v, %v", deployment, err)
	}
}
