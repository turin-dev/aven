package main

// repo 서비스는 start command로 로컬 프로세스를, image/database 서비스는 로컬 Docker 컨테이너를 띄운다.
// 사설 네트워크 호스트(<service>.<env>.keel.internal)는 localhost(Docker 안에서는 host.docker.internal)로 바꾼다.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type devOptions struct {
	action      string // up | down | clean | configure
	environment string
	dryRun      bool
	verbose     bool
	yes         bool
	// configure
	service string
	remove  bool
	command *string
	dir     *string
	port    *int
}

const devUsage = `usage:
  aven dev [up] [-e environment] [--dry-run] [-v]
  aven dev down [-e environment]
  aven dev clean [-e environment] [-y]
  aven dev configure [--service name] [--command cmd] [--dir path] [--port n]
  aven dev configure --remove [service]`

// optionalString은 지정 여부를 구분하는 문자열 플래그다(--command ""로 지울 수 있다).
type optionalString struct{ p **string }

func (o optionalString) String() string {
	if o.p == nil || *o.p == nil {
		return ""
	}
	return **o.p
}
func (o optionalString) Set(s string) error { *o.p = &s; return nil }

func parseDevArgs(args []string) (*devOptions, error) {
	o := &devOptions{action: "up"}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "up", "start":
			o.action = "up"
		case "down", "stop":
			o.action = "down"
		case "clean", "reset":
			o.action = "clean"
		case "configure", "config":
			o.action = "configure"
		default:
			return nil, fmt.Errorf("unknown subcommand %q\n%s", args[0], devUsage)
		}
		args = args[1:]
	}
	fl := flag.NewFlagSet("dev "+o.action, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	if o.action != "configure" {
		fl.StringVar(&o.environment, "e", "", "")
		fl.StringVar(&o.environment, "environment", "", "")
	}
	port := 0
	switch o.action {
	case "up":
		fl.BoolVar(&o.dryRun, "dry-run", false, "")
		fl.BoolVar(&o.verbose, "v", false, "")
		fl.BoolVar(&o.verbose, "verbose", false, "")
		fl.Bool("no-tui", false, "")
	case "clean":
		fl.BoolVar(&o.yes, "y", false, "")
		fl.BoolVar(&o.yes, "yes", false, "")
	case "configure":
		fl.StringVar(&o.service, "s", "", "")
		fl.StringVar(&o.service, "service", "", "")
		fl.BoolVar(&o.remove, "remove", false, "")
		fl.Var(optionalString{&o.command}, "command", "")
		fl.Var(optionalString{&o.dir}, "dir", "")
		fl.IntVar(&port, "port", -1, "")
	}
	if err := fl.Parse(args); err != nil {
		return nil, fmt.Errorf("%v\n%s", err, devUsage)
	}
	rest := fl.Args()
	if o.action == "configure" {
		fl.Visit(func(f *flag.Flag) {
			if f.Name == "port" {
				o.port = &port
			}
		})
		if o.port != nil && (*o.port < 0 || *o.port > 65535) {
			return nil, errors.New("--port must be between 0 and 65535 (0 clears it)")
		}
		if o.remove && len(rest) == 1 && o.service == "" {
			o.service, rest = rest[0], nil
		}
		if o.remove && (o.command != nil || o.dir != nil || o.port != nil) {
			return nil, errors.New("--remove cannot be combined with --command, --dir or --port")
		}
	}
	if len(rest) != 0 {
		return nil, errors.New(devUsage)
	}
	return o, nil
}

// ---- 서비스 계획 ----

type devInstance struct {
	ID            string  `json:"id"`
	PrivateDomain string  `json:"private_domain"`
	Image         *string `json:"image"`
	RootDir       string  `json:"root_dir"`
	StartCommand  *string `json:"start_command"`
	Port          *int    `json:"port"`
}

type devVolume struct {
	ID         string  `json:"id"`
	InstanceID *string `json:"service_instance_id"`
	Name       string  `json:"name"`
	MountPath  string  `json:"mount_path"`
}

type devService struct {
	service  svcRef
	instance devInstance
	// command와 dir는 로컬 설정(aven dev configure)을 반영한 repo 서비스의 실행 방법이다.
	command string
	dir     string
	// vars는 로컬 프로세스용, containerVars는 Docker 컨테이너용 변수다.
	vars          map[string]string
	containerVars map[string]string
	sealed        []string
	containerPort int
	localPort     int
	volumes       []devVolume
}

func (s *devService) container() bool { return s.service.SourceType != "repo" }

func (s *devService) kind() string {
	switch {
	case s.service.SourceType == "database":
		return "database"
	case s.container():
		return "docker"
	default:
		return "local"
	}
}

