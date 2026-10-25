package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

type cliFlagRule struct {
	ID    string          `json:"id"`
	When  string          `json:"when"`
	Value json.RawMessage `json:"value"`
}

type cliFeatureFlag struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	EnvironmentID string          `json:"environment_id"`
	DefaultValue  json.RawMessage `json:"default_value"`
	Rules         []cliFlagRule   `json:"rules"`
	Configured    bool            `json:"configured"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

type cliFlagSnapshot struct {
	ProjectID     string           `json:"project_id"`
	EnvironmentID string           `json:"environment_id"`
	Flags         []cliFeatureFlag `json:"flags"`
}

// flagOpts are the options every `aven flag` subcommand accepts.
type flagOpts struct {
	env, scope string
	json       bool
}

func (o *flagOpts) register(fl *flag.FlagSet) {
	fl.StringVar(&o.env, "environment", "", "environment name or id")
	fl.StringVar(&o.env, "e", "", "environment name or id")
	fl.StringVar(&o.scope, "scope", "", "project:<id> overrides the linked project")
	fl.BoolVar(&o.json, "json", false, "print JSON")
}

var flagCommonValueOpts = map[string]bool{"environment": true, "e": true, "scope": true}

func valueOpts(extra ...string) map[string]bool {
	m := map[string]bool{}
	for k := range flagCommonValueOpts {
		m[k] = true
	}
	for _, k := range extra {
		m[k] = true
	}
	return m
}

func linkedIfPresent(c *config) *link {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if l := c.Links[d]; l != nil {
			return l
		}
		if d == filepath.Dir(d) {
			return nil
		}
	}
}

// parseFlagScope reads `project:<id>` (--scope or KEEL_FLAGS_SCOPE).
func parseFlagScope(scope string) (string, error) {
	pid, ok := strings.CutPrefix(scope, "project:")
	if !ok || pid == "" {
		return "", fmt.Errorf("scope must look like project:<id>, got %q", scope)
	}
	return pid, nil
}

// flagTarget picks the project and environment: --scope/KEEL_FLAGS_SCOPE, then the project owning a
// project/environment KEEL_TOKEN, then the linked directory. --environment overrides the environment.
func flagTarget(c *config, o flagOpts) (string, string) {
	var pid, eid string
	scope := o.scope
	if scope == "" {
		scope = os.Getenv("KEEL_FLAGS_SCOPE")
	}
	l := linkedIfPresent(c)
	switch {
	case scope != "":
		p, err := parseFlagScope(scope)
		if err != nil {
			die("%v", err)
		}
		pid = p
		if l != nil && l.ProjectID == pid {
			eid = l.EnvironmentID
		}
	default:
		if os.Getenv("KEEL_TOKEN") != "" {
			var snapshot cliFlagSnapshot
			if err := c.call("GET", "/feature-flags", nil, &snapshot); err == nil {
				pid, eid = snapshot.ProjectID, snapshot.EnvironmentID
				break
			} else if l == nil {
				die("%v (use a project-scoped KEEL_TOKEN, run aven link, or pass --scope project:<id>)", err)
			}
		}
		if l == nil {
			die("this directory is not linked (run: aven link), or set a project-scoped KEEL_TOKEN")
		}
		pid, eid = l.ProjectID, l.EnvironmentID
	}
	if o.env != "" {
		p := c.project(pid)
		e := findEnv(p, o.env)
		if e == nil {
			die("environment %q not found (%s)", o.env, names(p.Environments, func(e envRef) string { return e.Name }))
		}
		eid = e.ID
	}
	return pid, eid
}

func flagSnapshot(c *config, pid, eid string) cliFlagSnapshot {
	var snapshot cliFlagSnapshot
	path := "/projects/" + url.PathEscape(pid) + "/flags"
	if eid != "" {
		path += "?environment_id=" + url.QueryEscape(eid)
	}
	if err := c.call("GET", path, nil, &snapshot); err != nil {
		die("%v", err)
	}
	return snapshot
}

func flagPath(pid, name string) string {
	return "/projects/" + url.PathEscape(pid) + "/flags/" + url.PathEscape(name)
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func cmdFlag(c *config, args []string) {
	if len(args) == 0 {
		die("usage: aven flag list|set|unset|delete (see docs/cli.md)")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list", "ls":
		flagList(c, args)
	case "set":
		flagSet(c, args)
	case "unset":
		flagUnset(c, args)
	case "delete", "rm", "remove":
		flagDelete(c, args)
	default:
		die("unknown flag command %q (use list, set, unset or delete)", sub)
	}
}

// reorderFlagArgs moves options before positionals so `aven flag set name value --when x` parses with package flag.
func reorderFlagArgs(args []string, valueFlags map[string]bool) []string {
	options, positional := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" || isNumberArg(a) {
			positional = append(positional, a)
			continue
		}
		options = append(options, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if valueFlags[name] && !hasValue && i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return append(append(options, "--"), positional...)
}

// isNumberArg keeps negative numbers (`aven flag set limit -5`) positional.
func isNumberArg(a string) bool {
	var n json.Number
	return json.Unmarshal([]byte(a), &n) == nil
}

func flagList(c *config, args []string) {
	var o flagOpts
	fl := flag.NewFlagSet("flag list", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	o.register(fl)
	full := fl.Bool("full", false, "show rule IDs and empty rule sections")
	if err := fl.Parse(reorderFlagArgs(args, valueOpts())); err != nil || fl.NArg() != 0 {
		die("usage: aven flag list [--full] [--environment env] [--scope project:<id>] [--json]")
	}
	pid, eid := flagTarget(c, o)
	snapshot := flagSnapshot(c, pid, eid)
	sort.Slice(snapshot.Flags, func(i, j int) bool { return snapshot.Flags[i].Name < snapshot.Flags[j].Name })
	if o.json {
		printJSON(snapshot)
		return
	}
	writeFlagList(os.Stdout, snapshot.Flags, *full)
}

func writeFlagList(out io.Writer, flags []cliFeatureFlag, full bool) {
	if len(flags) == 0 {
		fmt.Fprintln(out, "no feature flags (create one: aven flag set <name> <value>)")
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for i, f := range flags {
		if full && i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d rule(s)\n", f.Name, f.Type, string(f.DefaultValue), len(f.Rules))
		if !full {
			continue
		}
		if len(f.Rules) == 0 {
			fmt.Fprintln(w, "  rules:\t(none)\t\t")
		}
		for _, rule := range f.Rules {
			fmt.Fprintf(w, "  %s\twhen %s\tserve %s\t\n", rule.ID, rule.When, string(rule.Value))
		}
	}
	_ = w.Flush()
}

func findFlag(snapshot cliFlagSnapshot, name string) *cliFeatureFlag {
	for i := range snapshot.Flags {
		if snapshot.Flags[i].Name == name {
			return &snapshot.Flags[i]
		}
	}
	return nil
}

func defaultRuleID(name, when string) string {
	h := sha256.Sum256([]byte(name + "\x00" + when))
	return "rule-" + hex.EncodeToString(h[:6])
}

// upsertRule replaces the rule with the same id, or appends it.
func upsertRule(rules []cliFlagRule, rule cliFlagRule) []cliFlagRule {
	out := append([]cliFlagRule(nil), rules...)
	for i := range out {
		if out[i].ID == rule.ID {
			out[i] = rule
			return out
		}
	}
	return append(out, rule)
}

func flagSet(c *config, args []string) {
	var o flagOpts
	fl := flag.NewFlagSet("flag set", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	o.register(fl)
	when := fl.String("when", "", "targeting expression; serve the value when it matches")
	ruleID := fl.String("rule-id", "", "stable rule ID for --when")
	flagType := fl.String("type", "", "flag type: bool, string, number or json")
	force := fl.Bool("force", false, "allow changing an existing flag's type (clears its rules)")
	if err := fl.Parse(reorderFlagArgs(args, valueOpts("when", "rule-id", "type"))); err != nil || fl.NArg() != 2 {
		die("usage: aven flag set <name> <value> [--when expression] [--rule-id id] [--type bool|string|number|json] [--force]")
	}
	name, rawValue := fl.Arg(0), fl.Arg(1)
	pid, eid := flagTarget(c, o)
	snapshot := flagSnapshot(c, pid, eid)
	if eid == "" {
		eid = snapshot.EnvironmentID
	}
	existing := findFlag(snapshot, name)
	var result cliFeatureFlag

	if *when != "" {
		if existing == nil {
			die("flag %q does not exist; create it before adding a targeting rule", name)
		}
		if *flagType != "" && *flagType != existing.Type {
			die("--type cannot change a flag while adding a rule")
		}
		_, value, err := parseFlagValue(rawValue, existing.Type)
		if err != nil {
			die("%v", err)
		}
		id := *ruleID
		if id == "" {
			id = defaultRuleID(name, *when)
		}
		rules := upsertRule(existing.Rules, cliFlagRule{ID: id, When: *when, Value: value})
		result = flagUpdate(c, pid, eid, name, map[string]any{"rules": rules})
		if !o.json {
			fmt.Printf("set rule %s on %s: when %s serve %s\n", id, name, *when, string(value))
		}
	} else {
		if *ruleID != "" {
			die("--rule-id requires --when")
		}
		requested := *flagType
		if requested == "" && existing != nil {
			requested = existing.Type // 기존 플래그는 형식을 유지한다("123"은 string 플래그에서 문자열)
		}
		typ, value, err := parseFlagValue(rawValue, requested)
		if err != nil {
			die("%v", err)
		}
		switch {
		case existing == nil:
			if *force {
				die("--force is only used when changing an existing flag's type")
			}
			in := map[string]any{"name": name, "type": typ, "environment_id": eid, "default_value": value, "rules": []cliFlagRule{}}
			if err := c.call("POST", "/projects/"+url.PathEscape(pid)+"/flags", in, &result); err != nil {
				die("%v", err)
			}
			if !o.json {
				fmt.Printf("created %s (%s) = %s\n", name, typ, string(value))
			}
		case typ != existing.Type:
			if !*force {
				die("flag %q has type %s; changing it requires --force --type %s (clears its rules)", name, existing.Type, typ)
			}
			result = flagUpdate(c, pid, eid, name, map[string]any{"type": typ, "force": true, "default_value": value, "rules": []cliFlagRule{}})
			if !o.json {
				fmt.Printf("changed %s to %s = %s (rules cleared)\n", name, typ, string(value))
			}
		default:
			result = flagUpdate(c, pid, eid, name, map[string]any{"default_value": value})
			if !o.json {
				fmt.Printf("set %s default = %s\n", name, string(value))
			}
		}
	}
	if o.json {
		printJSON(result)
	}
}

// parseFlagValue turns a CLI argument into a typed JSON value. Without a type it infers one:
// true/false → bool, a number → number, other valid JSON → json (a JSON string → string), anything else → string.
func parseFlagValue(raw, requestedType string) (string, json.RawMessage, error) {
	switch requestedType {
	case "":
	case "bool":
		if raw != "true" && raw != "false" {
			return "", nil, errors.New("bool value must be true or false")
		}
		return requestedType, json.RawMessage(raw), nil
	case "number":
		var n json.Number
		if err := json.Unmarshal([]byte(raw), &n); err != nil || strings.HasPrefix(strings.TrimSpace(raw), `"`) {
			return "", nil, errors.New("number value must be a number")
		}
		return requestedType, json.RawMessage(n.String()), nil
	case "string":
		var s string
		if strings.HasPrefix(raw, `"`) && json.Unmarshal([]byte(raw), &s) == nil {
			raw = s // `"blue"` and blue are the same string
		}
		b, _ := json.Marshal(raw)
		return requestedType, b, nil
	case "json":
		if !json.Valid([]byte(raw)) {
			return "", nil, errors.New("json value must be valid JSON")
		}
		return requestedType, json.RawMessage(raw), nil
	default:
		return "", nil, errors.New("type must be bool, string, number or json")
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		switch value.(type) {
		case bool:
			return "bool", json.RawMessage(raw), nil
		case float64:
			return "number", json.RawMessage(raw), nil
		case string:
			return "string", json.RawMessage(raw), nil
		case nil:
		default:
			return "json", json.RawMessage(raw), nil
		}
	}
	b, _ := json.Marshal(raw)
	return "string", b, nil
}

