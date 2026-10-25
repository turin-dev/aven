package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/imageupdate"
	"github.com/turin-dev/aven/internal/store"
)

// redirectTransport는 모든 레지스트리 요청을 테스트 서버로 보낸다.
type redirectTransport struct{ target *url.URL }

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

type imageUpdateFixture struct {
	m          *Manager
	st         *store.Store
	in         *store.Instance
	id         string
	mu         sync.Mutex
	dispatched []string
}

func newImageUpdateFixture(t *testing.T, image string, tags map[string]string) *imageUpdateFixture {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			names := []string{}
			for tag := range tags {
				names = append(names, tag)
			}
			json.NewEncoder(w).Encode(map[string][]string{"tags": names})
			return
		}
		_, tag, _ := strings.Cut(r.URL.Path, "/manifests/")
		digest, ok := tags[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest)
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	prev := newRegistryClient
	newRegistryClient = func(auth *imageupdate.Auth) *imageupdate.Client {
		c := imageupdate.NewClient(auth)
		c.HTTP = &http.Client{Transport: redirectTransport{target}}
		return c
	}
	t.Cleanup(func() { newRegistryClient = prev })

	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &imageUpdateFixture{st: st}
	f.m = &Manager{Store: st, Agents: &fakeHub{}, localDrv: &localFake{n: newFakeNode("local", &opLog{})},
		Dispatch: func(_, dep string) { f.mu.Lock(); f.dispatched = append(f.dispatched, dep); f.mu.Unlock() }}
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
	in.Image = &image
	in.ImageUpdatePolicy, in.ImageUpdateWindow = "minor", "night"
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	d := &store.Deployment{ServiceID: svc.ID, EnvironmentID: env.ID, InstanceID: in.ID, Trigger: "manual",
		Spec: in.Spec, ReqSpec: in.Spec, NodeID: store.LocalNodeID, Status: store.DepLive}
	if err := st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID); err != nil {
		t.Fatal(err)
	}
	f.id = in.ID
	f.in = f.instance(t)
	return f
}

