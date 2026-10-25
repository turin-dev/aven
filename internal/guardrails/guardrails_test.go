package guardrails

import (
	"errors"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

func ptr(s string) *string { return &s }

func TestGitHubOwner(t *testing.T) {
	for _, tc := range []struct {
		in, owner string
		ok        bool
	}{
		{"https://github.com/Acme/app", "acme", true},
		{"https://github.com/acme/app.git", "acme", true},
		{"https://x-access-token:secret@github.com/acme/app.git", "acme", true},
		{"http://www.github.com/acme/app/", "acme", true},
		{"git@github.com:Acme-Corp/app.git", "acme-corp", true},
		{"ssh://git@github.com/acme/app.git", "acme", true},
		{"https://gitlab.com/acme/app", "", false},
		{"https://github.com.evil.com/acme/app", "", false},
		{"https://evil.com/github.com/acme/app", "", false},
		{"git@evil.com:acme/app.git", "", false},
		{"https://github.com/acme", "", false},
		{"https://github.com//app", "", false},
		{"file:///github.com/acme/app", "", false},
		{"acme/app", "", false},
		{"", "", false},
	} {
		owner, ok := GitHubOwner(tc.in)
		if owner != tc.owner || ok != tc.ok {
			t.Errorf("GitHubOwner(%q) = %q, %v; want %q, %v", tc.in, owner, ok, tc.owner, tc.ok)
		}
	}
}

func TestOfficialDatabaseImage(t *testing.T) {
	for kind, db := range catalog.Databases {
		if !OfficialDatabaseImage(kind, db.Image) {
			t.Errorf("%s: official image %s rejected", kind, db.Image)
		}
		if !OfficialDatabaseImage(kind, "docker.io/library/"+db.Image) {
			t.Errorf("%s: docker.io/library prefix rejected", kind)
		}
	}
	if !OfficialDatabaseImage("postgres", "postgres:16") || !OfficialDatabaseImage("postgres", "postgres@sha256:abc") {
		t.Error("other tags of the official image should be allowed")
	}
	if !OfficialDatabaseImage(catalog.BucketKind, catalog.BucketImage) {
		t.Error("bucket image rejected")
	}
	for _, bad := range [][2]string{{"postgres", "evil/postgres:17"}, {"postgres", "mysql:8.4"}, {"postgres", "ghcr.io/x/postgres"}, {"nope", "postgres:17"}, {"redis", "redis-evil:7"}} {
		if OfficialDatabaseImage(bad[0], bad[1]) {
			t.Errorf("%s %s accepted", bad[0], bad[1])
		}
	}
}

func TestCheck(t *testing.T) {
	off := &store.AccessControlSettings{ApprovedGitHubOrgs: []string{"acme"}}
	on := &store.AccessControlSettings{RestrictDeploymentSources: true, ApprovedGitHubOrgs: []string{"acme", "Platform"}}
	cases := []struct {
		name string
		src  Source
		ok   bool
	}{
		{"approved repo", Source{Type: "repo", RepoURL: ptr("https://github.com/acme/app")}, true},
		{"approved repo case", Source{Type: "repo", RepoURL: ptr("git@github.com:platform/app.git")}, true},
		{"personal repo", Source{Type: "repo", RepoURL: ptr("https://github.com/someone/app")}, false},
		{"non-github repo", Source{Type: "repo", RepoURL: ptr("https://gitlab.com/acme/app")}, false},
		{"repo without source", Source{Type: "repo"}, true},
		{"image", Source{Type: "image", Image: ptr("nginx:alpine")}, false},
		{"empty image service", Source{Type: "image", Image: ptr(" ")}, true},
		{"function", Source{Type: "function"}, false},
		{"database official", Source{Type: "database", DatabaseKind: "postgres", Image: ptr("postgres:17-alpine")}, true},
		{"database swapped image", Source{Type: "database", DatabaseKind: "postgres", Image: ptr("attacker/miner:latest")}, false},
		{"upload of approved repo service", Source{Type: "repo", RepoURL: ptr("https://github.com/acme/app"), Uploaded: true}, false},
		{"unknown", Source{Type: "weird", Image: ptr("x")}, false},
	}
	for _, tc := range cases {
		if err := Check(off, tc.src); err != nil {
			t.Errorf("%s: policy off but got %v", tc.name, err)
		}
		if err := Check(nil, tc.src); err != nil {
			t.Errorf("%s: nil policy but got %v", tc.name, err)
		}
		err := Check(on, tc.src)
		if (err == nil) != tc.ok {
			t.Errorf("%s: got %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && !errors.Is(err, store.ErrDeploymentSourceRestricted) {
			t.Errorf("%s: error does not wrap ErrDeploymentSourceRestricted: %v", tc.name, err)
		}
	}
}

func TestSourceOf(t *testing.T) {
	kind := "redis"
	sp := &store.Spec{Image: ptr("redis:7.4-alpine")}
	src := SourceOf(&store.Service{SourceType: "database", DatabaseKind: &kind}, sp)
	if src.Type != "database" || src.DatabaseKind != "redis" || src.Image != sp.Image {
		t.Errorf("SourceOf: %+v", src)
	}
}
