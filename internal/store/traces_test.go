package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTracingSettingsAndSpans(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, source string, domains []Domain) (*Service, *Instance) {
		sv := &Service{ProjectID: p.ID, Name: name, SourceType: source}
		if err := st.CreateService(ctx, sv); err != nil {
			t.Fatal(err)
		}
		in := &Instance{ServiceID: sv.ID, EnvironmentID: env.ID}
		in.Domains = domains
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		return sv, in
	}
	api, apiIn := mk("api", "image", []Domain{{Host: "api.example.com", Kind: "custom"}})
	worker, _ := mk("worker", "repo", nil)
	mk("db", "database", nil)

	if _, err := st.GetTracingSetting(ctx, apiIn.ID); err != ErrNotFound {
		t.Fatalf("missing setting: %v", err)
	}
	on, err := st.SetTracingSetting(ctx, apiIn.ID, true)
	if err != nil || !on.Enabled || len(on.Token) != 48 {
		t.Fatalf("enable: %+v %v", on, err)
	}
	id, err := st.LookupTraceToken(ctx, on.Token)
	if err != nil || id.ServiceID != api.ID || id.EnvironmentID != env.ID || id.ProjectID != p.ID || id.ServiceName != "api" || id.Component != "service" {
		t.Fatalf("lookup: %+v %v", id, err)
	}
	// 껐다 켜도 토큰은 같고, 꺼진 동안에는 찾지 못한다
	if _, err := st.SetTracingSetting(ctx, apiIn.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupTraceToken(ctx, on.Token); err != ErrNotFound {
		t.Errorf("lookup while disabled: %v", err)
	}
	again, _ := st.SetTracingSetting(ctx, apiIn.ID, true)
	if again.Token != on.Token {
		t.Error("token rotated on re-enable")
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	span := func(trace, id, parent, svc, comp, status string, off, dur time.Duration) TraceSpan {
		return TraceSpan{TraceID: trace, SpanID: id, ParentSpanID: parent, ProjectID: p.ID, EnvironmentID: env.ID,
			ServiceID: svc, ServiceName: "x", Component: comp, Name: "op-" + id, Kind: "server", Status: status,
			StartTime: base.Add(off), EndTime: base.Add(off + dur), Attributes: map[string]any{"n": int64(7), "f": 1.5}}
	}
	t1, t2 := "11111111111111111111111111111111", "22222222222222222222222222222222"
	spans := []TraceSpan{
		span(t1, "a1", "", api.ID, "edge", "unset", 0, 300*time.Millisecond),
		span(t1, "a2", "a1", api.ID, "service", "error", 10*time.Millisecond, 100*time.Millisecond),
		span(t2, "b1", "", worker.ID, "service", "ok", time.Minute, 50*time.Millisecond),
	}
	if err := st.InsertTraceSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	// 같은 span을 다시 받으면 덮어쓴다
	if err := st.InsertTraceSpans(ctx, spans[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTraceSpans(ctx, env.ID, t1, 0)
	if err != nil || len(got) != 2 || got[0].SpanID != "a1" || got[0].Attributes["n"] != int64(7) || got[0].Attributes["f"] != 1.5 {
		t.Fatalf("spans: %+v %v", got, err)
	}
	q := TraceQuery{EnvironmentID: env.ID, From: base.Add(-time.Minute), Until: time.Now(), Limit: 10}
	sum, err := st.ListTraceSummaries(ctx, q)
	if err != nil || len(sum) != 2 {
		t.Fatalf("summaries: %+v %v", sum, err)
	}
	if sum[0].TraceID != t2 || sum[1].TraceID != t1 || sum[1].SpanCount != 2 || sum[1].ErrorCount != 1 ||
		sum[1].RootName != "op-a1" || sum[1].ServiceID != api.ID || sum[1].DurationMS != 300 {
		t.Errorf("summaries: %+v", sum)
	}
	q.ServiceID = worker.ID
	if sum, _ := st.ListTraceSummaries(ctx, q); len(sum) != 1 || sum[0].TraceID != t2 {
		t.Errorf("by service: %+v", sum)
	}
	q.ServiceID, q.TraceIDs = "", []string{t1}
	if sum, _ := st.ListTraceSummaries(ctx, q); len(sum) != 1 || sum[0].TraceID != t1 {
		t.Errorf("by ids: %+v", sum)
	}
	var scanned []string
	st.ScanTraceSpans(ctx, env.ID, api.ID, base.Add(-time.Minute), time.Now(), 10, func(s *TraceSpan) bool {
		scanned = append(scanned, s.SpanID)
		return true
	})
	if len(scanned) != 2 || scanned[0] != "a2" {
		t.Errorf("scan newest first by service: %v", scanned)
	}

	list, err := st.ListTracingSettings(ctx, env.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("settings list (db excluded): %+v %v", list, err)
	}
	if list[0].Service != "api" || !list[0].Enabled || !list[0].HasDomain || list[0].LastEdgeAt == nil || list[0].LastAppAt == nil ||
		list[1].Service != "worker" || list[1].Enabled || list[1].HasDomain || list[1].LastEdgeAt != nil || list[1].LastAppAt == nil {
		t.Errorf("settings: %+v %+v", list[0], list[1])
	}

	if err := st.PruneTraceSpans(ctx, base.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTraceSpans(ctx, env.ID, t1, 0); len(got) != 0 {
		t.Errorf("prune left %d spans", len(got))
	}
	if got, _ := st.GetTraceSpans(ctx, env.ID, t2, 0); len(got) != 1 {
		t.Errorf("prune removed newer spans")
	}
}
