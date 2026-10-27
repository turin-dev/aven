package mesh

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/node"
)

func TestValidateImportAliases(t *testing.T) {
	base := Config{
		Peers:   []Peer{{NodeID: nodB, Address: "100.64.1.2:7443", Serials: []string{"ab12"}}},
		Imports: []Import{{EnvID: envA, EnvName: "production", Service: "db", Ports: []int{5432}, Node: nodB}},
	}
	with := func(aliases ...string) Config {
		c := base
		c.Imports = []Import{base.Imports[0]}
		c.Imports[0].Aliases = aliases
		return c
	}
	if err := Validate(with(), nodA); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 63)
	if err := Validate(with("pg", "pg.production.keel.internal", long, long+".production.keel.internal"), nodA); err != nil {
		t.Fatalf("valid aliases: %v", err)
	}
	for _, bad := range []string{
		"", "PG", "pg.", "-pg", "pg-", "pg_db", strings.Repeat("a", 64), "pg.keel.internal", "pg.staging.keel.internal",
		"pg.production.keel.internal.evil.com", "evil.com", "a.b.production.keel.internal", ".production.keel.internal", "pg db",
	} {
		if err := Validate(with(bad), nodA); err == nil {
			t.Errorf("alias %q accepted", bad)
		}
	}
	many := make([]string, maxAliases+1)
	for i := range many {
		many[i] = "a" + strings.Repeat("b", i)
	}
	if err := Validate(with(many...), nodA); err == nil {
		t.Error("too many aliases accepted")
	}
	// 별칭이 없으면 JSON에 필드가 없다(옛 agent가 거부하지 않게).
	b, _ := json.Marshal(base)
	if strings.Contains(string(b), "aliases") {
		t.Fatalf("aliases not omitted: %s", b)
	}
}

func TestForwarderSpecAliases(t *testing.T) {
	gw := netip.MustParseAddr("172.18.0.1")
	local := map[int]netip.AddrPort{5432: netip.AddrPortFrom(gw, 40001)}
	im := Import{EnvID: envA, EnvName: "production", Service: "db", Ports: []int{5432}, Node: nodB}
	s := ForwarderSpec(im, local)
	if !slices.Equal(s.Aliases, []string{"db", "db.production.keel.internal", "db.production.aven.internal"}) {
		t.Fatalf("aliases: %v", s.Aliases)
	}
	if _, ok := s.Labels[labelMeshAliases]; ok {
		t.Fatal("alias label without aliases")
	}
	im.Aliases = []string{"pg", "pg.production.keel.internal", "db"}
	s = ForwarderSpec(im, local)
	if !slices.Equal(s.Aliases, []string{"db", "db.production.keel.internal", "db.production.aven.internal", "pg", "pg.production.keel.internal"}) {
		t.Fatalf("custom aliases: %v", s.Aliases)
	}
	if s.Labels[labelMeshAliases] != "pg,pg.production.keel.internal" {
		t.Fatalf("label: %v", s.Labels)
	}
}

func TestEnsureRecreatesOnAliasChange(t *testing.T) {
	up := echoServer(t)
	ga, gb, _, b, _ := pair(t, int(up.Port()))
	f := &fakeDocker{labels: map[string]map[string]string{},
		nets: map[string]*node.NetworkInfo{"keel-" + envA: {Subnets: loopNet.Subnets, Gateways: []netip.Addr{loopNet.Gateway}}}}
	cfg := Config{Peers: []Peer{peerOf(gb, b)}, Imports: []Import{{EnvID: envA, EnvName: "production", Service: "api", Ports: []int{int(up.Port())}, Node: nodB}}}
	ctx := context.Background()
	if _, err := Ensure(ctx, ga, f, cfg); err != nil || len(f.ran) != 1 {
		t.Fatalf("first: %v %v", err, f.ran)
	}
	cfg.Imports[0].Aliases = []string{"backend", "backend.production.keel.internal"}
	if _, err := Ensure(ctx, ga, f, cfg); err != nil || len(f.ran) != 2 {
		t.Fatalf("alias added: %v %v", err, f.ran)
	}
	if _, err := Ensure(ctx, ga, f, cfg); err != nil || len(f.ran) != 2 {
		t.Fatalf("not idempotent: %v %v", err, f.ran)
	}
	cfg.Imports[0].Aliases = nil
	if _, err := Ensure(ctx, ga, f, cfg); err != nil || len(f.ran) != 3 {
		t.Fatalf("alias removed: %v %v", err, f.ran)
	}
}
