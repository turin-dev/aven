// aven login <url>                     비밀번호로 로그인(KEEL_PASSWORD 또는 프롬프트). KEEL_TOKEN이 있으면 Bearer 사용
// aven link [-p project] [-e env] [-s service]   현재 디렉터리를 서비스에 연결
// aven dev [up|down|clean|configure] [-e env]   프로젝트 서비스를 로컬에서 실행
// aven up [-d]                         현재 디렉터리를 tar.gz로 올려 배포하고 빌드 로그를 따라간다
// aven logs [-b] [-n 200]              현재 배포의 런타임 로그(-b: 빌드 로그)
// aven vars [set K=V ... | unset K ...] [--deploy]
// aven api [-F k=v]... <query|@file>  Public GraphQL API(/graphql/v2) 요청(쿼리를 생략하면 stdin)
// aven status | init | config | environment | service | add | variables | run | shell | ssh | connect | redeploy | down | restart | scale | domain | open | whoami | logout | version
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
}

func cfgPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "aven", "config.json")
}

func loadCfg() *config {
	c := &config{Links: map[string]*link{}}
	if b, err := os.ReadFile(cfgPath()); err == nil {
		json.Unmarshal(b, c)
	} else if b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(cfgPath())), "keel", "config.json")); err == nil {
		// Logins made by the keel CLI keep working until the next save writes the aven path.
		json.Unmarshal(b, c)
	}
	if c.Links == nil {
		c.Links = map[string]*link{}
	}
	if u := os.Getenv("KEEL_URL"); u != "" {
		c.URL = u
	}
	return c
}

