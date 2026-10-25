package netlog

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/node"
)

// Docker는 수집기가 쓰는 docker 조회다(node.Docker가 만족한다).
type Docker interface {
	ManagedNetworks(ctx context.Context) ([]string, error)
	NetworkInfo(ctx context.Context, name string) (*node.NetworkInfo, error)
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	InspectMany(ctx context.Context, names []string) (map[string]*node.ContainerState, error)
}

// capture는 인터페이스 하나의 패킷 소스다. Read는 시간 제한(약 1초)이 지나면 (0, 0, nil)을 돌려준다.
type capture interface {
	Read(buf []byte) (n, wire int, err error)
	Close() error
}

const (
	envNetworkPrefix = "keel-env_"
	labelMeshService = "keel.mesh.service" // mesh forwarder가 대신하는 서비스 이름(internal/mesh와 같은 값)
	ringSize         = 1024
)

// Collector는 노드의 모든 환경 네트워크를 관찰해 Event를 만든다. Subscribe로 최근 기록과 새 기록을 받는다.
type Collector struct {
	NodeID string
	Docker Docker
	Log    *slog.Logger
	// open은 테스트에서 바꾼다(기본: AF_PACKET).
	open func(ifname string) (capture, error)
	// iface는 gateway 주소를 가진 인터페이스 이름을 찾는다(테스트에서 바꾼다).
	iface func(gw []netip.Addr) (string, error)

	epMu      sync.RWMutex
	endpoints map[string]map[netip.Addr]Endpoint // env → 주소 → 정체
	subnets   map[string][]netip.Prefix
	gateways  map[string][]netip.Addr
	refreshed time.Time
	refreshMu sync.Mutex

	mu     sync.Mutex
	seq    uint64
	epoch  int64
	ring   []Event
	subs   map[chan Event]struct{}
	status Status
}

// Run은 ctx가 끝날 때까지 환경 네트워크를 찾아 캡처한다.
func (c *Collector) Run(ctx context.Context) {
	if c.open == nil {
		c.open = openCapture
	}
	if c.iface == nil {
		c.iface = interfaceFor
	}
	if err := probeCapture(); err != nil {
		c.setStatus(Status{Node: c.NodeID, Active: false, Error: err.Error()})
		c.log().Warn("network logs disabled", "err", err)
		<-ctx.Done()
		return
	}
	running := map[string]*runningCapture{}
	defer func() {
		for _, r := range running {
			r.cancel()
		}
	}()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		c.refresh(ctx)
		c.syncCaptures(ctx, running)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *Collector) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// syncCaptures는 지금 있는 환경 네트워크마다 캡처 하나를 돌리고 사라진 네트워크의 캡처를 멈춘다.
type runningCapture struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *Collector) syncCaptures(ctx context.Context, running map[string]*runningCapture) {
	c.epMu.RLock()
	want := map[string][]netip.Addr{}
	for env, gws := range c.gateways {
		want[env] = gws
	}
	c.epMu.RUnlock()
	var errs []string
	for env, r := range running {
		select {
		case <-r.done: // 읽기 오류로 끝났다: 아래에서 다시 연다
			delete(running, env)
			continue
		default:
		}
		if _, ok := want[env]; !ok {
			r.cancel()
			delete(running, env)
		}
	}
	for env, gws := range want {
		if _, ok := running[env]; ok {
			continue
		}
		ifname, err := c.iface(gws)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		cp, err := c.open(ifname)
		if err != nil {
			errs = append(errs, ifname+": "+err.Error())
			continue
		}
		cctx, cancel := context.WithCancel(ctx)
		r := &runningCapture{cancel: cancel, done: make(chan struct{})}
		running[env] = r
		go func() {
			defer close(r.done)
			c.captureLoop(cctx, env, cp)
		}()
	}
	nets := make([]string, 0, len(running))
	for env := range running {
		nets = append(nets, env)
	}
	sort.Strings(nets)
	st := Status{Node: c.NodeID, Active: true, Networks: nets}
	if len(errs) > 0 {
		sort.Strings(errs)
		st.Error = strings.Join(errs, "; ")
	}
	c.setStatus(st)
}

func (c *Collector) captureLoop(ctx context.Context, env string, cp capture) {
	defer cp.Close()
	t := &Tracker{Env: env, Classify: func(a netip.Addr) Endpoint { return c.Classify(env, a) }, Emit: c.record}
	buf := make([]byte, 2048)
	lastSweep := time.Now()
	for ctx.Err() == nil {
		n, wire, err := cp.Read(buf)
		now := time.Now()
		if err != nil {
			c.log().Warn("network log capture stopped", "environment", env, "err", err)
			t.Flush(now)
			return
		}
		if n > 0 {
			if p, ok := ParseFrame(buf[:n], wire); ok {
				t.Packet(p, now)
			}
		}
		if now.Sub(lastSweep) >= time.Second {
			t.Sweep(now)
			lastSweep = now
		}
	}
	t.Flush(time.Now())
}

