package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// hostIP는 main과 같은 주소를 가진 호스트의 ip 대역이다(eth0: ::2/64, ::25/128 deprecated, wg0: 66::1/112).
// 쓰기(replace/del)는 기록만 한다. 진짜 ip는 실행하지 않는다.
type hostIP struct {
	mu     sync.Mutex
	writes []string
}

func (h *hostIP) Run(_ context.Context, args ...string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "-j -6 addr show"):
		return `[{"ifname":"eth0","addr_info":[
			{"family":"inet6","local":"2001:db8:6b:1943::25","prefixlen":128,"scope":"global","deprecated":true,"preferred_life_time":0},
			{"family":"inet6","local":"2001:db8:6b:1943::2","prefixlen":64,"scope":"global","preferred_life_time":4294967295}]},
			{"ifname":"wg0","addr_info":[{"family":"inet6","local":"2001:db8:6b:1943:66::1","prefixlen":112,"scope":"global","preferred_life_time":4294967295}]}]`, nil
	case line == "-j -6 route show default":
		return `[{"dst":"default","dev":"eth0"}]`, nil
	case strings.HasPrefix(line, "-6 addr "):
		h.writes = append(h.writes, line)
		return "", nil
	}
	return "", fmt.Errorf("unexpected ip %s", line)
}

