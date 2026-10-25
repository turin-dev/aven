package deploy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestBuildCacheKeyTracksSourceAndBuildInputs(t *testing.T) {
	sha, command := "abc123", "npm run build"
	d := &store.Deployment{
		CommitSHA: &sha,
		Spec: store.Spec{
			RootDir: "/web", Builder: "railpack", DockerfilePath: "Dockerfile",
			BuildCommand: &command, BuildSecrets: []string{"NPM_TOKEN", "SENTRY_AUTH_TOKEN"},
			Compose: &store.ComposeSpec{Target: "production", BuildArgs: map[string]string{"MODE": "prod"}},
		},
	}
	vars := map[string]string{"API_URL": "https://api"}
	key := buildCacheKey(d, "railpack", vars)
	if key == "" {
		t.Fatal("repo deployment with a commit should have a cache key")
	}
	if buildCacheKey(d, "railpack", map[string]string{"API_URL": "https://api"}) != key {
		t.Fatal("key must be deterministic")
	}
	mutate := map[string]func(c *store.Deployment){
		"commit":     func(c *store.Deployment) { s := "def456"; c.CommitSHA = &s },
		"root_dir":   func(c *store.Deployment) { c.Spec.RootDir = "/api" },
		"dockerfile": func(c *store.Deployment) { c.Spec.DockerfilePath = "Dockerfile.prod" },
		"command":    func(c *store.Deployment) { s := "make"; c.Spec.BuildCommand = &s },
		"secrets":    func(c *store.Deployment) { c.Spec.BuildSecrets = []string{"NPM_TOKEN"} },
		"compose":    func(c *store.Deployment) { c.Spec.Compose = &store.ComposeSpec{Target: "dev"} },
	}
	for name, f := range mutate {
		c := *d
		f(&c)
		if buildCacheKey(&c, "railpack", vars) == key {
			t.Errorf("%s must affect the key", name)
		}
	}
	c := *d
	c.Spec.BuildSecrets = []string{"SENTRY_AUTH_TOKEN", "NPM_TOKEN"}
	if buildCacheKey(&c, "railpack", vars) != key {
		t.Error("secret ordering must not affect the key")
	}
	if buildCacheKey(d, "dockerfile", vars) == key {
		t.Error("builder must affect the key")
	}
	if buildCacheKey(d, "railpack", map[string]string{"API_URL": "https://other"}) == key {
		t.Error("build variable values must affect the key")
	}
	up := *d
	up.Source = "upload:/tmp/x.tgz"
	if buildCacheKey(&up, "railpack", vars) != "" {
		t.Error("uploads (aven up) always build")
	}
	noSHA := *d
	noSHA.CommitSHA = nil
	if buildCacheKey(&noSHA, "railpack", vars) != "" {
		t.Error("no commit, no key")
	}
}

func TestBuildTimeVarsIgnoresInjectedAndUndeclaredArgs(t *testing.T) {
	d := &store.Deployment{
		VarsRaw: map[string]string{"API_URL": "${{shared.API}}", "RUNTIME_ONLY": "x", "NPM_TOKEN": "t"},
		Spec:    store.Spec{BuildSecrets: []string{"NPM_TOKEN"}},
	}
	resolved := map[string]string{"API_URL": "https://api", "RUNTIME_ONLY": "x", "NPM_TOKEN": "t",
		"KEEL_DEPLOYMENT_ID": "dep_1", "KEEL_ENVIRONMENT_ID": "env_1"}
	all := buildTimeVars(d, resolved, "")
	if len(all) != 3 || all["API_URL"] != "https://api" || all["KEEL_DEPLOYMENT_ID"] != "" {
		t.Fatalf("railpack build vars: %v", all)
	}
	df := filepath.Join(t.TempDir(), "Dockerfile")
	os.WriteFile(df, []byte("FROM alpine\nARG API_URL\nRUN --mount=type=secret,id=NPM_TOKEN true\n"), 0o644)
	got := buildTimeVars(d, resolved, df)
	if len(got) != 2 || got["API_URL"] != "https://api" || got["NPM_TOKEN"] != "t" {
		t.Fatalf("dockerfile build vars: %v", got)
	}
}

