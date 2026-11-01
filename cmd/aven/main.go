// aven login <url>                     비밀번호로 로그인(AVEN_PASSWORD 또는 프롬프트, KEEL_PASSWORD도 동작). AVEN_TOKEN이 있으면 Bearer 사용
// aven link [-p project] [-e env] [-s service]   현재 디렉터리를 서비스에 연결
// aven dev [up|down|clean|configure] [-e env]   프로젝트 서비스를 로컬에서 실행
// aven up [-d]                         현재 디렉터리를 tar.gz로 올려 배포하고 빌드 로그를 따라간다
// aven logs [-b] [-n 200]              현재 배포의 런타임 로그(-b: 빌드 로그)
// aven vars [set K=V ... | unset K ...] [--deploy]
// aven api [-F k=v]... <query|@file>  Public GraphQL API(/graphql/v2) 요청(쿼리를 생략하면 stdin)
// aven status | init | config | environment | service | add | variables | run | shell | ssh | connect | redeploy | down | restart | scale | domain | open | whoami | logout | version
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type link struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ServiceID     string `json:"service_id"`
	Service       string `json:"service"`
	Environment   string `json:"environment"`
	Project       string `json:"project,omitempty"`
}

// version은 빌드 때 -ldflags "-X main.version=..."로 정한다.
var version = "dev"

type config struct {
	URL    string           `json:"url"`
	Cookie string           `json:"cookie,omitempty"`
	Links  map[string]*link `json:"links"`

	envURL  string // KEEL_URL as applied by loadCfg
	fileURL string // the URL stored in the file, kept when only KEEL_URL changed c.URL
}

// cfgDir is the per-OS CLI config directory: KEEL_CONFIG_DIR if set, else <user config dir>/aven
// (~/.config/aven on Linux, ~/Library/Application Support/aven on macOS, %AppData%\aven on Windows).
func cfgDir() string {
	if d := compat.Env("CONFIG_DIR"); d != "" {
		return d
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "aven")
}

func cfgPath() string { return filepath.Join(cfgDir(), "config.json") }

// legacyCfgPath is where the keel CLI kept its config (only used when KEEL_CONFIG_DIR is not set).
func legacyCfgPath() string {
	if compat.Env("CONFIG_DIR") != "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgDir()), "keel", "config.json")
}

func loadCfg() *config {
	c, warn := readCfg(cfgPath(), legacyCfgPath())
	if warn != "" {
		warnf("%s", warn)
	}
	c.fileURL = c.URL
	if u := compat.Env("URL"); u != "" {
		c.URL, c.envURL = u, u
	}
	return c
}

// readCfg reads the config file (or the legacy keel one). A damaged file is reported instead of silently
// looking like a logged-out CLI.
func readCfg(path, legacy string) (*config, string) {
	c := &config{Links: map[string]*link{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && legacy != "" {
		// Logins made by the keel CLI keep working until the next save writes the aven path.
		compat.WarnOnce("cli-config", "config %s is in use; %s is the new path (the old path still works)", legacy, path)
		path = legacy
		b, err = os.ReadFile(legacy)
	}
	warn := ""
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		warn = fmt.Sprintf("cannot read %s: %v", path, err)
	default:
		if err := json.Unmarshal(b, c); err != nil {
			c = &config{}
			warn = fmt.Sprintf("%s is damaged (%v); ignoring it, the next login rewrites it", path, err)
		}
	}
	if c.Links == nil {
		c.Links = map[string]*link{}
	}
	return c, warn
}

// save writes the config. A KEEL_URL override is not persisted: `KEEL_URL=x aven link` must not
// repoint every later command (only aven login changes the stored URL).
func (c *config) save() error {
	stored := *c
	if c.envURL != "" && c.URL == c.envURL {
		stored.URL = c.fileURL
	}
	return writeFileAtomic(cfgPath(), &stored)
}

// writeFileAtomic writes v as indented JSON with mode 0600 through a temporary file and a rename, so a
// crash or a concurrent aven process never leaves a half-written file, and an existing file that was
// readable by others (older CLIs, manual edits) ends up private again.
func writeFileAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "--help", "-h":
		cmdHelp(rest)
		return
	case "--version", "-v":
		name = "version"
	}
	cmd := lookupCommand(name)
	if cmd == nil {
		unknownCommand(name)
	}
	// `aven <command> -h|--help` prints that command's help (only as the first argument, so
	// `aven run -- npm --help` and `aven ssh -- ls --help` still reach the child).
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		printCommandHelp(os.Stdout, cmd.names[0])
		return
	}
	cmd.run(loadCfg(), rest)
}

