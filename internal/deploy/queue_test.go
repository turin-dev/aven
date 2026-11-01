package deploy

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// 취소된 queued 배포는 worker가 꺼내도 실행하지 않는다(취소와 시작 중 하나만 이긴다).
func TestExecuteDoesNotRunCancelledDeployment(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	d, err := f.m.Enqueue(ctx, in, EnqueueOpts{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.Cancel(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	f.m.execute(d.ID)
	got, _ := f.st.GetDeployment(ctx, d.ID)
	if got.Status != store.DepCancelled {
		t.Fatalf("status %s, want cancelled", got.Status)
	}
	for _, op := range f.log.list() {
		if strings.Contains(op, ":run:") {
			t.Fatalf("cancelled deployment started a container: %v", f.log.list())
		}
	}
	if cur, _ := f.st.GetInstance(ctx, f.in.ID); cur.Status == store.InstDeploying {
		t.Fatalf("instance left in deploying")
	}
	// 이미 가져간 배포(initializing)를 Cancel하면 queued → cancelled로 덮어쓰지 않고 실행 중인 배포를 취소한다.
	d2, err := f.m.Enqueue(ctx, in, EnqueueOpts{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	f.st.TransitionDeployment(ctx, d2.ID, store.DepQueued, store.DepInitializing, nil)
	cancelled := make(chan struct{})
	f.m.mu.Lock()
	f.m.cancels[d2.ID] = func(error) { close(cancelled) }
	f.m.mu.Unlock()
	if err := f.m.Cancel(ctx, d2.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("running deployment was not cancelled")
	}
	if got, _ := f.st.GetDeployment(ctx, d2.ID); got.Status != store.DepInitializing {
		t.Fatalf("running deployment status overwritten: %s", got.Status)
	}
}

// 큐가 꽉 차도 dispatch는 막히지 않고 가장 오래 기다린 배포를 대체된 것으로 끝낸다.
func TestDispatchDoesNotBlockOnFullQueue(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Dispatch = nil
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	// worker가 긴 배포를 실행 중인 상황: 큐는 있지만 아무도 꺼내지 않는다.
	q := make(chan string, queueSize)
	f.m.mu.Lock()
	f.m.queues[in.ID] = q
	f.m.mu.Unlock()
	var first string
	for i := 0; i < queueSize; i++ {
		d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec}
		if err := f.st.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = d.ID
		}
		q <- d.ID
	}
	last := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec}
	f.st.CreateDeployment(ctx, last)
	done := make(chan struct{})
	go func() { f.m.dispatch(in.ID, last.ID); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch blocked on a full queue")
	}
	if got, _ := f.st.GetDeployment(ctx, first); got.Status != store.DepRemoved {
		t.Fatalf("oldest queued deployment: %s, want removed", got.Status)
	}
	if len(q) != queueSize {
		t.Fatalf("queue length %d", len(q))
	}
}

// worker는 큐가 비면 끝나고 큐를 지운다. 다음 dispatch는 새 worker를 만든다.
func TestWorkerExitsWhenIdle(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Dispatch = nil
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	for round := 0; round < 2; round++ {
		d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec}
		f.st.CreateDeployment(ctx, d)
		f.st.TransitionDeployment(ctx, d.ID, store.DepQueued, store.DepCancelled, nil) // execute는 바로 돌아온다
		f.m.dispatch(in.ID, d.ID)
		deadline := time.Now().Add(5 * time.Second)
		for {
			f.m.mu.Lock()
			_, ok := f.m.queues[in.ID]
			f.m.mu.Unlock()
			if !ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: worker did not exit", round)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// cron 배포도 배포 중에 서비스가 중지되면 다시 scheduled로 되돌리지 않는다.
func TestCutoverCronRespectsStop(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	old := f.live(t)
	tg, err := f.m.Load(ctx, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstOffline, nil)
	cron := "*/5 * * * *"
	sp := f.in.Spec
	sp.CronSchedule = &cron
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Trigger: "manual", Spec: sp, ReqSpec: sp, NodeID: store.LocalNodeID}
	f.st.CreateDeployment(ctx, d)
	if err := f.m.cutoverCron(ctx, tg, d, testBook(t)); err == nil {
		t.Fatal("cron cutover went ahead on a stopped service")
	}
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if in.Status != store.InstOffline || in.CurrentDeploymentID == nil || *in.CurrentDeploymentID != old.ID {
		t.Fatalf("instance %s current %v", in.Status, in.CurrentDeploymentID)
	}
}

// 컷오버가 중지보다 늦으면 실패하고, 이전 컨테이너(recreate로 멈춘 것)는 다시 시작하지 않는다.
func TestCutoverAfterStopFails(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	old := f.live(t)
	tg, _ := f.m.Load(ctx, f.in.ID)
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstOffline, nil)
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Trigger: "manual", Spec: f.in.Spec, ReqSpec: f.in.Spec, NodeID: store.LocalNodeID}
	f.st.CreateDeployment(ctx, d)
	if err := f.m.cutover(ctx, tg, d, testBook(t), nil); err == nil {
		t.Fatal("cutover went ahead on a stopped service")
	}
	if in, _ := f.st.GetInstance(ctx, f.in.ID); in.Status != store.InstOffline || *in.CurrentDeploymentID != old.ID {
		t.Fatalf("instance %s current %v", in.Status, *in.CurrentDeploymentID)
	}
}

// healthcheck가 없는(none) 서비스는 healthcheck 제한 시간이 짧아도(2초) 배포된다.
func TestNoneHealthcheckWithShortTimeout(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.HealthcheckType = "none"
	in.HealthcheckTimeoutS = 2
	if err := f.st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	d := f.executeDeploy(t)
	if d.Status != store.DepLive {
		t.Fatalf("status %s: %v", d.Status, d.Error)
	}
}

// managedFake는 ListManaged로 주어진 컨테이너(라벨)를 돌려준다.
type managedFake struct {
	*localFake
	managed map[string]map[string]string
}

func (f *managedFake) ListManaged(context.Context) (map[string]map[string]string, error) {
	return f.managed, nil
}

// 끝난 cron 실행과 끝난 배포의 pre-deploy 컨테이너는 reconcile이 지운다. 실행 중인 것은 둔다.
func TestReconcileRemovesLeftoverRunners(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	d := f.live(t)
	running := &store.CronRun{ID: store.NewID("run"), DeploymentID: d.ID, InstanceID: f.in.ID, Container: "keel-cron-web-running"}
	finished := &store.CronRun{ID: store.NewID("run"), DeploymentID: d.ID, InstanceID: f.in.ID, Container: "keel-cron-web-finished"}
	f.st.CreateCronRun(ctx, running)
	f.st.CreateCronRun(ctx, finished)
	finished.Status = "failed"
	f.st.FinishCronRun(ctx, finished)
	pending := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Trigger: "manual", Spec: f.in.Spec, ReqSpec: f.in.Spec}
	f.st.CreateDeployment(ctx, pending)
	f.st.TransitionDeployment(ctx, pending.ID, store.DepQueued, store.DepPreDeploy, nil)

	lbl := func(role, extraKey, extraVal string) map[string]string {
		return map[string]string{node.LabelManaged: "true", node.LabelRole: role, node.LabelInstance: f.in.ID, extraKey: extraVal}
	}
	drv := &managedFake{localFake: &localFake{n: f.local}, managed: map[string]map[string]string{
		"keel-cron-web-running":   lbl(node.RoleCron, cronRunLabel, running.ID),
		"keel-cron-web-finished":  lbl(node.RoleCron, cronRunLabel, finished.ID),
		"keel-cron-web-unknown":   lbl(node.RoleCron, cronRunLabel, "run_doesnotexist"),
		"keel-predeploy-old":      lbl(node.RolePreDeploy, node.LabelDeploy, d.ID), // d는 live(끝난 상태)
		"keel-predeploy-current":  lbl(node.RolePreDeploy, node.LabelDeploy, pending.ID),
		"keel-helper-something-1": lbl(node.RoleHelper, "x", "y"),
	}}
	f.m.localDrv = drv
	if err := f.m.reconcileNode(ctx, store.LocalNodeID); err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, op := range f.log.list() {
		if name, ok := strings.CutPrefix(op, "local:remove:"); ok {
			removed[name] = true
		}
	}
	for name, want := range map[string]bool{
		"keel-cron-web-running": false, "keel-cron-web-finished": true, "keel-cron-web-unknown": true,
		"keel-predeploy-old": true, "keel-predeploy-current": false, "keel-helper-something-1": false,
	} {
		if removed[name] != want {
			t.Errorf("%s removed=%v, want %v (ops %v)", name, removed[name], want, f.log.list())
		}
	}
}

// 저장하기 전에 다시 고르면 같은 포트를 주지 않는다(동시에 TCP proxy를 켜는 두 요청).
func TestAllocateTCPPortReserves(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Cfg.TCPPortMin, f.m.Cfg.TCPPortMax = 23400, 23420
	seen := map[int]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.m.AllocateTCPPort(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[p] {
				t.Errorf("port %d handed out twice", p)
			}
			seen[p] = true
		}()
	}
	wg.Wait()
}
