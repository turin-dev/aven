package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/secret"
)

func domainTestStore(t *testing.T) (*Store, string, *Project, *Environment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &Project{Name: "dom"}
	env, err := st.CreateProject(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return st, path, p, env
}

func TestDomainStatesMigrationBackfill(t *testing.T) {
	ctx := context.Background()
	st, path, p, env := domainTestStore(t)
	a := resourceService(t, st, p, env, "a", Spec{Domains: []Domain{{Host: "a.example.com", Kind: "custom"}, {Host: "shared.example.com", Kind: "custom"}}})
	if err := st.CreateInstance(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := resourceService(t, st, p, env, "b", Spec{Domains: []Domain{{Host: "b.example.com", Kind: "custom"}}})
	if err := st.CreateInstance(ctx, b); err != nil {
		t.Fatal(err)
	}
	// 예전 경합으로 생긴 중복 호스트를 흉내 낸다(claim 없이 스펙만 쓴다).
	b.Domains = append(b.Domains, Domain{Host: "shared.example.com", Kind: "custom"})
	if _, err := st.q.ExecContext(ctx, `UPDATE instances SET spec=? WHERE id=?`, jsonStr(b.Spec), b.ID); err != nil {
		t.Fatal(err)
	}
	// 테이블이 없던 예전 DB로 되돌리고 다시 연다.
	for _, q := range []string{`DROP TABLE domain_states`, `ALTER TABLE nodes DROP COLUMN ingress_mode`, `ALTER TABLE nodes DROP COLUMN ingress_hostname`,
		`ALTER TABLE nodes DROP COLUMN tcp_hostname`, `ALTER TABLE nodes DROP COLUMN ipv6_pool`, `UPDATE schema_version SET version=42`} {
		if _, err := st.q.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	all, err := st.ListDomainStates(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("states: %+v %v", all, err)
	}
	for _, d := range all {
		if d.VerifyMethod != VerifyLegacy || d.VerifiedAt == nil || !d.Verified() || d.DNSStatus != DNSPending || d.CheckedAt != nil {
			t.Errorf("backfill %s: %+v", d.Host, d)
		}
	}
	if sh, _ := st.GetDomainState(ctx, "shared.example.com"); sh == nil || sh.InstanceID != a.ID {
		t.Errorf("duplicate host owner: %+v", sh)
	}
	// 예전 중복을 가진 쪽도 다른 설정 변경은 그대로 된다(같은 호스트를 새로 claim하지 않는다).
	got, _ := st.GetInstance(ctx, b.ID)
	got.Variables["X"] = "1"
	if err := st.UpdateInstanceConfig(ctx, got); err != nil {
		t.Fatalf("legacy duplicate update: %v", err)
	}
	n, err := st.LocalNode(ctx)
	if err != nil || n.IngressMode != IngressDirect || n.IngressHostname != "" {
		t.Fatalf("node ingress default: %+v %v", n, err)
	}
}

func TestDomainClaimsFollowSpec(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	a := resourceService(t, st, p, env, "a", Spec{})
	if err := st.CreateInstance(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := resourceService(t, st, p, env, "b", Spec{})
	if err := st.CreateInstance(ctx, b); err != nil {
		t.Fatal(err)
	}
	a.Domains = []Domain{{Host: "App.Example.com", Kind: "custom"}}
	if err := st.UpdateInstanceConfig(ctx, a); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetDomainState(ctx, "app.example.com")
	if err != nil || d.InstanceID != a.ID || d.Verified() || d.DNSStatus != DNSPending {
		t.Fatalf("claim: %+v %v", d, err)
	}
	b.Domains = []Domain{{Host: "app.example.com", Kind: "custom"}}
	err = st.UpdateInstanceConfig(ctx, b)
	var hc *HostClaimedError
	if !errors.Is(err, ErrConflict) || !errors.As(err, &hc) || hc.Host != "app.example.com" {
		t.Fatalf("duplicate: %v", err)
	}
	if got, _ := st.GetInstance(ctx, b.ID); len(got.Domains) != 0 {
		t.Fatalf("spec written despite conflict: %+v", got.Domains)
	}
	// 새 인스턴스도 마찬가지
	c := resourceService(t, st, p, env, "c", Spec{Domains: []Domain{{Host: "app.example.com"}}})
	if err := st.CreateInstance(ctx, c); !errors.Is(err, ErrConflict) {
		t.Fatalf("create duplicate: %v", err)
	}
	// 확인 결과 저장: 검증 시각은 처음 한 번만
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.RecordDomainCheck(ctx, "app.example.com", a.ID, DomainCheck{DNSStatus: DNSProxied, Proxy: "cloudflare", VerifyMethod: VerifyHTTP,
		Observed: []ObservedRR{{"A", "104.16.0.1"}}, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordDomainCheck(ctx, "app.example.com", a.ID, DomainCheck{DNSStatus: DNSActive, Proxy: "direct", VerifyMethod: VerifyDNS,
		CheckedAt: now.Add(time.Minute), CertStatus: "valid", CertNotAfter: &now}); err != nil {
		t.Fatal(err)
	}
	// 다른 인스턴스 이름으로 온 결과는 무시한다
	st.RecordDomainCheck(ctx, "app.example.com", b.ID, DomainCheck{DNSStatus: DNSError, CheckedAt: now})
	d, _ = st.GetDomainState(ctx, "app.example.com")
	if d.DNSStatus != DNSActive || d.Proxy != "direct" || d.VerifyMethod != VerifyHTTP || d.VerifiedAt == nil || !d.VerifiedAt.Equal(now) ||
		d.CertStatus != "valid" || d.CertNotAfter == nil || len(d.Observed) != 0 {
		t.Fatalf("recorded: %+v", d)
	}
	// 지우면 claim이 풀리고 다른 인스턴스가 가질 수 있다
	a.Domains = nil
	if err := st.UpdateInstanceConfig(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateInstanceConfig(ctx, b); err != nil {
		t.Fatalf("after release: %v", err)
	}
	// 인스턴스(서비스)를 지우면 claim도 지워진다(ON DELETE CASCADE)
	if err := st.DeleteService(ctx, b.ServiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDomainState(ctx, "app.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cascade: %v", err)
	}
}

func TestConcurrentDomainClaim(t *testing.T) {
	ctx := context.Background()
	st, _, p, env := domainTestStore(t)
	var ins []*Instance
	for _, n := range []string{"a", "b"} {
		in := resourceService(t, st, p, env, n, Spec{})
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		ins = append(ins, in)
	}
	for round := 0; round < 20; round++ {
		host := "race" + string(rune('a'+round)) + ".example.com"
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, in := range ins {
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				<-start
				cur, err := st.GetInstance(ctx, id)
				if err != nil {
					errs[i] = err
					return
				}
				cur.Domains = append(cur.Domains, Domain{Host: host, Kind: "custom"})
				errs[i] = st.UpdateInstanceConfig(ctx, cur)
			}(i, in.ID)
		}
		close(start)
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if !errors.Is(err, ErrConflict) {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d winners (%v)", round, ok, errs)
		}
		owner, _ := st.GetDomainState(ctx, host)
		with := 0
		for _, in := range ins {
			got, _ := st.GetInstance(ctx, in.ID)
			for _, d := range got.Domains {
				if d.Host == host {
					with++
					if owner.InstanceID != in.ID {
						t.Fatalf("round %d: claim owner %s, spec on %s", round, owner.InstanceID, in.ID)
					}
				}
			}
		}
		if with != 1 {
			t.Fatalf("round %d: host in %d specs", round, with)
		}
	}
}

func TestDomainToken(t *testing.T) {
	st, _, _, _ := domainTestStore(t)
	a := st.DomainToken("Shop.Example.com")
	if len(a) != 32 || a != st.DomainToken("shop.example.com") || a == st.DomainToken("other.example.com") {
		t.Fatalf("token without key: %q", a)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	box, err := secret.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretBox(box)
	b := st.DomainToken("shop.example.com")
	m := hmac.New(sha256.New, key)
	m.Write([]byte("domain:shop.example.com"))
	if want := hex.EncodeToString(m.Sum(nil))[:32]; b != want {
		t.Fatalf("token with key: %q, want %q", b, want)
	}
	if b == a {
		t.Fatalf("token with key: %q", b)
	}
	key2 := make([]byte, 32)
	key2[0] = 1
	box2, _ := secret.NewBox(key2)
	st.SetSecretBox(box2)
	if st.DomainToken("shop.example.com") == b {
		t.Fatal("token does not depend on the master key")
	}
}
