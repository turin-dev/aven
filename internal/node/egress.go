package node

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 아웃바운드 IPv6(P8).
//
// 상태(EgressProbe): 환경 네트워크의 컨테이너가 IPv6로 밖에 나갈 수 있는지를 읽기만 해서 판단한다.
//   1. 환경 네트워크(keel-<env>)에 IPv6 서브넷이 있는지(docker network inspect)
//   2. 그 환경의 실행 중인 컨테이너 network namespace에 IPv6 기본 경로가 있는지(/proc/<pid>/net/ipv6_route, exec 없음)
//   3. 호스트의 IPv6 forwarding과 기본 경로(/proc/sys, /proc/net/ipv6_route)
//   4. 서브넷이 ULA(fc00::/7)면 Docker의 NAT66(ip6tables nat POSTROUTING의 MASQUERADE/SNAT) 규칙
// 하나라도 확실히 없으면 false, 확인할 수 없으면 nil(모름)이다. 외부로 패킷을 보내지 않는다.
//
// 차단(EgressEnsure): outbound_ipv6=false인 인스턴스 컨테이너의 IPv6 /128에서 fc00::/7 밖으로 나가는 새 연결을
// ip6tables DOCKER-USER 체인에서 REJECT한다. 규칙마다 주석 keel:egress6:<instanceID>를 붙이고, 이 주석이 붙은 규칙만
// 만들고 지운다. 체인을 비우거나(-F) 만들지(-N) 않고, Docker나 다른 도구의 규칙은 건드리지 않는다.

// EgressTagPrefix는 aven이 DOCKER-USER에 넣는 규칙의 주석 접두사다(뒤에 instance ID).
const EgressTagPrefix = "keel:egress6:"

// EgressAllowedDst는 차단 중에도 허용하는 목적지다(ULA: 환경 네트워크, mesh, 다른 사설 주소).
const EgressAllowedDst = "fc00::/7"

// EgressStatus는 노드에서 본 환경 네트워크의 IPv6 egress 상태다.
type EgressStatus struct {
	// IPv6는 컨테이너가 IPv6로 밖에 나갈 수 있는지다(nil이면 확인하지 못함).
	IPv6   *bool  `json:"ipv6"`
	Reason string `json:"reason,omitempty"`
	// Enforce는 이 노드에서 서비스별 차단(DOCKER-USER ip6tables 규칙)을 할 수 있는지다.
	Enforce       bool   `json:"enforce"`
	EnforceReason string `json:"enforce_reason,omitempty"`
}

// EgressEnsureResult는 EgressEnsure의 결과다.
type EgressEnsureResult struct {
	// Supported가 false면 이 노드에서 차단 규칙을 쓸 수 없다(Reason). 차단할 인스턴스가 없으면 오류 없이 이것만 알린다.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Rules     int    `json:"rules"` // 반영 뒤 aven:egress6 규칙 수
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
}

// ErrEgressUnsupported는 차단할 인스턴스가 있는데 이 노드에서 ip6tables DOCKER-USER를 쓸 수 없을 때다.
var ErrEgressUnsupported = errors.New("이 노드에서는 아웃바운드 IPv6 차단을 쓸 수 없습니다")

// MaxEgressBlocked는 한 번에 받는 차단 인스턴스 수 상한이다.
const MaxEgressBlocked = 5000

var egressInstanceRe = regexp.MustCompile(`^ins_[a-z2-7]{4,40}$`)
var egressEnvRe = regexp.MustCompile(`^env_[a-zA-Z0-9]{4,64}$`)

// ValidEgressInstanceID는 차단 대상 instance ID 형식이다(주석에 그대로 들어간다).
func ValidEgressInstanceID(id string) bool { return egressInstanceRe.MatchString(id) }

// ValidEgressEnvID는 상태를 물을 환경 ID 형식이다.
func ValidEgressEnvID(id string) bool { return egressEnvRe.MatchString(id) }

// IP6Tables는 ip6tables 명령이다(테스트에서 가짜로 바꾼다). Run은 stdout+stderr를 돌려준다.
type IP6Tables interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// execIP6Tables는 진짜 ip6tables다. xtables 잠금은 최대 5초 기다린다.
type execIP6Tables struct{ bin string }

