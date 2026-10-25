package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/dokploy"
	"github.com/turin-dev/aven/internal/iac"
)

// ---- import dokploy: Dokploy → Aven migration commands ----
// Dokploy는 GET만 한다. aven 쪽은 IaC(partial "dokploy")와 compose API로 만들고 배포하지 않는다.

type importOpts struct {
	dokployURL string
	keyFile    string
	opts       dokploy.Options
	only       map[string]bool // aven 프로젝트 이름
	out        string
	asJSON     bool
	yes        bool
	dir        string
	offline    bool
}

func parseImportArgs(name string, args []string) (*importOpts, []string, error) {
	fl := flag.NewFlagSet("import dokploy "+name, flag.ContinueOnError)
	o := &importOpts{}
	fl.StringVar(&o.dokployURL, "dokploy-url", "", "Dokploy URL (default: $DOKPLOY_URL or http://127.0.0.1:3000)")
	fl.StringVar(&o.keyFile, "dokploy-key-file", "", "file with the Dokploy API key (default: $DOKPLOY_API_KEY)")
	var nodes, skips, moves, only, local stringList
	fl.Var(&nodes, "node", "dokploy-server=keel-node (repeatable; default: same name)")
	fl.Var(&skips, "skip", "project/name to leave out (Dokploy names, repeatable)")
	fl.Var(&moves, "move", "project/name=other-project: import that service into another project (repeatable)")
	fl.Var(&only, "project", "only this aven project (repeatable)")
	fl.Var(&local, "local-addr", "address of the Dokploy main server, e.g. its Tailscale IP (repeatable)")
	fl.BoolVar(&o.opts.KeepStandby, "keep-standby", false, "also import idle main copies of services that run on another server")
	fl.BoolVar(&o.opts.GitHubSSH, "github-ssh", false, "keep git@github.com URLs and use per-service deploy keys instead of aven's GitHub connection")
	fl.BoolVar(&o.opts.KeepNames, "keep-names", false, "keep -<server> suffixes instead of renaming to the standby's name")
	fl.StringVar(&o.out, "out", "", "write the plan (no secret values) as JSON to this file")
	fl.BoolVar(&o.asJSON, "json", false, "print JSON")
	fl.BoolVar(&o.yes, "yes", false, "apply without asking")
	fl.BoolVar(&o.yes, "y", false, "apply without asking")
	fl.StringVar(&o.dir, "dir", "", "export-compose: output directory")
	fl.BoolVar(&o.offline, "offline", false, "plan: do not ask aven to validate")
	if err := fl.Parse(reorderFlagSet(fl, args)); err != nil {
		return nil, nil, err
	}
	o.opts.NodeMap = map[string]string{}
	for _, n := range nodes {
		k, v, ok := strings.Cut(n, "=")
		if !ok || k == "" || v == "" {
			return nil, nil, fmt.Errorf("--node wants dokploy-server=keel-node, got %q", n)
		}
		o.opts.NodeMap[k] = v
	}
	o.opts.Skip = map[string]bool{}
	for _, s := range skips {
		if !strings.Contains(s, "/") {
			return nil, nil, fmt.Errorf("--skip wants project/name, got %q", s)
		}
		o.opts.Skip[s] = true
	}
	o.opts.Move = map[string]string{}
	for _, m := range moves {
		k, v, ok := strings.Cut(m, "=")
		if !ok || !strings.Contains(k, "/") || v == "" {
			return nil, nil, fmt.Errorf("--move wants project/name=project, got %q", m)
		}
		o.opts.Move[k] = v
	}
	o.opts.LocalAddrs = local
	o.only = map[string]bool{}
	for _, p := range only {
		o.only[dokploy.Slug(p)] = true
	}
	return o, fl.Args(), nil
}

