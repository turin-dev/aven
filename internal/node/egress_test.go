package node

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeIPT는 ip6tables 대역이다: DOCKER-USER 체인 내용(규칙 인자)을 메모리에 두고 -S/-I/-D/-t nat -S만 흉내 낸다.
// 진짜 ip6tables는 실행하지 않는다.
type fakeIPT struct {
	noChain bool       // DOCKER-USER가 없다(dockerd ip6tables 꺼짐)
	rules   [][]string // DOCKER-USER 규칙(체인 이름 뒤 인자)
	nat     string
	writes  []string // -I/-D 호출
	failAdd bool
}

func (f *fakeIPT) Run(_ context.Context, args ...string) (string, error) {
	switch {
	case len(args) == 3 && args[0] == "-t" && args[1] == "nat" && args[2] == "-S":
		return f.nat, nil
	case len(args) == 2 && args[0] == "-S" && args[1] == "DOCKER-USER":
		if f.noChain {
			return "ip6tables: No chain/target/match by that name.", errors.New("exit status 1")
		}
		var b strings.Builder
		b.WriteString("-N DOCKER-USER\n")
		for _, r := range f.rules {
			b.WriteString("-A DOCKER-USER")
			for i, a := range r {
				// 진짜 ip6tables처럼 ':'가 든 주석은 큰따옴표로 감싼다.
				if i > 0 && r[i-1] == "--comment" {
					a = `"` + a + `"`
				}
				b.WriteString(" " + a)
			}
			b.WriteString("\n")
		}
		// aven이 만들지 않은 규칙(건드리면 안 된다)
		b.WriteString(`-A DOCKER-USER -s fd00:99::/64 -m comment --comment "admin rule" -j DROP` + "\n")
		return b.String(), nil
	case len(args) > 2 && args[0] == "-I" && args[1] == "DOCKER-USER":
		f.writes = append(f.writes, strings.Join(args, " "))
		if f.failAdd {
			return "boom", errors.New("exit status 1")
		}
		f.rules = append([][]string{append([]string(nil), args[2:]...)}, f.rules...)
		return "", nil
	case len(args) > 2 && args[0] == "-D" && args[1] == "DOCKER-USER":
		f.writes = append(f.writes, strings.Join(args, " "))
		for i, r := range f.rules {
			if reflect.DeepEqual(r, args[2:]) {
				f.rules = append(f.rules[:i], f.rules[i+1:]...)
				return "", nil
			}
		}
		return "Bad rule (does a matching rule exist in that chain?)", errors.New("exit status 1")
	}
	return "", errors.New("unexpected ip6tables call: " + strings.Join(args, " "))
}

// fakeEgressDocker는 egressDocker 대역이다.
type fakeEgressDocker struct {
	managed map[string]map[string]string
	states  map[string]*ContainerState
	pids    map[string]int
	nets    map[string]*NetworkInfo
}

func (f *fakeEgressDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	return f.managed, nil
}
func (f *fakeEgressDocker) ContainerPIDs(_ context.Context, names []string) (map[string]int, error) {
	out := map[string]int{}
	for _, n := range names {
		if p := f.pids[n]; p > 0 {
			out[n] = p
		}
	}
	return out, nil
}
func (f *fakeEgressDocker) InspectMany(_ context.Context, names []string) (map[string]*ContainerState, error) {
	out := map[string]*ContainerState{}
	for _, n := range names {
		if s, ok := f.states[n]; ok {
			out[n] = s
		}
	}
	return out, nil
}
func (f *fakeEgressDocker) NetworkInfo(_ context.Context, name string) (*NetworkInfo, error) {
	if n, ok := f.nets[name]; ok {
		return n, nil
	}
	return nil, errors.New("no such network")
}

const (
	insA = "ins_aaaa"
	insB = "ins_bbbb"
)

func egressDockerFixture() *fakeEgressDocker {
	app := func(inst string) map[string]string {
		return map[string]string{LabelManaged: "true", LabelInstance: inst, LabelRole: RoleApp, LabelEnv: "env_prod1"}
	}
	return &fakeEgressDocker{
		managed: map[string]map[string]string{
			"keel-a-1": app(insA), "keel-a-2": app(insA), "keel-b-1": app(insB),
			"keel-a-stopped": app(insA),
		},
		states: map[string]*ContainerState{
			"keel-a-1":       {Running: true, IPs: map[string][]string{"keel-env_prod1": {"fd00:1234:5678:aa::6", "10.0.0.6"}, "aven": {"fe80::1", "172.18.0.2"}}},
			"keel-a-2":       {Running: true, IPs: map[string][]string{"keel-env_prod1": {"fd00:1234:5678:aa::7", "10.0.0.7"}}},
			"keel-b-1":       {Running: true, IPs: map[string][]string{"keel-env_prod1": {"fd00:1234:5678:aa::8"}}},
			"keel-a-stopped": {Running: false, IPs: map[string][]string{"keel-env_prod1": {"fd00:1234:5678:aa::9"}}},
		},
	}
}

