package agent

import (
	"net/http/httptest"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/proxy"
)

func TestAgentTLSAskFollowsAppliedRoutes(t *testing.T) {
	h := &Handler{Docker: &fakeDocker{}, Proxy: &fakeProxy{}}
	ask := func(domain string) int {
		r := httptest.NewRequest("GET", proxy.TLSAskPath+"?domain="+domain, nil)
		r.Host = proxy.TLSAskHost
		w := httptest.NewRecorder()
		h.WakeHandler().ServeHTTP(w, r)
		return w.Code
	}
	if ask("a.example.com") != 404 {
		t.Fatal("allowed before any routes were applied")
	}
	routes := []proxy.Route{
		{Hosts: []string{"*.example.com"}, Upstreams: []string{"keel-web-abcdefgh-1:80"}},
		{Hosts: []string{"exact.other.org"}, Upstreams: []string{"keel-web-abcdefgh-1:80"}},
	}
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: routes}); err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]int{"a.example.com": 200, "exact.other.org": 200, "x.y.example.com": 404, "other.org": 404} {
		if got := ask(domain); got != want {
			t.Errorf("%s: %d want %d", domain, got, want)
		}
	}
	// 라우트가 빠지면 더 이상 허가하지 않는다
	if _, err := handle(t, h, agentprotocol.OpProxyApply, agentprotocol.ProxyRequest{Routes: []proxy.Route{}}); err != nil {
		t.Fatal(err)
	}
	if ask("a.example.com") != 404 {
		t.Error("still allowed after the route was removed")
	}
}
