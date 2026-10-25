package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// fixtureProxy는 Caddy 관리 API 대신 /load를 받아 주는 가짜 서버를 붙이고, docker 명령은 실행되지 않게 한다.
func fixtureProxy(t *testing.T, f *ghFixture) {
	t.Helper()
	caddy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(caddy.Close)
	port, _ := strconv.Atoi(caddy.URL[strings.LastIndex(caddy.URL, ":")+1:])
	f.mgr.Proxy, f.mgr.HTTPLogs = proxy.New(proxy.Config{AdminPort: port}), deploy.NewHTTPLogHub(10)
	f.mgr.Docker = &node.Docker{Bin: "/bin/false"}
}

func TestRenameGeneratedDomainMovesAuthAndClaim(t *testing.T) {
	f := newGHFixture(t)
	fixtureProxy(t, f)
	pv := map[string]string{"sid": f.web.ID, "eid": f.prod.ID}
	code, out := f.call(f.srv.addDomain, "POST", "/x", `{}`, pv)
	if code != 201 || out["host"] != "web.example.com" || out["kind"] != "generated" {
		t.Fatalf("generate: %d %v", code, out)
	}
	host := func(h string) map[string]string {
		return map[string]string{"sid": f.web.ID, "eid": f.prod.ID, "host": h}
	}
	if code, out := f.call(f.srv.patchDomain, "PATCH", "/x", `{"basic_auth":{"username":"admin","password":"s3cret"}}`, host("web.example.com")); code != 200 {
		t.Fatalf("auth: %d %v", code, out)
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	hash, err := f.st.GetSetting(f.ctx, store.DomainAuthKey(in.ID, "web.example.com"))
	if err != nil || hash == "" {
		t.Fatalf("hash: %q %v", hash, err)
	}
	before, err := f.st.GetDomainState(f.ctx, "web.example.com")
	if err != nil {
		t.Fatal(err)
	}

	// 이름과 포트를 같이 바꾼다(UI가 보내는 모양)
	code, out = f.call(f.srv.patchDomain, "PATCH", "/x", `{"host":"Shop.example.com","target_port":3000}`, host("web.example.com"))
	if code != 200 || out["host"] != "shop.example.com" || out["target_port"] != float64(3000) || out["kind"] != "generated" {
		t.Fatalf("rename: %d %v", code, out)
	}
	in, _ = f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	var renamed *store.Domain
	for i, d := range in.Domains {
		if d.Host == "web.example.com" {
			t.Fatal("old host still in spec")
		}
		if d.Host == "shop.example.com" {
			renamed = &in.Domains[i]
		}
	}
	if renamed == nil || renamed.BasicAuth == nil || renamed.BasicAuth.Username != "admin" || !renamed.BasicAuth.HasPassword {
		t.Fatalf("renamed domain: %+v", renamed)
	}
	if h, _ := f.st.GetSetting(f.ctx, store.DomainAuthKey(in.ID, "shop.example.com")); h != hash {
		t.Fatalf("auth hash not moved: %q", h)
	}
	if h, err := f.st.GetSetting(f.ctx, store.DomainAuthKey(in.ID, "web.example.com")); err == nil && h != "" {
		t.Fatalf("old auth key left: %q", h)
	}
	after, err := f.st.GetDomainState(f.ctx, "shop.example.com")
	if err != nil || after.InstanceID != in.ID || !after.CreatedAt.Equal(before.CreatedAt) || after.VerifyMethod != before.VerifyMethod {
		t.Fatalf("claim not moved: %+v %v (before %+v)", after, err, before)
	}
	if _, err := f.st.GetDomainState(f.ctx, "web.example.com"); err == nil {
		t.Fatal("old claim left")
	}

	// 거부: 커스텀 도메인, 접미사 변경, 잘못된 label, 다른 서비스가 가진 이름, basic_auth와 함께
	if code, _ := f.call(f.srv.patchDomain, "PATCH", "/x", `{"host":"other.acme.dev"}`, host("app.acme.dev")); code != 400 {
		t.Errorf("custom rename: %d", code)
	}
	for _, bad := range []string{`{"host":"shop.other.org"}`, `{"host":"a.b.example.com"}`, `{"host":"-bad.example.com"}`, `{"host":"shop2.example.com","basic_auth":null}`, `{"host":5}`} {
		if code, out := f.call(f.srv.patchDomain, "PATCH", "/x", bad, host("shop.example.com")); code != 400 {
			t.Errorf("%s: %d %v", bad, code, out)
		}
	}
	if code, _ := f.call(f.srv.addDomain, "POST", "/x", `{"host":"taken.example.com"}`, map[string]string{"sid": f.db.ID, "eid": f.prod.ID}); code != 201 {
		t.Fatalf("other service domain: %d", code)
	}
	if code, out := f.call(f.srv.patchDomain, "PATCH", "/x", `{"host":"taken.example.com"}`, host("shop.example.com")); code != 409 {
		t.Errorf("taken: %d %v", code, out)
	}
	// 같은 이름(대소문자만 다름)은 이름 변경이 아니다
	if code, out := f.call(f.srv.patchDomain, "PATCH", "/x", `{"host":"SHOP.example.com","target_port":4000}`, host("shop.example.com")); code != 200 || out["host"] != "shop.example.com" {
		t.Errorf("same host: %d %v", code, out)
	}
}

func TestTCPProxyTunnelRefusedAndPatchKeepsPublicPort(t *testing.T) {
	f := newGHFixture(t)
	fixtureProxy(t, f)
	t.Setenv("KEEL_INGRESS_MODE", "")
	f.mgr.Cfg.TCPPortMin, f.mgr.Cfg.TCPPortMax, f.mgr.Cfg.ProxyBind = 29900, 29999, "127.0.0.1"
	pv := map[string]string{"sid": f.db.ID, "eid": f.prod.ID}

	// 공인 IP가 없는 터널 노드: 409
	if err := f.st.SetNodeIngress(f.ctx, store.LocalNodeID, store.NodeIngress{IngressMode: store.IngressCloudflareTunnel}); err != nil {
		t.Fatal(err)
	}
	code, out := f.call(f.srv.addTCPProxy, "POST", "/x", `{"container_port":5432}`, pv)
	if code != 409 || out["error"] != tcpNoPublicIPReason {
		t.Fatalf("tunnel: %d %v", code, out)
	}
	if code, out := f.call(f.srv.getTCPProxy, "GET", "/x", "", pv); code != 200 || out["available"] != false || out["enabled"] != false {
		t.Fatalf("tunnel view: %d %v", code, out)
	}

	if err := f.st.SetNodeIngress(f.ctx, store.LocalNodeID, store.NodeIngress{IngressMode: store.IngressDirect}); err != nil {
		t.Fatal(err)
	}
	// 프록시가 없으면 PATCH는 404
	if code, _ := f.call(f.srv.patchTCPProxy, "PATCH", "/x", `{"container_port":5433}`, pv); code != 404 {
		t.Fatalf("patch without proxy: %d", code)
	}
	code, out = f.call(f.srv.addTCPProxy, "POST", "/x", `{"container_port":5432}`, pv)
	if code != 200 {
		t.Fatalf("enable: %d %v", code, out)
	}
	in, _ := f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	pub := in.TCPProxy.PublicPort
	if pub < 29900 || pub > 29999 || in.TCPProxy.ContainerPort != 5432 {
		t.Fatalf("proxy: %+v", in.TCPProxy)
	}
	// public_port를 보내도 무시한다(접속 주소는 그대로)
	code, out = f.call(f.srv.patchTCPProxy, "PATCH", "/x", `{"container_port":6543,"public_port":1}`, pv)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, out)
	}
	in, _ = f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	if in.TCPProxy.PublicPort != pub || in.TCPProxy.ContainerPort != 6543 {
		t.Fatalf("patched: %+v (public was %d)", in.TCPProxy, pub)
	}
	for _, bad := range []string{`{"container_port":0}`, `{"container_port":70000}`, `{}`} {
		if code, _ := f.call(f.srv.patchTCPProxy, "PATCH", "/x", bad, pv); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, out = f.call(f.srv.getTCPProxy, "GET", "/x", "", pv)
	if code != 200 || out["enabled"] != true || out["public_port"] != float64(pub) || out["container_port"] != float64(6543) || out["host"] != "example.com" || out["available"] != true {
		t.Fatalf("view: %d %v", code, out)
	}
	// 이미 있는 프록시는 노드가 터널로 바뀌어도 내부 포트를 바꿀 수 있다(새로 여는 것만 막는다)
	f.st.SetNodeIngress(f.ctx, store.LocalNodeID, store.NodeIngress{IngressMode: store.IngressCloudflareTunnel})
	if code, _ := f.call(f.srv.addTCPProxy, "POST", "/x", `{"container_port":5432}`, pv); code != 200 {
		t.Fatalf("existing proxy port change on tunnel node: %d", code)
	}
}

