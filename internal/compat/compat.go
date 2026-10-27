// Package compat reads Aven settings while still accepting the Keel names
// existing installs already set. The new name wins. The first time a process
// uses an old name, it logs one deprecation warning for that name.
package compat

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	EnvPrefix       = "AVEN_"
	LegacyEnvPrefix = "KEEL_"

	// Resource prefixes. New Docker objects use Prefix; objects created
	// before the rename still use LegacyPrefix and keep working.
	Prefix       = "aven"
	LegacyPrefix = "keel"

	DNSSuffix       = "aven.internal"
	LegacyDNSSuffix = "keel.internal"

	DataDir       = "/var/lib/aven"
	LegacyDataDir = "/var/lib/keel"
	DBName        = "aven.db"
	LegacyDBName  = "keel.db"

	ConfDir        = "/etc/aven"
	LegacyConfDir  = "/etc/keel"
	EnvFile        = ConfDir + "/aven.env"
	LegacyEnvFile  = LegacyConfDir + "/aven.env"
	LegacyEnvFile2 = LegacyConfDir + "/keel.env"

	LibDir       = "/usr/local/lib/aven"
	LegacyLibDir = "/usr/local/lib/keel"

	AgentDataDir       = "/var/lib/aven-agent"
	LegacyAgentDataDir = "/var/lib/keel-agent"
	AgentConfDir       = "/etc/aven-agent"
	LegacyAgentConfDir = "/etc/keel-agent"

	IACDir       = ".aven"
	LegacyIACDir = ".keel"
)

// Warnf is the deprecation logger. Tests replace it.
var Warnf = log.Printf

var (
	warnMu sync.Mutex
	warned = map[string]bool{}
)

// ResetWarnings clears the once-only set. Tests use it.
func ResetWarnings() {
	warnMu.Lock()
	warned = map[string]bool{}
	warnMu.Unlock()
}

// WarnOnce logs format once per key.
func WarnOnce(key, format string, args ...any) {
	warnMu.Lock()
	if warned[key] {
		warnMu.Unlock()
		return
	}
	warned[key] = true
	warnMu.Unlock()
	Warnf("deprecated: "+format, args...)
}

func envName(suffix string) string    { return EnvPrefix + suffix }
func legacyName(suffix string) string { return LegacyEnvPrefix + suffix }

// From reads AVEN_<suffix> from getenv, then KEEL_<suffix>. An empty value
// counts as unset, matching os.Getenv. getenv is usually os.Getenv; tests and
// env-file views pass their own lookup.
func From(getenv func(string) string, suffix string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	next, prev := envName(suffix), legacyName(suffix)
	if v := getenv(next); v != "" {
		return v
	}
	if v := getenv(prev); v != "" {
		WarnOnce(prev, "%s is set; use %s instead (the old name still works)", prev, next)
		return v
	}
	return ""
}

// Env reads AVEN_<suffix>, then KEEL_<suffix>, from the process environment.
func Env(suffix string) string { return From(os.Getenv, suffix) }

// EnvDefault is Env, or def when both names are unset.
func EnvDefault(suffix, def string) string {
	if v := Env(suffix); v != "" {
		return v
	}
	return def
}

// Lookup reports whether AVEN_<suffix> or KEEL_<suffix> is present, even when
// the value is empty. The new name wins when it is present.
func Lookup(suffix string) (string, bool) {
	next, prev := envName(suffix), legacyName(suffix)
	if v, ok := os.LookupEnv(next); ok {
		return v, true
	}
	if v, ok := os.LookupEnv(prev); ok {
		WarnOnce(prev, "%s is set; use %s instead (the old name still works)", prev, next)
		return v, true
	}
	return "", false
}

// CanonicalEnv rewrites a KEEL_* key to AVEN_*. Other keys are unchanged.
func CanonicalEnv(key string) string {
	if strings.HasPrefix(key, LegacyEnvPrefix) {
		return EnvPrefix + strings.TrimPrefix(key, LegacyEnvPrefix)
	}
	return key
}

// LegacyEnv is the KEEL_* spelling of an AVEN_* key (or of a bare suffix).
func LegacyEnv(key string) string {
	switch {
	case strings.HasPrefix(key, EnvPrefix):
		return LegacyEnvPrefix + strings.TrimPrefix(key, EnvPrefix)
	case strings.HasPrefix(key, LegacyEnvPrefix):
		return key
	default:
		return LegacyEnvPrefix + key
	}
}

// LookupEither looks up key, then its KEEL_/AVEN_ twin. key may be either spelling.
func LookupEither(getenv func(string) (string, bool), key string) (string, bool) {
	if getenv == nil {
		getenv = os.LookupEnv
	}
	canon := CanonicalEnv(key)
	if v, ok := getenv(canon); ok {
		return v, true
	}
	prev := LegacyEnv(canon)
	if prev != canon {
		if v, ok := getenv(prev); ok {
			WarnOnce(prev, "%s is set; use %s instead (the old name still works)", prev, canon)
			return v, true
		}
	}
	if key != canon {
		if v, ok := getenv(key); ok {
			return v, true
		}
	}
	return "", false
}

