package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// command is one top-level `aven` command. names[0] is the canonical name, the rest are aliases.
type command struct {
	names  []string
	helpAs string // help entries to show (default names[0])
	run    func(c *config, args []string)
}

// commands is filled in init: the run functions refer back to this table (help, flag usage).
var commands []command

func init() {
	commands = []command{
		{names: []string{"login"}, run: cmdLogin},
		{names: []string{"logout"}, run: cmdLogout},
		{names: []string{"whoami"}, run: cmdWhoami},
		{names: []string{"tokens", "token"}, run: cmdTokens},
		{names: []string{"init"}, run: cmdInit},
		{names: []string{"link"}, run: cmdLink},
		{names: []string{"status"}, run: cmdStatus},
		{names: []string{"open"}, run: cmdOpen},
		{names: []string{"environment", "env"}, run: cmdEnvironment},
		{names: []string{"service"}, run: cmdService},
		{names: []string{"add"}, run: cmdAdd},
		{names: []string{"config"}, run: cmdConfig},
		{names: []string{"import"}, run: cmdImport},
		{names: []string{"github", "gh"}, run: cmdGitHub},
		{names: []string{"up"}, run: cmdUp},
		{names: []string{"redeploy"}, run: cmdRedeploy},
		{names: []string{"restart"}, run: cmdRestart},
		{names: []string{"down"}, run: cmdDown},
		{names: []string{"scale"}, run: cmdScale},
		{names: []string{"logs"}, run: cmdLogs},
		{names: []string{"variables", "variable"}, run: func(c *config, a []string) { cmdVars(c, a, true) }},
		{names: []string{"vars"}, helpAs: "variables", run: func(c *config, a []string) { cmdVars(c, a, false) }},
		{names: []string{"run"}, run: cmdRun},
		{names: []string{"shell"}, run: cmdShell},
		{names: []string{"dev", "develop"}, run: cmdDev},
		{names: []string{"ssh"}, run: cmdSSH},
		{names: []string{"connect"}, run: cmdConnect},
		{names: []string{"netdiag"}, run: cmdNetDiag},
		{names: []string{"sandbox", "sandboxes", "sbx"}, run: cmdSandboxes},
		{names: []string{"domain", "domains"}, run: cmdDomain},
		{names: []string{"tcp-proxy", "tcpproxy"}, run: cmdTCPProxy},
		{names: []string{"networking", "net"}, run: cmdNetworking},
		{names: []string{"cdn"}, run: cmdCDN},
		{names: []string{"waf"}, run: cmdWAF},
		{names: []string{"edge"}, run: cmdEdge},
		{names: []string{"ha"}, run: cmdHA},
		{names: []string{"pitr"}, run: cmdPITR},
		{names: []string{"mysql"}, helpAs: "pitr", run: func(c *config, args []string) {
			if len(args) == 0 || args[0] != "pitr" {
				die("usage: aven mysql pitr [status|enable|disable|base|restore|delete-archive]")
			}
			cmdPITR(c, args[1:])
		}},
		{names: []string{"postgres", "pg"}, run: func(c *config, args []string) {
			if len(args) > 0 && args[0] == "pitr" {
				cmdPITR(c, args[1:])
				return
			}
			cmdPostgres(c, args)
		}},
		{names: []string{"flag", "flags"}, run: cmdFlag},
		{names: []string{"usage"}, run: cmdUsage},
		{names: []string{"trace", "traces", "tracing"}, run: cmdTrace},
		{names: []string{"templates", "template"}, run: cmdTemplates},
		{names: []string{"api"}, run: cmdAPI},
		{names: []string{"mcp"}, run: cmdMCP},
		{names: []string{"agent"}, run: cmdAgent},
		{names: []string{"completion"}, run: func(_ *config, args []string) { cmdCompletion(args) }},
		{names: []string{"version"}, run: cmdVersion},
	}
}

func lookupCommand(name string) *command {
	for i := range commands {
		for _, n := range commands[i].names {
			if n == name {
				return &commands[i]
			}
		}
	}
	return nil
}