// Classify는 환경 env 네트워크의 주소를 분류한다. 모르는 컨테이너 주소면 컨테이너 목록을 (짧은 간격 제한으로) 다시 읽는다.
func (c *Collector) Classify(env string, a netip.Addr) Endpoint {
	a = a.Unmap()
	if ep, ok, inSubnet := c.lookup(env, a); ok {
		return ep
	} else if inSubnet {
		c.refreshSoon()
		if ep, ok, _ := c.lookup(env, a); ok {
			return ep
		}
		return Endpoint{Kind: "private"}
	}
	switch {
	case a.IsLoopback() || a.IsLinkLocalUnicast():
		return Endpoint{Kind: "host"}
	case a.IsPrivate() || cgnat.Contains(a):
		return Endpoint{Kind: "private"}
	case a.IsGlobalUnicast():
		return Endpoint{Kind: "internet"}
	}
	return Endpoint{Kind: "unknown"}
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (c *Collector) lookup(env string, a netip.Addr) (Endpoint, bool, bool) {
	c.epMu.RLock()
	defer c.epMu.RUnlock()
	if ep, ok := c.endpoints[env][a]; ok {
		return ep, true, true
	}
	for _, g := range c.gateways[env] {
		if g == a {
			return Endpoint{Kind: "host"}, true, true
		}
	}
	for _, p := range c.subnets[env] {
		if p.Contains(a) {
			return Endpoint{}, false, true
		}
	}
	return Endpoint{}, false, false
}

func (c *Collector) refreshSoon() {
	c.epMu.RLock()
	recent := time.Since(c.refreshed) < 3*time.Second
	c.epMu.RUnlock()
	if recent {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.refresh(ctx)
}

// refresh는 환경 네트워크(서브넷, gateway)와 관리 컨테이너의 주소를 다시 읽는다.
func (c *Collector) refresh(ctx context.Context) {
	if !c.refreshMu.TryLock() {
		return
	}
	defer c.refreshMu.Unlock()
	nets, err := c.Docker.ManagedNetworks(ctx)
	if err != nil {
		c.log().Debug("network logs: list networks", "err", err)
		return
	}
	subnets, gateways := map[string][]netip.Prefix{}, map[string][]netip.Addr{}
	for _, n := range nets {
		if !strings.HasPrefix(n, envNetworkPrefix) {
			continue
		}
		info, err := c.Docker.NetworkInfo(ctx, n)
		if err != nil {
			continue
		}
		env := strings.TrimPrefix(n, "keel-")
		subnets[env], gateways[env] = info.Subnets, info.Gateways
	}
	managed, err := c.Docker.ListManaged(ctx)
	if err != nil {
		return
	}
	var names []string
	for name, labels := range managed {
		if _, ok := subnets[labels[node.LabelEnv]]; ok || labels[node.LabelRole] == "proxy" {
			names = append(names, name)
		}
	}
	states, err := c.Docker.InspectMany(ctx, names)
	if err != nil {
		return
	}
	eps := map[string]map[netip.Addr]Endpoint{}
	for name, st := range states {
		ep := endpointOf(name, st.Labels)
		for env := range subnets {
			for _, raw := range st.IP("keel-" + env) {
				a, err := netip.ParseAddr(raw)
				if err != nil {
					continue
				}
				if eps[env] == nil {
					eps[env] = map[netip.Addr]Endpoint{}
				}
				eps[env][a.Unmap()] = ep
			}
		}
	}
	c.epMu.Lock()
	c.endpoints, c.subnets, c.gateways, c.refreshed = eps, subnets, gateways, time.Now()
	c.epMu.Unlock()
}

func endpointOf(name string, labels map[string]string) Endpoint {
	switch labels[node.LabelRole] {
	case node.RoleApp, node.RoleCron, node.RolePreDeploy:
		if labels[node.LabelService] != "" {
			return Endpoint{Kind: "service", ServiceID: labels[node.LabelService], Replica: name}
		}
	case node.RoleMeshProxy:
		return Endpoint{Kind: "mesh", ServiceName: labels[labelMeshService]}
	case node.RoleTCPProxy, "proxy":
		return Endpoint{Kind: "edge_proxy"}
	}
	return Endpoint{Kind: "private"}
}

// interfaceFor는 gateway 주소 중 하나를 가진 호스트 인터페이스(docker bridge)를 찾는다.
func interfaceFor(gws []netip.Addr) (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			for _, g := range gws {
				if ip.Unmap() == g.Unmap() {
					return ifc.Name, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no host interface has gateway %v", gws)
}

// ---------------- 기록 보관과 구독 ----------------

func (c *Collector) record(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == 0 {
		c.epoch = time.Now().UnixNano()
	}
	c.seq++
	e.ID = fmt.Sprintf("%s-%d-%d", c.NodeID, c.epoch, c.seq)
	c.ring = append(c.ring, e)
	if len(c.ring) > ringSize {
		c.ring = append([]Event(nil), c.ring[len(c.ring)-ringSize:]...)
	}
	c.publish(e)
}

func (c *Collector) publish(e Event) {
	for ch := range c.subs {
		select {
		case ch <- e:
		default:
			// 느린 구독자는 끊는다(다시 연결하면 ring을 replay하고 서버가 ID로 중복을 거른다).
			close(ch)
			delete(c.subs, ch)
		}
	}
}

func (c *Collector) setStatus(st Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st.Active == c.status.Active && st.Error == c.status.Error && strings.Join(st.Networks, ",") == strings.Join(c.status.Networks, ",") && c.status.Node != "" {
		return
	}
	c.status = st
	s := st
	c.publish(Event{Kind: "status", Status: &s})
}

// Subscribe는 현재 상태, 최근 기록, 새 기록 채널을 돌려준다.
func (c *Collector) Subscribe() (past []Event, ch chan Event, cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs == nil {
		c.subs = map[chan Event]struct{}{}
	}
	if c.status.Node != "" {
		s := c.status
		past = append(past, Event{Kind: "status", Status: &s})
	}
	past = append(past, c.ring...)
	ch = make(chan Event, 1024)
	c.subs[ch] = struct{}{}
	return past, ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
	}
}

// Follow는 최근 기록과 새 기록을 out으로 보낸다(ctx가 끝나거나 구독이 끊길 때까지).
func (c *Collector) Follow(ctx context.Context, out func(Event)) {
	past, ch, cancel := c.Subscribe()
	defer cancel()
	for _, e := range past {
		out(e)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			out(e)
		}
	}
}
