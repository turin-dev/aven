// Package compose는 docker compose 파일을 aven 서비스 계획(Plan)으로 바꾼다.
// 파싱과 보간은 compose-go가 하고, 여기서는 aven이 표현할 수 있는 것만 매핑하고
// 표현할 수 없는 것은 오류(가져올 수 없음) 또는 경고(무시함)로 알린다.
package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v4"

	"github.com/turin-dev/aven/internal/store"
)

func init() { logrus.SetLevel(logrus.PanicLevel) } // compose-go가 보간 경고를 전역 로거로 찍는다. 경고는 Plan에 담는다.

// Input은 가져올 compose 파일과 부가 정보다.
type Input struct {
	YAML     string            // compose 파일 내용
	Filename string            // 보고용 파일 이름
	Stack    string            // 스택 이름(비우면 compose의 name:)
	Vars     map[string]string // ${VAR} 보간에 쓰는 값(.env)
	EnvFiles map[string]string // env_file 경로 → 내용(저장소에서 읽을 수 없는 파일을 직접 줄 때)

	// 저장소에서 가져올 때: RepoDir는 clone 위치, ComposeDir는 compose 파일이 있는 디렉터리(RepoDir 안).
	RepoURL, Branch string
	RepoDir         string
	ComposeDir      string

	AllowBindMounts bool // bind mount를 허용한다(로컬 노드 전용, 다중 노드에서는 깨진다)
}

