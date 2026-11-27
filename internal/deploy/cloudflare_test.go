package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/cloudflare"
	"github.com/turin-dev/aven/internal/cloudflare/cftest"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

const cfToken = "test-token-0123456789"

// cfFixture: 로컬 노드(203.0.113.1, 2001:db8::1)와 원격 노드(198.51.100.7)에 replica가 있는 web.example.com.
func cfFixture(t *testing.T, mode string) (*scaleFixture, *cftest.Server) {
	t.Helper()
	ctx := context.Background()
	f := newScaleFixture(t)
	f.m.Cfg.PublicIPv4, f.m.Cfg.PublicIPv6 = "203.0.113.1", "2001:db8::1"
	v4 := "198.51.100.7"
	if err := f.st.SetNodeInfo(ctx, f.rid, store.NodeAddress{IPv4: &v4}, store.NodeInfo{}); err != nil {
		t.Fatal(err)
	}
	cf := cftest.New(cfToken)
	t.Cleanup(cf.Close)
	f.m.cfSt.apiBase, f.m.cfSt.noAsync = cf.URL, true
	cfg := store.DefaultCloudflareSettings()
	cfg.Enabled, cfg.Token, cfg.Mode, cfg.Proxied = true, cfToken, mode, true
	cfg.Zones = []string{"example.com"}
	if err := f.st.SetCloudflareSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Domains = []store.Domain{{Host: "web.example.com", Kind: "custom"}}
	if err := f.st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordDomainCheck(ctx, "web.example.com", in.ID, store.DomainCheck{DNSStatus: store.DNSPendingDNS, VerifyMethod: store.VerifyTXT, CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	f.in = in
	f.live(t)
	if err := f.m.SyncProxy(ctx); err != nil {
		t.Fatal(err)
	}
	return f, cf
}

func hostStatus(t *testing.T, m *Manager, host string) CloudflareHost {
	t.Helper()
	for _, h := range m.CloudflareStatus().Hosts {
		if h.Host == host {
			return h
		}
	}
	t.Fatalf("no status for %s: %+v", host, m.CloudflareStatus())
	return CloudflareHost{}
}

func TestCloudflareLoadBalancer(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "auto")
	rn, _ := f.st.GetNode(ctx, f.rid)
	rn.Labels = map[string]string{"geo": "37.57,126.98"}
	f.st.UpdateNode(ctx, rn)
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	h := hostStatus(t, f.m, "web.example.com")
	if h.Mode != "load_balancer" || len(h.Nodes) != 2 || h.Error != "" {
		t.Fatalf("host %+v", h)
	}
	cf.Mu.Lock()
	if len(cf.LBs["z1"]) != 1 || len(cf.Pools) != 2 || len(cf.Monitors) != 1 || len(cf.Records["z1"]) != 0 {
		t.Fatalf("lbs=%+v pools=%d monitors=%d records=%+v", cf.LBs, len(cf.Pools), len(cf.Monitors), cf.Records)
	}
	lb := cf.LBs["z1"][0]
	// 로컬 노드에 geo 라벨이 없다: proximity 대신 latency
	if lb.Name != "web.example.com" || !lb.Proxied || lb.SteeringPolicy != "dynamic_latency" || len(lb.DefaultPools) != 2 || !cloudflare.IsManaged(lb.Description) {
		t.Fatalf("lb %+v", lb)
	}
	var remotePool cloudflare.Pool
	for _, p := range cf.Pools {
		if strings.HasSuffix(p.Name, f.rid) {
			remotePool = p
		}
	}
	if remotePool.Latitude == nil || *remotePool.Latitude != 37.57 || len(remotePool.Origins) != 1 || remotePool.Origins[0].Address != "198.51.100.7" {
		t.Fatalf("remote pool %+v", remotePool)
	}
	cf.Writes = nil
	cf.Mu.Unlock()

	// 바뀐 것이 없으면 쓰지 않는다
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	cf.Mu.Lock()
	if len(cf.Writes) != 0 {
		t.Fatalf("idempotent sync wrote: %v", cf.Writes)
	}
	cf.Mu.Unlock()

	// 두 노드 모두 위치가 있으면 proximity
	ln, _ := f.st.LocalNode(ctx)
	ln.Labels = map[string]string{"geo": "50.11,8.68"}
	f.st.UpdateNode(ctx, ln)
	f.m.SyncCloudflare(ctx)
	cf.Mu.Lock()
	if cf.LBs["z1"][0].SteeringPolicy != "proximity" {
		t.Fatalf("steering %q", cf.LBs["z1"][0].SteeringPolicy)
	}
	cf.Mu.Unlock()

	// 호스트가 사라지면 로드밸런서, pool, monitor를 지운다
	f.m.cloudflareRoutes(nil)
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	cf.Mu.Lock()
	defer cf.Mu.Unlock()
	if len(cf.LBs["z1"]) != 0 || len(cf.Pools) != 0 || len(cf.Monitors) != 0 {
		t.Fatalf("not cleaned: lbs=%v pools=%v monitors=%v", cf.LBs, cf.Pools, cf.Monitors)
	}
}

func TestCloudflareFallsBackToRecords(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "auto")
	cf.NoLB = true
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	st := f.m.CloudflareStatus()
	if st.LoadBalancing != "unavailable" || st.LoadBalancingError == "" {
		t.Fatalf("status %+v", st)
	}
	if h := hostStatus(t, f.m, "web.example.com"); h.Mode != "dns" {
		t.Fatalf("host %+v", h)
	}
	cf.Mu.Lock()
	var got []string
	for _, r := range cf.Records["z1"] {
		got = append(got, r.Type+" "+r.Content)
		if !r.Proxied || r.TTL != 1 || !cloudflare.IsManaged(r.Comment) {
			t.Fatalf("record %+v", r)
		}
	}
	cf.Mu.Unlock()
	slices.Sort(got)
	if !slices.Equal(got, []string{"A 198.51.100.7", "A 203.0.113.1", "AAAA 2001:db8::1"}) {
		t.Fatalf("records %v", got)
	}
	// 원격 노드가 끊기면 그 주소를 뺀다
	delete(f.m.Agents.(*nodesHub).nodes, f.rid)
	f.m.SyncCloudflare(ctx)
	cf.Mu.Lock()
	for _, r := range cf.Records["z1"] {
		if r.Content == "198.51.100.7" {
			t.Fatalf("offline node still in records: %+v", cf.Records["z1"])
		}
	}
	cf.Mu.Unlock()

	// mode=load_balancer인데 쓸 수 없으면 건너뛰고 오류를 보인다
	cfg, _ := f.st.CloudflareSettings(ctx)
	cfg.Mode = "load_balancer"
	f.st.SetCloudflareSettings(ctx, cfg)
	f.m.Agents.(*nodesHub).nodes[f.rid] = f.remote
	f.m.SyncCloudflare(ctx)
	if h := hostStatus(t, f.m, "web.example.com"); h.Mode != "skipped" || !strings.Contains(h.Error, "Load Balancing") {
		t.Fatalf("host %+v", h)
	}
}

