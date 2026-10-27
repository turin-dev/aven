package proxy

import "testing"

func TestValidExternalUpstream(t *testing.T) {
	for _, ok := range []string{"grafana-1:3000", "compose-x-front-1:80", "10.0.20.254:80", "100.64.0.3:3000", "[fd7a::1]:443"} {
		if !ValidExternalUpstream(ok) {
			t.Errorf("%s should be allowed", ok)
		}
	}
	for _, bad := range []string{"localhost:2019", "127.0.0.1:2019", "[::1]:80", "0.0.0.0:80", "169.254.169.254:80", "aven-proxy:2019", "keel-proxy:2019",
		"192.0.2.10:80", "grafana", "grafana:0", "grafana:70000", "a b:80", "x.localhost:80"} {
		if ValidExternalUpstream(bad) {
			t.Errorf("%s should be rejected", bad)
		}
	}
}
