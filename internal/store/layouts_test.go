package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
)

func layoutStore(t *testing.T) (*Store, *Project, *Environment) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &Project{Name: "layouts"}
	env, err := st.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return st, p, env
}

func TestCanvasGroupValidation(t *testing.T) {
	ok := []CanvasGroup{
		{Name: " api tier "},
		{Name: "db", Color: "teal"},
		{Name: "x", Color: "#A1b2C3", ServiceIDs: []string{"a", "a", " ", "b"}, Rect: &CanvasRect{X: -10.4, Y: 3, W: 100, H: 50}},
	}
	for i := range ok {
		if err := ok[i].Normalize(); err != nil {
			t.Errorf("%d: %v", i, err)
		}
	}
	if ok[0].Name != "api tier" || ok[0].Color != "gray" {
		t.Errorf("defaults: %+v", ok[0])
	}
	if len(ok[2].ServiceIDs) != 2 || ok[2].Rect.X != -10 {
		t.Errorf("dedupe/round: %+v %+v", ok[2].ServiceIDs, ok[2].Rect)
	}
	long := make([]byte, 61)
	for i := range long {
		long[i] = 'a'
	}
	many := make([]string, MaxGroupServices+1)
	for i := range many {
		many[i] = NewID("svc")
	}
	for i, g := range []CanvasGroup{
		{Name: ""},
		{Name: "   "},
		{Name: string(long)},
		{Name: "a\nb"},
		{Name: "x", Color: "chartreuse"},
		{Name: "x", Color: "#12345"},
		{Name: "x", ServiceIDs: many},
		{Name: "x", Rect: &CanvasRect{W: -1}},
		{Name: "x", Rect: &CanvasRect{X: math.NaN()}},
		{Name: "x", Rect: &CanvasRect{Y: math.Inf(1)}},
	} {
		if err := g.Normalize(); !errors.Is(err, ErrInvalidLayout) {
			t.Errorf("bad %d accepted: %v", i, err)
		}
	}
}

