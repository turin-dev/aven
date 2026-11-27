package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/cloudflare"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// Cloudflare 연동(docs/self-hosting.md "Cloudflare"): 라우트가 있는 호스트 중 연동한 Cloudflare 존 안의 이름을, 그 호스트를
// 서비스하는 노드로 맞춘다.
//   - 여러 노드가 서비스하는 호스트: Load Balancing(노드마다 pool, TCP 443 health monitor, pool 좌표가 모두 있으면 proximity steering).
//   - 노드 하나만 서비스하거나, Load Balancing을 쓸 수 없거나(auto에서 구독 없음), mode=dns: 노드 주소의 A/AAAA 레코드.
//   - 와일드카드 호스트는 레코드로만 둔다.
// Aven이 만든 것(comment/description이 "aven:managed <설치 ID>")만 바꾸고 지운다. 같은 이름의 다른 레코드나 로드밸런서가 있으면
// 그 호스트는 건드리지 않고 상태에 conflict로 보인다. Cloudflare 터널 노드와 공인 주소가 없는 노드는 넣지 않는다.

// CloudflareHost는 호스트 하나의 동기화 결과다.
type CloudflareHost struct {
	Host   string   `json:"host"`
	Zone   string   `json:"zone"`
	Mode   string   `json:"mode"` // load_balancer | dns | skipped
	Nodes  []string `json:"nodes"`
	Reason string   `json:"reason,omitempty"` // skipped: conflict | no_public_node
	Detail string   `json:"detail,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// CloudflareZone은 관리하는 존이다.
type CloudflareZone struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Account       string `json:"account"`
	Plan          string `json:"plan"`
	Records       int    `json:"records"`        // Aven이 만든 레코드 수
	LoadBalancers int    `json:"load_balancers"` // Aven이 만든 로드밸런서 수
}

// CloudflarePool은 노드의 pool이다.
type CloudflarePool struct {
	NodeID    string   `json:"node_id"`
	Name      string   `json:"name"`
	ID        string   `json:"id"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// CloudflareStatus는 GET /api/owner/cloudflare의 상태 부분이다.
type CloudflareStatus struct {
	Enabled  bool       `json:"enabled"`
	Syncing  bool       `json:"syncing"`
	LastSync *time.Time `json:"last_sync"`
	Error    string     `json:"error,omitempty"`
	// LoadBalancing: "" (확인 전·쓰지 않음) | available | unavailable(구독이나 권한 없음, LoadBalancingError가 이유).
	LoadBalancing      string           `json:"load_balancing"`
	LoadBalancingError string           `json:"load_balancing_error,omitempty"`
	Changes            int              `json:"changes"` // 마지막 동기화에서 만들고 바꾸고 지운 수
	Zones              []CloudflareZone `json:"zones"`
	Hosts              []CloudflareHost `json:"hosts"`
	Pools              []CloudflarePool `json:"pools"`
}

type cloudflareState struct {
	mu     sync.Mutex
	hosts  map[string][]string // 마지막 syncProxy의 호스트 → 라우트가 있는 노드
	status CloudflareStatus
	kick   coalescer
	syncMu sync.Mutex
	// apiBase는 테스트용 Cloudflare API 주소다(비우면 실제 API). noAsync면 SyncCloudflareAsync가 아무것도 하지 않는다(테스트가 직접 부른다).
	apiBase string
	noAsync bool
}

// cloudflareRoutes는 syncProxy가 만든 라우트에서 호스트별 노드를 기억하고 백그라운드 동기화를 건다.
func (m *Manager) cloudflareRoutes(routes map[string][]proxy.Route) {
	hosts := map[string][]string{}
	for nk, rs := range routes {
		for _, r := range rs {
			for _, h := range r.Hosts {
				h = strings.ToLower(strings.TrimSuffix(h, "."))
				if !slices.Contains(hosts[h], nk) {
					hosts[h] = append(hosts[h], nk)
				}
			}
		}
	}
	for _, ids := range hosts {
		sort.Strings(ids)
	}
	st := &m.cfSt
	st.mu.Lock()
	changed := fmt.Sprint(st.hosts) != fmt.Sprint(hosts)
	st.hosts = hosts
	st.mu.Unlock()
	if changed {
		m.SyncCloudflareAsync()
	}
}

// SyncCloudflareAsync는 Cloudflare 동기화를 백그라운드에서 돌린다(돌고 있으면 끝난 뒤 한 번 더).
func (m *Manager) SyncCloudflareAsync() {
	if m == nil || m.Store == nil || m.cfSt.noAsync {
		return
	}
	m.cfSt.kick.Trigger(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := m.SyncCloudflare(ctx); err != nil && m.Log != nil {
			m.Log.Warn("cloudflare sync", "err", err)
		}
	})
}

