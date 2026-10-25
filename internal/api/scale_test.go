package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

type scaleAPIFixture struct {
	srv *Server
	st  *store.Store
	svc *store.Service
	env *store.Environment
	in  *store.Instance
	rid string
}

func newScaleAPIFixture(t *testing.T) *scaleAPIFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.CreateJoinToken(ctx, "h-r", "remote", time.Now().Add(time.Hour))
	rn, err := st.ConsumeJoinToken(ctx, "h-r", "")
	if err != nil {
		t.Fatal(err)
	}
	v4 := "198.51.100.7"
	if err := st.SetNodeInfo(ctx, rn.ID, store.NodeAddress{IPv4: &v4}, store.NodeInfo{}); err != nil {
		t.Fatal(err)
	}
	f := &scaleAPIFixture{st: st, rid: rn.ID}
	mgr := &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir(), PublicIPv4: "203.0.113.1", BaseDomain: "aven.example.com"},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Dispatch: func(string, string) {}}
	f.srv = &Server{Store: st, Deploy: mgr, Events: &events.Bus{Store: st}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p := &store.Project{Name: "acme"}
	if f.env, err = st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.svc = &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, f.svc); err != nil {
		t.Fatal(err)
	}
	img := "nginx:1"
	in := &store.Instance{ServiceID: f.svc.ID, EnvironmentID: f.env.ID}
	in.Image = &img
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.in = in
	return f
}

func (f *scaleAPIFixture) do(t *testing.T, h http.HandlerFunc, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "/x", bytes.NewBufferString(body))
	r.SetPathValue("sid", f.svc.ID)
	r.SetPathValue("eid", f.env.ID)
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestPatchPlacementValidation(t *testing.T) {
	f := newScaleAPIFixture(t)
	ctx := context.Background()
	patch := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		return f.do(t, f.srv.patchInstance, "PATCH", body)
	}
	good := `{"placement":[{"node_id":"node_local","replicas":1},{"node_id":"` + f.rid + `","replicas":2}]}`
	w, v := patch(good)
	if w.Code != 200 {
		t.Fatalf("placement: %d %s", w.Code, w.Body.String())
	}
	if v["replicas"].(float64) != 3 || v["node_id"] != store.LocalNodeID || len(v["placement"].([]any)) != 2 {
		t.Fatalf("view: replicas=%v node=%v placement=%v", v["replicas"], v["node_id"], v["placement"])
	}
	for name, body := range map[string]string{
		"unknown node": `{"placement":[{"node_id":"nod_missing","replicas":1}]}`,
		"duplicate":    `{"placement":[{"node_id":"node_local","replicas":1},{"node_id":"node_local","replicas":1}]}`,
		"zero":         `{"placement":[{"node_id":"node_local","replicas":0}]}`,
		"too many":     `{"placement":[{"node_id":"node_local","replicas":6},{"node_id":"` + f.rid + `","replicas":5}]}`,
		"no node":      `{"placement":[{"replicas":1}]}`,
		"replicas":     `{"replicas":5}`,                  // placement가 있으면 replicas만 바꿀 수 없다
		"node_id":      `{"node_id":"` + f.rid + `"}`,     // 주 노드는 placement의 첫 항목
		"sleep":        `{"sleep_when_idle":true}`,        // 여러 노드 + sleep
		"cron":         `{"cron_schedule":"*/5 * * * *"}`, // 여러 노드 + cron
	} {
		if w, _ := patch(body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// 주 노드를 바꾸면 node_id도 따라간다.
	w, v = patch(`{"placement":[{"node_id":"` + f.rid + `","replicas":1},{"node_id":"node_local","replicas":1}]}`)
	if w.Code != 200 || v["node_id"] != f.rid || v["replicas"].(float64) != 2 {
		t.Fatalf("primary change: %d %v", w.Code, v["node_id"])
	}
	// 볼륨이 있으면 여러 노드에 둘 수 없다.
	vol := &store.Volume{ProjectID: f.svc.ProjectID, EnvironmentID: f.env.ID, InstanceID: &f.in.ID, Name: "data", MountPath: "/data"}
	if err := f.st.CreateVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}
	if w, _ := patch(good); w.Code != 400 || !strings.Contains(w.Body.String(), "volume") {
		t.Fatalf("volume: %d %s", w.Code, w.Body.String())
	}
	// 하나짜리 placement는 node_id와 같다(볼륨이 있어도 된다).
	if w, v := patch(`{"placement":[{"node_id":"node_local","replicas":2}]}`); w.Code != 200 || v["replicas"].(float64) != 2 || v["node_id"] != store.LocalNodeID {
		t.Fatalf("single placement: %d %s", w.Code, w.Body.String())
	}
	// placement를 지우면 replicas를 다시 직접 정할 수 있다.
	if w, v := patch(`{"placement":null,"replicas":3}`); w.Code != 200 || v["placement"] != nil || v["replicas"].(float64) != 3 {
		t.Fatalf("clear placement: %d %s", w.Code, w.Body.String())
	}
}

