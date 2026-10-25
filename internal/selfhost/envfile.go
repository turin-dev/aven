package selfhost

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// 서버 설정(env 파일): 대시보드에서 aven-server 자신의 KEEL_* 설정을 읽고 고친다.
// 파일은 docker --env-file과 systemd EnvironmentFile이 함께 읽으므로 KEY=VALUE 한 줄씩, 따옴표 없이 쓴다.
// 주석과 줄 순서는 그대로 두고 바뀐 줄만 고친다. 새 키는 끝에 붙인다.

// ConfigKey는 대시보드가 아는 설정 키다. 설명 문구는 웹 UI가 이름으로 고른다.
type ConfigKey struct {
	Name   string
	Group  string // general | network | github | smtp | advanced | fixed
	Secret bool   // 값을 내보내지 않는다(설정 여부만)
	// Fixed는 대시보드에서 바꿀 수 없는 키다: 데이터 경로나 self 컨테이너가 덮어쓰는 값처럼 바꾸면 서버가 자기 파일을 잃는다.
	Fixed bool
}

// ConfigKeys는 aven-server가 읽는 설정이다(cmd/aven-server, internal의 os.Getenv("KEEL_…")).
var ConfigKeys = []ConfigKey{
	{Name: "KEEL_PUBLIC_URL", Group: "general"},
	{Name: "KEEL_BASE_DOMAIN", Group: "general"},
	{Name: "KEEL_ACME_EMAIL", Group: "general"},
	{Name: "KEEL_SUPERADMINS", Group: "general"},
	{Name: "KEEL_ADMIN_PASSWORD", Group: "general", Secret: true},
	{Name: "KEEL_API_TOKEN", Group: "general", Secret: true},
	{Name: "KEEL_DEBUG", Group: "general"},

	{Name: "KEEL_LISTEN", Group: "network"},
	{Name: "KEEL_LISTEN_UNIX", Group: "network"},
	{Name: "KEEL_TRUSTED_PROXIES", Group: "network"},
	{Name: "KEEL_INSECURE_COOKIE", Group: "network"},
	{Name: "KEEL_TLS", Group: "network"},
	{Name: "KEEL_TLS_CERT", Group: "network"},
	{Name: "KEEL_TLS_KEY", Group: "network"},
	{Name: "KEEL_HTTP_PORT", Group: "network"},
	{Name: "KEEL_HTTPS_PORT", Group: "network"},
	{Name: "KEEL_PROXY_BIND", Group: "network"},
	{Name: "KEEL_PUBLIC_IPV4", Group: "network"},
	{Name: "KEEL_PUBLIC_IPV6", Group: "network"},
	{Name: "KEEL_IPV6_POOL", Group: "network"},
	{Name: "KEEL_IPV6_IFACE", Group: "network"},
	{Name: "KEEL_INGRESS_MODE", Group: "network"},
	{Name: "KEEL_INGRESS_HOSTNAME", Group: "network"},
	{Name: "KEEL_TCP_PROXY_DOMAIN", Group: "network"},
	{Name: "KEEL_TCP_PORT_MIN", Group: "network"},
	{Name: "KEEL_TCP_PORT_MAX", Group: "network"},
	{Name: "KEEL_PRIVATE_ADDRESS", Group: "network"},
	{Name: "KEEL_MESH", Group: "network"},
	{Name: "KEEL_MESH_PORT", Group: "network"},
	{Name: "KEEL_EXTERNAL_UPSTREAMS", Group: "network"},
	{Name: "KEEL_PROXY_NETWORKS", Group: "network"},
	{Name: "KEEL_SELF_ROUTE", Group: "network"},
	{Name: "KEEL_NETWORK_LOGS", Group: "network"},

	{Name: "KEEL_GITHUB_CLIENT_ID", Group: "github"},
	{Name: "KEEL_GITHUB_CLIENT_SECRET", Group: "github", Secret: true},
	{Name: "KEEL_GITHUB_WEBHOOK_SECRET", Group: "github", Secret: true},
	{Name: "KEEL_GITHUB_TOKEN", Group: "github", Secret: true},
	{Name: "KEEL_GITHUB_API_URL", Group: "github"},
	{Name: "KEEL_ALLOW_FILE_REPO", Group: "github"},

	{Name: "KEEL_SMTP_HOST", Group: "smtp"},
	{Name: "KEEL_SMTP_PORT", Group: "smtp"},
	{Name: "KEEL_SMTP_USERNAME", Group: "smtp"},
	{Name: "KEEL_SMTP_PASSWORD", Group: "smtp", Secret: true},
	{Name: "KEEL_SMTP_FROM", Group: "smtp"},
	{Name: "KEEL_SMTP_TLS", Group: "smtp"},

	{Name: "KEEL_OTLP_ENDPOINT", Group: "advanced"},
	{Name: "KEEL_OTLP_LISTEN", Group: "advanced"},
	{Name: "KEEL_TRACE_RETENTION", Group: "advanced"},
	{Name: "KEEL_RAILPACK_FRONTEND", Group: "advanced"},
	{Name: "KEEL_IAC_RUNNER", Group: "advanced"},
	{Name: "KEEL_CADDY_BIN", Group: "advanced"},
	{Name: "KEEL_NOTIFY_ALLOW_PRIVATE", Group: "advanced"},
	{Name: "KEEL_AGENT_LEGACY_BEARER", Group: "advanced"},
	{Name: "KEEL_SELF_REPO", Group: "advanced"},
	{Name: "KEEL_SELF_BRANCH", Group: "advanced"},
	{Name: "KEEL_SELF_PROJECT", Group: "advanced"},
	{Name: "KEEL_SELF_UPDATE_INTERVAL", Group: "advanced"},

	{Name: "KEEL_DATA", Group: "fixed", Fixed: true},
	{Name: "KEEL_GATEWAY_SECRET_FILE", Group: "fixed", Fixed: true},
	{Name: "KEEL_AGENT_BIN_DIR", Group: "fixed", Fixed: true},
	{Name: "KEEL_RAILPACK_BIN", Group: "fixed", Fixed: true},
	{Name: "KEEL_SELF_ENV_FILE", Group: "fixed", Fixed: true},
	{Name: "KEEL_SELF_SERVICE", Group: "fixed", Fixed: true},
}

