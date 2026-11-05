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

func TestDiscoverTagSelection(t *testing.T) {
	for _, tc := range []struct {
		name, image, policy string
		tags                []string
		want                string
	}{
		{"numeric not lexical order", "acme/app:1.2.3", "patch", []string{"1.2.9", "1.2.10", "1.2.2"}, "acme/app:1.2.10"},
		{"minor picks highest minor then patch", "acme/app:1.2.3", "minor", []string{"1.10.0", "1.9.9", "1.10.1", "1.2.4"}, "acme/app:1.10.1"},
		{"never crosses major", "acme/app:1.2.3", "minor", []string{"2.0.0", "3.1.0"}, ""},
		{"ignores prereleases and odd tags", "acme/app:1.2.3", "minor", []string{"1.3.0-rc1", "1.3", "latest", "01.3.0", "1.3.0.1"}, ""},
		{"ignores older versions", "acme/app:1.2.3", "minor", []string{"1.2.2", "1.1.9", "1.2.3"}, ""},
		{"same v prefix only", "acme/app:v1.2.3", "patch", []string{"1.2.9", "v1.2.4"}, "acme/app:v1.2.4"},
		{"no v tags offered for plain current", "acme/app:1.2.3", "patch", []string{"v1.2.9"}, ""},
		{"int overflow is not a version", "acme/app:1.2.3", "patch", []string{"1.2.99999999999999999999"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeRegistry(t, tc.tags)
			got, err := c.Discover(context.Background(), tc.image, tc.policy, "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if got.Candidate != nil {
					t.Errorf("unexpected candidate %+v", got.Candidate)
				}
				return
			}
			if got.Candidate == nil || got.Candidate.Image != tc.want {
				t.Errorf("candidate = %+v, want %s", got.Candidate, tc.want)
			}
		})
	}
}

func TestParseMore(t *testing.T) {
	for _, tc := range []struct {
		image, registry, repo, tag, digest string
	}{
		{"localhost/app", "localhost", "app", "latest", ""},
		{"localhost:5000/app:dev", "localhost:5000", "app", "dev", ""},
		{"library/redis:7", "docker.io", "library/redis", "7", ""},
		{"ghcr.io/a/b/c:1.0.0@sha256:ff", "ghcr.io", "a/b/c", "1.0.0", "sha256:ff"},
		{"  nginx:1  ", "docker.io", "library/nginx", "1", ""},
	} {
		r, err := Parse(tc.image)
		if err != nil || r.Registry != tc.registry || r.Repository != tc.repo || r.Tag != tc.tag || r.Digest != tc.digest {
			t.Errorf("Parse(%q) = %+v, %v", tc.image, r, err)
		}
	}
	for _, image := range []string{"a@b@c", "ghcr.io/../x", "ghcr.io/", ":tag"} {
		if _, err := Parse(image); err == nil {
			t.Errorf("Parse(%q) succeeded", image)
		}
	}
	r, _ := Parse("registry.example:5000/team/app:1.0.0@sha256:aa")
	if got := r.replaceTag("1.0.1"); got != "registry.example:5000/team/app:1.0.1" {
		t.Errorf("replaceTag = %s", got)
	}
	if got := r.DigestRef("sha256:bb"); got != "registry.example:5000/team/app@sha256:bb" {
		t.Errorf("DigestRef = %s", got)
	}
}

func TestNextLink(t *testing.T) {
	base := "https://reg.example/v2/acme/app"
	for _, tc := range []struct{ header, want string }{
		{`</v2/acme/app/tags/list?last=b&n=2>; rel="next"`, "https://reg.example/v2/acme/app/tags/list?last=b&n=2"},
		{`<https://reg.example/v2/acme/app/tags/list?last=c>; rel="next"`, "https://reg.example/v2/acme/app/tags/list?last=c"},
		{`<https://evil.example/steal>; rel="next"`, ""},
		{`<http://reg.example/x>; rel="next"`, ""},
		{`</x>; rel="prev"`, ""},
		{"", ""},
	} {
		if got := nextLink(tc.header, base); got != tc.want {
			t.Errorf("nextLink(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestWindows(t *testing.T) {
	mon := func(h int) time.Time { return time.Date(2026, 10, 19, h, 30, 0, 0, time.UTC) }
	for _, tc := range []struct {
		window     string
		days       []int
		start, end int
		at         time.Time
		want       bool
	}{
		{"", nil, 0, 0, mon(12), true},
		{"anytime", nil, 0, 0, mon(3), true},
		{"weekends", nil, 0, 0, mon(12), false},
		{"night", nil, 0, 0, mon(2), true},
		{"night", nil, 0, 0, mon(5), true},
		{"custom", []int{1}, 0, 24, mon(23), true},
		{"custom", []int{1}, 9, 10, mon(9), true},
		{"custom", []int{1}, 9, 10, mon(10), false},
		{"custom", []int{2}, 0, 24, mon(12), false},
		{"custom", []int{1, 1}, 0, 24, mon(12), false},
		{"custom", nil, 0, 24, mon(12), false},
		{"bogus", nil, 0, 0, mon(12), false},
		// 다른 시간대의 시각도 UTC로 본다
		{"custom", []int{1}, 9, 10, time.Date(2026, 10, 19, 18, 30, 0, 0, time.FixedZone("KST", 9*3600)), true},
	} {
		if got := InWindow(tc.window, tc.days, tc.start, tc.end, tc.at); got != tc.want {
			t.Errorf("InWindow(%s %v %d-%d, %s) = %v", tc.window, tc.days, tc.start, tc.end, tc.at, got)
		}
	}
	for _, w := range []string{"", "anytime", "weekends", "night"} {
		if ValidateWindow(w, nil, 0, 0) != nil {
			t.Errorf("ValidateWindow(%q) failed", w)
		}
	}
	if ValidateWindow("custom", []int{7}, 0, 1) == nil || ValidateWindow("custom", []int{0}, -1, 1) == nil || ValidateWindow("x", nil, 0, 0) == nil {
		t.Error("invalid windows accepted")
	}
	if !Enabled("patch") || !Enabled("minor") || Enabled("") || Enabled("major") {
		t.Error("Enabled")
	}
}

// A transport error on the authenticated retry must come back as an error, not a nil response.
func TestRegistryBearerRetryTransportError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"token":"registry-token"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer registry-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srvURL(r)+`/token",service="test",scope="repository:acme/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close() // drop the connection without a response
		}
	}))
	defer srv.Close()
	c := NewClient(nil)
	c.HTTP, c.base = srv.Client(), srv.URL
	r, _ := Parse("acme/app:latest")
	if _, err := c.ManifestDigest(context.Background(), r, r.Tag); err == nil {
		t.Fatal("ManifestDigest succeeded after the registry dropped the connection")
	}
}