func (e execIP6Tables) Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return run(ctx, nil, nil, e.bin, append([]string{"-w", "5"}, args...)...)
}

// SystemIP6Tables는 이 호스트의 ip6tables다. 설치되어 있지 않거나 root가 아니면(ip6tables는 root가 필요하다, 단위 테스트 등) nil이다.
func SystemIP6Tables() IP6Tables {
	if os.Geteuid() != 0 {
		return nil
	}
	for _, p := range []string{"ip6tables", "/usr/sbin/ip6tables", "/sbin/ip6tables"} {
		if bin, err := exec.LookPath(p); err == nil {
			return execIP6Tables{bin: bin}
		}
	}
	return nil
}

// egressDocker는 EgressProbe와 EgressEnsure에 필요한 docker 기능이다(node.Docker가 구현한다).
type egressDocker interface {
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	ContainerPIDs(ctx context.Context, names []string) (map[string]int, error)
	InspectMany(ctx context.Context, names []string) (map[string]*ContainerState, error)
	NetworkInfo(ctx context.Context, name string) (*NetworkInfo, error)
}

// EgressStatus는 이 노드에서 환경(envID)의 IPv6 egress 상태다.
func (d *Docker) EgressStatus(ctx context.Context, envID string) (*EgressStatus, error) {
	return EgressProbe(ctx, d, SystemIP6Tables(), "/proc", envID)
}

// EgressEnsure는 이 노드의 aven:egress6 규칙을 blocked(차단할 instance ID 전체)에 맞춘다.
func (d *Docker) EgressEnsure(ctx context.Context, blocked []string) (*EgressEnsureResult, error) {
	return EgressEnsure(ctx, d, SystemIP6Tables(), blocked)
}

func boolp(b bool) *bool { return &b }

// EgressProbe는 envID 환경 네트워크의 IPv6 egress 상태를 읽는다. ipt가 nil이면 ip6tables가 없는 노드다.
func EgressProbe(ctx context.Context, d egressDocker, ipt IP6Tables, procRoot, envID string) (*EgressStatus, error) {
	if !ValidEgressEnvID(envID) {
		return nil, fmt.Errorf("environment %q is not allowed", envID)
	}
	st := &EgressStatus{}
	st.Enforce, st.EnforceReason = egressEnforceSupported(ctx, ipt)
	no := func(reason string) (*EgressStatus, error) {
		st.IPv6, st.Reason = boolp(false), reason
		return st, nil
	}
	unknown := func(reason string) (*EgressStatus, error) {
		st.IPv6, st.Reason = nil, reason
		return st, nil
	}

	// 1. 환경 네트워크의 IPv6 서브넷
	ni, err := d.NetworkInfo(ctx, "keel-"+envID)
	if err != nil {
		return unknown("이 노드에 환경 네트워크가 아직 없습니다")
	}
	var v6 []netip.Prefix
	for _, p := range ni.Subnets {
		if p.Addr().Is6() && !p.Addr().Is4In6() {
			v6 = append(v6, p)
		}
	}
	if len(v6) == 0 {
		return no("환경 네트워크에 IPv6가 꺼져 있습니다")
	}

	// 2. 컨테이너 namespace의 기본 경로(실행 중인 컨테이너가 있을 때만)
	if pid := envContainerPID(ctx, d, envID); pid > 0 {
		if has, err := hasDefaultRoute6(filepath.Join(procRoot, strconv.Itoa(pid), "net", "ipv6_route")); err == nil && !has {
			return no("컨테이너에 IPv6 기본 경로가 없습니다")
		}
	}

	// 3. 호스트 forwarding과 기본 경로
	if b, err := os.ReadFile(filepath.Join(procRoot, "sys", "net", "ipv6", "conf", "all", "forwarding")); err == nil && strings.TrimSpace(string(b)) != "1" {
		return no("노드의 IPv6 forwarding이 꺼져 있습니다")
	}
	if has, err := hasDefaultRoute6(filepath.Join(procRoot, "net", "ipv6_route")); err == nil && !has {
		return no("노드에 IPv6 기본 경로가 없습니다")
	} else if err != nil {
		return unknown("노드의 IPv6 경로를 읽지 못했습니다")
	}

	// 4. ULA 서브넷이면 NAT66이 있어야 한다
	var ula []netip.Prefix
	for _, p := range v6 {
		if isULA(p.Addr()) {
			ula = append(ula, p)
		}
	}
	if len(ula) == 0 {
		st.IPv6 = boolp(true)
		return st, nil
	}
	if ipt == nil {
		return unknown("ip6tables가 없어 Docker NAT66 규칙을 확인하지 못했습니다")
	}
	nat, err := ipt.Run(ctx, "-t", "nat", "-S")
	if err != nil {
		return unknown("ip6tables nat 테이블을 읽지 못했습니다")
	}
	if !hasDockerNAT(nat) {
		return unknown("ip6tables에서 Docker NAT 규칙을 찾지 못했습니다(nftables 백엔드이거나 dockerd ip6tables가 꺼져 있을 수 있습니다)")
	}
	for _, p := range ula {
		if !hasMasquerade(nat, p) {
			return no("환경 네트워크(" + p.String() + ")의 NAT66(MASQUERADE) 규칙이 없습니다")
		}
	}
	st.IPv6 = boolp(true)
	return st, nil
}

