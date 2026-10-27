package main

import (
	"bytes"
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

	"github.com/turin-dev/aven/internal/iac"
)

type iacChange struct {
	Action      string          `json:"action"`
	Resource    string          `json:"resource"`
	Field       string          `json:"field"`
	From        json.RawMessage `json:"from"`
	To          json.RawMessage `json:"to"`
	Destructive bool            `json:"destructive"`
	Detail      string          `json:"detail"`
}

type iacPlan struct {
	EnvironmentID string      `json:"environment_id"`
	Environment   string      `json:"environment"`
	Partial       string      `json:"partial"`
	ConfigETag    string      `json:"config_etag"`
	PlanHash      string      `json:"plan_hash"`
	Changes       []iacChange `json:"changes"`
	Claims        []string    `json:"claims"`
	Errors        []string    `json:"errors"`
	Warnings      []string    `json:"warnings"`
	Destructive   bool        `json:"destructive"`
	Summary       struct {
		Create  int `json:"create"`
		Change  int `json:"change"`
		Destroy int `json:"destroy"`
	} `json:"summary"`
}

// pinnedPlan은 plan --out이 쓰고 apply --plan이 읽는 파일이다(정의를 다시 평가하지 않는다).
type pinnedPlan struct {
	ProjectID     string          `json:"project_id"`
	EnvironmentID string          `json:"environment_id"`
	File          string          `json:"file"`
	Definition    *iac.Definition `json:"definition"`
	Plan          *iacPlan        `json:"plan"`
}

func cmdConfig(c *config, args []string) {
	if len(args) == 0 {
		die("usage: aven config import|plan|apply|partials (see aven help)")
	}
	switch args[0] {
	case "import", "pull":
		cmdConfigImport(c, args[1:])
	case "plan", "preview":
		cmdConfigPlan(c, args[1:], false)
	case "apply":
		cmdConfigPlan(c, args[1:], true)
	case "partials":
		cmdConfigPartials(c, args[1:])
	default:
		die("unknown config command %q (import, plan, apply, partials)", args[0])
	}
}

// configTarget는 연결된 프로젝트와 -e로 고른(없으면 연결된) 환경이다.
func configTarget(c *config, env string) (*link, *project) {
	l := *c.linked()
	p := c.project(l.ProjectID)
	if env != "" {
		e := findEnv(p, env)
		if e == nil {
			die("environment %q not found (%s)", env, names(p.Environments, func(e envRef) string { return e.Name }))
		}
		l.EnvironmentID, l.Environment = e.ID, e.Name
	}
	return &l, p
}

func configPath(l *link, rest ...string) string {
	p := "/projects/" + url.PathEscape(l.ProjectID) + "/environments/" + url.PathEscape(l.EnvironmentID) + "/config"
	return p + strings.Join(rest, "")
}

