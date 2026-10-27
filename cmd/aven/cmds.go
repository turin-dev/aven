package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// ---- target (-s/--service, -e/--environment) ----

type targetFlags struct {
	svc, env *string
}

func addTargetFlags(fl *flag.FlagSet) *targetFlags {
	t := &targetFlags{svc: new(string), env: new(string)}
	fl.StringVar(t.svc, "s", "", "service name or id (default: linked)")
	fl.StringVar(t.svc, "service", "", "service name or id (default: linked)")
	fl.StringVar(t.env, "e", "", "environment name or id (default: linked)")
	fl.StringVar(t.env, "environment", "", "environment name or id (default: linked)")
	return t
}

func (c *config) project(pid string) *project {
	var p project
	if err := c.call("GET", "/projects/"+url.PathEscape(pid), nil, &p); err != nil {
		die("%v", err)
	}
	return &p
}

func findEnv(p *project, s string) *envRef {
	for i := range p.Environments {
		if p.Environments[i].ID == s || p.Environments[i].Name == s {
			return &p.Environments[i]
		}
	}
	return nil
}

func findSvc(p *project, s string) *svcRef {
	for i := range p.Services {
		if p.Services[i].ID == s || p.Services[i].Name == s {
			return &p.Services[i]
		}
	}
	return nil
}

func names[T any](xs []T, f func(T) string) string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return strings.Join(out, ", ")
}

// resolve는 연결 정보에 -s/-e를 덮은 사본이다(설정 파일은 바꾸지 않는다).
func (t *targetFlags) resolve(c *config) *link {
	l := *c.linked()
	if (t.svc == nil || *t.svc == "") && (t.env == nil || *t.env == "") {
		return &l
	}
	p := c.project(l.ProjectID)
	if t.env != nil && *t.env != "" {
		e := findEnv(p, *t.env)
		if e == nil {
			die("environment %q not found (%s)", *t.env, names(p.Environments, func(e envRef) string { return e.Name }))
		}
		l.EnvironmentID, l.Environment = e.ID, e.Name
	}
	if t.svc != nil && *t.svc != "" {
		s := findSvc(p, *t.svc)
		if s == nil {
			die("service %q not found (%s)", *t.svc, names(p.Services, func(s svcRef) string { return s.Name }))
		}
		l.ServiceID, l.Service = s.ID, s.Name
	}
	return &l
}

// service는 resolve와 같지만 서비스가 정해져 있어야 한다.
func (t *targetFlags) service(c *config) *link {
	l := t.resolve(c)
	if l.ServiceID == "" {
		die("no service linked (run: aven service use <name>, or pass -s <service>)")
	}
	return l
}

func (l *link) instPath() string {
	return "/services/" + url.PathEscape(l.ServiceID) + "/instances/" + url.PathEscape(l.EnvironmentID)
}

func confirm(yes bool, prompt string) {
	if yes {
		return
	}
	if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		die("%s: pass -y to confirm in non-interactive use", prompt)
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(s)); a != "y" && a != "yes" {
		die("aborted")
	}
}

// ---- status / init / environment / service / add ----

