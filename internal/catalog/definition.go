package catalog

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// ---- 템플릿 정의 형식(JSON, version 1) ----
//
// 내장 템플릿(templates/*.json)과 사용자 템플릿이 같은 형식을 쓴다. 생성 함수(${{ secret(32) }} 등)와
// 입력(${{ inputs.KEY }})은 배포할 때 한 번 계산하고 정의에는 저장하지 않는다.

const (
	DefinitionVersion = 1
	// MaxDefinitionBytes는 정의 JSON 크기 상한이다.
	MaxDefinitionBytes = 256 << 10
	maxServices        = 20
	maxInputs          = 50
	maxVariables       = 200
)

// Definition은 템플릿 정의다.
type Definition struct {
	Version     int           `json:"version"`
	Key         string        `json:"key,omitempty"`
	Name        string        `json:"name,omitempty"`
	Description string        `json:"description,omitempty"`
	Icon        string        `json:"icon,omitempty"`
	Category    string        `json:"category,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Readme      string        `json:"readme,omitempty"`
	Primary     string        `json:"primary,omitempty"`
	Inputs      []Input       `json:"inputs,omitempty"`
	Services    []ServiceSpec `json:"services"`
}

// Input은 배포할 때 사용자에게 받는 값이다(${{ inputs.KEY }}).
type Input struct {
	Key         string `json:"key"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
}

// Source는 서비스 소스다. image, repo, database 중 정확히 하나만 쓴다.
type Source struct {
	Image    string      `json:"image,omitempty"`
	Repo     *RepoSource `json:"repo,omitempty"`
	Database string      `json:"database,omitempty"`
}

type RepoSource struct {
	URL     string `json:"url"`
	Branch  string `json:"branch,omitempty"`
	RootDir string `json:"root_dir,omitempty"`
}

type VolumeSpec struct {
	MountPath   string `json:"mount_path"`
	SizeLimitMB *int   `json:"size_limit_mb,omitempty"`
}

// DomainSpec은 generated domain이다(custom domain은 템플릿에 넣지 않는다).
type DomainSpec struct {
	Generate   bool `json:"generate"`
	TargetPort *int `json:"target_port,omitempty"`
}

type TCPProxySpec struct {
	ContainerPort int `json:"container_port"`
}

// Settings는 인스턴스 PATCH API(PATCH /api/services/{sid}/instances/{eid})와 같은 이름의 설정이다.
type Settings struct {
	Port                *int     `json:"port,omitempty"`
	StartCommand        *string  `json:"start_command,omitempty"`
	PreDeployCommand    *string  `json:"pre_deploy_command,omitempty"`
	Builder             *string  `json:"builder,omitempty"`
	DockerfilePath      *string  `json:"dockerfile_path,omitempty"`
	WatchPaths          []string `json:"watch_paths,omitempty"`
	AutoDeploy          *bool    `json:"auto_deploy,omitempty"`
	HealthcheckType     *string  `json:"healthcheck_type,omitempty"`
	HealthcheckPath     *string  `json:"healthcheck_path,omitempty"`
	HealthcheckTimeoutS *int     `json:"healthcheck_timeout_s,omitempty"`
	RestartPolicy       *string  `json:"restart_policy,omitempty"`
	RestartMaxRetries   *int     `json:"restart_max_retries,omitempty"`
	Replicas            *int     `json:"replicas,omitempty"`
	CronSchedule        *string  `json:"cron_schedule,omitempty"`
	SleepWhenIdle       *bool    `json:"sleep_when_idle,omitempty"`
	SleepAfterS         *int     `json:"sleep_after_s,omitempty"`
	MemoryLimitMB       *int     `json:"memory_limit_mb,omitempty"`
	CPULimit            *float64 `json:"cpu_limit,omitempty"`
	BuildSecrets        []string `json:"build_secrets,omitempty"`
	WaitForCI           *bool    `json:"wait_for_ci,omitempty"`
	OverlapSeconds      *int     `json:"overlap_seconds,omitempty"`
	DrainingSeconds     *int     `json:"draining_seconds,omitempty"`
	PrivatePorts        []int    `json:"private_ports,omitempty"`
}

