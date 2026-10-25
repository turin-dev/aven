package api

import (
	"context"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// egressNet은 networkingBackend와 egressBackend를 함께 흉내 낸다(실제 노드나 ip6tables에 닿지 않는다).
type egressNet struct {
	*fakeNet
	st    *node.EgressStatus
	err   error
	syncs int
	asked []string
}

func (e *egressNet) EgressStatus(_ context.Context, nodeID, envID string) (*node.EgressStatus, error) {
	e.asked = append(e.asked, nodeID+"|"+envID)
	return e.st, e.err
}
func (e *egressNet) EgressNodes(context.Context, *store.Instance) []string {
	return []string{store.LocalNodeID}
}
func (e *egressNet) SyncEgress(context.Context) error { e.syncs++; return nil }

func newEgressFixture(t *testing.T) (*netFixture, *egressNet) {
	f := newNetFixture(t, store.LocalNodeID)
	yes := true
	e := &egressNet{fakeNet: f.fake, st: &node.EgressStatus{IPv6: &yes, Enforce: true}}
	f.srv.netImpl = e
	return f, e
}

func (f *netFixture) patchOutbound(body string) (int, map[string]any) {
	f.t.Helper()
	return f.call(f.srv.patchInstance, "PATCH", "/", body, f.pv)
}

func (f *netFixture) outbound() *bool {
	f.t.Helper()
	in, err := f.st.GetInstance(f.ctx, f.in.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return in.OutboundIPv6
}

func TestNetworkingOutboundStatus(t *testing.T) {
	f, e := newEgressFixture(t)
	_, v := f.get()
	ob := v.Outbound
	if !ob.IPv6Enabled || ob.NodeIPv6Egress == nil || !*ob.NodeIPv6Egress || ob.EnforceSupported == nil || !*ob.EnforceSupported ||
		ob.Node != store.LocalNodeID || ob.StaticIPs.IPv6 == nil {
		t.Fatalf("outbound %+v", ob)
	}
	if len(e.asked) != 1 || e.asked[0] != store.LocalNodeID+"|"+f.prod.ID {
		t.Fatalf("asked %v", e.asked)
	}
	// 옛 agent: 상태는 null(모름)이고 이유가 붙는다. 나머지 화면은 그대로다.
	e.st, e.err = nil, deploy.ErrEgressAgentUnsupported
	_, v = f.get()
	ob = v.Outbound
	if ob.NodeIPv6Egress != nil || ob.EnforceSupported != nil || ob.EgressReason != deploy.ErrEgressAgentUnsupported.Error() || v.Private.FQDN == "" {
		t.Fatalf("old agent outbound %+v", ob)
	}
	// netImpl이 egress를 모르면(다른 테스트) 노드에 묻지 않고 null이다.
	f.srv.netImpl = f.fake
	_, v = f.get()
	if v.Outbound.NodeIPv6Egress != nil || !v.Outbound.IPv6Enabled {
		t.Fatalf("no egress backend %+v", v.Outbound)
	}
}

// outbound_ipv6은 재배포 없이 바로 반영된다: 저장, 규칙 동기화, pending_changes 제외.
func TestPatchOutboundIPv6Immediate(t *testing.T) {
	f, e := newEgressFixture(t)
	code, out := f.patchOutbound(`{"outbound_ipv6": false}`)
	if code != 200 {
		t.Fatalf("code %d %v", code, out)
	}
	if ob := f.outbound(); ob == nil || *ob {
		t.Fatalf("stored %v", ob)
	}
	if e.syncs != 1 {
		t.Fatalf("syncs %d", e.syncs)
	}
	if pc, _ := out["pending_changes"].([]any); len(pc) > 0 {
		for _, k := range pc {
			if k == "outbound_ipv6" {
				t.Fatalf("outbound_ipv6 must not be staged: %v", pc)
			}
		}
	}
	if len(f.queued()) != 0 {
		t.Fatalf("toggle must not deploy: %v", f.queued())
	}
	// 같은 값은 아무것도 하지 않는다
	if code, _ := f.patchOutbound(`{"outbound_ipv6": false}`); code != 200 || e.syncs != 1 {
		t.Fatalf("noop: %d syncs %d", code, e.syncs)
	}
	// null(기본값)이나 true로 다시 켜면 nil로 저장한다. 켜는 것은 노드 지원과 상관없다.
	e.st = &node.EgressStatus{Enforce: false, EnforceReason: "x"}
	if code, _ := f.patchOutbound(`{"outbound_ipv6": null}`); code != 200 || f.outbound() != nil || e.syncs != 2 {
		t.Fatalf("enable: %d %v syncs %d", code, f.outbound(), e.syncs)
	}
	if code, _ := f.patchOutbound(`{"outbound_ipv6": "no"}`); code != 400 {
		t.Fatalf("bad type: %d", code)
	}
	// 다른 설정과 함께 보내도 둘 다 반영된다
	e.st = &node.EgressStatus{Enforce: true}
	if code, out := f.patchOutbound(`{"outbound_ipv6": false, "healthcheck_path": "/hz"}`); code != 200 {
		t.Fatalf("combined: %d %v", code, out)
	}
	in, _ := f.st.GetInstance(f.ctx, f.in.ID)
	if in.OutboundIPv6 == nil || *in.OutboundIPv6 || in.HealthcheckPath != "/hz" {
		t.Fatalf("combined stored %+v %q", in.OutboundIPv6, in.HealthcheckPath)
	}
}

func TestPendingChangesSkipsOutboundIPv6(t *testing.T) {
	off := false
	in := &store.Instance{}
	in.Spec.Defaults()
	d := &store.Deployment{ReqSpec: in.Spec}
	in.OutboundIPv6 = &off
	if pc := PendingChanges(in, d); len(pc) != 0 {
		t.Fatalf("pending %v", pc)
	}
}

// 차단을 못 하는 노드(ip6tables/DOCKER-USER 없음, 옛 agent)에서는 끄기를 409로 거부하고 저장하지 않는다.
func TestPatchOutboundIPv6Unsupported(t *testing.T) {
	f, e := newEgressFixture(t)
	e.st = &node.EgressStatus{Enforce: false, EnforceReason: "이 노드에 Docker의 ip6tables DOCKER-USER 체인이 없습니다"}
	code, out := f.patchOutbound(`{"outbound_ipv6": false}`)
	msg, _ := out["error"].(string)
	if code != 409 || !strings.Contains(msg, "아웃바운드 IPv6를 끌 수 없습니다") || !strings.Contains(msg, "DOCKER-USER") {
		t.Fatalf("code %d %v", code, out)
	}
	if f.outbound() != nil || e.syncs != 0 {
		t.Fatalf("must not store or sync: %v %d", f.outbound(), e.syncs)
	}
	e.st, e.err = nil, deploy.ErrEgressAgentUnsupported
	code, out = f.patchOutbound(`{"outbound_ipv6": false, "healthcheck_path": "/nope"}`)
	msg, _ = out["error"].(string)
	if code != 409 || !strings.Contains(msg, "aven-agent") {
		t.Fatalf("old agent: %d %v", code, out)
	}
	if in, _ := f.st.GetInstance(f.ctx, f.in.ID); in.OutboundIPv6 != nil || in.HealthcheckPath == "/nope" {
		t.Fatal("rejected patch must not save other fields")
	}
}
