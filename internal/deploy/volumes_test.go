package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/store"
)

// volFake는 볼륨 내보내기·가져오기만 흉내 내는 드라이버다.
type volFake struct {
	Driver
	mu         sync.Mutex
	data       map[string]string
	failImport map[string]bool
	removed    []string
}

func newVolFake() *volFake {
	return &volFake{data: map[string]string{}, failImport: map[string]bool{}}
}

func (f *volFake) EnsureVolume(context.Context, string, map[string]string) error { return nil }
func (f *volFake) ExportVolume(_ context.Context, vol, _ string, w io.Writer) error {
	f.mu.Lock()
	d := f.data[vol]
	f.mu.Unlock()
	_, err := io.WriteString(w, d)
	return err
}
func (f *volFake) ImportVolume(_ context.Context, vol, _ string, r io.Reader) error {
	if f.failImport[vol] {
		return errors.New("disk full")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.data[vol] = string(b)
	f.mu.Unlock()
	return nil
}
func (f *volFake) RemoveVolume(_ context.Context, vol string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, vol)
	f.removed = append(f.removed, vol)
	return nil
}

// 볼륨 하나라도 복사에 실패하면 새 노드에 만든 복사본을 모두 지우고 이전 노드의 데이터는 건드리지 않는다.
func TestCopyVolumesCleansUpPartialCopies(t *testing.T) {
	m := &Manager{}
	from, to := newVolFake(), newVolFake()
	from.data["v-a"], from.data["v-b"], from.data["v-c"] = "aaa", "bbb", "ccc"
	to.failImport["v-c"] = true
	vols := []*store.Volume{{Name: "a", DockerName: "v-a"}, {Name: "b", DockerName: "v-b"}, {Name: "c", DockerName: "v-c"}}
	old := &store.Deployment{ID: "dep_old000000000", NodeID: store.LocalNodeID}
	d := &store.Deployment{ID: "dep_new000000000", NodeID: "node_remote"}
	if _, err := m.copyVolumes(context.Background(), logbook.Discard(), vols, from, to, old, d); err == nil {
		t.Fatal("copy should fail")
	}
	if len(to.data) != 0 {
		t.Fatalf("partial copies left on the new node: %v", to.data)
	}
	got := append([]string(nil), to.removed...)
	sort.Strings(got)
	if len(got) != 3 || got[0] != "v-a" || got[1] != "v-b" || got[2] != "v-c" {
		t.Fatalf("removed on new node: %v", got)
	}
	if len(from.removed) != 0 || len(from.data) != 3 {
		t.Fatalf("old node touched: removed=%v data=%v", from.removed, from.data)
	}

	// 모두 성공하면 복사본이 생기고 이전 위치 목록을 돌려준다.
	to2 := newVolFake()
	moved, err := m.copyVolumes(context.Background(), logbook.Discard(), vols, from, to2, old, d)
	if err != nil || len(moved) != 3 || to2.data["v-b"] != "bbb" {
		t.Fatalf("copy: %v %v %v", moved, err, to2.data)
	}
}

// restoreFake는 로컬 노드에서 볼륨 가져오기와 컨테이너 시작을 기록한다.
type restoreFake struct {
	*localFake
	mu       sync.Mutex
	imported string
	started  []string
}

func (f *restoreFake) EnsureVolume(context.Context, string, map[string]string) error { return nil }
func (f *restoreFake) ImportVolume(_ context.Context, _, _ string, r io.Reader) error {
	b, err := io.ReadAll(r)
	f.mu.Lock()
	f.imported = string(b)
	f.mu.Unlock()
	return err
}
func (f *restoreFake) Start(_ context.Context, name string) error {
	f.mu.Lock()
	f.started = append(f.started, name)
	f.mu.Unlock()
	return nil
}

// 복원 뒤 재배포를 큐에 넣지 못하면(노드가 배포를 받지 않는 등) 멈춰 둔 컨테이너를 다시 시작한다.
func TestRestoreRestartsWhenRedeployCannotBeQueued(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Placement = nil
	in.Replicas = 1
	f.st.UpdateInstanceConfig(ctx, in)
	img, bu := "nginx:1", "image"
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec,
		NodeID: store.LocalNodeID, Status: store.DepLive, Image: &img, BuilderUsed: &bu, Containers: []string{"keel-web-abcdefgh-1"}}
	f.st.CreateDeployment(ctx, d)
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID)
	vol := &store.Volume{ProjectID: f.svc.ProjectID, EnvironmentID: f.env.ID, InstanceID: &in.ID, Name: "data", MountPath: "/data"}
	if err := f.st.CreateVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}
	drv := &restoreFake{localFake: &localFake{n: f.local}}
	f.m.localDrv = drv

	path := filepath.Join(t.TempDir(), "b.tar.zst")
	fh, _ := os.Create(path)
	enc, _ := zstd.NewWriter(fh)
	io.WriteString(enc, "tar-bytes")
	enc.Close()
	fh.Close()
	b := &store.Backup{VolumeID: vol.ID, Status: "done", Path: path}

	ln, _ := f.st.LocalNode(ctx)
	ln.Schedulable = false
	if err := f.st.UpdateLocalNode(ctx, ln); err != nil {
		t.Fatal(err)
	}
	err := f.m.RestoreBackupSync(ctx, b)
	if err == nil || !errors.Is(err, ErrNodeUnavailable) {
		t.Fatalf("restore error: %v", err)
	}
	if drv.imported != "tar-bytes" {
		t.Fatalf("imported %q", drv.imported)
	}
	if len(drv.started) != 1 || drv.started[0] != d.Containers[0] {
		t.Fatalf("stopped container not restarted: %v (ops %v)", drv.started, f.log.list())
	}
}