// ---- HTTP ----

func (c *config) req(method, path string, body io.Reader, ctype string) (*http.Response, error) {
	if c.URL == "" {
		return nil, errors.New("not logged in (run: aven login <url>)")
	}
	r, err := http.NewRequest(method, strings.TrimRight(c.URL, "/")+"/api"+path, body)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	r.Header.Set("User-Agent", userAgent())
	c.authorize(r)
	resp, err := http.DefaultClient.Do(r)
	return resp, netHint(c.URL, err)
}

// authorize adds KEEL_TOKEN as a Bearer token, or the stored session cookie.
func (c *config) authorize(r *http.Request) {
	if t := compat.Env("TOKEN"); t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	} else if c.Cookie != "" {
		r.AddCookie(&http.Cookie{Name: "aven_session", Value: c.Cookie})
	}
}

func userAgent() string {
	return "aven-cli/" + version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

func (c *config) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.req(method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return responseError(method, path, resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%s %s: unexpected response: %w", method, path, err)
		}
	}
	return nil
}

// responseError turns a non-2xx response into an error with a next step. It reads at most 64 KiB.
func responseError(method, path string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil || e.Error == "" {
		// A proxy in front of the server may answer with HTML or plain text: show its first line only.
		e.Error = firstLine(string(b))
		if strings.HasPrefix(e.Error, "<") || len(e.Error) > 200 {
			e.Error = http.StatusText(resp.StatusCode)
		}
	}
	switch {
	case resp.StatusCode == 401:
		return errors.New("unauthorized: the session expired or the token is invalid\nhint: run aven login <url>, or check KEEL_TOKEN")
	case resp.StatusCode == 403 && e.Error == "mfa_setup_required":
		return errors.New("this workspace requires two-factor authentication: set up an authenticator app or passkey in the dashboard (Account → Security), then retry")
	}
	return &apiError{Method: method, Path: path, Status: resp.StatusCode, Msg: e.Error}
}

// ---- commands ----

// parseLoginArgs는 `login [--email e] <url>`을 읽는다(플래그는 url 앞뒤 어디에 와도 된다).
func parseLoginArgs(args []string) (rawURL, email string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--email" || a == "-email":
			if i+1 >= len(args) {
				return "", "", errors.New("--email needs a value")
			}
			email = args[i+1]
			i++
		case strings.HasPrefix(a, "--email="):
			email = strings.TrimPrefix(a, "--email=")
		case strings.HasPrefix(a, "-"):
			return "", "", fmt.Errorf("unknown flag %s", a)
		case rawURL == "":
			rawURL = a
		default:
			return "", "", errors.New("too many arguments")
		}
	}
	if rawURL == "" {
		return "", "", errors.New("url is required")
	}
	return rawURL, email, nil
}