// egressEnforceSupported는 ip6tables와 Docker의 DOCKER-USER(ip6) 체인이 있는지다. 체인은 Docker가 만든다(aven은 만들지 않는다).
func egressEnforceSupported(ctx context.Context, ipt IP6Tables) (bool, string) {
	if ipt == nil {
		return false, "이 노드에서 ip6tables를 쓸 수 없습니다(설치되어 있지 않거나 aven이 root로 돌지 않습니다)"
	}
	if _, err := ipt.Run(ctx, "-S", "DOCKER-USER"); err != nil {
		return false, "이 노드에 Docker의 ip6tables DOCKER-USER 체인이 없습니다(dockerd의 ip6tables가 꺼져 있을 수 있습니다)"
	}
	return true, ""
}

// envContainerPID는 환경의 실행 중인 aven 컨테이너 하나(이름순 첫 app, 없으면 아무 역할)의 PID다. 없으면 0이다.
func envContainerPID(ctx context.Context, d egressDocker, envID string) int {
	managed, err := d.ListManaged(ctx)
	if err != nil {
		return 0
	}
	var apps, others []string
	for name, l := range managed {
		if l[LabelEnv] != envID {
			continue
		}
		if l[LabelRole] == RoleApp {
			apps = append(apps, name)
		} else {
			others = append(others, name)
		}
	}
	sort.Strings(apps)
	sort.Strings(others)
	names := append(apps, others...)
	if len(names) > MaxPortScanContainers {
		names = names[:MaxPortScanContainers]
	}
	pids, err := d.ContainerPIDs(ctx, names)
	if err != nil {
		return 0
	}
	for _, n := range names {
		if pids[n] > 0 {
			return pids[n]
		}
	}
	return 0
}

// rtfUp, rtfReject는 /proc/net/ipv6_route flags다.
const (
	rtfUp     = 0x0001
	rtfReject = 0x0200
)

func hasDefaultRoute6(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return HasDefaultRoute6(f)
}

// HasDefaultRoute6은 /proc/net/ipv6_route 내용에 쓸 수 있는 IPv6 기본 경로(::/0, up, reject 아님, lo 아님)가 있는지다.
func HasDefaultRoute6(r io.Reader) (bool, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		if f[0] != strings.Repeat("0", 32) || f[1] != "00" || f[9] == "lo" {
			continue
		}
		flags, err := strconv.ParseUint(f[8], 16, 32)
		if err != nil || flags&rtfUp == 0 || flags&rtfReject != 0 {
			continue
		}
		return true, nil
	}
	return false, sc.Err()
}

var ula = netip.MustParsePrefix(EgressAllowedDst)

func isULA(a netip.Addr) bool { return ula.Contains(a) }

