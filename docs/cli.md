# aven CLI

`cmd/aven` is a CLI for aven-server. Build it with `go build ./cmd/aven` (set the version with `-ldflags "-X main.version=v1.5.0"`).

## Install

aven-server publishes the CLI itself: the dashboard's CLI page (`/cli/`, rail icon) lists the binaries it has with size and SHA-256 and shows the commands below for that server.

- Linux / macOS: `curl -fsSL https://aven.example.com/install.sh | sh -s -- --server https://aven.example.com` downloads `aven-<os>-<arch>` over HTTPS, checks it against the `.sha256` file and installs it to `~/.local/bin` (`/usr/local/bin` as root; `--dir DIR` or `AVEN_INSTALL_DIR` to change).
- Windows: `Invoke-WebRequest https://aven.example.com/downloads/aven-windows-amd64.exe -OutFile keel.exe`.
- Server side: `make clis` builds `bin/aven-{linux,darwin}-{amd64,arm64}` and `bin/aven-windows-{amd64,arm64}.exe`; copy them next to the agent binaries (`AVEN_AGENT_BIN_DIR`, default: next to aven-server). Missing ones are simply not listed.

## Auth and config

- `aven login [--email you@example.com] <url>` logs in and stores the session cookie. The password comes from `AVEN_PASSWORD` or a prompt; `--email` (or `AVEN_EMAIL`) selects a user account, without it the built-in owner account is used (password = `AVEN_ADMIN_PASSWORD`). When the account has 2FA the CLI asks for the code (or reads `AVEN_TOTP`; a recovery code also works). If `AVEN_TOKEN` is set, every request uses `Authorization: Bearer $AVEN_TOKEN` instead (the stored cookie is ignored); this accepts both the legacy `AVEN_API_TOKEN` and scoped `keel_…` API tokens.
- When SAML SSO is enforced for the account's email domain, `aven login` with a password fails with `this account must sign in with SAML SSO`: sign in through the dashboard's SSO button, create an API token in settings and use `AVEN_TOKEN=<token> aven login <url>`.
- `aven tokens [list]`, `aven tokens create --name <n> [--workspace <id> | --instance | --project <name|id> [--environment <name|id>]] [--role admin|member|viewer] [--expires-days n]` (prints the token once on stdout), `aven tokens revoke <id>`. `--environment` without `--project` uses the linked project. Token management needs a session or the legacy `AVEN_API_TOKEN`; a scoped token cannot create tokens.
- Sessions created before v1.5 (HMAC cookies) are no longer valid: run `aven login` again.
- Passkeys work only in the dashboard; for an account whose only second factor is a passkey, `aven login` asks for a recovery code instead. When the workspace requires 2FA and the account has none yet, `aven login` warns and other commands fail with `this workspace requires two-factor authentication …` until it is set up in the dashboard (Account → Security). API tokens (`AVEN_TOKEN`) are not affected.
- `AVEN_URL` overrides the stored server URL.
- Config file: `<user config dir>/keel/config.json` (mode 0600), e.g. `~/.config/keel/config.json` on Linux, `%AppData%\aven\config.json` on Windows. It holds the URL, the session cookie and the directory links.
- A directory link is project + environment + (optional) service. Commands look for a link in the current directory and its parents.
- `aven logout` ends the stored session on the server and clears it locally (it does not unset `AVEN_TOKEN`). `aven whoami` prints the user, email, instance role, server and auth method (plus the token's scope/role for scoped tokens).

## Targeting

Instance and workspace roles are independent. Workspace tokens bind to one workspace; `--instance` creates a token for instance operations only. Omitting `--workspace` on project/template creation or workspace-token creation selects `ws_default` for session callers. New accounts should pass their own workspace ID. A workspace token uses its bound workspace for project/template creation. See [Permissions and workspaces](permissions.md) for migration details. Duplicate project names across workspaces must be selected by ID.

Most service commands take `-s/--service <name|id>` and `-e/--environment <name|id>` to act on something other than the link, without changing it.

## Commands

| Command | What it does |
| --- | --- |
| `aven init <name> [--workspace id] [--description t]` | Create a project and link this directory to its default environment |
| `aven link [-p project] [-e env] [-s service]` | Link this directory (long forms `--project/--environment/--service`). With one service it is picked automatically; with several and no `-s`, the link has no service until `aven service use` |
| `aven status` | Linked project/environment/service, instance status, latest deployment, replicas, domains, TCP proxy, pending changes |
| `aven config import [--format ts\|json\|yaml] [--file f] [--include-variables] [--force] [--json] [-e env]` | Write the linked environment as `.keel/aven.ts` (alias `pull`; also writes `.keel/keel-iac.mjs`, its types and a `package.json`). Variables become `preserve()`; `--include-variables` inlines non-sealed values (warns) |
| `aven config plan [--file f] [-e env] [--json] [--show-values] [--detailed-exit-code] [--out plan.json]` | Evaluate the nearest `.keel/aven.ts` (or `aven.json`/`aven.yaml`) and print what would change (alias `preview`). `--detailed-exit-code`: exit 2 when changes are pending |
| `aven config apply [--file f] [-e env] [-y] [--confirm-destructive] [--deploy] [--json] [--plan plan.json]` | Plan, confirm and apply as staged changes (`--deploy` also deploys). Non-interactive destructive applies need `--confirm-destructive`; `--plan` applies a pinned `plan --out` file without re-evaluating |
| `aven import dokploy scan\|plan\|apply\|verify [--node srv=node] [--skip proj/name] [--move proj/name=proj] [--local-addr ip] [--project p] [--keep-standby] [--keep-names] [--github-ssh] [--out plan.json] [--json] [-y]` | Move a Dokploy install into aven. `git@github.com:` URLs become https and clone with aven's GitHub connection unless `--github-ssh` (then each service gets a deploy key). Reads Dokploy with GET only (`DOKPLOY_URL`, default `http://127.0.0.1:3000`; `DOKPLOY_API_KEY` or `--dokploy-key-file`, never argv). `scan` = plan without asking aven; `plan` also checks nodes and runs the IaC plan / compose preview for projects that exist; `apply --yes` creates `[ready]` services through IaC partial `dokploy` (plus volumes, deploy keys, registries, shared variables) and compose stacks, all without deploying, and refuses destructive plans; `verify` compares every variable value (after reference rewriting), domain and stack exactly. Plans never contain secret values. Run `aven import dokploy --help` for options. |
| `aven github status\|connect\|disconnect\|repos [q]\|webhook <repo>...` | GitHub connection (OAuth App, see api-v1 "GitHub"): `connect` prints/opens `<url>/api/github/oauth/start` for a browser logged in as workspace admin; `repos` lists repositories the account sees; `webhook` creates or updates aven's webhook on each repository (owner/name or URL) |
| `aven import dokploy export-compose --dir DIR [proj/name ...]` | Write stacks that stay outside aven (default: blocked/needs-decision ones) in Dokploy's layout (`<appName>/code/docker-compose.yml`, `.env` 0600, `../files/...`) to run with `docker compose -p <appName>` |
| `aven config partials [list] \| release <partial> \| transfer <from> <to> [--resource ADDR]... [--dry-run] [-y] [--json] [--base-config-etag t]` | Inspect or change named partial ownership (metadata only) |
| `aven environment [list]` | List environments (`*` = linked). Alias `aven env` |
| `aven environment new <name> [--copy-from env]` | Create an environment (optionally copying settings/variables) and switch to it |
| `aven environment use <name>` | Switch the linked environment |
| `aven environment delete <name> [-y]` | Delete an environment (not the linked one). Asks unless `-y` |
| `aven environment sync [target] --from <env> [--deploy] [-y]` | Print what differs between `<env>` and the linked (or `target`) environment (settings, variables with masked values, shared variables, missing volumes), then apply it after confirmation (`-y` skips). Changes are staged; `--deploy` also deploys the changed services. Domains, TCP proxy, node placement, deploy keys and database passwords are never synced |
| `aven environment restrict [name]` / `aven environment unrestrict [name]` | Restrict the linked (or named) environment so only project admins can access its services, variables, logs and settings (Environment RBAC; git push deploys continue). Project admins only. `aven environment list` marks restricted environments |
| `aven service [list]` / `aven service use <name>` | List services / switch the linked service |
| `aven add --database postgres\|mysql\|redis\|mongo` | Add a database service (first deploy starts in the linked environment) and link it |
| `aven add --database bucket` (alias `s3`) | Add an S3-compatible bucket service (MinIO, volume at `/data`, variables `BUCKET`, `ENDPOINT`, `ACCESS_KEY_ID`, `SECRET_ACCESS_KEY`, `REGION`, `AWS_*`) and link it |
| `aven add --image <img> [--name n]` | Add an image service |
| `aven add --repo <url> [--branch b] [--name n]` | Add a repo service |
| `aven up [-d]` | Upload this directory (tar.gz, skips `.git`, `node_modules`, ...) and follow the build |
| `aven redeploy [-y]` | Redeploy the latest deployment (same image, current variables); `--force-build` starts a deployment from the latest commit and builds it even if a retained image matches |
| `aven restart` | Restart the running containers |
| `aven scale [<n>]` | Without `n`: running/configured replicas, placement and autoscale status. With `n`: change the replica count now, without a redeploy (`POST .../scale`; waits for the new replicas' healthchecks) |
| `aven scale --auto --max 5 [--min 1] [--cpu 70] [--memory 80] [--rps 50] [--up-cooldown 60] [--down-cooldown 300]` / `aven scale --off` | Turn horizontal autoscaling on (needs at least one target) or off |
| `aven ha [status]` | Database high availability: cluster members with tier, engine role (primary/replica), health, replication lag and node; before conversion, whether the service can be converted, the selector options and what blocks it |
| `aven ha convert [--replicas n] [--edge n] [--internal n] [--no-auto-failover] [--skip-backup] [-y]` | Convert the linked postgres/redis/mongo service into an HA cluster (asks unless `-y`). Prints which services had their references moved to the cluster endpoint |
| `aven ha scale --replicas n` / `aven ha switchover --to <member>` / `aven ha auto-failover on\|off` / `aven ha revert [-y]` | Change the replica count, move the primary (Make Leader), toggle Aven's automatic postgres failover, or revert to a standalone database (only while the original service is primary; member volumes are kept) |
| `aven down [-y]` | Stop the service (`POST .../stop`); it stays down until the next deploy |
| `aven logs [-n 200] [--filter q] [--deployment id]` | Runtime logs of the current (or given) deployment; follows while it is live. `--filter` uses the server syntax: words, `"phrase"`, `-exclude` |
| `aven logs --build [--deployment id] [--filter q]` | Build logs of the latest (or given) deployment (`-b` also works) |
| `aven logs --dns [--domain d] [--qname n] [--qtype A] [--rcode NXDOMAIN] [--zone internal\|external] [--status ok\|failed]` | DNS lookups made by the service (columns Time, Zone, Type, Rcode, Name, Answers). Streams by default; `-n/--lines`, `-S/--since` or `-U/--until` (`30m`, `2h`, `1d`, RFC3339) fetch history instead. `--qtype`/`--rcode` are upper-cased. `--json` prints newline-delimited JSON (the API record). Not per deployment: `--build`/`--deployment` are rejected |
| `aven logs --network [--protocol tcp\|udp] [--direction ingress\|egress] [--peer p] [--peer-kind k] [--port n] [--status ok\|refused\|timeout\|reset] [--dropped true] [--src ip] [--dst ip] [--host ip]` | Connection (flow) logs of the service (columns Time, Dir, Proto, Source, Destination, Peer, Traffic, Latency, Status). Same `--lines/--since/--until/--json/--filter` as `--dns`. Typed flags become `@field:value` filter terms and combine with `-f/--filter` (see `docs/api-v1.md`, "DNS query and network flow logs") |
| `aven variables` | Raw variables as an aligned table (alias `aven vars`, which keeps the `KEY=VALUE` output) |
| `aven variables --set K=V --unset K [--deploy]` | Change variables (repeatable). `--deploy` applies them right away. The old `vars set K=V unset K` form still works |
| `aven variables --kv` | `KEY=VALUE` output |
| `aven variables --seal KEY [--seal KEY]...` | Seal variables (`sealed_variables` in PATCH). Irreversible: the value is never shown again by any API or CLI command but is still injected at deploy time and usable in references. Delete and recreate to unseal. Can be combined with `--set` in the same call |
| `aven variables --env` | Print the variables as a `.env` file (`GET .../variables/raw?format=env`). Sealed variables appear only as `# KEY=<sealed>` comments |
| `aven variables --import <file>` | Set every variable in a `.env` file (repeatable, `-` reads stdin, max 4 MiB). Supports comments, `export ` prefix, single/double/backtick quotes, escapes and multiline values. Explicit `--set`/`--unset` win over imported keys. Combine with `--deploy` to apply |
| `aven run [--] <cmd...>` | Run a local command with the service's **resolved** variables (`GET .../variables/resolved`) plus `AVEN_*` link info. Exit code is passed through; values are never printed. Sealed variables (and values derived from them) are not available locally; their names are listed on stderr |
| `aven dev [up] [-e environment] [--dry-run] [-v]` | Run the linked project/environment locally (alias `aven develop`, `up`/`start`). Repo services run their start command (or the local override from `aven dev configure`) in their root directory with the **resolved** variables; image/database services run with local Docker (`docker run`, images pulled when missing) on a temporary network where the private hostnames are network aliases. Each service gets `localhost:<port>` (its configured/`PORT` port, or a free one if busy). Private hostnames (`<service>.<env>.aven.internal[:port]`) in variables are rewritten to `localhost:<local port>` for local processes and to `host.docker.internal:<local port>` for containers that call local code services. Start order: databases → images → code, waiting until each container accepts connections. Output is multiplexed as `<service> \| line` (colored on a terminal; `NO_COLOR` disables). Ctrl-C stops everything gracefully (SIGTERM to process groups, `docker stop -t 10`); a second Ctrl-C forces it. `--dry-run` prints the plan without starting anything (no variable values); `-v` lists host replacements and the rewritten variable names. `--no-tui` is accepted for Railway compatibility (logs always stream) |
| `aven dev down [-e environment]` | Stop and remove leftover `aven dev` containers/networks of the project (all environments unless `-e`). Found by the Docker labels `keel.dev.project` / `keel.dev.environment` (alias `stop`) |
| `aven dev clean [-e environment] [-y]` | Like `down`, and also delete the local Docker volumes (`keel-dev-<volume-id>`) that hold local data (alias `reset`) |
| `aven dev configure [--service name] [--command cmd] [--dir path] [--port n]` | Set how a code service runs on this machine only (alias `config`); without flags it prompts for every code service (empty keeps, `-` clears). `--dir` is relative to the linked directory (absolute paths allowed). Stored in `dev.json` next to the CLI config. `--remove [service]` deletes the override(s) |
| `aven shell` | Open a local subshell (`$SHELL`, `%COMSPEC%` on Windows) with the same variables and `AVEN_SHELL=1` |
| `aven ssh [-r replica] [-- cmd...]` | Interactive shell inside a running replica over the exec WebSocket (see `docs/api-v1.md`, "Service shell"). Without `cmd` it starts bash or sh; with `cmd` it runs `sh -lc '<cmd>'`. Exit code is passed through |
| `aven connect [service] [--enable-proxy]` | Start `psql` / `mysql` / `redis-cli` / `mongosh` against a database service through its TCP proxy (`*_PUBLIC_URL`). If the proxy is off, it tells you to enable it, or enables it with `--enable-proxy` (on the database's default port; 409 on Cloudflare tunnel nodes, which have no public address) |
| `aven postgres pooling [status] [--json]` | PgBouncer connection pooling of a postgres service (aliases `aven pg`, `pgbouncer`): status, pool mode, pool sizes and which connection variables exist |
| `aven postgres pooling enable [--pool-mode transaction\|session\|statement] [--default-pool-size n] [--max-client-conn n] [--max-prepared-statements n]` | Add PgBouncer (alias `add`; pool mode defaults to `transaction`) and wait until it is live and `DATABASE_URL` points at it (`--detach` returns right away) |
| `aven postgres pooling configure [same flags] [--default-prepared-statements]` | Change PgBouncer settings (redeploys only PgBouncer, not the database) |
| `aven postgres pooling disable [-y]` | Remove PgBouncer (alias `remove`); `DATABASE_URL` points directly at PostgreSQL again |
| `aven postgres upgrade [status]` | Running major, last upgrade, backups and whether a revert is still possible |
| `aven postgres upgrade check [--to n]` | Read-only preflight report (`[FAIL]` = blocker, `[warn]` = warning); exit code 1 when not ready |
| `aven postgres upgrade start [--to n] [-y] [--detach]` | Run the preflight, then (after confirmation) back up the volume, upgrade in place with pg_upgrade and follow the phases. Default target: newest supported major |
| `aven postgres upgrade revert [-y] [--detach]` | Restore the pre-upgrade backup and previous image (writes since the upgrade are lost) |

| `aven pitr [status]` | Point-in-time recovery of the linked Postgres/MySQL service: on/off, retention, storage, restore window, archive size, last base backup/sync, errors, last restore. `aven postgres pitr …` and `aven mysql pitr …` are aliases |
| `aven pitr enable [--retention-days 7] [--destination <id>]` / `aven pitr disable [-y]` | Start/stop continuous WAL (Postgres) or binlog (MySQL) archiving to the backup storage (`--destination ""` = local). Enabling Postgres may restart the database once. Disabling keeps the archive |
| `aven pitr base` | Take a base backup now (otherwise daily) |
| `aven pitr restore --at <time> [--new-service-name n \| --in-place] [-y] [--no-wait]` | Restore to a moment: `--at` is RFC3339, local `YYYY-MM-DD HH:MM[:SS]` or a duration ago (`30m`, `2h`, `1d`, `1w`). Default: a new service `<name>-restored-YYYYMMDD-HHMM` with the source's variables; `--in-place` overwrites this service. Asks unless `-y`, then follows the restore steps until it finishes |
| `aven pitr delete-archive [-y]` | Delete the stored base backups and logs (PITR must be off) |

| `aven netdiag <host> [port] [-r replica] [--json]` | Network diagnostics from inside a running replica: resolves `host` (getent/nslookup) and, with a port (also `host:port`), tests a TCP connection (nc or bash). Prints both results and exits 1 if one failed |

| `aven cdn [status\|enable\|disable] [--json]` | CDN caching of the service (`status` shows availability, HTML caching, default TTL, SWR, purge on deploy and last purge times). Needs a public domain to enable |
| `aven cdn update [--html-caching auto\|force\|never] [--default-ttl 30m\|1h\|2h\|4h\|12h\|1d] [--swr\|--no-swr] [--purge-on-deploy off\|html\|all]` | Change CDN settings (`--default-ttl` also takes the matching seconds) |
| `aven cdn purge html\|all` | Purge cached HTML (static assets stay) or everything |
| `aven waf [status] [--json]` | Under Attack Mode state, allow/deny IP lists, rate limit and managed rules |
| `aven waf update [--allow-ip ip]... [--deny-ip ip]... [--clear-allow] [--clear-deny] [--rate-limit 100/60s\|off] [--managed-rules on\|off]` | Change WAF settings. `--allow-ip`/`--deny-ip` are repeatable or comma separated and replace the list |
| `aven waf under-attack status\|enable [--duration 1h\|3h\|12h\|24h]\|disable [--json]` | Manage WAF under-attack mode. Without `--duration` it stays on until disabled |
| `aven edge rules [get]` / `aven edge rules set <file.json\|->` | Print or replace the edge rules (Railway-compatible ruleset JSON, see `docs/api-v1.md` "Edge"). The server validates the whole ruleset and changes nothing on error |
| `aven domain [host] [--port n]` | Add a generated domain (no host) or a custom domain and print the DNS records to set |
| `aven domain list [--json]` | Domains of the service: host, kind, status (`active`, `proxied`, `pending_dns`, `error`), port (target port, or the service port) and proxy (`direct`, `cloudflare`, `tunnel`, `none`). Alias `aven domains`; `add` is also accepted as `aven domain add [host]` |
| `aven domain status <host> [--json]` | One domain in detail: verification method (`legacy`/`dns`/`http`/`txt`, or the TXT record to add when unverified), proxy, port, TLS mode, every DNS record to set with its status (`ok`/`proxied`/`mismatch`/`pending`) and the current value, the certificate (status, expiry), last check, last error and warnings. `*` marks an A/AAAA alternative to the recommended CNAME |
| `aven domain port <host> <n\|auto>` | Change the target port (`PATCH …/domains/{host}`); `auto` clears it so the domain follows the service port |
| `aven domain verify <host> [--json]` | Re-check DNS, ownership and the certificate now (`POST …/domains/{host}/verify`) and print the status |
| `aven domain rename <old> <new>` | Rename a generated domain. `<new>` may be just the first label (`aven domain rename web.keel.example.com shop` → `shop.keel.example.com`); the suffix cannot change. Custom domains cannot be renamed |
| `aven domain rm <host> [-y]` | Remove a domain (asks unless `-y`; aliases `remove`, `delete`) |
| `aven tcp-proxy [status] [--json]` | TCP proxy of the service: `host:public_port → container port`, or off; on a node without a public address (Cloudflare tunnel) it says why it is unavailable |
| `aven tcp-proxy enable [--port n]` | Open a TCP proxy to container port `n` (default: the service's effective port from `aven networking`). Fails with 409 on tunnel nodes |
| `aven tcp-proxy port <n>` | Change the container port; the public host and port stay the same |
| `aven tcp-proxy disable [-y]` | Close the TCP proxy and release its public port (asks unless `-y`) |
| `aven networking [--json]` | The Settings > Networking overview (`GET …/networking`, alias `aven net`): detected ports with their source (`listening`, `expose`, `env`, `loopback` with a warning), the effective port, public domains (status, port, Cloudflare), the TCP proxy, the private name with aliases and addresses (and ports open to other nodes), and outbound IPv6 (the service setting, whether the node has IPv6 egress, whether it can block it, and the node's static IPs) |
| `aven open [--print]` | Print and open the dashboard URL of the linked service |
| `aven usage [--from D] [--to D] [--group service\|environment\|day]` | Resource usage of the linked project as a table (CPU core·hours, memory GB·hours, egress GB, disk GB·hours, estimated cost when the server has `AVEN_PRICE_*`). `D` is `YYYY-MM-DD` (UTC; `--to` = end of that day) or RFC3339; default is this month. `--all` prints per-project workspace usage (workspace admin) |
| `aven trace on\|off [-s service \| --all] [-e env] [-p project] [--json]` | Turn tracing on/off for the linked (or given) service, or every non-database service with `--all` (aliases `enable`/`disable`; command aliases `aven traces`, `aven tracing`). The edge traces at once; `OTEL_*` variables arrive on the next deploy |
| `aven trace status [--all] [--json]` | Whether each service is traced and when the edge and the app last exported a span |
| `aven trace list [--all] [-f filter] [--errors] [-S 30m] [-U time] [-n 100] [--json]` | Traces of the linked service (or the whole environment with `--all`), newest first: start, duration, spans, errors, root span, service, trace id. `--filter` uses the Traces page syntax (`@http.route:/api @duration:>500`). `--json` prints one summary per line |
| `aven trace get <TRACE_ID> [--max-spans 1000] [--json]` | One trace as an indented span tree (name, component · service, duration, ERROR). Accepts a `traceparent`. `--json` prints one span per line (alias `show`) |
| `aven templates [list] [--q text] [--category c] [--tag t] [--sort popular\|new]` | Built-in and visible user templates (alias `aven template`) |
| `aven templates show <key>` | Services, inputs (required/secret/default) and readme |
| `aven templates deploy <key> [--input K=V]... [-e env]` | Deploy into the linked project (and environment, or `-e`). Missing required inputs and pattern mismatches come back as one error listing every problem |
| `aven templates publish <file.json> [--workspace id] [--visibility private\|workspace\|public] [--key k]` | Save a definition as a user template (`private` by default). The file may be a bare definition or the `{definition, ...}` output of `export-env` |
| `aven templates export <key>` | Print a template definition (JSON) |
| `aven templates export-env [--secrets generate\|inputs] [-e env] > file.json` | Generate a definition from the linked environment (not saved). Secret-looking values become `${{ secret(32) }}` (default) or required inputs; warnings go to stderr. Review, then `publish` |
| `aven api [-F key=value]... [--variables JSON\|@file] [--operation-name n] [--raw] <query \| @file \| ->` | Send a GraphQL request to `/graphql/v2` (see [`docs/graphql.md`](graphql.md)) and print the JSON response. Without a query, or with `-`, it reads stdin. `-F` values `true`/`false`/`null`/numbers are sent as JSON, everything else as a string. The command exits non-zero when the response has `errors` |

| `aven flag list [--full]` | Feature flags of the project (alias `ls`): name, type, default, rule count; `--full` adds rule IDs, conditions and served values. The project comes from `--scope project:<id>` (or `AVEN_FLAGS_SCOPE`), else from a project/environment `AVEN_TOKEN`, else from the link; `-e env` picks the environment. All `flag` subcommands take `--json` |
| `aven flag set <name> <value> [--type bool\|string\|number\|json] [--force]` | Create a flag or set its default in the environment. The type is inferred for new flags (`true`/`false` → bool, number → number, other JSON → json, anything else → string); existing flags keep their type. Changing it needs `--force --type t` and clears the rules in every environment |
| `aven flag set <name> <value> --when '<expr>' [--rule-id id]` | Add a targeting rule (the flag must exist), e.g. `--when 'plan == "enterprise" && bucket(key) < 0.25'`. Reusing a rule ID replaces that rule; without `--rule-id` the ID is derived from the name and expression. Expression syntax: docs/api-v1.md, "Feature flags" |
| `aven flag unset <name> --rule-id id` | Remove a targeting rule |
| `aven flag delete <name>` | Delete the flag in every environment (aliases `rm`, `remove`) |

| `aven agent` | Interactive chat with Aven Agent (the dashboard AI assistant; see `docs/api-v1.md`, "Aven Agent"). Type `exit` or Ctrl+C to quit. Uses the link (project/environment/service) as context when there is one; `-s/-e` override it. Actions (restart, redeploy, variables, rollback) ask `실행할까요? [y/N]` before they run |
| `aven agent -p "<prompt>" [--json] [-y]` | One prompt, streamed answer, then exit (the prompt can also be given as plain arguments). `--json` prints `{threadId, response, toolCalls: [{toolName, args, result?, isError}]}`. Without a terminal, actions are declined unless `-y` approves them |
| `aven agent --list [--json]` / `aven agent --thread-id <id>` | List your agent threads (of the linked project) / continue one: the last 10 messages are replayed, a pending action is offered for approval, then interactive mode starts |

| `aven sandbox create [--image img] [--checkpoint name] [--idle-timeout-minutes n] [--memory-mb n] [--cpu n] [--private-network] [--variable K=V]... [--env-file f]... [--node id] [--json]` | Create a sandbox in the linked (or `-e`) environment and make it the **active sandbox** (aliases `sandboxes`, `sbx`; `new`). `--idle-timeout-minutes 0` disables idle destruction. `--variable` is repeatable and comma-separable (split only when every part is `KEY=VALUE`); `--variable` wins over `--env-file` |
| `aven sandbox fork [id] [same options except image/checkpoint]` | Fork the active (or given) sandbox's filesystem into a new one, which becomes active. Settings are not inherited |
| `aven sandbox list [--json]` | Sandboxes in the environment; `*` marks the active one (alias `ls`) |
| `aven sandbox exec [--id id] [--timeout s] [--cwd dir] -- <command...>` | Run a command (one argument = shell command, so pipes work; several = argv). Piped stdin is sent (≤ 256 KiB). Prints stdout/stderr when it finishes and exits with the command's code; `124` when `--timeout` (max 110 s) expires |
| `aven sandbox ssh [--id id] [-- command...]` | Interactive shell over the sandbox shell WebSocket (aliases `connect`, `shell`) |
| `aven sandbox file ls [path] / read <path> / write <path> [local-file\|-] [--mode 0755] [--id id]` | Browse, download (stdout) or upload (≤ 256 KiB, stdin by default) files |
| `aven sandbox checkpoint create <name> [--id id] / list / rename <old> <new> / delete <name> [--json]` | Save the active sandbox's filesystem under a name (reusing a name replaces it), boot later with `create --checkpoint <name>` |
| `aven sandbox destroy [id]` | Destroy the active (or given) sandbox (aliases `rm`, `delete`) |
| `aven version` | CLI version and, if logged in, server version |
| `aven mcp` | Serve the hosted Aven MCP endpoint (`/mcp`) over stdio for an MCP client. Uses `AVEN_TOKEN` if set, otherwise the `aven login` session; `aven login` or `AVEN_URL` supplies the server URL |
| `aven mcp install [--agent claude-code\|cursor\|vscode]...` | Merge a `aven` stdio server (`aven mcp`) into `.mcp.json`, `.cursor/mcp.json` or `.vscode/mcp.json` in the current directory, keeping other servers. Without `--agent`: Claude Code, plus Cursor/VS Code if their directory exists |
| `aven help` | All commands |

## Notes

- **Secrets stay out of argv.** `aven connect` passes passwords through environment variables only (`PGPASSWORD`, `MYSQL_PWD`, `REDISCLI_AUTH`; for mongosh the URL is in `AVEN_MONGO_URL` and read by `--eval "db = connect(process.env.AVEN_MONGO_URL)"`). The `cmd` of `aven ssh` is sent to the server but not logged.
- **Terminal mode.** `aven ssh` puts the local terminal into raw mode (termios on Linux/macOS/BSD, console VT mode on Windows; implemented with `golang.org/x/sys`, no extra dependency) and forwards window size changes (SIGWINCH, or polling every 500 ms on Windows). If stdin is not a terminal it falls back to line mode (bytes are forwarded as they are read).
- **Ctrl-C** in `aven run`/`aven shell` goes to the child process; the CLI waits for it and returns its exit code.
- `aven dev` needs Docker only when the project has image/database services. Code services without a start command (and function services) are skipped with a hint. Sealed variables remain unavailable locally and their names are printed to stderr; an account that only sees masked values cannot run `aven dev`. Docker volumes attached to a service are kept between sessions as `keel-dev-<volume-id>` (remove with `aven dev clean`). Secret values are passed to `docker run` through the environment (`-e KEY`), never argv. If a port was busy and got remapped, separate port variables such as `PGPORT` still contain the original port (URLs with `host:port` are rewritten).
- `aven connect` needs the client binary in `PATH`.
- `aven mcp` forwards each newline-delimited JSON-RPC message to `POST /mcp` (adding the `MCP-Protocol-Version`/`Mcp-Method`/`Mcp-Name` headers) and writes only protocol responses to stdout; diagnostics go to stderr. To limit what an agent can do, give it a scoped token instead of your session, e.g. `AVEN_TOKEN=$(aven tokens create --name mcp --project <project> --environment <environment> --role member)`.


## Infrastructure as Code (`aven config`)

Project-level IaC: one file describes the services of an environment, `plan` diffs it against the server, `apply` saves the changes as staged changes (shown as pending changes until deployed). See `docs/api-v1.md` ("Infrastructure as Code") for the exact semantics.

- **Files.** `plan`/`apply` use the nearest `.keel/aven.ts` (also `.mts`, `.js`, `.mjs`), `.keel/aven.json` or `.keel/aven.yaml`/`.yml`, searching the current directory and its parents; keep only one. `--file` overrides.
- **TypeScript.** `aven.ts` is evaluated with `bun` if installed, else `node` (22.6+, run with `--experimental-strip-types`); `AVEN_IAC_RUNNER=bun|node|<path>` picks one. It imports the DSL from `./keel-iac.mjs` (written by `aven config import`, no npm package needed):
  ```ts
  import { defineAven, github, postgres, preserve, project, service } from "./keel-iac.mjs";

  export const partial = "api"; // optional: named partial for multi-repo projects

  export default defineAven((ctx) => {
    const db = postgres("db");
    const api = service("api", {
      source: github("acme/api", { branch: "main", rootDirectory: "apps/api" }),
      start: "pnpm start",
      healthcheck: "/health",
      replicas: ctx.isEnvironment("production") ? 3 : 1,
      domains: ["api.acme.dev", { domain: "admin.acme.dev", port: 8081 }],
      env: { DATABASE_URL: db.env.DATABASE_URL, SENTRY_DSN: ctx.shared.SENTRY_DSN, JWT_SECRET: preserve() },
      settings: { wait_for_ci: true }, // any other instance setting, by its API name
    });
    return project("acme", { resources: [api, db] });
  });
  ```
  Helpers: `service`, `postgres`/`mysql`/`redis`/`mongo`/`bucket` (or `database(kind, name)`), `functionService`, `group` (structural, flattened), `github`/`git` and `image` sources, `preserve()`. Options map to instance settings (`start`→`start_command`, `preDeploy`, `healthcheck`, `healthcheckTimeout`, `replicas`, `placement`, `port`, `builder`, `dockerfilePath`, `watchPaths`, `autoDeploy`, `restartPolicy`, `cron`, `sleepWhenIdle`, `memoryLimitMB`, `cpuLimit`, `privatePorts`, `autoscale`, …); unknown options are an error. `x.env.KEY` is `${{x.KEY}}`; `ctx` has `command`, `projectId`, `projectName`, `environmentId`, `environment`, `environmentName`, `isEnvironment(name)`, `shared`.
- **JSON/YAML** use the API definition directly (`{version: 1, partial?, services: [{name, source_type, database_kind, settings, variables}]}`).
- **Semantics.** Omitted settings are left untouched; a service's `env` is its complete variable list (omit `env` to leave variables alone). Without a partial, services missing from the file are deleted (in every environment). With a partial, only services that partial owns are deleted; the first apply claims ownership of everything the file declares.
- **Output.** Variable values are shown as `«hidden»` unless `--show-values`. Destructive lines are marked `!`. A clean import plans "Your Aven configuration is already up to date."
- **CI.** `aven config plan --out plan.json` pins the change set (definition + `plan_hash`); `aven config apply --plan plan.json --yes --confirm-destructive` applies exactly that and fails if the environment changed. Use an environment-scoped `AVEN_TOKEN` (creating/deleting services needs project-level member access).
- **Config as code.** Per-service `aven.json`/`railway.toml` keeps working: it is applied at deploy time on top of what IaC saved, so a setting in both places is decided by the repo file.
- Not managed by IaC yet: volumes (beyond what a new service creates), shared variables (reference them with `ctx.shared`), canvas groups, TCP proxy, service renames.

- **Active sandbox.** `aven sandbox create`/`fork` (and `exec`/`ssh` with `--id`) remember the sandbox per environment in `sandboxes.json` next to the CLI config; commands without `--id` act on it. With `--id` no linked directory is needed.