func TestPatchNodeIPv6Pool(t *testing.T) {
	f := newGHFixture(t)
	f.mgr.Cfg.PublicIPv6 = "2001:db8:6b:1943::2"
	f.mgr.IPRunner = &hostIP{}
	call := func(nid, body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPatch, "/api/nodes/"+nid, bytes.NewBufferString(body))
		r.SetPathValue("nid", nid)
		w := httptest.NewRecorder()
		f.srv.patchNode(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	// 원격 노드 하나에 풀이 있다.
	f.st.CreateJoinToken(f.ctx, "h-us", "us", time.Now().Add(time.Hour))
	us, err := f.st.ConsumeJoinToken(f.ctx, "h-us", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetNodeIPv6Pool(f.ctx, us.ID, "2001:db8:6b:1943:e::/80"); err != nil {
		t.Fatal(err)
	}

	code, out := call(store.LocalNodeID, `{"ipv6_pool":" 2001:db8:6b:1943:d::/80 "}`)
	if code != 200 || out["ipv6_pool"] != "2001:db8:6b:1943:d::/80" {
		t.Fatalf("set: %d %v", code, out)
	}
	if n, _ := f.st.LocalNode(f.ctx); n.IPv6Pool != "2001:db8:6b:1943:d::/80" {
		t.Fatalf("persisted: %q", n.IPv6Pool)
	}
	for body, want := range map[string]int{
		`{"ipv6_pool":"2001:db8:6b:1943::/48"}`:     400, // 너무 큼
		`{"ipv6_pool":"2001:db8:6b:1943:d::/121"}`:  400, // 너무 작음
		`{"ipv6_pool":"10.0.0.0/24"}`:               400,
		`{"ipv6_pool":"fd00:1::/80"}`:               400, // ULA
		`{"ipv6_pool":"2001:db8:6b:1943:d::9/80"}`:  400, // 네트워크 주소가 아님
		`{"ipv6_pool":"2001:db8:6b:1943::/64"}`:     409, // ::2 포함
		`{"ipv6_pool":"2001:db8:6b:1943::/120"}`:    409, // ::2 포함
		`{"ipv6_pool":"2001:db8:6b:1943:66::/120"}`: 409, // wg0 주소
		`{"ipv6_pool":"2001:db8:6b:1943:e:1::/96"}`: 409, // us 풀과 겹침
	} {
		code, out := call(store.LocalNodeID, body)
		if code != want {
			t.Errorf("%s: %d %v (want %d)", body, code, out, want)
		}
	}
	code, out = call(store.LocalNodeID, `{"ipv6_pool":"2001:db8:6b:1943:e:1::/96"}`)
	if !strings.Contains(fmt.Sprint(out["error"]), "us") {
		t.Fatalf("overlap message: %v", out)
	}
	// 실패한 요청은 저장 값을 바꾸지 않는다.
	if n, _ := f.st.LocalNode(f.ctx); n.IPv6Pool != "2001:db8:6b:1943:d::/80" {
		t.Fatalf("changed by a rejected request: %q", n.IPv6Pool)
	}
	// 지우기.
	if code, out := call(store.LocalNodeID, `{"ipv6_pool":""}`); code != 200 || out["ipv6_pool"] != "" {
		t.Fatalf("clear: %d %v", code, out)
	}
	// 로컬 노드는 KEEL_IPV6_POOL이 우선한다.
	f.mgr.Cfg.IPv6Pool = "2001:db8:6b:1943:f::/80"
	if code, out := call(store.LocalNodeID, `{"name":"main"}`); code != 200 || out["ipv6_pool"] != "2001:db8:6b:1943:f::/80" {
		t.Fatalf("env override: %d %v", code, out)
	}
}

func TestDomainDedicatedIPv6View(t *testing.T) {
	f := newGHFixture(t)
	f.mgr.Cfg.PublicIPv4, f.mgr.Cfg.PublicIPv6 = "192.0.2.10", "2001:db8:6b:1943::2"
	f.mgr.Cfg.IPv6Pool = "2001:db8:6b:1943:d::/80"
	caddy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(caddy.Close)
	port, _ := strconv.Atoi(caddy.URL[strings.LastIndex(caddy.URL, ":")+1:])
	f.mgr.Proxy, f.mgr.HTTPLogs = proxy.New(proxy.Config{AdminPort: port}), deploy.NewHTTPLogHub(10)
	pv := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}

	// 추가하면 바로 풀에서 배정되고, 권장 AAAA가 전용 주소다. A는 노드 IPv4 그대로.
	// (fixture의 기존 도메인이 먼저 만들어졌으므로 그 뒤 주소를 받는다.)
	code, out := f.call(f.srv.addDomain, "POST", "/x", `{"host":"app.example.org"}`, pv)
	appIP, _ := out["ipv6"].(string)
	if code != 201 || !strings.HasPrefix(appIP, "2001:db8:6b:1943:d::") {
		t.Fatalf("add: %d %v", code, out)
	}
	recs, _ := out["dns"].([]any)
	if len(recs) != 2 || recs[0].(map[string]any)["type"] != "AAAA" || recs[0].(map[string]any)["value"] != appIP ||
		recs[1].(map[string]any)["type"] != "A" || recs[1].(map[string]any)["value"] != "192.0.2.10" {
		t.Fatalf("records: %v", out["dns"])
	}
	code, out = f.call(f.srv.addDomain, "POST", "/x", `{"host":"two.example.org"}`, pv)
	if code != 201 || out["ipv6"] == appIP || !strings.HasPrefix(out["ipv6"].(string), "2001:db8:6b:1943:d::") {
		t.Fatalf("second: %d %v", code, out)
	}

	// netcheck는 전용 주소와 노드 기본 주소를 모두 이 노드로 본다.
	ts, err := f.srv.NetCheckTargets(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tg := range ts {
		if tg.Host == "app.example.org" {
			found = true
			if strings.Join(tg.IPv6, ",") != "2001:db8:6b:1943::2,"+appIP {
				t.Fatalf("target ipv6: %v", tg.IPv6)
			}
		}
	}
	if !found {
		t.Fatal("no netcheck target")
	}

	// 기존 DNS가 공용 주소(::2)를 가리키면 AAAA는 ok이고 안내가 붙는다.
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	now := time.Now()
	if err := f.st.RecordDomainCheck(f.ctx, "app.example.org", in.ID, store.DomainCheck{DNSStatus: store.DNSActive, Proxy: "direct",
		Observed: []store.ObservedRR{{Type: "AAAA", Value: "2001:db8:6b:1943::2"}, {Type: "A", Value: "192.0.2.10"}}, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	var app store.Domain
	for _, d := range in.Domains {
		if d.Host == "app.example.org" {
			app = d
		}
	}
	v := f.srv.domainView(f.ctx, in, app)
	if v.IPv6 != appIP || v.DNS[0].Status != "ok" || v.DNS[0].Note != sharedIPv6Note || v.DNS[1].Status != "ok" {
		t.Fatalf("shared: %+v", v.DNS)
	}
	// 전용 주소를 가리키면 ok, 안내 없음.
	f.st.RecordDomainCheck(f.ctx, "app.example.org", in.ID, store.DomainCheck{DNSStatus: store.DNSActive, Proxy: "direct",
		Observed: []store.ObservedRR{{Type: "AAAA", Value: appIP}}, CheckedAt: now})
	v = f.srv.domainView(f.ctx, in, app)
	if v.DNS[0].Status != "ok" || v.DNS[0].Note != "" {
		t.Fatalf("dedicated: %+v", v.DNS[0])
	}
	// ingress 이름이 있으면 CNAME이 먼저, 대안 AAAA가 전용 주소다.
	t.Setenv("KEEL_INGRESS_HOSTNAME", "main.edge.example.net")
	v = f.srv.domainView(f.ctx, in, app)
	if v.DNS[0].Type != "CNAME" || v.DNS[1].Type != "AAAA" || v.DNS[1].Value != appIP || !v.DNS[1].Alternative {
		t.Fatalf("cname: %+v", v.DNS)
	}
	// 지우면 주소가 풀리고, 다음 도메인이 그 주소를 받는다.
	if code, _ := f.call(f.srv.deleteDomain, "DELETE", "/x", "", map[string]string{"sid": f.web.ID, "eid": f.prod.ID, "host": "app.example.org"}); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, out := f.call(f.srv.addDomain, "POST", "/x", `{"host":"three.example.org"}`, pv); code != 201 || out["ipv6"] != appIP {
		t.Fatalf("reuse: %d %v", code, out)
	}
}

func TestWithDedicatedIPv6Replicas(t *testing.T) {
	nodes := []nodeIP{
		{id: store.LocalNodeID, v6: "2001:db8:6b:1943::2", v4: "192.0.2.10"},
		{id: "nod_us", v6: "2001:db8:2::"},
	}
	recs, _ := domainRecords("app.example.org", withDedicatedIPv6(nodes, "2001:db8:6b:1943:d::2", store.LocalNodeID))
	var aaaa []string
	for _, r := range recs {
		if r.Type == "AAAA" {
			aaaa = append(aaaa, r.Value)
		}
	}
	// home 노드만 전용 주소, 다른 replica 노드는 기본 주소가 추가 AAAA다.
	if strings.Join(aaaa, ",") != "2001:db8:6b:1943:d::2,2001:db8:2::" {
		t.Fatalf("aaaa: %v", aaaa)
	}
	if nodes[0].v6 != "2001:db8:6b:1943::2" {
		t.Fatal("input modified")
	}
	if got := withDedicatedIPv6(nodes, "", store.LocalNodeID); got[0].v6 != "2001:db8:6b:1943::2" {
		t.Fatal("no dedicated address should keep nodes")
	}
}