// devSkip은 로컬에서 실행할 수 없어 건너뛴 서비스와 그 이유다.
type devSkip struct{ name, reason string }

func loadDevServices(c *config, l *link, p *project, local devProjectConfig) ([]*devService, []devSkip, error) {
	if len(p.Services) == 0 {
		return nil, nil, errors.New("project has no services")
	}
	var volumes []devVolume
	if err := c.call("GET", "/environments/"+url.PathEscape(l.EnvironmentID)+"/volumes", nil, &volumes); err != nil {
		return nil, nil, err
	}
	volumesByInstance := map[string][]devVolume{}
	for _, v := range volumes {
		if v.InstanceID != nil {
			volumesByInstance[*v.InstanceID] = append(volumesByInstance[*v.InstanceID], v)
		}
	}
	var services []*devService
	var skipped []devSkip
	for _, svc := range p.Services {
		if svc.SourceType != "repo" && svc.SourceType != "image" && svc.SourceType != "database" {
			skipped = append(skipped, devSkip{svc.Name, svc.SourceType + " services cannot run locally"})
			continue
		}
		sl := *l
		sl.ServiceID, sl.Service = svc.ID, svc.Name
		var inst devInstance
		if err := c.call("GET", sl.instPath(), nil, &inst); err != nil {
			return nil, nil, fmt.Errorf("load %s settings: %w", svc.Name, err)
		}
		item := &devService{service: svc, instance: inst, volumes: volumesByInstance[inst.ID]}
		if svc.SourceType == "repo" {
			item.command = strPtrValue(inst.StartCommand)
			item.dir = inst.RootDir
		}
		ov := local[svc.ID]
		if ov != nil && svc.SourceType == "repo" {
			if ov.Command != "" {
				item.command = ov.Command
			}
			if ov.Directory != "" {
				item.dir = ov.Directory
			}
			if ov.Port > 0 {
				item.instance.Port = &ov.Port
			}
		}
		switch {
		case svc.SourceType == "repo" && strings.TrimSpace(item.command) == "":
			skipped = append(skipped, devSkip{svc.Name, "no start command (run: aven dev configure --service " + svc.Name + ")"})
			continue
		case svc.SourceType != "repo" && strPtrValue(inst.Image) == "":
			skipped = append(skipped, devSkip{svc.Name, "no Docker image configured"})
			continue
		}
		var raw map[string]resolvedValue
		if err := c.call("GET", sl.instPath()+"/variables/resolved", nil, &raw); err != nil {
			return nil, nil, fmt.Errorf("load %s variables: %w", svc.Name, err)
		}
		vars, sealed := visibleResolved(raw)
		for name, value := range vars {
			switch value {
			case "•••":
				return nil, nil, fmt.Errorf("variables for %s are masked; aven dev needs an account that can read variable values", svc.Name)
			case "<sealed>":
				delete(vars, name)
				sealed = append(sealed, name)
			}
		}
		sort.Strings(sealed)
		item.vars, item.containerVars, item.sealed = vars, cloneStringMap(vars), sealed
		item.containerPort = devServicePort(item)
		services = append(services, item)
	}
	return services, skipped, nil
}

func strPtrValue(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func devServicePort(s *devService) int {
	if s.instance.Port != nil && *s.instance.Port > 0 && *s.instance.Port < 65536 {
		return *s.instance.Port
	}
	if p, err := strconv.Atoi(s.vars["PORT"]); err == nil && p > 0 && p < 65536 {
		return p
	}
	if s.service.DatabaseKind != nil {
		switch *s.service.DatabaseKind {
		case "postgres":
			return 5432
		case "mysql":
			return 3306
		case "redis":
			return 6379
		case "mongo":
			return 27017
		case "bucket":
			return 9000
		}
	}
	return 8080
}

// reserveDevPorts는 고른 loopback 포트를 계획이 끝날 때까지 잡아 둔다.
// 서비스 포트가 이미 쓰이고 있으면 남는 임시 포트를 쓴다.
func reserveDevPorts(services []*devService) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(services))
	for _, svc := range services {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(svc.containerPort)))
		if err != nil {
			ln, err = net.Listen("tcp", "127.0.0.1:0")
		}
		if err != nil {
			closeDevPorts(listeners)
			return nil, fmt.Errorf("reserve a local port for %s: %w", svc.service.Name, err)
		}
		listeners = append(listeners, ln)
		svc.localPort = ln.Addr().(*net.TCPAddr).Port
	}
	return listeners, nil
}

func closeDevPorts(listeners []net.Listener) {
	for _, ln := range listeners {
		_ = ln.Close()
	}
}

func devPrivateDomain(s *devService, environment string) string {
	if host := strings.TrimSpace(s.instance.PrivateDomain); host != "" {
		return host
	}
	return s.service.Name + "." + environment + ".keel.internal"
}

