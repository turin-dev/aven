package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNetworkLogs(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	env, err := st.CreateProject(ctx, &Project{Name: "netlogs"})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	logs := []*NetworkLog{
		{ID: "n1", EnvironmentID: env.ID, ServiceID: "svc_api", Kind: "dns", RecordedAt: t0, Data: []byte(`{"qname":"a"}`)},
		{ID: "n2", EnvironmentID: env.ID, ServiceID: "svc_api", RelatedServiceID: "svc_db", Kind: "network", RecordedAt: t0.Add(time.Second), Data: []byte(`{}`)},
		{ID: "n3", EnvironmentID: env.ID, ServiceID: "svc_web", Kind: "network", RecordedAt: t0.Add(2 * time.Second), Data: []byte(`{}`)},
		{ID: "n4", EnvironmentID: "env_missing", ServiceID: "svc_x", Kind: "dns", RecordedAt: t0, Data: []byte(`{}`)},
	}
	in, err := st.AddNetworkLogs(ctx, logs)
	if err != nil || len(in) != 3 {
		t.Fatalf("insert: %d %v", len(in), err)
	}
	// 같은 ID는 다시 들어가지 않는다(agent replay).
	if ok, err := st.AddNetworkLog(ctx, &NetworkLog{ID: "n1", EnvironmentID: env.ID, Kind: "dns", Data: []byte(`{}`)}); ok || err != nil {
		t.Fatalf("duplicate: %v %v", ok, err)
	}
	if _, err := st.AddNetworkLogs(ctx, []*NetworkLog{{EnvironmentID: env.ID, Kind: "http", Data: []byte(`{}`)}}); err == nil {
		t.Fatal("bad kind accepted")
	}
	if _, err := st.AddNetworkLogs(ctx, []*NetworkLog{{EnvironmentID: env.ID, Kind: "dns", Data: []byte(`{`)}}); err == nil {
		t.Fatal("bad json accepted")
	}

	ids := func(q NetworkLogQuery) string {
		t.Helper()
		q.EnvironmentID = env.ID
		got, err := st.ListNetworkLogs(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		s := ""
		for _, l := range got {
			s += l.ID
		}
		return s
	}
	if got := ids(NetworkLogQuery{}); got != "n3n2n1" {
		t.Fatalf("all: %s", got)
	}
	if got := ids(NetworkLogQuery{Kind: "network"}); got != "n3n2" {
		t.Fatalf("kind: %s", got)
	}
	// 상대 서비스로도 고른다.
	if got := ids(NetworkLogQuery{Services: []string{"svc_db"}}); got != "n2" {
		t.Fatalf("related: %s", got)
	}
	if got := ids(NetworkLogQuery{Services: []string{"svc_api", "svc_web"}, Limit: 2}); got != "n3n2" {
		t.Fatalf("services+limit: %s", got)
	}
	if got := ids(NetworkLogQuery{Since: t0.Add(time.Second), Until: t0.Add(time.Second)}); got != "n2" {
		t.Fatalf("time range: %s", got)
	}
	got, _ := st.ListNetworkLogs(ctx, NetworkLogQuery{EnvironmentID: env.ID, Kind: "dns"})
	if len(got) != 1 || !got[0].RecordedAt.Equal(t0) || string(got[0].Data) != `{"qname":"a"}` {
		t.Fatalf("row: %+v", got)
	}

	// 보관: 시간 기준, 그다음 개수 기준.
	if n, err := st.PruneNetworkLogs(ctx, t0.Add(500*time.Millisecond), 0); err != nil || n != 1 {
		t.Fatalf("prune by time: %d %v", n, err)
	}
	if n, err := st.PruneNetworkLogs(ctx, t0, 1); err != nil || n != 1 {
		t.Fatalf("prune by count: %d %v", n, err)
	}
	if got := ids(NetworkLogQuery{}); got != "n3" {
		t.Fatalf("after prune: %s", got)
	}
	// 환경을 지우면 기록도 지워진다.
	if err := st.DeleteEnvironment(ctx, env.ID); err == nil {
		if got := ids(NetworkLogQuery{}); got != "" {
			t.Fatalf("cascade: %s", got)
		}
	}
}
