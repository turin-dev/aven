package store

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func ipv6Instance(t *testing.T, st *Store, p *Project, env *Environment, name string, hosts ...string) *Instance {
	t.Helper()
	var ds []Domain
	for _, h := range hosts {
		ds = append(ds, Domain{Host: h, Kind: "custom"})
	}
	in := resourceService(t, st, p, env, name, Spec{Domains: ds})
	if err := st.CreateInstance(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	return in
}

func wantAll(pool string, hosts ...string) map[string]netip.Prefix {
	p := netip.MustParsePrefix(pool)
	out := map[string]netip.Prefix{}
	for _, h := range hosts {
		out[h] = p
	}
	return out
}

func TestDomainIPv6AllocationOrderAndStability(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	ipv6Instance(t, st, p, env, "a", "a.example.com")
	time.Sleep(2 * time.Millisecond)
	ipv6Instance(t, st, p, env, "b", "b.example.com", "c.example.com")
	pool := "2001:db8:6b:1943:d::/80"
	ch, err := st.ReconcileDomainIPv6(ctx, wantAll(pool, "a.example.com", "b.example.com", "c.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 3 {
		t.Fatalf("changes: %+v", ch)
	}
	// 네트워크 주소(…d::)와 ::1을 건너뛰고 created_at, host 순서로 ::2, ::3, ::4.
	want := map[string]string{"a.example.com": "2001:db8:6b:1943:d::2", "b.example.com": "2001:db8:6b:1943:d::3", "c.example.com": "2001:db8:6b:1943:d::4"}
	for h, a := range want {
		if got := st.DomainIPv6(ctx, h); got != a {
			t.Fatalf("%s = %q, want %s", h, got, a)
		}
	}
	// 다시 맞춰도 바뀌지 않는다.
	ch, err = st.ReconcileDomainIPv6(ctx, wantAll(pool, "a.example.com", "b.example.com", "c.example.com"))
	if err != nil || len(ch) != 0 {
		t.Fatalf("second reconcile changed: %+v %v", ch, err)
	}
	ds, _ := st.GetDomainState(ctx, "b.example.com")
	if ds.IPv6 != want["b.example.com"] {
		t.Fatalf("DomainState.IPv6 = %q", ds.IPv6)
	}
}

func TestDomainIPv6ReleaseAndReuseLowest(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	a := ipv6Instance(t, st, p, env, "a", "a.example.com", "x.example.com")
	ipv6Instance(t, st, p, env, "b", "b.example.com")
	pool := "2a01:db8:1::/120"
	hosts := []string{"a.example.com", "x.example.com", "b.example.com"}
	if _, err := st.ReconcileDomainIPv6(ctx, wantAll(pool, hosts...)); err != nil {
		t.Fatal(err)
	}
	x := st.DomainIPv6(ctx, "x.example.com")
	if x != "2a01:db8:1::3" {
		t.Fatalf("x = %s", x)
	}
	// 도메인을 지우면 claim 행과 함께 주소가 풀린다.
	a.Domains = a.Domains[:1]
	if err := st.UpdateInstanceConfig(ctx, a); err != nil {
		t.Fatal(err)
	}
	m, _ := st.ManagedIPv6s(ctx)
	if _, ok := m[netip.MustParseAddr(x)]; ok {
		t.Fatal("address not released on delete")
	}
	// 새 도메인은 비어 있는 가장 낮은 주소(풀린 ::3)를 받는다.
	ipv6Instance(t, st, p, env, "c", "c.example.com")
	if _, err := st.ReconcileDomainIPv6(ctx, wantAll(pool, "a.example.com", "b.example.com", "c.example.com")); err != nil {
		t.Fatal(err)
	}
	if got := st.DomainIPv6(ctx, "c.example.com"); got != "2a01:db8:1::3" {
		t.Fatalf("c = %s, want reused ::3", got)
	}
}

func TestDomainIPv6MoveAndPoolRemoval(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	ipv6Instance(t, st, p, env, "a", "a.example.com")
	ipv6Instance(t, st, p, env, "b", "b.example.com")
	main, us := netip.MustParsePrefix("2001:db8:6b:1943:d::/80"), netip.MustParsePrefix("2001:db8:2:1::/112")
	if _, err := st.ReconcileDomainIPv6(ctx, map[string]netip.Prefix{"a.example.com": main, "b.example.com": main}); err != nil {
		t.Fatal(err)
	}
	// b가 us 노드로 옮겨 가면 us 풀에서 다시 받는다. a는 그대로다.
	ch, err := st.ReconcileDomainIPv6(ctx, map[string]netip.Prefix{"a.example.com": main, "b.example.com": us})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 1 || ch[0].Host != "b.example.com" || ch[0].Old != "2001:db8:6b:1943:d::3" || ch[0].New != "2001:db8:2:1::2" {
		t.Fatalf("move changes: %+v", ch)
	}
	if st.DomainIPv6(ctx, "a.example.com") != "2001:db8:6b:1943:d::2" {
		t.Fatal("a changed")
	}
	// 풀이 없는 노드(want에 없음)면 주소를 푼다.
	ch, err = st.ReconcileDomainIPv6(ctx, map[string]netip.Prefix{"a.example.com": main})
	if err != nil || len(ch) != 1 || ch[0].New != "" {
		t.Fatalf("removal: %+v %v", ch, err)
	}
	if st.DomainIPv6(ctx, "b.example.com") != "" {
		t.Fatal("b still has an address")
	}
}

func TestDomainIPv6RenameKeepsAddressAndPoolFull(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	a := ipv6Instance(t, st, p, env, "a", "a.example.com")
	ipv6Instance(t, st, p, env, "b", "b.example.com")
	ipv6Instance(t, st, p, env, "c", "c.example.com")
	// /126: 주소 4개 중 네트워크 주소, ::1을 빼면 두 개만 배정할 수 있다.
	pool := "2a01:db8:2::/126"
	if _, err := st.ReconcileDomainIPv6(ctx, wantAll(pool, "a.example.com", "b.example.com", "c.example.com")); err != nil {
		t.Fatal(err)
	}
	if st.DomainIPv6(ctx, "c.example.com") != "" {
		t.Fatal("c should not fit in the pool")
	}
	a.Domains[0].Host = "renamed.example.com"
	if err := st.RenameInstanceDomain(ctx, a, "a.example.com", "renamed.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := st.DomainIPv6(ctx, "renamed.example.com"); got != "2a01:db8:2::2" {
		t.Fatalf("renamed = %q", got)
	}
}

func TestDomainIPv6ConcurrentUnique(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	var hosts []string
	for i := 0; i < 30; i++ {
		h := fmt.Sprintf("h%02d.example.com", i)
		hosts = append(hosts, h)
		ipv6Instance(t, st, p, env, fmt.Sprintf("s%02d", i), h)
	}
	pool := "2001:db8:6b:1943:d::/80"
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// 도메인 추가, 백그라운드 루프, agent 재연결이 동시에 맞춰도 주소가 겹치지 않아야 한다.
			if _, err := st.ReconcileDomainIPv6(ctx, wantAll(pool, hosts...)); err != nil {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	m, err := st.ManagedIPv6s(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != len(hosts) {
		t.Fatalf("%d unique addresses for %d hosts", len(m), len(hosts))
	}
	// UNIQUE 인덱스: 같은 주소를 두 행에 직접 넣으면 실패한다.
	if _, err := st.q.ExecContext(ctx, `UPDATE domain_states SET ipv6=? WHERE host=?`, st.DomainIPv6(ctx, hosts[0]), hosts[1]); err == nil {
		t.Fatal("duplicate ipv6 accepted")
	}
}

func TestNodeIPv6PoolColumn(t *testing.T) {
	ctx := context.Background()
	st, _, _, _ := domainTestStore(t)
	if err := st.SetNodeIPv6Pool(ctx, "", "2001:db8:6b:1943:d::/80"); err != nil {
		t.Fatal(err)
	}
	n, err := st.LocalNode(ctx)
	if err != nil || n.IPv6Pool != "2001:db8:6b:1943:d::/80" {
		t.Fatalf("local pool: %+v %v", n, err)
	}
	if err := st.SetNodeIPv6Pool(ctx, LocalNodeID, ""); err != nil {
		t.Fatal(err)
	}
	n, _ = st.GetNode(ctx, LocalNodeID)
	if n.IPv6Pool != "" {
		t.Fatal("pool not cleared")
	}
}

func TestNextFreeIPv6(t *testing.T) {
	p := netip.MustParsePrefix("2a01:db8::/120")
	used := map[netip.Addr]bool{netip.MustParseAddr("2a01:db8::2"): true, netip.MustParseAddr("2a01:db8::4"): true, netip.MustParseAddr("2a01:db9::3"): true}
	a, ok := nextFreeIPv6(p, used)
	if !ok || a.String() != "2a01:db8::3" {
		t.Fatalf("got %v %v", a, ok)
	}
}