// Issue는 가져오기에서 발견한 문제다. Severity가 error면 apply할 수 없다.
type Issue struct {
	Severity string `json:"severity"` // error | warning | info
	Service  string `json:"service,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type VolumePlan struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	ReadOnly  bool   `json:"read_only"`
	Compose   string `json:"compose_volume,omitempty"`
}

// DomainCandidate는 공개 도메인으로 쓸 수 있는 HTTP 포트다. 기본으로는 공개하지 않는다.
type DomainCandidate struct {
	Port   int    `json:"port"`
	Host   string `json:"host,omitempty"`   // traefik 라벨에서 찾은 호스트 힌트
	Source string `json:"source,omitempty"` // ports | expose | traefik
}

type BuildPlan struct {
	Context    string   `json:"context"`
	Dockerfile string   `json:"dockerfile"`
	Target     string   `json:"target,omitempty"`
	ArgKeys    []string `json:"arg_keys,omitempty"`
}

// ServicePlan은 compose 서비스 하나가 aven 서비스로 어떻게 매핑되는지다.
type ServicePlan struct {
	Name        string     `json:"name"`         // aven 서비스 이름
	ComposeName string     `json:"compose_name"` // compose 서비스 이름
	SourceType  string     `json:"source_type"`  // image | repo
	Icon        string     `json:"icon"`
	Image       string     `json:"image,omitempty"`
	Build       *BuildPlan `json:"build,omitempty"`

	Port                *int               `json:"port"`
	HealthcheckType     string             `json:"healthcheck_type"`
	HealthcheckPath     string             `json:"healthcheck_path"`
	HealthcheckTimeoutS int                `json:"healthcheck_timeout_s"`
	RestartPolicy       string             `json:"restart_policy"`
	RestartMaxRetries   int                `json:"restart_max_retries"`
	Replicas            int                `json:"replicas"`
	Entrypoint          []string           `json:"entrypoint,omitempty"`
	Command             []string           `json:"command,omitempty"`
	EnvKeys             []string           `json:"env_keys"`
	Volumes             []VolumePlan       `json:"volumes"`
	BindMounts          []store.BindMount  `json:"bind_mounts,omitempty"`
	Aliases             []string           `json:"aliases"`
	DependsOn           []store.Dependency `json:"depends_on"`
	DomainCandidates    []DomainCandidate  `json:"domain_candidates"`
	TCPCandidates       []int              `json:"tcp_candidates"`
	Issues              []Issue            `json:"issues"`

	// 내부: apply가 인스턴스에 쓰는 값
	Variables map[string]string `json:"-"`
	Spec      store.Spec        `json:"-"`
}

// Plan은 compose 파일 전체의 매핑 결과다.
type Plan struct {
	Stack       string         `json:"stack"`
	Services    []*ServicePlan `json:"services"`
	Levels      [][]string     `json:"levels"` // 배포 순서(compose 서비스 이름, 같은 단계는 동시에)
	Warnings    []Issue        `json:"warnings"`
	Errors      []Issue        `json:"errors"`
	MissingVars []string       `json:"missing_variables"`
	Skipped     []string       `json:"skipped_services,omitempty"` // 활성화되지 않은 profile 등
}

// OK는 apply할 수 있는지 알려준다.
func (p *Plan) OK() bool { return len(p.Errors) == 0 }

func (p *Plan) AddIssue(i Issue) { p.add(i) }

func (p *Plan) add(i Issue) {
	if i.Severity == "error" {
		p.Errors = append(p.Errors, i)
	} else {
		p.Warnings = append(p.Warnings, i)
	}
	for _, s := range p.Services {
		if s.ComposeName == i.Service {
			s.Issues = append(s.Issues, i)
		}
	}
}

// Service는 compose 이름으로 서비스 계획을 찾는다.
func (p *Plan) Service(composeName string) *ServicePlan {
	for _, s := range p.Services {
		if s.ComposeName == composeName {
			return s
		}
	}
	return nil
}

var (
	nameInvalid = regexp.MustCompile(`[^a-z0-9-]+`)
	stackRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
)

// KeelName은 compose 서비스 이름을 aven 서비스 이름 형식으로 바꾼다.
func KeelName(s string) string {
	s = nameInvalid.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = "service"
	}
	return s
}

// SanitizeStack은 스택 이름으로 쓸 수 있는 형식으로 바꾼다.
func SanitizeStack(s string) string { return KeelName(s) }

// ValidStack은 스택 이름이 올바른지 알려준다.
func ValidStack(s string) bool { return stackRe.MatchString(s) }

// Parse는 compose 파일을 읽어 Plan을 만든다. 이름(Name)은 AssignNames로 정한다.
func Parse(ctx context.Context, in Input) (*Plan, error) {
	plan := &Plan{Services: []*ServicePlan{}, Levels: [][]string{}, Warnings: []Issue{}, Errors: []Issue{}, MissingVars: []string{}}
	if strings.TrimSpace(in.YAML) == "" {
		return nil, errors.New("compose file is empty")
	}
	if in.Filename == "" {
		in.Filename = "docker-compose.yml"
	}
	workDir := in.ComposeDir
	if workDir == "" {
		workDir = nowhere
	}
	// 보간 전에 YAML에서 ${VAR}를 모아 값이 없는 것을 알려준다.
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(in.YAML), &raw); err != nil {
		return nil, fmt.Errorf("compose yaml: %w", err)
	}
	vars := template.ExtractVariables(raw, template.DefaultPattern)
	for name, v := range vars {
		if _, ok := in.Vars[name]; !ok && v.DefaultValue == "" && v.PresenceValue == "" && !v.Required {
			plan.MissingVars = append(plan.MissingVars, name)
		}
	}
	sort.Strings(plan.MissingVars)
	for _, v := range plan.MissingVars {
		plan.add(Issue{Severity: "warning", Code: "missing_variable", Message: fmt.Sprintf("variable ${%s} has no value and no default; it is replaced by an empty string", v)})
	}

	env := types.Mapping{}
	for k, v := range in.Vars {
		env[k] = v
	}
	details := types.ConfigDetails{
		WorkingDir:  workDir,
		ConfigFiles: []types.ConfigFile{{Filename: filepath.Join(workDir, in.Filename), Content: []byte(in.YAML)}},
		Environment: env,
	}
	proj, err := loader.LoadWithContext(ctx, details, func(o *loader.Options) {
		o.SetProjectName(projectName(in, raw), true)
		o.ResolvePaths = true
		o.SkipResolveEnvironment = true // env_file은 아래에서 직접 읽는다(저장소 밖 파일을 읽지 않으려고)
		o.SkipInclude = true
		o.SkipExtends = true
		o.SkipConsistencyCheck = false
	})
	if err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	plan.Stack = in.Stack
	if plan.Stack == "" {
		plan.Stack = SanitizeStack(proj.Name)
	}
	if _, ok := raw["include"]; ok {
		plan.add(Issue{Severity: "warning", Code: "include", Message: "top-level include is not followed; merge the included files into one compose file"})
	}
	if len(proj.Secrets) > 0 || len(proj.Configs) > 0 {
		// 서비스가 쓰지 않으면 문제 없다. 쓰는 서비스는 서비스 단계에서 오류로 막는다.
		plan.add(Issue{Severity: "info", Code: "secrets_configs_declared", Message: "top-level secrets/configs are declared (only an error for services that use them)"})
	}
	for name := range proj.DisabledServices {
		plan.Skipped = append(plan.Skipped, name)
	}
	sort.Strings(plan.Skipped)

	names := make([]string, 0, len(proj.Services))
	for n := range proj.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	volUsers := map[string][]string{} // named volume → services
	customNets := map[string]bool{}
	for _, n := range names {
		sp := convertService(plan, proj, in, n, proj.Services[n], customNets)
		plan.Services = append(plan.Services, sp)
		for _, v := range sp.Volumes {
			if v.Compose != "" {
				volUsers[v.Compose] = append(volUsers[v.Compose], n)
			}
		}
	}
	var shared []string
	for v, users := range volUsers {
		if len(users) > 1 {
			shared = append(shared, v)
		}
	}
	sort.Strings(shared)
	for _, v := range shared {
		users := volUsers[v]
		sort.Strings(users)
		plan.add(Issue{Severity: "error", Service: users[0], Code: "shared_volume",
			Message: fmt.Sprintf("named volume %q is shared by %s; a aven volume belongs to exactly one service. Give each service its own volume or move the shared data into one service", v, strings.Join(users, ", "))})
	}
	if len(customNets) > 1 {
		plan.add(Issue{Severity: "warning", Code: "networks_merged", Message: "multiple compose networks are merged into the single environment network (network isolation between services is not preserved)"})
	}
	if err := order(plan); err != nil {
		plan.add(Issue{Severity: "error", Code: "depends_on_cycle", Message: err.Error()})
	}
	return plan, nil
}

func projectName(in Input, raw map[string]any) string {
	if in.Stack != "" {
		return in.Stack
	}
	if n, ok := raw["name"].(string); ok && n != "" {
		return loader.NormalizeProjectName(n)
	}
	return "stack"
}

// order는 depends_on으로 배포 단계를 만든다(Kahn).
func order(plan *Plan) error {
	deps := map[string][]string{}
	all := map[string]bool{}
	for _, s := range plan.Services {
		all[s.ComposeName] = true
	}
	for _, s := range plan.Services {
		for _, d := range s.DependsOn {
			if all[d.Service] {
				deps[s.ComposeName] = append(deps[s.ComposeName], d.Service)
			}
		}
	}
	levels, err := Levels(composeNames(plan), deps)
	if err != nil {
		return err
	}
	plan.Levels = levels
	// Services를 배포 순서(단계, 이름)로 정렬한다.
	pos := map[string]int{}
	i := 0
	for _, l := range levels {
		for _, n := range l {
			pos[n] = i
			i++
		}
	}
	sort.SliceStable(plan.Services, func(a, b int) bool { return pos[plan.Services[a].ComposeName] < pos[plan.Services[b].ComposeName] })
	return nil
}

func composeNames(plan *Plan) []string {
	out := []string{}
	for _, s := range plan.Services {
		out = append(out, s.ComposeName)
	}
	return out
}

// Levels는 의존 그래프(deps[x] = x가 의존하는 것들)의 위상 정렬 단계를 돌려준다. 순환이면 오류.
func Levels(names []string, deps map[string][]string) ([][]string, error) {
	remaining := map[string]bool{}
	for _, n := range names {
		remaining[n] = true
	}
	done := map[string]bool{}
	var levels [][]string
	for len(remaining) > 0 {
		var level []string
		for n := range remaining {
			ready := true
			for _, d := range deps[n] {
				if remaining[d] && d != n {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, n)
			}
		}
		if len(level) == 0 {
			var rest []string
			for n := range remaining {
				rest = append(rest, n)
			}
			sort.Strings(rest)
			return nil, fmt.Errorf("depends_on cycle among: %s", strings.Join(rest, ", "))
		}
		sort.Strings(level)
		for _, n := range level {
			delete(remaining, n)
			done[n] = true
		}
		levels = append(levels, level)
	}
	return levels, nil
}

// AssignNames는 aven 서비스 이름과 DNS alias를 정한다.
// existing: 이 스택이 이미 만든 서비스(compose 이름 → aven 이름). taken: 프로젝트에서 이미 쓰는 이름(이 스택 것 제외).
func (p *Plan) AssignNames(existing map[string]string, taken map[string]bool) {
	used := map[string]bool{}
	for k := range taken {
		used[k] = true
	}
	for _, s := range p.Services {
		if n, ok := existing[s.ComposeName]; ok {
			s.Name = n
			used[n] = true
		}
	}
	for _, s := range p.Services {
		if s.Name != "" {
			continue
		}
		base := KeelName(s.ComposeName)
		name := base
		if used[name] || name == "shared" {
			pref := p.Stack + "-" + base
			if len(pref) > 40 {
				pref = strings.TrimRight(pref[:40], "-")
			}
			name = pref
			for i := 2; used[name]; i++ {
				suffix := "-" + strconv.Itoa(i)
				b := pref
				if len(b)+len(suffix) > 40 {
					b = strings.TrimRight(b[:40-len(suffix)], "-")
				}
				name = b + suffix
			}
			p.add(Issue{Severity: "warning", Service: s.ComposeName, Code: "renamed",
				Message: fmt.Sprintf("service name %q is already used in this project; created as %q (it still resolves as %q inside the environment)", base, name, s.ComposeName)})
		}
		s.Name = name
		used[name] = true
	}
	for _, s := range p.Services {
		s.Aliases = aliasesFor(s)
		s.Spec.Compose.Aliases = s.Aliases
	}
}

var aliasRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func aliasesFor(s *ServicePlan) []string {
	seen := map[string]bool{s.Name: true}
	out := []string{}
	add := func(a string) {
		if a != "" && !seen[a] && aliasRe.MatchString(a) {
			seen[a] = true
			out = append(out, a)
		}
	}
	add(s.ComposeName)
	for _, a := range s.Spec.Compose.Aliases { // 파싱 단계에서 모아 둔 networks.aliases / links
		add(a)
	}
	sort.Strings(out)
	return out
}

// ---------------- service conversion ----------------

var knownTCPPorts = map[int]bool{
	21: true, 22: true, 25: true, 53: true, 110: true, 143: true, 389: true, 465: true, 587: true, 636: true, 993: true, 995: true,
	1433: true, 1521: true, 1883: true, 2181: true, 3306: true, 4222: true, 5432: true, 5672: true, 6379: true, 6380: true,
	8883: true, 9042: true, 9092: true, 11211: true, 27017: true,
}

func imageIcon(image string) string {
	base := image
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexAny(base, ":@"); i >= 0 {
		base = base[:i]
	}
	switch {
	case strings.HasPrefix(base, "postgres"), strings.HasPrefix(base, "pgvector"):
		return "postgres"
	case strings.HasPrefix(base, "redis"), strings.HasPrefix(base, "valkey"):
		return "redis"
	case strings.HasPrefix(base, "mysql"), strings.HasPrefix(base, "mariadb"):
		return "mysql"
	case strings.HasPrefix(base, "mongo"):
		return "mongo"
	}
	return "docker"
}

func portOf(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

func convertService(plan *Plan, proj *types.Project, in Input, name string, svc types.ServiceConfig, customNets map[string]bool) *ServicePlan {
	sp := &ServicePlan{ComposeName: name, SourceType: "image", Icon: "docker", EnvKeys: []string{}, Volumes: []VolumePlan{}, Aliases: []string{},
		DependsOn: []store.Dependency{}, DomainCandidates: []DomainCandidate{}, TCPCandidates: []int{}, Issues: []Issue{}, Variables: map[string]string{}}
	plan.Services = append(plan.Services, sp) // add()가 이슈를 서비스에 붙일 수 있게 먼저 넣는다
	defer func() { plan.Services = plan.Services[:len(plan.Services)-1] }()
	errf := func(code, f string, a ...any) {
		plan.add(Issue{Severity: "error", Service: name, Code: code, Message: fmt.Sprintf(f, a...)})
	}
	warnf := func(code, f string, a ...any) {
		plan.add(Issue{Severity: "warning", Service: name, Code: code, Message: fmt.Sprintf(f, a...)})
	}

	spec := &sp.Spec
	spec.Defaults()
	cs := &store.ComposeSpec{Name: name}
	spec.Compose = cs

	// ---- source ----
	switch {
	case svc.Build != nil:
		b := svc.Build
		if in.RepoURL == "" || in.RepoDir == "" {
			errf("build_needs_repo", "build: is only supported when importing from a git repository (the build context must be in the repo); use image: for pasted YAML")
		} else if b.DockerfileInline != "" {
			errf("dockerfile_inline", "build.dockerfile_inline is not supported; commit a Dockerfile to the repository")
		} else if strings.Contains(b.Context, "://") || strings.HasPrefix(b.Context, "git@") {
			errf("remote_build_context", "build.context %q is a remote URL; use a path inside the repository", b.Context)
		} else {
			rel, err := filepath.Rel(in.RepoDir, b.Context)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				errf("build_context_outside_repo", "build.context %q is outside the repository", b.Context)
			} else {
				rel = filepath.ToSlash(rel)
				spec.RepoURL = &in.RepoURL
				spec.Branch = in.Branch
				if spec.Branch == "" {
					spec.Branch = "main"
				}
				spec.RootDir = "/" + strings.TrimPrefix(path.Clean("/"+rel), "/")
				spec.Builder = "dockerfile"
				df := b.Dockerfile
				if df == "" {
					df = "Dockerfile"
				} else if filepath.IsAbs(df) {
					// compose-go가 context 기준 상대 경로를 절대 경로로 풀었을 수 있다.
					if r, err := filepath.Rel(b.Context, df); err == nil && !strings.HasPrefix(r, "..") {
						df = r
					} else {
						errf("dockerfile_outside_context", "build.dockerfile %q is outside the build context", b.Dockerfile)
					}
				}
				spec.DockerfilePath = filepath.ToSlash(df)
				sp.SourceType, sp.Icon = "repo", "github"
				sp.Build = &BuildPlan{Context: spec.RootDir, Dockerfile: spec.DockerfilePath, Target: b.Target}
				cs.Target = b.Target
				if len(b.Args) > 0 {
					cs.BuildArgs = map[string]string{}
					for k, v := range b.Args {
						if v != nil {
							cs.BuildArgs[k] = *v
							sp.Build.ArgKeys = append(sp.Build.ArgKeys, k)
						}
					}
					sort.Strings(sp.Build.ArgKeys)
				}
				if len(b.AdditionalContexts) > 0 {
					errf("additional_contexts", "build.additional_contexts is not supported")
				}
				if len(b.Secrets) > 0 || len(b.SSH) > 0 {
					errf("build_secrets", "build.secrets / build.ssh are not supported")
				}
			}
		}
	case svc.Image != "":
		sp.Image = svc.Image
		spec.Image = &sp.Image
		sp.Icon = imageIcon(svc.Image)
		if !imageRe.MatchString(svc.Image) || strings.HasSuffix(svc.Image, ":") || strings.HasSuffix(svc.Image, "/") {
			errf("invalid_image", "invalid image reference %q (is a ${VARIABLE} in the tag missing?)", svc.Image)
		}
	default:
		errf("no_source", "service has neither image nor build")
	}

	// ---- unsupported runtime features ----
	nm := svc.NetworkMode
	switch {
	case nm == "" || nm == "default" || nm == "bridge":
	default:
		errf("network_mode", "network_mode: %s is not supported (aven services always join the environment network). Run host-network services outside aven", nm)
	}
	if svc.Pid != "" {
		errf("pid", "pid: %s is not supported", svc.Pid)
	}
	if svc.Ipc != "" && svc.Ipc != "private" {
		errf("ipc", "ipc: %s is not supported", svc.Ipc)
	}
	if svc.Uts != "" {
		errf("uts", "uts: %s is not supported", svc.Uts)
	}
	if svc.Privileged {
		errf("privileged", "privileged: true is not supported")
	}
	if len(svc.Devices) > 0 {
		errf("devices", "devices are not supported")
	}
	if len(svc.CapAdd) > 0 {
		errf("cap_add", "cap_add (%s) is not supported", strings.Join(svc.CapAdd, ", "))
	}
	if len(svc.SecurityOpt) > 0 {
		errf("security_opt", "security_opt is not supported")
	}
	if len(svc.Sysctls) > 0 {
		errf("sysctls", "sysctls are not supported")
	}
	if len(svc.VolumesFrom) > 0 {
		errf("volumes_from", "volumes_from is not supported; mount named volumes instead")
	}
	if svc.UserNSMode != "" || svc.Cgroup != "" || svc.CgroupParent != "" || svc.Runtime != "" || len(svc.Gpus) > 0 {
		errf("host_features", "userns_mode / cgroup / runtime / gpus are not supported")
	}
	if len(svc.Secrets) > 0 {
		errf("secrets", "secrets are not supported: aven cannot mount files into containers. Pass the value through an environment variable (and the *_FILE-less variant of the image) instead")
	}
	if len(svc.Configs) > 0 {
		errf("configs", "configs are not supported: aven cannot mount files into containers. Bake the file into the image or pass it through an environment variable")
	}
	if len(svc.PostStart) > 0 || len(svc.PreStop) > 0 || len(svc.PreStart) > 0 {
		errf("hooks", "lifecycle hooks (post_start/pre_stop) are not supported")
	}
	for _, w := range []struct {
		code string
		set  bool
		msg  string
	}{
		{"ignored_cap_drop", len(svc.CapDrop) > 0, "cap_drop"},
		{"ignored_ulimits", len(svc.Ulimits) > 0, "ulimits"},
		{"ignored_limits", svc.MemLimit != 0 || svc.CPUS != 0 || svc.ShmSize != 0 || svc.PidsLimit != 0, "memory/cpu/shm/pids limits"},
		{"ignored_read_only", svc.ReadOnly, "read_only"},
		{"ignored_tmpfs", len(svc.Tmpfs) > 0, "tmpfs"},
		{"ignored_stop", svc.StopGracePeriod != nil || svc.StopSignal != "", "stop_grace_period / stop_signal"},
		{"ignored_init", svc.Init != nil || svc.Tty || svc.StdinOpen, "init / tty / stdin_open"},
		{"ignored_logging", svc.Logging != nil || svc.LogDriver != "", "logging"},
		{"ignored_dns", len(svc.DNS) > 0 || len(svc.DNSOpts) > 0 || len(svc.DNSSearch) > 0 || len(svc.ExtraHosts) > 0 || svc.Hostname != "" || svc.DomainName != "" || svc.MacAddress != "", "dns / extra_hosts / hostname / mac_address"},
		{"ignored_container_name", svc.ContainerName != "", "container_name"},
		{"ignored_platform", svc.Platform != "", "platform"},
		{"ignored_deploy", svc.Deploy != nil && (svc.Deploy.Resources.Limits != nil || svc.Deploy.Resources.Reservations != nil || len(svc.Deploy.Placement.Constraints) > 0), "deploy.resources / deploy.placement"},
		{"ignored_external_links", len(svc.ExternalLinks) > 0, "external_links"},
	} {
		if w.set {
			warnf(w.code, "%s is ignored", w.msg)
		}
	}
	if svc.Deploy != nil && svc.Deploy.Mode == "global" {
		errf("deploy_global", "deploy.mode: global is not supported")
	}
	cs.User = svc.User
	cs.WorkingDir = svc.WorkingDir
	cs.Entrypoint = append([]string(nil), svc.Entrypoint...)
	cs.Command = append([]string(nil), svc.Command...)
	sp.Entrypoint, sp.Command = cs.Entrypoint, cs.Command

	// ---- networks ----
	aliasSet := map[string]bool{}
	for netName, nc := range svc.Networks {
		def := proj.Networks[netName]
		if bool(def.External) {
			warnf("external_network", "external network %q is ignored (aven's own proxy routes traffic; expose a domain instead of traefik labels)", netName)
			continue
		}
		customNets[netName] = true
		if nc != nil {
			for _, a := range nc.Aliases {
				aliasSet[a] = true
			}
			if nc.Ipv4Address != "" || nc.Ipv6Address != "" {
				warnf("static_ip", "static IP address on network %q is ignored; use the service name", netName)
			}
		}
	}
	for _, l := range svc.Links {
		parts := strings.SplitN(l, ":", 2)
		if len(parts) == 2 {
			aliasSet[parts[1]] = true
		}
	}
	for a := range aliasSet {
		cs.Aliases = append(cs.Aliases, a)
	}
	sort.Strings(cs.Aliases)

	// ---- labels ----
	hosts, tport := traefikHints(svc.Labels)
	if len(svc.Labels) > 0 {
		cs.Labels = map[string]string{}
		dropped := false
		for k, v := range svc.Labels {
			if strings.HasPrefix(k, "traefik.") || strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, "dokploy.") {
				dropped = true
				continue
			}
			cs.Labels[k] = v
		}
		if dropped {
			warnf("traefik_labels", "traefik/compose labels are not carried over; domain hints are offered as domain candidates instead")
		}
		if len(cs.Labels) == 0 {
			cs.Labels = nil
		}
	}

	// ---- environment ----
	vars := map[string]string{}
	for _, ef := range svc.EnvFiles {
		content, ok := readEnvFile(in, ef.Path)
		if !ok {
			// 없으면 경고하고 계속한다: 변수는 사용자가 나중에 넣을 수 있다.
			warnf("env_file_missing", "env_file %q was not provided; its variables are not imported (paste its content in the environment field)", relEnvName(in, ef.Path))
			continue
		}
		m, err := dotenv.ParseWithLookup(strings.NewReader(content), func(k string) (string, bool) { v, ok := in.Vars[k]; return v, ok })
		if err != nil {
			errf("env_file_invalid", "env_file %q: %v", relEnvName(in, ef.Path), err)
			continue
		}
		for k, v := range m {
			vars[k] = v
		}
	}
	for k, v := range svc.Environment {
		if v == nil {
			if hv, ok := in.Vars[k]; ok {
				vars[k] = hv
			} else {
				warnf("env_unset", "environment variable %s has no value (pass-through from the host is not available); it is not set", k)
			}
			continue
		}
		vars[k] = *v
	}
	for k, v := range vars {
		if strings.Contains(v, "${{") {
			warnf("env_ref_syntax", "variable %s contains ${{ }}, which aven treats as a variable reference", k)
		}
		sp.EnvKeys = append(sp.EnvKeys, k)
	}
	sort.Strings(sp.EnvKeys)
	sp.Variables = vars
	cs.EnvKeys = sp.EnvKeys

	// ---- ports ----
	httpPorts, tcpPorts, anyPorts := []int{}, []int{}, []int{}
	seenPort := map[int]bool{}
	addPort := func(p int, src string) {
		if p == 0 || seenPort[p] {
			return
		}
		seenPort[p] = true
		anyPorts = append(anyPorts, p)
		if knownTCPPorts[p] {
			tcpPorts = append(tcpPorts, p)
			return
		}
		httpPorts = append(httpPorts, p)
		sp.DomainCandidates = append(sp.DomainCandidates, DomainCandidate{Port: p, Source: src})
	}
	for _, p := range svc.Ports {
		if p.Protocol == "udp" {
			warnf("udp_port", "udp port %d is ignored", p.Target)
			continue
		}
		addPort(int(p.Target), "ports")
	}
	for _, e := range svc.Expose {
		addPort(portOf(e), "expose")
	}
	if tport > 0 && !seenPort[tport] {
		if knownTCPPorts[tport] {
			addPort(tport, "traefik")
		} else {
			seenPort[tport] = true
			anyPorts = append(anyPorts, tport)
			httpPorts = append(httpPorts, tport)
			sp.DomainCandidates = append(sp.DomainCandidates, DomainCandidate{Port: tport, Source: "traefik"})
		}
	}
	if len(hosts) > 0 {
		hp := tport
		if hp == 0 && len(httpPorts) > 0 {
			hp = httpPorts[0]
		}
		var kept []DomainCandidate
		for _, c := range sp.DomainCandidates {
			if c.Port != hp {
				kept = append(kept, c)
			}
		}
		for _, h := range hosts {
			kept = append(kept, DomainCandidate{Port: hp, Host: h, Source: "traefik"})
		}
		sp.DomainCandidates = kept
	}
	sp.TCPCandidates = tcpPorts
	if len(svc.Ports) > 0 {
		warnf("published_ports", "published host ports are not opened; the service is reachable inside the environment. Expose a domain (HTTP) or a TCP proxy for public access")
	}

	// ---- healthcheck ----
	hcPort := 0
	hasHTTP := false
	switch hc := svc.HealthCheck; {
	case hc != nil && (hc.Disable || (len(hc.Test) == 1 && hc.Test[0] == "NONE")):
		spec.HealthcheckType = "none"
	case hc != nil && len(hc.Test) > 0:
		kind, port, p := parseHealthTest(hc.Test)
		switch kind {
		case "http":
			spec.HealthcheckType, spec.HealthcheckPath, hcPort, hasHTTP = "http", p, port, true
		case "tcp":
			spec.HealthcheckType, hcPort = "tcp", port
		default:
			spec.HealthcheckType = "tcp"
			warnf("healthcheck_command", "healthcheck command %q cannot run inside aven; replaced by a TCP check on the service port", strings.Join(hc.Test, " "))
		}
		d := 0
		if hc.StartPeriod != nil {
			d += int(*hc.StartPeriod) / 1e9
		}
		iv, rt := 30, 3
		if hc.Interval != nil {
			iv = int(*hc.Interval) / 1e9
		}
		if hc.Retries != nil {
			rt = int(*hc.Retries)
		}
		d += iv * rt
		if d < 60 {
			d = 60
		}
		if d > 600 {
			d = 600
		}
		spec.HealthcheckTimeoutS = d
	default:
		spec.HealthcheckType = "tcp"
	}
	// ---- port ----
	switch {
	case hcPort > 0:
		spec.Port = &hcPort
	case tport > 0:
		p := tport
		spec.Port = &p
	case len(httpPorts) > 0:
		p := httpPorts[0]
		spec.Port = &p
	case len(anyPorts) > 0:
		p := anyPorts[0]
		spec.Port = &p
	}
	if spec.HealthcheckType == "tcp" && spec.Port == nil {
		// 포트를 알 수 없으면 이미지의 EXPOSE에 맡길 수 없는 경우를 대비해 확인을 건너뛴다.
		if svc.HealthCheck != nil && len(svc.HealthCheck.Test) > 0 {
			warnf("healthcheck_no_port", "no port is known for this service; the healthcheck is skipped (the container only has to stay running)")
		}
		spec.HealthcheckType = "none"
	}
	_ = hasHTTP

	// ---- restart / replicas ----
	restart := svc.Restart
	maxRetries := 0
	if restart == "" && svc.Deploy != nil && svc.Deploy.RestartPolicy != nil {
		switch svc.Deploy.RestartPolicy.Condition {
		case "any":
			restart = "always"
		case "on-failure":
			restart = "on-failure"
		case "none":
			restart = "no"
		}
		if svc.Deploy.RestartPolicy.MaxAttempts != nil {
			maxRetries = int(*svc.Deploy.RestartPolicy.MaxAttempts)
		}
	}
	switch {
	case restart == "always" || restart == "unless-stopped":
		spec.RestartPolicy = "always"
	case restart == "no":
		spec.RestartPolicy = "never"
	case strings.HasPrefix(restart, "on-failure"):
		spec.RestartPolicy = "on_failure"
		if i := strings.IndexByte(restart, ':'); i >= 0 {
			maxRetries, _ = strconv.Atoi(restart[i+1:])
		}
	default:
		spec.RestartPolicy = "never" // compose 기본값은 no
	}
	if maxRetries > 0 {
		spec.RestartMaxRetries = maxRetries
	}
	spec.Replicas = svc.GetScale()
	if spec.Replicas < 1 {
		spec.Replicas = 1
	}
	if spec.Replicas > 10 {
		errf("replicas", "replicas %d exceeds aven's limit of 10", spec.Replicas)
		spec.Replicas = 10
	}
	if spec.Replicas > 1 && svc.ContainerName != "" {
		warnf("replicas_container_name", "container_name is ignored with multiple replicas")
	}

	// ---- depends_on ----
	var depNames []string
	for d := range svc.DependsOn {
		depNames = append(depNames, d)
	}
	sort.Strings(depNames)
	for _, d := range depNames {
		cfg := svc.DependsOn[d]
		if _, ok := proj.Services[d]; !ok {
			if !cfg.Required {
				continue
			}
			errf("depends_on_missing", "depends_on %q is not an active service (profile-disabled or missing)", d)
			continue
		}
		cond := "started"
		switch cfg.Condition {
		case "service_healthy":
			cond = "healthy"
		case "service_completed_successfully":
			cond = "completed"
			warnf("depends_on_completed", "depends_on %s: service_completed_successfully is treated as service_started (one-shot jobs are not supported)", d)
		}
		sp.DependsOn = append(sp.DependsOn, store.Dependency{Service: d, Condition: cond})
	}
	cs.DependsOn = sp.DependsOn

	// ---- volumes ----
	for _, v := range svc.Volumes {
		switch v.Type {
		case "volume":
			conv := ""
			vname := ""
			if v.Source != "" {
				conv = v.Source
				def := proj.Volumes[v.Source]
				if bool(def.External) {
					errf("external_volume", "volume %q is external; aven cannot attach pre-existing docker volumes. Create a new aven volume and restore data into it", v.Source)
					continue
				}
				if def.Driver != "" && def.Driver != "local" {
					errf("volume_driver", "volume %q uses driver %q; only the local driver is supported", v.Source, def.Driver)
					continue
				}
				if len(def.DriverOpts) > 0 {
					errf("volume_driver_opts", "volume %q has driver_opts (e.g. NFS/bind-backed volumes), which are not supported", v.Source)
					continue
				}
				vname = KeelName(v.Source)
			} else {
				vname = KeelName(strings.Trim(strings.ReplaceAll(v.Target, "/", "-"), "-"))
			}
			if v.ReadOnly {
				warnf("volume_read_only", "read-only flag on volume mount %s is ignored (mounted read-write)", v.Target)
			}
			if !validMount(v.Target) {
				errf("mount_path", "invalid volume mount path %q", v.Target)
				continue
			}
			sp.Volumes = append(sp.Volumes, VolumePlan{Name: vname, MountPath: v.Target, ReadOnly: v.ReadOnly, Compose: conv})
		case "bind":
			src := v.Source
			if underDir(src, "/keel-compose-nowhere") || in.RepoDir != "" && underDir(src, in.RepoDir) || in.ComposeDir != "" && underDir(src, in.ComposeDir) {
				errf("relative_bind_mount", "bind mount %s:%s uses a path relative to the compose file; aven cannot ship repository files into containers. Bake the files into an image or use a named volume", v.Source, v.Target)
				continue
			}
			if !in.AllowBindMounts {
				errf("bind_mount", "bind mount %s:%s is not allowed: host paths break multi-node scheduling. Use a named volume, or pass allow_bind_mounts to pin the stack to this node", v.Source, v.Target)
				continue
			}
			if !validMount(v.Target) {
				errf("mount_path", "invalid bind mount target %q", v.Target)
				continue
			}
			if !filepath.IsAbs(src) || strings.ContainsAny(src, ",:\n") {
				errf("bind_source", "invalid bind mount source %q", src)
				continue
			}
			cs.BindMounts = append(cs.BindMounts, store.BindMount{Source: src, Target: v.Target, ReadOnly: v.ReadOnly})
			warnf("bind_mount_allowed", "bind mount %s:%s pins this service to the node that owns the path (local node only)", src, v.Target)
		case "tmpfs":
			warnf("tmpfs_ignored", "tmpfs mount at %s is ignored", v.Target)
		default:
			errf("volume_type", "volume type %q (%s) is not supported", v.Type, v.Target)
		}
	}
	sp.BindMounts = cs.BindMounts
	// 볼륨 이름 중복(같은 서비스에서 다른 경로)은 접미사로 구분한다.
	nameCount := map[string]int{}
	for i := range sp.Volumes {
		n := sp.Volumes[i].Name
		nameCount[n]++
		if nameCount[n] > 1 {
			sp.Volumes[i].Name = fmt.Sprintf("%s-%d", n, nameCount[n])
		}
	}
	sp.Port = spec.Port
	sp.HealthcheckType, sp.HealthcheckPath, sp.HealthcheckTimeoutS = spec.HealthcheckType, spec.HealthcheckPath, spec.HealthcheckTimeoutS
	sp.RestartPolicy, sp.RestartMaxRetries, sp.Replicas = spec.RestartPolicy, spec.RestartMaxRetries, spec.Replicas
	return sp
}

var imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@+-]{0,254}$`)

