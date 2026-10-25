package dokploy

import (
	"encoding/json"
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/dotenv"
	"github.com/turin-dev/aven/internal/iac"
	yaml "go.yaml.in/yaml/v4"
)

// 서비스/스택 상태.
const (
	Ready         = "ready"          // 바로 가져올 수 있다
	NeedsDecision = "needs-decision" // 사용자가 정해야 한다(플래그로 해결)
	Blocked       = "blocked"        // aven로 옮길 수 없다(이유 참고)
	Skip          = "skip"           // 가져오지 않는다
)

// Partial은 import가 IaC 소유권을 기록하는 이름이다. 손으로 만든 서비스와 섞이지 않는다.
const Partial = "dokploy"

// Options는 plan의 사용자 결정이다.
type Options struct {
	// NodeMap은 Dokploy 서버 이름 → aven 노드 이름이다(없으면 같은 이름).
	NodeMap map[string]string
	// LocalAddrs는 Dokploy 메인 서버(로컬 노드)의 주소다. 이 주소의 외부 포트를 가리키는 env를 참조로 바꾼다.
	LocalAddrs []string
	// Skip은 가져오지 않을 "project/name"(Dokploy 이름, 대소문자 무시)이다.
	Skip map[string]bool
	// Move는 "project/name" → 대상 Dokploy 프로젝트 이름이다(다른 프로젝트의 DB를 쓰는 서비스용).
	Move map[string]string
	// KeepStandby는 main의 대기 복제본(status idle, 같은 이름 + "-<server>" 형제가 있음)도 가져온다.
	KeepStandby bool
	// KeepNames는 대기 복제본을 건너뛸 때 형제(`api-remote`)를 원래 이름(`api`)으로 바꾸지 않는다.
	KeepNames bool
	// GitHubSSH면 git@github.com 저장소를 SSH + 서비스별 deploy key로 둔다. 기본은 https로 바꿔
	// aven의 GitHub 연결(OAuth) 토큰으로 클론한다(저장소마다 키를 등록하지 않아도 된다).
	GitHubSSH bool
}

type Plan struct {
	DokployVersion string         `json:"dokploy_version,omitempty"`
	Projects       []*ProjectPlan `json:"projects"`
	Warnings       []string       `json:"warnings,omitempty"`
}

type ProjectPlan struct {
	Name        string            `json:"name"`    // aven 프로젝트 이름
	Sources     []string          `json:"sources"` // Dokploy 프로젝트 이름
	Environment string            `json:"environment"`
	Shared      map[string]string `json:"-"`
	SharedKeys  []string          `json:"shared_variables,omitempty"`
	Services    []*ServicePlan    `json:"services"`
	Stacks      []*StackPlan      `json:"stacks,omitempty"`
	Registries  []Registry        `json:"registries,omitempty"`
}

type ServicePlan struct {
	Source    string       `json:"source"` // Dokploy "project/name"
	DokployID string       `json:"dokploy_id"`
	AppName   string       `json:"app_name"`
	Name      string       `json:"name"`
	Kind      string       `json:"kind"` // app | database
	Status    string       `json:"status"`
	Reasons   []string     `json:"reasons,omitempty"`
	Warnings  []string     `json:"warnings,omitempty"`
	Node      string       `json:"node,omitempty"` // aven 노드 이름, 비면 로컬
	Service   iac.Service  `json:"-"`
	Settings  []string     `json:"settings,omitempty"`
	Variables []VarPlan    `json:"variables,omitempty"`
	Volumes   []VolumePlan `json:"volumes,omitempty"`
	Domains   []string     `json:"domains,omitempty"`
	DeployKey bool         `json:"deploy_key,omitempty"` // SSH로 클론: aven deploy key를 만들어 저장소에 등록해야 한다
	Upload    bool         `json:"upload,omitempty"`     // zip 업로드 소스: aven up으로 올려야 한다
	Data      string       `json:"data,omitempty"`       // 옮겨야 할 데이터(사람이 하는 단계)
	DependsOn []string     `json:"depends_on,omitempty"` // 같은 프로젝트에서 참조하는 서비스

	pending []domainEntry
}

// VarPlan은 변수 이름과 변환 종류다. 값은 담지 않는다.
type VarPlan struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // literal | reference | rewritten | build-arg
	To   string `json:"to,omitempty"`
}

type VolumePlan struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	Source    string `json:"source"` // Dokploy docker 볼륨 이름(데이터 복사 원본)
}

type StackDomain struct {
	Host       string `json:"host"`
	TargetPort int    `json:"target_port,omitempty"`
	TLSOff     bool   `json:"tls_off,omitempty"`
}

type StackPlan struct {
	Source          string                   `json:"source"`
	DokployID       string                   `json:"dokploy_id"`
	AppName         string                   `json:"app_name"`
	Name            string                   `json:"name"`
	Status          string                   `json:"status"`
	Reasons         []string                 `json:"reasons,omitempty"`
	Warnings        []string                 `json:"warnings,omitempty"`
	Node            string                   `json:"node,omitempty"`
	Domains         map[string][]StackDomain `json:"domains,omitempty"`
	AllowBindMounts bool                     `json:"allow_bind_mounts,omitempty"`
	EnvKeys         []string                 `json:"env_keys,omitempty"`
	Volumes         []string                 `json:"volumes,omitempty"`
	YAML            string                   `json:"-"`
	Env             string                   `json:"-"`
	Files           []Mount                  `json:"-"`
}

