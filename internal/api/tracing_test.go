package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestTracingAPI(t *testing.T) {
	f, _ := authServer(t)
	f.srv.Deploy = nil // proxy 동기화는 deploy 테스트가 다룬다(여기에는 keel-proxy가 없다)
	h := f.srv.Handler()
	auth := withBearer("legacy-token")
	inst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID + "/tracing"
	envBase := "/api/environments/" + f.prod.ID

	if w := req(h, "PUT", inst, `{}`, auth); w.Code != 400 {
		t.Errorf("missing enabled: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/services/"+f.db.ID+"/instances/"+f.prod.ID+"/tracing", `{"enabled":true}`, auth); w.Code != 400 {
		t.Errorf("database: %d", w.Code)
	}
	w := req(h, "PUT", inst, `{"enabled":true}`, auth)
	if w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	got := decodeBody[map[string]any](t, w)
	if got["enabled"] != true || got["has_public_domain"] != true || got["service_name"] != "web" || got["token"] != nil {
		t.Errorf("enable response: %v", got)
	}
	viewer := testUser(t, f.st, "tv@example.com", store.RoleViewer)
	f.st.SetMember(f.ctx, f.p.ID, viewer.ID, store.RoleViewer)
	vc := withCookie(testSessionCookie(t, f.srv, viewer))
	if w := req(h, "PUT", inst, `{"enabled":false}`, vc); w.Code != 403 {
		t.Errorf("viewer toggle: %d", w.Code)
	}
	if w := req(h, "GET", envBase+"/tracing", "", vc); w.Code != 200 {
		t.Errorf("viewer status: %d", w.Code)
	}
	list := decodeBody[[]map[string]any](t, req(h, "GET", envBase+"/tracing", "", auth))
	if len(list) != 2 { // web, other (db 제외)
		t.Fatalf("env settings: %v", list)
	}
	// 환경 전체를 켠다
	list = decodeBody[[]map[string]any](t, req(h, "PUT", envBase+"/tracing", `{"enabled":true}`, auth))
	for _, s := range list {
		if s["enabled"] != true {
			t.Errorf("enable all: %v", s)
		}
	}
	if w := req(h, "PUT", envBase+"/tracing", `{"enabled":false,"service_ids":["`+f.db.ID+`"]}`, auth); w.Code != 404 {
		t.Errorf("bulk with database id: %d", w.Code)
	}

	now := time.Now().Add(-5 * time.Minute)
	sp := func(trace, id, parent, name, status string, dur time.Duration, attrs map[string]any) store.TraceSpan {
		return store.TraceSpan{TraceID: trace, SpanID: id, ParentSpanID: parent, ProjectID: f.p.ID, EnvironmentID: f.prod.ID,
			ServiceID: f.web.ID, ServiceName: "web", Component: "service", Name: name, Kind: "server", Status: status,
			StartTime: now, EndTime: now.Add(dur), Attributes: attrs}
	}
	t1, t2 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := f.st.InsertTraceSpans(f.ctx, []store.TraceSpan{
		sp(t1, "1111111111111111", "", "GET /checkout", "unset", 800*time.Millisecond, map[string]any{"http.route": "/checkout"}),
		sp(t1, "2222222222222222", "1111111111111111", "SELECT orders", "error", 100*time.Millisecond, nil),
		sp(t2, "3333333333333333", "", "GET /health", "ok", time.Millisecond, map[string]any{"http.route": "/health"}),
	}); err != nil {
		t.Fatal(err)
	}
	all := decodeBody[[]store.TraceSummary](t, req(h, "GET", envBase+"/traces?since=1h", "", auth))
	if len(all) != 2 {
		t.Fatalf("traces: %+v", all)
	}
	for q, want := range map[string]int{
		"filter=%40status%3Aerror": 1,
		"errors=true":              1,
		"filter=%40http.route%3A%2Fhealth&errors=true": 0,
		"filter=%40duration%3A%3E500":                  1,
		"filter=health":                                1,
		"service=" + f.other.ID:                        0,
		"since=1m":                                     0,
	} {
		w := req(h, "GET", envBase+"/traces?"+q, "", auth)
		if w.Code != 200 {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
			continue
		}
		if got := decodeBody[[]store.TraceSummary](t, w); len(got) != want {
			t.Errorf("%s: %d traces, want %d", q, len(got), want)
		}
	}
	for _, q := range []string{"filter=(oops", "limit=0", "since=yesterday"} {
		if w := req(h, "GET", envBase+"/traces?"+q, "", auth); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	errs := decodeBody[[]store.TraceSummary](t, req(h, "GET", envBase+"/traces?errors=1", "", auth))
	if len(errs) != 1 || errs[0].TraceID != t1 || errs[0].RootName != "GET /checkout" || errs[0].SpanCount != 2 || errs[0].ErrorCount != 1 {
		t.Errorf("summary: %+v", errs)
	}
	// traceparent 전체를 넣어도 열린다
	w = req(h, "GET", envBase+"/traces/00-"+t1+"-1111111111111111-01", "", auth)
	if w.Code != 200 || len(decodeBody[[]store.TraceSpan](t, w)) != 2 {
		t.Errorf("get by traceparent: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", envBase+"/traces/"+t1+"?max_spans=1", "", auth); len(decodeBody[[]store.TraceSpan](t, w)) != 1 {
		t.Errorf("max_spans")
	}
	for path, code := range map[string]int{
		envBase + "/traces/xyz":                              http.StatusBadRequest,
		envBase + "/traces/cccccccccccccccccccccccccccccccc": http.StatusNotFound,
	} {
		if w := req(h, "GET", path, "", auth); w.Code != code {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	keys := decodeBody[[]string](t, req(h, "GET", envBase+"/trace-attributes", "", auth))
	if len(keys) != 1 || keys[0] != "http.route" {
		t.Errorf("attribute keys: %v", keys)
	}
	// 단일 상태에는 마지막 span 시각이 붙는다
	got = decodeBody[map[string]any](t, req(h, "GET", inst, "", auth))
	if got["last_service_span_at"] == nil || got["last_edge_span_at"] != nil {
		t.Errorf("status: %v", got)
	}
}