func commandNames(withAliases bool) []string {
	var out []string
	for _, c := range commands {
		if withAliases {
			out = append(out, c.names...)
		} else {
			out = append(out, c.names[0])
		}
	}
	return append(out, "help")
}

func usage() {
	fmt.Fprint(os.Stderr, helpText)
	os.Exit(exitUsage)
}

func unknownCommand(name string) {
	msg := fmt.Sprintf("unknown command %q", name)
	if strings.HasPrefix(name, "-") {
		msg = fmt.Sprintf("unknown flag %q (flags go after the command, e.g. aven logs -n 50)", name)
	} else if s := suggest(name, commandNames(true)); s != "" {
		msg += fmt.Sprintf("; did you mean %q?", s)
	}
	dieUsage("%s\nrun aven help for all commands", msg)
}

// cmdHelp: `aven help` lists everything, `aven help <command>` shows one command.
func cmdHelp(args []string) {
	if len(args) == 0 {
		fmt.Print(helpText)
		return
	}
	cmd := lookupCommand(args[0])
	if cmd == nil {
		unknownCommand(args[0])
	}
	printCommandHelp(os.Stdout, cmd.names[0])
}

func printCommandHelp(w io.Writer, name string) {
	cmd := lookupCommand(name)
	key := name
	if cmd != nil && cmd.helpAs != "" {
		key = cmd.helpAs
	}
	fmt.Fprint(w, commandHelp(key, cmd))
}

// commandHelp builds the help of one command from its entries in helpText plus its examples.
func commandHelp(key string, cmd *command) string {
	var b strings.Builder
	entries := helpEntries(key)
	b.WriteString("Usage:\n")
	for _, e := range entries {
		b.WriteString("  aven " + strings.TrimPrefix(e, "  "))
	}
	if cmd != nil && len(cmd.names) > 1 {
		fmt.Fprintf(&b, "\nAliases: %s\n", strings.Join(cmd.names[1:], ", "))
	}
	if ex := examples[key]; len(ex) > 0 {
		b.WriteString("\nExamples:\n")
		for _, e := range ex {
			b.WriteString("  " + e + "\n")
		}
	}
	if targetCommands[key] {
		b.WriteString("\nTarget flags:\n  -s, --service <name|id>        service (default: the linked one)\n  -e, --environment <name|id>    environment (default: the linked one)\n")
	}
	b.WriteString("\nMore: docs/cli.md\n")
	return b.String()
}

// helpEntries returns the helpText entries (first line plus indented continuation lines) of a command.
func helpEntries(key string) []string {
	var out []string
	var cur *strings.Builder
	flush := func() {
		if cur != nil {
			out = append(out, cur.String())
			cur = nil
		}
	}
	for _, line := range strings.SplitAfter(helpText, "\n") {
		switch {
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   "):
			flush()
			if first, _, _ := strings.Cut(strings.TrimSpace(line), " "); first == key {
				cur = &strings.Builder{}
				cur.WriteString(line)
			}
		case strings.HasPrefix(line, "   ") && cur != nil:
			cur.WriteString("     " + line) // keep continuation lines aligned under the added "aven "
		default:
			flush()
		}
	}
	flush()
	return out
}

// targetCommands accept -s/--service and -e/--environment.
var targetCommands = map[string]bool{
	"status": true, "up": true, "redeploy": true, "restart": true, "down": true, "scale": true, "logs": true,
	"variables": true, "run": true, "shell": true, "ssh": true, "domain": true, "tcp-proxy": true, "networking": true,
	"open": true, "cdn": true, "waf": true, "edge": true, "ha": true, "pitr": true, "postgres": true, "netdiag": true,
}