// reorderFlagSet은 위치 인자 뒤에 온 플래그도 읽히게 플래그를 앞으로 모은다(값 플래그는 FlagSet으로 판단).
func reorderFlagSet(fl *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(append(flags, pos...), args[i+1:]...)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue
			}
			f := fl.Lookup(name)
			if f == nil {
				continue
			}
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !(ok && b.IsBoolFlag()) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func (o *importOpts) client() (*dokploy.Client, error) {
	u := o.dokployURL
	if u == "" {
		u = os.Getenv("DOKPLOY_URL")
	}
	if u == "" {
		u = "http://127.0.0.1:3000"
	}
	key := os.Getenv("DOKPLOY_API_KEY")
	if o.keyFile != "" {
		b, err := os.ReadFile(o.keyFile)
		if err != nil {
			return nil, err
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		return nil, errors.New("set DOKPLOY_API_KEY or --dokploy-key-file (the key is never taken from argv)")
	}
	return &dokploy.Client{URL: u, Key: key}, nil
}

func (o *importOpts) translate() (*dokploy.Plan, error) {
	cl, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	snap, err := cl.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	p := dokploy.Translate(snap, o.opts)
	if len(o.only) > 0 {
		var keep []*dokploy.ProjectPlan
		for _, pp := range p.Projects {
			if o.only[pp.Name] {
				keep = append(keep, pp)
			}
		}
		p.Projects = keep
	}
	return p, nil
}

func cmdImport(c *config, args []string) {
	if len(args) == 0 || args[0] != "dokploy" {
		die("usage: aven import dokploy scan|plan|apply|verify|export-compose (see aven help)")
	}
	args = args[1:]
	if len(args) == 0 {
		die("usage: aven import dokploy scan|plan|apply|verify|export-compose")
	}
	o, rest, err := parseImportArgs(args[0], args[1:])
	if err != nil {
		die("%v", err)
	}
	switch args[0] {
	case "scan":
		o.offline = true
		importPlan(c, o)
	case "plan":
		importPlan(c, o)
	case "apply":
		importApply(c, o)
	case "verify":
		importVerify(c, o)
	case "export-compose":
		importExportCompose(o, rest)
	default:
		die("unknown import command %q (scan, plan, apply, verify, export-compose)", args[0])
	}
}

// ---- aven 쪽 조회 ----

type keelProject struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Environments []envRef `json:"environments"`
	Services     []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		SourceType  string `json:"source_type"`
		StackID     string `json:"stack_id"`
		ComposeName string `json:"compose_name"`
	} `json:"services"`
}

func (c *config) keelProjectByName(name string) (*keelProject, error) {
	var list []keelProject
	if err := c.call("GET", "/projects", nil, &list); err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.Name == name {
			var full keelProject
			if err := c.call("GET", "/projects/"+url.PathEscape(p.ID), nil, &full); err != nil {
				return nil, err
			}
			return &full, nil
		}
	}
	return nil, nil
}

func (p *keelProject) env(name string) string {
	for _, e := range p.Environments {
		if e.Name == name {
			return e.ID
		}
	}
	return ""
}

func (p *keelProject) service(name string) string {
	for _, s := range p.Services {
		if s.Name == name {
			return s.ID
		}
	}
	return ""
}

type keelNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsLocal bool   `json:"is_local"`
	Status  string `json:"status"`
}

func (c *config) nodesByName() (map[string]keelNode, error) {
	var ns []keelNode
	if err := c.call("GET", "/nodes", nil, &ns); err != nil {
		return nil, err
	}
	m := map[string]keelNode{}
	for _, n := range ns {
		m[n.Name] = n
	}
	return m, nil
}

func definitionOf(pp *dokploy.ProjectPlan) *iac.Definition {
	d := &iac.Definition{Version: 1, ProjectName: pp.Name, Partial: dokploy.Partial}
	for _, sp := range pp.Services {
		if sp.Status == dokploy.Ready {
			d.Services = append(d.Services, sp.Service)
		}
	}
	return d
}

// withoutPendingPaths는 아직 aven에 없는 서비스를 가리키는 경로 규칙을 뺀 정의다.
// 서버는 경로 규칙의 대상 서비스가 이미 있어야 받아 주므로, 처음에는 이것을 적용하고 전체 정의를 한 번 더 적용한다.
// 바뀐 것이 없으면 nil이다.
func withoutPendingPaths(d *iac.Definition, exists func(string) bool) *iac.Definition {
	out := *d
	out.Services = nil
	changed := false
	for _, s := range d.Services {
		raw, ok := s.Settings["domains"]
		if !ok {
			out.Services = append(out.Services, s)
			continue
		}
		var doms []map[string]any
		if json.Unmarshal(raw, &doms) != nil {
			out.Services = append(out.Services, s)
			continue
		}
		for _, dm := range doms {
			rules, _ := dm["paths"].([]any)
			var keep []any
			for _, r := range rules {
				m, _ := r.(map[string]any)
				if svc, _ := m["service"].(string); svc != "" && !exists(svc) {
					changed = true
					continue
				}
				keep = append(keep, r)
			}
			if len(keep) > 0 {
				dm["paths"] = keep
			} else {
				delete(dm, "paths")
			}
		}
		b, _ := json.Marshal(doms)
		settings := map[string]json.RawMessage{}
		for k, v := range s.Settings {
			settings[k] = v
		}
		settings["domains"] = b
		s.Settings = settings
		out.Services = append(out.Services, s)
	}
	if !changed {
		return nil
	}
	return &out
}

