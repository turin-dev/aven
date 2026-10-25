package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifySignature("s3cret", body, good) {
		t.Error("valid signature rejected")
	}
	for _, bad := range []string{"", "sha256=00", "sha1=" + good[7:], good[:len(good)-2] + "00"} {
		if VerifySignature("s3cret", body, bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if VerifySignature("", body, good) {
		t.Error("empty secret must reject everything")
	}
	if VerifySignature("s3cret", append(body, ' '), good) {
		t.Error("modified body accepted")
	}
}

func TestNormalizeRepo(t *testing.T) {
	want := "github.com/turin-dev/aven"
	for _, u := range []string{"https://github.com/turin-dev/aven", "https://github.com/Turin-Dev/aven.git", "git@github.com:turin-dev/aven.git",
		"ssh://git@github.com/turin-dev/aven", "https://x-access-token:abc@github.com/turin-dev/aven.git", "http://github.com/turin-dev/aven/"} {
		if got := NormalizeRepo(u); got != want {
			t.Errorf("%s → %s", u, got)
		}
	}
	if NormalizeRepo("file:///tmp/keel-e2e/good") != "/tmp/keel-e2e/good" {
		t.Error("file url")
	}
}

func TestPendingChanges(t *testing.T) {
	sp := store.Spec{}
	sp.Defaults()
	d := &store.Deployment{ReqSpec: sp, VarsRaw: map[string]string{"A": "1", "B": "2"}}
	in := &store.Instance{Spec: sp, Variables: map[string]string{"A": "1", "B": "2"}}
	if got := PendingChanges(in, d); len(got) != 0 {
		t.Fatalf("no changes expected: %v", got)
	}
	in.Variables = map[string]string{"A": "changed", "C": "new"}
	in.Replicas = 3
	cmd := "node x.js"
	in.StartCommand = &cmd
	in.Domains = []store.Domain{{Host: "a.example.com", Kind: "custom"}}
	in.TCPProxy = &store.TCPProxy{ContainerPort: 5432, PublicPort: 20001}
	want := []string{"replicas", "start_command", "variables.A", "variables.B", "variables.C"}
	if got := PendingChanges(in, d); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestVarSource(t *testing.T) {
	user := map[string]string{"PLAIN": "x", "SH": "${{ shared.REGION }}", "REF": "${{postgres.DATABASE_URL}}", "MIX": "a-${{shared.X}}"}
	for k, want := range map[string]string{"PLAIN": "user", "SH": "shared", "REF": "reference", "MIX": "reference", "PORT": "injected"} {
		if got := VarSource(user, k); got != want {
			t.Errorf("%s: %s, want %s", k, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	cases := map[string]string{nameFromRepo("https://github.com/example-org/My_App.git"): "My_App", nameFromImage("ghcr.io/umami-software/umami:postgresql-latest"): "umami",
		nameFromImage("traefik/whoami:v1.11"): "whoami", nameFromImage("localhost:5000/app"): "app"}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
	if sanitizeName("My_App..v2") != "my-app-v2" {
		t.Error(sanitizeName("My_App..v2"))
	}
}