// CloudflareLoop는 every마다 동기화한다(노드 연결 상태와 Cloudflare 쪽 변경을 따라간다).
func (m *Manager) CloudflareLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.SyncCloudflareAsync()
		}
	}
}

// CloudflareStatus는 마지막 동기화 상태다.
func (m *Manager) CloudflareStatus() CloudflareStatus {
	st := &m.cfSt
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.status
	s.Zones = append([]CloudflareZone{}, s.Zones...)
	s.Hosts = append([]CloudflareHost{}, s.Hosts...)
	s.Pools = append([]CloudflarePool{}, s.Pools...)
	return s
}

func (m *Manager) setCloudflareStatus(f func(s *CloudflareStatus)) {
	m.cfSt.mu.Lock()
	f(&m.cfSt.status)
	m.cfSt.mu.Unlock()
}

// CloudflareClient는 저장된 토큰(또는 token)으로 만든 API 클라이언트다.
func (m *Manager) CloudflareClient(token string) *cloudflare.Client {
	base := m.cfSt.apiBase
	if base == "" {
		base = m.Cfg.CloudflareAPI
	}
	return &cloudflare.Client{Token: token, BaseURL: base}
}

const cloudflareInstanceKey = "cloudflare_instance"

// cloudflareMarker는 이 설치가 만든 Cloudflare 리소스의 표시다("aven:managed <설치 ID>"). 같은 계정을 쓰는 다른 Aven 설치와 섞이지 않는다.
func (m *Manager) cloudflareMarker(ctx context.Context) (string, string, error) {
	id, err := m.Store.GetSetting(ctx, cloudflareInstanceKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && id == "") {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			return "", "", err
		}
		id = hex.EncodeToString(b)
		if err := m.Store.SetSetting(ctx, cloudflareInstanceKey, id); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	return cloudflare.ManagedMarker + " " + id, id, nil
}

// parseGeo는 노드 라벨 geo "<위도>,<경도>"다(proximity steering의 pool 좌표).
func parseGeo(s string) (lat, lon float64, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(s), ",")
	if !found {
		return 0, 0, false
	}
	la, e1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lo, e2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if e1 != nil || e2 != nil || math.Abs(la) > 90 || math.Abs(lo) > 180 {
		return 0, 0, false
	}
	return la, lo, true
}

// ValidGeoLabel은 노드 라벨 geo 값이 맞는지다(비면 맞다).
func ValidGeoLabel(s string) bool {
	if strings.TrimSpace(s) == "" {
		return true
	}
	_, _, ok := parseGeo(s)
	return ok
}

// zoneFor는 host가 들어가는 가장 긴 존이다.
func zoneFor(host string, zones []cloudflare.Zone) (cloudflare.Zone, bool) {
	h := strings.TrimPrefix(host, "*.")
	var best cloudflare.Zone
	found := false
	for _, z := range zones {
		n := strings.ToLower(z.Name)
		if (h == n || strings.HasSuffix(h, "."+n)) && len(n) > len(best.Name) {
			best, found = z, true
		}
	}
	return best, found
}

type cfPlan struct {
	host  string
	zone  cloudflare.Zone
	nodes []publicNode // 서비스하는 공개 노드(ID 순)
	mode  string       // load_balancer | dns | skipped
	res   *CloudflareHost
}