// DefaultDataDir is the data directory when AVEN_DATA and KEEL_DATA are unset.
// An existing Keel directory is used until the operator moves it.
func DefaultDataDir() string {
	if hasState(LegacyDataDir) && !hasState(DataDir) {
		WarnOnce("path:"+LegacyDataDir, "data directory %s is in use; set AVEN_DATA=%s to move it (the old path still works)", LegacyDataDir, DataDir)
		return LegacyDataDir
	}
	return DataDir
}

func hasState(dir string) bool {
	for _, name := range []string{DBName, LegacyDBName, "master.key"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// DBPath is the SQLite file inside dataDir. An existing keel.db is opened
// until an aven.db exists beside it.
func DBPath(dataDir string) string {
	next := filepath.Join(dataDir, DBName)
	prev := filepath.Join(dataDir, LegacyDBName)
	if _, err := os.Stat(next); err == nil {
		return next
	}
	if _, err := os.Stat(prev); err == nil {
		WarnOnce("db:"+prev, "database %s is in use; %s is the new name (the old file still works)", prev, next)
		return prev
	}
	return next
}

// ResolvePath returns prefer when it exists, otherwise the first existing
// legacy path (with one warning). When none exist, prefer is returned so
// callers can create it.
func ResolvePath(prefer string, legacy ...string) string {
	if _, err := os.Stat(prefer); err == nil {
		return prefer
	}
	for _, p := range legacy {
		if st, err := os.Stat(p); err == nil {
			kind := "file"
			if st.IsDir() {
				kind = "directory"
			}
			WarnOnce("path:"+p, "%s %s is in use; %s is the new path (the old path still works)", kind, p, prefer)
			return p
		}
	}
	return prefer
}

// ResolveDir is ResolvePath for directories that should be created at prefer
// when neither side exists.
func ResolveDir(prefer string, legacy ...string) string { return ResolvePath(prefer, legacy...) }

// DefaultEnvFile is the server env file. Existing Keel paths win when the
// new path has not been created yet.
func DefaultEnvFile() string {
	return ResolvePath(EnvFile, LegacyEnvFile, LegacyEnvFile2)
}

// DefaultLibDir is the directory of agent and CLI downloads.
func DefaultLibDir() string { return ResolveDir(LibDir, LegacyLibDir) }

// DefaultAgentDataDir is the agent data directory.
func DefaultAgentDataDir() string { return ResolveDir(AgentDataDir, LegacyAgentDataDir) }

// DefaultAgentConfDir is the agent env-file directory.
func DefaultAgentConfDir() string { return ResolveDir(AgentConfDir, LegacyAgentConfDir) }

// AltResource swaps an aven- name with its keel- name (and the bare network
// names "aven" and "keel"). Other names return "".
func AltResource(name string) string {
	switch {
	case strings.HasPrefix(name, Prefix+"-"):
		return LegacyPrefix + "-" + strings.TrimPrefix(name, Prefix+"-")
	case strings.HasPrefix(name, LegacyPrefix+"-"):
		return Prefix + "-" + strings.TrimPrefix(name, LegacyPrefix+"-")
	case name == Prefix:
		return LegacyPrefix
	case name == LegacyPrefix:
		return Prefix
	default:
		return ""
	}
}

// NoteLegacyResource logs once per class when an existing install still uses
// a keel resource name. class is a stable key such as "container-prefix".
func NoteLegacyResource(class, old, new string) {
	WarnOnce("resource:"+class, "%s is in use; %s is the new name (the old name still works)", old, new)
}

// PrivateHost is <label>.<env>.aven.internal.
func PrivateHost(label, env string) string {
	return label + "." + env + "." + DNSSuffix
}

// LegacyPrivateHost is <label>.<env>.keel.internal.
func LegacyPrivateHost(label, env string) string {
	return label + "." + env + "." + LegacyDNSSuffix
}

// NoteLegacyDNS logs once when a keel.internal name is still in use.
func NoteLegacyDNS() {
	NoteLegacyResource("private-dns", "suffix ."+LegacyDNSSuffix, "suffix ."+DNSSuffix)
}

// NamePattern is a regex fragment matching either resource prefix.
func NamePattern() string { return "(?:" + Prefix + "|" + LegacyPrefix + ")" }

// Describe returns a short operator-facing note used in error strings.
func Describe(suffix string) string {
	return fmt.Sprintf("%s (or %s)", envName(suffix), legacyName(suffix))
}

// InjectedAlias copies AVEN_* entries onto the matching KEEL_* keys when the
// caller did not already set the old name. Existing templates and processes
// that still read KEEL_SERVICE_NAME and the other injected names keep working.
func InjectedAlias(vars map[string]string) {
	if vars == nil {
		return
	}
	for k, v := range vars {
		if !strings.HasPrefix(k, EnvPrefix) {
			continue
		}
		prev := LegacyEnvPrefix + strings.TrimPrefix(k, EnvPrefix)
		if _, exists := vars[prev]; !exists {
			vars[prev] = v
		}
	}
}
