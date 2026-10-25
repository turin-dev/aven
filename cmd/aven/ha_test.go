package main

import (
	"strings"
	"testing"
)

func TestParseHAArgs(t *testing.T) {
	o, err := parseHAArgs(nil)
	if err != nil || o.sub != "status" {
		t.Fatalf("default: %+v %v", o, err)
	}
	o, err = parseHAArgs([]string{"convert", "--replicas", "3", "--edge", "2", "--no-auto-failover", "-y", "-s", "db"})
	if err != nil || *o.replicas != 3 || *o.edge != 2 || !o.noAuto || !o.yes || o.svc != "db" {
		t.Fatalf("convert: %+v %v", o, err)
	}
	b := o.convertBody()
	if b["replicas"] != 3 || b["edge"] != 2 || b["auto_failover"] != false || b["skip_backup"] != false {
		t.Errorf("body %v", b)
	}
	if _, ok := b["internal"]; ok {
		t.Error("internal not given")
	}
	if o, err := parseHAArgs([]string{"switchover", "--to", "db-replica-1"}); err != nil || o.to != "db-replica-1" {
		t.Errorf("switchover: %+v %v", o, err)
	}
	if o, err := parseHAArgs([]string{"auto-failover", "off", "-e", "staging"}); err != nil || o.toggle != "off" || o.env != "staging" {
		t.Errorf("auto-failover: %+v %v", o, err)
	}
	for _, bad := range [][]string{{"scale"}, {"switchover"}, {"auto-failover"}, {"auto-failover", "maybe"}, {"bogus"},
		{"convert", "--replicas", "0"}, {"revert", "--replicas", "2"}, {"status", "extra"}} {
		if _, err := parseHAArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestFormatHA(t *testing.T) {
	lag := uint64(128)
	st := &haStatus{Cluster: &haCluster{Kind: "postgres", Status: "active", AutoFailover: true, KeelFailover: true,
		Message: "db-replica-1를 주 노드로 승격했습니다(failover)",
		Members: []haMember{
			{Name: "db", Role: "root", DBRole: "replica", Healthy: true, NodeID: "node_local", LagBytes: &lag},
			{Name: "db-replica-1", Role: "replica", DBRole: "primary", Primary: true, Healthy: true, NodeID: "nod_b"},
			{Name: "db-ha", Role: "edge", Healthy: false, InstanceStatus: "crashed", NodeID: "node_local"},
		}}}
	out := formatHA("db", "production", st)
	for _, want := range []string{"postgres HA cluster, active, automatic failover on", "last event:", "db-replica-1  replica  primary", "128 B", "unhealthy (crashed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	st = &haStatus{Problems: []string{"no volume"}}
	if out := formatHA("db", "production", st); !strings.Contains(out, "standalone") || !strings.Contains(out, "! no volume") {
		t.Errorf("standalone: %s", out)
	}
}
