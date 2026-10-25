package ha

import (
	"reflect"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
)

func TestImageVersion(t *testing.T) {
	cases := map[string]Version{
		"postgres:17-alpine":                   {17, -1},
		"postgres:16.4":                        {16, 4},
		"docker.io/library/redis:7.4-alpine":   {7, 4},
		"localhost:5000/redis:8.0.2@sha256:ab": {8, 0},
		"mongo:8.0":                            {8, 0},
		"redis:v7.2":                           {7, 2},
	}
	for img, want := range cases {
		got, err := ImageVersion(img)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", img, got, err, want)
		}
	}
	for _, img := range []string{"postgres", "postgres:latest", "postgres:alpine", "localhost:5000/postgres"} {
		if _, err := ImageVersion(img); err == nil {
			t.Errorf("%s: want error", img)
		}
	}
}

func TestCheckImage(t *testing.T) {
	pg := catalog.Templates["postgres-ha"].Definition.HACompanionRoot().HAConversionConfig
	if _, probs := CheckImage(pg, "postgres:17-alpine"); len(probs) != 0 {
		t.Errorf("default postgres image refused: %v", probs)
	}
	if _, probs := CheckImage(pg, "docker.io/library/postgres:16"); len(probs) != 0 {
		t.Errorf("docker.io prefix refused: %v", probs)
	}
	if _, probs := CheckImage(pg, "postgis/postgis:17-3.5"); len(probs) != 1 || !strings.Contains(probs[0], "postgis/postgis") {
		t.Errorf("postgis: %v", probs)
	}
	if _, probs := CheckImage(pg, "postgres:12"); len(probs) != 1 {
		t.Errorf("pg 12: %v", probs)
	}
	if _, probs := CheckImage(pg, "postgres:latest"); len(probs) != 1 {
		t.Errorf("latest: %v", probs)
	}
	redis := catalog.Templates["redis-ha"].Definition.HACompanionRoot().HAConversionConfig
	if _, probs := CheckImage(redis, "redis:7.4-alpine"); len(probs) != 0 {
		t.Errorf("default redis image refused: %v", probs)
	}
	if _, probs := CheckImage(redis, "redis:7"); len(probs) != 1 || !strings.Contains(probs[0], "minor") {
		t.Errorf("redis needs minor pin: %v", probs)
	}
	if _, probs := CheckImage(redis, "valkey/valkey:8.0"); len(probs) != 1 {
		t.Errorf("valkey: %v", probs)
	}
}