// ServiceSpec은 템플릿 서비스 하나다. 설정(Settings)은 같은 객체에 평평하게 쓴다.
type ServiceSpec struct {
	Name      string            `json:"name"`
	Icon      string            `json:"icon,omitempty"`
	Source    Source            `json:"source"`
	Variables map[string]string `json:"variables,omitempty"`
	Volumes   []VolumeSpec      `json:"volumes,omitempty"`
	Domains   []DomainSpec      `json:"domains,omitempty"`
	TCPProxy  *TCPProxySpec     `json:"tcp_proxy,omitempty"`
	Canvas    *store.Canvas     `json:"canvas,omitempty"`
	Settings
	// HA는 고가용성 동반 템플릿 선언이다(ha.go).
	HA
}

// ---- 파싱/검증 ----

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)
	svcNameRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	iconRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
	varKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	inputKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	imageNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@+-]{0,254}$`)
	branchNameR = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	secretVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// tokenRe는 ${{ ... }} 하나다(참조, 입력, 함수).
	tokenRe = regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	refBody = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*$`)
	inBody  = regexp.MustCompile(`^\s*inputs\.([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	fnBody  = regexp.MustCompile(`^\s*([A-Za-z]+)\s*\((.*)\)\s*$`)
	// 함수 인자
	secretArgs = regexp.MustCompile(`^\s*(\d+)\s*(?:,\s*("(?:[^"\\]|\\.)*")\s*)?$`)
	intArgs    = regexp.MustCompile(`^\s*(-?\d+)\s*,\s*(-?\d+)\s*$`)
)

// DefaultSecretCharset은 secret(n)의 기본 문자 집합이다(헷갈리는 글자를 뺀 영숫자, store.RandomSecret과 같다).
const DefaultSecretCharset = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ValidationError는 정의의 문제 목록이다.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid template: " + strings.Join(e.Problems, "; ")
}