// devHostTarget은 사설 호스트를 바꿀 대상이다. host:<from>은 host:<to>가 되고, 다른 포트는 그대로 둔다.
type devHostTarget struct {
	host     string
	fromPort int
	toPort   int
}

var devHostToken = regexp.MustCompile(`[A-Za-z0-9_.-]+(?::[0-9]+)?`)

// rewriteDevHosts는 value 안의 호스트 이름(정확히 같은 토큰만, 대소문자 무시)을 targets로 바꾼다.
// "postgres.dev.keel.internal:5432"처럼 포트가 붙으면 그 포트도 로컬 포트로 바꾼다.
func rewriteDevHosts(value string, targets map[string]devHostTarget) string {
	if len(targets) == 0 {
		return value
	}
	return devHostToken.ReplaceAllStringFunc(value, func(tok string) string {
		host, port, hasPort := strings.Cut(tok, ":")
		t, ok := targets[strings.ToLower(host)]
		if !ok {
			return tok
		}
		if !hasPort {
			return t.host
		}
		if p, err := strconv.Atoi(port); err == nil && p == t.fromPort {
			return net.JoinHostPort(t.host, strconv.Itoa(t.toPort))
		}
		return t.host + ":" + port
	})
}

// devRewrites는 로컬 프로세스용(local)과 컨테이너용(container) 호스트 치환표다.
// 컨테이너끼리는 임시 Docker 네트워크의 alias(원래 사설 호스트)로 닿으므로 repo 서비스만 바꾼다.
func devRewrites(services []*devService, environment string) (local, container map[string]devHostTarget) {
	local, container = map[string]devHostTarget{}, map[string]devHostTarget{}
	for _, s := range services {
		host := strings.ToLower(devPrivateDomain(s, environment))
		local[host] = devHostTarget{host: "localhost", fromPort: s.containerPort, toPort: s.localPort}
		if !s.container() {
			container[host] = devHostTarget{host: "host.docker.internal", fromPort: s.containerPort, toPort: s.localPort}
		}
	}
	return local, container
}

func rewriteDevVariables(services []*devService, environment string) {
	local, container := devRewrites(services, environment)
	for _, svc := range services {
		for key, value := range svc.vars {
			svc.vars[key] = rewriteDevHosts(value, local)
		}
		for key, value := range svc.containerVars {
			svc.containerVars[key] = rewriteDevHosts(value, container)
		}
		if svc.container() {
			svc.containerVars["PORT"] = strconv.Itoa(svc.containerPort)
		} else {
			svc.vars["PORT"] = strconv.Itoa(svc.localPort)
		}
	}
}