// applyDefinition은 IaC plan → apply를 한 번 한다(파괴적인 계획은 거부한다).
func applyDefinition(c *config, base string, def *iac.Definition) error {
	var plan iacPlan
	if err := c.call("POST", base+"/config/plan", map[string]any{"definition": def}, &plan); err != nil {
		return err
	}
	if len(plan.Errors) > 0 {
		return fmt.Errorf("IaC plan has errors:\n  %s", strings.Join(plan.Errors, "\n  "))
	}
	if plan.Destructive {
		return errors.New("IaC plan is destructive (it would delete or overwrite something); the importer never confirms that. Inspect with aven config plan")
	}
	if len(plan.Changes) == 0 {
		return nil
	}
	var res struct {
		Services []struct {
			Service string `json:"service"`
			Action  string `json:"action"`
		} `json:"services"`
	}
	if err := c.call("POST", base+"/config/apply", map[string]any{"definition": def, "plan_hash": plan.PlanHash, "deploy": false}, &res); err != nil {
		return err
	}
	for _, s := range res.Services {
		fmt.Printf("  service %s %s\n", s.Service, s.Action)
	}
	return nil
}

func composeReq(st *dokploy.StackPlan, nodeID string, apply bool) map[string]any {
	req := map[string]any{"source": "yaml", "yaml": st.YAML, "name": st.Name, "env": st.Env}
	if st.AllowBindMounts {
		req["allow_bind_mounts"] = true
	}
	if nodeID != "" {
		req["node_id"] = nodeID
	}
	if apply {
		doms := map[string][]map[string]any{}
		for svc, ds := range st.Domains {
			for _, d := range ds {
				m := map[string]any{"host": d.Host}
				if d.TargetPort > 0 {
					m["target_port"] = d.TargetPort
				}
				doms[svc] = append(doms[svc], m)
			}
		}
		req["domains"] = doms
		req["deploy"] = false
	}
	return req
}

