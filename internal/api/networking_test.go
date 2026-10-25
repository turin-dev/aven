package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// fakeNet은 networkingBackend 테스트 대역이다.
type fakeNet struct {
	mu        sync.Mutex
	listening *node.ListeningPorts
	lerr      error
	expose    []int
	state     *node.ContainerState
	delay     time.Duration
	calls     []string
}

func (f *fakeNet) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeNet) ListeningPorts(ctx context.Context, nodeID, instanceID string) (*node.ListeningPorts, error) {
	f.rec("ports:" + nodeID + ":" + instanceID)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.listening, f.lerr
}
func (f *fakeNet) ImageExposedPortsOn(_ context.Context, nodeID, image string) []int {
	f.rec("expose:" + nodeID + ":" + image)
	return f.expose
}
func (f *fakeNet) InspectOn(_ context.Context, nodeID, name string) (*node.ContainerState, error) {
	f.rec("inspect:" + nodeID + ":" + name)
	if f.state == nil {
		return nil, node.ErrNoContainer
	}
	return f.state, nil
}

func TestMergeDetectedPorts(t *testing.T) {
	got := mergeDetectedPorts(&node.ListeningPorts{Ports: []int{8080, 3000}, Loopback: []int{9229, 5000}},
		[]int{3000, 4000, 5000, 0, 70000}, []int{4000, 6000, 8080})
	want := []detectedPort{
		{3000, "listening"}, {8080, "listening"}, // listening이 EXPOSE/PORT보다 앞선다
		{4000, "expose"}, // EXPOSE가 PORT 변수보다 앞선다
		{6000, "env"},
		{5000, "loopback"}, {9229, "loopback"}, // 실제 소켓(loopback 전용)이 EXPOSE보다 정확하다. 경고라 맨 뒤에
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if out := mergeDetectedPorts(nil, nil, nil); out == nil || len(out) != 0 {
		t.Fatalf("empty must be [] not null: %#v", out)
	}
}

type netFixture struct {
	*ghFixture
	fake *fakeNet
	in   *store.Instance
	pv   map[string]string
}

func newNetFixture(t *testing.T, nodeID string) *netFixture {
	f := newGHFixture(t)
	f.mgr.Cfg.PublicIPv4, f.mgr.Cfg.PublicIPv6 = "192.0.2.10", "2001:db8:6b:1943::2"
	fake := &fakeNet{}
	f.srv.netImpl = fake
	in, err := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	img := "ghcr.io/acme/web:1"
	d := &store.Deployment{ServiceID: f.web.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
		NodeID: nodeID, Image: &img, Containers: []string{"keel-web-a", "keel-web-b"},
		VarsRaw: map[string]string{}, Env: map[string]string{"PORT": "8080"}}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetInstanceStatus(f.ctx, in.ID, store.InstOnline, &d.ID); err != nil {
		t.Fatal(err)
	}
	in, _ = f.st.GetInstance(f.ctx, in.ID)
	return &netFixture{ghFixture: f, fake: fake, in: in, pv: map[string]string{"sid": f.web.ID, "eid": f.prod.ID}}
}

func (f *netFixture) get() (int, networkingView) {
	f.t.Helper()
	r := httptest.NewRequest("GET", "/api/services/x/instances/y/networking", nil)
	for k, v := range f.pv {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	f.srv.getNetworking(w, r)
	var v networkingView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		f.t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	return w.Code, v
}

func TestNetworkingHandler(t *testing.T) {
	f := newNetFixture(t, store.LocalNodeID)
	f.fake.listening = &node.ListeningPorts{Ports: []int{3000}, Loopback: []int{9229}, Containers: 2}
	f.fake.expose = []int{8080}
	env := deploy.EnvNetwork(f.prod.ID)
	f.fake.state = &node.ContainerState{Running: true, IPs: map[string][]string{env: {"fd00:1::5", "10.20.0.5"}, "aven": {"172.18.0.9"}}}
	f.in.Domains = []store.Domain{{Host: "web.example.com", Kind: "generated"}}
	f.in.TCPProxy = &store.TCPProxy{ContainerPort: 5432, PublicPort: 20001}
	if err := f.st.UpdateInstanceConfig(f.ctx, f.in); err != nil {
		t.Fatal(err)
	}
	code, v := f.get()
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if !reflect.DeepEqual(v.DetectedPorts, []detectedPort{{3000, "listening"}, {8080, "expose"}, {9229, "loopback"}}) {
		t.Errorf("detected: %+v", v.DetectedPorts)
	}
	if v.PortDetection.Source != "proc" || v.PortDetection.Containers != 2 || v.PortDetection.Node != store.LocalNodeID {
		t.Errorf("detection: %+v", v.PortDetection)
	}
	if v.EffectivePort != 8080 {
		t.Errorf("effective %d", v.EffectivePort)
	}
	if len(v.Domains) != 1 || v.Domains[0].Host != "web.example.com" || v.Domains[0].Status != "active" {
		t.Errorf("domains: %+v", v.Domains)
	}
	tp := v.TCPProxy
	if !tp.Enabled || !tp.Available || tp.Host != "example.com" || *tp.PublicPort != 20001 || *tp.ContainerPort != 5432 || tp.Reason != "" {
		t.Errorf("tcp: %+v", tp)
	}
	pr := v.Private
	fqdn := f.web.Name + "." + f.prod.Name + ".keel.internal"
	if pr.DNSName != f.web.Name || pr.FQDN != fqdn || !reflect.DeepEqual(pr.Aliases, []string{f.web.Name, fqdn}) ||
		!reflect.DeepEqual(pr.IPv4, []string{"10.20.0.5"}) || !reflect.DeepEqual(pr.IPv6, []string{"fd00:1::5"}) || !reflect.DeepEqual(pr.MeshPorts, []int{8080}) {
		t.Errorf("private: %+v", pr)
	}
	ob := v.Outbound
	if !ob.IPv6Enabled || ob.NodeIPv6Egress != nil || *ob.StaticIPs.IPv4 != "192.0.2.10" || *ob.StaticIPs.IPv6 != "2001:db8:6b:1943::2" {
		t.Errorf("outbound: %+v", ob)
	}
	// 첫 replica만 inspect하고, 포트는 노드 단위로 한 번 묻는다(agent가 replica를 합친다).
	want := map[string]bool{"ports:" + store.LocalNodeID + ":" + f.in.ID: true, "expose:" + store.LocalNodeID + ":ghcr.io/acme/web:1": true,
		"inspect:" + store.LocalNodeID + ":keel-web-a": true}
	if len(f.fake.calls) != 3 {
		t.Errorf("calls %v", f.fake.calls)
	}
	for _, c := range f.fake.calls {
		if !want[c] {
			t.Errorf("unexpected call %s", c)
		}
	}
	// JSON 키 모양(웹이 기대하는 이름).
	r := httptest.NewRequest("GET", "/", nil)
	for k, val := range f.pv {
		r.SetPathValue(k, val)
	}
	w := httptest.NewRecorder()
	f.srv.getNetworking(w, r)
	var raw map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"detected_ports", "effective_port", "domains", "tcp_proxy", "private", "outbound", "port_detection"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
}

// 옛 agent(ports.listening을 모름)면 감지 결과만 비고 나머지는 그대로 돌려준다.
func TestNetworkingOldAgentFallback(t *testing.T) {
	f := newNetFixture(t, store.LocalNodeID)
	f.fake.lerr = deploy.ErrPortDetectionUnsupported
	f.fake.expose = []int{8080}
	code, v := f.get()
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if v.PortDetection.Source != "unavailable" || v.PortDetection.Reason != deploy.ErrPortDetectionUnsupported.Error() {
		t.Errorf("detection: %+v", v.PortDetection)
	}
	if !reflect.DeepEqual(v.DetectedPorts, []detectedPort{{8080, "expose"}}) {
		t.Errorf("detected: %+v", v.DetectedPorts)
	}
	if v.EffectivePort != 8080 || v.Private.FQDN == "" {
		t.Errorf("rest of view: %+v", v)
	}
}

// 느린 agent는 짧은 제한 시간 뒤 unavailable로 끝난다.
func TestNetworkingSlowAgent(t *testing.T) {
	f := newNetFixture(t, store.LocalNodeID)
	f.fake.delay = time.Minute
	start := time.Now()
	code, v := f.get()
	if code != 200 || time.Since(start) > networkingTimeout+2*time.Second {
		t.Fatalf("code %d after %s", code, time.Since(start))
	}
	if v.PortDetection.Source != "unavailable" || v.PortDetection.Reason == "" {
		t.Errorf("detection: %+v", v.PortDetection)
	}
}

// 공인 IP가 없는 노드(Cloudflare 터널 노드)는 TCP 프록시를 쓸 수 없고 고정 IP도 없다.
func TestNetworkingTunnelNode(t *testing.T) {
	f := newNetFixture(t, store.LocalNodeID)
	if err := f.st.CreateJoinToken(f.ctx, "h", "remote", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := f.st.ConsumeJoinToken(f.ctx, "h", "c")
	if err != nil {
		t.Fatal(err)
	}
	priv := "100.64.0.1"
	if err := f.st.SetNodeInfo(f.ctx, n.ID, store.NodeAddress{Private: &priv}, store.NodeInfo{}); err != nil {
		t.Fatal(err)
	}
	d, _ := f.st.GetDeployment(f.ctx, *f.in.CurrentDeploymentID)
	img := "x:1"
	d2 := &store.Deployment{ServiceID: d.ServiceID, EnvironmentID: d.EnvironmentID, InstanceID: d.InstanceID, Status: store.DepLive,
		Trigger: "manual", NodeID: n.ID, Image: &img, Containers: []string{"keel-web-k"}}
	if err := f.st.CreateDeployment(f.ctx, d2); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(f.ctx, f.in.ID, store.InstOnline, &d2.ID)
	f.fake.listening = &node.ListeningPorts{Ports: []int{8080}, Loopback: []int{}, Containers: 1}
	code, v := f.get()
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if v.TCPProxy.Available || v.TCPProxy.Host != "" || v.TCPProxy.Reason != tcpNoPublicIPReason || v.TCPProxy.Enabled {
		t.Errorf("tcp: %+v", v.TCPProxy)
	}
	if v.Outbound.StaticIPs.IPv4 != nil || v.Outbound.StaticIPs.IPv6 != nil {
		t.Errorf("static ips: %+v", v.Outbound.StaticIPs)
	}
	if v.PortDetection.Node != n.ID {
		t.Errorf("node: %+v", v.PortDetection)
	}
}

// 배포가 없으면 노드에 묻지 않고 인스턴스 설정으로 포트를 정한다.
func TestNetworkingNoDeployment(t *testing.T) {
	f := newGHFixture(t)
	fake := &fakeNet{}
	f.srv.netImpl = fake
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	p := 4321
	in.Port = &p
	f.st.UpdateInstanceConfig(f.ctx, in)
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("sid", f.web.ID)
	r.SetPathValue("eid", f.prod.ID)
	w := httptest.NewRecorder()
	f.srv.getNetworking(w, r)
	var v networkingView
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || v.EffectivePort != 4321 || v.PortDetection.Source != "unavailable" || len(fake.calls) != 0 ||
		v.DetectedPorts == nil || v.Domains == nil || !reflect.DeepEqual(v.Private.MeshPorts, []int{4321}) {
		t.Fatalf("%d %+v calls=%v", w.Code, v, fake.calls)
	}
}

func TestNetworkingRouteIsViewer(t *testing.T) {
	f := newGHFixture(t)
	f.srv.Handler()
	for _, rt := range f.srv.Routes() {
		if rt.Pattern == "GET /api/services/{sid}/instances/{eid}/networking" {
			if rt.Access.Kind != accProject || rt.Access.Level != lvViewer {
				t.Fatalf("access %+v", rt.Access)
			}
			return
		}
	}
	t.Fatal("route not registered")
}

// 사설 DNS 이름을 정했으면 dns_name/fqdn/aliases가 그 이름을 따른다(privateDNS).
func TestNetworkingCustomPrivateName(t *testing.T) {
	f := newNetFixture(t, store.LocalNodeID)
	f.in.Spec.PrivateDNSName = "api"
	if err := f.st.UpdateInstanceConfig(f.ctx, f.in); err != nil {
		t.Fatal(err)
	}
	_, v := f.get()
	name, fqdn, aliases := privateDNS(f.in, f.web, f.prod)
	if name != "api" || v.Private.DNSName != name || v.Private.FQDN != fqdn || !reflect.DeepEqual(v.Private.Aliases, aliases) {
		t.Fatalf("%+v", v.Private)
	}
}