// SyncCloudflare는 Cloudflare를 지금 라우트에 맞춘다. 꺼져 있으면 아무것도 하지 않는다(만든 리소스도 그대로 둔다).
func (m *Manager) SyncCloudflare(ctx context.Context) error {
	st := &m.cfSt
	st.syncMu.Lock()
	defer st.syncMu.Unlock()
	cfg, err := m.Store.CloudflareSettings(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled || cfg.Token == "" {
		m.setCloudflareStatus(func(s *CloudflareStatus) { *s = CloudflareStatus{} })
		return nil
	}
	m.setCloudflareStatus(func(s *CloudflareStatus) { s.Enabled, s.Syncing = true, true })
	res, err := m.syncCloudflare(ctx, cfg)
	now := time.Now().UTC()
	m.setCloudflareStatus(func(s *CloudflareStatus) {
		*s = res
		s.Enabled, s.Syncing, s.LastSync = true, false, &now
		if err != nil {
			s.Error = err.Error()
		}
	})
	return err
}

func (m *Manager) syncCloudflare(ctx context.Context, cfg store.CloudflareSettings) (CloudflareStatus, error) {
	out := CloudflareStatus{Zones: []CloudflareZone{}, Hosts: []CloudflareHost{}, Pools: []CloudflarePool{}}
	marker, inst, err := m.cloudflareMarker(ctx)
	if err != nil {
		return out, err
	}
	c := m.CloudflareClient(cfg.Token)
	all, err := c.Zones(ctx)
	if err != nil {
		return out, err
	}
	var zones []cloudflare.Zone
	for _, z := range all {
		z.Name = strings.ToLower(z.Name)
		if z.Status != "" && z.Status != "active" {
			continue
		}
		if len(cfg.Zones) > 0 && !slices.ContainsFunc(cfg.Zones, func(n string) bool { return strings.EqualFold(strings.TrimSuffix(n, "."), z.Name) }) {
			continue
		}
		zones = append(zones, z)
	}
	if len(zones) == 0 {
		return out, errors.New("the token sees no active zone to manage (check the token's zone permissions and the zone list)")
	}
	nodes := map[string]publicNode{}
	for _, n := range m.publicNodes(ctx) {
		nodes[n.ID] = n
	}
	m.cfSt.mu.Lock()
	hostNodes := m.cfSt.hosts
	m.cfSt.mu.Unlock()

	// 계정별 Load Balancing 사용 가능 여부(pool 목록을 읽을 수 있는지)
	accounts := map[string][]cloudflare.Pool{}
	lbOK := map[string]error{}
	for _, z := range zones {
		a := z.Account.ID
		if _, seen := lbOK[a]; seen {
			continue
		}
		pools, err := c.Pools(ctx, a)
		lbOK[a] = err
		if err == nil {
			accounts[a] = pools
		}
	}
	for _, err := range lbOK {
		if err == nil {
			out.LoadBalancing = "available"
		} else if out.LoadBalancing == "" {
			out.LoadBalancing, out.LoadBalancingError = "unavailable", err.Error()
		}
	}

	// 호스트별 계획
	var plans []*cfPlan
	names := make([]string, 0, len(hostNodes))
	for h := range hostNodes {
		names = append(names, h)
	}
	sort.Strings(names)
	for _, h := range names {
		z, ok := zoneFor(h, zones)
		if !ok {
			continue
		}
		p := &cfPlan{host: h, zone: z, res: &CloudflareHost{Host: h, Zone: z.Name, Nodes: []string{}}}
		for _, id := range hostNodes[h] {
			if n, ok := nodes[id]; ok && n.Public() {
				p.nodes = append(p.nodes, n)
				p.res.Nodes = append(p.res.Nodes, id)
			}
		}
		switch {
		case len(p.nodes) == 0:
			p.mode, p.res.Reason = "skipped", "no_public_node"
		case len(p.nodes) >= 2 && !strings.HasPrefix(h, "*.") && cfg.Mode != "dns":
			p.mode = "load_balancer"
			if lbOK[z.Account.ID] != nil {
				if cfg.Mode == "load_balancer" {
					p.mode, p.res.Error = "skipped", "Load Balancing is not available: "+lbOK[z.Account.ID].Error()
				} else {
					p.mode = "dns" // auto: 구독이 없으면 레코드
				}
			}
		default:
			p.mode = "dns"
		}
		plans = append(plans, p)
	}

	changes := 0
	var errs []error
	note := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// 계정별 monitor와 pool(로드밸런서를 쓰는 노드만)
	poolID := map[string]map[string]string{} // account → node → pool id
	for a, pools := range accounts {
		need := map[string]publicNode{}
		for _, p := range plans {
			if p.mode == "load_balancer" && p.zone.Account.ID == a {
				for _, n := range p.nodes {
					need[n.ID] = n
				}
			}
		}
		ids, ch, err := m.cfEnsurePools(ctx, c, a, marker, inst, pools, need, &out)
		changes += ch
		poolID[a] = ids
		if err != nil {
			note(err)
			for _, p := range plans {
				if p.mode == "load_balancer" && p.zone.Account.ID == a {
					p.mode, p.res.Error = "skipped", err.Error()
				}
			}
		}
	}

	// 존별 레코드와 로드밸런서
	for _, z := range zones {
		zs := CloudflareZone{ID: z.ID, Name: z.Name, Account: z.Account.Name, Plan: z.Plan.Name}
		ch, err := m.cfSyncZone(ctx, c, cfg, z, marker, plans, poolID[z.Account.ID], lbOK[z.Account.ID] == nil, &zs)
		changes += ch
		note(err)
		out.Zones = append(out.Zones, zs)
	}

	// 쓰지 않게 된 pool과 monitor(로드밸런서를 지운 뒤에)
	for a, pools := range accounts {
		ch, err := m.cfCleanupPools(ctx, c, a, marker, inst, pools, poolID[a])
		changes += ch
		note(err)
	}
	for _, p := range plans {
		p.res.Mode = p.mode
		out.Hosts = append(out.Hosts, *p.res)
	}
	sort.Slice(out.Pools, func(i, j int) bool { return out.Pools[i].NodeID < out.Pools[j].NodeID })
	out.Changes = changes
	return out, errors.Join(errs...)
}

func cfPoolName(inst, nodeID string) string { return "aven-" + inst + "-" + nodeID }

// cfEnsurePools는 need의 노드마다 pool을 맞추고(없으면 만들고), monitor를 하나 둔다. 노드 → pool ID를 돌려준다.
func (m *Manager) cfEnsurePools(ctx context.Context, c *cloudflare.Client, account, marker, inst string, pools []cloudflare.Pool, need map[string]publicNode, out *CloudflareStatus) (map[string]string, int, error) {
	ids := map[string]string{}
	if len(need) == 0 {
		return ids, 0, nil
	}
	changes := 0
	monitors, err := c.Monitors(ctx, account)
	if err != nil {
		return ids, 0, fmt.Errorf("cloudflare monitors: %w", err)
	}
	monitorID := ""
	for _, mo := range monitors {
		if mo.Description == marker {
			monitorID = mo.ID
		}
	}
	if monitorID == "" {
		mo, err := c.CreateMonitor(ctx, account, cloudflare.Monitor{Type: "tcp", Port: 443, Interval: 60, Retries: 2, Timeout: 5, Description: marker})
		if err != nil {
			return ids, 0, fmt.Errorf("cloudflare monitor: %w", err)
		}
		monitorID, changes = mo.ID, changes+1
	}
	byName := map[string]cloudflare.Pool{}
	for _, p := range pools {
		byName[p.Name] = p
	}
	order := make([]string, 0, len(need))
	for id := range need {
		order = append(order, id)
	}
	sort.Strings(order)
	var errs []error
	for _, id := range order {
		n := need[id]
		want := cloudflare.Pool{Name: cfPoolName(inst, id), Description: marker + " node " + id, Enabled: true, MinimumOrigins: 1, Monitor: monitorID}
		if n.IPv4 != "" {
			want.Origins = append(want.Origins, cloudflare.Origin{Name: "ipv4", Address: n.IPv4, Enabled: true, Weight: 1})
		}
		if n.IPv6 != "" {
			want.Origins = append(want.Origins, cloudflare.Origin{Name: "ipv6", Address: n.IPv6, Enabled: true, Weight: 1})
		}
		if la, lo, ok := parseGeo(n.Labels["geo"]); ok {
			want.Latitude, want.Longitude = &la, &lo
		}
		cur, exists := byName[want.Name]
		if exists && !cloudflare.IsManaged(cur.Description) {
			errs = append(errs, fmt.Errorf("cloudflare pool %s exists and was not created by this Aven; rename it", want.Name))
			continue
		}
		var got cloudflare.Pool
		switch {
		case !exists:
			got, err = c.CreatePool(ctx, account, want)
			changes++
		case !samePool(cur, want):
			want.ID = cur.ID
			got, err = c.UpdatePool(ctx, account, want)
			changes++
		default:
			got = cur
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("cloudflare pool %s: %w", want.Name, err))
			continue
		}
		ids[id] = got.ID
		out.Pools = append(out.Pools, CloudflarePool{NodeID: id, Name: want.Name, ID: got.ID, Latitude: want.Latitude, Longitude: want.Longitude})
	}
	return ids, changes, errors.Join(errs...)
}