// ParseDefinition은 JSON 정의를 엄격하게 읽고 검증한다(모르는 필드, 크기 초과, 뒤에 붙은 데이터는 오류).
func ParseDefinition(b []byte) (*Definition, error) {
	if len(b) > MaxDefinitionBytes {
		return nil, &ValidationError{[]string{fmt.Sprintf("definition is larger than %d bytes", MaxDefinitionBytes)}}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var d Definition
	if err := dec.Decode(&d); err != nil {
		return nil, &ValidationError{[]string{"invalid json: " + err.Error()}}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &ValidationError{[]string{"invalid json: trailing data after the definition"}}
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// Validate는 정의 전체를 검사한다. 모든 문제를 모아서 돌려준다.
func (d *Definition) Validate() error {
	var probs []string
	bad := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	if d.Version != DefinitionVersion {
		bad("version must be %d", DefinitionVersion)
	}
	if d.Key != "" && !slugRe.MatchString(d.Key) {
		bad("key must be lowercase letters, digits and '-' (max 64)")
	}
	if len(d.Name) > 100 {
		bad("name is longer than 100 characters")
	}
	if len(d.Description) > 1000 {
		bad("description is longer than 1000 characters")
	}
	if len(d.Readme) > 64<<10 {
		bad("readme is larger than 64KB")
	}
	if !iconRe.MatchString(d.Icon) {
		bad("icon must be an icon name (letters, digits, '.', '_', '-')")
	}
	if d.Category != "" && !slugRe.MatchString(d.Category) {
		bad("category must be a slug")
	}
	if len(d.Tags) > 20 {
		bad("at most 20 tags")
	}
	for _, t := range d.Tags {
		if !slugRe.MatchString(t) || len(t) > 40 {
			bad("tag %q must be a slug (max 40)", t)
		}
	}
	// inputs
	inputs := map[string]*Input{}
	if len(d.Inputs) > maxInputs {
		bad("at most %d inputs", maxInputs)
	}
	for i := range d.Inputs {
		in := &d.Inputs[i]
		if !inputKeyRe.MatchString(in.Key) {
			bad("inputs[%d].key must match [A-Za-z_][A-Za-z0-9_]* (max 64)", i)
			continue
		}
		if inputs[in.Key] != nil {
			bad("duplicate input %s", in.Key)
		}
		inputs[in.Key] = in
		if len(in.Label) > 100 || len(in.Description) > 1000 || len(in.Default) > 4096 {
			bad("input %s: label/description/default too long", in.Key)
		}
		if in.Pattern != "" {
			re, err := compilePattern(in.Pattern)
			if err != nil {
				bad("input %s: invalid pattern: %v", in.Key, err)
			} else if in.Default != "" && !tokenRe.MatchString(in.Default) && !re.MatchString(in.Default) {
				bad("input %s: default does not match pattern", in.Key)
			}
		}
		for _, tok := range tokenRe.FindAllStringSubmatch(in.Default, -1) {
			if err := checkFunction(tok[1]); err != nil {
				bad("input %s default: %v", in.Key, err)
			}
		}
	}
	// services
	if len(d.Services) == 0 {
		bad("services must not be empty")
	}
	if len(d.Services) > maxServices {
		bad("at most %d services", maxServices)
	}
	byName := map[string]*ServiceSpec{}
	for i := range d.Services {
		sv := &d.Services[i]
		switch {
		case !svcNameRe.MatchString(sv.Name):
			bad("services[%d].name must be lowercase letters, digits and '-' (max 40)", i)
			continue
		case sv.Name == "shared" || sv.Name == "inputs":
			bad("service name %q is reserved", sv.Name)
			continue
		case byName[sv.Name] != nil:
			bad("duplicate service %s", sv.Name)
			continue
		}
		byName[sv.Name] = sv
	}
	for i := range d.Services {
		sv := &d.Services[i]
		if byName[sv.Name] != sv {
			continue
		}
		pre := "service " + sv.Name + ": "
		probs = append(probs, prefixAll(pre, sv.validate(inputs))...)
	}
	probs = append(probs, d.validateHA()...)
	if d.Primary != "" && byName[d.Primary] == nil {
		bad("primary %q is not a service of this template", d.Primary)
	}
	if len(probs) == 0 {
		probs = append(probs, d.checkReferences(byName)...)
	}
	if len(probs) > 0 {
		return &ValidationError{probs}
	}
	return nil
}

func prefixAll(pre string, ss []string) []string {
	for i := range ss {
		ss[i] = pre + ss[i]
	}
	return ss
}

func compilePattern(p string) (*regexp.Regexp, error) {
	if len(p) > 500 {
		return nil, errors.New("longer than 500 characters")
	}
	return regexp.Compile(`^(?:` + p + `)$`)
}

func (sv *ServiceSpec) validate(inputs map[string]*Input) []string {
	var probs []string
	bad := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	if !iconRe.MatchString(sv.Icon) {
		bad("icon must be an icon name")
	}
	src := sv.Source
	n := 0
	if src.Image != "" {
		n++
	}
	if src.Repo != nil {
		n++
	}
	if src.Database != "" {
		n++
	}
	if n != 1 {
		bad("source must have exactly one of image, repo, database")
	}
	// 입력/함수는 문자열 설정에도 쓸 수 있다(참조는 변수에만).
	plain := func(field, v string, check func(string) bool) {
		probs = append(probs, checkTokens(field, v, inputs, false)...)
		if !tokenRe.MatchString(v) && check != nil && !check(v) {
			bad("invalid %s", field)
		}
	}
	if src.Image != "" {
		plain("source.image", src.Image, imageNameRe.MatchString)
	}
	if r := src.Repo; r != nil {
		plain("source.repo.url", r.URL, validRepoURL)
		if r.Branch != "" {
			plain("source.repo.branch", r.Branch, func(b string) bool { return branchNameR.MatchString(b) && !strings.HasPrefix(b, "-") })
		}
		if strings.Contains(r.RootDir, "..") {
			bad("source.repo.root_dir must not contain '..'")
		}
	}
	if src.Database != "" {
		if _, ok := Databases[src.Database]; !ok {
			bad("unknown database %q (postgres, mysql, redis, mongo, bucket)", src.Database)
		}
	}
	if len(sv.Variables) > maxVariables {
		bad("at most %d variables", maxVariables)
	}
	for k, v := range sv.Variables {
		if !varKeyRe.MatchString(k) || len(k) > 256 {
			bad("invalid variable name %q", k)
		}
		if len(v) > 32<<10 {
			bad("variable %s is larger than 32KB", k)
		}
		probs = append(probs, checkTokens("variable "+k, v, inputs, true)...)
	}
	seen := map[string]bool{}
	for _, vol := range sv.Volumes {
		mp := vol.MountPath
		if !strings.HasPrefix(mp, "/") || strings.Contains(mp, "..") || mp == "/" || len(mp) > 255 || strings.ContainsAny(mp, ":,\x00\n") {
			bad("volume mount_path %q must be an absolute path", mp)
		}
		if seen[mp] {
			bad("duplicate volume %s", mp)
		}
		seen[mp] = true
		if vol.SizeLimitMB != nil && (*vol.SizeLimitMB < 1 || *vol.SizeLimitMB > 16777216) {
			bad("volume %s: size_limit_mb must be 1..16777216", mp)
		}
	}
	if len(sv.Domains) > 5 {
		bad("at most 5 domains")
	}
	for _, dm := range sv.Domains {
		if !dm.Generate {
			bad("domains: only generated domains ({\"generate\": true}) are supported")
		}
		if dm.TargetPort != nil && (*dm.TargetPort < 1 || *dm.TargetPort > 65535) {
			bad("domains: target_port must be 1..65535")
		}
	}
	if sv.TCPProxy != nil && (sv.TCPProxy.ContainerPort < 1 || sv.TCPProxy.ContainerPort > 65535) {
		bad("tcp_proxy.container_port must be 1..65535")
	}
	if c := sv.Canvas; c != nil && (math.IsNaN(c.X) || math.IsInf(c.X, 0) || math.IsNaN(c.Y) || math.IsInf(c.Y, 0) || math.Abs(c.X) > 1e6 || math.Abs(c.Y) > 1e6) {
		bad("canvas must be finite numbers")
	}
	probs = append(probs, sv.Settings.validate(inputs)...)
	return probs
}

func validRepoURL(u string) bool {
	return !strings.HasPrefix(u, "-") && len(u) <= 2048 && (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") ||
		strings.HasPrefix(u, "ssh://") || strings.HasPrefix(u, "git@"))
}

func (st *Settings) validate(inputs map[string]*Input) []string {
	var probs []string
	bad := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	rng := func(name string, p *int, lo, hi int) {
		if p != nil && (*p < lo || *p > hi) {
			bad("%s must be %d..%d", name, lo, hi)
		}
	}
	enum := func(name string, p *string, ok ...string) {
		if p == nil {
			return
		}
		for _, o := range ok {
			if *p == o {
				return
			}
		}
		bad("%s must be %s", name, strings.Join(ok, ", "))
	}
	rng("port", st.Port, 1, 65535)
	rng("healthcheck_timeout_s", st.HealthcheckTimeoutS, 1, 3600)
	rng("restart_max_retries", st.RestartMaxRetries, 0, 100)
	rng("replicas", st.Replicas, 1, 10)
	rng("sleep_after_s", st.SleepAfterS, 10, 7*24*3600)
	rng("memory_limit_mb", st.MemoryLimitMB, 6, 4194304)
	rng("overlap_seconds", st.OverlapSeconds, 0, config.MaxOverlapSeconds)
	rng("draining_seconds", st.DrainingSeconds, 0, config.MaxDrainingSeconds)
	if st.CPULimit != nil && (*st.CPULimit < 0.01 || *st.CPULimit > 256) {
		bad("cpu_limit must be 0.01..256")
	}
	enum("builder", st.Builder, "auto", "dockerfile", "railpack")
	enum("healthcheck_type", st.HealthcheckType, "http", "tcp", "none")
	enum("restart_policy", st.RestartPolicy, "always", "on_failure", "never")
	if st.CronSchedule != nil && strings.TrimSpace(*st.CronSchedule) != "" {
		if _, err := cronx.Parse(strings.TrimSpace(*st.CronSchedule)); err != nil {
			bad("cron_schedule: %v", err)
		}
	}
	if st.DockerfilePath != nil && strings.Contains(*st.DockerfilePath, "..") {
		bad("dockerfile_path must not contain '..'")
	}
	if st.HealthcheckPath != nil && (!strings.HasPrefix(*st.HealthcheckPath, "/") || len(*st.HealthcheckPath) > 1024) {
		bad("healthcheck_path must start with /")
	}
	if len(st.WatchPaths) > 100 {
		bad("at most 100 watch_paths")
	}
	if len(st.BuildSecrets) > 100 {
		bad("at most 100 build_secrets")
	}
	for _, n := range st.BuildSecrets {
		if !secretVarRe.MatchString(n) {
			bad("build_secrets must be variable names")
			break
		}
	}
	if len(st.PrivatePorts) > 32 {
		bad("at most 32 private_ports")
	}
	privatePorts := map[int]bool{}
	for _, port := range st.PrivatePorts {
		if port < 1 || port > 65535 || privatePorts[port] {
			bad("private_ports must contain unique ports from 1..65535")
			break
		}
		privatePorts[port] = true
	}
	for name, p := range map[string]*string{"start_command": st.StartCommand, "pre_deploy_command": st.PreDeployCommand} {
		if p != nil {
			if len(*p) > 8192 {
				bad("%s is longer than 8192 characters", name)
			}
			probs = append(probs, checkTokens(name, *p, inputs, false)...)
		}
	}
	return probs
}

// checkTokens는 값 안의 ${{ ... }}가 모두 올바른지 본다. allowRefs면 변수 참조도 허용한다(참조 대상은 따로 검사).
func checkTokens(field, v string, inputs map[string]*Input, allowRefs bool) []string {
	var probs []string
	for _, m := range tokenRe.FindAllStringSubmatch(v, -1) {
		body := m[1]
		if im := inBody.FindStringSubmatch(body); im != nil {
			if inputs[im[1]] == nil {
				probs = append(probs, fmt.Sprintf("%s: unknown input %q", field, im[1]))
			}
			continue
		}
		if fnBody.MatchString(body) {
			if err := checkFunction(body); err != nil {
				probs = append(probs, fmt.Sprintf("%s: %v", field, err))
			}
			continue
		}
		if refBody.MatchString(body) {
			if !allowRefs {
				probs = append(probs, fmt.Sprintf("%s: variable references are only allowed in variables", field))
			}
			continue
		}
		probs = append(probs, fmt.Sprintf("%s: invalid expression %q", field, m[0]))
	}
	return probs
}

// checkFunction은 함수 호출 하나를 검사한다(값은 만들지 않는다).
func checkFunction(body string) error {
	_, err := evalFunction(body, true)
	return err
}

// evalFunction은 secret(n[, "charset"]), randomInt(min, max), uuid()를 계산한다. dry면 인자만 검사한다.
func evalFunction(body string, dry bool) (string, error) {
	m := fnBody.FindStringSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("invalid expression %q", strings.TrimSpace(body))
	}
	name, args := m[1], m[2]
	switch name {
	case "secret":
		a := secretArgs.FindStringSubmatch(args)
		if a == nil {
			return "", errors.New(`secret expects (length) or (length, "charset")`)
		}
		n, err := strconv.Atoi(a[1])
		if err != nil || n < 1 || n > 512 {
			return "", errors.New("secret length must be 1..512")
		}
		charset := DefaultSecretCharset
		if a[2] != "" {
			cs, err := strconv.Unquote(a[2])
			if err != nil {
				return "", errors.New("secret charset must be a quoted string")
			}
			if err := checkCharset(cs); err != nil {
				return "", err
			}
			charset = cs
		}
		if dry {
			return "", nil
		}
		return randomString(n, charset)
	case "randomInt":
		a := intArgs.FindStringSubmatch(args)
		if a == nil {
			return "", errors.New("randomInt expects (min, max)")
		}
		lo, err1 := strconv.ParseInt(a[1], 10, 64)
		hi, err2 := strconv.ParseInt(a[2], 10, 64)
		if err1 != nil || err2 != nil || lo > hi || hi-lo < 0 || hi-lo > 1<<62 {
			return "", errors.New("randomInt needs min <= max (64-bit integers)")
		}
		if dry {
			return "", nil
		}
		v, err := rand.Int(rand.Reader, big.NewInt(hi-lo+1))
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(lo+v.Int64(), 10), nil
	case "uuid":
		if strings.TrimSpace(args) != "" {
			return "", errors.New("uuid takes no arguments")
		}
		if dry {
			return "", nil
		}
		return newUUID()
	}
	return "", fmt.Errorf("unknown function %s (secret, randomInt, uuid)", name)
}

func checkCharset(cs string) error {
	if len(cs) < 2 || len(cs) > 256 {
		return errors.New("secret charset must have 2..256 characters")
	}
	seen := map[byte]bool{}
	for i := 0; i < len(cs); i++ {
		c := cs[i]
		if c < 0x21 || c > 0x7e {
			return errors.New("secret charset must be printable ASCII without spaces")
		}
		if seen[c] {
			return fmt.Errorf("secret charset has %q twice", c)
		}
		seen[c] = true
	}
	return nil
}

// randomString은 charset에서 균등하게 n글자를 뽑는다(거부 샘플링으로 치우침이 없다).
func randomString(n int, charset string) (string, error) {
	out := make([]byte, n)
	limit := 256 - 256%len(charset)
	buf := make([]byte, 64)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out[i] = charset[int(b)%len(charset)]
			i++
			if i == n {
				break
			}
		}
	}
	return string(out), nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// injectedVar는 배포할 때 자동으로 들어오는 변수인지 본다(참조 대상 검사용).
func injectedVar(name string) bool {
	return strings.HasPrefix(name, "AVEN_") || strings.HasPrefix(name, "KEEL_") || name == "PORT"
}

// serviceVarNames는 서비스가 갖게 될 변수 이름이다(DB 기본 변수 포함).
func (sv *ServiceSpec) varNames() map[string]bool {
	out := map[string]bool{}
	if db, ok := Databases[sv.Source.Database]; ok {
		for k := range db.Variables("") {
			out[k] = true
		}
	}
	for k := range sv.Variables {
		out[k] = true
	}
	return out
}

// checkReferences는 참조 대상이 템플릿 안에 있는지, 순환이 없는지 본다.
func (d *Definition) checkReferences(byName map[string]*ServiceSpec) []string {
	var probs []string
	type node struct{ svc, name string }
	edges := map[node][]node{}
	names := map[string]map[string]bool{}
	for n, sv := range byName {
		names[n] = sv.varNames()
	}
	for i := range d.Services {
		sv := &d.Services[i]
		all := map[string]string{}
		if db, ok := Databases[sv.Source.Database]; ok {
			for k, v := range db.Variables("") {
				all[k] = v
			}
		}
		for k, v := range sv.Variables {
			all[k] = v
		}
		for k, v := range all {
			from := node{sv.Name, k}
			for _, ref := range vars.References(v) {
				if strings.HasPrefix(ref, "inputs.") {
					continue
				}
				scope, name, cross := strings.Cut(ref, ".")
				if !cross {
					scope, name = sv.Name, ref
				}
				if scope == "shared" {
					continue // 환경 공유 변수는 배포할 환경에 달려 있다
				}
				if names[scope] == nil {
					probs = append(probs, fmt.Sprintf("service %s: variable %s references unknown service %q", sv.Name, k, scope))
					continue
				}
				if !names[scope][name] {
					if injectedVar(name) {
						continue
					}
					probs = append(probs, fmt.Sprintf("service %s: variable %s references unknown variable %s.%s", sv.Name, k, scope, name))
					continue
				}
				edges[from] = append(edges[from], node{scope, name})
			}
		}
	}
	// 순환 검사(DFS). 같은 순환은 한 번만 알린다.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[node]int{}
	var stack []node
	reported := false
	var visit func(n node)
	visit = func(n node) {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range edges[n] {
			switch color[m] {
			case white:
				visit(m)
			case grey:
				if !reported {
					path := []string{}
					start := 0
					for i, s := range stack {
						if s == m {
							start = i
						}
					}
					for _, s := range stack[start:] {
						path = append(path, s.svc+"."+s.name)
					}
					path = append(path, m.svc+"."+m.name)
					probs = append(probs, "variable reference cycle: "+strings.Join(path, " → "))
					reported = true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
	}
	// 결정적인 순서로 돈다.
	for i := range d.Services {
		sv := &d.Services[i]
		keys := sortedKeys(sv.Variables)
		for _, k := range keys {
			if n := (node{sv.Name, k}); color[n] == white {
				visit(n)
			}
		}
	}
	return probs
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PrimaryService는 대표 서비스 이름이다(primary, 없으면 도메인이 있는 첫 서비스, 그것도 없으면 첫 서비스).
func (d *Definition) PrimaryService() string {
	if d.Primary != "" {
		return d.Primary
	}
	for _, sv := range d.Services {
		if len(sv.Domains) > 0 {
			return sv.Name
		}
	}
	if len(d.Services) > 0 {
		return d.Services[0].Name
	}
	return ""
}

// ---- 입력 ----

// InputError는 배포 입력의 문제(빠진 필수 입력, 패턴 불일치, 모르는 입력)다.
type InputError struct {
	Missing  []string `json:"missing,omitempty"`
	Invalid  []string `json:"invalid,omitempty"`
	Unknown  []string `json:"unknown,omitempty"`
	Messages []string `json:"messages"`
}

func (e *InputError) Error() string { return "invalid inputs: " + strings.Join(e.Messages, "; ") }

// ResolveInputs는 입력 값을 정한다(빈 값이면 기본값, 기본값 안의 함수는 계산한다).
func (d *Definition) ResolveInputs(given map[string]string) (map[string]string, error) {
	ie := &InputError{}
	declared := map[string]bool{}
	out := map[string]string{}
	for _, in := range d.Inputs {
		declared[in.Key] = true
		v, ok := given[in.Key]
		if !ok || v == "" {
			v = in.Default
			if tokenRe.MatchString(v) {
				ev, err := expandFunctions(v)
				if err != nil {
					return nil, err
				}
				v = ev
			}
		}
		if len(v) > 4096 {
			ie.Invalid = append(ie.Invalid, in.Key)
			ie.Messages = append(ie.Messages, in.Key+": longer than 4096 characters")
			continue
		}
		if in.Required && strings.TrimSpace(v) == "" {
			ie.Missing = append(ie.Missing, in.Key)
			ie.Messages = append(ie.Messages, in.Key+": required")
			continue
		}
		if in.Pattern != "" && v != "" {
			re, err := compilePattern(in.Pattern)
			if err != nil || !re.MatchString(v) {
				ie.Invalid = append(ie.Invalid, in.Key)
				ie.Messages = append(ie.Messages, fmt.Sprintf("%s: does not match pattern %s", in.Key, in.Pattern))
				continue
			}
		}
		out[in.Key] = v
	}
	for _, k := range sortedKeys(given) {
		if !declared[k] {
			ie.Unknown = append(ie.Unknown, k)
			ie.Messages = append(ie.Messages, k+": unknown input")
		}
	}
	if len(ie.Messages) > 0 {
		return nil, ie
	}
	return out, nil
}

// expandFunctions는 값 안의 함수만 계산한다(참조는 그대로 둔다).
func expandFunctions(v string) (string, error) {
	return expand(v, nil)
}

// expand는 ${{ inputs.X }}와 함수를 한 번에 바꾼다(입력 값 안의 ${{ }}는 다시 계산하지 않는다). 참조는 그대로 둔다.
func expand(v string, inputs map[string]string) (string, error) {
	var firstErr error
	out := tokenRe.ReplaceAllStringFunc(v, func(m string) string {
		body := tokenRe.FindStringSubmatch(m)[1]
		if im := inBody.FindStringSubmatch(body); im != nil && inputs != nil {
			return inputs[im[1]]
		}
		if fnBody.MatchString(body) {
			r, err := evalFunction(body, false)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			return r
		}
		return m
	})
	return out, firstErr
}

// ---- 배포 계획 ----

// Instantiate는 정의를 서비스 정의 목록으로 만든다. 입력을 검사하고 함수를 한 번씩 계산한다(DB 비밀번호도 새로 만든다).
// 대표 서비스(primary)의 위치도 돌려준다.
func (d *Definition) Instantiate(given map[string]string) ([]ServiceDef, int, error) {
	inputs, err := d.ResolveInputs(given)
	if err != nil {
		return nil, 0, err
	}
	ex := func(s string) (string, error) { return expand(s, inputs) }
	var out []ServiceDef
	primary := 0
	pname := d.PrimaryService()
	for i, sv := range d.Services {
		if sv.Name == pname {
			primary = i
		}
		var def ServiceDef
		switch {
		case sv.Source.Database != "":
			def, _ = DatabaseService(sv.Source.Database)
		case sv.Source.Repo != nil:
			def = ServiceDef{SourceType: "repo"}
			if def.RepoURL, err = ex(sv.Source.Repo.URL); err != nil {
				return nil, 0, err
			}
			if def.Branch, err = ex(sv.Source.Repo.Branch); err != nil {
				return nil, 0, err
			}
			if def.Branch == "" {
				def.Branch = "main"
			}
			if !validRepoURL(def.RepoURL) || !branchNameR.MatchString(def.Branch) || strings.HasPrefix(def.Branch, "-") {
				return nil, 0, &InputError{Invalid: []string{sv.Name}, Messages: []string{sv.Name + ": invalid repository URL or branch after inputs"}}
			}
			def.RootDir = sv.Source.Repo.RootDir
		default:
			def = ServiceDef{SourceType: "image"}
			if def.Image, err = ex(sv.Source.Image); err != nil {
				return nil, 0, err
			}
			if !imageNameRe.MatchString(def.Image) {
				return nil, 0, &InputError{Invalid: []string{sv.Name}, Messages: []string{sv.Name + ": invalid image after inputs"}}
			}
		}
		def.Name = sv.Name
		if sv.Icon != "" {
			def.Icon = sv.Icon
		}
		if def.Variables == nil {
			def.Variables = map[string]string{}
		}
		for _, k := range sortedKeys(sv.Variables) {
			v, err := ex(sv.Variables[k])
			if err != nil {
				return nil, 0, err
			}
			def.Variables[k] = v
		}
		for _, vol := range sv.Volumes {
			if !containsStr(def.Volumes, vol.MountPath) {
				def.Volumes = append(def.Volumes, vol.MountPath)
			}
			if vol.SizeLimitMB != nil {
				if def.VolumeLimits == nil {
					def.VolumeLimits = map[string]int{}
				}
				def.VolumeLimits[vol.MountPath] = *vol.SizeLimitMB
			}
		}
		for _, dm := range sv.Domains {
			def.Domains = append(def.Domains, DomainDef{TargetPort: dm.TargetPort})
		}
		if sv.TCPProxy != nil {
			def.TCPProxyPort = sv.TCPProxy.ContainerPort
		}
		if sv.Canvas != nil {
			c := *sv.Canvas
			def.Canvas = &c
		}
		st := sv.Settings
		for _, p := range []**string{&st.StartCommand, &st.PreDeployCommand} {
			if *p != nil {
				v, err := ex(**p)
				if err != nil {
					return nil, 0, err
				}
				*p = &v
			}
		}
		def.Settings = st
		out = append(out, def)
	}
	return out, primary, nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Apply는 설정을 인스턴스 설정에 덮어쓴다(지정한 것만).
func (st Settings) Apply(sp *store.Spec) {
	set := func(dst *string, p *string) {
		if p != nil {
			*dst = *p
		}
	}
	seti := func(dst *int, p *int) {
		if p != nil {
			*dst = *p
		}
	}
	if st.Port != nil {
		v := *st.Port
		sp.Port = &v
	}
	if st.StartCommand != nil {
		v := *st.StartCommand
		sp.StartCommand = &v
	}
	if st.PreDeployCommand != nil {
		v := *st.PreDeployCommand
		sp.PreDeployCommand = &v
	}
	set(&sp.Builder, st.Builder)
	set(&sp.DockerfilePath, st.DockerfilePath)
	if st.WatchPaths != nil {
		sp.WatchPaths = append([]string{}, st.WatchPaths...)
	}
	if st.AutoDeploy != nil {
		sp.AutoDeploy = *st.AutoDeploy
	}
	set(&sp.HealthcheckType, st.HealthcheckType)
	set(&sp.HealthcheckPath, st.HealthcheckPath)
	seti(&sp.HealthcheckTimeoutS, st.HealthcheckTimeoutS)
	set(&sp.RestartPolicy, st.RestartPolicy)
	seti(&sp.RestartMaxRetries, st.RestartMaxRetries)
	seti(&sp.Replicas, st.Replicas)
	if st.CronSchedule != nil {
		v := strings.TrimSpace(*st.CronSchedule)
		if v == "" {
			sp.CronSchedule = nil
		} else {
			sp.CronSchedule = &v
		}
	}
	if st.SleepWhenIdle != nil {
		sp.SleepWhenIdle = *st.SleepWhenIdle
	}
	seti(&sp.SleepAfterS, st.SleepAfterS)
	if st.MemoryLimitMB != nil {
		v := *st.MemoryLimitMB
		sp.MemoryLimitMB = &v
	}
	if st.CPULimit != nil {
		v := *st.CPULimit
		sp.CPULimit = &v
	}
	if st.BuildSecrets != nil {
		sp.BuildSecrets = append([]string{}, st.BuildSecrets...)
	}
	if st.WaitForCI != nil {
		sp.WaitForCI = *st.WaitForCI
	}
	seti(&sp.OverlapSeconds, st.OverlapSeconds)
	if st.DrainingSeconds != nil {
		v := *st.DrainingSeconds
		sp.DrainingSeconds = &v
	}
	if st.PrivatePorts != nil {
		sp.PrivatePorts = append([]int{}, st.PrivatePorts...)
	}
}