func cmdLogin(c *config, args []string) {
	raw, email, err := parseLoginArgs(args)
	if err != nil {
		die("%v\nusage: aven login [--email you@example.com] <url>", err)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		die("url must start with http:// or https:// and name a host, e.g. https://aven.example.com")
	}
	if u.User != nil {
		die("the url must not contain credentials (they would be stored and printed); use --email and KEEL_PASSWORD or the prompt")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		die("the url must not have a query or fragment: use the dashboard address, e.g. %s://%s", u.Scheme, u.Host)
	}
	c.URL = strings.TrimRight(raw, "/")
	c.envURL = "" // an explicit login replaces the stored URL
	if compat.Env("TOKEN") != "" {
		if err := c.call("GET", "/auth/me", nil, nil); err != nil {
			die("%v", err)
		}
		if err := c.save(); err != nil {
			die("%v", err)
		}
		fmt.Printf("saved %s; requests authenticate with "+tokenEnvName()+" while it is set (the token itself is not stored)\n", c.URL)
		return
	}
	if email == "" {
		email = compat.Env("EMAIL")
	}
	pw := compat.Env("PASSWORD")
	if pw == "" {
		fmt.Fprint(os.Stderr, "password: ")
		pw = readSecret()
		fmt.Fprintln(os.Stderr)
	}
	body := map[string]string{"password": pw}
	if email != "" {
		body["email"] = email
	}
	var methods []string
	post := func() (*http.Response, string) {
		b, _ := json.Marshal(body)
		r, _ := http.NewRequest("POST", c.URL+"/api/auth/login", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", userAgent())
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			die("%v", netHint(c.URL, err))
		}
		var e struct {
			Error   string   `json:"error"`
			Methods []string `json:"methods"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		resp.Body.Close()
		methods = e.Methods
		return resp, e.Error
	}
	resp, msg := post()
	if resp.StatusCode == 401 && msg == "totp_required" {
		code := compat.Env("TOTP")
		if code == "" {
			fmt.Fprint(os.Stderr, secondFactorPrompt(methods))
			code = readSecret()
			fmt.Fprintln(os.Stderr)
		}
		body["totp_code"] = strings.TrimSpace(code)
		resp, msg = post()
	}
	if resp.StatusCode != 204 {
		if resp.StatusCode == 403 && msg == "sso_required" {
			die("this account must sign in with SAML SSO.\nopen %s/login in a browser, choose SSO, create an API token in Settings, then run: AVEN_TOKEN=<token> aven login %s", c.URL, c.URL)
		}
		if msg != "" {
			die("login failed: %s (%s)", resp.Status, msg)
		}
		die("login failed: %s", resp.Status)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "keel_session" {
			compat.NoteLegacyResource("session-cookie", "cookie keel_session", "cookie aven_session")
		}
		if ck.Name == "aven_session" || ck.Name == "keel_session" {
			c.Cookie = ck.Value
		}
	}
	if c.Cookie == "" {
		die("login failed: no session cookie")
	}
	if err := c.save(); err != nil {
		die("%v", err)
	}
	fmt.Println("logged in to", c.URL)
	var me struct {
		MFASetupRequired bool `json:"mfa_setup_required"`
	}
	if c.call("GET", "/auth/me", nil, &me) == nil && me.MFASetupRequired {
		fmt.Fprintln(os.Stderr, "warning: this workspace requires two-factor authentication; set it up in the dashboard (Account → Security) before using the CLI")
	}
}

// secondFactorPrompt는 로그인 두 번째 단계 입력 안내다. passkey는 CLI에서 쓸 수 없으므로 인증 앱 코드나 복구 코드를 받는다.
func secondFactorPrompt(methods []string) string {
	for _, m := range methods {
		if m == "totp" {
			return "2FA code (or recovery code): "
		}
	}
	if len(methods) > 0 {
		return "recovery code (passkeys only work in the dashboard): "
	}
	return "2FA code (or recovery code): "
}

type project struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	DefaultEnvironmentID string   `json:"default_environment_id"`
	Environments         []envRef `json:"environments"`
	Services             []svcRef `json:"services"`
}

type envRef struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Restricted bool   `json:"restricted"` // Environment RBAC: 프로젝트 admin만 리소스에 접근
}

type svcRef struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	SourceType   string  `json:"source_type"`
	DatabaseKind *string `json:"database_kind"`
}

func cmdLink(c *config, args []string) {
	fl := newFlagSet("link")
	pf := fl.String("p", "", "project name or id")
	ef := fl.String("e", "", "environment name or id (default: project default)")
	sf := fl.String("s", "", "service name or id")
	fl.StringVar(pf, "project", "", "project name or id")
	fl.StringVar(ef, "environment", "", "environment name or id")
	fl.StringVar(sf, "service", "", "service name or id")
	asJSON := fl.Bool("json", false, "print the link as JSON")
	fl.Parse(args)
	if fl.NArg() > 0 {
		die("unexpected argument %q\nusage: aven link [-p project] [-e env] [-s service]", fl.Arg(0))
	}
	var ps []project
	if err := c.call("GET", "/projects", nil, &ps); err != nil {
		die("%v", err)
	}
	p, err := chooseProject(ps, *pf, interactive())
	if err != nil {
		die("%v", err)
	}
	var full project
	if err := c.call("GET", "/projects/"+url.PathEscape(p.ID), nil, &full); err != nil {
		die("%v", err)
	}
	l := &link{ProjectID: full.ID, Project: full.Name}
	for _, e := range full.Environments {
		if (*ef == "" && e.ID == full.DefaultEnvironmentID) || e.ID == *ef || e.Name == *ef {
			l.EnvironmentID, l.Environment = e.ID, e.Name
		}
	}
	if l.EnvironmentID == "" && *ef == "" && len(full.Environments) > 0 {
		l.EnvironmentID, l.Environment = full.Environments[0].ID, full.Environments[0].Name
	}
	if l.EnvironmentID == "" {
		die("environment %q not found (%s)", *ef, names(full.Environments, func(e envRef) string { return e.Name }))
	}
	s, err := chooseService(full.Services, *sf, interactive())
	if err != nil {
		die("%v", err)
	}
	if s != nil {
		l.ServiceID, l.Service = s.ID, s.Name
	}
	dir, _ := os.Getwd()
	c.Links[dir] = l
	if err := c.save(); err != nil {
		die("%v", err)
	}
	if *asJSON {
		printJSON(map[string]any{"directory": dir, "link": l})
		return
	}
	if l.ServiceID == "" {
		fmt.Printf("linked %s → %s / %s (no service; run: aven service use <name>)\n", dir, out(styleBold, full.Name), l.Environment)
		return
	}
	fmt.Printf("linked %s → %s / %s / %s\n", dir, out(styleBold, full.Name), l.Environment, out(styleBold, l.Service))
}

// chooseProject picks the project for aven link: by id or (unique) name, the only one, or by asking.
func chooseProject(ps []project, want string, ask bool) (*project, error) {
	if len(ps) == 0 {
		return nil, errors.New("no projects yet (create one: aven init <name>)")
	}
	if want != "" {
		var match *project
		for i := range ps {
			if ps[i].ID == want {
				return &ps[i], nil
			}
			if ps[i].Name == want {
				if match != nil {
					return nil, fmt.Errorf("project name %q exists in several workspaces; pass its ID with -p", want)
				}
				match = &ps[i]
			}
		}
		if match == nil {
			return nil, fmt.Errorf("project %q not found (%s)", want, names(ps, func(p project) string { return p.Name }))
		}
		return match, nil
	}
	if len(ps) == 1 {
		return &ps[0], nil
	}
	if !ask {
		return nil, fmt.Errorf("choose a project with -p (%s)", names(ps, func(p project) string { return p.Name }))
	}
	i, err := pick("Select a project:", projectLabels(ps), false)
	if err != nil {
		return nil, err
	}
	return &ps[i], nil
}

// projectLabels shows names, adding the ID where a name is used twice.
func projectLabels(ps []project) []string {
	count := map[string]int{}
	for _, p := range ps {
		count[p.Name]++
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
		if count[p.Name] > 1 {
			out[i] += " (" + p.ID + ")"
		}
	}
	return out
}

// chooseService picks the linked service: -s, the only one, by asking (or none), else none.
func chooseService(ss []svcRef, want string, ask bool) (*svcRef, error) {
	if want != "" {
		for i := range ss {
			if ss[i].ID == want || ss[i].Name == want {
				return &ss[i], nil
			}
		}
		return nil, fmt.Errorf("service %q not found (%s)", want, names(ss, func(s svcRef) string { return s.Name }))
	}
	switch {
	case len(ss) == 1:
		return &ss[0], nil
	case len(ss) == 0 || !ask:
		return nil, nil
	}
	i, err := pick("Select a service (0 links only the environment):", names2(ss), true)
	if err != nil || i < 0 {
		return nil, err
	}
	return &ss[i], nil
}

func names2(ss []svcRef) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

func (c *config) linked() *link {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if l, ok := c.Links[d]; ok {
			return l
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	die("this directory is not linked to a project\nhint: run aven link (or aven init <name> for a new project)")
	return nil
}

// buildFilter는 빌드 로그용 클라이언트 쪽 필터다(서버의 q와 같은 문법: 단어, "구절", -제외; 대소문자 무시).
func buildFilter(q string) func(string) bool {
	var want, not []string
	for _, t := range splitQuery(q) {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			not = append(not, strings.ToLower(t[1:]))
		} else {
			want = append(want, strings.ToLower(t))
		}
	}
	return func(line string) bool {
		l := strings.ToLower(line)
		for _, w := range want {
			if !strings.Contains(l, w) {
				return false
			}
		}
		for _, w := range not {
			if strings.Contains(l, w) {
				return false
			}
		}
		return true
	}
}

func splitQuery(q string) []string {
	var out []string
	for q = strings.TrimSpace(q); q != ""; q = strings.TrimSpace(q) {
		neg := ""
		if strings.HasPrefix(q, `-"`) {
			neg, q = "-", q[1:]
		}
		if strings.HasPrefix(q, `"`) {
			end := strings.IndexByte(q[1:], '"')
			if end < 0 {
				out = append(out, neg+q[1:])
				break
			}
			out = append(out, neg+q[1:end+1])
			q = q[end+2:]
			continue
		}
		tok, rest, _ := strings.Cut(q, " ")
		out = append(out, neg+tok)
		q = rest
	}
	return out
}