type instanceView struct {
	Status              string  `json:"status"`
	CurrentDeploymentID *string `json:"current_deployment_id"`
	PrivateDomain       string  `json:"private_domain"`
	Domains             []struct {
		Host   string `json:"host"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	} `json:"domains"`
	TCPProxy *struct {
		ContainerPort int `json:"container_port"`
		PublicPort    int `json:"public_port"`
	} `json:"tcp_proxy"`
	PendingChanges []string `json:"pending_changes"`
}

type deploymentView struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	Trigger       string    `json:"trigger"`
	CommitSHA     *string   `json:"commit_sha"`
	CommitMessage *string   `json:"commit_message"`
	Error         *string   `json:"error"`
	CreatedAt     time.Time `json:"created_at"`
	Replicas      []struct {
		Name     string `json:"name"`
		Status   string `json:"status"`
		Restarts int    `json:"restarts"`
	} `json:"replicas"`
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func cmdStatus(c *config, args []string) {
	fl := flag.NewFlagSet("status", flag.ExitOnError)
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.resolve(c)
	pname := l.Project
	if pname == "" {
		pname = c.project(l.ProjectID).Name
	}
	fmt.Printf("Project:      %s\nEnvironment:  %s\n", pname, l.Environment)
	if l.ServiceID == "" {
		fmt.Println("Service:      (none; run: aven service use <name>)")
		return
	}
	fmt.Printf("Service:      %s\n", l.Service)
	var in instanceView
	if err := c.call("GET", l.instPath(), nil, &in); err != nil {
		die("%v", err)
	}
	fmt.Printf("Status:       %s\n", in.Status)
	var deps []deploymentView
	c.call("GET", l.instPath()+"/deployments?limit=1", nil, &deps)
	if len(deps) > 0 {
		d := deps[0]
		line := fmt.Sprintf("%s (%s, %s, %s)", d.ID, d.Status, d.Trigger, ago(d.CreatedAt))
		if d.CommitSHA != nil && len(*d.CommitSHA) >= 7 {
			line += " " + (*d.CommitSHA)[:7]
			if d.CommitMessage != nil {
				line += " " + firstLine(*d.CommitMessage)
			}
		}
		fmt.Printf("Deployment:   %s\n", line)
		if d.Error != nil {
			fmt.Printf("Error:        %s\n", firstLine(*d.Error))
		}
		for _, r := range d.Replicas {
			fmt.Printf("Replica:      %s %s (restarts %d)\n", r.Name, r.Status, r.Restarts)
		}
	} else {
		fmt.Println("Deployment:   (none)")
	}
	for _, d := range in.Domains {
		fmt.Printf("Domain:       https://%s (%s, %s)\n", d.Host, d.Kind, d.Status)
	}
	if in.PrivateDomain != "" {
		fmt.Printf("Private:      %s\n", in.PrivateDomain)
	}
	if in.TCPProxy != nil {
		fmt.Printf("TCP proxy:    :%d → %d\n", in.TCPProxy.PublicPort, in.TCPProxy.ContainerPort)
	}
	if len(in.PendingChanges) > 0 {
		fmt.Printf("Pending:      %s (apply with: aven redeploy or aven variables --deploy)\n", strings.Join(in.PendingChanges, ", "))
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func (c *config) saveLink(l *link) {
	dir, _ := os.Getwd()
	c.Links[dir] = l
	if err := c.save(); err != nil {
		die("%v", err)
	}
}

func cmdInit(c *config, args []string) {
	fl := flag.NewFlagSet("init", flag.ExitOnError)
	desc := fl.String("description", "", "project description")
	workspace := fl.String("workspace", "", "workspace ID")
	fl.Parse(args)
	if fl.NArg() != 1 {
		die("usage: aven init <name> [--description text]")
	}
	var p project
	if err := c.call("POST", "/projects", map[string]string{"name": fl.Arg(0), "description": *desc, "workspace_id": *workspace}, &p); err != nil {
		die("%v", err)
	}
	full := c.project(p.ID)
	l := &link{ProjectID: full.ID, Project: full.Name}
	for _, e := range full.Environments {
		if e.ID == full.DefaultEnvironmentID {
			l.EnvironmentID, l.Environment = e.ID, e.Name
		}
	}
	if l.EnvironmentID == "" && len(full.Environments) > 0 {
		l.EnvironmentID, l.Environment = full.Environments[0].ID, full.Environments[0].Name
	}
	c.saveLink(l)
	fmt.Printf("created project %s (%s) and linked this directory to %s\nnext: aven add --repo <url> | --image <img> | --database postgres\n", full.Name, full.ID, l.Environment)
}

func cmdEnvironment(c *config, args []string) {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	l := c.linked()
	switch sub {
	case "list", "ls":
		p := c.project(l.ProjectID)
		for _, e := range p.Environments {
			mark := " "
			if e.ID == l.EnvironmentID {
				mark = "*"
			}
			def := ""
			if e.ID == p.DefaultEnvironmentID {
				def = " (default)"
			}
			if e.Restricted {
				def += " (restricted)"
			}
			fmt.Printf("%s %s%s\n", mark, e.Name, def)
		}
	case "new", "create":
		fl := flag.NewFlagSet("environment new", flag.ExitOnError)
		from := fl.String("copy-from", "", "environment to copy settings and variables from")
		fl.Parse(reorderFlags(args))
		if fl.NArg() != 1 {
			die("usage: aven environment new <name> [--copy-from env]")
		}
		body := map[string]any{"name": fl.Arg(0)}
		if *from != "" {
			e := findEnv(c.project(l.ProjectID), *from)
			if e == nil {
				die("environment %q not found", *from)
			}
			body["copy_from"] = e.ID
		}
		var e envRef
		if err := c.call("POST", "/projects/"+url.PathEscape(l.ProjectID)+"/environments", body, &e); err != nil {
			die("%v", err)
		}
		l.EnvironmentID, l.Environment = e.ID, e.Name
		c.saveLink(l)
		fmt.Printf("created environment %s and switched to it\n", e.Name)
	case "use", "switch":
		if len(args) != 1 {
			die("usage: aven environment use <name>")
		}
		e := findEnv(c.project(l.ProjectID), args[0])
		if e == nil {
			die("environment %q not found", args[0])
		}
		l.EnvironmentID, l.Environment = e.ID, e.Name
		c.saveLink(l)
		fmt.Printf("using environment %s\n", e.Name)
	case "delete", "rm":
		fl := flag.NewFlagSet("environment delete", flag.ExitOnError)
		yes := fl.Bool("y", false, "don't ask for confirmation")
		fl.Parse(reorderFlags(args))
		if fl.NArg() != 1 {
			die("usage: aven environment delete <name> [-y]")
		}
		e := findEnv(c.project(l.ProjectID), fl.Arg(0))
		if e == nil {
			die("environment %q not found", fl.Arg(0))
		}
		if e.ID == l.EnvironmentID {
			die("environment %s is linked to this directory; switch first (aven environment use <other>)", e.Name)
		}
		confirm(*yes, fmt.Sprintf("delete environment %s with all its containers, volumes and deployments?", e.Name))
		if err := c.call("DELETE", "/environments/"+url.PathEscape(e.ID), nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Printf("deleted environment %s\n", e.Name)
	case "sync":
		cmdEnvironmentSync(c, l, args)
	case "restrict", "unrestrict":
		cmdEnvironmentRestrict(c, l, sub == "restrict", args) // access.go
	default:
		die("usage: aven environment [list | new <name> [--copy-from env] | use <name> | delete <name> | sync [target] --from <env> [--deploy] [-y] | restrict|unrestrict [name]]")
	}
}

// syncPlan은 GET /environments/{eid}/sync-preview 응답이다.
type syncPlan struct {
	From     envRef `json:"from"`
	To       envRef `json:"to"`
	Services []struct {
		ServiceID string `json:"service_id"`
		Name      string `json:"service_name"`
		Action    string `json:"action"`
		Settings  []struct {
			Field string          `json:"field"`
			From  json.RawMessage `json:"from"`
			To    json.RawMessage `json:"to"`
		} `json:"settings"`
		Variables []syncVar `json:"variables"`
		Volumes   []struct {
			MountPath string `json:"mount_path"`
		} `json:"volumes"`
		Protected []string `json:"protected_variables"`
	} `json:"services"`
	Shared []syncVar `json:"shared_variables"`
}

type syncVar struct {
	Key    string  `json:"key"`
	Action string  `json:"action"`
	From   *string `json:"from"`
	To     *string `json:"to"`
}

func (v syncVar) line() string {
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch v.Action {
	case "added":
		return "+ " + v.Key + " = " + s(v.To)
	case "removed":
		return "- " + v.Key
	}
	return "~ " + v.Key + ": " + s(v.From) + " → " + s(v.To)
}

// formatSyncPlan은 동기화 미리보기를 사람이 읽는 diff로 만든다. 변경이 없으면 빈 문자열.
func formatSyncPlan(p *syncPlan) string {
	var b strings.Builder
	for _, s := range p.Services {
		tag := ""
		if s.Action == "create" {
			tag = " (new)"
		}
		fmt.Fprintf(&b, "%s%s\n", s.Name, tag)
		for _, f := range s.Settings {
			fmt.Fprintf(&b, "  ~ %s: %s → %s\n", f.Field, string(f.From), string(f.To))
		}
		for _, v := range s.Variables {
			fmt.Fprintf(&b, "  %s\n", v.line())
		}
		for _, v := range s.Volumes {
			fmt.Fprintf(&b, "  + volume %s (empty)\n", v.MountPath)
		}
		if len(s.Protected) > 0 {
			fmt.Fprintf(&b, "  (not synced: database credentials %s)\n", strings.Join(s.Protected, ", "))
		}
	}
	if len(p.Shared) > 0 {
		b.WriteString("shared variables\n")
		for _, v := range p.Shared {
			fmt.Fprintf(&b, "  %s\n", v.line())
		}
	}
	return b.String()
}

func cmdEnvironmentSync(c *config, l *link, args []string) {
	fl := flag.NewFlagSet("environment sync", flag.ExitOnError)
	from := fl.String("from", "", "environment to copy changes from")
	deploy := fl.Bool("deploy", false, "deploy the changed services after applying")
	yes := fl.Bool("y", false, "don't ask for confirmation")
	fl.Parse(reorderFlags(args))
	if *from == "" || fl.NArg() > 1 {
		die("usage: aven environment sync [target] --from <env> [--deploy] [-y]")
	}
	p := c.project(l.ProjectID)
	target := findEnv(p, l.EnvironmentID)
	if fl.NArg() == 1 {
		target = findEnv(p, fl.Arg(0))
	}
	if target == nil {
		die("target environment not found")
	}
	src := findEnv(p, *from)
	if src == nil {
		die("environment %q not found", *from)
	}
	var plan syncPlan
	if err := c.call("GET", "/environments/"+url.PathEscape(target.ID)+"/sync-preview?from="+url.QueryEscape(src.ID), nil, &plan); err != nil {
		die("%v", err)
	}
	diff := formatSyncPlan(&plan)
	if diff == "" {
		fmt.Printf("%s already matches %s\n", target.Name, src.Name)
		return
	}
	fmt.Printf("changes to %s (from %s):\n%s", target.Name, src.Name, diff)
	q := fmt.Sprintf("apply these changes to %s?", target.Name)
	if *deploy {
		q = fmt.Sprintf("apply these changes to %s and deploy the changed services?", target.Name)
	}
	confirm(*yes, q)
	var res struct {
		Services []struct {
			Name          string  `json:"service_name"`
			DeploymentID  *string `json:"deployment_id"`
			DeploySkipped *string `json:"deploy_skipped"`
		} `json:"services"`
		Shared int `json:"shared_variables"`
	}
	if err := c.call("POST", "/environments/"+url.PathEscape(target.ID)+"/sync", map[string]any{"from": src.ID, "deploy": *deploy}, &res); err != nil {
		die("%v", err)
	}
	fmt.Printf("synced %d service(s) and %d shared variable(s) into %s\n", len(res.Services), res.Shared, target.Name)
	for _, s := range res.Services {
		switch {
		case s.DeploymentID != nil:
			fmt.Printf("  %s: deploying (%s)\n", s.Name, *s.DeploymentID)
		case s.DeploySkipped != nil:
			fmt.Printf("  %s: not deployed: %s\n", s.Name, *s.DeploySkipped)
		}
	}
	if !*deploy {
		fmt.Println("changes are staged; deploy with `aven redeploy` or the dashboard")
	}
}

// reorderFlags는 위치 인자 뒤에 온 플래그도 읽히게 플래그를 앞으로 옮긴다(`new staging --copy-from production`).
// 값을 받는 플래그는 다음 인자를 함께 옮긴다.
func reorderFlags(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			return append(flags, pos...)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			flags = append(flags, a)
			if !strings.Contains(a, "=") && valueFlag[strings.TrimLeft(a, "-")] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// valueFlag는 reorderFlags가 값을 함께 옮기는 플래그다.
var valueFlag = map[string]bool{"copy-from": true, "from": true, "name": true, "branch": true, "port": true, "p": true, "replica": true,
	"s": true, "service": true, "e": true, "environment": true, "database": true, "image": true, "repo": true,
	"idle-timeout-minutes": true, "memory-mb": true, "cpu": true, "node": true, "checkpoint": true, "id": true, "timeout": true,
	"cwd": true, "variable": true, "env-file": true, "mode": true}

func cmdService(c *config, args []string) {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	l := c.linked()
	switch sub {
	case "list", "ls":
		p := c.project(l.ProjectID)
		for _, s := range p.Services {
			mark := " "
			if s.ID == l.ServiceID {
				mark = "*"
			}
			kind := s.SourceType
			if s.DatabaseKind != nil {
				kind += ":" + *s.DatabaseKind
			}
			fmt.Printf("%s %-24s %s\n", mark, s.Name, kind)
		}
	case "use", "link":
		if len(args) != 1 {
			die("usage: aven service use <name>")
		}
		p := c.project(l.ProjectID)
		s := findSvc(p, args[0])
		if s == nil {
			die("service %q not found (%s)", args[0], names(p.Services, func(s svcRef) string { return s.Name }))
		}
		l.ServiceID, l.Service = s.ID, s.Name
		c.saveLink(l)
		fmt.Printf("using service %s\n", s.Name)
	default:
		// `aven service <name>`은 use와 같다
		cmdService(c, append([]string{"use", sub}, args...))
	}
}

// parseAddArgs는 `aven add` 인자를 서비스 생성 요청 본문으로 바꾼다. --database/--image/--repo 중 하나만.
func parseAddArgs(args []string) (map[string]any, error) {
	fl := flag.NewFlagSet("add", flag.ContinueOnError)
	fl.SetOutput(new(strings.Builder))
	db := fl.String("database", "", "postgres | mysql | redis | mongo | bucket")
	fl.StringVar(db, "d", "", "postgres | mysql | redis | mongo | bucket")
	image := fl.String("image", "", "docker image")
	fl.StringVar(image, "i", "", "docker image")
	repo := fl.String("repo", "", "git repository URL")
	fl.StringVar(repo, "r", "", "git repository URL")
	branch := fl.String("branch", "", "git branch (default: main)")
	name := fl.String("name", "", "service name")
	fl.StringVar(name, "n", "", "service name")
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if fl.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fl.Arg(0))
	}
	set := 0
	for _, v := range []string{*db, *image, *repo} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return nil, errors.New("give exactly one of --database, --image or --repo")
	}
	body := map[string]any{}
	switch {
	case *db != "":
		switch *db {
		case "postgres", "postgresql", "pg":
			*db = "postgres"
		case "mysql", "redis", "mongo", "bucket":
		case "mongodb":
			*db = "mongo"
		case "s3":
			*db = "bucket"
		default:
			return nil, fmt.Errorf("unknown database %q (postgres, mysql, redis, mongo, bucket)", *db)
		}
		body["source_type"], body["database_kind"] = "database", *db
	case *image != "":
		body["source_type"], body["image"] = "image", *image
	default:
		body["source_type"], body["repo_url"] = "repo", *repo
		if *branch != "" {
			body["branch"] = *branch
		}
	}
	if *branch != "" && *repo == "" {
		return nil, errors.New("--branch only applies to --repo")
	}
	if *name != "" {
		body["name"] = *name
	}
	return body, nil
}

func cmdAdd(c *config, args []string) {
	body, err := parseAddArgs(args)
	if err != nil {
		die("%v\nusage: aven add --database postgres|mysql|redis|mongo | --image <img> | --repo <url> [--branch b] [--name n]", err)
	}
	l := c.linked()
	body["environment_id"] = l.EnvironmentID // 연결된 환경에서 바로 첫 배포를 시작한다
	var s svcRef
	if err := c.call("POST", "/projects/"+url.PathEscape(l.ProjectID)+"/services", body, &s); err != nil {
		die("%v", err)
	}
	l.ServiceID, l.Service = s.ID, s.Name
	c.saveLink(l)
	fmt.Printf("created service %s in %s (first deploy started) and linked it\n", s.Name, l.Environment)
}

// ---- run / shell (로컬 명령에 변수 주입) ----

func (c *config) resolvedVars(l *link) map[string]string {
	var raw map[string]resolvedValue
	if err := c.call("GET", l.instPath()+"/variables/resolved", nil, &raw); err != nil {
		die("%v", err)
	}
	out, sealed := visibleResolved(raw)
	if len(sealed) > 0 {
		sort.Strings(sealed)
		fmt.Fprintf(os.Stderr, "aven: sealed variables are not available locally: %s\n", strings.Join(sealed, ", "))
	}
	return out
}

// runEnv는 base 환경에 서비스 변수와 AVEN_* 연결 정보를 덮는다. KEEL_*도 같이 넣는다. 서버가 준 값이 있으면 그것이 우선이다.
// fold면 Windows처럼 이름의 대소문자를 구분하지 않는다.
func runEnv(base []string, vars map[string]string, l *link, fold bool) []string {
	all := map[string]string{}
	for _, prefix := range []string{"AVEN_", "KEEL_"} {
		for k, v := range map[string]string{
			prefix + "PROJECT_ID": l.ProjectID, prefix + "ENVIRONMENT_ID": l.EnvironmentID, prefix + "SERVICE_ID": l.ServiceID,
			prefix + "PROJECT_NAME": l.Project, prefix + "ENVIRONMENT_NAME": l.Environment, prefix + "SERVICE_NAME": l.Service,
		} {
			if v != "" {
				all[k] = v
			}
		}
	}
	for k, v := range vars {
		all[k] = v
	}
	key := func(s string) string {
		if fold {
			return strings.ToUpper(s)
		}
		return s
	}
	override := map[string]bool{}
	for k := range all {
		override[key(k)] = true
	}
	out := make([]string, 0, len(base)+len(all))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !override[key(k)] {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+all[k])
	}
	return out
}

// runLocal은 명령을 변수와 함께 실행하고 종료 코드를 그대로 돌려준다(값은 출력하지 않는다).
func runLocal(env []string, name string, args ...string) int {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl-C는 터미널이 자식에게도 보낸다. 부모는 자식이 끝날 때까지 기다린다.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		return 1
	default:
		fmt.Fprintf(os.Stderr, "aven: %v\n", err)
		return 127
	}
}

func cmdRun(c *config, args []string) {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	tf := addTargetFlags(fl)
	fl.Parse(args)
	rest := fl.Args()
	if len(rest) == 0 {
		die("usage: aven run [-s service] [-e env] [--] <command> [args...]")
	}
	l := tf.service(c)
	env := runEnv(os.Environ(), c.resolvedVars(l), l, runtime.GOOS == "windows")
	os.Exit(runLocal(env, rest[0], rest[1:]...))
}

func cmdShell(c *config, args []string) {
	fl := flag.NewFlagSet("shell", flag.ExitOnError)
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	vars := c.resolvedVars(l)
	sh := os.Getenv("SHELL")
	if runtime.GOOS == "windows" {
		if sh = os.Getenv("COMSPEC"); sh == "" {
			sh = "cmd.exe"
		}
	} else if sh == "" {
		sh = "/bin/sh"
	}
	env := append(runEnv(os.Environ(), vars, l, runtime.GOOS == "windows"), "AVEN_SHELL=1", "KEEL_SHELL=1")
	fmt.Fprintf(os.Stderr, "aven: subshell with %d variables of %s (%s); type exit to leave\n", len(vars), l.Service, l.Environment)
	os.Exit(runLocal(env, sh))
}

// ---- connect (DB 클라이언트) ----

var dbDefaults = map[string]struct {
	port   int
	urlKey string
	bin    string
}{
	"postgres": {5432, "DATABASE_PUBLIC_URL", "psql"},
	"mysql":    {3306, "MYSQL_PUBLIC_URL", "mysql"},
	"redis":    {6379, "REDIS_PUBLIC_URL", "redis-cli"},
	"mongo":    {27017, "MONGO_PUBLIC_URL", "mongosh"},
}

// dbClient는 DB 종류와 공개 URL로 실행할 클라이언트를 정한다. 비밀번호는 argv가 아니라 환경변수로만 넘긴다.
func dbClient(kind, raw string) (bin string, args, env []string, err error) {
	d, ok := dbDefaults[kind]
	if !ok {
		return "", nil, nil, fmt.Errorf("unsupported database %q", kind)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return "", nil, nil, errors.New("public URL is not available (is the TCP proxy enabled?)")
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	db := strings.TrimPrefix(u.Path, "/")
	switch kind {
	case "postgres":
		env = []string{"PGHOST=" + u.Hostname(), "PGPORT=" + u.Port(), "PGUSER=" + user, "PGPASSWORD=" + pass, "PGDATABASE=" + db}
	case "mysql":
		env = []string{"MYSQL_PWD=" + pass}
		args = []string{"--protocol=TCP", "-h", u.Hostname(), "-P", u.Port(), "-u", user}
		if db != "" {
			args = append(args, db)
		}
	case "redis":
		env = []string{"REDISCLI_AUTH=" + pass}
		args = []string{"-h", u.Hostname(), "-p", u.Port()}
		if user != "" && user != "default" {
			args = append(args, "--user", user)
		}
	case "mongo":
		env = []string{"AVEN_MONGO_URL=" + raw, "KEEL_MONGO_URL=" + raw}
		args = []string{"--nodb", "--shell", "--eval", "db = connect(process.env.AVEN_MONGO_URL || process.env.KEEL_MONGO_URL)"}
	}
	return d.bin, args, env, nil
}

func cmdConnect(c *config, args []string) {
	fl := flag.NewFlagSet("connect", flag.ExitOnError)
	enable := fl.Bool("enable-proxy", false, "enable the TCP proxy if it is off")
	envf := fl.String("e", "", "environment name or id (default: linked)")
	fl.StringVar(envf, "environment", "", "environment name or id (default: linked)")
	fl.Parse(reorderFlags(args))
	svc := ""
	if fl.NArg() > 0 {
		svc = fl.Arg(0)
	}
	l := (&targetFlags{svc: &svc, env: envf}).resolve(c)
	if l.ServiceID == "" {
		die("usage: aven connect [service] [--enable-proxy]")
	}
	s := findSvc(c.project(l.ProjectID), l.ServiceID)
	if s == nil || s.SourceType != "database" || s.DatabaseKind == nil {
		die("%s is not a database service", l.Service)
	}
	kind := *s.DatabaseKind
	var in instanceView
	if err := c.call("GET", l.instPath(), nil, &in); err != nil {
		die("%v", err)
	}
	if in.TCPProxy == nil {
		if !*enable {
			die("the TCP proxy of %s is off; enable it in the dashboard (Settings › Networking) or run: aven connect %s --enable-proxy", l.Service, l.Service)
		}
		if err := c.call("POST", l.instPath()+"/tcp-proxy", map[string]int{"container_port": dbDefaults[kind].port}, &in); err != nil {
			die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "aven: enabled TCP proxy on public port %d\n", in.TCPProxy.PublicPort)
	}
	vars := c.resolvedVars(l)
	raw := vars[dbDefaults[kind].urlKey]
	if raw == "" {
		die("%s has no %s variable", l.Service, dbDefaults[kind].urlKey)
	}
	bin, cargs, cenv, err := dbClient(kind, raw)
	if err != nil {
		die("%v", err)
	}
	if _, err := exec.LookPath(bin); err != nil {
		die("%s not found in PATH; install it first", bin)
	}
	os.Exit(runLocal(append(os.Environ(), cenv...), bin, cargs...))
}

// ---- redeploy / down / restart ----
// aven domain은 domain.go, aven tcp-proxy와 aven networking은 tcpproxy.go에 있다.

func cmdRedeploy(c *config, args []string) {
	fl := flag.NewFlagSet("redeploy", flag.ExitOnError)
	yes := fl.Bool("y", false, "don't ask for confirmation")
	fl.BoolVar(yes, "yes", false, "don't ask for confirmation")
	forceBuild := fl.Bool("force-build", false, "build the latest commit without reusing a retained image")
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	if *forceBuild {
		confirm(*yes, fmt.Sprintf("force a new build of the latest commit for %s (%s)?", l.Service, l.Environment))
		var d deploymentView
		if err := c.call("POST", l.instPath()+"/deploy", map[string]bool{"force_build": true}, &d); err != nil {
			die("%v", err)
		}
		fmt.Println("deployment", d.ID, d.Status)
		return
	}
	var deps []deploymentView
	if err := c.call("GET", l.instPath()+"/deployments?limit=1", nil, &deps); err != nil {
		die("%v", err)
	}
	if len(deps) == 0 {
		die("no deployments to redeploy (run: aven up)")
	}
	confirm(*yes, fmt.Sprintf("redeploy %s (%s) from %s?", l.Service, l.Environment, deps[0].ID))
	var d deploymentView
	if err := c.call("POST", "/deployments/"+url.PathEscape(deps[0].ID)+"/redeploy", nil, &d); err != nil {
		die("%v", err)
	}
	fmt.Println("deployment", d.ID, d.Status)
}

func cmdDown(c *config, args []string) {
	fl := flag.NewFlagSet("down", flag.ExitOnError)
	yes := fl.Bool("y", false, "don't ask for confirmation")
	fl.BoolVar(yes, "yes", false, "don't ask for confirmation")
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	confirm(*yes, fmt.Sprintf("stop %s in %s? it stays down until the next deploy", l.Service, l.Environment))
	if err := c.call("POST", l.instPath()+"/stop", nil, nil); err != nil {
		die("%v", err)
	}
	fmt.Printf("stopped %s (%s)\n", l.Service, l.Environment)
}

func cmdRestart(c *config, args []string) {
	fl := flag.NewFlagSet("restart", flag.ExitOnError)
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	if err := c.call("POST", l.instPath()+"/restart", nil, nil); err != nil {
		die("%v", err)
	}
	fmt.Printf("restarted %s (%s)\n", l.Service, l.Environment)
}

// ---- open / whoami / logout / version ----

// dashboardURL은 연결된 서비스의 캔버스 주소다(웹 UI의 /?project=&env=&svc= 형식).
func dashboardURL(base string, l *link) string {
	q := url.Values{"project": {l.ProjectID}}
	if l.Environment != "" {
		q.Set("env", l.Environment)
	}
	if l.ServiceID != "" {
		q.Set("svc", l.ServiceID)
	}
	return strings.TrimRight(base, "/") + "/?" + q.Encode()
}

func cmdOpen(c *config, args []string) {
	fl := flag.NewFlagSet("open", flag.ExitOnError)
	printOnly := fl.Bool("print", false, "only print the URL")
	tf := addTargetFlags(fl)
	fl.Parse(args)
	if c.URL == "" {
		die("not logged in (run: aven login <url>)")
	}
	u := dashboardURL(c.URL, tf.resolve(c))
	fmt.Println(u)
	if *printOnly {
		return
	}
	openBrowser(u)
}

// openBrowser는 기본 브라우저로 u를 연다(실패해도 조용히 넘어간다: 주소는 이미 출력했다).
func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Start()
}

type meInfo struct {
	User        string `json:"user"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	TOTPEnabled bool   `json:"totp_enabled"`
	Token       *struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
		Role  string `json:"role"`
	} `json:"token"`
}