func samePool(a, b cloudflare.Pool) bool {
	eqf := func(x, y *float64) bool { return (x == nil) == (y == nil) && (x == nil || math.Abs(*x-*y) < 1e-6) }
	if a.Description != b.Description || !a.Enabled || a.Monitor != b.Monitor || a.MinimumOrigins != b.MinimumOrigins || !eqf(a.Latitude, b.Latitude) || !eqf(a.Longitude, b.Longitude) || len(a.Origins) != len(b.Origins) {
		return false
	}
	for i := range a.Origins {
		if a.Origins[i].Name != b.Origins[i].Name || a.Origins[i].Address != b.Origins[i].Address || !a.Origins[i].Enabled {
			return false
		}
	}
	return true
}

// cfCleanupPools는 이 설치가 만든 pool 중 지금 쓰지 않는 것과(pool이 남지 않으면) monitor를 지운다.
func (m *Manager) cfCleanupPools(ctx context.Context, c *cloudflare.Client, account, marker, inst string, pools []cloudflare.Pool, keep map[string]string) (int, error) {
	changes := 0
	var errs []error
	kept := map[string]bool{}
	for _, id := range keep {
		kept[id] = true
	}
	for _, p := range pools {
		if !strings.HasPrefix(p.Description, marker+" ") || kept[p.ID] {
			continue
		}
		if err := c.DeletePool(ctx, account, p.ID); err != nil {
			errs = append(errs, fmt.Errorf("cloudflare pool %s: %w", p.Name, err))
			continue
		}
		changes++
	}
	if len(keep) == 0 {
		monitors, err := c.Monitors(ctx, account)
		if err == nil {
			for _, mo := range monitors {
				if mo.Description == marker {
					if err := c.DeleteMonitor(ctx, account, mo.ID); err != nil {
						errs = append(errs, fmt.Errorf("cloudflare monitor: %w", err))
					} else {
						changes++
					}
				}
			}
		}
	}
	return changes, errors.Join(errs...)
}