type composeIssue struct {
	Severity string `json:"severity"`
	Service  string `json:"service"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type composePreview struct {
	OK   bool `json:"ok"`
	Plan struct {
		Warnings []composeIssue `json:"warnings"`
		Errors   []composeIssue `json:"errors"`
		Missing  []string       `json:"missing_variables"`
	} `json:"plan"`
}

// ---- plan ----

func importPlan(c *config, o *importOpts) {
	p, err := o.translate()
	if err != nil {
		die("%v", err)
	}
	var nodes map[string]keelNode
	if !o.offline {
		if nodes, err = c.nodesByName(); err != nil {
			fmt.Fprintf(os.Stderr, "aven validation skipped: %v (use --offline to silence)\n", err)
			o.offline = true
		}
	}
	checks := map[string][]string{} // project → aven 검증 결과
	if !o.offline {
		for _, pp := range p.Projects {
			checks[pp.Name] = validateWithKeel(c, pp, nodes)
		}
	}
	if o.out != "" {
		b, _ := json.MarshalIndent(p, "", "  ")
		if err := os.WriteFile(o.out, append(b, '\n'), 0o600); err != nil {
			die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "plan written to %s (no secret values)\n", o.out)
	}
	if o.asJSON {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Print(formatImportPlan(p, checks))
}

// validateWithKeel은 노드 존재를 확인하고, 프로젝트가 이미 있으면 IaC plan과 compose preview를 돌린다.
// 상태(Ready)를 Blocked로 바꿀 수 있다.
func validateWithKeel(c *config, pp *dokploy.ProjectPlan, nodes map[string]keelNode) []string {
	var out []string
	for _, sp := range pp.Services {
		if sp.Status == dokploy.Ready && sp.Node != "" {
			if _, ok := nodes[sp.Node]; !ok {
				sp.Status = dokploy.Blocked
				sp.Reasons = append(sp.Reasons, fmt.Sprintf("aven node %q does not exist yet: join it (aven node join token) or map it with --node", sp.Node))
			}
		}
	}
	for _, st := range pp.Stacks {
		if st.Status == dokploy.Ready && st.Node != "" {
			if _, ok := nodes[st.Node]; !ok {
				st.Status = dokploy.Blocked
				st.Reasons = append(st.Reasons, fmt.Sprintf("aven node %q does not exist yet", st.Node))
			}
		}
	}
	dokploy.Propagate(pp)
	if needsGitHub(pp) {
		var g githubConn
		if err := c.call("GET", "/github/connection", nil, &g); err == nil && g.Source == "" {
			out = append(out, "GitHub is not connected: private github.com repositories will fail to clone. Connect it first (Settings › Integrations or aven github connect)")
		}
	}
	kp, err := c.keelProjectByName(pp.Name)
	if err != nil {
		return []string{"aven: " + err.Error()}
	}
	if kp == nil {
		return []string{"aven project does not exist yet: apply creates it (IaC/compose validation runs then)"}
	}
	eid := kp.env(pp.Environment)
	if eid == "" {
		return []string{"aven environment " + pp.Environment + " does not exist yet: apply creates it"}
	}
	def := definitionOf(pp)
	if len(def.Services) > 0 {
		var plan iacPlan
		if err := c.call("POST", "/projects/"+kp.ID+"/environments/"+eid+"/config/plan", map[string]any{"definition": def}, &plan); err != nil {
			out = append(out, "IaC plan: "+err.Error())
		} else {
			out = append(out, fmt.Sprintf("IaC plan: create %d, change %d, destroy %d", plan.Summary.Create, plan.Summary.Change, plan.Summary.Destroy))
			for _, e := range plan.Errors {
				out = append(out, "  error: "+e)
			}
			for _, w := range plan.Warnings {
				out = append(out, "  warning: "+w)
			}
			if plan.Destructive {
				out = append(out, "  destructive: apply will refuse (the importer never destroys)")
			}
		}
	}
	for _, st := range pp.Stacks {
		if st.Status != dokploy.Ready {
			continue
		}
		var pv composePreview
		nodeID := ""
		if st.Node != "" {
			nodeID = nodes[st.Node].ID
		}
		if err := c.call("POST", "/projects/"+kp.ID+"/environments/"+eid+"/compose/preview", composeReq(st, nodeID, false), &pv); err != nil {
			st.Status = dokploy.Blocked
			st.Reasons = append(st.Reasons, "aven compose preview: "+err.Error())
			continue
		}
		applyPreview(st, &pv)
	}
	return out
}

// needsGitHub는 프로젝트에 https github.com 저장소를 쓰는 ready 서비스가 있는지다.
func needsGitHub(pp *dokploy.ProjectPlan) bool {
	for _, sp := range pp.Services {
		if sp.Status != dokploy.Ready {
			continue
		}
		var u string
		if raw, ok := sp.Service.Settings["repo_url"]; ok && json.Unmarshal(raw, &u) == nil && strings.HasPrefix(u, "https://github.com/") {
			return true
		}
	}
	return false
}

func applyPreview(st *dokploy.StackPlan, pv *composePreview) {
	for _, e := range pv.Plan.Errors {
		st.Status = dokploy.Blocked
		st.Reasons = append(st.Reasons, "aven: "+issueText(e))
	}
	for _, w := range pv.Plan.Warnings {
		st.Warnings = append(st.Warnings, "aven: "+issueText(w))
	}
	if len(pv.Plan.Missing) > 0 {
		st.Warnings = append(st.Warnings, "aven: missing variables "+strings.Join(pv.Plan.Missing, ", "))
	}
}

func issueText(i composeIssue) string {
	if i.Service != "" {
		return i.Service + ": " + i.Message
	}
	return i.Message
}

func formatImportPlan(p *dokploy.Plan, checks map[string][]string) string {
	var b strings.Builder
	count := map[string]int{}
	var manual []string
	for _, pp := range p.Projects {
		if len(pp.Services) == 0 && len(pp.Stacks) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\nproject %s / %s  (Dokploy: %s)\n", pp.Name, pp.Environment, strings.Join(pp.Sources, ", "))
		if len(pp.SharedKeys) > 0 {
			fmt.Fprintf(&b, "  shared variables: %s\n", strings.Join(pp.SharedKeys, ", "))
		}
		for _, r := range pp.Registries {
			fmt.Fprintf(&b, "  registry: %s (user %s)\n", r.Host, r.Username)
		}
		for _, sp := range pp.Services {
			count[sp.Status]++
			node := sp.Node
			if node == "" {
				node = "local"
			}
			fmt.Fprintf(&b, "  %-15s %-22s %-8s %-6s %s\n", "["+sp.Status+"]", sp.Name, sp.Kind, node, sp.Source)
			for _, r := range sp.Reasons {
				fmt.Fprintf(&b, "      - %s\n", r)
			}
			if sp.Status == dokploy.Skip {
				continue
			}
			if len(sp.Settings) > 0 {
				fmt.Fprintf(&b, "      settings: %s\n", strings.Join(sp.Settings, ", "))
			}
			if len(sp.Variables) > 0 {
				var rw []string
				for _, v := range sp.Variables {
					if v.Kind == "rewritten" {
						s := v.Name
						if v.To != "" {
							s += " → " + v.To
						}
						rw = append(rw, s)
					}
				}
				fmt.Fprintf(&b, "      variables: %d", len(sp.Variables))
				if len(rw) > 0 {
					fmt.Fprintf(&b, " (rewritten: %s)", strings.Join(rw, "; "))
				}
				b.WriteString("\n")
			}
			if len(sp.Domains) > 0 {
				fmt.Fprintf(&b, "      domains: %s\n", strings.Join(sp.Domains, ", "))
			}
			for _, v := range sp.Volumes {
				fmt.Fprintf(&b, "      volume: %s ← %s\n", v.MountPath, v.Source)
			}
			for _, w := range sp.Warnings {
				fmt.Fprintf(&b, "      ! %s\n", w)
			}
			if sp.Status != dokploy.Ready {
				continue
			}
			if sp.Data != "" {
				manual = append(manual, fmt.Sprintf("data   %s/%s: %s", pp.Name, sp.Name, sp.Data))
			}
			if sp.DeployKey {
				manual = append(manual, fmt.Sprintf("key    %s/%s: add the aven deploy key (printed by apply) to the repository", pp.Name, sp.Name))
			}
			if sp.Upload {
				manual = append(manual, fmt.Sprintf("upload %s/%s: aven up from /etc/dokploy/applications/%s/code", pp.Name, sp.Name, sp.AppName))
			}
		}
		for _, st := range pp.Stacks {
			count[st.Status]++
			node := st.Node
			if node == "" {
				node = "local"
			}
			fmt.Fprintf(&b, "  %-15s %-22s %-8s %-6s %s\n", "["+st.Status+"]", st.Name, "compose", node, st.Source)
			for _, r := range st.Reasons {
				fmt.Fprintf(&b, "      - %s\n", r)
			}
			if st.Status == dokploy.Skip {
				continue
			}
			for svc, ds := range st.Domains {
				for _, d := range ds {
					fmt.Fprintf(&b, "      domain: %s → %s:%d\n", d.Host, svc, d.TargetPort)
				}
			}
			for _, w := range st.Warnings {
				fmt.Fprintf(&b, "      ! %s\n", w)
			}
			if st.Status == dokploy.Ready && len(st.Volumes) > 0 {
				manual = append(manual, fmt.Sprintf("data   %s/%s: copy named volumes (%s) from Dokploy project %s", pp.Name, st.Name, strings.Join(st.Volumes, ", "), st.AppName))
			}
		}
		for _, line := range checks[pp.Name] {
			fmt.Fprintf(&b, "  aven> %s\n", line)
		}
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\n! %s\n", w)
	}
	fmt.Fprintf(&b, "\nsummary: ready %d, needs-decision %d, blocked %d, skip %d\n", count[dokploy.Ready], count[dokploy.NeedsDecision], count[dokploy.Blocked], count[dokploy.Skip])
	if len(manual) > 0 {
		sort.Strings(manual)
		b.WriteString("\nmanual steps after apply (the importer does not move data):\n")
		for _, m := range manual {
			fmt.Fprintf(&b, "  %s\n", m)
		}
	}
	return b.String()
}

// ---- apply ----

func importApply(c *config, o *importOpts) {
	p, err := o.translate()
	if err != nil {
		die("%v", err)
	}
	nodes, err := c.nodesByName()
	if err != nil {
		die("%v", err)
	}
	if !o.yes {
		for _, pp := range p.Projects {
			validateWithKeel(c, pp, nodes)
		}
		fmt.Print(formatImportPlan(p, nil))
		fmt.Println("\nnothing applied: rerun with --yes. Only [ready] services and stacks are created, nothing is deployed, Dokploy is not changed.")
		return
	}
	failed := false
	for _, pp := range p.Projects {
		if err := applyProject(c, pp, nodes); err != nil {
			fmt.Fprintf(os.Stderr, "project %s: %v\n", pp.Name, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("\ndone. Nothing was deployed. Next: copy data, then deploy service by service (see `aven import dokploy plan` manual steps).")
}

func applyProject(c *config, pp *dokploy.ProjectPlan, nodes map[string]keelNode) error {
	ready := 0
	for _, sp := range pp.Services {
		if sp.Status == dokploy.Ready {
			ready++
		}
	}
	for _, st := range pp.Stacks {
		if st.Status == dokploy.Ready {
			ready++
		}
	}
	if ready == 0 {
		return nil
	}
	validateWithKeel(c, pp, nodes) // 노드 확인(Ready → Blocked)
	fmt.Printf("project %s / %s\n", pp.Name, pp.Environment)
	kp, err := c.keelProjectByName(pp.Name)
	if err != nil {
		return err
	}
	if kp == nil {
		var created struct {
			ID string `json:"id"`
		}
		if err := c.call("POST", "/projects", map[string]any{"name": pp.Name, "description": "imported from Dokploy (" + strings.Join(pp.Sources, ", ") + ")"}, &created); err != nil {
			return err
		}
		fmt.Printf("  created project %s\n", pp.Name)
		if kp, err = c.keelProjectByName(pp.Name); err != nil || kp == nil {
			return fmt.Errorf("project %s not found after create: %v", pp.Name, err)
		}
	}
	eid := kp.env(pp.Environment)
	if eid == "" {
		var e envRef
		if err := c.call("POST", "/projects/"+kp.ID+"/environments", map[string]any{"name": pp.Environment}, &e); err != nil {
			return err
		}
		eid = e.ID
		fmt.Printf("  created environment %s\n", pp.Environment)
	}
	base := "/projects/" + kp.ID + "/environments/" + eid

	// 레지스트리: 기존 항목은 비밀번호를 그대로 둔다(null).
	if len(pp.Registries) > 0 {
		var cur []struct {
			Host     string `json:"host"`
			Username string `json:"username"`
		}
		if err := c.call("GET", "/projects/"+kp.ID+"/registries", nil, &cur); err != nil {
			return err
		}
		next := []map[string]any{}
		have := map[string]bool{}
		for _, r := range cur {
			have[r.Host] = true
			next = append(next, map[string]any{"host": r.Host, "username": r.Username, "password": nil})
		}
		for _, r := range pp.Registries {
			if !have[r.Host] {
				next = append(next, map[string]any{"host": r.Host, "username": r.Username, "password": r.Password})
				fmt.Printf("  registry %s added\n", r.Host)
			}
		}
		if err := c.call("PUT", "/projects/"+kp.ID+"/registries", next, nil); err != nil {
			return err
		}
	}
	// 공유 변수: 없는 키만 더한다.
	if len(pp.Shared) > 0 {
		cur := map[string]string{}
		if err := c.call("GET", "/environments/"+eid+"/shared-variables", nil, &cur); err != nil {
			return err
		}
		for k, v := range pp.Shared {
			if _, ok := cur[k]; !ok {
				cur[k] = v
			}
		}
		if err := c.call("PUT", "/environments/"+eid+"/shared-variables", cur, nil); err != nil {
			return err
		}
	}

	// 서비스(IaC partial)
	def := definitionOf(pp)
	if len(def.Services) > 0 {
		if first := withoutPendingPaths(def, func(n string) bool { return kp.service(n) != "" }); first != nil {
			if err := applyDefinition(c, base, first); err != nil {
				return err
			}
		}
		if err := applyDefinition(c, base, def); err != nil {
			return err
		}
		if kp, err = c.keelProjectByName(pp.Name); err != nil {
			return err
		}
	}
	var canvas struct {
		Services []struct {
			ID       string `json:"id"`
			Instance struct {
				NodeID string `json:"node_id"`
			} `json:"instance"`
			Volumes []struct {
				MountPath string `json:"mount_path"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := c.call("GET", "/environments/"+eid+"/canvas", nil, &canvas); err != nil {
		return err
	}
	nodeOf, mounts := map[string]string{}, map[string]map[string]bool{}
	for _, s := range canvas.Services {
		nodeOf[s.ID] = s.Instance.NodeID
		mounts[s.ID] = map[string]bool{}
		for _, v := range s.Volumes {
			mounts[s.ID][v.MountPath] = true
		}
	}
	for _, sp := range pp.Services {
		if sp.Status != dokploy.Ready {
			continue
		}
		sid := kp.service(sp.Name)
		if sid == "" {
			return fmt.Errorf("service %s missing after apply", sp.Name)
		}
		inst := "/services/" + sid + "/instances/" + eid
		if sp.Node != "" {
			n := nodes[sp.Node]
			if nodeOf[sid] != n.ID {
				// 새로 만든 서비스라 볼륨이 비어 있다. move_volumes=1이 빈 볼륨을 대상 노드에 다시 만든다.
				if err := c.call("PATCH", inst+"?move_volumes=1", map[string]any{"node_id": n.ID}, nil); err != nil {
					return fmt.Errorf("%s: move to node %s: %w", sp.Name, sp.Node, err)
				}
				fmt.Printf("  %s placed on node %s\n", sp.Name, sp.Node)
			}
		}
		for _, v := range sp.Volumes {
			if mounts[sid][v.MountPath] {
				continue
			}
			if err := c.call("POST", "/environments/"+eid+"/volumes", map[string]any{"service_id": sid, "mount_path": v.MountPath, "name": v.Name}, nil); err != nil {
				return fmt.Errorf("%s: volume %s: %w", sp.Name, v.MountPath, err)
			}
			fmt.Printf("  %s volume %s (copy data from Dokploy volume %s)\n", sp.Name, v.MountPath, v.Source)
		}
		if sp.DeployKey {
			var key struct {
				PublicKey   string `json:"public_key"`
				Fingerprint string `json:"fingerprint"`
			}
			if err := c.call("GET", "/services/"+sid+"/deploy-key", nil, &key); err != nil {
				if err := c.call("POST", "/services/"+sid+"/deploy-key", map[string]any{}, &key); err != nil {
					return fmt.Errorf("%s: deploy key: %w", sp.Name, err)
				}
			}
			fmt.Printf("  %s deploy key (add it read-only to the repository):\n    %s\n", sp.Name, key.PublicKey)
		}
	}
	// compose 스택
	for _, st := range pp.Stacks {
		if st.Status != dokploy.Ready {
			continue
		}
		nodeID := ""
		if st.Node != "" {
			nodeID = nodes[st.Node].ID
		}
		var pv composePreview
		if err := c.call("POST", base+"/compose/preview", composeReq(st, nodeID, false), &pv); err != nil {
			return fmt.Errorf("stack %s: %w", st.Name, err)
		}
		if len(pv.Plan.Errors) > 0 {
			applyPreview(st, &pv)
			fmt.Printf("  stack %s skipped: %s\n", st.Name, strings.Join(st.Reasons, "; "))
			continue
		}
		var res struct {
			Stack struct {
				ID string `json:"id"`
			} `json:"stack"`
		}
		if err := c.call("POST", base+"/compose/apply", composeReq(st, nodeID, true), &res); err != nil {
			return fmt.Errorf("stack %s: %w", st.Name, err)
		}
		fmt.Printf("  stack %s imported (not deployed)\n", st.Name)
		if err := stackTLSOff(c, pp, st, eid); err != nil {
			return fmt.Errorf("stack %s: %w", st.Name, err)
		}
	}
	return nil
}