// nowhere는 붙여 넣은 YAML의 가상 작업 디렉터리다. 상대 경로 bind mount를 알아보는 데 쓴다.
const nowhere = "/keel-compose-nowhere/stack"

func underDir(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimRight(dir, "/")+"/")
}

func validMount(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "/" && !strings.ContainsAny(p, ",:\n")
}

func relEnvName(in Input, p string) string {
	if in.ComposeDir != "" {
		if r, err := filepath.Rel(in.ComposeDir, p); err == nil {
			return filepath.ToSlash(r)
		}
	}
	return filepath.Base(p)
}

// readEnvFile은 env_file 내용을 찾는다: 직접 준 EnvFiles, 아니면 저장소 안의 파일.
func readEnvFile(in Input, p string) (string, bool) {
	keys := []string{relEnvName(in, p), filepath.Base(p), p}
	for _, k := range keys {
		if c, ok := in.EnvFiles[k]; ok {
			return c, true
		}
	}
	if in.RepoDir == "" {
		return "", false
	}
	abs := filepath.Clean(p)
	if rel, err := filepath.Rel(in.RepoDir, abs); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ---------------- healthcheck / traefik parsing ----------------

var (
	urlRe      = regexp.MustCompile(`https?://(?:[^/\s:'"]+)(?::(\d+))?(/[^\s'"]*)?`)
	hostPortRe = regexp.MustCompile(`(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d+)(/[^\s'"]*)?`)
	pFlagRe    = regexp.MustCompile(`(?:^|\s)-p\s*(\d+)`)
	ncRe       = regexp.MustCompile(`\bnc\b.*?\s(\d{2,5})\b`)
	devTCPRe   = regexp.MustCompile(`/dev/tcp/[^/\s]+/(\d+)`)
)

// parseHealthTest는 compose healthcheck.test에서 aven이 할 수 있는 확인(http/tcp)을 뽑는다.
func parseHealthTest(test []string) (kind string, port int, path string) {
	cmd := strings.Join(test, " ")
	if len(test) > 0 && (test[0] == "CMD" || test[0] == "CMD-SHELL") {
		cmd = strings.Join(test[1:], " ")
	}
	if m := urlRe.FindStringSubmatch(cmd); m != nil {
		port = 80
		if strings.HasPrefix(m[0], "https") {
			port = 443
		}
		if m[1] != "" {
			port, _ = strconv.Atoi(m[1])
		}
		p := m[2]
		if p == "" {
			p = "/"
		}
		return "http", port, p
	}
	if m := hostPortRe.FindStringSubmatch(cmd); m != nil && (strings.Contains(cmd, "curl") || strings.Contains(cmd, "wget")) {
		port, _ = strconv.Atoi(m[1])
		p := m[2]
		if p == "" {
			p = "/"
		}
		return "http", port, p
	}
	if m := devTCPRe.FindStringSubmatch(cmd); m != nil {
		port, _ = strconv.Atoi(m[1])
		return "tcp", port, ""
	}
	flagPort := func(def int) int {
		if m := pFlagRe.FindStringSubmatch(cmd); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n
			}
		}
		return def
	}
	switch {
	case strings.Contains(cmd, "pg_isready"):
		return "tcp", flagPort(5432), ""
	case strings.Contains(cmd, "redis-cli") || strings.Contains(cmd, "valkey-cli"):
		return "tcp", flagPort(6379), ""
	case strings.Contains(cmd, "mysqladmin") || strings.Contains(cmd, "mariadb-admin") || strings.Contains(cmd, "healthcheck.sh") && strings.Contains(cmd, "innodb"):
		return "tcp", 3306, ""
	case strings.Contains(cmd, "mongosh") || strings.Contains(cmd, "mongo "):
		return "tcp", 27017, ""
	case strings.Contains(cmd, "rabbitmq-diagnostics"):
		return "tcp", 5672, ""
	}
	if m := ncRe.FindStringSubmatch(cmd); m != nil {
		port, _ = strconv.Atoi(m[1])
		return "tcp", port, ""
	}
	return "", 0, ""
}

