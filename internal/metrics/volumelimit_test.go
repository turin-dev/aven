package metrics

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

func TestLimitStateAndEvent(t *testing.T) {
	const mb = 1024 * 1024
	for _, tc := range []struct {
		size  int64
		limit int
		want  string
	}{
		{0, 100, store.VolumeLimitOK}, {89 * mb, 100, store.VolumeLimitOK}, {90 * mb, 100, store.VolumeLimitNear},
		{100*mb - 1, 100, store.VolumeLimitNear}, {100 * mb, 100, store.VolumeLimitExceeded}, {5 * mb, 0, store.VolumeLimitOK},
	} {
		if got := LimitState(tc.size, tc.limit); got != tc.want {
			t.Errorf("LimitState(%d, %d) = %q, want %q", tc.size, tc.limit, got, tc.want)
		}
	}
	for _, tc := range []struct{ prev, cur, want string }{
		{"", "near", "volume.near_limit"}, {"", "exceeded", "volume.limit_exceeded"}, {"near", "exceeded", "volume.limit_exceeded"},
		{"near", "near", ""}, {"exceeded", "near", ""}, {"exceeded", "", ""}, {"near", "", ""},
	} {
		if got := LimitEvent(tc.prev, tc.cur); got != tc.want {
			t.Errorf("LimitEvent(%q, %q) = %q, want %q", tc.prev, tc.cur, got, tc.want)
		}
	}
}

func TestCheckVolumeLimitsEmitsOncePerCrossing(t *testing.T) {
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
	lim := 100
	v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, Name: "data", MountPath: "/data", SizeLimitMB: &lim}
	if err := st.CreateVolume(ctx, v); err != nil {
		t.Fatal(err)
	}
	free := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, Name: "free", MountPath: "/free"}
	if err := st.CreateVolume(ctx, free); err != nil {
		t.Fatal(err)
	}
	c := &Collector{Store: st, Events: &events.Bus{Store: st}}
	kinds := func() []string {
		evs, err := st.ListEvents(ctx, p.ID, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			if e.Kind == "volume.near_limit" || e.Kind == "volume.limit_exceeded" {
				out = append(out, e.Kind)
			}
		}
		return out
	}
	const mb = 1024 * 1024
	step := func(size int64) {
		c.checkVolumeLimits(ctx, map[string]int64{v.DockerName: size, free.DockerName: 1 << 40})
	}
	step(10 * mb)
	step(95 * mb)
	step(96 * mb) // 이미 near: 다시 알리지 않는다
	if k := kinds(); len(k) != 1 || k[0] != "volume.near_limit" {
		t.Fatalf("after near: %v", k)
	}
	step(120 * mb)
	step(130 * mb)
	if k := kinds(); len(k) != 2 {
		t.Fatalf("after exceeded: %v", k)
	}
	got, _ := st.GetVolume(ctx, v.ID)
	if got.LimitState != store.VolumeLimitExceeded || *got.SizeLimitMB != 100 {
		t.Errorf("stored state: %+v", got)
	}
	step(10 * mb) // 다시 내려가면 조용히 초기화
	step(91 * mb) // 다시 넘으면 다시 알린다
	if k := kinds(); len(k) != 3 {
		t.Fatalf("after re-crossing: %v", k)
	}
}
