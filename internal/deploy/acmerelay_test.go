package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func TestDecorateACMERelay(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Cfg.PublicIPv4, f.m.Cfg.PublicIPv6 = "203.0.113.1", "2001:db8::1"
	v6 := "2001:db8::7"
	if err := f.st.SetNodeInfo(ctx, f.rid, store.NodeAddress{IPv6: &v6}, store.NodeInfo{}); err != nil {
		t.Fatal(err)
	}
	routes := map[string][]proxy.Route{
		store.LocalNodeID: {{Hosts: []string{"web.example.com"}}, {Hosts: []string{"web.example.com"}, Path: "/api/*"}, {Hosts: []string{"solo.example.com"}}},
		f.rid:             {{Hosts: []string{"WEB.example.com"}}},
		"nod_offline":     {{Hosts: []string{"solo.example.com"}}}, // 존에 없는(연결 안 된) 노드는 고리에 들어가지 않는다
	}
	f.m.decorateACMERelay(ctx, routes)
	for _, r := range routes[store.LocalNodeID][:2] {
		// 로컬 노드는 IPv4가 있지만 원격 노드는 IPv6뿐이다
		if r.ACMERelay == nil || r.ACMERelay.Self != store.LocalNodeID || r.ACMERelay.Next != f.rid || !slices.Equal(r.ACMERelay.NextAddrs, []string{"[2001:db8::7]:80"}) {
			t.Fatalf("local relay %+v", r.ACMERelay)
		}
	}
	if routes[store.LocalNodeID][2].ACMERelay != nil {
		t.Fatal("single-node host got a relay")
	}
	r := routes[f.rid][0].ACMERelay
	// 원격 노드는 IPv6만 있으므로 로컬 노드의 IPv6를 먼저 쓴다
	if r == nil || r.Self != f.rid || r.Next != store.LocalNodeID || !slices.Equal(r.NextAddrs, []string{"[2001:db8::1]:80", "203.0.113.1:80"}) {
		t.Fatalf("remote relay %+v", r)
	}
	if !proxy.ValidACMERelay(r) {
		t.Fatal("invalid relay produced")
	}
}

func TestDecorateACMERelaySkipsNoRelayNode(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Cfg.PublicIPv4 = "203.0.113.1"
	v4a := "198.51.100.7"
	if err := f.st.SetNodeInfo(ctx, f.rid, store.NodeAddress{IPv4: &v4a}, store.NodeInfo{}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateJoinToken(ctx, "h-r2", "edge", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n2, err := f.st.ConsumeJoinToken(ctx, "h-r2", "")
	if err != nil {
		t.Fatal(err)
	}
	v4b := "198.51.100.8"
	if err := f.st.SetNodeInfo(ctx, n2.ID, store.NodeAddress{IPv4: &v4b}, store.NodeInfo{}); err != nil {
		t.Fatal(err)
	}
	f.m.Agents.(*nodesHub).nodes[n2.ID] = newFakeNode("edge", f.log)
	f.m.proxyCompat.noRelay = map[string]time.Time{f.rid: time.Now()}
	routes := map[string][]proxy.Route{
		store.LocalNodeID: {{Hosts: []string{"web.example.com"}}},
		f.rid:             {{Hosts: []string{"web.example.com"}}},
		n2.ID:             {{Hosts: []string{"web.example.com"}}},
	}
	f.m.decorateACMERelay(ctx, routes)
	if routes[f.rid][0].ACMERelay != nil {
		t.Fatalf("old agent stayed in the ring: %+v", routes[f.rid][0].ACMERelay)
	}
	local, other := routes[store.LocalNodeID][0].ACMERelay, routes[n2.ID][0].ACMERelay
	if local == nil || local.Next != n2.ID || other == nil || other.Next != store.LocalNodeID {
		t.Fatalf("ring local=%+v other=%+v", local, other)
	}
}

func TestDecorateACMERelayPrivateAddress(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Cfg.PublicIPv4 = "10.0.0.1" // 사설 주소로는 넘기지 않는다
	v4 := "203.0.113.9"
	f.st.SetNodeInfo(ctx, f.rid, store.NodeAddress{IPv4: &v4}, store.NodeInfo{})
	routes := map[string][]proxy.Route{store.LocalNodeID: {{Hosts: []string{"web.example.com"}}}, f.rid: {{Hosts: []string{"web.example.com"}}}}
	f.m.decorateACMERelay(ctx, routes)
	if routes[f.rid][0].ACMERelay != nil {
		t.Fatalf("relay to a private address: %+v", routes[f.rid][0].ACMERelay)
	}
	if routes[store.LocalNodeID][0].ACMERelay == nil {
		t.Fatal("relay to the public remote node missing")
	}
}

// agent가 ACMERelay만 모르면 그 필드만 빼고 다시 보낸다(도메인 확인 필드는 남긴다).
type relayOldHub struct{ calls [][]proxy.Route }

func (h *relayOldHub) Online(string) bool { return true }
func (h *relayOldHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *relayOldHub) CallStream(_ context.Context, _, op string, req, _ any, _ func(json.RawMessage)) error {
	r := req.(agentprotocol.ProxyRequest)
	h.calls = append(h.calls, r.Routes)
	b, _ := json.Marshal(r)
	var probe struct {
		Routes []map[string]json.RawMessage `json:"routes"`
	}
	json.NewDecoder(bytes.NewReader(b)).Decode(&probe)
	for _, rt := range probe.Routes {
		if _, ok := rt["ACMERelay"]; ok {
			return &agentErr{`invalid payload: json: unknown field "ACMERelay"`}
		}
	}
	return nil
}

func TestApplyRemoteProxyNoRelayFallback(t *testing.T) {
	ctx := context.Background()
	hub := &relayOldHub{}
	m := &Manager{Agents: hub}
	relay := &proxy.ACMERelay{Self: "nod_a", Next: "nod_b", NextAddrs: []string{"203.0.113.2:80"}}
	routes := []proxy.Route{{Hosts: []string{"web.example.com"}, CheckToken: "0123456789abcdef0123456789abcdef", ACMERelay: relay}}
	if err := m.applyRemoteProxy(ctx, "nod_a", routes); err != nil || len(hub.calls) != 2 {
		t.Fatalf("fallback: %v %d", err, len(hub.calls))
	}
	if got := hub.calls[1][0]; got.ACMERelay != nil || got.CheckToken == "" {
		t.Fatalf("second call %+v", got)
	}
	if routes[0].ACMERelay == nil {
		t.Fatal("caller's routes modified")
	}
	hub.calls = nil
	if err := m.applyRemoteProxy(ctx, "nod_a", routes); err != nil || len(hub.calls) != 1 || hub.calls[0][0].ACMERelay != nil {
		t.Fatalf("remembered: %v %d", err, len(hub.calls))
	}
	if !m.DomainCheckSupported("nod_a") {
		t.Fatal("relay refusal must not disable domain checks")
	}
}