func TestPatchAutoscaleValidationAndPending(t *testing.T) {
	f := newScaleAPIFixture(t)
	ctx := context.Background()
	patch := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		return f.do(t, f.srv.patchInstance, "PATCH", body)
	}
	for name, body := range map[string]string{
		"no target":     `{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":3}}`,
		"max < min":     `{"autoscale":{"enabled":true,"min_replicas":3,"max_replicas":2,"target_cpu_percent":70}}`,
		"max > 10":      `{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":11,"target_cpu_percent":70}}`,
		"cpu <= 0":      `{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":3,"target_cpu_percent":0}}`,
		"mem > 100":     `{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":3,"target_memory_percent":120}}`,
		"long cooldown": `{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":3,"target_rps_per_replica":5,"scale_up_cooldown_s":100000}}`,
	} {
		if w, _ := patch(body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	w, v := patch(`{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":4,"target_cpu_percent":70}}`)
	if w.Code != 200 {
		t.Fatalf("autoscale: %d %s", w.Code, w.Body.String())
	}
	as := v["autoscale"].(map[string]any)
	if as["scale_up_cooldown_s"].(float64) != 60 || as["scale_down_cooldown_s"].(float64) != 300 {
		t.Fatalf("cooldown defaults: %v", as)
	}
	if v["autoscale_status"] == nil || v["current_replicas"].(float64) != 0 {
		t.Fatalf("status fields: %v %v", v["autoscale_status"], v["current_replicas"])
	}
	// 여러 노드 placement면 min_replicas는 노드 수 이상이어야 한다.
	if w, _ := patch(`{"placement":[{"node_id":"node_local","replicas":1},{"node_id":"` + f.rid + `","replicas":1}]}`); w.Code != 400 {
		t.Fatalf("min < nodes: %d", w.Code)
	}
	// autoscale은 pending change가 아니고, placement는 pending change다.
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec, Status: store.DepLive}
	d.ReqSpec.Autoscale = nil
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.GetDeployment(ctx, d.ID)
	if pc := PendingChanges(in, got); len(pc) != 0 {
		t.Fatalf("autoscale shown as pending: %v", pc)
	}
	in.Placement = []store.Placement{{NodeID: store.LocalNodeID, Replicas: 1}}
	if pc := PendingChanges(in, got); len(pc) != 1 || pc[0] != "placement" {
		t.Fatalf("placement pending: %v", pc)
	}
	// 반대로 배포 스냅샷에만 있는 placement(지운 경우)도 pending이다(omitempty 키).
	got.ReqSpec.Placement = in.Placement
	in.Placement = nil
	if pc := PendingChanges(in, got); len(pc) != 1 || pc[0] != "placement" {
		t.Fatalf("cleared placement pending: %v", pc)
	}
}

func TestScaleEndpointGuards(t *testing.T) {
	f := newScaleAPIFixture(t)
	for _, tc := range []struct {
		body string
		code int
	}{{`{}`, 400}, {`{"replicas":0}`, 400}, {`{"replicas":11}`, 400}, {`{"replicas":2}`, 409}} { // 409: 배포된 적 없음
		if w, _ := f.do(t, f.srv.scaleInstance, "POST", tc.body); w.Code != tc.code {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	if w, _ := f.do(t, f.srv.patchInstance, "PATCH", `{"autoscale":{"enabled":true,"min_replicas":1,"max_replicas":3,"target_cpu_percent":70}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w, _ := f.do(t, f.srv.scaleInstance, "POST", `{"replicas":2}`); w.Code != 409 || !strings.Contains(w.Body.String(), "자동 확장") {
		t.Fatalf("autoscale on: %d %s", w.Code, w.Body.String())
	}
}

func TestDomainDNSForMultiNodePlacement(t *testing.T) {
	f := newScaleAPIFixture(t)
	ctx := context.Background()
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	single := f.srv.domainView(ctx, in, store.Domain{Host: "web.keel.example.com", Kind: "generated"})
	if len(single.DNS) != 0 {
		t.Fatalf("generated single-node domain has records: %+v", single.DNS)
	}
	custom := f.srv.domainView(ctx, in, store.Domain{Host: "shop.example.org", Kind: "custom"})
	if len(custom.DNS) != 1 || custom.DNS[0].Value != "203.0.113.1" {
		t.Fatalf("single node custom: %+v", custom.DNS)
	}
	// live 배포가 로컬 + 원격 노드에 replica를 갖는다 → 두 노드의 A 레코드(round-robin).
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec,
		Status: store.DepLive, NodeID: store.LocalNodeID, Containers: []string{"keel-web-abcdefgh-1"},
		Extra: []store.NodeContainers{{NodeID: f.rid, Containers: []string{"keel-web-abcdefgh-2"}}}}
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID)
	in, _ = f.st.GetInstance(ctx, in.ID)
	for _, kind := range []string{"custom", "generated"} {
		v := f.srv.domainView(ctx, in, store.Domain{Host: "web.example.org", Kind: kind})
		vals := []string{}
		for _, r := range v.DNS {
			if r.Type == "A" {
				vals = append(vals, r.Value)
			}
		}
		if len(vals) != 2 || vals[0] != "203.0.113.1" || vals[1] != "198.51.100.7" {
			t.Fatalf("%s: %+v", kind, v.DNS)
		}
	}
	view := f.srv.instanceView(ctx, f.svc, f.env, in)
	if view.CurrentReplicas != 2 || view.AutoscaleStatus != nil {
		t.Fatalf("view: current=%d status=%v", view.CurrentReplicas, view.AutoscaleStatus)
	}
	if got := dnsRecords("h", []string{"::1", "::1"}, []string{"1.2.3.4"}); len(got) != 2 || got[0].Type != "AAAA" {
		t.Fatalf("dnsRecords dedupe: %+v", got)
	}
}
