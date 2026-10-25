package imageupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		image, registry, repo, tag, digest string
	}{
		{"nginx", "docker.io", "library/nginx", "latest", ""},
		{"ghcr.io/acme/api:v1.2.3", "ghcr.io", "acme/api", "v1.2.3", ""},
		{"registry.example:5000/team/app:canary", "registry.example:5000", "team/app", "canary", ""},
		{"acme/api@sha256:abcd", "docker.io", "acme/api", "", "sha256:abcd"},
	} {
		r, err := Parse(tc.image)
		if err != nil || r.Registry != tc.registry || r.Repository != tc.repo || r.Tag != tc.tag || r.Digest != tc.digest {
			t.Errorf("Parse(%q) = %+v, %v", tc.image, r, err)
		}
	}
	for _, image := range []string{"", "nginx:", "nginx@", "nginx:tag extra"} {
		if _, err := Parse(image); err == nil {
			t.Errorf("Parse(%q) succeeded", image)
		}
	}
}

func TestDiscoverSemverPolicies(t *testing.T) {
	c := fakeRegistry(t, []string{"v1.2.4", "v1.3.0", "v2.0.0", "canary"})
	for _, tc := range []struct {
		policy, want string
	}{
		{"patch", "ghcr.io/acme/app:v1.2.4"},
		{"minor", "ghcr.io/acme/app:v1.3.0"},
		{"disabled", ""},
	} {
		got, err := c.Discover(context.Background(), "ghcr.io/acme/app:v1.2.3", tc.policy, "")
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" && got.Candidate != nil || tc.want != "" && (got.Candidate == nil || got.Candidate.Image != tc.want) {
			t.Errorf("policy %s candidate = %+v, want %q", tc.policy, got.Candidate, tc.want)
		}
	}
}

func TestDiscoverMajorPinAndMutableDigest(t *testing.T) {
	c := fakeRegistry(t, []string{"1.2.4", "1.3.0", "2.0.0"})
	got, err := c.Discover(context.Background(), "acme/app:1.2.3", "minor", "")
	if err != nil || got.Candidate == nil || got.Candidate.Image != "acme/app:1.3.0" || !strings.Contains(got.Candidate.DigestImage, "@sha256:") {
		t.Fatalf("major-pinned update: %+v, %v", got, err)
	}
	c = fakeRegistry(t, []string{"latest", "canary"})
	got, err = c.Discover(context.Background(), "acme/app:canary", "patch", "sha256:old")
	if err != nil || got.Candidate == nil || got.Candidate.Image != "acme/app:canary" || got.Candidate.Digest != "sha256:new" {
		t.Fatalf("mutable tag update: %+v, %v", got, err)
	}
	got, err = c.Discover(context.Background(), "acme/app@sha256:fixed", "minor", "")
	if err != nil || got.Candidate != nil {
		t.Fatalf("digest-pinned source was considered mutable: %+v, %v", got, err)
	}
}

func TestRegistryBearerAuthAndMaintenanceWindows(t *testing.T) {
	var authenticated bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			user, pass, ok := r.BasicAuth()
			authenticated = ok && user == "reader" && pass == "secret"
			fmt.Fprint(w, `{"token":"registry-token"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer registry-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srvURL(r)+`/token",service="test",scope="repository:acme/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/manifests/latest") {
			w.Header().Set("Docker-Content-Digest", "sha256:tag")
			return
		}
		fmt.Fprint(w, `{"tags":["latest"]}`)
	}))
	defer srv.Close()
	c := NewClient(&Auth{Username: "reader", Password: "secret"})
	c.HTTP, c.base = srv.Client(), srv.URL
	r, _ := Parse("acme/app:latest")
	if _, err := c.ManifestDigest(context.Background(), r, r.Tag); err != nil || !authenticated {
		t.Fatalf("authenticated registry digest: %v (auth=%t)", err, authenticated)
	}

	if err := ValidateWindow("custom", []int{1, 3}, 22, 6); err == nil {
		t.Error("overnight custom window should be rejected")
	}
	if err := ValidateWindow("custom", []int{1, 3}, 2, 6); err != nil {
		t.Fatal(err)
	}
	if InWindow("weekends", nil, 0, 0, time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)) != true {
		t.Error("Saturday should be inside the weekends window")
	}
	if InWindow("night", nil, 0, 0, time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)) {
		t.Error("06:00 UTC should be outside the night window")
	}
	if InWindow("custom", []int{1}, 2, 6, time.Date(2026, 10, 19, 5, 59, 0, 0, time.UTC)) != true {
		t.Error("custom schedule should include hours before its exclusive end")
	}
}

func srvURL(r *http.Request) string {
	return "https://" + r.Host
}

func fakeRegistry(t *testing.T, tags []string) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tags/list") {
			body, _ := json.Marshal(map[string][]string{"tags": tags})
			w.Write(body)
			return
		}
		tag := strings.TrimPrefix(r.URL.Path, "/v2/acme/app/manifests/")
		digest := "sha256:new"
		if tag == "v1.2.3" || tag == "1.2.3" {
			digest = "sha256:old"
		}
		w.Header().Set("Docker-Content-Digest", digest)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(nil)
	c.HTTP, c.base = srv.Client(), srv.URL
	return c
}
