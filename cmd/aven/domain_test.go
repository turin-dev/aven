package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

type recordedCall struct{ method, path, body string }

// fakeNetServer는 도메인·TCP 프록시·networking REST를 흉내 내고 받은 요청을 기록한다.
func fakeNetServer(t *testing.T) (*config, *[]recordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	inst := "/api/services/svc_1/instances/env_1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, recordedCall{r.Method, r.URL.Path, string(b)})
		mu.Unlock()
		enc := json.NewEncoder(w)
		switch {
		case r.Method == "POST" && r.URL.Path == inst+"/domains":
			w.WriteHeader(201)
			enc.Encode(map[string]any{"host": "web.keel.example.com", "kind": "generated", "status": "active", "dns": []any{}})
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, inst+"/domains/"):
			var body map[string]any
			json.Unmarshal(b, &body)
			host, _ := body["host"].(string)
			if host == "" {
				host = strings.TrimPrefix(r.URL.Path, inst+"/domains/")
			}
			enc.Encode(map[string]any{"host": host, "kind": "generated", "target_port": body["target_port"], "effective_port": 8080})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/verify"):
			enc.Encode(map[string]any{"host": "api.example.org", "kind": "custom", "status": "active", "proxy": "direct"})
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == inst:
			enc.Encode(map[string]any{"domains": []any{
				map[string]any{"host": "web.keel.example.com", "kind": "generated", "status": "active", "effective_port": 8080},
				map[string]any{"host": "api.example.org", "kind": "custom", "status": "proxied", "proxy": "cloudflare", "target_port": 3000},
			}})
		case r.Method == "GET" && r.URL.Path == inst+"/networking":
			enc.Encode(map[string]any{"effective_port": 5432})
		case r.URL.Path == inst+"/tcp-proxy" && r.Method == "GET":
			enc.Encode(map[string]any{"enabled": true, "host": "example.com", "public_port": 20001, "container_port": 5432, "available": true})
		case r.URL.Path == inst+"/tcp-proxy":
			enc.Encode(map[string]any{})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KEEL_TOKEN", "tok")
	dir, _ := os.Getwd()
	c := &config{URL: srv.URL, Links: map[string]*link{dir: {ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production", ServiceID: "svc_1", Service: "web"}}}
	return c, &calls
}

// captureStdout는 f가 표준 출력에 쓴 내용이다.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestCmdDomainRequests(t *testing.T) {
	c, calls := fakeNetServer(t)
	inst := "/api/services/svc_1/instances/env_1"
	for _, tc := range []struct {
		args []string
		want []recordedCall
		out  string
	}{
		// 예전 동작: 호스트 없음 = 생성 도메인, 호스트와 --port(뒤에 와도 된다)
		{[]string{}, []recordedCall{{"POST", inst + "/domains", `{}`}}, "https://web.keel.example.com (generated, active)"},
		{[]string{"API.example.org", "--port", "3000"}, []recordedCall{{"POST", inst + "/domains", `{"host":"api.example.org","target_port":3000}`}}, ""},
		{[]string{"add", "-p", "81"}, []recordedCall{{"POST", inst + "/domains", `{"target_port":81}`}}, ""},
		{[]string{"list"}, []recordedCall{{"GET", inst, ""}}, "api.example.org"},
		{[]string{"status", "api.example.org"}, []recordedCall{{"GET", inst, ""}}, "Cloudflare proxy detected"},
		{[]string{"port", "web.keel.example.com", "auto"}, []recordedCall{{"PATCH", inst + "/domains/web.keel.example.com", `{"target_port":null}`}}, "8080 (service port)"},
		{[]string{"port", "web.keel.example.com", "3001"}, []recordedCall{{"PATCH", inst + "/domains/web.keel.example.com", `{"target_port":3001}`}}, "port 3001"},
		{[]string{"rename", "web.keel.example.com", "shop"}, []recordedCall{{"PATCH", inst + "/domains/web.keel.example.com", `{"host":"shop.keel.example.com"}`}}, "https://shop.keel.example.com"},
		{[]string{"verify", "api.example.org"}, []recordedCall{{"POST", inst + "/domains/api.example.org/verify", `{}`}}, "proxy:         direct"},
		{[]string{"rm", "api.example.org", "-y"}, []recordedCall{{"DELETE", inst + "/domains/api.example.org", ""}}, "removed api.example.org"},
		{[]string{"tcp-proxy:enable"}, []recordedCall{{"GET", inst + "/networking", ""}, {"POST", inst + "/tcp-proxy", `{"container_port":5432}`}, {"GET", inst + "/tcp-proxy", ""}}, "example.com:20001 → port 5432"},
		{[]string{"tcp-proxy:port", "6543"}, []recordedCall{{"PATCH", inst + "/tcp-proxy", `{"container_port":6543}`}, {"GET", inst + "/tcp-proxy", ""}}, ""},
		{[]string{"tcp-proxy:disable", "--yes"}, []recordedCall{{"DELETE", inst + "/tcp-proxy", ""}}, "disabled"},
	} {
		*calls = nil
		out := captureStdout(t, func() {
			if len(tc.args) > 0 && strings.HasPrefix(tc.args[0], "tcp-proxy:") {
				cmdTCPProxy(c, append([]string{strings.TrimPrefix(tc.args[0], "tcp-proxy:")}, tc.args[1:]...))
				return
			}
			cmdDomain(c, tc.args)
		})
		got := *calls
		if len(got) != len(tc.want) {
			t.Errorf("%v: calls %+v", tc.args, got)
			continue
		}
		for i := range got {
			if got[i].method != tc.want[i].method || got[i].path != tc.want[i].path || strings.TrimSpace(got[i].body) != tc.want[i].body {
				t.Errorf("%v: call %d = %+v, want %+v", tc.args, i, got[i], tc.want[i])
			}
		}
		if !strings.Contains(out, tc.out) {
			t.Errorf("%v: output %q missing %q", tc.args, out, tc.out)
		}
	}
}

func TestRenameHostAndTargetPort(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"web.keel.example.com", "shop"}:                   "shop.keel.example.com",
		{"web.keel.example.com", "Shop.keel.example.com."}: "shop.keel.example.com",
		{"web", "x"}: "x",
	} {
		if got := renameHost(in[0], in[1]); got != want {
			t.Errorf("%v: %s", in, got)
		}
	}
	if v, err := parseTargetPort("auto"); err != nil || v != nil {
		t.Errorf("auto: %v %v", v, err)
	}
	if v, err := parseTargetPort("8080"); err != nil || v != 8080 {
		t.Errorf("8080: %v %v", v, err)
	}
	for _, bad := range []string{"0", "70000", "x"} {
		if _, err := parseTargetPort(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFormatDomainStatus(t *testing.T) {
	var d domainDetail
	json.Unmarshal([]byte(`{"host":"api.example.org","kind":"custom","status":"pending_dns","proxy":"none","effective_port":8080,
		"verification":{"method":"","verified_at":null,"txt":{"name":"_keel-verify.api.example.org","value":"keel-verify=abc"}},
		"cert":{"status":"valid","not_after":"2027-01-02T00:00:00Z"},
		"dns":[{"type":"CNAME","name":"api.example.org","value":"main.edge.keel.example.com","status":"pending","current":[]},
		       {"type":"AAAA","name":"api.example.org","value":"2001:db8::1","status":"mismatch","current":["2001:db8::9"],"alternative":true}],
		"warnings":["IPv4 클라이언트는 Cloudflare 프록시가 필요합니다"]}`), &d)
	out := formatDomainStatus(&d)
	for _, want := range []string{"status:        pending_dns", "port:          8080 (service port)", "no DNS records",
		"not verified (DNS pointing here, or TXT _keel-verify.api.example.org = keel-verify=abc)", "certificate:   valid, expires 2027-01-0",
		"main.edge.keel.example.com", "AAAA*", "mismatch", "2001:db8::9", "alternative to the CNAME", "warning: IPv4"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	list := formatDomainList("web", "production", []domainDetail{d})
	if !strings.Contains(list, "pending_dns") || !strings.Contains(list, "HOST") {
		t.Errorf("list:\n%s", list)
	}
	if !strings.Contains(formatDomainList("web", "production", nil), "no domains") {
		t.Error("empty list")
	}
}

func TestFormatNetworkingAndTCPProxy(t *testing.T) {
	var v networkingOverview
	json.Unmarshal([]byte(`{"detected_ports":[{"port":3000,"source":"listening"},{"port":9229,"source":"loopback"}],
		"port_detection":{"source":"proc","node":"node_local","containers":1},"effective_port":3000,
		"domains":[{"host":"api.example.org","kind":"custom","status":"proxied","proxy":"cloudflare","effective_port":3000}],
		"tcp_proxy":{"enabled":false,"host":"","available":false,"reason":"이 노드는 공개 IP가 없어 TCP 프록시를 열 수 없습니다"},
		"private":{"dns_name":"api","fqdn":"api.production.keel.internal","aliases":["web","api"],"ipv4":["172.20.0.5"],"ipv6":["fd00::5"],"mesh_ports":[5432]},
		"outbound":{"ipv6_enabled":false,"node_ipv6_egress":true,"enforce_supported":true,"node":"node_local","static_ips":{"ipv4":"192.0.2.10","ipv6":null}}}`), &v)
	out := formatNetworking("web", "production", &v)
	for _, want := range []string{"effective port: 3000", "3000 (listening), 9229 (loopback)", "port 9229 listens on localhost only",
		"https://api.example.org  (custom · proxied · port 3000 (service port) · Cloudflare proxy detected)", "unavailable: 이 노드는",
		"name:    api.production.keel.internal", "ips:     fd00::5, 172.20.0.5", "ports open to other nodes: 5432", "outbound IPv6: off",
		"node node_local IPv6 egress: yes", "static IPs: IPv6 -, IPv4 192.0.2.10"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	pub, cp := 20000, 5432
	if got := formatTCPProxy("db", "production", &tcpProxyStatus{Enabled: true, Host: "example.com", PublicPort: &pub, ContainerPort: &cp, Available: true}); !strings.Contains(got, "example.com:20000 → port 5432") {
		t.Errorf("enabled: %s", got)
	}
	if got := formatTCPProxy("db", "production", &tcpProxyStatus{Available: true}); !strings.Contains(got, "off (enable:") {
		t.Errorf("off: %s", got)
	}
}