func isAddrRecord(t string) bool { return t == "A" || t == "AAAA" || t == "CNAME" }

// cfSyncZone은 존 하나의 레코드와 로드밸런서를 계획에 맞춘다.
func (m *Manager) cfSyncZone(ctx context.Context, c *cloudflare.Client, cfg store.CloudflareSettings, z cloudflare.Zone, marker string, plans []*cfPlan, pools map[string]string, lbAvailable bool, zs *CloudflareZone) (int, error) {
	records, err := c.DNSRecords(ctx, z.ID)
	if err != nil {
		for _, p := range plans {
			if p.zone.ID == z.ID && p.mode != "skipped" {
				p.mode, p.res.Error = "skipped", err.Error()
			}
		}
		return 0, fmt.Errorf("cloudflare %s records: %w", z.Name, err)
	}
	var lbs []cloudflare.LoadBalancer
	if lbAvailable {
		if lbs, err = c.LoadBalancers(ctx, z.ID); err != nil {
			lbAvailable = false
		}
	}
	recByName := map[string][]cloudflare.DNSRecord{}
	for _, r := range records {
		n := strings.ToLower(r.Name)
		recByName[n] = append(recByName[n], r)
	}
	lbByName := map[string]cloudflare.LoadBalancer{}
	for _, lb := range lbs {
		lbByName[strings.ToLower(lb.Name)] = lb
	}
	changes := 0
	var errs []error
	desired := map[string]bool{}
	ttl := 60
	if cfg.Proxied {
		ttl = 1 // 자동
	}
	for _, p := range plans {
		if p.zone.ID != z.ID || p.mode == "skipped" {
			continue
		}
		var foreign []string
		for _, r := range recByName[p.host] {
			if isAddrRecord(r.Type) && r.Comment != marker {
				foreign = append(foreign, r.Type+" "+r.Content)
			}
		}
		if lb, ok := lbByName[p.host]; ok && lb.Description != marker {
			foreign = append(foreign, "load balancer")
		}
		if len(foreign) > 0 {
			p.mode, p.res.Reason, p.res.Detail = "skipped", "conflict", strings.Join(foreign, ", ")
			continue // 우리 것이 아닌 레코드가 있다: 이 이름은 건드리지 않는다(남은 우리 레코드도 지우지 않는다)
		}
		desired[p.host] = true
		var perr error
		var ch int
		if p.mode == "load_balancer" {
			ch, perr = cfApplyLB(ctx, c, z, marker, cfg.Proxied, p, pools, lbByName[p.host], recByName[p.host])
		} else {
			ch, perr = cfApplyRecords(ctx, c, z, marker, cfg.Proxied, ttl, p, recByName[p.host], lbByName[p.host])
		}
		changes += ch
		if perr != nil {
			p.res.Error = perr.Error()
			errs = append(errs, perr)
		}
	}
	// 더는 원하지 않는 우리 레코드·로드밸런서(충돌로 건너뛴 이름은 desired에 없지만 그대로 둔다)
	skipped := map[string]bool{}
	for _, p := range plans {
		if p.zone.ID == z.ID && p.mode == "skipped" {
			skipped[p.host] = true
		}
	}
	for _, r := range records {
		n := strings.ToLower(r.Name)
		if r.Comment != marker || desired[n] || skipped[n] {
			continue
		}
		if err := c.DeleteDNSRecord(ctx, z.ID, r.ID); err != nil {
			errs = append(errs, fmt.Errorf("cloudflare %s: delete %s %s: %w", z.Name, r.Type, r.Name, err))
		} else {
			changes++
		}
	}
	for _, lb := range lbs {
		n := strings.ToLower(lb.Name)
		if lb.Description != marker || desired[n] || skipped[n] {
			continue
		}
		if err := c.DeleteLoadBalancer(ctx, z.ID, lb.ID); err != nil {
			errs = append(errs, fmt.Errorf("cloudflare %s: delete load balancer %s: %w", z.Name, lb.Name, err))
		} else {
			changes++
		}
	}
	// 존 요약(동기화 뒤 우리 것)
	for _, p := range plans {
		if p.zone.ID == z.ID && desired[p.host] && p.res.Error == "" {
			if p.mode == "load_balancer" {
				zs.LoadBalancers++
			} else {
				zs.Records += len(cfWantRecords(p))
			}
		}
	}
	return changes, errors.Join(errs...)
}

