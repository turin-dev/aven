package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestResourceLimitsBlockScaleBeforeContainersStart(t *testing.T) {
	f := newScaleFixture(t)
	ctx := context.Background()
	cpu := 1.0
	mem := 512
	capCPU := 2.0
	f.in.CPULimit = &cpu
	f.in.MemoryLimitMB = &mem
	if err := f.st.UpdateInstanceConfig(ctx, f.in); err != nil {
		t.Fatal(err)
	}
	d := f.live(t)
	if err := f.st.SetWorkspaceResourceLimits(ctx, store.DefaultWorkspaceID, store.ResourceLimits{CPU: &capCPU}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.ScaleReplicas(ctx, f.in, 3, "quota-test"); !errors.Is(err, store.ErrResourceLimit) {
		t.Fatalf("scale quota: %v", err)
	}
	got, err := f.st.GetDeployment(ctx, d.ID)
	if err != nil || len(got.AllContainers()) != 2 || len(f.local.names()) != 1 || len(f.remote.names()) != 1 {
		t.Fatalf("scale started containers: %+v %v", got, err)
	}
	// A failed healthcheck must release the temporary scale reservation.
	capCPU = 4
	if err := f.st.SetWorkspaceResourceLimits(ctx, store.DefaultWorkspaceID, store.ResourceLimits{CPU: &capCPU}); err != nil {
		t.Fatal(err)
	}
	f.remote.probeFail = true
	if _, err := f.m.ScaleReplicas(ctx, f.in, 4, "failed-healthcheck"); err == nil {
		t.Fatal("scale unexpectedly succeeded")
	}
	u, err := f.st.WorkspaceResourceUsage(ctx, store.DefaultWorkspaceID)
	if err != nil || u.CPU != 2 {
		t.Fatalf("failed scale kept capacity: %+v %v", u, err)
	}
	// Rollback can carry an older, larger replica plan than the current config.
	over := f.in.Spec
	over.Replicas = 6
	over.Placement = nil
	if _, err := f.m.Enqueue(ctx, f.in, EnqueueOpts{Trigger: "rollback", Image: d.Image, Spec: &over}); !errors.Is(err, store.ErrResourceLimit) {
		t.Fatalf("rollback quota: %v", err)
	}
}
