package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

func TestCheckDataCapsAlertsOncePerStep(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "home-apps"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInstance(ctx, &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}); err != nil {
		t.Fatal(err)
	}
	// 다른 프로젝트(이 노드에 서비스 없음)에는 알림이 가지 않는다.
	other := &store.Project{Name: "elsewhere"}
	if _, err := st.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeTraits(ctx, store.LocalNodeID, store.NodeTraits{Site: "home", DataCapGB: 1}); err != nil {
		t.Fatal(err)
	}
	c := &Collector{Store: st, Events: &events.Bus{Store: st}}
	kinds := func(pid string) []string {
		evs, err := st.ListEvents(ctx, pid, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(evs) - 1; i >= 0; i-- {
			if k := evs[i].Kind; k == "node.data_cap_warning" || k == "node.data_cap_reached" {
				out = append(out, k)
			}
		}
		return out
	}
	now := time.Now().UTC()
	st.AddNodeTraffic(ctx, store.LocalNodeID, 0, 0, now)
	st.AddNodeTraffic(ctx, store.LocalNodeID, 500_000_000, 100_000_000, now.Add(10*time.Second)) // 60%
	c.CheckDataCaps(ctx, now)
	if got := kinds(p.ID); len(got) != 0 {
		t.Fatalf("60%%: %v", got)
	}
	st.AddNodeTraffic(ctx, store.LocalNodeID, 700_000_000, 150_000_000, now.Add(20*time.Second)) // 85%
	c.CheckDataCaps(ctx, now)
	c.CheckDataCaps(ctx, now)
	if got := kinds(p.ID); len(got) != 1 || got[0] != "node.data_cap_warning" {
		t.Fatalf("85%%: %v", got)
	}
	st.AddNodeTraffic(ctx, store.LocalNodeID, 900_000_000, 200_000_000, now.Add(30*time.Second)) // 110%
	c.CheckDataCaps(ctx, now)
	c.CheckDataCaps(ctx, now)
	if got := kinds(p.ID); len(got) != 2 || got[1] != "node.data_cap_reached" {
		t.Fatalf("110%%: %v", got)
	}
	if got := kinds(other.ID); len(got) != 0 {
		t.Fatalf("other project: %v", got)
	}
}
