package netcheck

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

type fakeResolver struct {
	ips   map[string][]string
	cname map[string]string
	txt   map[string][]string
	fail  map[string]error
}

func (f *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if err := f.fail[host]; err != nil {
		return nil, err
	}
	v, ok := f.ips[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []net.IPAddr
	for _, s := range v {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

func (f *fakeResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if c, ok := f.cname[host]; ok {
		return c + ".", nil
	}
	return host + ".", nil
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return f.txt[name], nil
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClassification(t *testing.T) {
	tok := func(h string) string { return "tok-" + h }
	res := &fakeResolver{
		ips: map[string][]string{
			"direct.example.com":         {"2001:db8:6b:1943::2", "192.0.2.154"},
			"cname.example.com":          {"192.0.2.154"},
			"cf.example.com":             {"104.16.1.1", "2606:4700::1"},
			"cfok.example.com":           {"104.16.1.2"},
			"tun.example.com":            {"104.16.1.3"},
			"argo.example.com":           {"104.16.1.4"},
			"other.example.com":          {"192.0.2.10"},
			"txt.example.com":            {"192.0.2.11"},
			"keel-dns-check.example.org": {"192.0.2.154"},
		},
		cname: map[string]string{"cname.example.com": "main.ingress.example.com", "argo.example.com": "abc.cfargotunnel.com"},
		txt:   map[string][]string{"_keel-verify.txt.example.com": {"keel-verify=tok-txt.example.com"}},
		fail:  map[string]error{"broken.example.com": &net.DNSError{Err: "server misbehaving", Name: "broken.example.com", IsTemporary: true}},
	}
	var probed []string
	cl := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		probed = append(probed, r.URL.Host)
		if r.URL.Path != CheckPath {
			t.Errorf("probe path %s", r.URL.Path)
		}
		body := "nope"
		if r.URL.Host == "cfok.example.com" {
			body = CheckBody("tok-cfok.example.com")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	c := &Checker{Resolver: res, HTTP: cl, Token: tok}
	node := Target{IPv6: []string{"2001:db8:6b:1943::2"}, IPv4: []string{"192.0.2.154"}, Ingress: []string{"main.ingress.example.com"}}
	for _, tc := range []struct {
		host           string
		tunnel         bool
		status, proxy  string
		verify         string
		observedPrefix string
	}{
		{"direct.example.com", false, store.DNSActive, ProxyDirect, store.VerifyDNS, "AAAA"},
		{"cname.example.com", false, store.DNSActive, ProxyDirect, store.VerifyDNS, "CNAME"},
		{"cf.example.com", false, store.DNSProxied, ProxyCloudflare, "", "A"},
		{"cfok.example.com", false, store.DNSProxied, ProxyCloudflare, store.VerifyHTTP, "A"},
		{"tun.example.com", true, store.DNSProxied, ProxyTunnel, "", "A"},
		{"argo.example.com", false, store.DNSProxied, ProxyTunnel, "", "CNAME"},
		{"other.example.com", false, store.DNSPendingDNS, ProxyNone, "", "A"},
		{"txt.example.com", false, store.DNSPendingDNS, ProxyNone, store.VerifyTXT, "A"},
		{"missing.example.com", false, store.DNSPendingDNS, ProxyNone, "", ""},
		{"broken.example.com", false, store.DNSError, ProxyNone, "", ""},
		{"*.example.org", false, store.DNSActive, ProxyDirect, store.VerifyDNS, "A"},
	} {
		tg := node
		tg.Host, tg.Tunnel = tc.host, tc.tunnel
		r := c.Check(context.Background(), tg)
		if r.Status != tc.status || r.Proxy != tc.proxy || r.VerifyMethod != tc.verify {
			t.Errorf("%s: got %s/%s/%q, want %s/%s/%q (%+v)", tc.host, r.Status, r.Proxy, r.VerifyMethod, tc.status, tc.proxy, tc.verify, r)
		}
		if tc.observedPrefix != "" && (len(r.Observed) == 0 || r.Observed[0].Type != tc.observedPrefix) {
			t.Errorf("%s: observed %+v", tc.host, r.Observed)
		}
		if tc.status == store.DNSError && r.Error == "" {
			t.Errorf("%s: no error text", tc.host)
		}
	}
	// HTTPS 확인은 프록시 뒤 도메인에만 한다.
	for _, h := range probed {
		if h != "cf.example.com" && h != "cfok.example.com" && h != "tun.example.com" && h != "argo.example.com" {
			t.Errorf("unexpected probe of %s", h)
		}
	}
}

func TestDefaultClientOnlyDialsCloudflare(t *testing.T) {
	c := &Checker{Resolver: &fakeResolver{ips: map[string][]string{"evil.example.com": {"127.0.0.1", "10.0.0.1"}}}}
	_, err := c.probe(context.Background(), "evil.example.com", "x")
	if err == nil || !strings.Contains(err.Error(), "Cloudflare") {
		t.Fatalf("private address probed: %v", err)
	}
}

func TestDialPreferV6FallsBack(t *testing.T) {
	var tried []string
	dial := func(_ context.Context, network, addr string) (net.Conn, error) {
		tried = append(tried, network)
		if network == "tcp6" {
			return nil, errors.New("v6 unreachable")
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	ips := mustAddrs("192.0.2.1", "2001:db8::1")
	conn, err := dialPreferV6(context.Background(), dial, ips, "443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(tried) != 2 || tried[0] != "tcp6" || tried[1] != "tcp4" {
		t.Fatalf("order: %v", tried)
	}
}

func TestCheckAndRecordBackoff(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "n"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, Spec: store.Spec{Domains: []store.Domain{{Host: "app.example.com", Kind: "custom"}}}}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	res := &fakeResolver{ips: map[string][]string{"app.example.com": {"192.0.2.154"}}}
	tg := Target{Host: "app.example.com", InstanceID: in.ID, IPv4: []string{"192.0.2.154"}}
	c := &Checker{Store: st, Resolver: res, Targets: func(context.Context) ([]Target, error) { return []Target{tg}, nil }}
	c.RunOnce(ctx)
	ds, _ := st.GetDomainState(ctx, "app.example.com")
	if ds.DNSStatus != store.DNSActive || ds.Proxy != ProxyDirect || ds.VerifyMethod != store.VerifyDNS || ds.CheckedAt == nil {
		t.Fatalf("state: %+v", ds)
	}
	first := c.sched["app.example.com"].interval
	c.CheckAndRecord(ctx, tg)
	c.CheckAndRecord(ctx, tg)
	if got := c.sched["app.example.com"].interval; got != 4*first || got > 10*time.Minute {
		t.Fatalf("backoff: %v then %v", first, got)
	}
	for i := 0; i < 10; i++ {
		c.CheckAndRecord(ctx, tg)
	}
	if got := c.sched["app.example.com"].interval; got != 10*time.Minute {
		t.Fatalf("max backoff: %v", got)
	}
	// 연결이 끊기면(DNS가 다른 곳) 기본 간격으로 돌아온다.
	res.ips["app.example.com"] = []string{"192.0.2.1"}
	c.CheckAndRecord(ctx, tg)
	if got := c.sched["app.example.com"].interval; got != time.Minute {
		t.Fatalf("reset: %v", got)
	}
	c.Kick("app.example.com")
	if !c.sched["app.example.com"].next.IsZero() {
		t.Fatal("kick did not reset schedule")
	}
	if r, ok := c.CheckHost(ctx, "APP.example.com"); !ok || r.Status != store.DNSPendingDNS {
		t.Fatalf("check host: %+v %v", r, ok)
	}
}

func mustAddrs(s ...string) (out []netip.Addr) {
	for _, v := range s {
		out = append(out, netip.MustParseAddr(v))
	}
	return out
}

func TestOnVerifiedFiresOnceOnTransition(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "n"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, Spec: store.Spec{Domains: []store.Domain{{Host: "app.example.com", Kind: "custom"}}}}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	res := &fakeResolver{ips: map[string][]string{"app.example.com": {"192.0.2.1"}}}
	tg := Target{Host: "app.example.com", InstanceID: in.ID, IPv4: []string{"192.0.2.154"}}
	var fired []string
	c := &Checker{Store: st, Resolver: res, OnVerified: func(h string) { fired = append(fired, h) }}
	c.CheckAndRecord(ctx, tg) // 다른 곳을 가리킨다: 확인 안 됨
	if len(fired) != 0 {
		t.Fatalf("fired while unverified: %v", fired)
	}
	res.ips["app.example.com"] = []string{"192.0.2.154"}
	c.CheckAndRecord(ctx, tg) // 이 노드를 가리킨다: 이번에 확인됨
	c.CheckAndRecord(ctx, tg) // 이미 확인됨: 다시 부르지 않는다
	if len(fired) != 1 || fired[0] != "app.example.com" {
		t.Fatalf("fired: %v", fired)
	}
}

// 도메인별 IPv6: 전용 주소를 가리켜도, 노드 기본 주소(::2)를 가리켜도 direct(active)다. 다른 도메인의 전용 주소는 아니다.
func TestDedicatedIPv6Direct(t *testing.T) {
	res := &fakeResolver{ips: map[string][]string{
		"dedicated.example.com": {"2001:db8:6b:1943:d::2"},
		"shared.example.com":    {"2001:db8:6b:1943::2"},
		"neighbor.example.com":  {"2001:db8:6b:1943:d::3"},
	}}
	c := &Checker{Resolver: res, Token: func(string) string { return "" }}
	for host, want := range map[string]string{"dedicated.example.com": ProxyDirect, "shared.example.com": ProxyDirect, "neighbor.example.com": ProxyNone} {
		r := c.Check(context.Background(), Target{Host: host, IPv6: []string{"2001:db8:6b:1943::2", "2001:db8:6b:1943:d::2"}, IPv4: []string{"192.0.2.154"}})
		if r.Proxy != want {
			t.Errorf("%s: %+v (want %s)", host, r, want)
		}
		if want == ProxyDirect && r.Status != store.DNSActive {
			t.Errorf("%s: status %s", host, r.Status)
		}
	}
}
