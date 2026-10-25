package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/edge"
	"github.com/turin-dev/aven/internal/netcheck"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// 도메인 확인 라우트(P3): 토큰, ACME 보류, TLS ask, 옛 agent fallback.

func TestDomainCheckBodyMatchesNetcheck(t *testing.T) {
	if proxy.DomainCheckPath != netcheck.CheckPath || proxy.DomainCheckBody("abc") != netcheck.CheckBody("abc") {
		t.Fatal("proxy check route and netcheck probe disagree")
	}
}

// domainFixture는 도메인 claim이 있는 인스턴스 하나를 만든다. 호스트별 상태는 set이 정한다.
func domainFixture(t *testing.T, doms []store.Domain) (*Manager, *store.Instance) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	sv := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, sv); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: sv.ID, EnvironmentID: env.ID}
	in.Domains = doms
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	return &Manager{Store: st, HTTPLogs: NewHTTPLogHub(10)}, in
}

func record(t *testing.T, m *Manager, in *store.Instance, host string, c store.DomainCheck) {
	t.Helper()
	c.CheckedAt = time.Now()
	if err := m.Store.RecordDomainCheck(context.Background(), host, in.ID, c); err != nil {
		t.Fatal(err)
	}
}

func TestDecorateDomainRoutesHoldsOnlyNewUnverifiedHosts(t *testing.T) {
	ctx := context.Background()
	m, in := domainFixture(t, []store.Domain{
		{Host: "legacy.example.org", Kind: "custom"},
		{Host: "new.example.org", Kind: "custom"},
		{Host: "dnsok.example.org", Kind: "custom"},
		{Host: "txt.example.org", Kind: "custom"},
		{Host: "*.wild.example.org", Kind: "custom"},
		{Host: "app.keel.example.com", Kind: "generated"},
		{Host: "plain.example.org", Kind: "custom", TLS: "off"},
	})
	// 마이그레이션이 legacy로 채운 호스트, DNS가 이 노드를 가리키는 호스트, TXT로 확인된 호스트
	record(t, m, in, "legacy.example.org", store.DomainCheck{DNSStatus: store.DNSProxied, VerifyMethod: store.VerifyLegacy})
	record(t, m, in, "dnsok.example.org", store.DomainCheck{DNSStatus: store.DNSActive})
	record(t, m, in, "txt.example.org", store.DomainCheck{DNSStatus: store.DNSProxied, VerifyMethod: store.VerifyTXT})

	kinds := map[string]string{}
	var rs []proxy.Route
	for _, d := range in.Domains {
		kinds[d.Host] = d.Kind
		rs = append(rs, proxy.Route{Hosts: []string{d.Host}, Upstreams: []string{"keel-web-abcdefgh-1:8080"}, TLS: proxy.TLSMode(d.TLS)})
	}
	// 경로 규칙 라우트(같은 호스트)와 aven-server 자신의 라우트(도메인 아님)
	rs = append(rs, proxy.Route{Hosts: []string{"new.example.org"}, Path: "/api/*", Upstreams: []string{"keel-api-abcdefgh-1:8080"}})
	rs = append(rs, proxy.Route{Hosts: []string{"aven.example.com"}, Upstreams: []string{"unix//run/keel/keel-server.sock"}, SetHeaders: map[string]string{"X": "y"}})
	routes := map[string][]proxy.Route{store.LocalNodeID: rs}
	m.decorateDomainRoutes(ctx, routes, kinds)

	wantHold := map[string]bool{"new.example.org": true, "*.wild.example.org": true, "plain.example.org": true}
	for _, r := range routes[store.LocalNodeID] {
		h := r.Hosts[0]
		if h == "aven.example.com" {
			if r.CheckToken != "" || r.Unverified {
				t.Fatalf("self route decorated: %+v", r)
			}
			continue
		}
		if r.CheckToken != m.Store.DomainToken(h) || len(r.CheckToken) != 32 {
			t.Errorf("%s: token %q", h, r.CheckToken)
		}
		if r.Unverified != wantHold[h] {
			t.Errorf("%s: unverified=%v", h, r.Unverified)
		}
	}

	raw, err := proxy.New(proxy.Config{TLS: proxy.TLSAuto}).Build(routes[store.LocalNodeID])
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			TLS struct {
				Automation struct {
					Policies []struct {
						Subjects []string `json:"subjects"`
						OnDemand bool     `json:"on_demand"`
					} `json:"policies"`
				} `json:"automation"`
			} `json:"tls"`
			HTTP struct {
				Servers map[string]struct {
					AutomaticHTTPS struct {
						Skip []string `json:"skip_certificates"`
					} `json:"automatic_https"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var managed, onDemand []string
	for _, p := range cfg.Apps.TLS.Automation.Policies {
		if p.OnDemand {
			onDemand = append(onDemand, p.Subjects...)
		} else {
			managed = append(managed, p.Subjects...)
		}
	}
	slices.Sort(managed)
	slices.Sort(onDemand)
	// legacy·확인된·DNS가 맞는 호스트와 생성 도메인, aven 자신은 지금처럼 관리 정책. tls=off 호스트는 인증서가 없다.
	if !slices.Equal(managed, []string{"app.keel.example.com", "aven.example.com", "dnsok.example.org", "legacy.example.org", "txt.example.org"}) {
		t.Errorf("managed: %v", managed)
	}
	if !slices.Equal(onDemand, []string{"*.wild.example.org", "new.example.org"}) {
		t.Errorf("on-demand: %v", onDemand)
	}
	skip := cfg.Apps.HTTP.Servers["keel"].AutomaticHTTPS.Skip
	slices.Sort(skip)
	if !slices.Equal(skip, []string{"*.wild.example.org", "new.example.org"}) {
		t.Errorf("skip_certificates: %v", skip)
	}
	if _, ok := cfg.Apps.HTTP.Servers["keel-tls-ask"]; !ok {
		t.Error("ask server missing")
	}
}

func TestLocalTLSAllowedFollowsDomainStates(t *testing.T) {
	m, in := domainFixture(t, []store.Domain{
		{Host: "legacy.example.org", Kind: "custom"},
		{Host: "new.example.org", Kind: "custom"},
		{Host: "dnsok.example.org", Kind: "custom"},
		{Host: "*.wild.example.org", Kind: "custom"},
		{Host: "*.old.example.org", Kind: "custom"},
	})
	record(t, m, in, "legacy.example.org", store.DomainCheck{DNSStatus: store.DNSProxied, VerifyMethod: store.VerifyLegacy})
	record(t, m, in, "*.old.example.org", store.DomainCheck{DNSStatus: store.DNSActive, VerifyMethod: store.VerifyLegacy})
	record(t, m, in, "dnsok.example.org", store.DomainCheck{DNSStatus: store.DNSActive})
	hosts := map[string]string{"unclaimed.example.org": in.ID}
	for _, d := range in.Domains {
		hosts[d.Host] = in.ID
	}
	m.HTTPLogs.SetHosts(hosts)
	for host, want := range map[string]bool{
		"legacy.example.org":    true,  // legacy
		"dnsok.example.org":     true,  // DNS가 이 노드를 가리킨다
		"new.example.org":       false, // 확인 전 신규
		"a.wild.example.org":    false, // 확인 전 신규 와일드카드의 하위 이름
		"a.old.example.org":     true,  // legacy 와일드카드
		"unclaimed.example.org": true,  // claim 행 없음: 예전 동작
		"other.example.net":     false, // 설정되지 않은 호스트
	} {
		if got := m.TLSAllowed(host); got != want {
			t.Errorf("%s: %v want %v", host, got, want)
		}
	}
	// TXT로 확인되면 바로(라우트를 다시 보내기 전에도) 허가한다.
	record(t, m, in, "new.example.org", store.DomainCheck{DNSStatus: store.DNSProxied, VerifyMethod: store.VerifyTXT})
	if !m.TLSAllowed("new.example.org") {
		t.Error("verified host still refused")
	}
}

// preP3Route는 도메인 확인 필드가 생기기 전의 proxy.Route다(옛 agent의 엄격한 디코딩을 흉내 낸다).
type preP3Route struct {
	Hosts           []string
	Upstreams       []string
	Path            string
	StripPrefix     bool
	PathsOnly       bool              `json:",omitempty"`
	SetHeaders      map[string]string `json:",omitempty"`
	TLS             proxy.TLSMode
	TrustProto      bool
	Redirect        *proxy.Redirect
	Auth            *proxy.BasicAuth
	Trace           bool
	TraceAttributes map[string]string
	InstanceID      string         `json:",omitempty"`
	Edge            *edge.Settings `json:",omitempty"`
}

// proxyHub는 proxy.apply를 받는 agent다. old면 옛 agent처럼 모르는 필드를 거부한다.
type proxyHub struct {
	old   bool
	calls [][]proxy.Route
}

func (h *proxyHub) Online(string) bool { return true }
func (h *proxyHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *proxyHub) CallStream(_ context.Context, _, op string, req, _ any, _ func(json.RawMessage)) error {
	if op != agentprotocol.OpProxyApply {
		return &agentErr{`operation "` + op + `" is not allowed`}
	}
	r := req.(agentprotocol.ProxyRequest)
	h.calls = append(h.calls, r.Routes)
	if h.old {
		b, _ := json.Marshal(r)
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var v struct {
			Routes []preP3Route `json:"routes"`
		}
		if err := dec.Decode(&v); err != nil {
			return &agentErr{"invalid payload: " + err.Error()}
		}
	}
	return nil
}

func TestApplyRemoteProxyOldAgentFallback(t *testing.T) {
	ctx := context.Background()
	const N = "nod_ccccccccccccc"
	routes := []proxy.Route{
		{Hosts: []string{"a.example.org"}, Upstreams: []string{"keel-web-abcdefgh-1:8080"}, CheckToken: "0123456789abcdef0123456789abcdef", Unverified: true},
		{Hosts: []string{"b.example.org"}, Upstreams: []string{"keel-web-abcdefgh-1:8080"}},
	}
	hasFields := func(rs []proxy.Route) bool {
		for _, r := range rs {
			if r.CheckToken != "" || r.Unverified {
				return true
			}
		}
		return false
	}

	// 새 agent: 한 번에 그대로 받는다.
	hub := &proxyHub{}
	m := &Manager{Agents: hub}
	if err := m.applyRemoteProxy(ctx, N, routes); err != nil || len(hub.calls) != 1 || !hasFields(hub.calls[0]) {
		t.Fatalf("new agent: %v %d", err, len(hub.calls))
	}

	// 옛 agent: 한 번 거부 → 필드를 빼고 다시 보낸다. 다음부터는 처음부터 뺀다(노드별 기억).
	hub = &proxyHub{old: true}
	m = &Manager{Agents: hub}
	if err := m.applyRemoteProxy(ctx, N, routes); err != nil || len(hub.calls) != 2 || hasFields(hub.calls[1]) {
		t.Fatalf("fallback: %v %d", err, len(hub.calls))
	}
	if !hasFields(routes) {
		t.Fatal("caller's routes were modified")
	}
	if m.DomainCheckSupported(N) || !m.DomainCheckSupported("nod_ddddddddddddd") || !m.DomainCheckSupported(store.LocalNodeID) {
		t.Fatal("DomainCheckSupported")
	}
	hub.calls = nil
	if err := m.applyRemoteProxy(ctx, N, routes); err != nil || len(hub.calls) != 1 || hasFields(hub.calls[0]) {
		t.Fatalf("remembered: %v %d", err, len(hub.calls))
	}
	hub.calls = nil
	if err := m.applyRemoteProxy(ctx, "nod_ddddddddddddd", routes); err != nil || len(hub.calls) != 2 {
		t.Fatalf("other node: %v %d", err, len(hub.calls))
	}
	// 시간이 지나면 다시 보내 본다(그 사이 agent가 업그레이드됐다).
	m.proxyCompat.noCheck[N] = time.Now().Add(-noDomainCheckRetry - time.Second)
	hub.old, hub.calls = false, nil
	if err := m.applyRemoteProxy(ctx, N, routes); err != nil || len(hub.calls) != 1 || !hasFields(hub.calls[0]) {
		t.Fatalf("retry after upgrade: %v %d", err, len(hub.calls))
	}

	// 다른 오류는 그대로 돌려주고 다시 보내지 않는다.
	bad := &proxyHub{old: true}
	m = &Manager{Agents: bad}
	if err := m.applyRemoteProxy(ctx, N, []proxy.Route{{Hosts: []string{"c.example.org"}}}); err != nil || len(bad.calls) != 1 {
		t.Fatalf("no new fields: %v %d", err, len(bad.calls))
	}
	if !isDomainCheckFieldRefused(&agentErr{`invalid payload: json: unknown field "Unverified"`}) || isDomainCheckFieldRefused(&agentErr{`json: unknown field "Edge"`}) {
		t.Fatal("isDomainCheckFieldRefused")
	}
}
