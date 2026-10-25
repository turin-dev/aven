package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 실제 REST(in-process)로: 이름 사용 가능 확인(환경 단위·서비스 단위), 이름 변경(대기 변경), ipv6EgressEnabled 읽기.
func TestGraphQLPrivateNetworkEndpointREST(t *testing.T) {
	f := newGQLFixture(t)
	env, sid := f.env.ID, f.svc.ID
	avail := func(prefix, extra string) bool {
		t.Helper()
		data := mustGQL(t, f.h, f.owner, `{ privateNetworkEndpointNameAvailable(environmentId: "`+env+`", prefix: "`+prefix+`"`+extra+`) }`, nil)
		return data["privateNetworkEndpointNameAvailable"] == true
	}
	// 환경 단위: 서비스 이름 "web"은 쓰는 중이다. 서비스 단위(serviceId)면 자기 이름이라 쓸 수 있다.
	if avail("web", "") || !avail("web", `, serviceId: "`+sid+`"`) || !avail("api-internal", `, privateNetworkId: "ignored"`) {
		t.Fatal("availability")
	}
	if avail("shared", "") || avail("Bad_Name", "") || avail("", "") {
		t.Fatal("reserved/invalid/empty names must be unavailable")
	}

	data := mustGQL(t, f.h, f.owner, `mutation { privateNetworkEndpointRename(serviceId: "`+sid+`", environmentId: "`+env+`", dnsName: "Frontend") }`, nil)
	if data["privateNetworkEndpointRename"] != true {
		t.Fatalf("rename: %v", data)
	}
	in, err := f.st.GetInstanceFor(context.Background(), sid, env)
	if err != nil || in.Spec.PrivateDNSName != "frontend" {
		t.Fatalf("spec: %+v %v", in.Spec.PrivateDNSName, err)
	}
	// endpoint id(pne_<instance>)로도 이름을 바꾼다. 서비스 이름으로 바꾸면 기본값으로 돌아간다.
	data = mustGQL(t, f.h, f.owner, `mutation { privateNetworkEndpointRename(id: "pne_`+in.ID+`", environmentId: "`+env+`", dnsName: "web") }`, nil)
	if data["privateNetworkEndpointRename"] != true {
		t.Fatalf("rename by id: %v", data)
	}
	if in, _ = f.st.GetInstanceFor(context.Background(), sid, env); in.Spec.PrivateDNSName != "" {
		t.Fatalf("reset: %q", in.Spec.PrivateDNSName)
	}
	for _, bad := range []string{
		`privateNetworkEndpointRename(environmentId: "` + env + `", dnsName: "x")`,
		`privateNetworkEndpointRename(id: "pne_missing", environmentId: "` + env + `", dnsName: "x")`,
		`privateNetworkEndpointRename(serviceId: "` + sid + `", environmentId: "` + env + `", dnsName: "keel-x")`,
	} {
		if out := gql(t, f.h, f.owner, `mutation { `+bad+` }`, nil); len(out.Errors) == 0 {
			t.Errorf("accepted: %s", bad)
		}
	}

	// ipv6EgressEnabled: 값이 없으면 기본값(켜짐)이다. 켜기는 바뀌는 것이 없어 노드에 묻지 않는다.
	q := `{ serviceInstance(serviceId: "` + sid + `", environmentId: "` + env + `") { ipv6EgressEnabled } }`
	if v := dig(mustGQL(t, f.h, f.owner, q, nil), "serviceInstance.ipv6EgressEnabled"); v != true {
		t.Fatalf("default ipv6EgressEnabled: %v", v)
	}
	mustGQL(t, f.h, f.owner, `mutation { serviceInstanceUpdate(serviceId: "`+sid+`", environmentId: "`+env+`", input: {ipv6EgressEnabled: true}) }`, nil)
	off := false
	in.OutboundIPv6 = &off
	if err := f.st.UpdateInstanceConfig(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if v := dig(mustGQL(t, f.h, f.owner, q, nil), "serviceInstance.ipv6EgressEnabled"); v != false {
		t.Fatalf("stored off: %v", v)
	}
}

// 가짜 REST로: privateNetworkEndpoint의 모양, serviceInstanceUpdate.ipv6EgressEnabled → outbound_ipv6.
func TestGraphQLPrivateNetworkEndpointMapping(t *testing.T) {
	f := newGHFixture(t)
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	inst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	type call struct{ method, path, body string }
	var calls []call
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(b)})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == inst:
			writeJSON(w, 200, map[string]any{"id": in.ID, "service_id": f.web.ID, "environment_id": f.prod.ID})
		case r.Method == http.MethodGet && r.URL.Path == inst+"/networking":
			writeJSON(w, 200, map[string]any{"private": map[string]any{
				"dns_name": "frontend", "fqdn": "frontend.production.keel.internal",
				"aliases": []string{"web", "web.production.keel.internal", "frontend", "frontend.production.keel.internal"},
				"ipv4":    []string{"172.20.0.5"}, "ipv6": []string{"fd00::5"}, "mesh_ports": []int{},
			}})
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
	data, e := run(`{ privateNetworkEndpoint(serviceId: "` + f.web.ID + `", environmentId: "` + f.prod.ID + `", privateNetworkId: "x") {
		publicId dnsName fqdn privateIps serviceInstanceId tags aliases serviceId environmentId } }`)
	if e != "" {
		t.Fatal(e)
	}
	ep := dig(data, "privateNetworkEndpoint")
	if dig(ep, "publicId") != "pne_"+in.ID || dig(ep, "dnsName") != "frontend" || dig(ep, "fqdn") != "frontend.production.keel.internal" ||
		dig(ep, "privateIps.0") != "fd00::5" || dig(ep, "privateIps.1") != "172.20.0.5" || dig(ep, "serviceInstanceId") != in.ID ||
		dig(ep, "aliases.2") != "frontend" || dig(ep, "tags.1") != "ipv6" || dig(ep, "serviceId") != f.web.ID {
		t.Fatalf("endpoint: %v", ep)
	}

	if _, e := run(`mutation { serviceInstanceUpdate(serviceId: "` + f.web.ID + `", environmentId: "` + f.prod.ID + `", input: {ipv6EgressEnabled: false}) }`); e != "" ||
		len(calls) != 1 || calls[0].method != http.MethodPatch || calls[0].path != inst || calls[0].body != `{"outbound_ipv6":false}` {
		t.Fatalf("ipv6EgressEnabled: %s %+v", e, calls)
	}
	if _, e := run(`mutation { privateNetworkEndpointRename(id: "pne_` + in.ID + `", environmentId: "` + f.prod.ID + `", dnsName: "api") }`); e != "" ||
		len(calls) != 1 || calls[0].path != inst || calls[0].body != `{"private_dns_name":"api"}` {
		t.Fatalf("rename: %s %+v", e, calls)
	}
	if _, e := run(`mutation { privateNetworkEndpointRename(id: "pne_` + in.ID + `", environmentId: "env_other", dnsName: "api") }`); !strings.Contains(e, "another environment") {
		t.Fatalf("cross-env rename: %q", e)
	}
	if _, e := run(`{ privateNetworkEndpointNameAvailable(environmentId: "` + f.prod.ID + `", prefix: "a b") }`); e != "" ||
		calls[0].path != "/api/environments/"+f.prod.ID+"/private-dns/available?name=a+b" {
		t.Fatalf("available path: %s %+v", e, calls)
	}
}