var examples = map[string][]string{
	"login": {
		"aven login https://aven.example.com",
		"aven login --email you@example.com https://aven.example.com",
		"KEEL_TOKEN=keel_... aven login https://aven.example.com   # CI: use an API token",
	},
	"link":   {"aven link                      # pick the project (asks on a terminal)", "aven link -p shop -e staging -s api"},
	"status": {"aven status", "aven status --json | jq .deployment.status"},
	"up":     {"aven up                        # upload, build and follow the logs", "aven up -d                     # don't wait for the build", "aven up -s worker -e staging"},
	"logs": {
		"aven logs                      # runtime logs of the live deployment, follows",
		"aven logs -n 50 --filter 'error -healthcheck'",
		"aven logs --build --deployment dep_123",
		"aven logs --errors --replica web-1",
		"aven logs --json | jq -r .msg",
	},
	"variables": {
		"aven variables",
		"aven variables --set PORT=8080 --set NODE_ENV=production --deploy",
		"aven variables --import .env.production",
		"aven variables --env > .env.remote",
	},
	"run":      {"aven run npm run migrate", "aven run -e staging -- node scripts/seed.js --dry-run"},
	"shell":    {"aven shell", "aven shell -s api"},
	"domain":   {"aven domain                    # generated *.your-base-domain", "aven domain shop.example.com --port 3000", "aven domain status shop.example.com"},
	"add":      {"aven add --database postgres", "aven add --image nginx:1.27 --name web", "aven add --repo https://github.com/acme/api --branch main"},
	"redeploy": {"aven redeploy -y", "aven redeploy --force-build"},
	"connect":  {"aven connect db", "aven connect db --enable-proxy"},
	"ssh":      {"aven ssh", "aven ssh -- ls -la /app"},
	"environment": {
		"aven environment new staging --copy-from production",
		"aven environment use staging",
		"aven environment sync --from production --deploy",
	},
	"service":    {"aven service", "aven service use api"},
	"tokens":     {"aven tokens create --name ci --project shop --environment production --role member"},
	"completion": {"source <(aven completion bash)", "aven completion zsh > \"${fpath[1]}/_aven\"", "aven completion fish > ~/.config/fish/completions/aven.fish", "aven completion powershell | Out-String | Invoke-Expression"},
	"api":        {"aven api '{ me { email } }'", "aven api -F id=prj_1 @query.graphql"},
	"open":       {"aven open", "aven open --print"},
	"whoami":     {"aven whoami", "aven whoami --json"},
}

// sortedKeys is used by tests to check every example belongs to a command.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const helpText = `aven: command line for aven-server

Usage: aven <command> [flags]       aven help <command> shows one command with examples

Getting started:
  login [--email you@x] <url>      log in (KEEL_PASSWORD or prompt, KEEL_TOTP for 2FA; no email = built-in admin;
                                   KEEL_TOKEN uses Bearer: KEEL_API_TOKEN or a keel_ API token)
  logout                           end the saved session
  whoami [--json]                  show the logged-in user, role and server
  init <name> [--workspace id]     create a project and link this directory to it
  link [-p project] [-e env] [-s service]
                                   link this directory (also --project/--environment/--service; asks on a terminal)
  status [--json]                  linked project/environment/service, instance status, latest deployment
  open [--print]                   open the dashboard for the linked service

Projects, environments and services:
  environment [list] [--json]      list environments (alias: env)
  environment new <name> [--copy-from env]
  environment use <name>           switch the linked environment
  environment delete <name> [-y]
  environment sync [target] --from <env> [--deploy] [-y]
                                   show what differs from <env> and apply it to the linked (or target) environment
  environment restrict|unrestrict [name]
                                   only project admins may access the environment
  service [list] [--json]          list services
  service use <name>               switch the linked service
  add --database postgres|mysql|redis|mongo|bucket | --image <img> | --repo <url> [--branch b] [--name n]
                                   add a service to the linked environment and link it

Deploy:
  up [dir] [-d] [--no-gitignore] [--json]
                                   upload this directory and deploy it, following the build logs
  redeploy [-y] [--force-build]    redeploy the latest deployment or force-build the latest commit
  restart                          restart the running containers
  down [-y]                        stop the service (containers are removed until the next deploy)
  scale [<n>]                      show or change the replica count now (no redeploy)
  scale --auto --max n [--min n] [--cpu %] [--memory %] [--rps n] | --off
                                   horizontal autoscaling
  logs [--build] [--deployment id] [--filter q] [-n lines] [--errors] [--replica r] [--stream stdout|stderr] [--json]
                                   runtime logs (follows while live) or build logs
  logs --dns [--domain d] [--qname n] [--qtype A] [--rcode NXDOMAIN] [--zone internal|external] [--status ok|failed]
  logs --network [--protocol tcp|udp] [--direction ingress|egress] [--peer p] [--peer-kind k] [--port n] [--status s]
                                   DNS query / network flow logs (also --src --dst --host --dropped --since --until --json)

