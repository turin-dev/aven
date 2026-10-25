package main

import (
	"encoding/json"
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/iac"
)

func TestReorderFlagSet(t *testing.T) {
	fl := flag.NewFlagSet("x", flag.ContinueOnError)
	var skips stringList
	fl.Var(&skips, "skip", "")
	yes := fl.Bool("yes", false, "")
	dir := fl.String("dir", "", "")
	args := []string{"a/b", "--skip", "example-org/sample-legacy", "--yes", "c/d", "--dir", "/tmp/x", "--skip=example-org/sample-mail"}
	if err := fl.Parse(reorderFlagSet(fl, args)); err != nil {
		t.Fatal(err)
	}
	if !*yes || *dir != "/tmp/x" || !reflect.DeepEqual([]string(skips), []string{"example-org/sample-legacy", "example-org/sample-mail"}) {
		t.Errorf("flags: yes=%v dir=%q skips=%v", *yes, *dir, skips)
	}
	if !reflect.DeepEqual(fl.Args(), []string{"a/b", "c/d"}) {
		t.Errorf("positional: %v", fl.Args())
	}
}

func TestWithoutPendingPaths(t *testing.T) {
	doms, _ := json.Marshal([]map[string]any{{"host": "x.example", "paths": []map[string]any{
		{"path": "/api/*", "service": "api"}, {"path": "/static/*"}}}})
	d := &iac.Definition{Version: 1, Services: []iac.Service{
		{Name: "web", Settings: map[string]json.RawMessage{"domains": doms}},
		{Name: "api"},
	}}
	first := withoutPendingPaths(d, func(string) bool { return false })
	if first == nil {
		t.Fatal("a rule to a new service must be held back")
	}
	got := string(first.Services[0].Settings["domains"])
	if strings.Contains(got, `"api"`) || !strings.Contains(got, "/static/*") {
		t.Errorf("first pass domains: %s", got)
	}
	if !strings.Contains(string(d.Services[0].Settings["domains"]), `"api"`) {
		t.Error("the full definition must not be modified")
	}
	if withoutPendingPaths(d, func(string) bool { return true }) != nil {
		t.Error("nothing to hold back when the target exists")
	}
}
