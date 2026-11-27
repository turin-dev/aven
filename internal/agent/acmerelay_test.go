package agent

import (
	"testing"

	"github.com/turin-dev/aven/internal/proxy"
)

func TestValidateRoutesACMERelay(t *testing.T) {
	good := []proxy.Route{{Hosts: []string{"web.example.com"}, Upstreams: []string{"keel-web-1:80"},
		ACMERelay: &proxy.ACMERelay{Self: "nod_a1", Next: "node_local", NextAddrs: []string{"203.0.113.1:80"}}}}
	if err := ValidateRoutesWith(good, nil); err != nil {
		t.Fatal(err)
	}
	bad := []proxy.Route{{Hosts: []string{"web.example.com"}, Upstreams: []string{"keel-web-1:80"},
		ACMERelay: &proxy.ACMERelay{Self: "nod_a1", Next: "node_local", NextAddrs: []string{"192.168.1.1:80"}}}}
	if err := ValidateRoutesWith(bad, nil); err == nil {
		t.Fatal("relay to a private address accepted")
	}
}
