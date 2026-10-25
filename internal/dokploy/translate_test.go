package dokploy

import (
	"encoding/json"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }

// fixture는 대기 복제본, 원격 노드, 다른 프로젝트의 DB, 경로 라우팅을 담은 합성 스냅샷이다.
func fixture() *Snapshot {
	remote := "srv-remote"
	return &Snapshot{
		Servers: []*Server{{ID: remote, Name: "remote", IPAddress: "100.64.0.1", Type: "deploy"}},
		Projects: []*Project{
			{Name: "sample-primary", Environments: []*Environment{{Name: "production",
				Databases: []*Database{
					{Kind: "postgres", ID: "pg1", Name: "postgres-main-remote", AppName: "postgres-main-remote-abc", DatabaseName: "sample-primary", DatabaseUser: "postgres",
						Password: "pgpw", DockerImage: "postgres:16", ExternalPort: ip(15432), ServerID: &remote,
						Mounts: []Mount{{Type: "volume", VolumeName: "postgres-main-remote-abc-data", MountPath: "/var/lib/postgresql/data"}}},
					{Kind: "redis", ID: "rd1", Name: "redis-main-remote", AppName: "redis-main-remote-xyz", Password: "rdpw", DockerImage: "redis:7", ExternalPort: ip(16379), ServerID: &remote},
					{Kind: "postgres", ID: "pg2", Name: "postgres-main", AppName: "postgres-main-main", DatabaseName: "sample-primary", DatabaseUser: "postgres", Password: "mainpw", DockerImage: "postgres:16"},
				},
				Applications: []*Application{
					{ID: "a1", Name: "api", AppName: "sample-primary-api-1", Status: "idle", SourceType: "git", CustomGitURL: "git@github.com:o/r.git", CustomGitBranch: "main", BuildType: "dockerfile",
						Domains: []Domain{{Host: "api.example.net", Port: ip(3000), HTTPS: true, Path: "/"}}},
					{ID: "a2", Name: "api-remote", AppName: "sample-primary-api-remote-1", Status: "done", ServerID: &remote, BuildServerID: sp("srv-build"), SourceType: "git", CustomGitURL: "git@github.com:o/r.git", CustomGitBranch: "main",
						BuildType: "dockerfile", Dockerfile: "Dockerfile", DockerContext: ".", Replicas: 1, MemoryLimit: float64(512 << 20), BuildArgs: "APP=api",
						Env:     "DATABASE_URL=postgresql://postgres:pgpw@postgres-main-remote-abc:5432/sample-primary\nREDIS_URL=redis://:rdpw@redis-main-remote-xyz:6379\nMAIL=http://mail-stack-front-1\nPLAIN=hello",
						Domains: []Domain{{Host: "api.example.net", Port: ip(3000), HTTPS: false, Path: "/"}}},
					{ID: "a3", Name: "sample-mail", AppName: "sample-primary-sample-mail", Status: "done", SourceType: "github", Owner: "o", Repository: "r", Branch: "main", BuildType: "dockerfile",
						Env:     "DATABASE_URL=postgresql://postgres:pgpw@100.64.0.1:15432/sample-primary?sslmode=disable\nREDIS_URL=redis://:rdpw@100.64.0.1:16379",
						Domains: []Domain{{Host: "mail.example.net", Port: ip(3013), HTTPS: true, Path: "/"}}},
				},
				Composes: []*Compose{
					{ID: "c1", Name: "mail", AppName: "mail-stack", SourceType: "raw", ComposeType: "docker-compose",
						ComposeFile: "services:\n  front:\n    image: mailu/nginx\n    ports:\n      - \"25:25\"\n      - \"80\"\n"},
					{ID: "c2", Name: "tunnel", AppName: "tunnel-1", SourceType: "raw", ComposeType: "docker-compose", ServerID: &remote,
						ComposeFile: "services:\n  cloudflared:\n    image: cloudflare/cloudflared\n    network_mode: host\n"},
				},
			}}},
			{Name: "sample-secondary", Environments: []*Environment{{Name: "production",
				Applications: []*Application{
					{ID: "b1", Name: "sample-storage", AppName: "sample-storage-1", Status: "done", SourceType: "github", Owner: "o", Repository: "p", Branch: "master", BuildType: "railpack",
						Env:     "DATABASE_URL=postgresql://postgres:mainpw@postgres-main-main:5432/sample-primary",
						Domains: []Domain{{Host: "example.com", Port: ip(3000), HTTPS: true, Path: "/"}}},
					{ID: "b2", Name: "sample-bot", AppName: "fbot", Status: "done", SourceType: "github", Owner: "o", Repository: "f", Branch: "main", BuildType: "dockerfile",
						Dockerfile: "bot/Dockerfile", DockerContext: "bot", WatchPaths: []string{"bot/**"},
						Domains: []Domain{{Host: "Bot.f.example.com", Port: ip(3000), HTTPS: true, Path: "/"}, {Host: "bot.f.example.com", Port: ip(3000), HTTPS: true, Path: "/"}}},
					{ID: "b3", Name: "web", AppName: "web-1", Status: "done", SourceType: "docker", DockerImage: "ghcr.io/o/web:latest", RegistryURL: "ghcr.io", Username: "u", Password: "p",
						Domains: []Domain{{Host: "x.example.com", Port: ip(3000), HTTPS: true, Path: "/"}}},
					{ID: "b4", Name: "public", AppName: "pub-1", Status: "done", SourceType: "docker", DockerImage: "ghcr.io/o/pub:latest",
						Domains: []Domain{{Host: "x.example.com", Port: ip(8080), HTTPS: true, Path: "/api"}, {Host: "x.example.com", Port: ip(8080), HTTPS: true, Path: "/healthz"}}},
				},
				Composes: []*Compose{
					{ID: "c3", Name: "Downloads", AppName: "dl-1", SourceType: "raw", ComposeType: "docker-compose",
						ComposeFile: "services:\n  nginx:\n    image: nginx\n    volumes:\n      - /srv/downloads:/usr/share/nginx/html:ro\n",
						Domains:     []Domain{{Host: "dl.example.com", Port: ip(80), HTTPS: true, Path: "/", ServiceName: "nginx"}}},
				},
			}}},
		},
	}
}