func relPath(p string) string {
	if wd, err := os.Getwd(); err == nil {
		if r, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}

func cmdConfigImport(c *config, args []string) {
	fl := flag.NewFlagSet("config import", flag.ExitOnError)
	file := fl.String("file", "", "file to write (default: the existing .aven/aven.* or .aven/aven.ts; .keel still works)")
	format := fl.String("format", "", "ts | json | yaml (default: from --file, else ts)")
	include := fl.Bool("include-variables", false, "inline non-sealed variable values (secrets!) instead of preserve()")
	force := fl.Bool("force", false, "overwrite an existing file")
	asJSON := fl.Bool("json", false, "print the definition as JSON instead of writing files")
	env := fl.String("e", "", "environment name or id (default: linked)")
	fl.StringVar(env, "environment", "", "environment name or id (default: linked)")
	fl.Parse(reorderConfigFlags(args))
	l, _ := configTarget(c, *env)
	q := ""
	if *include {
		q = "?include_variables=1"
		fmt.Fprintln(os.Stderr, "warning: --include-variables writes non-sealed variable values, including secrets, into the file; sealed variables stay preserve()")
	}
	var d iac.Definition
	if err := c.call("GET", configPath(l, q), nil, &d); err != nil {
		die("%v", err)
	}
	if *asJSON {
		b, err := iac.MarshalJSON(&d)
		if err != nil {
			die("%v", err)
		}
		os.Stdout.Write(b)
		return
	}
	path := *file
	if path == "" {
		wd, _ := os.Getwd()
		if found, err := iac.FindFile(wd); err == nil {
			path = found
		} else {
			name := "aven.ts"
			if *format == "json" || *format == "yaml" {
				name = "aven." + *format
			}
			path = filepath.Join(wd, iac.Dir, name)
		}
	}
	if *format == "" {
		*format = iac.Format(path)
	}
	if _, err := os.Stat(path); err == nil && !*force {
		die("%s already exists (pass --force to overwrite it)", relPath(path))
	}
	var out []byte
	var err error
	switch *format {
	case "ts":
		out = iac.RenderTS(&d)
		err = iac.WriteSupportFiles(filepath.Dir(path))
	case "json":
		out, err = iac.MarshalJSON(&d)
	case "yaml", "yml":
		out, err = iac.MarshalYAML(&d)
	default:
		die("--format must be ts, json or yaml")
	}
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, out, 0o644)
	}
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("wrote %s (%d services from %s)\n", relPath(path), len(d.Services), l.Environment)
	fmt.Println("next: aven config plan  (a clean import shows no changes)")
}

// loadDefinition은 --file 또는 가장 가까운 .aven/aven.*를 평가한다(.keel도 찾는다).
func loadDefinition(file, command string, l *link, p *project) (string, *iac.Definition) {
	if file == "" {
		wd, _ := os.Getwd()
		found, err := iac.FindFile(wd)
		if err != nil {
			die("%v", err)
		}
		file = found
	}
	d, err := iac.Load(file, iac.Context{Command: command, ProjectID: p.ID, ProjectName: p.Name, EnvironmentID: l.EnvironmentID, Environment: l.Environment})
	if err != nil {
		die("%v", err)
	}
	return file, d
}