// whoamiLine은 whoami 출력 한 줄이다. 옛 서버(user만 있음)도 그대로 보여 준다.
func whoamiLine(me meInfo, base, how string) string {
	who := me.User
	if me.Email != "" {
		who += " <" + me.Email + ">"
	}
	if me.Role != "" {
		who += " [" + me.Role + "]"
	}
	if me.Token != nil {
		how += ", token " + me.Token.Name + " " + me.Token.Scope + "/" + me.Token.Role
	}
	return fmt.Sprintf("%s @ %s (%s)", who, base, how)
}

// tokenEnvName is the credential variable that is set: AVEN_TOKEN, else KEEL_TOKEN.
func tokenEnvName() string {
	if os.Getenv("AVEN_TOKEN") != "" {
		return "AVEN_TOKEN"
	}
	if os.Getenv("KEEL_TOKEN") != "" {
		return "KEEL_TOKEN"
	}
	return ""
}

func cmdWhoami(c *config, _ []string) {
	var me meInfo
	if err := c.call("GET", "/auth/me", nil, &me); err != nil {
		die("%v", err)
	}
	how := "session"
	if name := tokenEnvName(); name != "" {
		how = name
	}
	fmt.Println(whoamiLine(me, c.URL, how))
}

type apiToken struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Prefix        string  `json:"prefix"`
	Scope         string  `json:"scope"`
	WorkspaceID   *string `json:"workspace_id"`
	ProjectID     *string `json:"project_id"`
	EnvironmentID *string `json:"environment_id"`
	Role          string  `json:"role"`
	ExpiresAt     *string `json:"expires_at"`
	LastUsedAt    *string `json:"last_used_at"`
	Token         string  `json:"token"`
}