type Registry struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"-"`
}

// ---- 내부 인덱스 ----

type target struct {
	project, env string // aven
	name         string // aven 서비스 이름
	kind         string // app | database | stack
	node         string // aven 노드 이름, 비면 로컬
	db           *Database
	skipped      bool
}

type domainEntry struct {
	svc   string
	d     Domain
	stack bool
}

type translator struct {
	s       *Snapshot
	o       Options
	servers map[string]*Server
	plan    *Plan
	byKey   map[string]*ProjectPlan // project|env
	apps    map[string]*target      // Dokploy appName → aven 서비스
	addrs   map[string]*target      // "ip:port" → DB (외부 포트)
	stacks  map[string]*target      // compose appName → 스택

	pendingStack []stackDomain
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// Slug는 aven 이름 규칙([a-z0-9-])으로 바꾼다.
func Slug(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if s == "" {
		s = "service"
	}
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	return s
}

func key(project, name string) string { return strings.ToLower(project + "/" + name) }

func (t *translator) nodeOf(serverID *string) string {
	if serverID == nil || *serverID == "" {
		return ""
	}
	sv := t.servers[*serverID]
	if sv == nil {
		return *serverID
	}
	if n, ok := t.o.NodeMap[sv.Name]; ok {
		return n
	}
	return sv.Name
}

func (t *translator) serverName(serverID *string) string {
	if serverID == nil {
		return ""
	}
	if sv := t.servers[*serverID]; sv != nil {
		return sv.Name
	}
	return ""
}

func (t *translator) project(name, env string) *ProjectPlan {
	k := name + "|" + env
	if p := t.byKey[k]; p != nil {
		return p
	}
	p := &ProjectPlan{Name: name, Environment: env, Shared: map[string]string{}}
	t.byKey[k] = p
	t.plan.Projects = append(t.plan.Projects, p)
	return p
}

// Translate는 Dokploy 스냅샷을 aven 계획으로 바꾼다. 네트워크를 쓰지 않는다(aven 쪽 검증은 CLI가 한다).
func Translate(s *Snapshot, o Options) *Plan {
	t := &translator{s: s, o: o, servers: map[string]*Server{}, plan: &Plan{DokployVersion: s.Version},
		byKey: map[string]*ProjectPlan{}, apps: map[string]*target{}, addrs: map[string]*target{}, stacks: map[string]*target{}}
	for _, sv := range s.Servers {
		t.servers[sv.ID] = sv
	}
	skip := map[string]bool{}
	for k, v := range o.Skip {
		skip[strings.ToLower(k)] = v
	}
	move := map[string]string{}
	for k, v := range o.Move {
		move[strings.ToLower(k)] = v
	}

	type appItem struct {
		p    *Project
		e    *Environment
		a    *Application
		sp   *ServicePlan
		pp   *ProjectPlan
		name string
	}
	var items []*appItem
	var dbItems []*ServicePlan
	dbOf := map[*ServicePlan]*Database{}
	used := map[string]map[string]bool{} // aven project|env → 이름
	claim := func(pp *ProjectPlan, want string, sp *ServicePlan) string {
		k := pp.Name + "|" + pp.Environment
		if used[k] == nil {
			used[k] = map[string]bool{}
		}
		n := want
		for i := 2; used[k][n]; i++ {
			n = fmt.Sprintf("%s-%d", want, i)
		}
		if n != want {
			sp.Warnings = append(sp.Warnings, fmt.Sprintf("name %q is taken in project %s, using %q", want, pp.Name, n))
		}
		used[k][n] = true
		return n
	}

	// 1) 이름, 건너뛰기, 대기 복제본, 프로젝트 이동을 먼저 정한다(참조 변환에 필요).
	for _, p := range s.Projects {
		for _, e := range p.Environments {
			envName := Slug(e.Name)
			base := t.project(Slug(p.Name), envName)
			addSource(base, p.Name)
			for k, v := range parseEnv(p.Env) {
				base.Shared[k] = v
			}
			for k, v := range parseEnv(e.Env) {
				base.Shared[k] = v
			}
			// DB
			for _, db := range e.Databases {
				sp := &ServicePlan{Source: p.Name + "/" + db.Name, DokployID: db.ID, AppName: db.AppName, Kind: "database", Status: Ready, Node: t.nodeOf(db.ServerID)}
				pp := base
				if to, ok := move[key(p.Name, db.Name)]; ok {
					pp = t.project(Slug(to), envName)
					addSource(pp, to)
				}
				sp.Name = claim(pp, Slug(db.Name), sp)
				if skip[key(p.Name, db.Name)] {
					sp.Status, sp.Reasons = Skip, []string{"skipped by --skip"}
				}
				pp.Services = append(pp.Services, sp)
				dbItems = append(dbItems, sp)
				dbOf[sp] = db
				tg := &target{project: pp.Name, env: envName, name: sp.Name, kind: "database", node: sp.Node, db: db, skipped: sp.Status == Skip}
				t.apps[db.AppName] = tg
				if db.ExternalPort != nil {
					for _, ip := range t.addrsOf(db.ServerID) {
						t.addrs[net.JoinHostPort(ip, strconv.Itoa(*db.ExternalPort))] = tg
					}
				}
			}
			// 앱: 대기 복제본(main에서 idle, "<name>-<server>" 형제가 있음)
			byName := map[string]*Application{}
			for _, a := range e.Applications {
				byName[a.Name] = a
			}
			standby := map[*Application]*Application{} // 대기 → 형제
			for _, a := range e.Applications {
				if a.ServerID != nil || a.Status != "idle" {
					continue
				}
				for _, b := range e.Applications {
					if b != a && b.ServerID != nil && b.Name == a.Name+"-"+t.serverName(b.ServerID) {
						standby[a] = b
					}
				}
			}
			renamed := map[*Application]string{}
			if !o.KeepStandby && !o.KeepNames {
				for a, b := range standby {
					renamed[b] = a.Name
				}
			}
			for _, a := range e.Applications {
				sp := &ServicePlan{Source: p.Name + "/" + a.Name, DokployID: a.ID, AppName: a.AppName, Kind: "app", Status: Ready, Node: t.nodeOf(a.ServerID)}
				pp := base
				if to, ok := move[key(p.Name, a.Name)]; ok {
					pp = t.project(Slug(to), envName)
					addSource(pp, to)
				}
				want := a.Name
				if r, ok := renamed[a]; ok {
					want = r
					sp.Warnings = append(sp.Warnings, fmt.Sprintf("renamed to %q: the main standby copy is not imported", Slug(r)))
				}
				switch sib, isStandby := standby[a]; {
				case skip[key(p.Name, a.Name)]:
					sp.Status, sp.Reasons = Skip, []string{"skipped by --skip"}
				case isStandby && !o.KeepStandby:
					sp.Status, sp.Reasons = Skip, []string{fmt.Sprintf("standby copy (status idle) of %s on %s; pass --keep-standby to import it", sib.Name, t.serverName(sib.ServerID))}
				}
				if sp.Status == Skip {
					sp.Name = Slug(want) // 이름을 차지하지 않는다(형제가 원래 이름을 쓴다)
				} else {
					sp.Name = claim(pp, Slug(want), sp)
				}
				pp.Services = append(pp.Services, sp)
				items = append(items, &appItem{p: p, e: e, a: a, sp: sp, pp: pp})
				t.apps[a.AppName] = &target{project: pp.Name, env: envName, name: sp.Name, kind: "app", node: sp.Node, skipped: sp.Status == Skip}
			}
			for _, c := range e.Composes {
				t.stacks[c.AppName] = &target{project: base.Name, env: envName, name: Slug(c.Name), kind: "stack", skipped: skip[key(p.Name, c.Name)]}
			}
		}
	}

	// 2) DB 서비스
	for _, sp := range dbItems {
		t.database(sp, dbOf[sp])
	}
	// 3) 앱 서비스
	regs := map[*ProjectPlan]map[string]Registry{}
	for _, it := range items {
		if it.sp.Status == Skip {
			continue
		}
		if r := t.application(it.pp, it.sp, it.a); r != nil {
			if regs[it.pp] == nil {
				regs[it.pp] = map[string]Registry{}
			}
			if old, ok := regs[it.pp][r.Host]; ok && (old.Username != r.Username || old.Password != r.Password) {
				it.sp.Warnings = append(it.sp.Warnings, "another service uses different credentials for registry "+r.Host+"; aven keeps one per project (the first)")
			} else {
				regs[it.pp][r.Host] = *r
			}
		}
	}
	for pp, m := range regs {
		for _, r := range m {
			pp.Registries = append(pp.Registries, r)
		}
		sort.Slice(pp.Registries, func(i, j int) bool { return pp.Registries[i].Host < pp.Registries[j].Host })
	}
	// 4) compose
	for _, p := range s.Projects {
		for _, e := range p.Environments {
			pp := t.project(Slug(p.Name), Slug(e.Name))
			for _, c := range e.Composes {
				t.compose(pp, p, c, skip[key(p.Name, c.Name)])
			}
		}
	}
	// 5) 도메인(서비스와 스택을 다 본 뒤 호스트별로 합친다)
	t.domains()

	for _, pp := range t.plan.Projects {
		for k := range pp.Shared {
			pp.SharedKeys = append(pp.SharedKeys, k)
		}
		sort.Strings(pp.SharedKeys)
	}
	for _, pp := range t.plan.Projects {
		Propagate(pp)
		for _, sp := range pp.Services {
			sp.Reasons, sp.Warnings, sp.DependsOn = uniq(sp.Reasons), uniq(sp.Warnings), uniq(sp.DependsOn)
		}
		for _, st := range pp.Stacks {
			st.Reasons, st.Warnings = uniq(st.Reasons), uniq(st.Warnings)
		}
	}
	// 빈 프로젝트(이동으로 모두 빠진 경우)는 남겨 두되 정렬한다.
	sort.SliceStable(t.plan.Projects, func(i, j int) bool { return t.plan.Projects[i].Name < t.plan.Projects[j].Name })
	return t.plan
}

// Propagate는 가져오지 않는(ready가 아닌) 서비스를 참조하는 ready 서비스를 blocked로 바꾼다.
// aven 쪽 검증(노드 없음 등)으로 상태가 바뀐 뒤에도 다시 부른다.
func Propagate(pp *ProjectPlan) {
	by := map[string]*ServicePlan{}
	for _, sp := range pp.Services {
		if sp.Status != Skip || by[sp.Name] == nil {
			by[sp.Name] = sp
		}
	}
	for changed := true; changed; {
		changed = false
		for _, sp := range pp.Services {
			if sp.Status != Ready {
				continue
			}
			for _, d := range sp.DependsOn {
				dep := by[d]
				if dep == nil || dep == sp || dep.Status == Ready {
					continue
				}
				if dep.Status == Skip {
					continue // 경고는 rewrite가 이미 남겼다
				}
				sp.Status = Blocked
				sp.Reasons = append(sp.Reasons, fmt.Sprintf("depends on %s, which is %s", d, dep.Status))
				changed = true
				break
			}
		}
	}
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func addSource(pp *ProjectPlan, name string) {
	for _, s := range pp.Sources {
		if s == name {
			return
		}
	}
	pp.Sources = append(pp.Sources, name)
}

func (t *translator) addrsOf(serverID *string) []string {
	if serverID == nil {
		return append([]string{"127.0.0.1", "localhost"}, t.o.LocalAddrs...)
	}
	if sv := t.servers[*serverID]; sv != nil && sv.IPAddress != "" {
		return []string{sv.IPAddress}
	}
	return nil
}

func parseEnv(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return map[string]string{}
	}
	m, err := dotenv.Parse(s)
	if err != nil {
		// Dokploy는 값을 거의 손대지 않으므로 줄 단위로 다시 읽는다.
		m = map[string]string{}
		for _, l := range strings.Split(s, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if k, v, ok := strings.Cut(l, "="); ok {
				m[strings.TrimSpace(k)] = v
			}
		}
	}
	return m
}

// envNames는 env 이름을 정렬해 돌려준다.
func envNames(s string) []string {
	m := parseEnv(s)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func setSetting(sp *ServicePlan, k string, v any) {
	if sp.Service.Settings == nil {
		sp.Service.Settings = map[string]json.RawMessage{}
	}
	sp.Service.Settings[k] = raw(v)
}

// ---- DB ----

var dbVars = map[string]struct{ host, port, password, url string }{
	"postgres": {"PGHOST", "PGPORT", "POSTGRES_PASSWORD", "DATABASE_URL"},
	"redis":    {"REDISHOST", "REDISPORT", "REDIS_PASSWORD", "REDIS_URL"},
}

func (t *translator) database(sp *ServicePlan, db *Database) {
	if sp.Status == Skip {
		return
	}
	sp.Service = iac.Service{Name: sp.Name, SourceType: "database", DatabaseKind: db.Kind}
	def, ok := catalog.Databases[db.Kind]
	if !ok || (db.Kind != "postgres" && db.Kind != "redis") {
		sp.Status, sp.Reasons = Blocked, []string{db.Kind + " databases are not handled by the importer yet (none were in use when it was written)"}
		return
	}
	vars := def.Variables(db.Password)
	switch db.Kind {
	case "postgres":
		if db.DatabaseUser != "" {
			vars["POSTGRES_USER"] = db.DatabaseUser
		}
		if db.DatabaseName != "" {
			vars["POSTGRES_DB"] = db.DatabaseName
		}
	}
	for k, v := range parseEnv(db.Env) {
		vars[k] = v
		sp.Warnings = append(sp.Warnings, "extra env "+k+" copied from Dokploy")
	}
	m := map[string]iac.Variable{}
	for k, v := range vars {
		m[k] = iac.Variable{Value: v}
	}
	sp.Service.Variables = &m
	for _, k := range sortedKeys(vars) {
		kind := "literal"
		if strings.Contains(vars[k], "${{") {
			kind = "reference"
		}
		sp.Variables = append(sp.Variables, VarPlan{Name: k, Kind: kind})
	}
	if db.DockerImage != "" {
		// 데이터는 덤프/복원으로 옮기지만, 앱이 기대하는 메이저 버전을 그대로 둔다.
		setSetting(sp, "image", db.DockerImage)
		sp.Settings = append(sp.Settings, "image="+db.DockerImage)
	}
	if db.Command != "" || db.Args != nil {
		sp.Warnings = append(sp.Warnings, "custom command/args are not imported (aven starts the database with its own command)")
	}
	if db.ExternalPort != nil {
		sp.Warnings = append(sp.Warnings, fmt.Sprintf("Dokploy publishes it on host port %d; aven gives other services the private address, add a TCP proxy only if something outside aven needs it", *db.ExternalPort))
	}
	if len(db.Backups) > 0 {
		sp.Warnings = append(sp.Warnings, fmt.Sprintf("%d Dokploy backup schedule(s): set up aven database backups (db-backups/config) and an S3 destination", len(db.Backups)))
	}
	vol := "(no volume: nothing to copy)"
	for _, mt := range db.Mounts {
		if mt.Type == "volume" {
			vol = "volume " + mt.VolumeName
		}
	}
	switch db.Kind {
	case "postgres":
		sp.Data = fmt.Sprintf("pg_dump -Fc from %s (%s) → aven db-backups restore", db.AppName, vol)
	case "redis":
		if strings.HasPrefix(vol, "(") {
			sp.Data = "nothing (Dokploy keeps no redis data on disk)"
		} else {
			sp.Data = fmt.Sprintf("redis RDB from %s (%s), or start empty if it only holds cache/sessions", db.AppName, vol)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- 앱 ----

func (t *translator) application(pp *ProjectPlan, sp *ServicePlan, a *Application) *Registry {
	block := func(f string, args ...any) {
		sp.Status = Blocked
		sp.Reasons = append(sp.Reasons, fmt.Sprintf(f, args...))
	}
	warn := func(f string, args ...any) { sp.Warnings = append(sp.Warnings, fmt.Sprintf(f, args...)) }
	sp.Service = iac.Service{Name: sp.Name}
	var reg *Registry
	buildPath := strings.Trim(a.BuildPath, "/")
	repo := false
	switch a.SourceType {
	case "github":
		sp.Service.SourceType = "repo"
		setSetting(sp, "repo_url", "https://github.com/"+a.Owner+"/"+a.Repository)
		setSetting(sp, "branch", a.Branch)
		sp.Settings = append(sp.Settings, fmt.Sprintf("repo=github.com/%s/%s@%s", a.Owner, a.Repository, a.Branch))
		repo = true
	case "git":
		sp.Service.SourceType = "repo"
		repoURL := a.CustomGitURL
		if gh, ok := githubHTTPS(repoURL); ok && !t.o.GitHubSSH {
			repoURL = gh
			sp.Warnings = append(sp.Warnings, "SSH URL "+a.CustomGitURL+" changed to "+gh+": cloned with aven's GitHub connection (pass --github-ssh to keep SSH and a deploy key)")
		}
		setSetting(sp, "repo_url", repoURL)
		setSetting(sp, "branch", a.CustomGitBranch)
		buildPath = strings.Trim(a.CustomGitPath, "/")
		sp.Settings = append(sp.Settings, fmt.Sprintf("repo=%s@%s", repoURL, a.CustomGitBranch))
		if strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "ssh://") {
			sp.DeployKey = true
		}
		repo = true
	case "docker":
		sp.Service.SourceType = "image"
		setSetting(sp, "image", a.DockerImage)
		sp.Settings = append(sp.Settings, "image="+a.DockerImage)
		if a.Username != "" {
			host := a.RegistryURL
			if host == "" {
				host = "docker.io"
			}
			reg = &Registry{Host: host, Username: a.Username, Password: a.Password}
		}
	case "drop":
		// 저장소가 없다: 이미지 없는 빈 서비스로 만들고 aven up(업로드 빌드)으로 올린다.
		sp.Service.SourceType = "image"
		sp.Upload = true
		repo = true
		warn("source was a zip upload: deploy it with `aven up` from the code in /etc/dokploy/applications/%s/code", a.AppName)
	default:
		block("source type %q is not supported (GitHub, git, docker image and zip upload are)", a.SourceType)
	}
	if repo {
		switch a.BuildType {
		case "dockerfile":
			setSetting(sp, "builder", "dockerfile")
			ctx := strings.Trim(path.Clean("/"+a.DockerContext), "/")
			df := strings.TrimPrefix(path.Clean("/"+a.Dockerfile), "/")
			if a.Dockerfile == "" {
				df = "Dockerfile"
			}
			if ctx != "" {
				if !strings.HasPrefix(df, ctx+"/") {
					block("the Dockerfile %s is outside its build context %s (aven builds with root_dir as context)", df, ctx)
				}
				df = strings.TrimPrefix(df, ctx+"/")
			}
			root := strings.Trim(path.Join(buildPath, ctx), "/")
			if root != "" && root != "." {
				setSetting(sp, "root_dir", "/"+root)
				sp.Settings = append(sp.Settings, "root_dir=/"+root)
			}
			if df != "Dockerfile" {
				setSetting(sp, "dockerfile_path", df)
				sp.Settings = append(sp.Settings, "dockerfile_path="+df)
			}
			if a.DockerStage != "" {
				warn("Dockerfile build stage %q is not supported; aven builds the last stage", a.DockerStage)
			}
		case "railpack", "nixpacks":
			setSetting(sp, "builder", "railpack")
			sp.Settings = append(sp.Settings, "builder=railpack")
			if a.BuildType == "nixpacks" {
				warn("nixpacks build is replaced by railpack")
			}
			if buildPath != "" {
				setSetting(sp, "root_dir", "/"+buildPath)
			}
		default:
			block("build type %q is not supported (dockerfile and railpack are)", a.BuildType)
		}
		if !sp.Upload {
			setSetting(sp, "auto_deploy", a.AutoDeploy)
		}
		// Dokploy builds assigned to a separate build server are built on aven-server and sent to the remote node.
		if a.BuildServerID != nil && *a.BuildServerID != "" && sp.Node != "" {
			setSetting(sp, "build_node", "local")
			sp.Settings = append(sp.Settings, "build_node=local (Dokploy built it on "+t.serverName(a.BuildServerID)+")")
		}
		if len(a.WatchPaths) > 0 {
			setSetting(sp, "watch_paths", a.WatchPaths)
		}
	}
	replicas := a.Replicas
	if replicas < 1 {
		replicas = 1
	}
	setSetting(sp, "replicas", replicas)
	// Dokploy(Swarm)는 healthcheck 없이 always 재시작으로 돌리고 있었다. 같은 동작으로 시작한다.
	setSetting(sp, "healthcheck_type", "none")
	setSetting(sp, "restart_policy", "always")
	if mb := memMB(a.MemoryLimit); mb > 0 {
		setSetting(sp, "memory_limit_mb", mb)
		sp.Settings = append(sp.Settings, fmt.Sprintf("memory_limit_mb=%d", mb))
	}
	if cpu := cpuLimit(a.CPULimit); cpu > 0 {
		setSetting(sp, "cpu_limit", cpu)
		sp.Settings = append(sp.Settings, fmt.Sprintf("cpu_limit=%g", cpu))
	}
	if a.Command != "" {
		cmd := strings.TrimSpace(a.Command + " " + strings.Join(a.Args, " "))
		setSetting(sp, "start_command", cmd)
		sp.Settings = append(sp.Settings, "start_command (from Dokploy command/args)")
	} else if len(a.Args) > 0 {
		warn("args without a command are not imported (aven has a start command only)")
	}
	if len(a.Redirects) > 0 || len(a.Security) > 0 {
		warn("%d redirect(s) and %d basic auth rule(s) are not imported: add them to the aven domain", len(a.Redirects), len(a.Security))
	}
	if len(a.Ports) > 0 {
		warn("%d published port(s) are not imported: use a aven TCP proxy", len(a.Ports))
	}

	// 변수
	env := parseEnv(a.Env)
	vars := map[string]iac.Variable{}
	for _, k := range sortedKeys(env) {
		v, vp := t.rewrite(pp, sp, k, env[k])
		vars[k] = iac.Variable{Value: v}
		sp.Variables = append(sp.Variables, vp)
	}
	for _, k := range sortedKeys(parseEnv(a.BuildArgs)) {
		if _, ok := env[k]; ok {
			continue
		}
		v, vp := t.rewrite(pp, sp, k, parseEnv(a.BuildArgs)[k])
		vp.Kind = "build-arg"
		vars[k] = iac.Variable{Value: v}
		sp.Variables = append(sp.Variables, vp)
	}
	var secrets []string
	for _, k := range sortedKeys(parseEnv(a.BuildSecrets)) {
		secrets = append(secrets, k)
		if _, ok := vars[k]; !ok {
			vars[k] = iac.Variable{Value: parseEnv(a.BuildSecrets)[k]}
			sp.Variables = append(sp.Variables, VarPlan{Name: k, Kind: "literal", To: "build secret"})
		}
	}
	if len(secrets) > 0 {
		setSetting(sp, "build_secrets", secrets)
		sp.Settings = append(sp.Settings, "build_secrets="+strings.Join(secrets, ","))
	}
	sp.Service.Variables = &vars

	// 마운트
	for _, m := range a.Mounts {
		switch m.Type {
		case "volume":
			sp.Volumes = append(sp.Volumes, VolumePlan{Name: Slug(m.VolumeName), MountPath: m.MountPath, Source: m.VolumeName})
		case "bind":
			warn("bind mount %s → %s is not imported (aven services cannot mount host paths)", m.HostPath, m.MountPath)
		case "file":
			warn("file mount %s is not imported: bake it into the image or set it as a variable", m.MountPath)
		}
	}
	if len(sp.Volumes) > 1 {
		// aven 인스턴스는 볼륨을 여러 개 가질 수 있지만 KEEL_VOLUME_MOUNT_PATH는 첫 번째만 가리킨다.
		warn("%d volumes: KEEL_VOLUME_MOUNT_PATH points at the first one only", len(sp.Volumes))
	}
	if len(sp.Volumes) > 0 {
		var src []string
		for _, v := range sp.Volumes {
			src = append(src, v.Source)
		}
		sp.Data = "copy volume(s) " + strings.Join(src, ", ") + " (stop the Dokploy service first)"
	}
	for _, d := range a.Domains {
		if d.Enabled != nil && !*d.Enabled {
			continue
		}
		sp.pending = append(sp.pending, domainEntry{svc: sp.Name, d: d})
	}
	return reg
}

// githubHTTPS는 github.com SSH URL(git@github.com:o/r.git, ssh://git@github.com/o/r)을 https URL로 바꾼다.
func githubHTTPS(u string) (string, bool) {
	var rest string
	switch {
	case strings.HasPrefix(u, "git@github.com:"):
		rest = strings.TrimPrefix(u, "git@github.com:")
	case strings.HasPrefix(u, "ssh://git@github.com/"):
		rest = strings.TrimPrefix(u, "ssh://git@github.com/")
	default:
		return "", false
	}
	rest = strings.TrimSuffix(strings.Trim(rest, "/"), ".git")
	if strings.Count(rest, "/") != 1 {
		return "", false
	}
	return "https://github.com/" + rest, true
}

func memMB(v any) int {
	n := toFloat(v)
	if n <= 0 {
		return 0
	}
	mb := int(n / (1 << 20))
	if mb < 6 {
		mb = 6
	}
	return mb
}

// cpuLimit: Dokploy는 NanoCPUs(1e9 = 1 CPU)로 저장한다.
func cpuLimit(v any) float64 {
	n := toFloat(v)
	if n <= 0 {
		return 0
	}
	if n >= 1e6 {
		n /= 1e9
	}
	return n
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

// ---- 변수 값 변환 ----

var (
	urlRe        = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*)://(?:([^\s:/@'"]*)(?::([^\s@'"]*))?@)?([A-Za-z0-9._-]+|\[[0-9a-fA-F:]+\])(?::(\d+))?([^\s'"]*)`)
	dokployRefRe = regexp.MustCompile(`\$\{\{\s*(project|environment)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	hostTokRe    = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]*`)
	ipPortRe     = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3}):(\d{2,5})\b`)
)

func (t *translator) ref(svc, v string) string { return "${{" + svc + "." + v + "}}" }

// rewrite는 Dokploy env 값 하나를 aven 값으로 바꾼다. 다른 서비스의 호스트 이름(appName), DB의 외부 주소,
// Dokploy 공유 변수 참조를 aven 참조로 바꾼다. 바꾸지 못한 의존성은 경고로 남긴다.
func (t *translator) rewrite(pp *ProjectPlan, sp *ServicePlan, k, v string) (string, VarPlan) {
	vp := VarPlan{Name: k, Kind: "literal"}
	orig := v
	if strings.Contains(v, "${{") {
		vp.Kind = "reference"
		v = dokployRefRe.ReplaceAllString(v, "${{ shared.$2 }}")
	}
	cross := func(tg *target) bool {
		if tg.project != pp.Name || tg.env != pp.Environment {
			sp.Status = NeedsDecision
			sp.Reasons = append(sp.Reasons, fmt.Sprintf("%s refers to %s/%s in another aven project; aven references stay inside one environment. Pass --move %s=%s (Dokploy names) or edit the value after import", k, tg.project, tg.name, sp.Source, tg.project))
			return true
		}
		if tg.skipped {
			sp.Warnings = append(sp.Warnings, fmt.Sprintf("%s refers to %s, which is not imported", k, tg.name))
			return true
		}
		return false
	}
	// URL(DB 연결 문자열 등)
	v = urlRe.ReplaceAllStringFunc(v, func(m string) string {
		g := urlRe.FindStringSubmatch(m)
		scheme, user, pass, host, port, rest := g[1], g[2], g[3], g[4], g[5], g[6]
		tg := t.apps[host]
		viaAddr := false
		if tg == nil && port != "" {
			if tg = t.addrs[net.JoinHostPort(strings.Trim(host, "[]"), port)]; tg != nil {
				viaAddr = true
			}
		}
		if tg == nil {
			if st := t.stackFor(host); st != nil {
				if cross(st.tg) {
					return m
				}
				return strings.Replace(m, host, st.alias, 1)
			}
			return m
		}
		if cross(tg) {
			return m
		}
		sp.DependsOn = append(sp.DependsOn, tg.name)
		if tg.node != sp.Node {
			sp.Warnings = append(sp.Warnings, fmt.Sprintf("%s reaches %s on another node (%s): aven routes it over the mesh, so both nodes need a private address", k, tg.name, nodeLabel(tg.node)))
		}
		if tg.kind != "database" {
			return strings.Replace(m, "://"+userinfo(user, pass)+host, "://"+userinfo(user, pass)+tg.name, 1)
		}
		dv, ok := dbVars[tg.db.Kind]
		if !ok {
			return m
		}
		defPort := strconv.Itoa(catalog.Databases[tg.db.Kind].Port)
		if port == "" {
			port = defPort
		}
		if viaAddr {
			port = defPort // 외부 포트 대신 사설 주소의 기본 포트로 붙는다
		}
		// aven이 만드는 URL과 같은 모양이면 통째로 참조한다.
		if m == orig || strings.TrimSpace(orig) == m {
			switch tg.db.Kind {
			case "postgres":
				if (scheme == "postgres" || scheme == "postgresql") && user == tg.db.DatabaseUser && pass == tg.db.Password &&
					port == defPort && rest == "/"+tg.db.DatabaseName {
					return t.ref(tg.name, dv.url)
				}
			case "redis":
				if scheme == "redis" && (user == "" || user == "default") && pass == tg.db.Password && port == defPort && rest == "" {
					return t.ref(tg.name, dv.url)
				}
			}
		}
		out := scheme + "://"
		if user != "" || pass != "" {
			out += user
			if pass != "" {
				if pass == tg.db.Password {
					out += ":" + t.ref(tg.name, dv.password)
				} else {
					out += ":" + pass
					sp.Warnings = append(sp.Warnings, fmt.Sprintf("%s uses a password that is not %s's; check it after import", k, tg.name))
				}
			}
			out += "@"
		}
		out += t.ref(tg.name, dv.host) + ":" + t.ref(tg.name, dv.port) + rest
		return out
	})
	// 맨 호스트 이름(PGHOST=postgres-main-xxx 같은 값)
	v = hostTokRe.ReplaceAllStringFunc(v, func(tok string) string {
		if tg := t.apps[tok]; tg != nil {
			if cross(tg) {
				return tok
			}
			sp.DependsOn = append(sp.DependsOn, tg.name)
			if tg.kind == "database" {
				if dv, ok := dbVars[tg.db.Kind]; ok {
					return t.ref(tg.name, dv.host)
				}
			}
			return tg.name
		}
		if st := t.stackFor(tok); st != nil && !cross(st.tg) {
			return st.alias
		}
		return tok
	})
	// 남은 IP:port 주소
	for _, g := range ipPortRe.FindAllStringSubmatch(v, -1) {
		sp.Warnings = append(sp.Warnings, fmt.Sprintf("%s points at %s:%s, an address outside aven", k, g[1], g[2]))
	}
	if strings.Contains(v, "dokploy") || strings.HasPrefix(k, "DOKPLOY_") {
		sp.Warnings = append(sp.Warnings, fmt.Sprintf("%s refers to Dokploy itself; it stops working when Dokploy is removed (switch the code to the aven API)", k))
	}
	if v != orig {
		vp.Kind = "rewritten"
		if strings.Contains(v, "${{") {
			vp.To = maskLiterals(v)
		}
	}
	return v, vp
}

func nodeLabel(n string) string {
	if n == "" {
		return "local"
	}
	return n
}

func userinfo(user, pass string) string {
	switch {
	case user == "" && pass == "":
		return ""
	case pass == "":
		return user + "@"
	}
	return user + ":" + pass + "@"
}

// maskLiterals는 plan 출력용으로 참조가 아닌 비밀번호 부분을 가린다.
func maskLiterals(v string) string {
	return urlRe.ReplaceAllStringFunc(v, func(m string) string {
		g := urlRe.FindStringSubmatch(m)
		if g[3] != "" && !strings.Contains(g[3], "${{") {
			return strings.Replace(m, ":"+g[3]+"@", ":***@", 1)
		}
		return m
	})
}

type stackRef struct {
	tg    *target
	alias string
}

// stackFor는 Dokploy compose 컨테이너 이름(<appName>-<service>-<n>)을 compose 서비스 별칭으로 바꾼다.
func (t *translator) stackFor(host string) *stackRef {
	for app, tg := range t.stacks {
		for _, sep := range []string{"-", "_"} {
			p := app + sep
			if !strings.HasPrefix(host, p) {
				continue
			}
			rest := strings.TrimPrefix(host, p)
			if i := strings.LastIndex(rest, sep); i > 0 {
				if _, err := strconv.Atoi(rest[i+1:]); err == nil {
					rest = rest[:i]
				}
			}
			if rest != "" {
				return &stackRef{tg: tg, alias: rest}
			}
		}
	}
	return nil
}

// ---- compose ----

type composeFile struct {
	Services map[string]struct {
		NetworkMode string `yaml:"network_mode"`
		Privileged  bool   `yaml:"privileged"`
		Pid         string `yaml:"pid"`
		Ports       []any  `yaml:"ports"`
		Volumes     []any  `yaml:"volumes"`
	} `yaml:"services"`
	Volumes map[string]any `yaml:"volumes"`
}

func (t *translator) compose(pp *ProjectPlan, p *Project, c *Compose, skipped bool) {
	st := &StackPlan{Source: p.Name + "/" + c.Name, DokployID: c.ID, AppName: c.AppName, Name: Slug(c.Name), Status: Ready,
		Node: t.nodeOf(c.ServerID), YAML: c.ComposeFile, Env: c.Env, EnvKeys: envNames(c.Env)}
	pp.Stacks = append(pp.Stacks, st)
	for _, m := range c.Mounts {
		if m.Type == "file" {
			st.Files = append(st.Files, m)
		}
	}
	block := func(status, f string, a ...any) {
		if st.Status != Blocked {
			st.Status = status
		}
		st.Reasons = append(st.Reasons, fmt.Sprintf(f, a...))
	}
	if skipped {
		st.Status, st.Reasons = Skip, []string{"skipped by --skip"}
		return
	}
	if c.SourceType != "raw" {
		block(Blocked, "compose source %q is not supported by the importer (raw YAML only)", c.SourceType)
	}
	if c.ComposeType == "stack" {
		block(Blocked, "Swarm stack files are not supported")
	}
	var cf composeFile
	if err := yaml.Unmarshal([]byte(c.ComposeFile), &cf); err != nil {
		block(Blocked, "compose YAML does not parse: %v", err)
		return
	}
	domainPorts := map[string]bool{}
	for _, d := range c.Domains {
		if d.Port != nil {
			domainPorts[strconv.Itoa(*d.Port)] = true
		}
	}
	var hostInfra, published, binds []string
	files := false
	for _, name := range sortedKeys(cf.Services) {
		s := cf.Services[name]
		if s.NetworkMode == "host" || s.Privileged || s.Pid == "host" {
			hostInfra = append(hostInfra, name)
		}
		for _, p := range s.Ports {
			hp, cp := hostPort(p)
			if hp != "" && !domainPorts[cp] {
				published = append(published, name+":"+hp)
			}
		}
		for _, v := range s.Volumes {
			src := volumeSource(v)
			switch {
			case strings.HasPrefix(src, "../files") || strings.HasPrefix(src, "./"):
				files = true
			case strings.HasPrefix(src, "/"):
				binds = append(binds, name+":"+src)
			case src != "":
				st.Volumes = append(st.Volumes, name+":"+src)
			}
		}
	}
	if len(hostInfra) > 0 {
		block(Blocked, "uses host networking, host PID or privileged mode (%s): host infrastructure, keep it outside aven (`aven import dokploy export-compose`)", strings.Join(hostInfra, ", "))
	}
	if files || len(st.Files) > 0 {
		block(Blocked, "uses Dokploy file mounts (../files/...): keep it outside aven (`export-compose`) or bake the files into an image")
	}
	if len(published) > 0 {
		block(NeedsDecision, "publishes host ports %s; aven does not publish host ports (only HTTP domains and TCP proxies). Keep it outside aven (`export-compose`) or pass --skip %s", strings.Join(published, ", "), st.Source)
	}
	if len(binds) > 0 {
		if st.Node == "" {
			st.AllowBindMounts = true
			st.Warnings = append(st.Warnings, "bind mounts "+strings.Join(binds, ", ")+" are kept (local node only)")
		} else {
			block(Blocked, "bind mounts %s on remote node %s", strings.Join(binds, ", "), st.Node)
		}
	}
	if c.Isolated {
		st.Warnings = append(st.Warnings, "Dokploy isolated deployment: aven puts every service of the environment on one network")
	}
	if strings.Contains(c.ComposeFile, "dokploy-network") {
		st.Warnings = append(st.Warnings, "references the external network dokploy-network; aven ignores external networks")
	}
	if len(st.Volumes) > 0 {
		st.Warnings = append(st.Warnings, "named volumes start empty in aven: copy the data from the Dokploy volumes ("+c.AppName+"_<name>)")
	}
	// 막힌 스택도 도메인을 기록한다(export-compose가 따로 라우팅해야 할 호스트를 알려 준다).
	{
		for _, d := range c.Domains {
			if d.Enabled != nil && !*d.Enabled {
				continue
			}
			t.pendingStack = append(t.pendingStack, stackDomain{pp: pp, st: st, d: d})
		}
	}
}

type stackDomain struct {
	pp *ProjectPlan
	st *StackPlan
	d  Domain
}

func hostPort(p any) (host, container string) {
	switch x := p.(type) {
	case string:
		parts := strings.Split(strings.SplitN(x, "/", 2)[0], ":")
		switch len(parts) {
		case 1:
			return "", parts[0]
		case 2:
			return parts[0], parts[1]
		default:
			return parts[len(parts)-2], parts[len(parts)-1]
		}
	case int:
		return "", strconv.Itoa(x)
	case map[string]any:
		hp := fmt.Sprint(x["published"])
		if x["published"] == nil {
			hp = ""
		}
		return hp, fmt.Sprint(x["target"])
	}
	return "", ""
}

func volumeSource(v any) string {
	switch x := v.(type) {
	case string:
		src, _, _ := strings.Cut(x, ":")
		if !strings.Contains(x, ":") {
			return "" // 익명 볼륨
		}
		return src
	case map[string]any:
		if s, ok := x["source"].(string); ok {
			return s
		}
	}
	return ""
}