func TestGraphQLTCPProxyMapping(t *testing.T) {
	f := newGHFixture(t)
	in, _ := f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	type call struct{ method, path, body string }
	var calls []call
	enabled := false
	inst := "/api/services/" + f.db.ID + "/instances/" + f.prod.ID
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(b)})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == inst:
			writeJSON(w, 200, map[string]any{"id": in.ID, "service_id": f.db.ID, "environment_id": f.prod.ID})
		case r.Method == http.MethodGet && r.URL.Path == inst+"/tcp-proxy":
			if !enabled {
				writeJSON(w, 200, map[string]any{"enabled": false, "host": "example.com", "available": true})
				return
			}
			writeJSON(w, 200, map[string]any{"enabled": true, "host": "example.com", "public_port": 20000, "container_port": 5432, "available": true})
		case r.Method == http.MethodPost && r.URL.Path == inst+"/tcp-proxy":
			enabled = true
			writeJSON(w, 200, map[string]any{"id": in.ID})
		default:
			w.WriteHeader(204)
		}
	})
	schema, err := newGraphQLSchema()
	if err != nil {
		t.Fatal(err)
	}
	run := func(q string) (map[string]any, string) {
		t.Helper()
		calls = nil
		r := httptestRequest()
		exec := &graphQLExecution{rest: mux, request: r, store: f.st}
		res := graphqlDo(schema, q, context.WithValue(r.Context(), graphQLContextKey{}, exec))
		if len(res.Errors) > 0 {
			return nil, res.Errors[0].Message
		}
		return res.Data.(map[string]any), ""
	}
	q := `{ tcpProxies(serviceId: "` + f.db.ID + `", environmentId: "` + f.prod.ID + `") { id domain proxyPort applicationPort serviceId environmentId } }`
	if data, e := run(q); e != "" || len(dig(data, "tcpProxies").([]any)) != 0 {
		t.Fatalf("empty: %v %s", data, e)
	}
	data, e := run(`mutation { tcpProxyCreate(input: {serviceId: "` + f.db.ID + `", environmentId: "` + f.prod.ID + `", applicationPort: 5432}) { id domain proxyPort applicationPort } }`)
	if e != "" || dig(data, "tcpProxyCreate.id") != "tcp_"+in.ID || dig(data, "tcpProxyCreate.proxyPort") != 20000 || dig(data, "tcpProxyCreate.applicationPort") != 5432 ||
		dig(data, "tcpProxyCreate.domain") != "example.com" {
		t.Fatalf("create: %v %s", data, e)
	}
	if calls[0].method != http.MethodPost || calls[0].path != inst+"/tcp-proxy" || calls[0].body != `{"container_port":5432}` {
		t.Fatalf("create calls: %+v", calls)
	}
	if data, e := run(q); e != "" || dig(data, "tcpProxies.0.id") != "tcp_"+in.ID || dig(data, "tcpProxies.0.serviceId") != f.db.ID {
		t.Fatalf("list: %v %s", data, e)
	}
	if _, e := run(`mutation { tcpProxyDelete(id: "tcp_` + in.ID + `") }`); e != "" || len(calls) != 1 || calls[0].method != http.MethodDelete || calls[0].path != inst+"/tcp-proxy" {
		t.Fatalf("delete: %s %+v", e, calls)
	}
	for _, bad := range []string{"tcp_", "ins_x", "tcp_ins_missing"} {
		if _, e := run(`mutation { tcpProxyDelete(id: "` + bad + `") }`); e == "" {
			t.Errorf("delete %q accepted", bad)
		}
	}
	// serviceDomainUpdate: id로 고르고 domain이 다르면 이름 변경(host)으로 보낸다
	id := domainID(f.web.ID, f.prod.ID, "web.example.com")
	if _, e := run(`mutation { serviceDomainUpdate(input: {serviceDomainId: "` + id + `", domain: "shop.example.com", targetPort: 3000}) }`); e != "" ||
		len(calls) != 1 || calls[0].method != http.MethodPatch || !strings.HasSuffix(calls[0].path, "/domains/web.example.com") ||
		!strings.Contains(calls[0].body, `"host":"shop.example.com"`) || !strings.Contains(calls[0].body, `"target_port":3000`) {
		t.Fatalf("serviceDomainUpdate rename: %s %+v", e, calls)
	}
}
