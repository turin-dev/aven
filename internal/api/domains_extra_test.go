package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
	"github.com/turin-dev/aven/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestValidateDomainExtras(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{Store: st}
	in := &store.Instance{ID: "ins_x", EnvironmentID: "env_x", ServiceID: "svc_x"}
	port := func(p int) *int { return &p }
	cases := []struct {
		name string
		d    store.Domain
		ok   bool
	}{
		{"plain", store.Domain{Host: "a.example.com"}, true},
		{"tls off", store.Domain{Host: "a.example.com", TLS: "off"}, true},
		{"tls default alias", store.Domain{Host: "a.example.com", TLS: "default"}, true},
		{"tls bogus", store.Domain{Host: "a.example.com", TLS: "http"}, false},
		{"redirect ok", store.Domain{Host: "www.example.com", Redirect: &store.Redirect{To: "example.com", Permanent: true}}, true},
		{"redirect url", store.Domain{Host: "www.example.com", Redirect: &store.Redirect{To: "https://example.com/x"}}, true},
		{"redirect self", store.Domain{Host: "www.example.com", Redirect: &store.Redirect{To: "https://www.example.com/"}}, false},
		{"redirect placeholder injection", store.Domain{Host: "w.example.com", Redirect: &store.Redirect{To: "x.com{http.request.host}"}}, false},
		{"redirect + paths", store.Domain{Host: "w.example.com", Redirect: &store.Redirect{To: "x.com"}, Paths: []store.PathRule{{Path: "/a"}}}, false},
		{"auth user bad", store.Domain{Host: "a.example.com", BasicAuth: &store.BasicAuth{Username: "a b"}}, false},
		{"path ok", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "/api/*", TargetPort: port(8080), StripPrefix: true}, {Path: "/healthz"}}}, true},
		{"path no slash", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "api"}}}, false},
		{"path placeholder", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "/{http.request.host}"}}}, false},
		{"path dup", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "/a"}, {Path: "/a"}}}, false},
		{"strip on exact", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "/a", StripPrefix: true}}}, false},
		{"bad port", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "/a/*", TargetPort: port(70000)}}}, false},
		{"unknown service", store.Domain{Host: "a.example.com", Paths: []store.PathRule{{Path: "/a/*", Service: "ghost"}}}, false},
	}
	for _, c := range cases {
		d := c.d
		err := s.validateDomainExtras(ctx, in, &d)
		if (err == nil) != c.ok {
			t.Errorf("%s: ok=%v err=%v", c.name, c.ok, err)
		}
	}
}

func TestApplyDomainAuthHashesAndKeeps(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secret.LoadOrCreate(filepath.Join(t.TempDir(), "m.key"))
	st.SetSecretBox(box)
	s := &Server{Store: st}
	in := &store.Instance{ID: "ins_a"}
	doms := []store.Domain{{Host: "a.example.com", BasicAuth: &store.BasicAuth{Username: "u", Password: "s3cret-pass"}}}
	if err := s.applyDomainAuth(ctx, in, doms); err != nil {
		t.Fatal(err)
	}
	if doms[0].BasicAuth.Password != "" || !doms[0].BasicAuth.HasPassword {
		t.Errorf("password must be cleared from the stored domain: %+v", doms[0].BasicAuth)
	}
	h, _ := st.GetSetting(ctx, store.DomainAuthKey("ins_a", "a.example.com"))
	if strings.Contains(h, "s3cret-pass") || bcrypt.CompareHashAndPassword([]byte(h), []byte("s3cret-pass")) != nil {
		t.Fatalf("not a bcrypt hash of the password: %q", h)
	}
	// 비밀번호 없이 같은 사용자 → 기존 해시 유지
	in.Domains = doms
	next := []store.Domain{{Host: "a.example.com", BasicAuth: &store.BasicAuth{Username: "v", HasPassword: true}}}
	if err := s.applyDomainAuth(ctx, in, next); err != nil {
		t.Fatal(err)
	}
	if h2, _ := st.GetSetting(ctx, store.DomainAuthKey("ins_a", "a.example.com")); h2 != h {
		t.Error("hash changed although no new password was given")
	}
	// 새 호스트는 비밀번호 필요
	if err := s.applyDomainAuth(ctx, in, []store.Domain{{Host: "b.example.com", BasicAuth: &store.BasicAuth{Username: "u"}}}); err == nil {
		t.Error("password required for a new protected host")
	}
	// 제거하면 해시도 지운다
	if err := s.applyDomainAuth(ctx, in, []store.Domain{{Host: "a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if h3, _ := st.GetSetting(ctx, store.DomainAuthKey("ins_a", "a.example.com")); h3 != "" {
		t.Error("hash not removed")
	}
}