// repoFixture는 scaleFixture를 repo 서비스로 보고 Dockerfile이 있는 소스 디렉터리를 만든다.
func repoBuildFixture(t *testing.T) (*scaleFixture, *Target, string) {
	t.Helper()
	f := newScaleFixture(t)
	tg, err := f.m.Load(context.Background(), f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	tg.Service.SourceType = "repo"
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte("FROM alpine\nARG API_URL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, tg, src
}

func repoDep(f *scaleFixture, sha string, vars map[string]string, skip bool) *store.Deployment {
	s := sha
	return &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, NodeID: store.LocalNodeID,
		Trigger: "github", Status: store.DepQueued, CommitSHA: &s, VarsRaw: vars,
		Spec: store.Spec{Builder: "auto", DockerfilePath: "Dockerfile", SkipBuilds: skip}}
}

func TestBuildReusesRetainedImage(t *testing.T) {
	f, tg, src := repoBuildFixture(t)
	ctx := context.Background()
	book := testBook(t)
	defer book.Close("")

	// 이전 배포: 같은 커밋/설정으로 이미 빌드해 이미지가 노드에 남아 있다.
	prev := repoDep(f, "abc123", map[string]string{"API_URL": "https://api", "RUNTIME": "a"}, false)
	if err := f.st.CreateDeployment(ctx, prev); err != nil {
		t.Fatal(err)
	}
	key := f.m.sourceBuildKey(ctx, tg, prev, src)
	if key == "" {
		t.Fatal("expected a cache key")
	}
	img, bu := "keel/web:dep-prev", "dockerfile"
	prev.Image, prev.BuilderUsed, prev.BuildCacheKey, prev.Status = &img, &bu, key, store.DepRemoved
	if err := f.st.UpdateDeployment(ctx, prev); err != nil {
		t.Fatal(err)
	}
	f.local.images[img] = true

	// 런타임 전용 변수(ARG가 아님)가 바뀌어도 재사용한다.
	d := repoDep(f, "abc123", map[string]string{"API_URL": "https://api", "RUNTIME": "b"}, true)
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := f.m.build(ctx, tg, d, book, src, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if d.Image == nil || *d.Image != img || d.BuildReusedFrom == nil || *d.BuildReusedFrom != prev.ID ||
		d.BuilderUsed == nil || *d.BuilderUsed != bu || d.BuildCacheKey != key {
		t.Fatalf("reused deployment: %+v", d)
	}
	got, err := f.st.GetDeployment(ctx, d.ID)
	if err != nil || got.BuildReusedFrom == nil || *got.BuildReusedFrom != prev.ID || got.Image == nil || *got.Image != img {
		t.Fatalf("stored deployment: %+v %v", got, err)
	}

	// 재사용한 배포를 다시 재사용하면 원래 빌드한 배포를 가리킨다.
	d2 := repoDep(f, "abc123", map[string]string{"API_URL": "https://api"}, true)
	f.st.CreateDeployment(ctx, d2)
	got.Status = store.DepLive
	f.st.UpdateDeployment(ctx, got)
	if !f.m.reuseBuild(ctx, tg, d2, book, key) || *d2.BuildReusedFrom != prev.ID {
		t.Fatalf("chained reuse: %+v", d2)
	}

	// 빌드 시점 변수(ARG), 커밋이 바뀌면 키가 달라진다.
	if k := f.m.sourceBuildKey(ctx, tg, repoDep(f, "abc123", map[string]string{"API_URL": "https://other"}, true), src); k == key {
		t.Fatal("ARG value change must change the key")
	}
	if k := f.m.sourceBuildKey(ctx, tg, repoDep(f, "fff999", map[string]string{"API_URL": "https://api"}, true), src); k == key {
		t.Fatal("commit change must change the key")
	}

	// 강제 빌드, 꺼진 설정, 지워진 이미지는 재사용하지 않는다.
	force := repoDep(f, "abc123", map[string]string{"API_URL": "https://api"}, true)
	force.ForceBuild = true
	if f.m.reuseBuild(ctx, tg, force, book, key) || force.Image != nil {
		t.Fatal("force build must bypass reuse")
	}
	off := repoDep(f, "abc123", map[string]string{"API_URL": "https://api"}, false)
	if f.m.reuseBuild(ctx, tg, off, book, key) {
		t.Fatal("skip_builds off must not reuse")
	}
	delete(f.local.images, img)
	gone := repoDep(f, "abc123", map[string]string{"API_URL": "https://api"}, true)
	if f.m.reuseBuild(ctx, tg, gone, book, key) || gone.Image != nil {
		t.Fatal("pruned image must not be reused")
	}
}