// cfWantRecords는 레코드 모드 호스트의 A/AAAA다: 연결된 노드(없으면 전부)의 주소.
func cfWantRecords(p *cfPlan) []cloudflare.DNSRecord {
	use := []publicNode{}
	for _, n := range p.nodes {
		if n.Up {
			use = append(use, n)
		}
	}
	if len(use) == 0 {
		use = p.nodes
	}
	var out []cloudflare.DNSRecord
	seen := map[string]bool{}
	for _, n := range use {
		for _, r := range []cloudflare.DNSRecord{{Type: "A", Content: n.IPv4}, {Type: "AAAA", Content: n.IPv6}} {
			if r.Content != "" && !seen[r.Type+r.Content] {
				seen[r.Type+r.Content] = true
				r.Name = p.host
				out = append(out, r)
			}
		}
	}
	return out
}

func cfApplyRecords(ctx context.Context, c *cloudflare.Client, z cloudflare.Zone, marker string, proxied bool, ttl int, p *cfPlan, existing []cloudflare.DNSRecord, lb cloudflare.LoadBalancer) (int, error) {
	changes := 0
	var errs []error
	if lb.ID != "" && lb.Description == marker {
		if err := c.DeleteLoadBalancer(ctx, z.ID, lb.ID); err != nil {
			errs = append(errs, err)
		} else {
			changes++
		}
	}
	want := cfWantRecords(p)
	have := map[string]cloudflare.DNSRecord{}
	for _, r := range existing {
		if r.Comment == marker {
			have[r.Type+" "+r.Content] = r
		}
	}
	for _, w := range want {
		w.Proxied, w.TTL, w.Comment = proxied, ttl, marker
		k := w.Type + " " + w.Content
		cur, ok := have[k]
		delete(have, k)
		var err error
		switch {
		case !ok:
			_, err = c.CreateDNSRecord(ctx, z.ID, w)
			changes++
		case cur.Proxied != proxied || cur.TTL != ttl:
			w.ID = cur.ID
			_, err = c.UpdateDNSRecord(ctx, z.ID, w)
			changes++
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s %s: %w", p.host, w.Type, w.Content, err))
		}
	}
	for _, r := range have {
		if err := c.DeleteDNSRecord(ctx, z.ID, r.ID); err != nil {
			errs = append(errs, err)
		} else {
			changes++
		}
	}
	return changes, errors.Join(errs...)
}

