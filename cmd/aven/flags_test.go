package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseFlagValueInfersAndValidatesTypes(t *testing.T) {
	cases := []struct{ raw, typ, wantType, wantJSON string }{
		{"true", "", "bool", "true"},
		{"100", "", "number", "100"},
		{"-2.5", "", "number", "-2.5"},
		{`{"name":"small","temperature":0.2}`, "", "json", `{"name":"small","temperature":0.2}`},
		{`[1,2]`, "", "json", `[1,2]`},
		{"blue", "", "string", `"blue"`},
		{`"blue"`, "", "string", `"blue"`},
		{"null", "", "string", `"null"`},
		{"123", "string", "string", `"123"`},
		{`"x"`, "string", "string", `"x"`},
		{"1e3", "number", "number", "1e3"},
		{"true", "json", "json", "true"},
	}
	for _, c := range cases {
		typ, v, err := parseFlagValue(c.raw, c.typ)
		if err != nil || typ != c.wantType || string(v) != c.wantJSON {
			t.Errorf("parseFlagValue(%q, %q) = %s %s %v; want %s %s", c.raw, c.typ, typ, v, err, c.wantType, c.wantJSON)
		}
	}
	for _, c := range [][2]string{{"yes", "bool"}, {"abc", "number"}, {`"5"`, "number"}, {"{", "json"}, {"1", "list"}} {
		if _, _, err := parseFlagValue(c[0], c[1]); err == nil {
			t.Errorf("parseFlagValue(%q, %q) accepted", c[0], c[1])
		}
	}
}

func TestReorderFlagArgs(t *testing.T) {
	got := reorderFlagArgs([]string{"checkout.v2", "true", "--when", `plan == "pro" && bucket(key) < 0.25`, "--force", "--rule-id=x"}, valueOpts("when", "rule-id"))
	want := []string{"--when", `plan == "pro" && bucket(key) < 0.25`, "--force", "--rule-id=x", "--", "checkout.v2", "true"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder: %q", got)
	}
	if got := reorderFlagArgs([]string{"limit", "-5", "-e", "staging"}, valueOpts()); !reflect.DeepEqual(got, []string{"-e", "staging", "--", "limit", "-5"}) {
		t.Fatalf("negative number: %q", got)
	}
}

func TestRuleHelpers(t *testing.T) {
	id := defaultRuleID("checkout.v2", `plan == "pro"`)
	if id != defaultRuleID("checkout.v2", `plan == "pro"`) || id == defaultRuleID("checkout.v2", `plan == "free"`) || !strings.HasPrefix(id, "rule-") {
		t.Fatalf("defaultRuleID not stable/unique: %s", id)
	}
	rules := []cliFlagRule{{ID: "a", When: "x == 1", Value: json.RawMessage("true")}}
	rules = upsertRule(rules, cliFlagRule{ID: "a", When: "x == 2", Value: json.RawMessage("false")})
	rules = upsertRule(rules, cliFlagRule{ID: "b", When: "true", Value: json.RawMessage("true")})
	if len(rules) != 2 || rules[0].When != "x == 2" || rules[1].ID != "b" {
		t.Fatalf("upsertRule: %+v", rules)
	}
	if _, err := parseFlagScope("project:prj_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseFlagScope("prj_1"); err == nil {
		t.Fatal("scope without project: prefix accepted")
	}
}

func TestFlagTargetPrefersProjectToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/feature-flags" || r.Header.Get("Authorization") != "Bearer keel_proj" {
			w.WriteHeader(403)
			return
		}
		json.NewEncoder(w).Encode(cliFlagSnapshot{ProjectID: "prj_tok", EnvironmentID: "env_tok"})
	}))
	defer srv.Close()
	t.Setenv("KEEL_FLAGS_SCOPE", "")
	t.Setenv("KEEL_TOKEN", "keel_proj")
	c := &config{URL: srv.URL, Links: map[string]*link{}}
	if pid, eid := flagTarget(c, flagOpts{}); pid != "prj_tok" || eid != "env_tok" {
		t.Fatalf("token target: %s %s", pid, eid)
	}
	t.Setenv("KEEL_FLAGS_SCOPE", "project:prj_scope")
	if pid, eid := flagTarget(c, flagOpts{}); pid != "prj_scope" || eid != "" {
		t.Fatalf("scope target: %s %s", pid, eid)
	}
}

func TestWriteFlagList(t *testing.T) {
	var b bytes.Buffer
	writeFlagList(&b, []cliFeatureFlag{
		{Name: "checkout.v2", Type: "bool", DefaultValue: json.RawMessage("false"), Rules: []cliFlagRule{{ID: "ent", When: `plan == "enterprise"`, Value: json.RawMessage("true")}}},
		{Name: "theme", Type: "string", DefaultValue: json.RawMessage(`"blue"`)},
	}, true)
	out := b.String()
	for _, want := range []string{"checkout.v2", "ent", `when plan == "enterprise"`, "serve true", "theme", "(none)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}
