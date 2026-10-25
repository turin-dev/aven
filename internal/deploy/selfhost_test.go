package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/selfhost"
	"github.com/turin-dev/aven/internal/store"
)

// fakeSelfDocker는 upgrader 컨테이너 docker 명령을 기록한다. Wait는 exit 값을 바로 돌려준다(교체 실패).
type fakeSelfDocker struct {
	mu      sync.Mutex
	runs    [][]string
	removed []string
	exit    int
	runErr  error
	unit    string     // systemd 유닛(""면 systemd 밖)
	host    [][]string // 호스트 명령
}

func (f *fakeSelfDocker) Host(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.host = append(f.host, append([]string{name}, args...))
	switch name {
	case "systemctl": // bootstrap 유닛이 바로 끝났다
		return "", errors.New("inactive")
	case "journalctl":
		return "image not found", nil
	}
	return "", nil
}
func (f *fakeSelfDocker) Unit() string { return f.unit }

func (f *fakeSelfDocker) Run(_ context.Context, args []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, args)
	return f.runErr
}
func (f *fakeSelfDocker) Wait(context.Context, string) (int, error) { return f.exit, nil }
func (f *fakeSelfDocker) TailLogs(context.Context, string, int) []string {
	return []string{"upgrader says hi"}
}
func (f *fakeSelfDocker) Remove(_ context.Context, name string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}

// newSelfFixture는 로컬 노드에 replica 1개로 배포하는 서비스를 self 서비스로 둔다.
func newSelfFixture(t *testing.T) (*scaleFixture, *fakeSelfDocker) {
	t.Helper()
	f := newScaleFixture(t)
	f.in.Placement = nil
	f.in.Replicas = 1
	if err := f.st.UpdateInstanceConfig(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	f.m.Cfg.SelfServiceID = f.svc.ID
	f.m.Cfg.SelfEnvFile = filepath.Join(t.TempDir(), "aven.env")
	os.WriteFile(f.m.Cfg.SelfEnvFile, []byte("KEEL_ADMIN_PASSWORD=password123\n"), 0o600)
	sd := &fakeSelfDocker{exit: 1}
	f.m.selfDrv = sd
	return f, sd
}

func TestSelfDeployRejectsMultiNodePlacement(t *testing.T) {
	f := newScaleFixture(t) // placement: 로컬 1 + 원격 1
	f.m.Cfg.SelfServiceID = f.svc.ID
	sd := &fakeSelfDocker{}
	f.m.selfDrv = sd
	d := f.executeDeploy(t)
	if d.Status != store.DepFailed || d.Error == nil || !strings.Contains(*d.Error, "replica 1개") {
		t.Fatalf("status %s err %v", d.Status, d.Error)
	}
	if len(sd.runs) != 0 || len(f.local.names()) != 0 || len(f.remote.names()) != 0 || f.log.prefixed("local:pull:") >= 0 {
		t.Fatalf("nothing should run or pull: upgrader=%v ops=%v", sd.runs, f.log.list())
	}
}

func TestSelfDeployNeedsSelfContainer(t *testing.T) {
	t.Setenv(selfhost.EnvActive, "")
	f, sd := newSelfFixture(t)
	d := f.executeDeploy(t)
	if d.Status != store.DepFailed || d.Error == nil || !strings.Contains(*d.Error, "스스로 교체할 수 없습니다") {
		t.Fatalf("status %s err %v", d.Status, d.Error)
	}
	// 이미지는 준비하지만 컨테이너는 띄우지 않는다.
	if f.log.index("local:pull:nginx:1") < 0 || len(f.local.names()) != 0 || len(sd.runs) != 0 {
		t.Fatalf("ops %v containers %v upgrader %v", f.log.list(), f.local.names(), sd.runs)
	}
}

func TestSelfDeployHandsOffToUpgrader(t *testing.T) {
	t.Setenv(selfhost.EnvActive, "1")
	f, sd := newSelfFixture(t)
	prev := f.live(t)
	d := f.executeDeploy(t)
	// fake upgrader는 바꾸지 못하고 끝난다(exit 1): 배포는 실패하고 이전 배포가 계속 현재다.
	if d.Status != store.DepFailed || d.Error == nil || !strings.Contains(*d.Error, "exit 1") {
		t.Fatalf("status %s err %v", d.Status, d.Error)
	}
	if len(sd.runs) != 1 {
		t.Fatalf("upgrader runs: %v", sd.runs)
	}
	args := strings.Join(sd.runs[0], " ")
	for _, want := range []string{"--name " + selfhost.UpgraderName, "nginx:1 aven-server selfhost upgrade", "--deployment " + d.ID, "--previous " + prev.ID, "--data " + f.m.Cfg.DataDir} {
		if !strings.Contains(args, want) {
			t.Errorf("upgrader args missing %q: %s", want, args)
		}
	}
	if len(d.AllContainers()) != 0 {
		t.Fatalf("self deployment must not own containers: %v", d.AllContainers())
	}
	in, _ := f.st.GetInstance(context.Background(), f.in.ID)
	if in.CurrentDeploymentID == nil || *in.CurrentDeploymentID != prev.ID || in.Status != store.InstOnline {
		t.Fatalf("instance %s current %v", in.Status, in.CurrentDeploymentID)
	}
}

// selfDeploying은 upgrader에 넘겨 진행 중(deploying)인 self 배포를 만든다(이전 서버가 멈춘 직후의 DB 상태).
func selfDeploying(t *testing.T, f *scaleFixture, img string) *store.Deployment {
	t.Helper()
	ctx := context.Background()
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Trigger: "manual", Spec: f.in.Spec, ReqSpec: f.in.Spec,
		NodeID: store.LocalNodeID, Status: store.DepDeploying, Image: &img}
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstDeploying, nil)
	return d
}