func cfApplyLB(ctx context.Context, c *cloudflare.Client, z cloudflare.Zone, marker string, proxied bool, p *cfPlan, pools map[string]string, cur cloudflare.LoadBalancer, existing []cloudflare.DNSRecord) (int, error) {
	var ids []string
	allGeo := true
	for _, n := range p.nodes {
		id, ok := pools[n.ID]
		if !ok {
			return 0, fmt.Errorf("%s: no pool for node %s", p.host, n.ID)
		}
		ids = append(ids, id)
		if _, _, ok := parseGeo(n.Labels["geo"]); !ok {
			allGeo = false
		}
	}
	steering := "dynamic_latency" // health monitor의 RTT로 고른다
	if allGeo {
		steering = "proximity"
	}
	want := cloudflare.LoadBalancer{Name: p.host, Description: marker, DefaultPools: ids, FallbackPool: ids[0], Proxied: proxied, SteeringPolicy: steering}
	if !proxied {
		want.TTL = 30
	}
	changes := 0
	var err error
	switch {
	case cur.ID == "":
		_, err = c.CreateLoadBalancer(ctx, z.ID, want)
		changes++
	case !slices.Equal(cur.DefaultPools, want.DefaultPools) || cur.FallbackPool != want.FallbackPool || cur.Proxied != proxied || cur.SteeringPolicy != steering || (!proxied && cur.TTL != want.TTL):
		want.ID = cur.ID
		_, err = c.UpdateLoadBalancer(ctx, z.ID, want)
		changes++
	}
	if err != nil {
		return changes, fmt.Errorf("%s: load balancer: %w", p.host, err)
	}
	// 로드밸런서로 옮긴 호스트의 우리 레코드
	var errs []error
	for _, r := range existing {
		if r.Comment == marker {
			if err := c.DeleteDNSRecord(ctx, z.ID, r.ID); err != nil {
				errs = append(errs, err)
			} else {
				changes++
			}
		}
	}
	return changes, errors.Join(errs...)
}

// RemoveCloudflareResources는 이 설치가 Cloudflare에 만든 레코드, 로드밸런서, pool, monitor를 모두 지운다(연동을 끈 뒤 정리용).
func (m *Manager) RemoveCloudflareResources(ctx context.Context) (int, error) {
	m.cfSt.syncMu.Lock()
	defer m.cfSt.syncMu.Unlock()
	cfg, err := m.Store.CloudflareSettings(ctx)
	if err != nil {
		return 0, err
	}
	if cfg.Token == "" {
		return 0, errors.New("no Cloudflare API token is saved")
	}
	marker, _, err := m.cloudflareMarker(ctx)
	if err != nil {
		return 0, err
	}
	c := m.CloudflareClient(cfg.Token)
	zones, err := c.Zones(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	accounts := map[string]bool{}
	for _, z := range zones {
		accounts[z.Account.ID] = true
		if lbs, err := c.LoadBalancers(ctx, z.ID); err == nil {
			for _, lb := range lbs {
				if lb.Description == marker {
					if err := c.DeleteLoadBalancer(ctx, z.ID, lb.ID); err != nil {
						errs = append(errs, err)
					} else {
						n++
					}
				}
			}
		}
		recs, err := c.DNSRecords(ctx, z.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range recs {
			if r.Comment == marker {
				if err := c.DeleteDNSRecord(ctx, z.ID, r.ID); err != nil {
					errs = append(errs, err)
				} else {
					n++
				}
			}
		}
	}
	for a := range accounts {
		pools, err := c.Pools(ctx, a)
		if err != nil {
			continue
		}
		ch, err := m.cfCleanupPools(ctx, c, a, marker, "", pools, nil)
		n += ch
		if err != nil {
			errs = append(errs, err)
		}
	}
	m.setCloudflareStatus(func(s *CloudflareStatus) {
		s.Zones, s.Hosts, s.Pools = []CloudflareZone{}, []CloudflareHost{}, []CloudflarePool{}
	})
	return n, errors.Join(errs...)
}