type varsOpts struct {
	patch   map[string]*string
	deploy  bool
	kv      bool
	json    bool
	service string
	env     string
	seal    []string // --seal KEY: 봉인(값을 다시 볼 수 없다)
	dotenv  bool     // --env: .env 형식으로 출력(봉인 변수는 주석)
	imports []string // --import FILE: .env 파일의 변수를 설정한다("-"는 표준 입력)
}

// parseVarsArgs는 `vars`/`variables` 인자를 읽는다. 옛 형식(set K=V ... unset K ...)과
// --set K=V / --unset K / --kv / --deploy / -s / -e 를 함께 받는다(--flag=value도 된다).
func parseVarsArgs(args []string) (*varsOpts, error) {
	o := &varsOpts{patch: map[string]*string{}}
	mode := ""
	setKV := func(a string) error {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return fmt.Errorf("set expects KEY=VALUE, got %q", a)
		}
		o.patch[k] = &v
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "-") {
			name, hasVal = a, false
		}
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "--deploy", "-deploy":
			o.deploy = true
		case "--kv", "-kv":
			o.kv = true
		case "--json", "-json":
			o.json = true
		case "--env", "-env":
			o.dotenv = true
		case "--seal", "-seal":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.seal = append(o.seal, v)
		case "--import", "-import":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.imports = append(o.imports, v)
		case "--set", "-set":
			v, err := next()
			if err != nil {
				return nil, err
			}
			if err := setKV(v); err != nil {
				return nil, err
			}
		case "--unset", "-unset":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.patch[v] = nil
		case "-s", "--service", "-service":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.service = v
		case "-e", "--environment", "-environment":
			v, err := next()
			if err != nil {
				return nil, err
			}
			o.env = v
		case "set", "unset":
			mode = a
		default:
			switch {
			case strings.HasPrefix(a, "-"):
				return nil, fmt.Errorf("unknown flag %s", a)
			case mode == "set":
				if err := setKV(a); err != nil {
					return nil, err
				}
			case mode == "unset":
				o.patch[a] = nil
			default:
				return nil, fmt.Errorf("unexpected argument %q", a)
			}
		}
	}
	return o, nil
}