func TestCloudflareConflictAndCleanup(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "dns")
	cf.Mu.Lock()
	cf.Records["z1"] = []cloudflare.DNSRecord{{ID: "user1", Type: "CNAME", Name: "web.example.com", Content: "elsewhere.example.net"},
		{ID: "user2", Type: "A", Name: "other.example.com", Content: "192.0.2.1"}}
	cf.Mu.Unlock()
	f.m.SyncCloudflare(ctx)
	if h := hostStatus(t, f.m, "web.example.com"); h.Mode != "skipped" || h.Reason != "conflict" || !strings.Contains(h.Detail, "CNAME") {
		t.Fatalf("host %+v", h)
	}
	cf.Mu.Lock()
	if len(cf.Records["z1"]) != 2 {
		t.Fatalf("user records touched: %+v", cf.Records["z1"])
	}
	cf.Records["z1"] = cf.Records["z1"][1:] // 사용자가 CNAME을 지웠다
	cf.Mu.Unlock()
	f.m.SyncCloudflare(ctx)
	if h := hostStatus(t, f.m, "web.example.com"); h.Mode != "dns" || h.Error != "" {
		t.Fatalf("host %+v", h)
	}
	cf.Mu.Lock()
	if len(cf.Records["z1"]) != 4 {
		t.Fatalf("records %+v", cf.Records["z1"])
	}
	cf.Mu.Unlock()
	// 연동을 끄면 동기화는 아무것도 하지 않고, 정리는 Aven 레코드만 지운다
	cfg, _ := f.st.CloudflareSettings(ctx)
	cfg.Enabled = false
	f.st.SetCloudflareSettings(ctx, cfg)
	f.m.SyncCloudflare(ctx)
	if st := f.m.CloudflareStatus(); st.Enabled {
		t.Fatalf("status %+v", st)
	}
	n, err := f.m.RemoveCloudflareResources(ctx)
	if err != nil || n != 3 {
		t.Fatalf("cleanup %d %v", n, err)
	}
	cf.Mu.Lock()
	defer cf.Mu.Unlock()
	if len(cf.Records["z1"]) != 1 || cf.Records["z1"][0].ID != "user2" {
		t.Fatalf("after cleanup %+v", cf.Records["z1"])
	}
}

