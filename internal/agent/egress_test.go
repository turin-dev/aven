package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// recIPT는 ip6tables 대역이다. DOCKER-USER는 비어 있고, 호출만 기록한다(진짜 ip6tables는 실행하지 않는다).
type recIPT struct {
	calls   []string
	noChain bool
}

func (r *recIPT) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	if len(args) == 2 && args[0] == "-S" && args[1] == "DOCKER-USER" {
		if r.noChain {
			return "", errors.New("no chain")
		}
		return "-N DOCKER-USER", nil
	}
	return "", nil
}

type egressAgentDocker struct {
	portsDocker
	states map[string]*node.ContainerState
}

func (d *egressAgentDocker) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		if s := d.states[n]; s != nil {
			out[n] = s
		}
	}
	return out, nil
}
func (d *egressAgentDocker) NetworkInfo(context.Context, string) (*node.NetworkInfo, error) {
	return nil, errors.New("no such network")
}

func TestEgressOps(t *testing.T) {
	ipt := &recIPT{}
	old := ip6tables
	ip6tables = func() node.IP6Tables { return ipt }
	t.Cleanup(func() { ip6tables = old })
	d := &egressAgentDocker{
		portsDocker: portsDocker{managed: map[string]map[string]string{"keel-web-1": {node.LabelInstance: "ins_abcd", node.LabelRole: node.RoleApp}}},
		states:      map[string]*node.ContainerState{"keel-web-1": {Running: true, IPs: map[string][]string{"keel-env_abcd": {"fd00:1::5"}}}},
	}
	h := &Handler{Docker: d}
	ctx := context.Background()

	res, err := h.Handle(ctx, agentprotocol.OpEgressEnsure, []byte(`{"blocked":["ins_abcd"]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(*node.EgressEnsureResult); !r.Supported || r.Added != 1 {
		t.Fatalf("%+v", r)
	}
	want := "-I DOCKER-USER -s fd00:1::5/128 ! -d fc00::/7"
	if !strings.HasPrefix(ipt.calls[len(ipt.calls)-1], want) || !strings.Contains(ipt.calls[len(ipt.calls)-1], "keel:egress6:ins_abcd") {
		t.Fatalf("calls %v", ipt.calls)
	}

	st, err := h.Handle(ctx, agentprotocol.OpEgressStatus, []byte(`{"env_id":"env_abcd"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := st.(*node.EgressStatus); s.IPv6 != nil || !s.Enforce {
		t.Fatalf("status %+v", s)
	}

	// 잘못된 ID, 모르는 필드는 거부한다.
	for op, body := range map[string]string{
		agentprotocol.OpEgressStatus: `{"env_id":"../x"}`,
		agentprotocol.OpEgressEnsure: `{"blocked":["ins_abcd","-F"]}`,
	} {
		if _, err := h.Handle(ctx, op, []byte(body), nil); err == nil {
			t.Fatalf("%s %s accepted", op, body)
		}
	}
	if _, err := h.Handle(ctx, agentprotocol.OpEgressEnsure, []byte(`{"blocked":[],"chain":"INPUT"}`), nil); err == nil {
		t.Fatal("unknown field accepted")
	}

	// DOCKER-USER가 없으면 차단 요청을 거부한다(서버는 이 문장으로 unsupported를 안다).
	ipt.noChain = true
	_, err = h.Handle(ctx, agentprotocol.OpEgressEnsure, []byte(`{"blocked":["ins_abcd"]}`), nil)
	if err == nil || !strings.Contains(err.Error(), node.ErrEgressUnsupported.Error()) {
		t.Fatalf("want unsupported, got %v", err)
	}
	for _, c := range ipt.calls {
		if strings.HasPrefix(c, "-N") || strings.HasPrefix(c, "-F") || strings.HasPrefix(c, "-X") {
			t.Fatalf("must never create/flush chains: %s", c)
		}
	}
}
