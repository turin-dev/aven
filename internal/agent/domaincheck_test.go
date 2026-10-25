package agent

import (
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/proxy"
)

func TestValidateRoutesCheckToken(t *testing.T) {
	r := func(tok string) []proxy.Route {
		return []proxy.Route{{Hosts: []string{"a.example.com"}, Upstreams: []string{"keel-web-abcdefgh-1:80"}, CheckToken: tok, Unverified: true}}
	}
	for _, ok := range []string{"", "0123456789abcdef0123456789abcdef"} {
		if err := ValidateRoutes(r(ok)); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"0123456789ABCDEF0123456789ABCDEF",  // 대문자
		"0123456789abcdef0123456789abcde",   // 31자
		"0123456789abcdef0123456789abcdef0", // 33자
		"0123456789abcdef0123456789abcdeg",  // hex 아님
		"0123456789abcdef\\n123456789abcdef",
		`0123456789abcdef"}]}],"x":"012345`,
	} {
		if err := ValidateRoutes(r(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// proxy.apply 전체 경로에서도 거부된다(Caddy에 넣기 전)
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: r("nothex")}); err == nil || !strings.Contains(err.Error(), "check token") {
		t.Fatalf("apply with bad token: %v", err)
	}
}

func TestAgentTLSAskHoldsUnverifiedRoutes(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}}
	ask := func(domain string) int {
		r := httptest.NewRequest("GET", proxy.TLSAskPath+"?domain="+domain, nil)
		r.Host = proxy.TLSAskHost
		w := httptest.NewRecorder()
		h.WakeHandler().ServeHTTP(w, r)
		return w.Code
	}
	up := []string{"keel-web-abcdefgh-1:80"}
	routes := []proxy.Route{
		{Hosts: []string{"legacy.example.com"}, Upstreams: up},
		{Hosts: []string{"new.example.com"}, Upstreams: up, Unverified: true},
		// 경로 라우트 하나만 Unverified여도 호스트 전체가 보류된다(순서와 무관)
		{Hosts: []string{"mixed.example.com"}, Upstreams: up, Unverified: true, Path: "/x"},
		{Hosts: []string{"mixed.example.com"}, Upstreams: up},
		{Hosts: []string{"*.wild.example.com"}, Upstreams: up, Unverified: true},
		{Hosts: []string{"*.old.example.com"}, Upstreams: up},
		// 정확한 호스트가 확인 전이면 와일드카드가 확인됐어도 보류
		{Hosts: []string{"x.old.example.com"}, Upstreams: up, Unverified: true},
	}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: routes}); err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]int{
		"legacy.example.com": 200, "new.example.com": 404, "mixed.example.com": 404,
		"a.wild.example.com": 404, "a.old.example.com": 200, "x.old.example.com": 404, "other.example.com": 404,
	} {
		if got := ask(domain); got != want {
			t.Errorf("%s: %d want %d", domain, got, want)
		}
	}
	// 확인되면 서버가 Unverified 없이 다시 보낸다
	for i := range routes {
		routes[i].Unverified = false
	}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: routes}); err != nil {
		t.Fatal(err)
	}
	if ask("new.example.com") != 200 || ask("a.wild.example.com") != 200 {
		t.Error("verified hosts still refused")
	}
}

func TestPortFree(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}, TCPBind: "127.0.0.1"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := ln.Addr().(*net.TCPAddr).Port
	res, err := handle(t, h, agentprotocol.OpPortFree, agentprotocol.PortFreeRequest{Port: busy})
	if err != nil || res.(agentprotocol.PortFreeResult).Free {
		t.Fatalf("busy port: %v %v", res, err)
	}
	ln.Close()
	if busy >= 1024 {
		if res, err := handle(t, h, agentprotocol.OpPortFree, agentprotocol.PortFreeRequest{Port: busy}); err != nil || !res.(agentprotocol.PortFreeResult).Free {
			t.Fatalf("freed port %d: %v %v", busy, res, err)
		}
	}
	for _, bad := range []int{0, 22, 1023, 65536} {
		if _, err := handle(t, h, agentprotocol.OpPortFree, agentprotocol.PortFreeRequest{Port: bad}); err == nil {
			t.Errorf("port %s accepted", strconv.Itoa(bad))
		}
	}
	// 모르는 필드는 거부(엄격한 디코딩)
	if _, err := handle(t, h, agentprotocol.OpPortFree, map[string]any{"port": 20000, "bind": "0.0.0.0"}); err == nil {
		t.Error("unknown field accepted")
	}
}