func find(p *Plan, project, name string) *ServicePlan {
	for _, pp := range p.Projects {
		if pp.Name != project {
			continue
		}
		for _, s := range pp.Services {
			if s.Name == name && s.Status != Skip {
				return s
			}
		}
	}
	return nil
}

func findStack(p *Plan, project, name string) *StackPlan {
	for _, pp := range p.Projects {
		if pp.Name == project {
			for _, s := range pp.Stacks {
				if s.Name == name {
					return s
				}
			}
		}
	}
	return nil
}

func vars(t *testing.T, s *ServicePlan) map[string]string {
	t.Helper()
	out := map[string]string{}
	if s.Service.Variables == nil {
		return out
	}
	for k, v := range *s.Service.Variables {
		out[k] = v.Value
	}
	return out
}

func setting(t *testing.T, s *ServicePlan, k string, out any) {
	t.Helper()
	raw, ok := s.Service.Settings[k]
	if !ok {
		t.Fatalf("%s: setting %s missing", s.Name, k)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func TestStandbyAndRename(t *testing.T) {
	p := Translate(fixture(), Options{})
	api := find(p, "sample-primary", "api")
	if api == nil || api.Source != "sample-primary/api-remote" || api.Node != "remote" || api.Status != Ready {
		t.Fatalf("api-remote should become api on remote: %+v", api)
	}
	if api.DeployKey {
		t.Error("GitHub SSH URLs become https (GitHub connection), no deploy key")
	}
	var ru string
	setting(t, api, "repo_url", &ru)
	if ru != "https://github.com/o/r" {
		t.Errorf("repo_url %q", ru)
	}
	if p := Translate(fixture(), Options{GitHubSSH: true}); !find(p, "sample-primary", "api").DeployKey {
		t.Error("--github-ssh keeps SSH and needs a deploy key")
	}
	var bn string
	setting(t, api, "build_node", &bn)
	if bn != "local" {
		t.Errorf("Dokploy build server should become build_node=local, got %q", bn)
	}
	var mem int
	setting(t, api, "memory_limit_mb", &mem)
	if mem != 512 {
		t.Errorf("memory %d", mem)
	}
	skipped := 0
	for _, pp := range p.Projects {
		for _, s := range pp.Services {
			if s.Source == "sample-primary/api" && s.Status == Skip {
				skipped++
			}
		}
	}
	if skipped != 1 {
		t.Error("main standby api should be skipped")
	}
	// --keep-standby: 둘 다 가져오고 이름은 그대로
	p = Translate(fixture(), Options{KeepStandby: true})
	if find(p, "sample-primary", "api") == nil || find(p, "sample-primary", "api-remote") == nil {
		t.Error("keep-standby should import both")
	}
}

func TestVariableRewrite(t *testing.T) {
	p := Translate(fixture(), Options{})
	v := vars(t, find(p, "sample-primary", "api"))
	if v["DATABASE_URL"] != "${{postgres-main-remote.DATABASE_URL}}" {
		t.Errorf("canonical postgres URL: %q", v["DATABASE_URL"])
	}
	if v["REDIS_URL"] != "${{redis-main-remote.REDIS_URL}}" {
		t.Errorf("redis URL with empty user: %q", v["REDIS_URL"])
	}
	if v["MAIL"] != "http://front" {
		t.Errorf("compose container name should become the compose service alias: %q", v["MAIL"])
	}
	if v["PLAIN"] != "hello" || v["APP"] != "api" {
		t.Errorf("literal / build arg: %q %q", v["PLAIN"], v["APP"])
	}
	// tailnet IP + 외부 포트, 쿼리스트링 → 구성 요소별 참조
	m := vars(t, find(p, "sample-primary", "sample-mail"))
	want := "postgresql://postgres:${{postgres-main-remote.POSTGRES_PASSWORD}}@${{postgres-main-remote.PGHOST}}:${{postgres-main-remote.PGPORT}}/sample-primary?sslmode=disable"
	if m["DATABASE_URL"] != want {
		t.Errorf("addr form:\n got %q\nwant %q", m["DATABASE_URL"], want)
	}
	if m["REDIS_URL"] != "${{redis-main-remote.REDIS_URL}}" {
		t.Errorf("redis via tailnet: %q", m["REDIS_URL"])
	}
	mw := find(p, "sample-primary", "sample-mail")
	if !strings.Contains(strings.Join(mw.Warnings, "\n"), "another node") {
		t.Errorf("cross-node reference should warn: %v", mw.Warnings)
	}
	// 계획 출력에는 비밀번호가 없다
	b, _ := json.Marshal(p)
	for _, secret := range []string{"pgpw", "rdpw", "mainpw", `"p"`} {
		if strings.Contains(string(b), secret) {
			t.Errorf("plan JSON leaks %s", secret)
		}
	}
}

func TestDatabaseVariables(t *testing.T) {
	p := Translate(fixture(), Options{})
	db := find(p, "sample-primary", "postgres-main-remote")
	v := vars(t, db)
	if v["POSTGRES_PASSWORD"] != "pgpw" || v["POSTGRES_USER"] != "postgres" || v["POSTGRES_DB"] != "sample-primary" {
		t.Errorf("credentials: %v", v)
	}
	if !strings.Contains(v["DATABASE_URL"], "${{PGHOST}}") {
		t.Errorf("generated vars should stay: %q", v["DATABASE_URL"])
	}
	var img string
	setting(t, db, "image", &img)
	if img != "postgres:16" {
		t.Errorf("image %q", img)
	}
}

func TestCrossProjectNeedsMove(t *testing.T) {
	p := Translate(fixture(), Options{})
	pot := find(p, "sample-secondary", "sample-storage")
	if pot.Status != NeedsDecision || len(pot.Reasons) != 1 {
		t.Fatalf("sample-storage should need a decision once: %s %v", pot.Status, pot.Reasons)
	}
	p = Translate(fixture(), Options{Move: map[string]string{"sample-secondary/sample-storage": "sample-primary"}})
	pot = find(p, "sample-primary", "sample-storage")
	if pot == nil || pot.Status != Ready {
		t.Fatalf("moved sample-storage: %+v", pot)
	}
	if vars(t, pot)["DATABASE_URL"] != "${{postgres-main.DATABASE_URL}}" {
		t.Errorf("moved reference: %q", vars(t, pot)["DATABASE_URL"])
	}
}

func TestDomains(t *testing.T) {
	p := Translate(fixture(), Options{})
	api := find(p, "sample-primary", "api")
	var doms []iacDomain
	setting(t, api, "domains", &doms)
	if len(doms) != 1 || doms[0].Host != "api.example.net" || doms[0].TLS != "off" || !doms[0].TrustForwardedProto {
		t.Errorf("tunnel domain: %+v", doms)
	}
	bot := find(p, "sample-secondary", "sample-bot")
	doms = nil
	setting(t, bot, "domains", &doms)
	if len(doms) != 1 || doms[0].Host != "bot.f.example.com" {
		t.Errorf("case duplicates should merge: %+v", doms)
	}
	if !strings.Contains(strings.Join(bot.Warnings, "\n"), "letter case") {
		t.Errorf("merge should be reported: %v", bot.Warnings)
	}
	var root, df string
	setting(t, bot, "root_dir", &root)
	if root != "/bot" {
		t.Errorf("root_dir %q", root)
	}
	if _, ok := bot.Service.Settings["dockerfile_path"]; ok {
		setting(t, bot, "dockerfile_path", &df)
		t.Errorf("bot/Dockerfile in context bot should be the default, got %q", df)
	}
	web := find(p, "sample-secondary", "web")
	doms = nil
	setting(t, web, "domains", &doms)
	if len(doms) != 1 || len(doms[0].Paths) != 2 {
		t.Fatalf("path rules: %+v", doms)
	}
	r := doms[0].Paths[0]
	if r.Path != "/api/*" || r.Service != "public" || r.TargetPort == nil || *r.TargetPort != 8080 {
		t.Errorf("rule: %+v", r)
	}
	if _, ok := find(p, "sample-secondary", "public").Service.Settings["domains"]; ok {
		t.Error("path-rule target must not own the host")
	}
}

func TestCompose(t *testing.T) {
	p := Translate(fixture(), Options{})
	if s := findStack(p, "sample-primary", "mail"); s.Status != NeedsDecision || !strings.Contains(s.Reasons[0], "front:25") {
		t.Errorf("mail: %s %v", s.Status, s.Reasons)
	}
	if s := findStack(p, "sample-primary", "tunnel"); s.Status != Blocked {
		t.Errorf("host network stack: %s", s.Status)
	}
	dl := findStack(p, "sample-secondary", "downloads")
	if dl.Status != Ready || !dl.AllowBindMounts || dl.Domains["nginx"][0].Host != "dl.example.com" {
		t.Errorf("downloads: %+v", dl)
	}
	p = Translate(fixture(), Options{Skip: map[string]bool{"SAMPLE-PRIMARY/Mail": true}})
	if s := findStack(p, "sample-primary", "mail"); s.Status != Skip {
		t.Errorf("skip is case-insensitive: %s", s.Status)
	}
	// 건너뛴 스택을 가리키는 env는 그대로 두고 경고한다
	if v := vars(t, find(p, "sample-primary", "api"))["MAIL"]; v != "http://mail-stack-front-1" {
		t.Errorf("reference to a skipped stack should stay: %q", v)
	}
}

func TestRegistry(t *testing.T) {
	p := Translate(fixture(), Options{})
	for _, pp := range p.Projects {
		if pp.Name == "sample-secondary" {
			if len(pp.Registries) != 1 || pp.Registries[0].Host != "ghcr.io" || pp.Registries[0].Password != "p" {
				t.Errorf("registries: %+v", pp.Registries)
			}
			return
		}
	}
	t.Error("no sample-secondary project")
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Sample Game": "sample-game", "Nginx Static JSON Server": "nginx-static-json-server", "Private": "private", "a__b": "a-b", "": "service"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPropagate(t *testing.T) {
	p := Translate(fixture(), Options{Skip: map[string]bool{"sample-primary/postgres-main-remote": true}})
	// 건너뛴 DB를 참조하면 경고만 한다(사용자가 고른 것)
	if s := find(p, "sample-primary", "sample-mail"); s.Status != Ready {
		t.Errorf("skip is a user choice: %s %v", s.Status, s.Reasons)
	}
	p = Translate(fixture(), Options{})
	for _, pp := range p.Projects {
		if pp.Name != "sample-primary" {
			continue
		}
		for _, s := range pp.Services {
			if s.Name == "redis-main-remote" {
				s.Status = Blocked // aven 노드가 없다고 가정
			}
		}
		Propagate(pp)
	}
	if s := find(p, "sample-primary", "sample-mail"); s.Status != Blocked || !strings.Contains(strings.Join(s.Reasons, ""), "redis-main-remote") {
		t.Errorf("sample-mail depends on a blocked DB: %s %v", s.Status, s.Reasons)
	}
}
