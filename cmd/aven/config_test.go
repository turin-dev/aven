package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReorderConfigFlags(t *testing.T) {
	got := reorderConfigFlags([]string{"transfer", "a", "--resource", "service.web", "b", "--file", "x.ts", "--dry-run"})
	want := []string{"--resource", "service.web", "--file", "x.ts", "--dry-run", "transfer", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestFormatIACPlan(t *testing.T) {
	var p iacPlan
	json.Unmarshal([]byte(`{"environment":"production","partial":"api","destructive":true,
		"summary":{"create":1,"change":1,"destroy":1},
		"changes":[
			{"action":"create","resource":"service.worker","detail":"image"},
			{"action":"change","resource":"service.web","field":"replicas","from":1,"to":2},
			{"action":"change","resource":"service.web","field":"variables.OLD","from":"«hidden»","destructive":true},
			{"action":"change","resource":"service.web","field":"variables.NEW","to":"«hidden»"},
			{"action":"destroy","resource":"service.old","destructive":true,"detail":"deletes the service in every environment"}],
		"claims":["service.web"]}`), &p)
	out := formatIACPlan(".keel/aven.ts", &p, false)
	for _, s := range []string{
		"Using        .keel/aven.ts", "Partial      api", "Plan: 1 to add, 1 to change, 1 to destroy",
		"+ Create service worker (image)", "~ Update service web", "    ~ replicas: 1 → 2", "  ! - variables.OLD",
		"    + variables.NEW = «hidden»", "! Destroy service old (deletes the service in every environment)",
		"partial api claims service.web", "Run aven config apply",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in\n%s", s, out)
		}
	}
	if out := formatIACPlan("f", &iacPlan{Environment: "e"}, false); !strings.Contains(out, "already up to date") {
		t.Errorf("empty plan:\n%s", out)
	}
	if out := formatIACPlan("f", &iacPlan{Errors: []string{"boom"}}, false); !strings.Contains(out, "error: boom") {
		t.Errorf("errors:\n%s", out)
	}
}
