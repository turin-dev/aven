package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func resourcePtr[T int | int64 | float64](n T) *T { return &n }

func resourceService(t *testing.T, st *Store, p *Project, env *Environment, name string, spec Spec) *Instance {
	t.Helper()
	svc := &Service{ProjectID: p.ID, Name: name, SourceType: "image"}
	if err := st.CreateService(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	return &Instance{ServiceID: svc.ID, EnvironmentID: env.ID, Spec: spec}
}

func TestResourceLimitsAllocationAndRollback(t *testing.T) {
	ctx := context.Background()
	st, p, e := layoutStore(t)
	limits := ResourceLimits{resourcePtr(2.0), resourcePtr(int64(1024)), resourcePtr(int64(2048))}
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, limits); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		in := resourceService(t, st, p, e, fmt.Sprintf("service-%d", i), Spec{})
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		if *in.CPULimit != 1 || *in.MemoryLimitMB != 512 {
			t.Fatalf("defaults: %+v", in.Spec)
		}
		vol := &Volume{ProjectID: p.ID, EnvironmentID: e.ID, InstanceID: &in.ID, Name: fmt.Sprintf("data-%d", i), MountPath: "/data"}
		if err := st.CreateVolume(ctx, vol); err != nil {
			t.Fatal(err)
		}
		if *vol.SizeLimitMB != 1024 {
			t.Fatalf("volume default: %+v", vol)
		}
	}
	in := resourceService(t, st, p, e, "over", Spec{})
	if err := st.CreateInstance(ctx, in); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("over quota: %v", err)
	}
	if _, err := st.GetInstance(ctx, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected instance persisted: %v", err)
	}
	instances, _ := st.ListInstancesByEnv(ctx, e.ID)
	current := instances[0]
	current.CPULimit = nil
	if err := st.UpdateInstanceConfig(ctx, current); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("unlimited bypass: %v", err)
	}
	got, _ := st.GetInstance(ctx, current.ID)
	if got.CPULimit == nil || *got.CPULimit != 1 {
		t.Fatal("failed config change persisted")
	}
	vols, _ := st.ListVolumesByProject(ctx, p.ID)
	v := vols[0]
	v.SizeLimitMB = resourcePtr(2048)
	if err := st.UpdateVolume(ctx, v); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("expand volume beyond quota: %v", err)
	}
	saved, _ := st.GetVolume(ctx, v.ID)
	if *saved.SizeLimitMB != 1024 {
		t.Fatal("failed volume resize persisted")
	}
	limits.CPU = resourcePtr(1.0)
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, limits); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("lower limit: %v", err)
	}
	l, _ := st.WorkspaceResourceLimits(ctx, DefaultWorkspaceID)
	if *l.CPU != 2 {
		t.Fatal("failed limit change persisted")
	}
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, ResourceLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateInstanceConfig(ctx, current); err != nil {
		t.Fatal(err)
	}
}

func TestResourceLimitsConcurrentAdmission(t *testing.T) {
	ctx := context.Background()
	st, p, e := layoutStore(t)
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, ResourceLimits{CPU: resourcePtr(1.0)}); err != nil {
		t.Fatal(err)
	}
	var inputs []*Instance
	for i := 0; i < 8; i++ {
		inputs = append(inputs, resourceService(t, st, p, e, fmt.Sprintf("racer-%d", i), Spec{CPULimit: resourcePtr(.75)}))
	}
	start := make(chan struct{})
	results := make(chan error, len(inputs))
	var wg sync.WaitGroup
	for _, in := range inputs {
		wg.Add(1)
		go func(in *Instance) { defer wg.Done(); <-start; results <- st.CreateInstance(ctx, in) }(in)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrResourceLimit) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("admitted %d concurrent allocations", success)
	}
	u, err := st.WorkspaceResourceUsage(ctx, DefaultWorkspaceID)
	if err != nil || u.CPU != .75 || u.Instances != 1 {
		t.Fatalf("usage: %+v %v", u, err)
	}
}