// cmdTokens: aven tokens [list] | create --name n [--project p] [--environment e] [--role r] [--expires-days n] | revoke <id>
func cmdTokens(c *config, args []string) {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		var ts []apiToken
		if err := c.call("GET", "/tokens", nil, &ts); err != nil {
			die("%v", err)
		}
		if len(ts) == 0 {
			fmt.Println("no API tokens")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tSCOPE\tROLE\tEXPIRES\tLAST USED")
		for _, t := range ts {
			scope := t.Scope
			if t.EnvironmentID != nil {
				scope += ":" + *t.EnvironmentID
			} else if t.ProjectID != nil {
				scope += ":" + *t.ProjectID
			} else if t.WorkspaceID != nil {
				scope += ":" + *t.WorkspaceID
			}
			fmt.Fprintf(tw, "%s\t%s\t%s…\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Prefix, scope, t.Role, orDash(t.ExpiresAt), orDash(t.LastUsedAt))
		}
		tw.Flush()
	case "create", "new":
		fl := flag.NewFlagSet("tokens create", flag.ExitOnError)
		name := fl.String("name", "", "token name (required)")
		wsid := fl.String("workspace", "", "workspace ID (workspace-scoped token)")
		instanceToken := fl.Bool("instance", false, "instance operations only (instance admin required)")
		pf := fl.String("project", "", "project name or id (project-scoped token)")
		ef := fl.String("environment", "", "environment name or id (environment-scoped token; needs --project or a linked project)")
		role := fl.String("role", "member", "admin | member | viewer (never above your own role)")
		days := fl.Int("expires-days", 0, "expire after N days (0 = never)")
		fl.Parse(args)
		if *name == "" {
			die("usage: aven tokens create --name <name> [--project p] [--environment e] [--role member] [--expires-days n]")
		}
		body := map[string]any{"name": *name, "role": *role}
		if *instanceToken {
			if *wsid != "" || *pf != "" || *ef != "" {
				die("--instance cannot be combined with a workspace/project/environment")
			}
			body["scope"] = "instance"
		} else if *wsid != "" {
			body["workspace_id"] = *wsid
		}
		if *days > 0 {
			body["expires_in_days"] = *days
		}
		if *pf != "" || *ef != "" {
			pid := *pf
			if pid == "" {
				l := c.linked()
				if l == nil {
					die("--environment needs --project (or run aven link first)")
				}
				pid = l.ProjectID
			}
			p := c.findProject(pid)
			body["project_id"] = p.ID
			if *ef != "" {
				e := findEnv(p, *ef)
				if e == nil {
					die("environment %q not found in %s", *ef, p.Name)
				}
				body["environment_id"] = e.ID
			}
		}
		var t apiToken
		if err := c.call("POST", "/tokens", body, &t); err != nil {
			die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "created %s (%s, %s). It is shown only once:\n", t.ID, t.Scope, t.Role)
		fmt.Println(t.Token)
	case "revoke", "delete", "rm":
		if len(args) != 1 {
			die("usage: aven tokens revoke <id>")
		}
		if err := c.call("DELETE", "/tokens/"+url.PathEscape(args[0]), nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Println("revoked", args[0])
	default:
		die("usage: aven tokens [list|create|revoke]")
	}
}

// findProject는 이름 또는 id로 프로젝트를 찾는다.
func (c *config) findProject(s string) *project {
	var ps []project
	if err := c.call("GET", "/projects", nil, &ps); err != nil {
		die("%v", err)
	}
	for _, p := range ps {
		if p.ID == s {
			return c.project(p.ID)
		}
	}
	var match *project
	for i := range ps {
		if ps[i].Name != s {
			continue
		}
		if match != nil {
			die("project name %q exists in multiple workspaces; use its ID", s)
		}
		match = &ps[i]
	}
	if match != nil {
		return c.project(match.ID)
	}
	die("project %q not found", s)
	return nil
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	if len(*s) >= 10 {
		return (*s)[:10]
	}
	return *s
}

func cmdLogout(c *config, _ []string) {
	if c.Cookie != "" && c.URL != "" {
		if resp, err := c.req("POST", "/auth/logout", nil, ""); err == nil {
			resp.Body.Close()
		}
	}
	c.Cookie = ""
	if err := c.save(); err != nil {
		die("%v", err)
	}
	fmt.Println("logged out")
	if name := tokenEnvName(); name != "" {
		fmt.Fprintf(os.Stderr, "aven: %s is still set in the environment and will keep authenticating\n", name)
	}
}

func cmdVersion(c *config, _ []string) {
	fmt.Println("aven CLI", version)
	if c.URL == "" {
		return
	}
	var sys struct {
		Version string `json:"version"`
	}
	if err := c.call("GET", "/system", nil, &sys); err == nil {
		fmt.Println("aven-server", sys.Version, "at", c.URL)
	}
}
