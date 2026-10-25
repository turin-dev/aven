package node

import "testing"

func TestImageHostAndAuth(t *testing.T) {
	for img, want := range map[string]string{"nginx:alpine": "docker.io", "traefik/whoami:v1": "docker.io", "ghcr.io/a/b:1": "ghcr.io",
		"127.0.0.1:5005/x/y:1": "127.0.0.1:5005", "localhost/app": "localhost", "registry.example.com/app@sha256:ab": "registry.example.com"} {
		if got := ImageHost(img); got != want {
			t.Errorf("%s → %s want %s", img, got, want)
		}
	}
	auths := []RegistryAuth{{Host: "https://ghcr.io/", Username: "u"}, {Host: "index.docker.io", Username: "hub"}}
	if a := MatchAuth("ghcr.io/x/y", auths); a == nil || a.Username != "u" {
		t.Error("ghcr match")
	}
	if a := MatchAuth("nginx", auths); a == nil || a.Username != "hub" {
		t.Error("docker hub match")
	}
	if MatchAuth("quay.io/x", auths) != nil {
		t.Error("no match expected")
	}
}

func TestParseSize(t *testing.T) {
	for s, want := range map[string]int64{"0B": 0, "1.5kB": 1500, "2MiB": 2 << 20, " 3.3GB ": 3300000000} {
		if got := ParseSize(s); got != want {
			t.Errorf("%q → %d want %d", s, got, want)
		}
	}
}