var (
	hostRuleRe = regexp.MustCompile("Host\\(([^)]*)\\)")
	btRe       = regexp.MustCompile("`([^`]+)`")
)

// traefikHints는 traefik 라벨에서 호스트 이름과 서비스 포트를 뽑는다.
func traefikHints(labels types.Labels) (hosts []string, port int) {
	seen := map[string]bool{}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := labels[k]
		if strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".rule") {
			for _, m := range hostRuleRe.FindAllStringSubmatch(v, -1) {
				for _, h := range btRe.FindAllStringSubmatch(m[1], -1) {
					host := strings.ToLower(h[1])
					if !seen[host] && !strings.ContainsAny(host, "{}*") {
						seen[host] = true
						hosts = append(hosts, host)
					}
				}
			}
		}
		if port == 0 && strings.HasPrefix(k, "traefik.http.services.") && strings.HasSuffix(k, ".loadbalancer.server.port") {
			port = portOf(v)
		}
	}
	return hosts, port
}

// ---------------- apply helpers ----------------

// ApplyTo는 계획한 설정을 인스턴스에 쓴다. 사용자가 따로 정한 값(도메인, TCP proxy, 노드, 캔버스, compose가 만들지 않은 변수)은 건드리지 않는다.
// prev는 이전에 가져온 compose 설정(없으면 nil)으로, compose가 넣었던 변수 중 사라진 것을 지우는 데 쓴다.
func (sp *ServicePlan) ApplyTo(in *store.Instance) {
	prev := in.Spec.Compose
	// 소스/실행 설정
	s := &in.Spec
	s.RepoURL, s.Image = sp.Spec.RepoURL, sp.Spec.Image
	if sp.SourceType == "repo" {
		s.Branch, s.RootDir, s.Builder, s.DockerfilePath = sp.Spec.Branch, sp.Spec.RootDir, sp.Spec.Builder, sp.Spec.DockerfilePath
	}
	s.Port = sp.Spec.Port
	s.HealthcheckType, s.HealthcheckPath, s.HealthcheckTimeoutS = sp.Spec.HealthcheckType, sp.Spec.HealthcheckPath, sp.Spec.HealthcheckTimeoutS
	s.RestartPolicy, s.RestartMaxRetries, s.Replicas = sp.Spec.RestartPolicy, sp.Spec.RestartMaxRetries, sp.Spec.Replicas
	cp := *sp.Spec.Compose
	s.Compose = &cp
	// 변수: 이전에 compose가 넣은 키는 새 값으로 바꾸거나 지우고, 사용자가 만든 키는 둔다.
	if in.Variables == nil {
		in.Variables = map[string]string{}
	}
	if prev != nil {
		for _, k := range prev.EnvKeys {
			if _, still := sp.Variables[k]; !still {
				delete(in.Variables, k)
			}
		}
	}
	for k, v := range sp.Variables {
		in.Variables[k] = v
	}
}

// Sources는 변경 비교용으로 계획의 소스를 문자열로 요약한다.
func (sp *ServicePlan) Sources() string {
	if sp.Build != nil {
		return "build:" + sp.Build.Context + ":" + sp.Build.Dockerfile
	}
	return "image:" + sp.Image
}
