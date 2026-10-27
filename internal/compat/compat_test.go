package compat

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnvPrefersAven(t *testing.T) {
	ResetWarnings()
	var logs []string
	prev := Warnf
	Warnf = func(format string, args ...any) { logs = append(logs, format) }
	t.Cleanup(func() { Warnf = prev; ResetWarnings() })

	getenv := func(k string) string {
		switch k {
		case "AVEN_PUBLIC_URL":
			return "https://new.example"
		case "KEEL_PUBLIC_URL":
			return "https://old.example"
		default:
			return ""
		}
	}
	if got := From(getenv, "PUBLIC_URL"); got != "https://new.example" {
		t.Fatalf("AVEN_ wins, got %q", got)
	}
	if len(logs) != 0 {
		t.Fatalf("no warning when the new name is set: %v", logs)
	}
}

func TestEnvFallsBackToKeelOnce(t *testing.T) {
	ResetWarnings()
	var logs []string
	var mu sync.Mutex
	prev := Warnf
	Warnf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, format)
		mu.Unlock()
	}
	t.Cleanup(func() { Warnf = prev; ResetWarnings() })

	getenv := func(k string) string {
		if k == "KEEL_ADMIN_PASSWORD" {
			return "legacy-secret"
		}
		return ""
	}
	if got := From(getenv, "ADMIN_PASSWORD"); got != "legacy-secret" {
		t.Fatalf("fallback got %q", got)
	}
	if got := From(getenv, "ADMIN_PASSWORD"); got != "legacy-secret" {
		t.Fatalf("second read got %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "%s") {
		t.Fatalf("one warning, got %v", logs)
	}
}

func TestLookupEmptyAvenDoesNotFallBack(t *testing.T) {
	ResetWarnings()
	t.Setenv("AVEN_DEBUG", "")
	t.Setenv("KEEL_DEBUG", "1")
	v, ok := Lookup("DEBUG")
	if !ok || v != "" {
		t.Fatalf("empty AVEN_DEBUG is present and wins, got %q ok=%v", v, ok)
	}
}

func TestLookupKeelFallback(t *testing.T) {
	ResetWarnings()
	var logs []string
	prev := Warnf
	Warnf = func(format string, args ...any) { logs = append(logs, format) }
	t.Cleanup(func() { Warnf = prev; ResetWarnings() })

	t.Setenv("KEEL_MESH", "off")
	os.Unsetenv("AVEN_MESH")
	v, ok := Lookup("MESH")
	if !ok || v != "off" {
		t.Fatalf("Lookup fallback got %q ok=%v", v, ok)
	}
	if len(logs) != 1 {
		t.Fatalf("warning count %d", len(logs))
	}
}

func TestDBPathFallsBack(t *testing.T) {
	ResetWarnings()
	var logs []string
	prev := Warnf
	Warnf = func(format string, args ...any) { logs = append(logs, format) }
	t.Cleanup(func() { Warnf = prev; ResetWarnings() })

	dir := t.TempDir()
	if got := DBPath(dir); got != filepath.Join(dir, DBName) {
		t.Fatalf("new install uses %s, got %s", DBName, got)
	}
	if err := os.WriteFile(filepath.Join(dir, LegacyDBName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DBPath(dir); got != filepath.Join(dir, LegacyDBName) {
		t.Fatalf("existing keel.db, got %s", got)
	}
	if len(logs) != 1 {
		t.Fatalf("warning count %d", len(logs))
	}
	if err := os.WriteFile(filepath.Join(dir, DBName), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DBPath(dir); got != filepath.Join(dir, DBName) {
		t.Fatalf("aven.db wins, got %s", got)
	}
}

func TestResolvePath(t *testing.T) {
	ResetWarnings()
	var logs []string
	prev := Warnf
	Warnf = func(format string, args ...any) { logs = append(logs, format) }
	t.Cleanup(func() { Warnf = prev; ResetWarnings() })

	dir := t.TempDir()
	next := filepath.Join(dir, "aven")
	prevDir := filepath.Join(dir, "keel")
	if err := os.Mkdir(prevDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDir(next, prevDir); got != prevDir {
		t.Fatalf("legacy dir, got %s", got)
	}
	if len(logs) != 1 {
		t.Fatalf("warning count %d", len(logs))
	}
	if err := os.Mkdir(next, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDir(next, prevDir); got != next {
		t.Fatalf("new dir wins, got %s", got)
	}
}

func TestInjectedAlias(t *testing.T) {
	vars := map[string]string{"AVEN_SERVICE_NAME": "api", "KEEL_PORT": "nope", "PORT": "8080"}
	InjectedAlias(vars)
	if vars["KEEL_SERVICE_NAME"] != "api" || vars["PORT"] != "8080" || vars["KEEL_PORT"] != "nope" {
		t.Fatalf("%v", vars)
	}
	vars["KEEL_SERVICE_NAME"] = "user"
	InjectedAlias(vars)
	if vars["KEEL_SERVICE_NAME"] != "user" {
		t.Fatalf("user value overwritten: %v", vars)
	}
}

func TestAltResource(t *testing.T) {
	if AltResource("aven-proxy") != "keel-proxy" || AltResource("keel-vol-ab") != "aven-vol-ab" || AltResource("aven") != "keel" || AltResource("other") != "" {
		t.Fatal(AltResource("aven-proxy"), AltResource("keel-vol-ab"), AltResource("aven"), AltResource("other"))
	}
}

func TestCanonicalEnv(t *testing.T) {
	if CanonicalEnv("KEEL_DATA") != "AVEN_DATA" || CanonicalEnv("AVEN_DATA") != "AVEN_DATA" || LegacyEnv("AVEN_DATA") != "KEEL_DATA" {
		t.Fatal(CanonicalEnv("KEEL_DATA"), LegacyEnv("AVEN_DATA"))
	}
}
