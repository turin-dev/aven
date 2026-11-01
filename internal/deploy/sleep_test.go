package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// sleepFixture는 replica 하나짜리 로컬 서비스가 live인 상태다(sleep은 한 노드의 replica만 다룬다).
func sleepFixture(t *testing.T) (*scaleFixture, *store.Deployment, *restoreFake) {
	t.Helper()
	ctx := context.Background()
	f := newScaleFixture(t)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Placement, in.Replicas = nil, 1
	in.SleepWhenIdle, in.SleepAfterS = true, 1
	if err := f.st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	img, bu := "nginx:1", "image"
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec,
		NodeID: store.LocalNodeID, Status: store.DepLive, Image: &img, BuilderUsed: &bu, Env: map[string]string{"PORT": "80"}}
	f.st.CreateDeployment(ctx, d)
	d.Containers = []string{containerName("web", d.ID, 0)}
	f.st.UpdateDeployment(ctx, d)
	f.local.run(d.Containers[0])
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID)
	f.in, _ = f.st.GetInstance(ctx, in.ID)
	f.m.Cfg.WakeAddr = "127.0.0.1:1"
	drv := &restoreFake{localFake: &localFake{n: f.local}}
	f.m.localDrv = drv
	// 마지막 요청은 오래전이다.
	f.m.mu.Lock()
	f.m.awake[in.ID] = time.Now().Add(-time.Hour)
	f.m.mu.Unlock()
	return f, d, drv
}

func instStatus(t *testing.T, f *scaleFixture) string {
	t.Helper()
	in, err := f.st.GetInstance(context.Background(), f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	return in.Status
}

func TestSleepAndWake(t *testing.T) {
	ctx := context.Background()
	f, d, drv := sleepFixture(t)
	if err := f.m.Sleep(ctx, f.in); err != nil {
		t.Fatal(err)
	}
	if st := instStatus(t, f); st != store.InstSleeping {
		t.Fatalf("status after sleep: %s", st)
	}
	stopped := false
	for _, op := range f.log.list() {
		if strings.HasPrefix(op, "local:stop:"+d.Containers[0]) {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("container not stopped: %v", f.log.list())
	}
	// 잠든 서비스의 라우트는 wake handler를 가리킨다.
	if f.log.index("local:proxy:"+f.m.Cfg.WakeAddr) < 0 {
		t.Fatalf("wake route missing: %v", f.log.list())
	}
	if err := f.m.Wake(ctx, f.in.ID); err != nil {
		t.Fatal(err)
	}
	if st := instStatus(t, f); st != store.InstOnline {
		t.Fatalf("status after wake: %s", st)
	}
	if len(drv.started) != 1 || drv.started[0] != d.Containers[0] {
		t.Fatalf("started %v", drv.started)
	}
}

// 배포가 진행 중이면 재우지 않는다.
func TestSleepSkipsDeployingInstance(t *testing.T) {
	ctx := context.Background()
	f, _, _ := sleepFixture(t)
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstDeploying, nil)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if err := f.m.Sleep(ctx, in); err != nil {
		t.Fatal(err)
	}
	if st := instStatus(t, f); st != store.InstDeploying {
		t.Fatalf("status %s", st)
	}
	for _, op := range f.log.list() {
		if strings.HasPrefix(op, "local:stop:") {
			t.Fatalf("stopped a deploying service: %v", f.log.list())
		}
	}
}

// startHook은 컨테이너를 시작할 때 hook을 부른다(깨우는 도중의 상태 변화를 흉내 낸다).
type startHook struct {
	*restoreFake
	hook func()
}

func (s *startHook) Start(ctx context.Context, name string) error {
	s.hook()
	return s.restoreFake.Start(ctx, name)
}

// 깨우는 사이 새 배포가 시작되면(deploying) 그 상태를 online으로 덮어쓰지 않는다.
func TestWakeDoesNotOverwriteDeploying(t *testing.T) {
	ctx := context.Background()
	f, _, drv := sleepFixture(t)
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstSleeping, nil)
	f.m.localDrv = &startHook{restoreFake: drv, hook: func() { f.st.SetInstanceStatus(ctx, f.in.ID, store.InstDeploying, nil) }}
	f.m.Wake(ctx, f.in.ID)
	if st := instStatus(t, f); st != store.InstDeploying {
		t.Fatalf("status %s, want deploying", st)
	}
}