func TestResourceLimitsReserveLiveQueuedAndAutoscale(t *testing.T) {
	ctx := context.Background()
	st, p, e := layoutStore(t)
	l := ResourceLimits{CPU: resourcePtr(4.0), MemoryMB: resourcePtr(int64(4096))}
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, l); err != nil {
		t.Fatal(err)
	}
	in := resourceService(t, st, p, e, "runtime", Spec{CPULimit: resourcePtr(1.0), MemoryLimitMB: resourcePtr(512), Replicas: 2})
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	d := &Deployment{ServiceID: in.ServiceID, EnvironmentID: e.ID, InstanceID: in.ID, Spec: in.Spec, Status: DepLive}
	if err := st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInstanceStatus(ctx, in.ID, InstOnline, &d.ID); err != nil {
		t.Fatal(err)
	}
	in.CPULimit = resourcePtr(.5)
	in.Replicas = 1
	if err := st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	u, _ := st.WorkspaceResourceUsage(ctx, DefaultWorkspaceID)
	if u.CPU != 2 {
		t.Fatalf("live capacity released before redeploy: %+v", u)
	}
	if err := st.PrepareDeploymentResources(ctx, d, 5); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("runtime scale bypass: %v", err)
	}
	u, _ = st.WorkspaceResourceUsage(ctx, DefaultWorkspaceID)
	if u.CPU != 2 {
		t.Fatalf("rejected scale reservation persisted: %+v", u)
	}
	d.Spec.Replicas = 6
	if err := st.PrepareDeploymentResources(ctx, d, 6); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("config-as-code bypass: %v", err)
	}
	queued := &Deployment{ServiceID: in.ServiceID, EnvironmentID: e.ID, InstanceID: in.ID, Spec: Spec{CPULimit: resourcePtr(1.0), MemoryLimitMB: resourcePtr(512), Replicas: 5}}
	if err := st.CreateDeployment(ctx, queued); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("rollback/queued bypass: %v", err)
	}
	in.Autoscale = &Autoscale{Enabled: true, MinReplicas: 1, MaxReplicas: 10}
	if err := st.UpdateInstanceConfig(ctx, in); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("autoscale max bypass: %v", err)
	}
}

func TestResourceLimitsSandboxAndWorkspaceMove(t *testing.T) {
	ctx := context.Background()
	st, p, e := layoutStore(t)
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, ResourceLimits{CPU: resourcePtr(1.0)}); err != nil {
		t.Fatal(err)
	}
	b := &Sandbox{ProjectID: p.ID, EnvironmentID: e.ID, ContainerName: "sandbox-one", CPUCores: .75, MemoryMB: 256}
	if err := st.CreateSandbox(ctx, b); err != nil {
		t.Fatal(err)
	}
	b2 := &Sandbox{ProjectID: p.ID, EnvironmentID: e.ID, ContainerName: "sandbox-two", CPUCores: .5, MemoryMB: 256}
	if err := st.CreateSandbox(ctx, b2); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("sandbox bypass: %v", err)
	}
	u := &User{Email: "quota-owner@example.com", Name: "Owner", Role: RoleMember}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	ws, err := st.CreateWorkspace(ctx, "Destination", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkspaceResourceLimits(ctx, ws.ID, ResourceLimits{CPU: resourcePtr(.5)}); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveProject(ctx, p.ID, ws.ID); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("move bypass: %v", err)
	}
	prj, _ := st.GetProject(ctx, p.ID)
	if prj.WorkspaceID != DefaultWorkspaceID {
		t.Fatal("failed move persisted")
	}
}

func TestResourceLimitsMigrationAndUnlimitedResources(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Project{Name: "legacy"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	in := resourceService(t, st, p, env, "unlimited", Spec{})
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE workspace_resource_limits`, `ALTER TABLE deployments DROP COLUMN resource_replicas`, `DROP TABLE domain_states`,
		`ALTER TABLE nodes DROP COLUMN ingress_mode`, `ALTER TABLE nodes DROP COLUMN ingress_hostname`, `ALTER TABLE nodes DROP COLUMN tcp_hostname`, `ALTER TABLE nodes DROP COLUMN ipv6_pool`, `UPDATE schema_version SET version=41`} {
		if _, err := st.q.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l, err := st.WorkspaceResourceLimits(ctx, DefaultWorkspaceID)
	if err != nil || l.CPU != nil || l.MemoryMB != nil || l.VolumeMB != nil {
		t.Fatalf("migration changed limits: %+v %v", l, err)
	}
	if _, err := st.GetInstance(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkspaceResourceLimits(ctx, DefaultWorkspaceID, ResourceLimits{CPU: resourcePtr(2.0)}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("unlimited active allocation accepted: %v", err)
	}
	u, err := st.WorkspaceResourceUsage(ctx, DefaultWorkspaceID)
	if err != nil || u.UnlimitedCPU != 1 || u.UnlimitedMemory != 1 {
		t.Fatalf("usage: %+v %v", u, err)
	}
}