func TestCloudflareSingleNodeAndZones(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "auto")
	// 다른 Aven 설치가 만든 레코드(표시의 설치 ID가 다르다)는 남의 것으로 본다
	cf.Mu.Lock()
	cf.Records["z1"] = []cloudflare.DNSRecord{{ID: "x1", Type: "A", Name: "gone.example.com", Content: "192.0.2.9", Comment: cloudflare.ManagedMarker + " otherinst"}}
	cf.Mu.Unlock()
	claimHost(t, f, "solo.example.com", "generated", "")
	f.m.cloudflareRoutes(map[string][]proxy.Route{store.LocalNodeID: {{Hosts: []string{"solo.example.com", "*.pr.example.com", "out.example.org"}}}})
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, f.m, "solo.example.com"); h.Mode != "dns" {
		t.Fatalf("solo %+v", h)
	}
	for _, h := range f.m.CloudflareStatus().Hosts {
		if h.Host == "*.pr.example.com" {
			t.Fatalf("wildcard synced without the setting: %+v", h)
		}
	}
	if cfHasName(cf, "*.pr.example.com") {
		t.Fatal("wildcard record created")
	}
	for _, h := range f.m.CloudflareStatus().Hosts {
		if h.Host == "out.example.org" {
			t.Fatal("host outside every zone was planned")
		}
	}
	cf.Mu.Lock()
	defer cf.Mu.Unlock()
	if len(cf.LBs["z1"]) != 0 {
		t.Fatal("single-node host got a load balancer")
	}
	found := false
	for _, r := range cf.Records["z1"] {
		if r.ID == "x1" {
			found = true
		}
	}
	if !found {
		t.Fatal("another installation's record was deleted")
	}
}

func cfHasName(cf *cftest.Server, name string) bool {
	cf.Mu.Lock()
	defer cf.Mu.Unlock()
	for _, r := range cf.Records["z1"] {
		if strings.EqualFold(r.Name, name) {
			return true
		}
	}
	for _, lb := range cf.LBs["z1"] {
		if strings.EqualFold(lb.Name, name) {
			return true
		}
	}
	return false
}

func cfDeletes(cf *cftest.Server) []string {
	cf.Mu.Lock()
	defer cf.Mu.Unlock()
	var out []string
	for _, w := range cf.Writes {
		if strings.HasPrefix(w, "DELETE ") {
			out = append(out, w)
		}
	}
	return out
}

