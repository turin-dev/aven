package api

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/usage"
)

func TestUsageLimitAPI(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	ctx := f.ctx
	base := "/api/projects/" + f.p.ID
	// 이번 달 첫 시간에 5 코어·시간을 집계해 둔다(watermark는 그 다음 시간).
	ms := usage.MonthStart(time.Now()).Unix()
	f.st.UpsertUsage(ctx, []store.UsageRow{{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, ServiceID: f.web.ID, Hour: ms, CPUCoreSeconds: 5 * 3600}})
	f.st.SetSetting(ctx, "usage_rollup_until", strconv.FormatInt(ms+3600, 10))

	for _, bad := range []string{`{"usage_limit":{"cpu_core_hours":-1}}`, `{"usage_limit":{"cpu_core_hours":1,"action":"delete"}}`, `{"usage_limit":"x"}`} {
		if w := req(h, "PATCH", base, bad, legacy); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	// 한도를 이미 넘은 값으로 정하면 바로 정지한다(action 기본값 stop).
	w := req(h, "PATCH", base, `{"usage_limit":{"cpu_core_hours":1}}`, legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"usage_limit":{"cpu_core_hours":1,"action":"stop"}`) || !strings.Contains(w.Body.String(), `"active":true`) {
		t.Fatalf("set limit: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", base, "", legacy); !strings.Contains(w.Body.String(), `"usage_suspension":{`) {
		t.Errorf("project view: %s", w.Body)
	}
	lim := decodeBody[map[string]any](t, req(h, "GET", base+"/usage/limit", "", legacy))
	if ex, _ := lim["exceeded"].([]any); len(ex) != 1 || ex[0] != "cpu_core_hours" {
		t.Errorf("limit view: %v", lim)
	}
	if u := decodeBody[map[string]any](t, req(h, "GET", base+"/usage", "", legacy)); u["usage_limit"] == nil || u["usage_suspension"] == nil {
		t.Errorf("usage view: %v", u)
	}
	// 정지 중에는 새 배포가 409다.
	w = req(h, "POST", "/api/services/"+f.web.ID+"/instances/"+f.prod.ID+"/deploy", "", legacy)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "사용량 한도") {
		t.Fatalf("deploy while suspended: %d %s", w.Code, w.Body)
	}
	// Enqueue를 거치지 않는 재시작, 수동 scale도 막는다.
	for path, body := range map[string]string{"/restart": "", "/scale": `{"replicas":2}`} {
		w := req(h, "POST", "/api/services/"+f.web.ID+"/instances/"+f.prod.ID+path, body, legacy)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "사용량 한도") {
			t.Errorf("%s while suspended: %d %s", path, w.Code, w.Body)
		}
	}
	// 한도를 넘은 채로는 resume할 수 없다.
	if w := req(h, "POST", base+"/usage/resume", "", legacy); w.Code != 409 {
		t.Errorf("resume over limit: %d %s", w.Code, w.Body)
	}
	// 정지 때 멈춘 서비스가 있었던 것으로 둔다(이 fixture는 docker가 없어 실제 Stop은 하지 않는다).
	in, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil)
	pl, _ := f.st.GetUsageLimit(ctx, f.p.ID)
	pl.Suspension.StoppedInstances = []string{in.ID}
	f.st.SetUsageSuspension(ctx, f.p.ID, pl.Suspension)

	// 한도를 지우면 자동으로 풀린다(서비스는 다시 시작하지 않는다).
	w = req(h, "PATCH", base, `{"usage_limit":null}`, legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"usage_limit":null`) || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatalf("clear limit: %d %s", w.Code, w.Body)
	}
	if n := len(f.queued()); n != 0 {
		t.Fatalf("services restarted automatically: %d", n)
	}
	// resume은 멈춘 서비스를 다시 배포하고 기록을 지운다.
	w = req(h, "POST", base+"/usage/resume", "", legacy)
	if w.Code != 200 || !strings.Contains(w.Body.String(), in.ID) || len(f.queued()) != 1 {
		t.Fatalf("resume: %d %s queued=%v", w.Code, w.Body, f.queued())
	}
	if lim := decodeBody[map[string]any](t, req(h, "GET", base+"/usage/limit", "", legacy)); lim["usage_suspension"] != nil {
		t.Errorf("suspension after resume: %v", lim)
	}
	if w := req(h, "POST", base+"/usage/resume", "", legacy); w.Code != 409 {
		t.Errorf("resume without suspension: %d", w.Code)
	}
	// notify 한도는 멈추지 않는다.
	if w := req(h, "PATCH", base, `{"usage_limit":{"cpu_core_hours":1,"action":"notify"}}`, legacy); w.Code != 200 || !strings.Contains(w.Body.String(), `"usage_suspension":null`) {
		t.Errorf("notify limit: %d %s", w.Code, w.Body)
	}
	evs, _ := f.st.ListEvents(ctx, f.p.ID, "", 100)
	kinds := map[string]int{}
	for _, e := range evs {
		kinds[e.Kind]++
	}
	if kinds["usage.limit_reached"] != 1 || kinds["usage.unsuspended"] != 1 || kinds["usage.resumed"] != 1 {
		t.Errorf("events: %v", kinds)
	}
}