func TestCanvasGroupCRUD(t *testing.T) {
	ctx := context.Background()
	st, p, env := layoutStore(t)
	g1 := &CanvasGroup{EnvironmentID: env.ID, Name: "backend", ServiceIDs: []string{"svc_a", "svc_b"}, Rect: &CanvasRect{X: 1, Y: 2, W: 3, H: 4}}
	if err := st.CreateGroup(ctx, g1); err != nil {
		t.Fatal(err)
	}
	if g1.ID == "" || g1.Color != "gray" {
		t.Fatalf("created: %+v", g1)
	}
	got, err := st.GetGroup(ctx, g1.ID)
	if err != nil || got.Name != "backend" || len(got.ServiceIDs) != 2 || got.Rect == nil || got.Rect.W != 3 {
		t.Fatalf("get: %+v %v", got, err)
	}
	// 다른 그룹에 넣으면 원래 그룹에서 빠진다.
	g2 := &CanvasGroup{EnvironmentID: env.ID, Name: "data", Color: "blue", ServiceIDs: []string{"svc_b", "svc_c"}}
	if err := st.CreateGroup(ctx, g2); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetGroup(ctx, g1.ID); len(got.ServiceIDs) != 1 || got.ServiceIDs[0] != "svc_a" {
		t.Errorf("claim on create: %v", got.ServiceIDs)
	}
	g1.ServiceIDs = []string{"svc_a", "svc_c"}
	g1.Name, g1.Rect = "renamed", nil
	if err := st.UpdateGroup(ctx, g1); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetGroup(ctx, g2.ID); len(got.ServiceIDs) != 1 || got.ServiceIDs[0] != "svc_b" {
		t.Errorf("claim on update: %v", got.ServiceIDs)
	}
	if got, _ := st.GetGroup(ctx, g1.ID); got.Name != "renamed" || got.Rect != nil {
		t.Errorf("update: %+v", got)
	}
	// 다른 환경의 그룹은 건드리지 않는다.
	other := &Environment{ProjectID: p.ID, Name: "staging"}
	if err := st.CreateEnvironment(ctx, other); err != nil {
		t.Fatal(err)
	}
	g3 := &CanvasGroup{EnvironmentID: other.ID, Name: "s", ServiceIDs: []string{"svc_a"}}
	if err := st.CreateGroup(ctx, g3); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetGroup(ctx, g1.ID); len(got.ServiceIDs) != 2 {
		t.Errorf("cross-env claim: %v", got.ServiceIDs)
	}
	list, err := st.ListGroups(ctx, env.ID)
	if err != nil || len(list) != 2 || list[0].ID != g1.ID {
		t.Fatalf("list: %v %v", list, err)
	}
	bad := &CanvasGroup{EnvironmentID: env.ID, Name: ""}
	if err := st.CreateGroup(ctx, bad); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("invalid create: %v", err)
	}
	if err := st.DeleteGroup(ctx, g1.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGroup(ctx, g1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if err := st.UpdateGroup(ctx, g1); !errors.Is(err, ErrNotFound) {
		t.Errorf("update deleted: %v", err)
	}
	// 환경을 지우면 그룹도 지워진다.
	if err := st.DeleteEnvironment(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetGroup(ctx, g3.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cascade: %v", err)
	}
}

func TestCanvasGroupLimit(t *testing.T) {
	ctx := context.Background()
	st, _, env := layoutStore(t)
	for i := 0; i < MaxGroupsPerEnv; i++ {
		if err := st.CreateGroup(ctx, &CanvasGroup{EnvironmentID: env.ID, Name: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateGroup(ctx, &CanvasGroup{EnvironmentID: env.ID, Name: "g"}); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("limit: %v", err)
	}
}

func lsp(s string) *string { return &s }

func TestDashboardValidation(t *testing.T) {
	d := Dashboard{Name: " ops ", Widgets: []Widget{
		{Type: "metric", Metric: "cpu_percent", EnvironmentID: lsp("env_a")},
		{ID: "w1", Type: "logs", EnvironmentID: lsp("env_a"), ServiceID: lsp(" "), Metric: "cpu_percent", Range: "1h", W: 12, H: 8},
		{ID: "w1", Type: "http", EnvironmentID: lsp("env_a"), ServiceID: lsp("svc_a"), X: 11, Y: 3},
		{Type: "events", Title: "  deploys  "},
	}}
	if err := d.Normalize(); err != nil {
		t.Fatal(err)
	}
	w := d.Widgets
	if d.Name != "ops" || w[0].ID == "" || w[0].Range != "6h" || w[0].W != 6 || w[0].H != 3 {
		t.Errorf("defaults: %+v", w[0])
	}
	if w[1].ID != "w1" || w[2].ID == "w1" || w[1].ServiceID != nil || w[1].Metric != "" {
		t.Errorf("ids/trim: %+v %+v", w[1], w[2])
	}
	if w[3].Title != "deploys" {
		t.Errorf("title: %q", w[3].Title)
	}
	many := make([]Widget, MaxDashboardWidgets+1)
	for i := range many {
		many[i] = Widget{Type: "events"}
	}
	for i, bad := range []Dashboard{
		{Name: ""},
		{Name: "a\tb"},
		{Name: "x", Widgets: many},
		{Name: "x", Widgets: []Widget{{Type: "chart"}}},
		{Name: "x", Widgets: []Widget{{Type: "metric", EnvironmentID: lsp("e")}}},
		{Name: "x", Widgets: []Widget{{Type: "metric", Metric: "load", EnvironmentID: lsp("e")}}},
		{Name: "x", Widgets: []Widget{{Type: "metric", Metric: "cpu_percent"}}},
		{Name: "x", Widgets: []Widget{{Type: "logs"}}},
		{Name: "x", Widgets: []Widget{{Type: "http", EnvironmentID: lsp("e")}}},
		{Name: "x", Widgets: []Widget{{Type: "events", Range: "30d"}}},
		{Name: "x", Widgets: []Widget{{Type: "events", W: 13}}},
		{Name: "x", Widgets: []Widget{{Type: "events", H: 9}}},
		{Name: "x", Widgets: []Widget{{Type: "events", W: -1}}},
		{Name: "x", Widgets: []Widget{{Type: "events", X: 12}}},
		{Name: "x", Widgets: []Widget{{Type: "events", Y: -1}}},
	} {
		if err := bad.Normalize(); !errors.Is(err, ErrInvalidLayout) {
			t.Errorf("bad %d accepted: %v", i, err)
		}
	}
}

func TestDashboardCRUD(t *testing.T) {
	ctx := context.Background()
	st, p, env := layoutStore(t)
	d := &Dashboard{ProjectID: p.ID, Name: "main", Widgets: []Widget{{Type: "metric", Metric: "memory_bytes", EnvironmentID: &env.ID, Title: "mem"}}}
	if err := st.CreateDashboard(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDashboard(ctx, d.ID)
	if err != nil || got.Name != "main" || len(got.Widgets) != 1 || got.Widgets[0].ID != d.Widgets[0].ID || *got.Widgets[0].EnvironmentID != env.ID {
		t.Fatalf("get: %+v %v", got, err)
	}
	got.Name = "renamed"
	got.Widgets = append(got.Widgets, Widget{Type: "events"})
	if err := st.UpdateDashboard(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListDashboards(ctx, p.ID)
	if err != nil || len(list) != 1 || list[0].Name != "renamed" || len(list[0].Widgets) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	got.Name = ""
	if err := st.UpdateDashboard(ctx, got); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("invalid update: %v", err)
	}
	empty := &Dashboard{ProjectID: p.ID, Name: "empty"}
	if err := st.CreateDashboard(ctx, empty); err != nil || empty.Widgets == nil {
		t.Fatalf("empty: %v", err)
	}
	if e, _ := st.GetDashboard(ctx, empty.ID); e.Widgets == nil || len(e.Widgets) != 0 {
		t.Errorf("empty widgets: %v", e.Widgets)
	}
	if err := st.DeleteDashboard(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDashboard(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
	for i := 1; i < MaxDashboardsPerProject; i++ {
		if err := st.CreateDashboard(ctx, &Dashboard{ProjectID: p.ID, Name: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateDashboard(ctx, &Dashboard{ProjectID: p.ID, Name: "d"}); !errors.Is(err, ErrInvalidLayout) {
		t.Errorf("limit: %v", err)
	}
	// 프로젝트를 지우면 대시보드도 지워진다.
	if err := st.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.ListDashboards(ctx, p.ID); len(l) != 0 {
		t.Errorf("cascade: %d", len(l))
	}
}