func claimHost(t *testing.T, f *scaleFixture, host, kind, method string) {
	t.Helper()
	ctx := context.Background()
	in, err := f.st.GetInstance(ctx, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range in.Domains {
		if strings.EqualFold(in.Domains[i].Host, host) {
			in.Domains[i].Kind = kind
			found = true
		}
	}
	if !found {
		in.Domains = append(in.Domains, store.Domain{Host: host, Kind: kind})
	}
	if err := f.st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.in = in
	if method == "" {
		return
	}
	if err := f.st.RecordDomainCheck(ctx, host, in.ID, store.DomainCheck{DNSStatus: store.DNSPendingDNS, VerifyMethod: method, CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflareSkipsUnverifiedAndWildcard(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "dns")
	cfg, err := f.st.CloudflareSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Zones = []string{"example.com"}
	cfg.Proxied = true
	if err := f.st.SetCloudflareSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	claimHost(t, f, "web.example.com", "custom", store.VerifyTXT)
	claimHost(t, f, "login.example.com", "custom", "")
	claimHost(t, f, "pointed.example.com", "custom", store.VerifyDNS)
	claimHost(t, f, "probed.example.com", "custom", store.VerifyHTTP)
	claimHost(t, f, "token.example.com", "custom", store.VerifyCloudflare)
	claimHost(t, f, "old.example.com", "custom", store.VerifyLegacy)
	claimHost(t, f, "app.example.com", "generated", "")
	claimHost(t, f, "*.pr.example.com", "custom", store.VerifyTXT)
	claimHost(t, f, "*.gen.example.com", "generated", "")
	claimHost(t, f, "*.evil.example.com", "custom", "")
	f.m.Cfg.SelfHost = "dash.example.com"
	hosts := []string{"web.example.com", "login.example.com", "pointed.example.com", "probed.example.com", "token.example.com", "old.example.com", "app.example.com", "*.pr.example.com", "*.gen.example.com", "*.evil.example.com", "dash.example.com"}
	f.m.cloudflareRoutes(map[string][]proxy.Route{store.LocalNodeID: {{Hosts: hosts}}})
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"web.example.com", "token.example.com", "old.example.com", "app.example.com", "dash.example.com"} {
		if !cfHasName(cf, host) {
			t.Fatalf("owned host %s was not synced", host)
		}
	}
	for _, host := range []string{"login.example.com", "pointed.example.com", "probed.example.com", "*.pr.example.com", "*.gen.example.com", "*.evil.example.com"} {
		if cfHasName(cf, host) {
			t.Fatalf("host %s was synced", host)
		}
		for _, h := range f.m.CloudflareStatus().Hosts {
			if h.Host == host && h.Mode != "skipped" {
				t.Fatalf("host %s in status as %s", host, h.Mode)
			}
		}
	}
	cfg.Wildcards = true
	if err := f.st.SetCloudflareSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	if !cfHasName(cf, "*.pr.example.com") || !cfHasName(cf, "*.gen.example.com") {
		t.Fatal("verified or generated wildcard was not synced after it was enabled")
	}
	if cfHasName(cf, "*.evil.example.com") || cfHasName(cf, "login.example.com") || cfHasName(cf, "pointed.example.com") {
		t.Fatal("unverified or DNS-only host was synced after wildcards were enabled")
	}
}

func TestCloudflareSyncSkipsUntilRoutesKnown(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "dns")
	marker, _, err := f.m.cloudflareMarker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.m.cfSt.mu.Lock()
	f.m.cfSt.hosts = nil
	f.m.cfSt.mu.Unlock()
	cf.Mu.Lock()
	cf.Records["z1"] = []cloudflare.DNSRecord{
		{ID: "keep1", Type: "A", Name: "web.example.com", Content: "203.0.113.1", Comment: marker},
		{ID: "keep2", Type: "A", Name: "api.example.com", Content: "203.0.113.1", Comment: marker},
	}
	cf.Writes = nil
	cf.Mu.Unlock()
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	if cfHasName(cf, "web.example.com") == false || cfHasName(cf, "api.example.com") == false {
		t.Fatal("records deleted before the first proxy sync")
	}
	if d := cfDeletes(cf); len(d) != 0 {
		t.Fatalf("deletes before routes were known: %v", d)
	}
	if f.m.CloudflareStatus().Warning != "" {
		t.Fatalf("unknown routes reported as a deletion cap: %s", f.m.CloudflareStatus().Warning)
	}
}

func TestCloudflareDeletionCapAndCleanup(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "dns")
	cfg, _ := f.st.CloudflareSettings(ctx)
	cfg.Zones = []string{"example.com"}
	if err := f.st.SetCloudflareSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	marker, _, err := f.m.cloudflareMarker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(names ...string) {
		t.Helper()
		cf.Mu.Lock()
		defer cf.Mu.Unlock()
		cf.Records["z1"] = nil
		for _, name := range names {
			cf.Records["z1"] = append(cf.Records["z1"], cloudflare.DNSRecord{ID: "seed-" + name, Type: "A", Name: name, Content: "203.0.113.1", Comment: marker})
		}
		cf.Records["z1"] = append(cf.Records["z1"], cloudflare.DNSRecord{ID: "user", Type: "A", Name: "hand.example.com", Content: "192.0.2.9"})
		cf.Writes = nil
	}
	keep := make([]string, 19)
	drop := make([]string, 11)
	for i := range keep {
		keep[i] = strings.ReplaceAll("keep00.example.com", "00", two(i))
		claimHost(t, f, keep[i], "generated", "")
	}
	for i := range drop {
		drop[i] = strings.ReplaceAll("drop00.example.com", "00", two(i))
	}
	seed(append(append([]string{}, keep...), drop...)...)
	routes := map[string][]proxy.Route{store.LocalNodeID: {{Hosts: keep}}}
	f.m.cloudflareRoutes(routes)
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range drop {
		if !cfHasName(cf, name) {
			t.Fatalf("mass deletion removed %s", name)
		}
	}
	if !strings.Contains(f.m.CloudflareStatus().Warning, "skipped deleting") {
		t.Fatalf("status %+v", f.m.CloudflareStatus())
	}
	if d := cfDeletes(cf); len(d) != 0 {
		t.Fatalf("capped sync still deleted: %v", d)
	}

	// 4개 중 1개는 한도를 넘지 않으므로 지운다.
	four := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com"}
	for _, name := range four[:3] {
		claimHost(t, f, name, "generated", "")
	}
	seed(four...)
	f.m.cloudflareRoutes(map[string][]proxy.Route{store.LocalNodeID: {{Hosts: four[:3]}}})
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	if cfHasName(cf, "d.example.com") {
		t.Fatal("a single unwanted record was kept")
	}
	for _, name := range four[:3] {
		if !cfHasName(cf, name) {
			t.Fatalf("kept record %s was deleted", name)
		}
	}
	if f.m.CloudflareStatus().Warning != "" {
		t.Fatalf("small deletion warned: %s", f.m.CloudflareStatus().Warning)
	}

	// 비율 한도: 6개 전부를 지우는 동기화는 멈춘다.
	six := []string{"s0.example.com", "s1.example.com", "s2.example.com", "s3.example.com", "s4.example.com", "s5.example.com"}
	seed(six...)
	f.m.cloudflareRoutes(map[string][]proxy.Route{})
	if err := f.m.SyncCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range six {
		if !cfHasName(cf, name) {
			t.Fatalf("ratio cap removed %s", name)
		}
	}
	if f.m.CloudflareStatus().Warning == "" {
		t.Fatal("ratio cap did not warn")
	}

	n, err := f.m.RemoveCloudflareResources(ctx)
	if err != nil || n != len(six) {
		t.Fatalf("cleanup %d %v", n, err)
	}
	if !cfHasName(cf, "hand.example.com") {
		t.Fatal("cleanup removed a record Aven did not create")
	}
	for _, name := range six {
		if cfHasName(cf, name) {
			t.Fatalf("cleanup left %s", name)
		}
	}
}