// secretPattern은 카탈로그에 없는 키 중 이름으로 비밀값을 짐작한다.
var secretPattern = regexp.MustCompile(`(PASSWORD|SECRET|TOKEN|_KEY$)`)

var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func lookupKey(name string) ConfigKey {
	for _, k := range ConfigKeys {
		if k.Name == name {
			return k
		}
	}
	return ConfigKey{Name: name, Group: "other", Secret: secretPattern.MatchString(name)}
}

// EnvFile은 읽어 들인 env 파일이다. lines는 원문 줄(주석 포함)이다.
type EnvFile struct {
	Path  string
	lines []string
	Mode  os.FileMode
}

func ReadEnvFile(path string) (*EnvFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(string(b), "\n")
	f := &EnvFile{Path: path, Mode: st.Mode().Perm()}
	if s != "" {
		f.lines = strings.Split(s, "\n")
	}
	return f, nil
}

func parseLine(line string) (key, value string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	k, v, ok := strings.Cut(t, "=")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(k, "export ")), v, true
}

// Values는 키 → 값이다(같은 키가 여러 번이면 마지막 값).
func (f *EnvFile) Values() map[string]string {
	out := map[string]string{}
	for _, l := range f.lines {
		if k, v, ok := parseLine(l); ok {
			out[k] = v
		}
	}
	return out
}

// Bytes는 파일 내용이다.
func (f *EnvFile) Bytes() []byte {
	if len(f.lines) == 0 {
		return nil
	}
	return []byte(strings.Join(f.lines, "\n") + "\n")
}

// ErrConfig는 받아들일 수 없는 설정 변경이다(400).
var ErrConfig = errors.New("서버 설정")

// Apply는 키를 바꾸거나(set) 지운다(unset). 바꿀 수 없는 키나 형식이 잘못된 값은 거부한다.
func (f *EnvFile) Apply(set map[string]string, unset []string) error {
	for k, v := range set {
		if err := validateEntry(k, v); err != nil {
			return err
		}
	}
	for _, k := range unset {
		if !keyPattern.MatchString(k) {
			return fmt.Errorf("%w: 잘못된 키 %q", ErrConfig, k)
		}
		if lookupKey(k).Fixed {
			return fmt.Errorf("%w: %s는 대시보드에서 바꿀 수 없습니다", ErrConfig, k)
		}
		if k == "KEEL_ADMIN_PASSWORD" {
			return fmt.Errorf("%w: KEEL_ADMIN_PASSWORD는 지울 수 없습니다(서버가 시작하지 못합니다)", ErrConfig)
		}
	}
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	done := map[string]bool{}
	out := f.lines[:0:0]
	for _, l := range f.lines {
		k, _, ok := parseLine(l)
		switch {
		case !ok:
			out = append(out, l)
		case drop[k]:
		case hasKey(set, k):
			if !done[k] {
				out = append(out, k+"="+set[k])
				done[k] = true
			}
		default:
			out = append(out, l)
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		if !done[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+set[k])
	}
	f.lines = out
	return nil
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

func validateEntry(k, v string) error {
	if !keyPattern.MatchString(k) {
		return fmt.Errorf("%w: 잘못된 키 %q(대문자, 숫자, _만)", ErrConfig, k)
	}
	if lookupKey(k).Fixed {
		return fmt.Errorf("%w: %s는 대시보드에서 바꿀 수 없습니다", ErrConfig, k)
	}
	if strings.ContainsAny(v, "\n\r\x00") {
		return fmt.Errorf("%w: %s 값에 줄바꿈을 넣을 수 없습니다", ErrConfig, k)
	}
	if v != strings.TrimSpace(v) {
		return fmt.Errorf("%w: %s 값의 앞뒤 공백을 빼세요", ErrConfig, k)
	}
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return fmt.Errorf("%w: %s 값에서 따옴표를 빼세요", ErrConfig, k)
	}
	switch k {
	case "KEEL_ADMIN_PASSWORD":
		if len(v) < 8 {
			return fmt.Errorf("%w: KEEL_ADMIN_PASSWORD는 8자 이상이어야 합니다", ErrConfig)
		}
	case "KEEL_PUBLIC_URL":
		if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			return fmt.Errorf("%w: KEEL_PUBLIC_URL은 http:// 또는 https://로 시작해야 합니다", ErrConfig)
		}
	}
	return nil
}