func cmdConfigPlan(c *config, args []string, apply bool) {
	name := "config plan"
	if apply {
		name = "config apply"
	}
	fl := flag.NewFlagSet(name, flag.ExitOnError)
	file := fl.String("file", "", "configuration file (default: nearest .aven/aven.ts|json|yaml; .keel still works)")
	env := fl.String("e", "", "environment name or id (default: linked)")
	fl.StringVar(env, "environment", "", "environment name or id (default: linked)")
	asJSON := fl.Bool("json", false, "machine-readable output")
	show := fl.Bool("show-values", false, "print variable values instead of «hidden»")
	detailed, out, yes, destructive, deploy, pinned := new(bool), new(string), new(bool), new(bool), new(bool), new(string)
	if apply {
		fl.BoolVar(yes, "y", false, "apply without asking")
		fl.BoolVar(yes, "yes", false, "apply without asking")
		fl.BoolVar(destructive, "confirm-destructive", false, "allow destructive changes non-interactively")
		fl.BoolVar(deploy, "deploy", false, "deploy the created/changed services after applying (default: stage only)")
		fl.StringVar(pinned, "plan", "", "apply a pinned plan written by aven config plan --out")
	} else {
		fl.BoolVar(detailed, "detailed-exit-code", false, "exit 2 when changes are pending, 0 when none")
		fl.StringVar(out, "out", "", "write a pinned plan file for aven config apply --plan")
	}
	fl.Parse(reorderConfigFlags(args))
	l, p := configTarget(c, *env)

	var path string
	var d *iac.Definition
	var plan iacPlan
	if *pinned != "" {
		b, err := os.ReadFile(*pinned)
		if err != nil {
			die("%v", err)
		}
		var pp pinnedPlan
		if err := json.Unmarshal(b, &pp); err != nil || pp.Definition == nil || pp.Plan == nil {
			die("%s is not a aven config plan --out file", *pinned)
		}
		if pp.ProjectID != l.ProjectID || (*env == "" && pp.EnvironmentID != l.EnvironmentID) {
			die("%s was planned for another project or environment", *pinned)
		}
		l.EnvironmentID, l.Environment = pp.EnvironmentID, pp.Plan.Environment
		path, d, plan = pp.File, pp.Definition, *pp.Plan
	} else {
		cmd := "plan"
		if apply {
			cmd = "apply"
		}
		path, d = loadDefinition(*file, cmd, l, p)
		if err := c.call("POST", configPath(l, "/plan"), map[string]any{"definition": d, "show_values": *show}, &plan); err != nil {
			die("%v", err)
		}
	}
	pending := len(plan.Changes) > 0 || len(plan.Claims) > 0
	if *asJSON && !apply {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(plan)
	} else if !*asJSON {
		fmt.Print(formatIACPlan(relPath(path), &plan, apply))
	}
	if len(plan.Errors) > 0 {
		os.Exit(1)
	}
	if *out != "" {
		b, _ := json.MarshalIndent(pinnedPlan{ProjectID: l.ProjectID, EnvironmentID: l.EnvironmentID, File: relPath(path), Definition: d, Plan: &plan}, "", "  ")
		if err := os.WriteFile(*out, append(b, '\n'), 0o600); err != nil {
			die("%v", err)
		}
		if !*asJSON {
			fmt.Printf("wrote pinned plan %s (apply it with: aven config apply --plan %s)\n", *out, *out)
		}
	}
	if !apply {
		if *detailed && pending {
			os.Exit(2)
		}
		return
	}
	if !pending {
		if *asJSON {
			fmt.Println(`{"services":[],"claimed":[]}`)
		}
		return
	}
	interactive := !*yes && !*asJSON && isTerminal(os.Stdin)
	if plan.Destructive && !interactive && !*destructive {
		die("the plan contains destructive changes; pass --confirm-destructive with --yes/--json to apply them")
	}
	if interactive {
		q := fmt.Sprintf("apply these changes to %s?", plan.Environment)
		if plan.Destructive {
			q = fmt.Sprintf("apply these changes to %s, INCLUDING the destructive ones marked !?", plan.Environment)
		}
		confirm(false, q)
	} else if !*yes && !*asJSON {
		die("pass --yes to apply non-interactively")
	}
	var res struct {
		Staged   bool `json:"staged"`
		Services []struct {
			Service       string  `json:"service"`
			Action        string  `json:"action"`
			DeploymentID  *string `json:"deployment_id"`
			DeploySkipped *string `json:"deploy_skipped"`
		} `json:"services"`
		Claimed []string `json:"claimed"`
	}
	body := map[string]any{"definition": d, "plan_hash": plan.PlanHash, "confirm_destructive": plan.Destructive, "deploy": *deploy}
	raw, err := c.rawCall("POST", configPath(l, "/apply"), body)
	if err != nil {
		die("%v", err)
	}
	if *asJSON {
		os.Stdout.Write(raw)
		return
	}
	json.Unmarshal(raw, &res)
	for _, s := range res.Services {
		line := fmt.Sprintf("%s %s", s.Action, s.Service)
		if s.DeploymentID != nil {
			line += " · deploying " + *s.DeploymentID
		} else if s.DeploySkipped != nil {
			line += " · not deployed: " + *s.DeploySkipped
		}
		fmt.Println(line)
	}
	if len(res.Claimed) > 0 {
		fmt.Printf("partial %s now owns %s\n", plan.Partial, strings.Join(res.Claimed, ", "))
	}
	if res.Staged && plan.Summary.Create+plan.Summary.Change > 0 {
		fmt.Println("changes are staged: they take effect on the next deploy (aven redeploy, or apply with --deploy)")
	}
}