func two(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func TestCloudflareRequiresZoneList(t *testing.T) {
	ctx := context.Background()
	f, cf := cfFixture(t, "dns")
	cfg, err := f.st.CloudflareSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Zones = nil
	cfg.Proxied = false
	if err := f.st.SetCloudflareSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	claimHost(t, f, "web.example.com", "custom", store.VerifyTXT)
	cf.Mu.Lock()
	cf.Zones = append(cf.Zones, cloudflare.Zone{ID: "z2", Name: "other.example.net", Status: "active"})
	cf.Zones[1].Account.ID, cf.Zones[1].Account.Name = "acc1", "Acme"
	cf.Writes = nil
	cf.Mu.Unlock()
	err = f.m.SyncCloudflare(ctx)
	if err == nil || !strings.Contains(err.Error(), "zone") {
		t.Fatalf("empty zone list: %v", err)
	}
	if cfHasName(cf, "web.example.com") || len(cf.Records["z2"]) != 0 || len(cfDeletes(cf)) != 0 {
		t.Fatal("empty zone list still changed Cloudflare")
	}
	if !strings.Contains(f.m.CloudflareStatus().Error, "zone") {
		t.Fatalf("status %+v", f.m.CloudflareStatus())
	}
	def := store.DefaultCloudflareSettings()
	if def.Proxied || len(def.Zones) != 0 || def.Wildcards {
		t.Fatalf("defaults %+v", def)
	}
}

func TestParseGeo(t *testing.T) {
	if la, lo, ok := parseGeo(" 37.57, 126.98 "); !ok || la != 37.57 || lo != 126.98 {
		t.Fatal("parse")
	}
	for _, s := range []string{"", "37.5", "a,b", "91,0", "0,181"} {
		if _, _, ok := parseGeo(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
	if !ValidGeoLabel("") || ValidGeoLabel("x") {
		t.Fatal("ValidGeoLabel")
	}
}