// Write는 파일을 같은 권한으로 원자적으로 바꾼다(같은 디렉터리의 임시 파일 → rename).
func (f *EnvFile) Write() error { return writeFileAtomic(f.Path, f.Bytes(), f.Mode) }

// Writable은 이 프로세스가 env 파일을 고칠 수 있는지다(self 컨테이너에 읽기 전용으로 마운트됐으면 false).
func Writable(path string) bool {
	return syscall.Access(filepath.Dir(path), 2) == nil && syscall.Access(path, 2) == nil
}

// ConfigEntry는 대시보드에 보여 줄 키 하나다.
type ConfigEntry struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Secret bool   `json:"secret"`
	Fixed  bool   `json:"fixed"`
	Set    bool   `json:"set"`             // 파일에 있다
	Value  string `json:"value,omitempty"` // 비밀값이면 비운다
	// Pending은 파일 값이 지금 도는 서버의 값과 다르다는 뜻이다(다음 배포에 적용된다).
	Pending bool `json:"pending"`
}

// Entries는 카탈로그 키(파일에 없어도)와 파일에만 있는 키를 돌려준다. running은 지금 서버 프로세스의 환경이다.
func (f *EnvFile) Entries(running func(string) (string, bool)) []ConfigEntry {
	vals := f.Values()
	seen := map[string]bool{}
	var out []ConfigEntry
	add := func(k ConfigKey) {
		v, set := vals[k.Name]
		e := ConfigEntry{Name: k.Name, Group: k.Group, Secret: k.Secret, Fixed: k.Fixed, Set: set}
		if set && !k.Secret {
			e.Value = v
		}
		// self 컨테이너가 덮어쓰는 키(Fixed)는 프로세스 값이 파일과 달라도 대기 중인 변경이 아니다.
		if !k.Fixed {
			cur, ok := running(k.Name)
			e.Pending = ok != set || cur != v
		}
		out = append(out, e)
		seen[k.Name] = true
	}
	for _, k := range ConfigKeys {
		add(k)
	}
	var extra []string
	for k := range vals {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		add(lookupKey(k))
	}
	return out
}

// ---------------- 마지막으로 정상 동작한 설정 ----------------

// goodEnvName은 마지막으로 서버가 정상으로 뜬 env 파일 사본이다(KEEL_DATA 아래). 교체가 실패하면 env 파일을 이것으로 되돌린다.
const goodEnvName = "selfhost-env.good"

func goodEnvPath(dataDir string) string { return filepath.Join(dataDir, goodEnvName) }

// SaveGoodEnv는 env 파일을 정상 사본으로 저장한다.
func SaveGoodEnv(dataDir, envFile string) error {
	b, err := os.ReadFile(envFile)
	if err != nil {
		return err
	}
	return writeFileAtomic(goodEnvPath(dataDir), b, 0o600)
}

// HasGoodEnv는 정상 사본이 있는지다.
func HasGoodEnv(dataDir string) bool {
	_, err := os.Stat(goodEnvPath(dataDir))
	return err == nil
}

// RestoreGoodEnv는 env 파일이 정상 사본과 다르면 되돌린다. 되돌렸으면 true다.
func RestoreGoodEnv(dataDir, envFile string) (bool, error) {
	good, err := os.ReadFile(goodEnvPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cur, err := os.ReadFile(envFile)
	if err == nil && bytes.Equal(cur, good) {
		return false, nil
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(envFile); err == nil {
		mode = st.Mode().Perm()
	}
	if err := writeFileAtomic(envFile, good, mode); err != nil {
		return false, err
	}
	return true, nil
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aven-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