// rawCall은 call과 같지만 응답 본문을 그대로 돌려준다(오류 응답의 plan 오류도 보여 준다).
func (c *config) rawCall(method, path string, in any) ([]byte, error) {
	b, _ := json.Marshal(in)
	resp, err := c.req(method, path, bytes.NewReader(b), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 {
		return nil, errors.New("unauthorized (run: aven login <url>)")
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	return raw, nil
}

func iacValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(none)"
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	return string(raw)
}

func formatIACPlan(file string, p *iacPlan, apply bool) string {
	var b strings.Builder
	b.WriteString("Aven configuration\n")
	fmt.Fprintf(&b, "  Using        %s\n", file)
	fmt.Fprintf(&b, "  Environment  %s\n", p.Environment)
	if p.Partial != "" {
		fmt.Fprintf(&b, "  Partial      %s\n", p.Partial)
	}
	b.WriteString("\n")
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	if len(p.Errors) > 0 {
		for _, e := range p.Errors {
			fmt.Fprintf(&b, "error: %s\n", e)
		}
		return b.String()
	}
	if len(p.Changes) == 0 && len(p.Claims) == 0 {
		b.WriteString("Your Aven configuration is already up to date.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Plan: %d to add, %d to change, %d to destroy\n\n", p.Summary.Create, p.Summary.Change, p.Summary.Destroy)
	byRes := map[string][]iacChange{}
	order := []string{}
	for _, ch := range p.Changes {
		if _, ok := byRes[ch.Resource]; !ok {
			order = append(order, ch.Resource)
		}
		byRes[ch.Resource] = append(byRes[ch.Resource], ch)
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	for _, res := range order {
		chs := byRes[res]
		kind, name, _ := strings.Cut(res, ".")
		head := chs[0]
		switch head.Action {
		case "create":
			detail := ""
			if head.Detail != "" {
				detail = " (" + head.Detail + ")"
			}
			fmt.Fprintf(&b, "+ Create %s %s%s\n", kind, name, detail)
		case "destroy":
			fmt.Fprintf(&b, "! Destroy %s %s (%s)\n", kind, name, head.Detail)
			continue
		default:
			fmt.Fprintf(&b, "~ Update %s %s\n", kind, name)
		}
		for _, ch := range chs {
			if ch.Action != "change" {
				continue
			}
			mark := " "
			if ch.Destructive {
				mark = "!"
			}
			switch {
			case len(ch.From) == 0:
				fmt.Fprintf(&b, "  %s + %s = %s\n", mark, ch.Field, iacValue(ch.To))
			case len(ch.To) == 0:
				fmt.Fprintf(&b, "  %s - %s\n", mark, ch.Field)
			default:
				fmt.Fprintf(&b, "  %s ~ %s: %s → %s\n", mark, ch.Field, iacValue(ch.From), iacValue(ch.To))
			}
		}
	}
	if len(p.Claims) > 0 {
		fmt.Fprintf(&b, "\nOwnership: partial %s claims %s\n", p.Partial, strings.Join(p.Claims, ", "))
	}
	if p.Destructive {
		b.WriteString("\nLines marked ! are destructive.\n")
	}
	if !apply {
		b.WriteString("\nNext\n  • Run aven config apply to apply these changes (saved as staged changes; deploy to roll them out).\n")
	}
	return b.String()
}

// configValueFlags는 config 명령에서 값을 받는 플래그다(reorderFlags의 valueFlag에 더한다).
var configValueFlags = map[string]bool{"file": true, "format": true, "out": true, "plan": true, "resource": true, "base-config-etag": true}

// reorderConfigFlags는 reorderFlags처럼 플래그를 위치 인자 앞으로 모은다(config 전용 값 플래그 포함).
func reorderConfigFlags(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(append(flags, pos...), args[i+1:]...)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(a, "=") && (valueFlag[name] || configValueFlags[name]) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// stringList는 반복 가능한 문자열 플래그다(--resource A --resource B).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func cmdConfigPartials(c *config, args []string) {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fl := flag.NewFlagSet("config partials "+sub, flag.ExitOnError)
	env := fl.String("e", "", "environment name or id (default: linked)")
	fl.StringVar(env, "environment", "", "environment name or id (default: linked)")
	asJSON := fl.Bool("json", false, "machine-readable output (release/transfer: execute without prompting unless --dry-run)")
	var resources stringList
	fl.Var(&resources, "resource", "owned address to select (repeatable; default: the whole partial)")
	dry := fl.Bool("dry-run", false, "preview the affected addresses without changing ownership")
	yes := fl.Bool("yes", false, "confirm without prompting")
	fl.BoolVar(yes, "y", false, "confirm without prompting")
	base := fl.String("base-config-etag", "", "require this config etag (from list or a dry run)")
	fl.Parse(reorderConfigFlags(args))
	l, _ := configTarget(c, *env)

	type ownership struct {
		EnvironmentID         string            `json:"environment_id"`
		EnvironmentName       string            `json:"environment_name"`
		ConfigETag            string            `json:"config_etag"`
		BaseConfigETag        string            `json:"base_config_etag"`
		Partials              map[string]string `json:"iac_partials"`
		WholeProjectAvailable bool              `json:"whole_project_available"`
		Affected              []string          `json:"affected_resources"`
		DryRun                bool              `json:"dry_run"`
	}
	printOwners := func(o ownership) {
		byPartial := map[string][]string{}
		for addr, owner := range o.Partials {
			byPartial[owner] = append(byPartial[owner], addr)
		}
		if len(byPartial) == 0 {
			fmt.Println("no named partials (whole-project configuration is available)")
			return
		}
		keys := make([]string, 0, len(byPartial))
		for k := range byPartial {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sort.Strings(byPartial[k])
			fmt.Printf("%s\n  %s\n", k, strings.Join(byPartial[k], "\n  "))
		}
	}
	switch sub {
	case "list":
		var o ownership
		raw, err := c.rawCall("GET", configPath(l, "/partials"), nil)
		if err != nil {
			die("%v", err)
		}
		if *asJSON {
			os.Stdout.Write(raw)
			return
		}
		json.Unmarshal(raw, &o)
		fmt.Printf("environment %s · config etag %s\n", o.EnvironmentName, o.ConfigETag)
		printOwners(o)
	case "release", "transfer":
		want := 1
		if sub == "transfer" {
			want = 2
		}
		if fl.NArg() != want {
			if sub == "transfer" {
				die("usage: aven config partials transfer <from> <to> [--resource ADDRESS]... [--dry-run] [--yes] [--json]")
			}
			die("usage: aven config partials release <partial> [--resource ADDRESS]... [--dry-run] [--yes] [--json]")
		}
		if *dry && *yes {
			die("--dry-run cannot be combined with --yes")
		}
		body := map[string]any{"partial": fl.Arg(0), "resources": []string(resources), "dry_run": true, "base_config_etag": *base}
		if sub == "transfer" {
			body["to"] = fl.Arg(1)
		}
		path := configPath(l, "/partials/"+sub)
		raw, err := c.rawCall("POST", path, body)
		if err != nil {
			die("%v", err)
		}
		var preview ownership
		json.Unmarshal(raw, &preview)
		if *dry {
			if *asJSON {
				os.Stdout.Write(raw)
				return
			}
			fmt.Printf("would %s %s: %s\n", sub, fl.Arg(0), strings.Join(preview.Affected, ", "))
			return
		}
		if !*asJSON {
			verb := "release from " + fl.Arg(0)
			if sub == "transfer" {
				verb = fmt.Sprintf("transfer from %s to %s", fl.Arg(0), fl.Arg(1))
			}
			fmt.Printf("%s:\n  %s\n", verb, strings.Join(preview.Affected, "\n  "))
			confirm(*yes, "change ownership of these resources (no resource is changed or deployed)?")
		}
		body["dry_run"], body["resources"], body["base_config_etag"] = false, preview.Affected, preview.BaseConfigETag
		raw, err = c.rawCall("POST", path, body)
		if err != nil {
			die("%v", err)
		}
		if *asJSON {
			os.Stdout.Write(raw)
			return
		}
		var o ownership
		json.Unmarshal(raw, &o)
		fmt.Printf("done (%d resources). Re-plan before the next apply.\n", len(o.Affected))
		printOwners(o)
	default:
		die("unknown partials command %q (list, release, transfer)", sub)
	}
}