func (c *config) save() error {
	os.MkdirAll(filepath.Dir(cfgPath()), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(cfgPath(), b, 0o600)
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "aven: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	c := loadCfg()
	args := os.Args[2:]
	switch os.Args[1] {
	case "login":
		cmdLogin(c, args)
	case "link":
		cmdLink(c, args)
	case "up":
		cmdUp(c, args)
	case "logs":
		cmdLogs(c, args)
	case "api":
		cmdAPI(c, args)
	case "vars":
		cmdVars(c, args, false)
	case "variables", "variable":
		cmdVars(c, args, true)
	case "config":
		cmdConfig(c, args)
	case "import":
		cmdImport(c, args)
	case "github", "gh":
		cmdGitHub(c, args)
	case "status":
		cmdStatus(c, args)
	case "init":
		cmdInit(c, args)
	case "environment", "env":
		cmdEnvironment(c, args)
	case "service":
		cmdService(c, args)
	case "add":
		cmdAdd(c, args)
	case "run":
		cmdRun(c, args)
	case "dev", "develop":
		cmdDev(c, args)
	case "shell":
		cmdShell(c, args)
	case "ssh":
		cmdSSH(c, args)
	case "connect":
		cmdConnect(c, args)
	case "netdiag":
		cmdNetDiag(c, args)
	case "redeploy":
		cmdRedeploy(c, args)
	case "down":
		cmdDown(c, args)
	case "restart":
		cmdRestart(c, args)
	case "scale":
		cmdScale(c, args)
	case "flag", "flags":
		cmdFlag(c, args)
	case "pitr":
		cmdPITR(c, args)
	case "mysql":
		if len(args) == 0 || args[0] != "pitr" {
			die("usage: aven mysql pitr [status|enable|disable|base|restore|delete-archive]")
		}
		cmdPITR(c, args[1:])
	case "cdn":
		cmdCDN(c, args)
	case "waf":
		cmdWAF(c, args)
	case "edge":
		cmdEdge(c, args)
	case "ha":
		cmdHA(c, args)
	case "domain", "domains":
		cmdDomain(c, args)
	case "tcp-proxy", "tcpproxy":
		cmdTCPProxy(c, args)
	case "networking", "net":
		cmdNetworking(c, args)
	case "postgres", "pg":
		if len(args) > 0 && args[0] == "pitr" {
			cmdPITR(c, args[1:])
			return
		}
		cmdPostgres(c, args)
	case "open":
		cmdOpen(c, args)
	case "whoami":
		cmdWhoami(c, args)
	case "tokens", "token":
		cmdTokens(c, args)
	case "usage":
		cmdUsage(c, args)
	case "trace", "traces", "tracing":
		cmdTrace(c, args)
	case "templates", "template":
		cmdTemplates(c, args)
	case "mcp":
		cmdMCP(c, args)
	case "agent":
		cmdAgent(c, args)
	case "sandbox", "sandboxes", "sbx":
		cmdSandboxes(c, args)
	case "logout":
		cmdLogout(c, args)
	case "version", "--version", "-v":
		cmdVersion(c, args)
	case "help", "--help", "-h":
		fmt.Print(helpText)
	default:
		usage()
	}
}

const helpText = `aven: command line for aven-server (see docs/cli.md)

  login [--email you@x] <url>      log in (KEEL_PASSWORD or prompt, KEEL_TOTP for 2FA; no email = built-in admin;
                                   KEEL_TOKEN uses Bearer: KEEL_API_TOKEN or a keel_ API token)
  logout                           end the saved session
  whoami                           show the logged-in user, role and server
  tokens [list]                    list your API tokens
  tokens create --name n [--workspace id | --instance | --project p [--environment e]] [--role member] [--expires-days n]
                                   create a scoped API token (printed once)
  tokens revoke <id>               revoke an API token
  init <name> [--workspace id]     create a project and link this directory to it
  link [-p project] [-e env] [-s service]
                                   link this directory (also --project/--environment/--service)
  config import [--file path] [--format ts|json|yaml] [--include-variables] [--force] [--json] [-e env]
                                   write the linked environment to .keel/aven.ts (alias: pull)
  config plan [--file path] [-e env] [--json] [--show-values] [--detailed-exit-code] [--out plan.json]
                                   diff .keel/aven.ts|json|yaml against the environment (alias: preview)
  config apply [--file path] [-e env] [-y] [--confirm-destructive] [--deploy] [--json] [--plan plan.json]
                                   apply the plan as staged changes (--deploy: also deploy them)
  import dokploy scan|plan|apply|verify [--node srv=node] [--skip proj/name] [--move proj/name=proj]
                 [--local-addr ip] [--project p] [--keep-standby] [--keep-names] [--out plan.json] [--json] [-y]
                                   move a Dokploy install into aven (DOKPLOY_URL, DOKPLOY_API_KEY; Dokploy is only read)
  github status|connect|disconnect|repos [q]|webhook <repo>...
                                   GitHub connection (OAuth) used for private clones, commit statuses and repo webhooks
  import dokploy export-compose --dir DIR [proj/name ...]   write stacks that stay outside aven as plain compose
  config partials [list] | release <partial> | transfer <from> <to> [--resource addr]... [--dry-run] [-y]
                                   inspect or change named partial ownership
  status                           linked project/environment/service, instance status, latest deployment
  environment [list]               list environments (alias: env)
  environment new <name> [--copy-from env]
  environment use <name>           switch the linked environment
  environment delete <name> [-y]
  environment sync [target] --from <env> [--deploy] [-y]
                                   show what differs from <env> and apply it to the linked (or target) environment
  service [list]                   list services
  service use <name>               switch the linked service
  add --database postgres|mysql|redis|mongo | --image <img> | --repo <url> [--branch b] [--name n]
  up [-d]                          upload this directory and deploy it
  redeploy [-y] [--force-build]    redeploy the latest deployment or force-build the latest commit
  restart                          restart the running containers
  scale [<n>]                      show or change the replica count now (no redeploy)
  scale --auto --max n [--min n] [--cpu %] [--memory %] [--rps n] | --off
                                   horizontal autoscaling
  flag list [--full]               feature flags (project from KEEL_TOKEN or link; -e env, --scope project:<id>, --json)
  flag set <name> <value> [--when expr] [--rule-id id] [--type bool|string|number|json] [--force]
                                   set the default, or add/replace a targeting rule with --when
  flag unset <name> --rule-id id   remove a targeting rule
  flag delete <name>               delete a flag in every environment (alias: rm)
  cdn [status|enable|disable]      CDN caching of the service (also: --json)
  cdn update [--html-caching auto|force|never] [--default-ttl 2h] [--swr|--no-swr] [--purge-on-deploy off|html|all]
  cdn purge html|all               purge cached HTML or everything
  waf [status]                     WAF settings and Under Attack Mode status
  waf update [--allow-ip ip]... [--deny-ip ip]... [--clear-allow] [--clear-deny] [--rate-limit 100/60s|off] [--managed-rules on|off]
  waf under-attack status|enable [--duration 1h|3h|12h|24h]|disable
  edge rules [get] | edge rules set <file.json|->
                                   print or replace the edge rules (Railway-compatible JSON)
  ha [status]                      database high availability: cluster members, roles and health
  ha convert [--replicas n] [--edge n] [--no-auto-failover] [--skip-backup] [-y]
                                   convert the linked database to an HA cluster
  ha scale --replicas n | switchover --to <member> | auto-failover on|off | revert [-y]
  down [-y]                        stop the service (containers are removed until the next deploy)
  logs [--build] [--deployment id] [--filter q] [-n lines]
  api [-F key=value]... [--variables JSON|@file] [--raw] <query | @file>
                                   send a request to the GraphQL API at /graphql/v2 (stdin when query is omitted)
  logs --dns [--domain d] [--qname n] [--qtype A] [--rcode NXDOMAIN] [--zone internal|external] [--status ok|failed]
  logs --network [--protocol tcp|udp] [--direction ingress|egress] [--peer p] [--peer-kind k] [--port n] [--status s]
                                   DNS query / network flow logs (also --src --dst --host --dropped --since --until --json)
  variables [--set K=V]... [--unset K]... [--import .env]... [--seal K]... [--kv | --env] [--deploy]   (alias: vars)
  run <cmd...>                     run a local command with the service's variables
  dev [up|down|clean|configure] [-e env] [--dry-run] [-v]   (alias: develop)
                                   run project services locally (code + Docker images/databases)
  shell                            open a local subshell with the service's variables
  ssh [--replica r] [-- cmd...]    open a shell inside a running replica
  sandbox create|fork|list|exec|ssh|file|checkpoint|destroy
                                   ephemeral isolated containers in the linked environment
  connect [service] [--enable-proxy]
                                   open psql/mysql/redis-cli/mongosh against a database service
  pitr [status]                    point-in-time recovery status and restore window (alias: postgres pitr, mysql pitr)
  pitr enable [--retention-days 7] [--destination id] | pitr disable [-y]
                                   continuous WAL/binlog archiving to the backup storage
  pitr base                        take a base backup now
  pitr restore --at <time> [--new-service-name n | --in-place] [-y] [--no-wait]
                                   restore to a moment (RFC3339, local "YYYY-MM-DD HH:MM[:SS]", or 30m/2h/1d/1w ago)
  pitr delete-archive [-y]         delete the archive of a database with PITR off
  netdiag <host> [port] [-r replica]
                                   resolve a name and test a TCP connection from inside a running replica
  domain [host] [--port n]         add a generated (no host) or custom domain and print DNS records
  domain list [--json]             domains of the service with status, port and proxy
  domain status <host> [--json]    verification, proxy, DNS records (status, current value), certificate, warnings
  domain port <host> <n|auto>      change the target port (auto: follow the service port)
  domain verify <host>             re-check DNS and verification now
  domain rename <old> <new>        rename a generated domain (<new> may be just the first label)
  domain rm <host> [-y]            remove a domain
  tcp-proxy [status] [--json]      TCP proxy host:port and the container port behind it
  tcp-proxy enable [--port n] | port <n> | disable [-y]
                                   open, retarget or close the TCP proxy (not on Cloudflare tunnel nodes)
  networking [--json]              ports (detected/effective), domains, TCP proxy, private name and outbound IPv6 (alias: net)
  postgres pooling [status]        PgBouncer connection pooling of a postgres service (alias: pg, pgbouncer)
  postgres pooling enable|configure [--pool-mode transaction|session|statement] [--default-pool-size n]
                                   [--max-client-conn n] [--max-prepared-statements n]
  postgres pooling disable [-y]    remove PgBouncer; DATABASE_URL connects directly again
  postgres upgrade [status]        PostgreSQL major version and the last upgrade
  postgres upgrade check [--to n]  run the read-only preflight report
  postgres upgrade start [--to n] [-y]
                                   back up, then upgrade in place with pg_upgrade (database stops meanwhile)
  postgres upgrade revert [-y]     restore the pre-upgrade backup and previous major
  open                             open the dashboard for the linked service
  usage [--from D] [--to D] [--group service|environment|day] [--all]
                                   resource usage of the linked project (--all: per project, workspace admin)
  trace on|off|status|list|get [TRACE_ID] [-s service] [-e env] [-p project]
                                   enable per-service tracing or inspect traces (--all, --filter, --json)
  templates [list] [--q text] [--category c] [--tag t] [--sort popular|new]
  templates show <key>             services, inputs and readme of a template
  templates deploy <key> [--input K=V]... [-e env]
                                   deploy a template into the linked project/environment
  templates publish <file.json> [--workspace id] [--visibility private|workspace|public] [--key k]
  templates export <key>           print a template definition (JSON)
  templates export-env [--secrets generate|inputs] [-e env] > file.json
                                   generate a template definition from the linked environment
  mcp                              serve the hosted MCP endpoint over stdio (KEEL_TOKEN or the aven login session)
  mcp install [--agent claude-code|cursor|vscode]...
                                   add the aven MCP server to the MCP client config in this directory
  agent [-p prompt] [--json] [-y]  chat with Aven Agent (interactive without -p; -y approves actions)
  agent --list | --thread-id <id>  list previous agent threads / continue one
  version                          print CLI and server versions
  help                             this help

Most service commands accept -s/--service and -e/--environment to target something other than the link.
`

func usage() {
	fmt.Fprint(os.Stderr, helpText)
	os.Exit(2)
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
	if t := os.Getenv("KEEL_TOKEN"); t != "" {
		r.Header.Set("Authorization", "Bearer "+t)
	} else if c.Cookie != "" {
		r.AddCookie(&http.Cookie{Name: "keel_session", Value: c.Cookie})
	}
	return http.DefaultClient.Do(r)
}

func (c *config) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	resp, err := c.req(method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if resp.StatusCode == 401 {
			return errors.New("unauthorized (run: aven login <url>)")
		}
		if resp.StatusCode == 403 && e.Error == "mfa_setup_required" {
			return errors.New("this workspace requires two-factor authentication: set up an authenticator app or passkey in the dashboard (Account → Security), then retry")
		}
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// sse는 SSE 스트림을 읽어 (event, data)를 넘긴다. f가 false를 돌려주면 멈춘다.
func (c *config) sse(path string, f func(event, data string) bool) error {
	resp, err := c.req("GET", path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %d", path, resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	event := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "event: "):
			event = l[7:]
		case strings.HasPrefix(l, "data: "):
			if !f(event, l[6:]) {
				return nil
			}
		}
	}
	return sc.Err()
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
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		die("url must start with http:// or https://")
	}
	c.URL = strings.TrimRight(raw, "/")
	if os.Getenv("KEEL_TOKEN") != "" {
		if err := c.call("GET", "/auth/me", nil, nil); err != nil {
			die("%v", err)
		}
		c.save()
		fmt.Println("logged in with KEEL_TOKEN")
		return
	}
	if email == "" {
		email = os.Getenv("KEEL_EMAIL")
	}
	pw := os.Getenv("KEEL_PASSWORD")
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
		resp, err := http.Post(c.URL+"/api/auth/login", "application/json", bytes.NewReader(b))
		if err != nil {
			die("%v", err)
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
		code := os.Getenv("KEEL_TOTP")
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
			die("this account must sign in with SAML SSO.\nopen %s/login in a browser, choose SSO, create an API token in Settings, then run: KEEL_TOKEN=<token> aven login %s", c.URL, c.URL)
		}
		if msg != "" {
			die("login failed: %s (%s)", resp.Status, msg)
		}
		die("login failed: %s", resp.Status)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "keel_session" {
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
	if c.call("GET", "/api/auth/me", nil, &me) == nil && me.MFASetupRequired {
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

func readSecret() string {
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		off := exec.Command("stty", "-echo")
		off.Stdin = os.Stdin
		off.Run()
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = os.Stdin
			on.Run()
		}()
	}
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(s, "\r\n")
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
	fl := flag.NewFlagSet("link", flag.ExitOnError)
	pf := fl.String("p", "", "project name or id")
	ef := fl.String("e", "", "environment name or id (default: project default)")
	sf := fl.String("s", "", "service name or id")
	fl.StringVar(pf, "project", "", "project name or id")
	fl.StringVar(ef, "environment", "", "environment name or id")
	fl.StringVar(sf, "service", "", "service name or id")
	fl.Parse(args)
	var ps []project
	if err := c.call("GET", "/projects", nil, &ps); err != nil {
		die("%v", err)
	}
	var p *project
	for i := range ps {
		if *pf == "" && len(ps) == 1 || ps[i].ID == *pf || ps[i].Name == *pf {
			p = &ps[i]
		}
	}
	if p == nil {
		names := []string{}
		for _, x := range ps {
			names = append(names, x.Name)
		}
		die("choose a project with -p (%s)", strings.Join(names, ", "))
	}
	var full project
	if err := c.call("GET", "/projects/"+p.ID, nil, &full); err != nil {
		die("%v", err)
	}
	l := &link{ProjectID: full.ID, Project: full.Name}
	for _, e := range full.Environments {
		if (*ef == "" && e.ID == full.DefaultEnvironmentID) || e.ID == *ef || e.Name == *ef {
			l.EnvironmentID, l.Environment = e.ID, e.Name
		}
	}
	if l.EnvironmentID == "" {
		die("environment %q not found", *ef)
	}
	for _, s := range full.Services {
		if (*sf == "" && len(full.Services) == 1) || s.ID == *sf || s.Name == *sf {
			l.ServiceID, l.Service = s.ID, s.Name
		}
	}
	if l.ServiceID == "" && *sf != "" {
		names := []string{}
		for _, s := range full.Services {
			names = append(names, s.Name)
		}
		die("service %q not found (%s)", *sf, strings.Join(names, ", "))
	}
	dir, _ := os.Getwd()
	c.Links[dir] = l
	if err := c.save(); err != nil {
		die("%v", err)
	}
	if l.ServiceID == "" {
		fmt.Printf("linked %s → %s / %s (no service; run: aven service use <name>)\n", dir, full.Name, l.Environment)
		return
	}
	fmt.Printf("linked %s → %s / %s / %s\n", dir, full.Name, l.Environment, l.Service)
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
	die("this directory is not linked (run: aven link)")
	return nil
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".next": true, "__pycache__": true, ".venv": true}

// tarDir은 dir을 tar.gz로 묶는다(.git, node_modules 등 제외).
func tarDir(dir string, w io.Writer) (int, error) {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			n++
			return err
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	if err := tw.Close(); err != nil {
		return n, err
	}
	return n, gz.Close()
}

type deployment struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

func cmdUp(c *config, args []string) {
	fl := flag.NewFlagSet("up", flag.ExitOnError)
	detach := fl.Bool("d", false, "don't follow build logs")
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	dir, _ := os.Getwd()
	var src bytes.Buffer
	n, err := tarDir(dir, &src)
	if err != nil {
		die("pack: %v", err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("source", "source.tar.gz")
	fw.Write(src.Bytes())
	mw.Close()
	fmt.Printf("uploading %d files (%d KiB) to %s/%s...\n", n, src.Len()/1024, l.Environment, l.Service)
	resp, err := c.req("POST", "/services/"+l.ServiceID+"/instances/"+l.EnvironmentID+"/upload", &body, mw.FormDataContentType())
	if err != nil {
		die("%v", err)
	}
	defer resp.Body.Close()
	var d deployment
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		die("upload failed: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	json.NewDecoder(resp.Body).Decode(&d)
	fmt.Println("deployment", d.ID)
	if *detach {
		return
	}
	final := ""
	c.sse("/deployments/"+d.ID+"/build-logs", func(ev, data string) bool {
		switch ev {
		case "log":
			fmt.Println(data)
		case "done":
			final = data
			return false
		}
		return true
	})
	if final == "" {
		var cur deployment
		c.call("GET", "/deployments/"+d.ID, nil, &cur)
		final = cur.Status
	}
	fmt.Println("status:", final)
	if final != "live" {
		os.Exit(1)
	}
}

func cmdLogs(c *config, args []string) {
	if kind := netLogKind(args); kind != "" {
		cmdNetLogs(c, kind, args) // --dns / --network: networklogs.go
		return
	}
	fl := flag.NewFlagSet("logs", flag.ExitOnError)
	build := fl.Bool("b", false, "build logs of the latest deployment")
	fl.BoolVar(build, "build", false, "build logs of the latest deployment")
	n := fl.Int("n", 200, "lines of history")
	fl.IntVar(n, "lines", 200, "lines of history")
	depID := fl.String("deployment", "", "deployment id (default: current / latest)")
	filter := fl.String("filter", "", `filter: words, "phrase", -exclude`)
	tf := addTargetFlags(fl)
	fl.Parse(args)
	l := tf.service(c)
	base := "/services/" + l.ServiceID + "/instances/" + l.EnvironmentID
	if *build {
		id := *depID
		if id == "" {
			var deps []deployment
			c.call("GET", base+"/deployments?limit=1", nil, &deps)
			if len(deps) == 0 {
				die("no deployments")
			}
			id = deps[0].ID
		}
		match := buildFilter(*filter)
		err := c.sse("/deployments/"+url.PathEscape(id)+"/build-logs", func(ev, data string) bool {
			if ev == "log" && match(data) {
				fmt.Println(data)
			}
			return ev != "done"
		})
		if err != nil {
			die("%v", err)
		}
		return
	}
	id := *depID
	if id == "" {
		var inst struct {
			CurrentDeploymentID *string `json:"current_deployment_id"`
		}
		if err := c.call("GET", base, nil, &inst); err != nil {
			die("%v", err)
		}
		if inst.CurrentDeploymentID == nil {
			die("no live deployment")
		}
		id = *inst.CurrentDeploymentID
	}
	q := url.Values{"tail": {fmt.Sprint(*n)}}
	if *filter != "" {
		q.Set("q", *filter)
	}
	err := c.sse("/deployments/"+url.PathEscape(id)+"/deploy-logs?"+q.Encode(), func(ev, data string) bool {
		if ev == "log" {
			var e struct{ T, Replica, Stream, Msg string }
			json.Unmarshal([]byte(data), &e)
			t, _ := time.Parse(time.RFC3339Nano, e.T)
			fmt.Printf("%s %s %s\n", t.Local().Format("15:04:05"), e.Replica, e.Msg)
		}
		return ev != "done"
	})
	if err != nil {
		die("%v", err)
	}
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
		die("%v\nusage: aven variables [--set KEY=VALUE]... [--unset KEY]... [--import FILE]... [--seal KEY]... [--kv | --env] [--deploy] [-s service] [-e env]", err)
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
	sealed := map[string]bool{}
	for _, k := range inst.SealedVariables {
		sealed[k] = true
	}
	keys := make([]string, 0, len(inst.Variables))
	width := 0
	for k := range inst.Variables {
		keys = append(keys, k)
		width = max(width, len(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case table && !o.kv:
			fmt.Printf("%-*s  %s\n", width, k, inst.Variables[k])
		case sealed[k]:
			fmt.Printf("# %s=<sealed>\n", k) // 봉인 값은 읽을 수 없다
		default:
			fmt.Printf("%s=%s\n", k, inst.Variables[k])
		}
	}
	if len(inst.PendingChanges) > 0 && !o.deploy {
		fmt.Fprintf(os.Stderr, "pending changes (apply with --deploy): %s\n", strings.Join(inst.PendingChanges, ", "))
	}
}
