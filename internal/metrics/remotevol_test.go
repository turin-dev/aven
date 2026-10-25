package metrics

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// remoteSrc는 원격 노드 드라이버처럼 Source와 VolumeSizer를 둘 다 구현한다.
type remoteSrc struct {
	sizes map[string]int64
	err   error
}

func (r *remoteSrc) ListManaged(context.Context) (map[string]map[string]string, error) {
	return nil, nil
}
func (r *remoteSrc) Stats(context.Context, []string) ([]node.Stats, error) { return nil, nil }
func (r *remoteSrc) VolumeSizes(context.Context) (map[string]int64, error) { return r.sizes, r.err }

// statsOnly는 volume.sizes를 모르는 소스다(VolumeSizer가 아니다).
type statsOnly struct{}

func (statsOnly) ListManaged(context.Context) (map[string]map[string]string, error) { return nil, nil }
func (statsOnly) Stats(context.Context, []string) ([]node.Stats, error)             { return nil, nil }

func TestMergeVolumeSizes(t *testing.T) {
	ctx := context.Background()
	a := &remoteSrc{sizes: map[string]int64{"keel-vol-a": 10, "keel-vol-x": 5}}
	b := &remoteSrc{sizes: map[string]int64{"keel-vol-b": 20, "keel-vol-x": 7}}
	got, ok := MergeVolumeSizes(ctx, []VolumeSizer{a, b}, nil)
	if !ok || got["keel-vol-a"] != 10 || got["keel-vol-b"] != 20 || got["keel-vol-x"] != 7 {
		t.Fatalf("merge: %v %v", got, ok)
	}
	// b를 이번에 재지 못하면 b의 이전 값을 유지하고, a의 새 값은 반영한다.
	a.sizes = map[string]int64{"keel-vol-a": 11}
	b.err = errors.New("offline")
	got, ok = MergeVolumeSizes(ctx, []VolumeSizer{a, b}, got)
	if !ok || got["keel-vol-a"] != 11 || got["keel-vol-b"] != 20 {
		t.Fatalf("partial: %v", got)
	}
	if _, ok := MergeVolumeSizes(ctx, []VolumeSizer{b}, got); ok {
		t.Fatal("all sources failed but merge reported ok")
	}
}

func TestRefreshVolumesIncludesRemoteNodes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	lim := 1
	v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, Name: "data", MountPath: "/data", SizeLimitMB: &lim}
	if err := st.CreateVolume(ctx, v); err != nil {
		t.Fatal(err)
	}
	remote := &remoteSrc{sizes: map[string]int64{v.DockerName: 2 << 20}}
	c := &Collector{Store: st, Events: &events.Bus{Store: st}, Remote: func(context.Context) []Source { return []Source{statsOnly{}, remote} }}
	c.refreshVolumes(ctx)
	if sz := c.VolumeSize(v.DockerName); sz == nil || *sz != 2<<20 {
		t.Fatalf("remote volume size: %v", sz)
	}
	got, _ := st.GetVolume(ctx, v.ID)
	if got.LimitState != store.VolumeLimitExceeded {
		t.Fatalf("limit state: %q", got.LimitState)
	}
	evs, _ := st.ListEvents(ctx, p.ID, "", 10)
	found := false
	for _, e := range evs {
		found = found || e.Kind == "volume.limit_exceeded"
	}
	if !found {
		t.Fatal("no limit event for a remote volume")
	}
}