// stackTLSOff는 Dokploy에서 https=false였던 compose 도메인을 tls off로 바꾼다.
func stackTLSOff(c *config, pp *dokploy.ProjectPlan, st *dokploy.StackPlan, eid string) error {
	kp, err := c.keelProjectByName(pp.Name)
	if err != nil {
		return err
	}
	for svc, ds := range st.Domains {
		for _, d := range ds {
			if !d.TLSOff {
				continue
			}
			for _, s := range kp.Services {
				if s.ComposeName != svc || s.StackID == "" {
					continue
				}
				path := "/services/" + s.ID + "/instances/" + eid + "/domains/" + url.PathEscape(d.Host)
				if err := c.call("PATCH", path, map[string]any{"tls": "off", "trust_forwarded_proto": true}, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ---- verify ----

func importVerify(c *config, o *importOpts) {
	p, err := o.translate()
	if err != nil {
		die("%v", err)
	}
	nodes, err := c.nodesByName()
	if err != nil {
		die("%v", err)
	}
	bad := 0
	for _, pp := range p.Projects {
		validateWithKeel(c, pp, nodes)
		kp, err := c.keelProjectByName(pp.Name)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("project %s / %s\n", pp.Name, pp.Environment)
		if kp == nil {
			fmt.Println("  not imported")
			continue
		}
		eid := kp.env(pp.Environment)
		for _, sp := range pp.Services {
			if sp.Status != dokploy.Ready {
				continue
			}
			sid := kp.service(sp.Name)
			if sid == "" || eid == "" {
				fmt.Printf("  MISSING %s\n", sp.Name)
				bad++
				continue
			}
			var in struct {
				Variables map[string]string `json:"variables"`
				Domains   []struct {
					Host string `json:"host"`
					Kind string `json:"kind"`
				} `json:"domains"`
			}
			if err := c.call("GET", "/services/"+sid+"/instances/"+eid, nil, &in); err != nil {
				die("%v", err)
			}
			var diffs []string
			if sp.Service.Variables != nil {
				for k, want := range *sp.Service.Variables {
					got, ok := in.Variables[k]
					switch {
					case !ok:
						diffs = append(diffs, "variable "+k+" missing")
					case got != want.Value:
						diffs = append(diffs, "variable "+k+" differs")
					}
				}
			}
			have := map[string]bool{}
			for _, d := range in.Domains {
				have[d.Host] = true
			}
			for _, h := range sp.Domains {
				if strings.Contains(h, " ") || strings.Contains(h, "/") {
					continue // 다른 서비스의 경로 규칙
				}
				if !have[h] {
					diffs = append(diffs, "domain "+h+" missing")
				}
			}
			sort.Strings(diffs)
			if len(diffs) == 0 {
				fmt.Printf("  OK      %s\n", sp.Name)
				continue
			}
			bad++
			fmt.Printf("  DIFF    %s: %s\n", sp.Name, strings.Join(diffs, "; "))
		}
		for _, st := range pp.Stacks {
			if st.Status != dokploy.Ready {
				continue
			}
			found := false
			for _, s := range kp.Services {
				if s.StackID != "" && strings.HasPrefix(s.Name, st.Name) {
					found = true
				}
			}
			if !found {
				var stacks []struct {
					Name string `json:"name"`
				}
				c.call("GET", "/projects/"+kp.ID+"/environments/"+eid+"/stacks", nil, &stacks)
				for _, s := range stacks {
					found = found || s.Name == st.Name
				}
			}
			if found {
				fmt.Printf("  OK      stack %s\n", st.Name)
			} else {
				fmt.Printf("  MISSING stack %s\n", st.Name)
				bad++
			}
		}
	}
	if bad > 0 {
		fmt.Printf("\n%d difference(s). Values are compared exactly (Dokploy values after reference rewriting); they are never printed.\n", bad)
		os.Exit(1)
	}
	fmt.Println("\nall imported services match Dokploy (variables, domains, stacks)")
}

// ---- export-compose ----

// importExportCompose는 compose 스택을 Dokploy와 같은 배치(<dir>/<appName>/code/docker-compose.yml, .env, ../files/...)로 쓴다.
// 같은 compose 프로젝트 이름(-p <appName>)으로 올리면 Dokploy가 만든 컨테이너와 볼륨을 그대로 이어받는다.
func importExportCompose(o *importOpts, names []string) {
	if o.dir == "" {
		die("usage: aven import dokploy export-compose --dir DIR [project/name ...] (default: every stack that cannot move to aven)")
	}
	p, err := o.translate()
	if err != nil {
		die("%v", err)
	}
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	n := 0
	for _, pp := range p.Projects {
		for _, st := range pp.Stacks {
			pick := want[strings.ToLower(st.Source)]
			if len(want) == 0 {
				pick = st.Status == dokploy.Blocked || st.Status == dokploy.NeedsDecision
			}
			if !pick {
				continue
			}
			if err := writeStack(o.dir, st); err != nil {
				die("%s: %v", st.Source, err)
			}
			n++
			fmt.Printf("%s → %s\n", st.Source, filepath.Join(o.dir, st.AppName))
			fmt.Printf("    run: cd %s && docker compose -p %s up -d\n", filepath.Join(o.dir, st.AppName, "code"), st.AppName)
			fmt.Println("    ! this is the file as stored in Dokploy: Dokploy adds Traefik labels and dokploy-network at deploy time, so the containers are recreated once")
			if hosts := stackHosts(st); hosts != "" {
				fmt.Printf("    ! its domains (%s) were routed by Dokploy's Traefik; aven cannot route to containers outside aven, plan another route\n", hosts)
			}
			if strings.Contains(st.YAML, "dokploy-network") {
				fmt.Println("    ! uses the external network dokploy-network: create it or edit the file before Dokploy is removed")
			}
			if st.Node != "" {
				fmt.Printf("    ! runs on %s: copy the directory there\n", st.Node)
			}
		}
	}
	fmt.Printf("%d stack(s) exported. .env files are 0600 and contain secrets.\n", n)
}

func stackHosts(st *dokploy.StackPlan) string {
	var hs []string
	for _, ds := range st.Domains {
		for _, d := range ds {
			hs = append(hs, d.Host)
		}
	}
	sort.Strings(hs)
	return strings.Join(hs, ", ")
}

func writeStack(dir string, st *dokploy.StackPlan) error {
	root := filepath.Join(dir, st.AppName)
	code := filepath.Join(root, "code")
	if err := os.MkdirAll(code, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(code, "docker-compose.yml"), []byte(st.YAML), 0o600); err != nil {
		return err
	}
	if strings.TrimSpace(st.Env) != "" {
		if err := os.WriteFile(filepath.Join(code, ".env"), []byte(st.Env+"\n"), 0o600); err != nil {
			return err
		}
	}
	for _, f := range st.Files {
		rel := filepath.Clean("/" + f.FilePath)
		dst := filepath.Join(root, "files", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(f.Content), 0o600); err != nil {
			return err
		}
	}
	return nil
}