func cmdVars(c *config, args []string, table bool) {
	o, err := parseVarsArgs(args)
	if err != nil {
		die("%v\nusage: aven variables [--set KEY=VALUE]... [--unset KEY]... [--import FILE]... [--seal KEY]... [--kv | --env | --json] [--deploy] [-s service] [-e env]", err)
	}
	if o.json && (o.kv || o.dotenv) {
		die("--json cannot be combined with --kv or --env\nusage: aven variables [--json]")
	}
	// --import: 파일의 변수를 먼저 넣고 --set/--unset이 덮는다.
	if len(o.imports) > 0 {
		imported := map[string]string{}
		for _, f := range o.imports {
			m, err := readDotenvFile(f)
			if err != nil {
				die("%v", err)
			}
			for k, v := range m {
				imported[k] = v
			}
		}
		for k, v := range imported {
			if _, explicit := o.patch[k]; !explicit {
				o.patch[k] = &v
			}
		}
		fmt.Fprintf(os.Stderr, "aven: importing %d variables\n", len(imported))
	}
	l := (&targetFlags{svc: &o.service, env: &o.env}).service(c)
	path := "/services/" + l.ServiceID + "/instances/" + l.EnvironmentID
	var inst struct {
		Variables       map[string]string `json:"variables"`
		SealedVariables []string          `json:"sealed_variables"`
		PendingChanges  []string          `json:"pending_changes"`
	}
	if len(o.patch) > 0 || len(o.seal) > 0 || o.deploy {
		q := ""
		if o.deploy {
			q = "?deploy=1"
		}
		body := map[string]any{"variables_patch": o.patch}
		if len(o.seal) > 0 {
			body["sealed_variables"] = o.seal
		}
		if err := c.call("PATCH", path+q, body, &inst); err != nil {
			die("%v", err)
		}
		if len(o.seal) > 0 {
			fmt.Fprintf(os.Stderr, "aven: sealed %s (values can no longer be read; delete and recreate to unseal)\n", strings.Join(o.seal, ", "))
		}
	} else if !o.dotenv {
		if err := c.call("GET", path, nil, &inst); err != nil {
			die("%v", err)
		}
	}
	if o.dotenv {
		// .env 형식(raw 편집기와 같은 내용, 봉인 변수는 주석)
		var raw struct {
			Content string `json:"content"`
		}
		if err := c.call("GET", path+"/variables/raw?format=env", nil, &raw); err != nil {
			die("%v", err)
		}
		fmt.Print(raw.Content)
		if len(inst.PendingChanges) > 0 && !o.deploy {
			fmt.Fprintf(os.Stderr, "pending changes (apply with --deploy): %s\n", strings.Join(inst.PendingChanges, ", "))
		}
		return
	}
	if o.json {
		printJSON(map[string]any{"variables": nonNilMap(inst.Variables), "sealed": nonNil(inst.SealedVariables),
			"pending_changes": nonNil(inst.PendingChanges)})
		return
	}
	fmt.Print(formatVars(inst.Variables, inst.SealedVariables, table && !o.kv))
	if len(inst.PendingChanges) > 0 && !o.deploy {
		fmt.Fprintf(os.Stderr, "pending changes (apply with --deploy): %s\n", strings.Join(inst.PendingChanges, ", "))
	}
}

// formatVars prints variables sorted by name, as an aligned table or KEY=VALUE lines. Sealed values can
// never be read; they are shown as <sealed> (a comment in KEY=VALUE output, so it stays a valid .env).
func formatVars(vars map[string]string, sealedKeys []string, table bool) string {
	sealed := map[string]bool{}
	for _, k := range sealedKeys {
		sealed[k] = true
	}
	keys := make([]string, 0, len(vars))
	width := 0
	for k := range vars {
		keys = append(keys, k)
		width = max(width, len(k))
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		switch {
		case table && sealed[k]:
			fmt.Fprintf(&b, "%s  %s\n", pad(out(styleBold, k), k, width), out(styleDim, "<sealed>"))
		case table:
			fmt.Fprintf(&b, "%s  %s\n", pad(out(styleBold, k), k, width), vars[k])
		case sealed[k]:
			fmt.Fprintf(&b, "# %s=<sealed>\n", k) // 봉인 값은 읽을 수 없다
		default:
			fmt.Fprintf(&b, "%s=%s\n", k, vars[k])
		}
	}
	if table && len(keys) == 0 {
		b.WriteString("no variables (add one: aven variables --set KEY=VALUE)\n")
	}
	return b.String()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
