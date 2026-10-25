package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"net/netip"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/node"
)

type meshDocker struct {
	fakeDocker
	nets map[string]*node.NetworkInfo
}

func (m *meshDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	return map[string]map[string]string{}, nil
}
func (m *meshDocker) NetworkInfo(_ context.Context, name string) (*node.NetworkInfo, error) {
	return m.nets[name], nil
}

func meshHandler(t *testing.T) (*Handler, *meshDocker) {
	t.Helper()
	ca, _ := agentpki.NewCA()
	pub, priv, _ := ed25519.GenerateKey(nil)
	is, err := ca.Issue("nod_aaaaaaaaaaaaa", pub)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := agentpki.TLSCertificate(is.CertPEM, priv)
	if err != nil {
		t.Fatal(err)
	}
	gw := &mesh.Gateway{NodeID: "nod_aaaaaaaaaaaaa", Roots: ca.Pool(), Cert: func() (*tls.Certificate, error) { return cert, nil }}
	t.Cleanup(gw.Close)
	d := &meshDocker{fakeDocker: fakeDocker{states: map[string]*node.ContainerState{}}, nets: map[string]*node.NetworkInfo{
		"keel-env_aaaaaaaaaaaaa": {Subnets: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Gateways: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
	}}
	return &Handler{Docker: d, Proxy: &fakeProxy{}, Mesh: gw, MeshPort: 7443, NodeID: "nod_aaaaaaaaaaaaa"}, d
}

func validMesh() agentprotocol.MeshEnsureRequest {
	return agentprotocol.MeshEnsureRequest{
		Peers:   []mesh.Peer{{NodeID: "nod_bbbbbbbbbbbbb", Address: "100.64.0.2:7443", Serials: []string{"ab"}}},
		Exports: []mesh.Export{{EnvID: "env_aaaaaaaaaaaaa", ServiceID: "svc_aaaaaaaaaaaaa", Service: "api", Ports: []int{8080}, Peers: []string{"nod_bbbbbbbbbbbbb"}}},
		Imports: []mesh.Import{{EnvID: "env_aaaaaaaaaaaaa", EnvName: "production", Service: "db", Ports: []int{5432}, Node: "nod_bbbbbbbbbbbbb"}},
	}
}

func TestMeshEnsureStrict(t *testing.T) {
	h, d := meshHandler(t)
	res, err := handle(t, h, agentprotocol.OpMeshEnsure, validMesh())
	if err != nil {
		t.Fatal(err)
	}
	st := res.(agentprotocol.MeshStatus)
	if !st.Enabled || len(st.Peers) != 1 || st.Peers[0].NodeID != "nod_bbbbbbbbbbbbb" {
		t.Fatalf("status: %+v", st)
	}
	if len(d.ran) != 1 {
		t.Fatalf("forwarder not created: %+v", d.ran)
	}
	s := d.ran[0]
	if len(s.Publish) != 0 || len(s.Mounts) != 0 || s.Image != node.TCPForwardImage || s.Labels[node.LabelRole] != node.RoleMeshProxy || s.Network != "keel-env_aaaaaaaaaaaaa" {
		t.Fatalf("forwarder spec: %+v", s)
	}
	// 알 수 없는 필드(listen 주소, publish, image 등)는 거부한다: 서버는 listen 주소나 컨테이너 스펙을 정하지 못한다.
	for _, raw := range []string{
		`{"peers":[],"exports":[],"imports":[],"listen":"0.0.0.0:22"}`,
		`{"peers":[],"exports":[],"imports":[{"env_id":"env_aaaaaaaaaaaaa","env_name":"p","service":"db","ports":[1],"node":"nod_bbbbbbbbbbbbb","image":"evil"}]}`,
		`{"peers":[],"exports":[],"imports":[]} {}`,
	} {
		if _, err := h.Handle(context.Background(), agentprotocol.OpMeshEnsure, []byte(raw), func(any) {}); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	bad := validMesh()
	bad.Peers[0].Address = "203.0.113.5:7443" // 공인 주소
	if _, err := handle(t, h, agentprotocol.OpMeshEnsure, bad); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("public peer address accepted: %v", err)
	}
	bad = validMesh()
	bad.Peers[0].NodeID = "nod_aaaaaaaaaaaaa" // 자기 자신
	if _, err := handle(t, h, agentprotocol.OpMeshEnsure, bad); err == nil {
		t.Fatal("self peer accepted")
	}
	// mesh가 꺼진 agent는 거부하고 ping에 mesh_port를 싣지 않는다.
	off := &Handler{Docker: d, Proxy: &fakeProxy{}, NodeID: "nod_aaaaaaaaaaaaa"}
	if _, err := handle(t, off, agentprotocol.OpMeshEnsure, validMesh()); err == nil {
		t.Fatal("disabled mesh accepted ensure")
	}
	if off.meshReady() || !h.meshReady() {
		t.Fatal("meshReady")
	}
}
