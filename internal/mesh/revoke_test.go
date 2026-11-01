package mesh

import (
	"io"
	"net"
	"testing"
	"time"
)

// echoOnce는 터널로 바이트를 보내고 그대로 돌아오는지 본다.
func echoOnce(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	defer c.SetDeadline(time.Time{})
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q %v", buf, err)
	}
}

// waitClosed는 상대가 연결을 닫을 때까지 기다린다(읽기가 EOF/오류로 끝나야 한다).
func waitClosed(t *testing.T, c net.Conn, what string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err := c.Read(make([]byte, 1))
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("%s: tunnel is still open after the config change", what)
	}
	if err == nil {
		t.Fatalf("%s: tunnel still delivers data", what)
	}
}

// 이미 열린 터널도 설정이 바뀌면(노드 삭제, 인증서 폐기, export에서 빠짐) 닫힌다. 관계없는 변경에는 남는다.
func TestInboundTunnelsClosedWhenNoLongerAllowed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config func(ga *Gateway, a ident, port int) Config
		keep   bool
	}{
		{"unchanged config", func(ga *Gateway, a ident, port int) Config {
			return Config{Peers: []Peer{peerOf(ga, a)}, Exports: []Export{{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{port}, Peers: []string{nodA}}}}
		}, true},
		{"node removed", func(*Gateway, ident, int) Config { return Config{} }, false},
		{"certificate revoked", func(ga *Gateway, a ident, port int) Config {
			p := peerOf(ga, a)
			p.Serials = []string{"abcdef"}
			return Config{Peers: []Peer{p}, Exports: []Export{{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{port}, Peers: []string{nodA}}}}
		}, false},
		{"peer left the environment", func(ga *Gateway, a ident, port int) Config {
			return Config{Peers: []Peer{peerOf(ga, a)}, Exports: []Export{{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{port}, Peers: []string{"nod_ccccccccccccc"}}}}
		}, false},
		{"port no longer private", func(ga *Gateway, a ident, port int) Config {
			return Config{Peers: []Peer{peerOf(ga, a)}, Exports: []Export{{EnvID: envA, ServiceID: svcA, Service: "api", Ports: []int{port + 1}, Peers: []string{nodA}}}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := echoServer(t)
			port := int(up.Port())
			ga, gb, a, _, _ := pair(t, port)
			r, c := dialTunnel(t, gb, a, Header{V: 1, Op: "dial", Env: envA, Service: "api", Port: port})
			defer c.Close()
			if !r.OK {
				t.Fatalf("tunnel: %+v", r)
			}
			echoOnce(t, c)
			if n := gb.ActiveTunnels(); n != 1 {
				t.Fatalf("active tunnels = %d, want 1", n)
			}
			gb.Apply(tc.config(ga, a, port), nil, nil)
			if tc.keep {
				echoOnce(t, c)
				return
			}
			waitClosed(t, c, tc.name)
			deadline := time.Now().Add(3 * time.Second)
			for gb.ActiveTunnels() != 0 || gb.Tunnels(nodA) != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("tunnel bookkeeping not released: active=%d per-peer=%d", gb.ActiveTunnels(), gb.Tunnels(nodA))
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// 이 노드가 연 터널(import)도 상대 노드가 빠지거나 import가 사라지면 닫힌다.
func TestOutboundTunnelsClosedWhenPeerOrImportRemoved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config func(gb *Gateway, b ident, port int) (Config, map[string]EnvNet)
	}{
		{"peer removed", func(gb *Gateway, b ident, port int) (Config, map[string]EnvNet) { return Config{}, nil }},
		{"peer certificate revoked", func(gb *Gateway, b ident, port int) (Config, map[string]EnvNet) {
			p := peerOf(gb, b)
			p.Serials = []string{"abcdef"}
			return Config{Peers: []Peer{p}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{port}, Node: nodB}}}, map[string]EnvNet{envA: loopNet}
		}},
		{"import removed", func(gb *Gateway, b ident, port int) (Config, map[string]EnvNet) {
			return Config{Peers: []Peer{peerOf(gb, b)}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := echoServer(t)
			port := int(up.Port())
			ga, gb, _, b, _ := pair(t, port)
			binds, err := ga.Apply(Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{port}, Node: nodB}}},
				map[string]EnvNet{envA: loopNet}, nil)
			if err != nil || len(binds) != 1 {
				t.Fatalf("apply: %v %v", binds, err)
			}
			c, err := net.Dial("tcp", binds[0].Local.String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			echoOnce(t, c)
			cfg, nets := tc.config(gb, b, port)
			ga.Apply(cfg, nets, nil)
			waitClosed(t, c, tc.name)
		})
	}
}