Variables and local development:
  variables [--set K=V]... [--unset K]... [--import .env]... [--seal K]... [--kv | --env | --json] [--deploy]
                                   show or change variables (alias: vars, which prints KEY=VALUE)
  run <cmd...>                     run a local command with the service's variables
  shell                            open a local subshell with the service's variables
  dev [up|down|clean|configure] [-e env] [--dry-run] [-v]   (alias: develop)
                                   run project services locally (code + Docker images/databases)
  ssh [--replica r] [-- cmd...]    open a shell inside a running replica
  connect [service] [--enable-proxy]
                                   open psql/mysql/redis-cli/mongosh against a database service
  netdiag <host> [port] [-r replica]
                                   resolve a name and test a TCP connection from inside a running replica
  sandbox create|fork|list|exec|ssh|file|checkpoint|destroy
                                   ephemeral isolated containers in the linked environment

Networking:
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
  cdn [status|enable|disable]      CDN caching of the service (also: --json)
  cdn update [--html-caching auto|force|never] [--default-ttl 2h] [--swr|--no-swr] [--purge-on-deploy off|html|all]
  cdn purge html|all               purge cached HTML or everything
  waf [status]                     WAF settings and Under Attack Mode status
  waf update [--allow-ip ip]... [--deny-ip ip]... [--clear-allow] [--clear-deny] [--rate-limit 100/60s|off] [--managed-rules on|off]
  waf under-attack status|enable [--duration 1h|3h|12h|24h]|disable
  edge rules [get] | edge rules set <file.json|->
                                   print or replace the edge rules (Railway-compatible JSON)

Databases:
  ha [status]                      database high availability: cluster members, roles and health
  ha convert [--replicas n] [--edge n] [--no-auto-failover] [--skip-backup] [-y]
                                   convert the linked database to an HA cluster
  ha scale --replicas n | switchover --to <member> | auto-failover on|off | revert [-y]
  pitr [status]                    point-in-time recovery status and restore window (alias: postgres pitr, mysql pitr)
  pitr enable [--retention-days 7] [--destination id] | pitr disable [-y]
                                   continuous WAL/binlog archiving to the backup storage
  pitr base                        take a base backup now
  pitr restore --at <time> [--new-service-name n | --in-place] [-y] [--no-wait]
                                   restore to a moment (RFC3339, local "YYYY-MM-DD HH:MM[:SS]", or 30m/2h/1d/1w ago)
  pitr delete-archive [-y]         delete the archive of a database with PITR off
  postgres pooling [status]        PgBouncer connection pooling of a postgres service (alias: pg, pgbouncer)
  postgres pooling enable|configure [--pool-mode transaction|session|statement] [--default-pool-size n]
                                   [--max-client-conn n] [--max-prepared-statements n]
  postgres pooling disable [-y]    remove PgBouncer; DATABASE_URL connects directly again
  postgres upgrade [status]        PostgreSQL major version and the last upgrade
  postgres upgrade check [--to n]  run the read-only preflight report
  postgres upgrade start [--to n] [-y]
                                   back up, then upgrade in place with pg_upgrade (database stops meanwhile)
  postgres upgrade revert [-y]     restore the pre-upgrade backup and previous major

