package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSandboxAndCheckpointLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects: %v, %v", projects, err)
	}
	envs, err := s.ListEnvironments(ctx, projects[0].ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %v, %v", envs, err)
	}
	env := envs[0]
	b := &Sandbox{ProjectID: projects[0].ID, EnvironmentID: env.ID, NodeID: LocalNodeID,
		ContainerName: "keel-sbx-test", NetworkName: "keel-sbx-test", Image: "ubuntu:24.04",
		MemoryMB: 512, CPUCores: 1, IdleTimeoutMinutes: 30}
	if err := s.CreateSandbox(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSandbox(ctx, b.ID)
	if err != nil || got.Status != SandboxCreating || got.NetworkMode != SandboxIsolated || got.ExpiresAt == nil {
		t.Fatalf("sandbox: %+v, %v", got, err)
	}
	if want := got.LastActivityAt.Add(30 * time.Minute); !got.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", got.ExpiresAt, want)
	}
	if err := s.MarkSandboxRunning(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListSandboxes(ctx, env.ID); err != nil || len(list) != 1 || list[0].Status != SandboxRunning {
		t.Fatalf("list sandboxes: %v, %v", list, err)
	}
	now := time.Now().UTC()
	if expired, err := s.ListExpiredSandboxes(ctx, now); err != nil || len(expired) != 0 {
		t.Fatalf("early expiry: %v, %v", expired, err)
	}
	if expired, err := s.ListExpiredSandboxes(ctx, now.Add(31*time.Minute)); err != nil || len(expired) != 1 || expired[0].ID != b.ID {
		t.Fatalf("expired sandboxes: %v, %v", expired, err)
	}
	// 활동하면 유휴 타이머가 다시 시작된다.
	activity := now.Add(20 * time.Minute)
	if err := s.TouchSandbox(ctx, b.ID, activity); err != nil {
		t.Fatal(err)
	}
	if expired, err := s.ListExpiredSandboxes(ctx, now.Add(31*time.Minute)); err != nil || len(expired) != 0 {
		t.Fatalf("touched sandbox expired: %v, %v", expired, err)
	}
	got, _ = s.GetSandbox(ctx, b.ID)
	if !got.LastActivityAt.Equal(activity) || !got.ExpiresAt.Equal(activity.Add(30*time.Minute)) {
		t.Fatalf("activity: %+v", got)
	}

	// idle 0 = 유휴 삭제 없음.
	forever := &Sandbox{ProjectID: b.ProjectID, EnvironmentID: env.ID, NodeID: LocalNodeID, ContainerName: "keel-sbx-forever",
		NetworkName: "keel-" + env.ID, NetworkMode: SandboxPrivate, Image: "ubuntu:24.04", MemoryMB: 512, CPUCores: 1}
	if err := s.CreateSandbox(ctx, forever); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSandbox(ctx, forever.ID, now); err != nil {
		t.Fatal(err)
	}
	if expired, err := s.ListExpiredSandboxes(ctx, now.Add(1000*time.Hour)); err != nil || len(expired) != 1 || expired[0].ID != b.ID {
		t.Fatalf("idle 0 sandbox expired: %v, %v", expired, err)
	}
	if all, err := s.ListAllSandboxes(ctx); err != nil || len(all) != 2 {
		t.Fatalf("all sandboxes: %v, %v", all, err)
	}

	cp := &SandboxCheckpoint{ProjectID: b.ProjectID, EnvironmentID: env.ID, NodeID: b.NodeID, Name: "base", Image: "keel/sandbox:scp-one"}
	if oldNode, oldImage, err := s.SaveSandboxCheckpoint(ctx, cp); err != nil || oldImage != "" || oldNode != "" {
		t.Fatalf("save: %q %q %v", oldNode, oldImage, err)
	}
	// 같은 이름으로 다시 캡처하면 바뀌고 이전 이미지를 돌려준다.
	again := &SandboxCheckpoint{ProjectID: b.ProjectID, EnvironmentID: env.ID, NodeID: b.NodeID, Name: "base", Image: "keel/sandbox:scp-two"}
	if _, oldImage, err := s.SaveSandboxCheckpoint(ctx, again); err != nil || oldImage != "keel/sandbox:scp-one" || again.ID != cp.ID {
		t.Fatalf("replace: %q %v (%s vs %s)", oldImage, err, again.ID, cp.ID)
	}
	if list, err := s.ListSandboxCheckpoints(ctx, env.ID); err != nil || len(list) != 1 || list[0].Image != "keel/sandbox:scp-two" {
		t.Fatalf("checkpoints: %v, %v", list, err)
	}
	for _, ref := range []string{"base", cp.ID} {
		if found, err := s.FindSandboxCheckpoint(ctx, env.ID, ref); err != nil || found.ID != cp.ID {
			t.Fatalf("find %q: %v, %v", ref, found, err)
		}
	}
	if _, err := s.FindSandboxCheckpoint(ctx, "env_other", "base"); err != ErrNotFound {
		t.Fatalf("checkpoint visible from another environment: %v", err)
	}
	other := &SandboxCheckpoint{ProjectID: b.ProjectID, EnvironmentID: env.ID, NodeID: b.NodeID, Name: "other", Image: "keel/sandbox:scp-three"}
	if _, _, err := s.SaveSandboxCheckpoint(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameSandboxCheckpoint(ctx, other.ID, "base"); err != ErrConflict {
		t.Fatalf("rename onto existing name: %v", err)
	}
	if err := s.RenameSandboxCheckpoint(ctx, other.ID, "node-base"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSandboxCheckpoint(ctx, cp.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSandbox(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
}