func flagUpdate(c *config, pid, eid, name string, in map[string]any) cliFeatureFlag {
	in["environment_id"] = eid
	var updated cliFeatureFlag
	if err := c.call("PUT", flagPath(pid, name), in, &updated); err != nil {
		die("%v", err)
	}
	return updated
}

func flagUnset(c *config, args []string) {
	var o flagOpts
	fl := flag.NewFlagSet("flag unset", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	o.register(fl)
	ruleID := fl.String("rule-id", "", "targeting rule ID")
	if err := fl.Parse(reorderFlagArgs(args, valueOpts("rule-id"))); err != nil || fl.NArg() != 1 || *ruleID == "" {
		die("usage: aven flag unset <name> --rule-id id [--environment env]")
	}
	name := fl.Arg(0)
	pid, eid := flagTarget(c, o)
	snapshot := flagSnapshot(c, pid, eid)
	if eid == "" {
		eid = snapshot.EnvironmentID
	}
	f := findFlag(snapshot, name)
	if f == nil {
		die("flag %q not found", name)
	}
	rules := make([]cliFlagRule, 0, len(f.Rules))
	for _, rule := range f.Rules {
		if rule.ID != *ruleID {
			rules = append(rules, rule)
		}
	}
	if len(rules) == len(f.Rules) {
		die("rule %q not found on %s (see: aven flag list --full)", *ruleID, name)
	}
	result := flagUpdate(c, pid, eid, name, map[string]any{"rules": rules})
	if o.json {
		printJSON(result)
		return
	}
	fmt.Printf("removed rule %s from %s\n", *ruleID, name)
}

func flagDelete(c *config, args []string) {
	var o flagOpts
	fl := flag.NewFlagSet("flag delete", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	o.register(fl)
	if err := fl.Parse(reorderFlagArgs(args, valueOpts())); err != nil || fl.NArg() != 1 {
		die("usage: aven flag delete <name> [--scope project:<id>]")
	}
	name := fl.Arg(0)
	pid, _ := flagTarget(c, o)
	if err := c.call("DELETE", flagPath(pid, name), nil, nil); err != nil {
		die("%v", err)
	}
	if o.json {
		printJSON(map[string]any{"deleted": name})
		return
	}
	fmt.Printf("deleted %s (all environments)\n", name)
}
