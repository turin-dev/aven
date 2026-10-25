package ghstatus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

type got struct {
	path, auth string
	body       map[string]string
}

func fakeGitHub(t *testing.T) (*httptest.Server, func() []got) {
	var mu sync.Mutex
	var reqs []got
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		reqs = append(reqs, got{path: r.Method + " " + r.URL.Path, auth: r.Header.Get("Authorization"), body: b})
		mu.Unlock()
		w.WriteHeader(201)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []got {
		mu.Lock()
		defer mu.Unlock()
		return append([]got(nil), reqs...)
	}
}

func TestPostCommitStatus(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "p"}
	env, _ := st.CreateProject(ctx, p)
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "repo"}
	st.CreateService(ctx, svc)

	ts, reqs := fakeGitHub(t)
	poster := &Poster{Store: st, Token: "ghp_test", APIBase: ts.URL, PublicURL: "https://aven.example.com/"}
	repo := "https://github.com/Example-Org/App.git"
	sha := "0123456789abcdef0123456789abcdef01234567"
	d := &store.Deployment{ID: "dep_x", ServiceID: svc.ID, EnvironmentID: env.ID, CommitSHA: &sha, Status: store.DepQueued}
	d.Spec.RepoURL = &repo
	if err := poster.Post(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Status = store.DepLive
	poster.Post(ctx, d)
	r := reqs()
	if len(r) != 2 {
		t.Fatalf("requests: %+v", r)
	}
	if r[0].path != "POST /repos/example-org/app/statuses/"+sha || r[0].auth != "Bearer ghp_test" {
		t.Errorf("request: %+v", r[0])
	}
	if r[0].body["state"] != "pending" || r[1].body["state"] != "success" || r[0].body["context"] != "keel/web-production" {
		t.Errorf("bodies: %+v", r)
	}
	if r[0].body["target_url"] != "https://aven.example.com/?dep=dep_x&env=production&project="+p.ID+"&svc="+svc.ID {
		t.Errorf("target_url: %s", r[0].body["target_url"])
	}

	// 올리지 않는 경우: 토큰 없음, github 아닌 저장소, 잘못된 sha, watch paths로 건너뛴 배포
	gl := "https://gitlab.com/a/b"
	bad := "../../x"
	cases := []func() (*Poster, *store.Deployment){
		func() (*Poster, *store.Deployment) { c := *d; return &Poster{APIBase: ts.URL}, &c },
		func() (*Poster, *store.Deployment) { c := *d; c.Spec.RepoURL = &gl; return poster, &c },
		func() (*Poster, *store.Deployment) { c := *d; c.CommitSHA = &bad; return poster, &c },
		func() (*Poster, *store.Deployment) { c := *d; c.Status = store.DepSkipped; return poster, &c },
	}
	for _, f := range cases {
		pp, dd := f()
		if err := pp.Post(ctx, dd); err != nil {
			t.Error(err)
		}
	}
	if n := len(reqs()); n != 2 {
		t.Fatalf("unexpected requests: %d", n)
	}
	// CI 실패로 건너뛴 배포는 error로 닫는다
	c := *d
	msg := "CI 실패"
	c.Status, c.Error = store.DepSkipped, &msg
	poster.Post(ctx, &c)
	if r := reqs(); len(r) != 3 || r[2].body["state"] != "error" {
		t.Fatalf("ci failure status: %+v", r)
	}
}

func TestHookAsyncOrderAndErrors(t *testing.T) {
	ts, reqs := fakeGitHub(t)
	poster := &Poster{Token: "t", APIBase: ts.URL}
	repo := "git@github.com:o/r.git"
	sha := "abcdef1"
	for _, s := range []store.DeployStatus{store.DepQueued, store.DepInitializing, store.DepFailed} {
		d := store.Deployment{ID: "dep_1", CommitSHA: &sha, Status: s}
		d.Spec.RepoURL = &repo
		poster.Hook(d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(reqs()) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r := reqs()
	if len(r) != 3 || r[0].body["state"] != "pending" || r[1].body["state"] != "pending" || r[2].body["state"] != "failure" {
		t.Fatalf("order: %+v", r)
	}
	// GitHub 오류는 에러로 돌려주지만(로그) 패닉하거나 막지 않는다
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(422) }))
	defer fail.Close()
	d := store.Deployment{ID: "dep_2", CommitSHA: &sha, Status: store.DepLive}
	d.Spec.RepoURL = &repo
	if err := (&Poster{Token: "t", APIBase: fail.URL}).Post(context.Background(), &d); err == nil {
		t.Error("expected error for HTTP 422")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer slow.Close()
	start := time.Now()
	if err := (&Poster{Token: "t", APIBase: slow.URL, Timeout: 100 * time.Millisecond}).Post(context.Background(), &d); err == nil || time.Since(start) > time.Second {
		t.Errorf("timeout not enforced: %v %s", err, time.Since(start))
	}
}

func TestOwnerRepo(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/a/b": "a/b", "https://x-access-token:t@github.com/A/B.git": "a/b", "git@github.com:a/b.git": "a/b",
		"ssh://git@github.com/a/b": "a/b", "https://gitlab.com/a/b": "", "https://github.com/a": "", "https://github.com/a/b/c": "", "https://github.com/../b": "",
	} {
		o, r, ok := OwnerRepo(in)
		got := ""
		if ok {
			got = o + "/" + r
		}
		if got != want {
			t.Errorf("%s → %q, want %q", in, got, want)
		}
	}
}
