package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

// call은 핸들러를 path value와 함께 부른다.
func (f *ghFixture) call(h http.HandlerFunc, method, target, body string, pv map[string]string) (int, map[string]any) {
	f.t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range pv {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestWildcardDomainNormalizeAndDNS(t *testing.T) {
	f := newGHFixture(t)
	f.mgr.Cfg.PublicIPv4 = "203.0.113.7"
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	doms, err := f.srv.normalizeDomains(f.ctx, in, []store.Domain{{Host: " *.Shop.Example.org "}, {Host: "*.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	// 와일드카드는 base domain 아래여도 custom이다(DNS에 * 레코드가 필요하다).
	if doms[0].Host != "*.shop.example.org" || doms[0].Kind != "custom" || doms[1].Kind != "custom" {
		t.Errorf("normalized: %+v", doms)
	}
	for _, bad := range []string{"*.com", "*.*.example.com", "a.*.example.com", "*example.com"} {
		if _, err := f.srv.normalizeDomains(f.ctx, in, []store.Domain{{Host: bad}}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	v := f.srv.domainView(f.ctx, in, doms[0])
	if len(v.DNS) != 1 || v.DNS[0].Type != "A" || v.DNS[0].Name != "*.shop.example.org" || v.DNS[0].Value != "203.0.113.7" {
		t.Errorf("dns records: %+v", v.DNS)
	}
	if dnsProbeHost("*.shop.example.org") != "keel-dns-check.shop.example.org" || dnsProbeHost("a.b.c") != "a.b.c" {
		t.Error("dnsProbeHost")
	}
}

func TestPatchInstanceOverlapDraining(t *testing.T) {
	f := newGHFixture(t)
	pv := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}
	before, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if before.OverlapSeconds != 0 || before.DrainingSeconds != nil {
		t.Fatalf("defaults: %d %v", before.OverlapSeconds, before.DrainingSeconds)
	}
	code, out := f.call(f.srv.patchInstance, "PATCH", "/x", `{"overlap_seconds":30,"draining_seconds":45}`, pv)
	if code != 200 || out["overlap_seconds"] != float64(30) || out["draining_seconds"] != float64(45) {
		t.Fatalf("PATCH: %d %v %v", code, out["overlap_seconds"], out["draining_seconds"])
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	d := &store.Deployment{ReqSpec: before.Spec, VarsRaw: in.Variables}
	pc := strings.Join(PendingChanges(in, d), ",")
	if !strings.Contains(pc, "overlap_seconds") || !strings.Contains(pc, "draining_seconds") {
		t.Errorf("pending changes: %s", pc)
	}
	// 같은 값이면 pending이 아니다(이전 스냅샷에 키가 없어도 기본값과 같다)
	if pc := PendingChanges(before, &store.Deployment{ReqSpec: before.Spec, VarsRaw: before.Variables}); len(pc) != 0 {
		t.Errorf("no-op pending: %v", pc)
	}
	for _, body := range []string{`{"overlap_seconds":601}`, `{"overlap_seconds":-1}`, `{"draining_seconds":301}`, `{"draining_seconds":-2}`} {
		if code, _ := f.call(f.srv.patchInstance, "PATCH", "/x", body, pv); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, out := f.call(f.srv.patchInstance, "PATCH", "/x", `{"draining_seconds":null}`, pv); code != 200 || out["draining_seconds"] != nil {
		t.Errorf("reset draining: %d %v", code, out["draining_seconds"])
	}
}

func TestVolumeSizeLimitAPI(t *testing.T) {
	f := newGHFixture(t)
	pv := map[string]string{"pid": f.p.ID, "eid": f.prod.ID}
	if code, _ := f.call(f.srv.createVolume, "POST", "/x", `{"service_id":"`+f.web.ID+`","mount_path":"/data","size_limit_mb":0}`, pv); code != 400 {
		t.Errorf("size_limit_mb 0 accepted: %d", code)
	}
	code, out := f.call(f.srv.createVolume, "POST", "/x", `{"service_id":"`+f.web.ID+`","mount_path":"/data","size_limit_mb":1024}`, pv)
	if code != 201 || out["size_limit_mb"] != float64(1024) {
		t.Fatalf("create: %d %v", code, out)
	}
	vid := out["id"].(string)
	f.st.SetVolumeLimitState(f.ctx, vid, store.VolumeLimitNear)
	code, out = f.call(f.srv.updateVolume, "PATCH", "/x", `{"size_limit_mb":2048}`, map[string]string{"vid": vid})
	if code != 200 || out["size_limit_mb"] != float64(2048) {
		t.Fatalf("patch: %d %v", code, out)
	}
	v, _ := f.st.GetVolume(f.ctx, vid)
	if v.LimitState != store.VolumeLimitOK || *v.SizeLimitMB != 2048 {
		t.Errorf("limit change must reset the notified state: %+v", v)
	}
	if code, out = f.call(f.srv.updateVolume, "PATCH", "/x", `{"size_limit_mb":null}`, map[string]string{"vid": vid}); code != 200 || out["size_limit_mb"] != nil {
		t.Errorf("clear: %d %v", code, out)
	}
	if code, _ = f.call(f.srv.updateVolume, "PATCH", "/x", `{"size_limit_mb":"big"}`, map[string]string{"vid": vid}); code != 400 {
		t.Errorf("bad type: %d", code)
	}
	// 알림 이벤트로 고를 수 있다
	f.patchProject(`{"notify_events":["volume.near_limit","volume.limit_exceeded"]}`)
	p, _ := f.st.GetProject(f.ctx, f.p.ID)
	if strings.Join(p.NotifyEvents, ",") != "volume.near_limit,volume.limit_exceeded" {
		t.Errorf("notify events: %v", p.NotifyEvents)
	}
}