// hasDockerNAT는 ip6tables nat 테이블에 Docker 체인이 있는지다(없으면 Docker가 ip6tables로 NAT하지 않는다).
func hasDockerNAT(nat string) bool {
	for _, line := range strings.Split(nat, "\n") {
		if strings.TrimSpace(line) == "-N DOCKER" {
			return true
		}
	}
	return false
}

// hasMasquerade는 nat POSTROUTING에 서브넷 p에서 나가는 MASQUERADE 또는 SNAT 규칙이 있는지다.
func hasMasquerade(nat string, p netip.Prefix) bool {
	for _, line := range strings.Split(nat, "\n") {
		args := splitRule(line)
		if len(args) < 2 || args[0] != "-A" || args[1] != "POSTROUTING" {
			continue
		}
		src, jump := "", ""
		for i := 2; i+1 < len(args); i++ {
			switch args[i] {
			case "-s":
				if i == 2 || args[i-1] != "!" {
					src = args[i+1]
				}
			case "-j":
				jump = args[i+1]
			}
		}
		if jump != "MASQUERADE" && jump != "SNAT" {
			continue
		}
		if sp, err := netip.ParsePrefix(src); err == nil && sp.Masked() == p.Masked() {
			return true
		}
	}
	return false
}

// splitRule은 `ip6tables -S` 한 줄을 인자로 나눈다(큰따옴표와 백슬래시 이스케이프를 푼다).
func splitRule(line string) []string {
	var out []string
	var cur strings.Builder
	in, quoted, esc := false, false, false
	for _, r := range strings.TrimSpace(line) {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quoted:
			esc = true
		case r == '"':
			quoted = !quoted
			in = true
		case (r == ' ' || r == '\t') && !quoted:
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// egressRule은 DOCKER-USER의 aven 규칙 하나다(키는 instance + 원본 주소).
type egressRule struct {
	Instance string
	Addr     netip.Addr
}

func (r egressRule) key() string { return r.Instance + "|" + r.Addr.String() }

// EgressRuleSpec은 규칙의 ip6tables 인자다(체인 이름 뒤, -I/-D 없이). 응답 패킷(ESTABLISHED/RELATED)은 막지 않는다:
// 밖에서 들어온 연결(공개 포트)에 대한 응답은 그대로 나간다.
func EgressRuleSpec(instanceID string, addr netip.Addr) []string {
	return []string{"-s", addr.String() + "/128", "!", "-d", EgressAllowedDst,
		"-m", "conntrack", "!", "--ctstate", "RELATED,ESTABLISHED",
		"-m", "comment", "--comment", EgressTagPrefix + instanceID,
		"-j", "REJECT", "--reject-with", "icmp6-adm-prohibited"}
}

// parseEgressRules는 `ip6tables -S DOCKER-USER` 출력에서 aven:egress6 규칙을 고른다. 값은 -D에 그대로 쓸 인자다.
func parseEgressRules(out string) map[string]existingRule {
	rules := map[string]existingRule{}
	for _, line := range strings.Split(out, "\n") {
		args := splitRule(line)
		if len(args) < 2 || args[0] != "-A" || args[1] != "DOCKER-USER" {
			continue
		}
		var inst, src string
		for i := 2; i+1 < len(args); i++ {
			switch args[i] {
			case "--comment":
				if strings.HasPrefix(args[i+1], EgressTagPrefix) {
					inst = strings.TrimPrefix(args[i+1], EgressTagPrefix)
				}
			case "-s":
				src = args[i+1]
			}
		}
		if inst == "" {
			continue
		}
		r := egressRule{Instance: inst}
		if p, err := netip.ParsePrefix(src); err == nil {
			r.Addr = p.Addr()
		} else if a, err := netip.ParseAddr(src); err == nil {
			r.Addr = a
		}
		k := r.key()
		if _, dup := rules[k]; dup { // 같은 규칙이 두 번 있으면 둘째부터는 지운다
			k += "|dup" + strconv.Itoa(len(rules))
		}
		rules[k] = existingRule{rule: r, del: append([]string{"-D"}, args[1:]...)}
	}
	return rules
}

type existingRule struct {
	rule egressRule
	del  []string
}

// EgressEnsure는 이 노드의 aven:egress6 규칙을 blocked에 맞춘다: blocked 인스턴스의 실행 중인 컨테이너마다 IPv6 주소(/128)별로
// 규칙 하나를 두고, 그 밖의 aven:egress6 규칙(풀린 인스턴스, 바뀐 주소, 지워진 컨테이너)은 지운다. 이미 맞으면 아무것도 바꾸지 않는다.
// ipt가 nil이거나 DOCKER-USER가 없으면 blocked가 비었을 때는 Supported=false로 끝나고, 아니면 ErrEgressUnsupported다.
func EgressEnsure(ctx context.Context, d egressDocker, ipt IP6Tables, blocked []string) (*EgressEnsureResult, error) {
	if len(blocked) > MaxEgressBlocked {
		return nil, errors.New("too many instances")
	}
	want := map[string]bool{}
	for _, id := range blocked {
		if !ValidEgressInstanceID(id) {
			return nil, fmt.Errorf("instance %q is not allowed", id)
		}
		want[id] = true
	}
	res := &EgressEnsureResult{}
	if ok, reason := egressEnforceSupported(ctx, ipt); !ok {
		res.Reason = reason
		if len(want) > 0 {
			return res, fmt.Errorf("%w: %s", ErrEgressUnsupported, reason)
		}
		return res, nil
	}
	res.Supported = true
	cur, err := ipt.Run(ctx, "-S", "DOCKER-USER")
	if err != nil {
		return res, fmt.Errorf("ip6tables -S DOCKER-USER: %w", err)
	}
	have := parseEgressRules(cur)

	desired := map[string]egressRule{}
	if len(want) > 0 {
		desired, err = desiredEgressRules(ctx, d, want)
		if err != nil {
			return res, err
		}
	}

	var errs []error
	// 먼저 지우고(오래된 주소가 새 컨테이너에 다시 배정됐을 수 있다) 그다음 넣는다. 정렬해서 순서를 고정한다.
	delKeys := make([]string, 0, len(have))
	for k := range have {
		if _, ok := desired[k]; !ok {
			delKeys = append(delKeys, k)
		}
	}
	sort.Strings(delKeys)
	for _, k := range delKeys {
		if _, err := ipt.Run(ctx, have[k].del...); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", k, err))
			continue
		}
		res.Removed++
		delete(have, k)
	}
	addKeys := make([]string, 0, len(desired))
	for k := range desired {
		if _, ok := have[k]; !ok {
			addKeys = append(addKeys, k)
		}
	}
	sort.Strings(addKeys)
	for _, k := range addKeys {
		r := desired[k]
		// -I(맨 앞): 옛 Docker가 DOCKER-USER 끝에 두는 RETURN보다 앞에 와야 한다.
		args := append([]string{"-I", "DOCKER-USER"}, EgressRuleSpec(r.Instance, r.Addr)...)
		if _, err := ipt.Run(ctx, args...); err != nil {
			errs = append(errs, fmt.Errorf("add %s: %w", k, err))
			continue
		}
		res.Added++
		have[k] = existingRule{rule: r}
	}
	res.Rules = len(have)
	return res, errors.Join(errs...)
}

// desiredEgressRules는 want 인스턴스의 실행 중인 aven 컨테이너(역할 무관)의 IPv6 주소마다 규칙 하나다.
// 링크 로컬 주소는 FORWARD를 지나지 않으므로 뺀다.
func desiredEgressRules(ctx context.Context, d egressDocker, want map[string]bool) (map[string]egressRule, error) {
	managed, err := d.ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, l := range managed {
		if want[l[LabelInstance]] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := map[string]egressRule{}
	if len(names) == 0 {
		return out, nil
	}
	states, err := d.InspectMany(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		st := states[name]
		if st == nil || !st.Running {
			continue
		}
		inst := managed[name][LabelInstance]
		for _, ips := range st.IPs {
			for _, ip := range ips {
				a, err := netip.ParseAddr(ip)
				if err != nil || !a.Is6() || a.Is4In6() || a.IsLinkLocalUnicast() || a.IsLoopback() || a.IsUnspecified() {
					continue
				}
				r := egressRule{Instance: inst, Addr: a}
				out[r.key()] = r
			}
		}
	}
	return out, nil
}