func TestCounts(t *testing.T) {
	cfg := catalog.Templates["postgres-ha"].Definition.HACompanionRoot().HAConversionConfig
	protos := map[string]bool{"replica": true, "edge": true}
	got, err := Counts(cfg, nil, protos)
	if err != nil || !reflect.DeepEqual(got, map[string]int{"replica": 2, "edge": 2}) {
		t.Fatalf("defaults: %v %v", got, err)
	}
	got, err = Counts(cfg, map[string]int{"replica": 3, "edge": 1}, protos)
	if err != nil || got["replica"] != 3 || got["edge"] != 1 {
		t.Fatalf("explicit: %v %v", got, err)
	}
	for _, bad := range []map[string]int{{"replica": 9}, {"internal": 3}, {"bogus": 1}, {"edge": 0}} {
		if _, err := Counts(cfg, bad, protos); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// 선택지가 없는 역할은 1로 고정
	if got, err := Counts(&catalog.HAConversionConfig{}, map[string]int{"edge": 1}, map[string]bool{"edge": true}); err != nil || got["edge"] != 1 {
		t.Errorf("fixed role: %v %v", got, err)
	}
	if _, err := Counts(&catalog.HAConversionConfig{}, map[string]int{"edge": 2}, map[string]bool{"edge": true}); err == nil {
		t.Error("fixed role accepted 2")
	}
}

func TestSpreadNodes(t *testing.T) {
	// root가 n1에 있으면 replica는 다른 노드부터 채운다.
	got := SpreadNodes([]string{"n1", "n2", "n3"}, map[string]int{"n1": 1}, 4)
	if want := []string{"n2", "n3", "n1", "n2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := SpreadNodes([]string{"local"}, map[string]int{"local": 1}, 2); !reflect.DeepEqual(got, []string{"local", "local"}) {
		t.Errorf("single node: %v", got)
	}
	if SpreadNodes(nil, nil, 2) != nil || SpreadNodes([]string{"a"}, nil, 0) != nil {
		t.Error("empty")
	}
	order, n := Placement([]string{"b", "a", "b"})
	if !reflect.DeepEqual(order, []string{"b", "a"}) || n["b"] != 2 || n["a"] != 1 {
		t.Errorf("placement %v %v", order, n)
	}
}

func TestMemberName(t *testing.T) {
	if got := MemberName("main-db", "postgres", "postgres-replica", 2, true); got != "main-db-replica-2" {
		t.Error(got)
	}
	if got := MemberName("main-db", "postgres", "postgres-ha", 0, false); got != "main-db-ha" {
		t.Error(got)
	}
	if got := MemberName("cache", "redis", "haproxy", 0, false); got != "cache-haproxy" {
		t.Error(got)
	}
}

func TestWiring(t *testing.T) {
	w := catalog.Templates["postgres-ha"].Definition.HACompanionRoot().ClusterWiring
	top := Topology{RootName: "db", Primary: "db-replica-1", Data: []string{"db", "db-replica-1", "db-replica-2"}, Edge: "db-ha"}
	root := DataVars(w, top, "db", RejoinStandby)
	want := map[string]string{"KEEL_HA_ROLE": "replica", "KEEL_HA_PRIMARY": "${{db-replica-1.KEEL_PRIVATE_DOMAIN}}",
		"KEEL_HA_REJOIN": "standby", "KEEL_HA_NODE_NAME": "db"}
	if !reflect.DeepEqual(root, want) {
		t.Errorf("root vars %v", root)
	}
	if v := DataVars(w, top, "db-replica-1", ""); v["KEEL_HA_ROLE"] != "primary" || v["KEEL_HA_REJOIN"] != RejoinReclone {
		t.Errorf("primary vars %v", v)
	}
	// postgres edge는 주 노드만 본다
	if e := EdgeVars(w, top); e["KEEL_HA_NODES"] != "${{db-replica-1.KEEL_PRIVATE_DOMAIN}}:5432" {
		t.Errorf("edge %v", e)
	}
	rw := catalog.Templates["redis-ha"].Definition.HACompanionRoot().ClusterWiring
	rtop := Topology{RootName: "cache", Primary: "cache", Data: []string{"cache", "cache-replica-1", "cache-replica-2"}, Edge: "cache-ha"}
	rv := DataVars(rw, rtop, "cache-replica-2", "")
	if rv["KEEL_HA_QUORUM"] != "2" || rv["KEEL_HA_PEERS"] != "${{cache.KEEL_PRIVATE_DOMAIN}},${{cache-replica-1.KEEL_PRIVATE_DOMAIN}},${{cache-replica-2.KEEL_PRIVATE_DOMAIN}}" ||
		rv["KEEL_HA_PRIMARY"] != "${{cache.KEEL_PRIVATE_DOMAIN}}" {
		t.Errorf("redis vars %v", rv)
	}
	if _, ok := rv["KEEL_HA_ROLE"]; ok {
		t.Error("redis has no role variable")
	}
	if e := EdgeVars(rw, rtop); strings.Count(e["KEEL_HA_NODES"], ":6379") != 3 {
		t.Errorf("redis edge %v", e)
	}
	// 조정 노드(internal) 배선
	iw := &catalog.ClusterWiring{InternalNodeNameVariable: "ETCD_NAME", InternalNodesVariable: "ETCD_INITIAL_CLUSTER",
		InternalNodeFormat: "{name}=http://{host}:2380", CoordinatorHostsVariable: "ETCD_HOSTS", CoordinatorPort: 2379}
	itop := Topology{RootName: "pg", Primary: "pg", Data: []string{"pg"}, Internal: []string{"pg-etcd-1", "pg-etcd-2"}}
	if v := InternalVars(iw, itop, "pg-etcd-2"); v["ETCD_NAME"] != "pg-etcd-2" ||
		v["ETCD_INITIAL_CLUSTER"] != "pg-etcd-1=http://${{pg-etcd-1.KEEL_PRIVATE_DOMAIN}}:2380,pg-etcd-2=http://${{pg-etcd-2.KEEL_PRIVATE_DOMAIN}}:2380" {
		t.Errorf("internal %v", v)
	}
	if v := DataVars(iw, itop, "pg", ""); v["ETCD_HOSTS"] != "${{pg-etcd-1.KEEL_PRIVATE_DOMAIN}}:2379,${{pg-etcd-2.KEEL_PRIVATE_DOMAIN}}:2379" {
		t.Errorf("coordinator hosts %v", v)
	}
	if len(WiringKeys(w)) != 5 {
		t.Errorf("wiring keys %v", WiringKeys(w))
	}
}

func TestMigrateRefs(t *testing.T) {
	keys := map[string]bool{"DATABASE_URL": true, "PGHOST": true}
	in := "${{db.DATABASE_URL}} ${{ db.PGHOST }} ${{db.OTHER}} ${{dbx.DATABASE_URL}} ${{shared.DATABASE_URL}} ${{DATABASE_URL}}"
	want := "${{db-ha.DATABASE_URL}} ${{db-ha.PGHOST}} ${{db.OTHER}} ${{dbx.DATABASE_URL}} ${{shared.DATABASE_URL}} ${{DATABASE_URL}}"
	if got := MigrateRefs(in, "db", "db-ha", keys); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := MigrateRefs(want, "db-ha", "db", keys); !strings.HasPrefix(got, "${{db.DATABASE_URL}} ${{db.PGHOST}}") {
		t.Errorf("reverse: %s", got)
	}
	if MigrateRefs("plain", "db", "x", keys) != "plain" {
		t.Error("plain")
	}
}

func TestParseProbe(t *testing.T) {
	p, err := ParseProbe("postgres", "f|0/3000148|2|\n")
	if err != nil || p.Role != "primary" || p.Offset != 0x3000148 || p.Followers != 2 {
		t.Errorf("pg primary %+v %v", p, err)
	}
	p, err = ParseProbe("postgres", "t|1/A0|0|streaming")
	if err != nil || p.Role != "replica" || p.Offset != 1<<32|0xA0 || !p.Streaming {
		t.Errorf("pg replica %+v %v", p, err)
	}
	if _, err := ParseProbe("postgres", "psql: error"); err == nil {
		t.Error("pg garbage")
	}
	p, err = ParseProbe("redis", "# Replication\r\nrole:master\r\nconnected_slaves:2\r\nmaster_repl_offset:1234\r\n")
	if err != nil || p.Role != "primary" || p.Followers != 2 || p.Offset != 1234 {
		t.Errorf("redis master %+v %v", p, err)
	}
	p, err = ParseProbe("redis", "role:slave\r\nmaster_host:cache.production.keel.internal\r\nmaster_link_status:up\r\nslave_repl_offset:99\r\n")
	if err != nil || p.Role != "replica" || !p.Streaming || p.Upstream != "cache.production.keel.internal" || p.Offset != 99 {
		t.Errorf("redis replica %+v %v", p, err)
	}
	p, err = ParseProbe("mongo", `{"isWritablePrimary":true,"setName":"rs0","me":"a:27017","primary":"a:27017","hosts":["a:27017","b:27017"]}`)
	if err != nil || p.Role != "primary" || p.Members != 2 {
		t.Errorf("mongo primary %+v %v", p, err)
	}
	p, err = ParseProbe("mongo", `{"isWritablePrimary":false,"secondary":true,"setName":"rs0","primary":"a:27017"}`)
	if err != nil || p.Role != "replica" || p.Upstream != "a:27017" {
		t.Errorf("mongo secondary %+v %v", p, err)
	}
	if p, _ := ParseProbe("mongo", `{"isWritablePrimary":true}`); p.Role != "uninitialized" {
		t.Errorf("mongo standalone %+v", p)
	}
}

func TestCommandsKeepSecretsOutOfArgv(t *testing.T) {
	vs := map[string]string{"PGPASSWORD": "pg-secret", "REDISPASSWORD": "r-secret", "MONGOUSER": "mongo", "MONGOPASSWORD": "m-secret"}
	cmds := []Command{PromoteCommand(vs), RedisPriorityCommand(vs, 1), SentinelFailoverCommand(vs),
		MongoReconcileCommand(vs, "rs0", []string{"a:27017"}), MongoPreferCommand(vs, "a:27017"), MongoStandaloneCommand(vs, "a:27017")}
	for _, k := range []string{"postgres", "redis", "mongo"} {
		c, err := ProbeCommand(k, vs)
		if err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		argv := strings.Join(c.Cmd, " ") + string(c.Stdin)
		for _, s := range []string{"pg-secret", "r-secret", "m-secret"} {
			if strings.Contains(argv, s) {
				t.Errorf("secret %s in argv/stdin: %v", s, c.Cmd)
			}
		}
	}
	if !strings.Contains(strings.Join(SentinelFailoverCommand(vs).Cmd, " "), "-p 26379") {
		t.Error("sentinel port")
	}
	if _, err := ProbeCommand("mysql", vs); err == nil {
		t.Error("mysql probe")
	}
}

func TestPickPromotion(t *testing.T) {
	st := []MemberState{
		{ServiceID: "root", OK: false, Fails: 3},
		{ServiceID: "r1", OK: true, Probe: Probe{Role: "replica", Offset: 100}},
		{ServiceID: "r2", OK: true, Probe: Probe{Role: "replica", Offset: 300}},
		{ServiceID: "r3", OK: false, Probe: Probe{Role: "replica", Offset: 900}},
		{ServiceID: "r4", OK: true, Probe: Probe{Role: "primary", Offset: 999}},
	}
	if id, ok := PickPromotion(st, "root"); !ok || id != "r2" {
		t.Errorf("got %s %v", id, ok)
	}
	if _, ok := PickPromotion(st[:1], "root"); ok {
		t.Error("no candidate")
	}
	if LagBytes(500, 300) != 200 || LagBytes(300, 500) != 0 {
		t.Error("lag")
	}
}

func TestObservedPrimary(t *testing.T) {
	st := []MemberState{
		{ServiceID: "a", OK: true, Probe: Probe{Role: "primary", Followers: 0}},
		{ServiceID: "b", OK: true, Probe: Probe{Role: "primary", Followers: 2}},
		{ServiceID: "c", OK: true, Probe: Probe{Role: "replica"}},
	}
	if id, ok := ObservedPrimary(st, "a"); !ok || id != "b" {
		t.Errorf("got %s", id)
	}
	st[1].Probe.Followers = 0
	if id, _ := ObservedPrimary(st, "b"); id != "b" {
		t.Errorf("tie should keep recorded: %s", id)
	}
	if _, ok := ObservedPrimary(st[2:], "a"); ok {
		t.Error("no primary")
	}
}

func TestBuiltinCompanions(t *testing.T) {
	for _, key := range []string{"postgres", "redis", "mongo"} {
		std := catalog.Templates[key].Definition
		code := std.HATemplateCode()
		comp, ok := catalog.Templates[code]
		if !ok {
			t.Fatalf("%s: companion %q missing", key, code)
		}
		root := comp.Definition.HACompanionRoot()
		if root == nil || root.RevertsToTemplateCode != key || root.Source.Database != key {
			t.Errorf("%s: companion root %+v", key, root)
		}
		if comp.Definition.HAPrototype(catalog.RoleReplica) == nil {
			t.Errorf("%s: no replica prototype", key)
		}
		if HasEdge(key) != (comp.Definition.HAPrototype(catalog.RoleEdge) != nil) {
			t.Errorf("%s: edge prototype mismatch", key)
		}
		if !std.IsHACompanion() == false || !comp.Definition.IsHACompanion() {
			t.Errorf("%s: IsHACompanion", key)
		}
	}
}