func (f *imageUpdateFixture) instance(t *testing.T) *store.Instance {
	t.Helper()
	in, err := f.st.GetInstance(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func (f *imageUpdateFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dispatched)
}

func (f *imageUpdateFixture) waitDispatch(t *testing.T) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		f.mu.Lock()
		n := len(f.dispatched)
		f.mu.Unlock()
		if n > 0 {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.dispatched[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("image update was not dispatched")
	return ""
}

func (f *imageUpdateFixture) history(t *testing.T) []*store.ImageUpdate {
	t.Helper()
	h, err := f.st.ListImageUpdates(context.Background(), f.in.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestImageUpdateWaitsForWindowThenRestoresOnFailure(t *testing.T) {
	ctx := context.Background()
	f := newImageUpdateFixture(t, "ghcr.io/acme/app:1.2.3", map[string]string{
		"1.2.3": "sha256:old", "1.2.4": "sha256:p", "1.3.0": "sha256:m", "2.0.0": "sha256:major"})

	noon := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	f.m.imageUpdateTick(ctx, noon)
	h := f.history(t)
	if len(h) != 1 || h[0].Status != "available" || h[0].TargetImage != "ghcr.io/acme/app:1.3.0" || h[0].DigestImage != "ghcr.io/acme/app@sha256:m" {
		t.Fatalf("after detection: %+v", h)
	}
	if f.count() != 0 || *f.instance(t).Image != "ghcr.io/acme/app:1.2.3" {
		t.Fatal("update applied outside the maintenance window")
	}

	// 자동 적용은 실제 현재 시각으로 창을 다시 확인하므로, 테스트에서는 창을 anytime으로 바꿔 창이 열린 상태를 만든다.
	in := f.instance(t)
	in.ImageUpdateWindow = "anytime"
	if err := f.st.UpdateInstanceConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.m.imageUpdateTick(ctx, noon.Add(time.Minute)) // 캐시 안: 레지스트리는 다시 묻지 않고 적용만 한다
	depID := f.waitDispatch(t)
	if got := *f.instance(t).Image; got != "ghcr.io/acme/app:1.3.0" {
		t.Fatalf("configured image = %s, want the new semver tag", got)
	}
	d, err := f.st.GetDeployment(ctx, depID)
	if err != nil || d.Trigger != "image_update" || d.Image == nil || *d.Image != "ghcr.io/acme/app@sha256:m" {
		t.Fatalf("deployment: %+v, %v", d, err)
	}
	if h := f.history(t); h[0].Status != "deploying" || !h[0].Automatic || h[0].DeploymentID != depID {
		t.Fatalf("while deploying: %+v", h[0])
	}

	f.m.finish(ctx, d, store.DepFailed, "healthcheck failed")
	if h := f.history(t); h[0].Status != "failed" || h[0].Error == nil {
		t.Fatalf("after failure: %+v", h[0])
	}
	if got := *f.instance(t).Image; got != "ghcr.io/acme/app:1.2.3" {
		t.Fatalf("configured image after failed update = %s, want the previous tag restored", got)
	}
	// 실패한 버전은 자동으로 다시 시도하지 않는다.
	f.m.imageUpdateTick(ctx, noon.Add(time.Hour))
	if f.count() != 1 {
		t.Fatalf("failed update retried automatically: %v", f.dispatched)
	}
}

func TestManualImageUpdateAndSkip(t *testing.T) {
	ctx := context.Background()
	f := newImageUpdateFixture(t, "acme/app:latest", map[string]string{"latest": "sha256:one"})
	in := f.instance(t)
	in.ImageUpdatePolicy = "disabled" // 수동 확인/적용은 자동 업데이트가 꺼져 있어도 된다

	if u, err := f.m.CheckImageUpdate(ctx, in, time.Now()); err != nil || u != nil {
		t.Fatalf("first check records the baseline digest only: %+v, %v", u, err)
	}
	f.m.Store.SetImageUpdateState(ctx, &store.ImageUpdateState{InstanceID: in.ID, LastDigest: "sha256:running"})
	u, err := f.m.CheckImageUpdate(ctx, in, time.Now())
	if err != nil || u == nil || u.TargetImage != "acme/app:latest" || u.DigestImage != "docker.io/acme/app@sha256:one" {
		t.Fatalf("mutable tag update: %+v, %v", u, err)
	}

	if err := f.st.SkipImageUpdate(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	again, err := f.m.CheckImageUpdate(ctx, in, time.Now())
	if err != nil || again == nil || again.Status != "skipped" {
		t.Fatalf("skipped version offered again: %+v, %v", again, err)
	}
	if _, err := f.m.ApplyImageUpdate(ctx, in, again, true); err == nil {
		t.Fatal("automatic apply of a skipped/disabled update should fail")
	}

	// 새 digest가 올라오면 다시 업데이트가 생기고, 수동 적용은 창과 상관없이 바로 배포한다.
	f2 := newImageUpdateFixture(t, "acme/app:latest", map[string]string{"latest": "sha256:two"})
	in2 := f2.instance(t)
	f2.st.SetImageUpdateState(ctx, &store.ImageUpdateState{InstanceID: in2.ID, LastDigest: "sha256:one"})
	u2, err := f2.m.CheckImageUpdate(ctx, in2, time.Now())
	if err != nil || u2 == nil {
		t.Fatalf("second update: %+v, %v", u2, err)
	}
	d, err := f2.m.ApplyImageUpdate(ctx, in2, u2, false)
	if err != nil || d == nil {
		t.Fatalf("manual apply: %v", err)
	}
	f2.m.finish(ctx, d, store.DepLive, "")
	h := f2.history(t)
	if h[0].Status != "applied" || h[0].Automatic {
		t.Fatalf("after manual apply: %+v", h[0])
	}
	if st, _ := f2.st.GetImageUpdateState(ctx, in2.ID); st.LastDigest != "sha256:two" {
		t.Fatalf("applied digest not recorded: %+v", st)
	}
	if got := *f2.instance(t).Image; got != "acme/app:latest" {
		t.Fatalf("mutable tag must be preserved, got %s", got)
	}
}