// devChangedKeys는 치환으로 값이 바뀐 변수 이름이다(-v 출력용, 값은 보여 주지 않는다).
func devChangedKeys(before, after map[string]string) []string {
	var keys []string
	for k, v := range after {
		if old, ok := before[k]; ok && old != v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// devWorkingDir은 서비스 root 디렉터리를 연결된 디렉터리(base) 기준으로 정한다.
// 서버 설정(root_dir)은 base 안이어야 하고, 로컬 설정(allowAbs)은 절대 경로도 된다.
func devWorkingDir(base, root string, allowAbs bool) (string, error) {
	root = strings.TrimSpace(root)
	var dir string
	switch {
	case allowAbs && filepath.IsAbs(root):
		dir = filepath.Clean(root)
	default:
		root = strings.TrimLeft(root, "/\\")
		if root == "" {
			return base, nil
		}
		clean := filepath.Clean(filepath.FromSlash(root))
		if !allowAbs && !filepath.IsLocal(clean) {
			return "", fmt.Errorf("root directory %q must stay inside the linked directory", root)
		}
		dir = filepath.Join(base, clean)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("service directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("service directory %s is not a directory", dir)
	}
	return dir, nil
}

// linkedDir은 현재 디렉터리에 적용되는 링크가 있는 디렉터리다(linked()와 같은 탐색).
func linkedDir(c *config) string {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if _, ok := c.Links[d]; ok {
			return d
		}
		if d == filepath.Dir(d) {
			return dir
		}
	}
}

func devSlug(s string) string {
	s = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-_.")
}

func cmdDev(c *config, args []string) {
	o, err := parseDevArgs(args)
	if err != nil {
		die("%v", err)
	}
	l := *c.linked()
	p := c.project(l.ProjectID)
	l.Project = p.Name
	envFilter := ""
	if o.environment != "" {
		e := findEnv(p, o.environment)
		if e == nil {
			die("environment %q not found (%s)", o.environment, names(p.Environments, func(e envRef) string { return e.Name }))
		}
		l.EnvironmentID, l.Environment = e.ID, e.Name
		envFilter = e.ID
	}
	switch o.action {
	case "configure":
		cmdDevConfigure(p, o)
		return
	case "down", "clean":
		docker, err := devDocker()
		if err != nil {
			die("%v", err)
		}
		if o.action == "clean" {
			confirm(o.yes, fmt.Sprintf("stop local services of %s and delete their local Docker volumes (data)?", p.Name))
		}
		if err := devDown(docker, p.ID, envFilter, o.action == "clean", os.Stderr); err != nil {
			die("%v", err)
		}
		return
	}
	if l.EnvironmentID == "" {
		die("project has no linked environment; run: aven environment use <name>")
	}
	local, err := loadDevConfig()
	if err != nil {
		die("%v", err)
	}
	services, skipped, err := loadDevServices(c, &l, p, local.Projects[p.ID])
	if err != nil {
		die("%v", err)
	}
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "aven dev: skipping %s: %s\n", s.name, s.reason)
	}
	if len(services) == 0 {
		die("no services can run locally")
	}
	for _, svc := range services {
		if len(svc.sealed) > 0 {
			fmt.Fprintf(os.Stderr, "aven dev: sealed variables are unavailable for %s: %s\n", svc.service.Name, strings.Join(svc.sealed, ", "))
		}
	}
	ports, err := reserveDevPorts(services)
	if err != nil {
		die("%v", err)
	}
	defer closeDevPorts(ports)
	before := make([]map[string]string, len(services))
	for i, svc := range services {
		before[i] = cloneStringMap(svc.vars)
	}
	rewriteDevVariables(services, l.Environment)
	docker := ""
	for _, svc := range services {
		if svc.container() {
			if docker, err = devDocker(); err != nil {
				die("%v", err)
			}
			break
		}
	}
	network := ""
	if docker != "" {
		network = fmt.Sprintf("keel-dev-%s-%d", devSlug(l.Environment), os.Getpid())
	}
	plan, err := devProcessPlan(linkedDir(c), &l, services, docker, network)
	if err != nil {
		die("%v", err)
	}
	printDevSummary(os.Stderr, p.Name, l.Environment, services, plan)
	if o.verbose || o.dryRun {
		printDevRewrites(os.Stderr, services, before, l.Environment)
	}
	if o.dryRun {
		printDevPlan(os.Stdout, plan)
		return
	}
	if docker != "" {
		if n := devLeftovers(docker, p.ID, l.EnvironmentID); n > 0 {
			fmt.Fprintf(os.Stderr, "aven dev: %d container(s) from another aven dev session are still running (stop them with: aven dev down)\n", n)
		}
	}
	closeDevPorts(ports)
	ports = nil
	if err := runDevSession(plan, docker, network, os.Stdout); err != nil {
		die("%v", err)
	}
}

func printDevSummary(w io.Writer, projectName, envName string, services []*devService, plan []devProcessSpec) {
	fmt.Fprintf(w, "aven dev: %s / %s — %d service(s); press Ctrl-C to stop\n", projectName, envName, len(services))
	width := 0
	for _, s := range services {
		width = max(width, len(s.service.Name))
	}
	for i, s := range services {
		detail := plan[i].describe
		fmt.Fprintf(w, "  %-*s  %-8s  localhost:%-5d  %s\n", width, s.service.Name, s.kind(), s.localPort, detail)
		if s.localPort != s.containerPort {
			fmt.Fprintf(w, "  %-*s  port %d is busy; using %d (standalone *PORT variables still say %d)\n", width, "", s.containerPort, s.localPort, s.containerPort)
		}
	}
}

func printDevRewrites(w io.Writer, services []*devService, before []map[string]string, environment string) {
	local, container := devRewrites(services, environment)
	hosts := make([]string, 0, len(local))
	for h := range local {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	anyContainer := false
	for _, s := range services {
		anyContainer = anyContainer || s.container()
	}
	fmt.Fprintln(w, "aven dev: private host replacements")
	for _, h := range hosts {
		t := local[h]
		line := fmt.Sprintf("  %s:%d → localhost:%d", h, t.fromPort, t.toPort)
		if ct, ok := container[h]; ok && anyContainer {
			line += fmt.Sprintf(" (in Docker: host.docker.internal:%d)", ct.toPort)
		}
		fmt.Fprintln(w, line)
	}
	for i, s := range services {
		after := s.vars
		if s.container() {
			after = s.containerVars
		}
		if keys := devChangedKeys(before[i], after); len(keys) > 0 {
			fmt.Fprintf(w, "  %s: rewrote %s\n", s.service.Name, strings.Join(keys, ", "))
		}
	}
}