Configuration as code and migration:
  config import [--file path] [--format ts|json|yaml] [--include-variables] [--force] [--json] [-e env]
                                   write the linked environment to .keel/aven.ts (alias: pull)
  config plan [--file path] [-e env] [--json] [--show-values] [--detailed-exit-code] [--out plan.json]
                                   diff .keel/aven.ts|json|yaml against the environment (alias: preview)
  config apply [--file path] [-e env] [-y] [--confirm-destructive] [--deploy] [--json] [--plan plan.json]
                                   apply the plan as staged changes (--deploy: also deploy them)
  config partials [list] | release <partial> | transfer <from> <to> [--resource addr]... [--dry-run] [-y]
                                   inspect or change named partial ownership
  import dokploy scan|plan|apply|verify [--node srv=node] [--skip proj/name] [--move proj/name=proj]
                 [--local-addr ip] [--project p] [--keep-standby] [--keep-names] [--out plan.json] [--json] [-y]
                                   move a Dokploy install into aven (DOKPLOY_URL, DOKPLOY_API_KEY; Dokploy is only read)
  import dokploy export-compose --dir DIR [proj/name ...]   write stacks that stay outside aven as plain compose
  templates [list] [--q text] [--category c] [--tag t] [--sort popular|new]
  templates show <key>             services, inputs and readme of a template
  templates deploy <key> [--input K=V]... [-e env]
                                   deploy a template into the linked project/environment
  templates publish <file.json> [--workspace id] [--visibility private|workspace|public] [--key k]
  templates export <key>           print a template definition (JSON)
  templates export-env [--secrets generate|inputs] [-e env] > file.json
                                   generate a template definition from the linked environment

Observability and feature flags:
  usage [--from D] [--to D] [--group service|environment|day] [--all]
                                   resource usage of the linked project (--all: per project, workspace admin)
  trace on|off|status|list|get [TRACE_ID] [-s service] [-e env] [-p project]
                                   enable per-service tracing or inspect traces (--all, --filter, --json)
  flag list [--full]               feature flags (project from KEEL_TOKEN or link; -e env, --scope project:<id>, --json)
  flag set <name> <value> [--when expr] [--rule-id id] [--type bool|string|number|json] [--force]
                                   set the default, or add/replace a targeting rule with --when
  flag unset <name> --rule-id id   remove a targeting rule
  flag delete <name>               delete a flag in every environment (alias: rm)

Access and automation:
  tokens [list] [--json]           list your API tokens
  tokens create --name n [--workspace id | --instance | --project p [--environment e]] [--role member] [--expires-days n]
                                   create a scoped API token (printed once)
  tokens revoke <id>               revoke an API token
  github status|connect|disconnect|repos [q]|webhook <repo>...
                                   GitHub connection (OAuth) used for private clones, commit statuses and repo webhooks
  api [-F key=value]... [--variables JSON|@file] [--raw] <query | @file>
                                   send a request to the GraphQL API at /graphql/v2 (stdin when query is omitted)
  mcp                              serve the hosted MCP endpoint over stdio (KEEL_TOKEN or the aven login session)
  mcp install [--agent claude-code|cursor|vscode]...
                                   add the aven MCP server to the MCP client config in this directory
  agent [-p prompt] [--json] [-y]  chat with Aven Agent (interactive without -p; -y approves actions)
  agent --list | --thread-id <id>  list previous agent threads / continue one
  completion bash|zsh|fish|powershell
                                   print a shell completion script
  version [--json]                 print CLI and server versions
  help [command]                   this help, or one command with examples

Most service commands accept -s/--service and -e/--environment to target something other than the link.
Environment: KEEL_URL, KEEL_TOKEN, KEEL_CONFIG_DIR, NO_COLOR. Exit codes: 0 ok, 1 error, 2 usage, 130 interrupted.
Docs: docs/cli.md
`

// newFlagSet is flag.NewFlagSet with ExitOnError and, on a bad flag or -h, the command's own help
// (usage, examples) instead of package flag's raw listing.
func newFlagSet(name string) *flag.FlagSet {
	fl := flag.NewFlagSet(name, flag.ExitOnError)
	top, _, _ := strings.Cut(name, " ")
	fl.Usage = func() {
		if cmd := lookupCommand(top); cmd != nil {
			printCommandHelp(fl.Output(), cmd.names[0])
			return
		}
		fmt.Fprintf(fl.Output(), "usage of aven %s:\n", name)
		fl.PrintDefaults()
	}
	return fl
}