func TestFinishSelfDeployMarksNewServerLive(t *testing.T) {
	ctx := context.Background()
	f, sd := newSelfFixture(t)
	prev := f.live(t)
	d := selfDeploying(t, f, "keel/self:new")
	t.Setenv(selfhost.EnvActive, "1")
	t.Setenv(selfhost.EnvImage, "keel/self:new")
	if err := f.m.Init(ctx); err != nil { // 새 aven-server가 시작한다
		t.Fatal(err)
	}
	got, _ := f.st.GetDeployment(ctx, d.ID)
	old, _ := f.st.GetDeployment(ctx, prev.ID)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if got.Status != store.DepLive || old.Status != store.DepRemoved {
		t.Fatalf("new %s old %s", got.Status, old.Status)
	}
	if in.Status != store.InstOnline || in.CurrentDeploymentID == nil || *in.CurrentDeploymentID != d.ID {
		t.Fatalf("instance %s current %v", in.Status, in.CurrentDeploymentID)
	}
	_ = sd
}

func TestFinishSelfDeployAppliesRollback(t *testing.T) {
	ctx := context.Background()
	f, sd := newSelfFixture(t)
	prev := f.live(t)
	d := selfDeploying(t, f, "keel/self:bad")
	// upgrader가 되돌리고 이전 이미지의 aven-server를 다시 띄웠다.
	if err := selfhost.WriteResult(f.m.Cfg.DataDir, selfhost.Result{Deployment: d.ID, Previous: prev.ID, Image: "keel/self:bad", Error: "aven-server 헬스체크 실패", Log: []string{"boom"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(selfhost.EnvActive, "1")
	t.Setenv(selfhost.EnvImage, "nginx:1")
	if err := f.m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.GetDeployment(ctx, d.ID)
	old, _ := f.st.GetDeployment(ctx, prev.ID)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if got.Status != store.DepFailed || got.Error == nil || !strings.Contains(*got.Error, "rolled back") || old.Status != store.DepLive {
		t.Fatalf("new %s %v old %s", got.Status, got.Error, old.Status)
	}
	if in.Status != store.InstOnline || in.CurrentDeploymentID == nil || *in.CurrentDeploymentID != prev.ID {
		t.Fatalf("instance %s current %v", in.Status, in.CurrentDeploymentID)
	}
	if r, _ := selfhost.TakeResult(f.m.Cfg.DataDir); r != nil {
		t.Fatalf("result file should be consumed: %+v", r)
	}
	if len(sd.removed) == 0 || sd.removed[0] != selfhost.UpgraderName {
		t.Fatalf("upgrader not removed: %v", sd.removed)
	}
}

func TestSelfServiceRefusesContainerOps(t *testing.T) {
	ctx := context.Background()
	f, _ := newSelfFixture(t)
	f.live(t)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if err := f.m.Stop(ctx, in); !errors.Is(err, ErrSelfService) {
		t.Errorf("Stop: %v", err)
	}
	if err := f.m.Restart(ctx, in); !errors.Is(err, ErrSelfService) {
		t.Errorf("Restart: %v", err)
	}
	if _, err := f.m.ScaleReplicas(ctx, in, 2, "test"); !errors.Is(err, ErrSelfService) {
		t.Errorf("ScaleReplicas: %v", err)
	}
	if err := f.m.Sleep(ctx, in); !errors.Is(err, ErrSelfService) {
		t.Errorf("Sleep: %v", err)
	}
	if got, _ := f.st.GetInstance(ctx, f.in.ID); got.Status != store.InstOnline {
		t.Fatalf("instance status changed: %s", got.Status)
	}
}

func TestSelfDeployBootstrapsFromSystemd(t *testing.T) {
	t.Setenv(selfhost.EnvActive, "")
	f, sd := newSelfFixture(t)
	sd.unit = "aven-server"
	old := selfBootstrapWait
	selfBootstrapWait = time.Second
	t.Cleanup(func() { selfBootstrapWait = old })
	d := f.executeDeploy(t)
	// fake bootstrap 유닛은 이 서버를 멈추지 않고 끝난다: 배포는 실패하고 journal 로그가 남는다.
	if d.Status != store.DepFailed || d.Error == nil || !strings.Contains(*d.Error, "bootstrap") {
		t.Fatalf("status %s err %v", d.Status, d.Error)
	}
	if len(sd.host) == 0 || sd.host[0][0] != "systemd-run" {
		t.Fatalf("host commands: %v", sd.host)
	}
	run := strings.Join(sd.host[0], " ")
	for _, want := range []string{"--unit aven-selfbootstrap-", "selfhost bootstrap --image nginx:1 --systemd-unit aven-server --deployment " + d.ID, "--env-file " + f.m.Cfg.SelfEnvFile} {
		if !strings.Contains(run, want) {
			t.Errorf("systemd-run missing %q: %s", want, run)
		}
	}
	if len(sd.runs) != 0 {
		t.Fatalf("no upgrader container in systemd mode: %v", sd.runs)
	}
}

func TestSelfDeployRejectsQuotedEnvFile(t *testing.T) {
	t.Setenv(selfhost.EnvActive, "1")
	f, sd := newSelfFixture(t)
	os.WriteFile(f.m.Cfg.SelfEnvFile, []byte("KEEL_ADMIN_PASSWORD=\"secret\"\n"), 0o600)
	d := f.executeDeploy(t)
	if d.Status != store.DepFailed || d.Error == nil || !strings.Contains(*d.Error, "KEEL_ADMIN_PASSWORD") || len(sd.runs) != 0 {
		t.Fatalf("status %s err %v runs %v", d.Status, d.Error, sd.runs)
	}
}

func TestSelfServiceFromSetting(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	if f.m.IsSelfService(f.svc.ID) {
		t.Fatal("not self yet")
	}
	if err := f.m.SetSelfService(ctx, f.svc.ID); err != nil {
		t.Fatal(err)
	}
	m2 := &Manager{Store: f.st, Logs: f.m.Logs, Log: f.m.Log, Cfg: Config{DataDir: t.TempDir()}, localDrv: f.m.localDrv}
	m2.Dispatch = func(string, string) {}
	if err := m2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !m2.IsSelfService(f.svc.ID) {
		t.Fatal("setting not loaded on start")
	}
	if err := m2.SetSelfService(ctx, ""); err != nil || m2.IsSelfService(f.svc.ID) {
		t.Fatalf("unset: %v", err)
	}
	m2.Cfg.SelfServiceID = "svc_env"
	if err := m2.SetSelfService(ctx, f.svc.ID); err == nil || m2.SelfServiceID() != "svc_env" {
		t.Fatal("env must win and lock the setting")
	}
}
