package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func TestAddDomainTunnelDefaultsAndClaims(t *testing.T) {
	f := newGHFixture(t)
	f.mgr.Cfg.PublicIPv4 = "203.0.113.7"
	caddy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(caddy.Close)
	port, _ := strconv.Atoi(caddy.URL[strings.LastIndex(caddy.URL, ":")+1:])
	f.mgr.Proxy, f.mgr.HTTPLogs = proxy.New(proxy.Config{AdminPort: port}), deploy.NewHTTPLogHub(10)
	pv := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}

	// 직접 받는 노드: TLS 설정은 비운 그대로(기존 동작)
	code, out := f.call(f.srv.addDomain, "POST", "/x", `{"host":"direct.example.org"}`, pv)
	if code != 201 || out["tls"] != nil || out["trust_forwarded_proto"] != nil {
		t.Fatalf("direct: %d %v", code, out)
	}
	if out["status"] != "pending_dns" || out["effective_port"] != float64(8080) {
		t.Fatalf("view: %v", out)
	}
	vf, _ := out["verification"].(map[string]any)
	txt, _ := vf["txt"].(map[string]any)
	if txt["name"] != "_aven-verify.direct.example.org" || !strings.HasPrefix(txt["value"].(string), "aven-verify=") || len(txt["value"].(string)) != len("aven-verify=")+32 {
		t.Fatalf("verification: %v", vf)
	}
	if recs, _ := out["dns"].([]any); len(recs) != 1 || recs[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("records: %v", out["dns"])
	}

	// 터널 노드: tls=off, trust_forwarded_proto=true를 명시적으로 넣는다
	if err := f.st.SetNodeIngress(f.ctx, store.LocalNodeID, store.NodeIngress{IngressMode: store.IngressCloudflareTunnel}); err != nil {
		t.Fatal(err)
	}
	code, out = f.call(f.srv.addDomain, "POST", "/x", `{"host":"tunnel.example.org"}`, pv)
	if code != 201 || out["tls"] != "off" || out["trust_forwarded_proto"] != true {
		t.Fatalf("tunnel: %d %v", code, out)
	}
	if recs, _ := out["dns"].([]any); len(recs) != 1 || recs[0].(map[string]any)["type"] != "CNAME" ||
		!strings.Contains(recs[0].(map[string]any)["value"].(string), "cfargotunnel.com") {
		t.Fatalf("tunnel records: %v", out["dns"])
	}
	// 사용자가 정한 tls는 그대로
	code, out = f.call(f.srv.addDomain, "POST", "/x", `{"host":"explicit.example.org","tls":"internal"}`, pv)
	if code != 201 || out["tls"] != "internal" || out["trust_forwarded_proto"] != nil {
		t.Fatalf("explicit tls: %d %v", code, out)
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	for _, d := range in.Domains {
		if d.Host == "direct.example.org" && (d.TLS != "" || d.TrustForwardedProto) {
			t.Fatalf("existing domain changed: %+v", d)
		}
	}

	// 다른 서비스가 같은 호스트를 추가하면 409(한국어)
	code, out = f.call(f.srv.addDomain, "POST", "/x", `{"host":"direct.example.org"}`, map[string]string{"sid": f.db.ID, "eid": f.prod.ID})
	if code != 409 || !strings.Contains(out["error"].(string), "이미 다른 서비스") {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	// claim 행이 있고, 지우면 사라진다
	if ds, err := f.st.GetDomainState(f.ctx, "direct.example.org"); err != nil || ds.InstanceID != in.ID {
		t.Fatalf("claim: %+v %v", ds, err)
	}
	if code, _ := f.call(f.srv.deleteDomain, "DELETE", "/x", "", map[string]string{"sid": f.web.ID, "eid": f.prod.ID, "host": "direct.example.org"}); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := f.st.GetDomainState(f.ctx, "direct.example.org"); err == nil {
		t.Fatal("claim survived delete")
	}
	// 지운 뒤에는 다른 서비스가 가질 수 있다
	if code, out := f.call(f.srv.addDomain, "POST", "/x", `{"host":"direct.example.org"}`, map[string]string{"sid": f.db.ID, "eid": f.prod.ID}); code != 201 {
		t.Fatalf("re-add elsewhere: %d %v", code, out)
	}
	// 강제 확인(확인기 없음 → 저장된 상태로 응답)
	if code, out := f.call(f.srv.verifyDomain, "POST", "/x", "", map[string]string{"sid": f.web.ID, "eid": f.prod.ID, "host": "tunnel.example.org"}); code != 200 || out["host"] != "tunnel.example.org" {
		t.Fatalf("verify: %d %v", code, out)
	}
	if code, _ := f.call(f.srv.verifyDomain, "POST", "/x", "", map[string]string{"sid": f.web.ID, "eid": f.prod.ID, "host": "nope.example.org"}); code != 404 {
		t.Fatalf("verify missing: %d", code)
	}
}

func TestDomainRecordsIngress(t *testing.T) {
	// ingress 이름이 있으면 CNAME을 권장하고 AAAA/A는 대안
	recs, warn := domainRecords("app.example.org", []nodeIP{{v6: "2001:db8::1", v4: "203.0.113.1", ingress: store.NodeIngress{IngressMode: "direct", IngressHostname: "main.ingress.example.net"}}})
	if len(recs) != 3 || recs[0].Type != "CNAME" || recs[0].Value != "main.ingress.example.net" || recs[0].Alternative || !recs[1].Alternative || len(warn) != 0 {
		t.Fatalf("ingress: %+v %v", recs, warn)
	}
	// IPv6 전용 노드: 경고
	recs, warn = domainRecords("app.example.org", []nodeIP{{v6: "2001:db8::2", ingress: store.NodeIngress{IngressMode: "direct"}}})
	if len(recs) != 1 || recs[0].Type != "AAAA" || len(warn) != 1 {
		t.Fatalf("v6-only: %+v %v", recs, warn)
	}
	// 레코드 상태: 프록시 뒤는 proxied, 맞으면 ok, 다른 곳이면 mismatch, 확인 전이면 pending
	checked := time.Now()
	recs = dnsRecords("app.example.org", []string{"2001:db8::1"}, []string{"203.0.113.1"})
	annotateRecords(recs, &store.DomainState{DNSStatus: store.DNSProxied, Proxy: "cloudflare", CheckedAt: &checked,
		Observed: []store.ObservedRR{{Type: "A", Value: "104.16.0.1"}}})
	if recs[0].Status != "proxied" || recs[1].Status != "proxied" || len(recs[1].Current) != 1 {
		t.Fatalf("proxied: %+v", recs)
	}
	annotateRecords(recs, &store.DomainState{DNSStatus: store.DNSPendingDNS, Proxy: "none", CheckedAt: &checked,
		Observed: []store.ObservedRR{{Type: "A", Value: "203.0.113.1"}, {Type: "AAAA", Value: "2001:db8::9"}}})
	if recs[0].Status != "mismatch" || recs[1].Status != "ok" {
		t.Fatalf("mixed: %+v", recs)
	}
	annotateRecords(recs, nil)
	if recs[0].Status != "pending" {
		t.Fatalf("unchecked: %+v", recs)
	}
}

func TestPatchNodeIngress(t *testing.T) {
	f := newGHFixture(t)
	call := func(nid, body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPatch, "/api/nodes/"+nid, bytes.NewBufferString(body))
		r.SetPathValue("nid", nid)
		w := httptest.NewRecorder()
		f.srv.patchNode(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call(store.LocalNodeID, `{"ingress_mode":"cloudflare_tunnel","ingress_hostname":"Main.Ingress.Example.net.","tcp_hostname":"tcp.example.net"}`)
	if code != 200 || out["ingress_mode"] != "cloudflare_tunnel" || out["ingress_hostname"] != "main.ingress.example.net" || out["tcp_hostname"] != "tcp.example.net" {
		t.Fatalf("patch: %d %v", code, out)
	}
	n, _ := f.st.LocalNode(f.ctx)
	if !n.Tunnel() || n.IngressHostname != "main.ingress.example.net" {
		t.Fatalf("persisted: %+v", n.NodeIngress)
	}
	// 보낸 키만 바뀐다
	if code, out := call(store.LocalNodeID, `{"tcp_hostname":""}`); code != 200 || out["ingress_mode"] != "cloudflare_tunnel" || out["tcp_hostname"] != "" {
		t.Fatalf("partial: %d %v", code, out)
	}
	for _, bad := range []string{`{"ingress_mode":"magic"}`, `{"ingress_hostname":"not a host"}`, `{"ingress_hostname":"*.example.net"}`} {
		if code, _ := call(store.LocalNodeID, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// 로컬 노드는 env가 우선한다
	t.Setenv("KEEL_INGRESS_MODE", "direct")
	t.Setenv("KEEL_INGRESS_HOSTNAME", "env.example.net")
	f.mgr.Cfg.TCPProxyDomain = "tcp-env.example.net"
	ing := f.srv.nodeIngress(f.ctx, store.LocalNodeID)
	if ing.Tunnel() || ing.IngressHostname != "env.example.net" || ing.TCPHostname != "tcp-env.example.net" {
		t.Fatalf("env override: %+v", ing)
	}
	if code, _ := call("nod_missing", `{"ingress_mode":"direct"}`); code != 404 {
		t.Fatalf("missing node: %d", code)
	}
}