func ruleSources(f *fakeIPT) []string {
	var out []string
	for _, r := range f.rules {
		var src, tag string
		for i := 0; i+1 < len(r); i++ {
			switch r[i] {
			case "-s":
				src = r[i+1]
			case "--comment":
				tag = r[i+1]
			}
		}
		out = append(out, tag+" "+src)
	}
	return out
}

func TestEgressRuleSpec(t *testing.T) {
	got := strings.Join(EgressRuleSpec(insA, netip.MustParseAddr("fd00:1234:5678:aa::6")), " ")
	want := "-s fd00:1234:5678:aa::6/128 ! -d fc00::/7 -m conntrack ! --ctstate RELATED,ESTABLISHED -m comment --comment keel:egress6:ins_aaaa -j REJECT --reject-with icmp6-adm-prohibited"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestEgressEnsureAddsIdempotentAndCleans(t *testing.T) {
	ctx := context.Background()
	d := egressDockerFixture()
	ipt := &fakeIPT{}

	// 1. A를 막는다: 실행 중인 replica 둘의 IPv6 주소마다 하나(링크 로컬, IPv4, 멈춘 컨테이너는 뺀다)
	res, err := EgressEnsure(ctx, d, ipt, []string{insA})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Supported || res.Added != 2 || res.Removed != 0 || res.Rules != 2 {
		t.Fatalf("res %+v", res)
	}
	got := ruleSources(ipt)
	if len(got) != 2 || !contains(got, "keel:egress6:ins_aaaa fd00:1234:5678:aa::6/128") || !contains(got, "keel:egress6:ins_aaaa fd00:1234:5678:aa::7/128") {
		t.Fatalf("rules %v", got)
	}
	for _, w := range ipt.writes {
		if !strings.HasPrefix(w, "-I DOCKER-USER ") {
			t.Fatalf("must insert at the top of DOCKER-USER only: %s", w)
		}
	}

	// 2. 같은 상태로 다시: 아무것도 쓰지 않는다
	ipt.writes = nil
	res, err = EgressEnsure(ctx, d, ipt, []string{insA})
	if err != nil || res.Added != 0 || res.Removed != 0 || res.Rules != 2 || len(ipt.writes) != 0 {
		t.Fatalf("not idempotent: %+v %v %v", res, err, ipt.writes)
	}

	// 3. 컨테이너 재시작으로 주소가 바뀌면 옛 규칙을 지우고 새 규칙을 넣는다
	d.states["keel-a-2"].IPs["keel-env_prod1"] = []string{"fd00:1234:5678:aa::17"}
	ipt.writes = nil
	res, err = EgressEnsure(ctx, d, ipt, []string{insA})
	if err != nil || res.Added != 1 || res.Removed != 1 || res.Rules != 2 {
		t.Fatalf("readdress: %+v %v", res, err)
	}
	if !strings.HasPrefix(ipt.writes[0], "-D DOCKER-USER -s fd00:1234:5678:aa::7/128") || !strings.HasPrefix(ipt.writes[1], "-I DOCKER-USER -s fd00:1234:5678:aa::17/128") {
		t.Fatalf("delete must come before insert: %v", ipt.writes)
	}

	// 4. A를 풀고 B를 막는다: A 규칙은 모두 지워지고 B만 남는다. 관리자 규칙은 그대로다.
	res, err = EgressEnsure(ctx, d, ipt, []string{insB})
	if err != nil || res.Added != 1 || res.Removed != 2 || res.Rules != 1 {
		t.Fatalf("switch: %+v %v", res, err)
	}
	if got := ruleSources(ipt); !reflect.DeepEqual(got, []string{"keel:egress6:ins_bbbb fd00:1234:5678:aa::8/128"}) {
		t.Fatalf("rules %v", got)
	}

	// 5. 아무것도 막지 않으면 aven 규칙만 모두 지운다. 그 뒤로는 쓰지 않는다.
	ipt.writes = nil
	res, err = EgressEnsure(ctx, d, ipt, nil)
	if err != nil || res.Removed != 1 || res.Rules != 0 || len(ipt.rules) != 0 {
		t.Fatalf("clear: %+v %v %v", res, err, ipt.rules)
	}
	for _, w := range ipt.writes {
		if strings.Contains(w, "admin rule") || !strings.Contains(w, EgressTagPrefix) {
			t.Fatalf("touched a non-aven rule: %s", w)
		}
	}
	ipt.writes = nil
	if res, err := EgressEnsure(ctx, d, ipt, nil); err != nil || res.Rules != 0 || len(ipt.writes) != 0 {
		t.Fatalf("empty ensure must not write: %+v %v %v", res, err, ipt.writes)
	}
}

// 지워진 컨테이너나 인스턴스의 규칙(stale)은 태그로 찾아 지운다. 중복 규칙도 하나만 남긴다.
func TestEgressEnsureStaleAndDuplicate(t *testing.T) {
	ctx := context.Background()
	d := egressDockerFixture()
	gone := EgressRuleSpec("ins_gone", netip.MustParseAddr("fd00:1234:5678:aa::99"))
	dup := EgressRuleSpec(insB, netip.MustParseAddr("fd00:1234:5678:aa::8"))
	ipt := &fakeIPT{rules: [][]string{gone, dup, dup}}
	res, err := EgressEnsure(ctx, d, ipt, []string{insB})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 || res.Added != 0 || res.Rules != 1 {
		t.Fatalf("res %+v rules %v", res, ruleSources(ipt))
	}
	if got := ruleSources(ipt); !reflect.DeepEqual(got, []string{"keel:egress6:ins_bbbb fd00:1234:5678:aa::8/128"}) {
		t.Fatalf("rules %v", got)
	}
}

func TestEgressEnsureUnsupported(t *testing.T) {
	ctx := context.Background()
	d := egressDockerFixture()
	// ip6tables가 없다
	if res, err := EgressEnsure(ctx, d, nil, nil); err != nil || res.Supported || res.Reason == "" {
		t.Fatalf("nil ipt, nothing blocked: %+v %v", res, err)
	}
	if _, err := EgressEnsure(ctx, d, nil, []string{insA}); !errors.Is(err, ErrEgressUnsupported) {
		t.Fatalf("nil ipt: %v", err)
	}
	// DOCKER-USER가 없다: 체인을 만들지 않고 거부한다
	ipt := &fakeIPT{noChain: true}
	_, err := EgressEnsure(ctx, d, ipt, []string{insA})
	if !errors.Is(err, ErrEgressUnsupported) || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatalf("no chain: %v", err)
	}
	if len(ipt.writes) != 0 {
		t.Fatalf("wrote %v", ipt.writes)
	}
	// 잘못된 instance ID는 주석에 들어가지 않는다
	if _, err := EgressEnsure(ctx, d, &fakeIPT{}, []string{"ins_x; -F"}); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestEgressEnsureAddFailure(t *testing.T) {
	ipt := &fakeIPT{failAdd: true}
	res, err := EgressEnsure(context.Background(), egressDockerFixture(), ipt, []string{insB})
	if err == nil || res.Added != 0 || res.Rules != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSplitRule(t *testing.T) {
	got := splitRule(`-A DOCKER-USER -s fd00::1/128 -m comment --comment "keel:egress6:ins_abcd" -m comment --comment "a \"b\" c" -j REJECT`)
	want := []string{"-A", "DOCKER-USER", "-s", "fd00::1/128", "-m", "comment", "--comment", "keel:egress6:ins_abcd", "-m", "comment", "--comment", `a "b" c`, "-j", "REJECT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestHasDefaultRoute6(t *testing.T) {
	zero := strings.Repeat("0", 32)
	reject := zero + " 00 " + zero + " 00 " + zero + " ffffffff 00000001 00000000 00200200       lo\n"
	conn := "fd8bca60ccbd000a0000000000000000 40 " + zero + " 00 " + zero + " 00000100 00000001 00000000 00000001     eth0\n"
	def := zero + " 00 " + zero + " 00 fd8bca60ccbd000a0000000000000001 00000400 00000001 00000000 00000003     eth0\n"
	for _, c := range []struct {
		in   string
		want bool
	}{{reject + conn, false}, {conn + def + reject, true}, {"", false}} {
		got, err := HasDefaultRoute6(strings.NewReader(c.in))
		if err != nil || got != c.want {
			t.Fatalf("%q: %v %v", c.in, got, err)
		}
	}
}

// main에서 읽은 실제 모양(Docker 29, iptables 백엔드)을 줄인 nat 테이블이다.
const natMain = `-P PREROUTING ACCEPT
-N DOCKER
-A POSTROUTING -s fd00:1234:5678:25::/64 ! -o br-mail-sample -j SNAT --to-source 2001:db8:6b:1943::25
-A POSTROUTING -s fd00:1234:5678:aa::/64 ! -o br-example123456 -j MASQUERADE
-A POSTROUTING -j ts-postrouting
`

func egressProc(t *testing.T, forwarding string, hostDefault, ctrDefault bool) string {
	t.Helper()
	root := t.TempDir()
	zero := strings.Repeat("0", 32)
	def := zero + " 00 " + zero + " 00 " + zero + " 00000400 00000001 00000000 00000003  eth0\n"
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sys/net/ipv6/conf/all/forwarding", forwarding+"\n")
	h, c := "", ""
	if hostDefault {
		h = def
	}
	if ctrDefault {
		c = def
	}
	write("net/ipv6_route", h)
	write("4242/net/ipv6_route", c)
	return root
}

func TestEgressProbe(t *testing.T) {
	ctx := context.Background()
	ula := &NetworkInfo{Subnets: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("fd00:1234:5678:aa::/64")}}
	v4only := &NetworkInfo{Subnets: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	newD := func(ni *NetworkInfo) *fakeEgressDocker {
		d := egressDockerFixture()
		d.pids = map[string]int{"keel-a-1": 4242}
		d.nets = map[string]*NetworkInfo{"keel-env_prod1": ni}
		return d
	}
	b := func(v bool) *bool { return &v }
	cases := []struct {
		name    string
		d       *fakeEgressDocker
		ipt     IP6Tables
		proc    string
		want    *bool
		enforce bool
	}{
		{"main today", newD(ula), &fakeIPT{nat: natMain}, egressProc(t, "1", true, true), b(true), true},
		{"no masquerade", newD(ula), &fakeIPT{nat: "-N DOCKER\n"}, egressProc(t, "1", true, true), b(false), true},
		{"nftables backend", newD(ula), &fakeIPT{nat: "-P POSTROUTING ACCEPT\n", noChain: true}, egressProc(t, "1", true, true), nil, false},
		{"v4-only network", newD(v4only), &fakeIPT{nat: natMain}, egressProc(t, "1", true, true), b(false), true},
		{"container no route", newD(ula), &fakeIPT{nat: natMain}, egressProc(t, "1", true, false), b(false), true},
		{"forwarding off", newD(ula), &fakeIPT{nat: natMain}, egressProc(t, "0", true, true), b(false), true},
		{"host no route (us without v6?)", newD(ula), &fakeIPT{nat: natMain}, egressProc(t, "1", false, true), b(false), true},
		{"no ip6tables", newD(ula), nil, egressProc(t, "1", true, true), nil, false},
		{"no network yet", &fakeEgressDocker{}, &fakeIPT{nat: natMain}, egressProc(t, "1", true, true), nil, true},
	}
	for _, c := range cases {
		st, err := EgressProbe(ctx, c.d, c.ipt, c.proc, "env_prod1")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(st.IPv6, c.want) || st.Enforce != c.enforce {
			t.Errorf("%s: got ipv6=%v enforce=%v (%+v)", c.name, ptrStr(st.IPv6), st.Enforce, st)
		}
		if st.IPv6 == nil || !*st.IPv6 {
			if st.Reason == "" {
				t.Errorf("%s: missing reason", c.name)
			}
		}
		if !st.Enforce && st.EnforceReason == "" {
			t.Errorf("%s: missing enforce reason", c.name)
		}
	}
	// 상태 조회는 쓰지 않는다
	ipt := &fakeIPT{nat: natMain}
	EgressProbe(ctx, newD(ula), ipt, egressProc(t, "1", true, true), "env_prod1")
	if len(ipt.writes) != 0 {
		t.Fatalf("probe wrote %v", ipt.writes)
	}
	if _, err := EgressProbe(ctx, newD(ula), ipt, "/proc", "../etc"); err == nil {
		t.Fatal("bad env id accepted")
	}
}

func ptrStr(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
