# Aven HTTP API v1

This document replaces `docs/api.md` (v0). The `/api/apps*` endpoints are removed.
All paths start with `/api`. Request and response bodies are JSON. Time values are RFC3339 UTC strings.
Errors use the shape `{"error": string}`.
Auth is unchanged from v0: the `keel_session` cookie or `Authorization: Bearer`. `/api/auth/*` is the same as v0.

Instance operations and workspace membership are independent. See [Permissions and workspaces](permissions.md) for the role model, token scopes and migration 41 compatibility changes.

`GET /api/instance/overview` returns administrator operating counts, workspace
inventory and live node connection summaries. It requires instance admin authority;
see [Admin panel](permissions.md#admin-panel) for the response and visibility rules.

`DELETE /api/instance/workspaces/{wsid}` with body `{"confirm": "<workspace name>"}`
deletes a workspace and every project in it. It requires a server admin browser
session (tokens get 403) and the `workspaces` admin capability; see
[Admin panel](permissions.md#admin-panel).

Workspace CPU, memory and volume allocation quotas are managed through
`GET /api/instance/resource-limits`, `PUT /api/instance/resource-limits/{wsid}`
and `GET /api/workspaces/{wsid}/resource-limits`. The PUT body is
`{"cpu_cores": 4, "memory_mb": 8192, "volume_mb": 20480}`; `null` means unlimited,
`0` prevents allocations, and omitted fields become unlimited. Over-limit
allocation writes return HTTP 409. See [Workspace resource limits](permissions.md#workspace-resource-limits)
for authorization, allocation accounting and the volume driver's write-limit caveat.

## MCP server

`POST /mcp` serves the Model Context Protocol over stateless Streamable HTTP (JSON responses, no SSE stream; `GET`/`DELETE` return 405). It accepts the `2026-07-28` per-request metadata protocol (`server/discover`, `_meta` protocol version and client capabilities, matching `MCP-Protocol-Version`/`Mcp-Method`/`Mcp-Name` headers) and the older `initialize` handshake (`2025-11-25`, `2025-06-18`, `2025-03-26`; the client's version is echoed when supported). Authenticate with `Authorization: Bearer <token>` (`AVEN_API_TOKEN` or a `keel_` API token); a session cookie also works for non-browser clients such as `aven mcp`, with the usual cross-origin rejection.

Tools: `whoami`, `list_projects`, `inspect_project`, `list_services`, `list_environments`, `inspect_environment`, `inspect_service`, `list_templates`, `list_deployments`, `inspect_deployment`, `get_logs` (build or runtime, `tail` ≤ 1000, optional `filter`), `get_variables`, `set_variables` (merge; `null` deletes; optional `deploy`), `deploy`, `redeploy`, `restart_service`, `rollback`, `create_service_from_image`, `create_service_from_template`, `add_domain`. Each tool calls the matching `/api` route with the caller's credentials, so token scope and project RBAC apply (hidden resources stay 404, insufficient role is 403) and the error is returned as a tool result with `isError: true`. Sealed variables are always redacted and kept when variables change. Deploys, rollbacks, restarts and variable changes carry `destructiveHint: true`.

`aven mcp` is the stdio bridge for MCP clients (see cli.md). The dashboard's **설정 → AI 도구 · MCP** section shows Claude Code, Cursor and VS Code configuration.

## Model

```
Project ─┬─ Environment (production, staging, ...)
         ├─ Service (source definition: repo / image / database / cron belongs to type)
         │     └─ ServiceInstance = Service × Environment   ← per-environment settings, variables, domains, status
         │            └─ Deployment ─ Replica containers
         ├─ Volume (owned by one ServiceInstance)
         └─ SharedVariables (per Environment)
```
Every **setting is per environment**. `Service` holds only identity: name, icon, source type.
Creating a service creates an instance in every environment. Creating an environment copies settings from a source environment.

## Types

```ts
type ID = string;

type Project = { id: ID; name: string; description: string; created_at: string;
  default_environment_id: ID;
  notify_webhook_url: string | null;   // Discord/Slack-compatible webhook
  notify_events: NotifyEvent[];        // which events are sent
};
type NotifyEvent = "deploy.success" | "deploy.failed" | "deploy.crashed" | "volume.backup.failed";

type Environment = { id: ID; project_id: ID; name: string; created_at: string };

type SourceType = "repo" | "image" | "database";
type DatabaseKind = "postgres" | "mysql" | "redis" | "mongo";

type Service = {
  id: ID; project_id: ID; name: string;       // [a-z0-9-], unique within the project
  icon: string | null;                        // icon key: "postgres" | "redis" | "github" | "docker" | ...
  source_type: SourceType;
  database_kind: DatabaseKind | null;         // only when source_type=database
  template: string | null;                    // template key the service was created from
  created_at: string;
};

type RestartPolicy = "always" | "on_failure" | "never";
type HealthcheckType = "http" | "tcp" | "none";
type Builder = "auto" | "dockerfile" | "railpack";  // auto: Dockerfile if present, otherwise railpack

type ServiceInstance = {
  id: ID; service_id: ID; environment_id: ID;
  // source (per environment)
  repo_url: string | null; branch: string; root_dir: string;      // root_dir: monorepo subdirectory, default "/"
  image: string | null;                                           // source_type=image|database
  image_update_policy?: "disabled" | "patch" | "minor";
  image_update_window?: "anytime" | "weekends" | "night" | "custom";
  image_update_days?: number[]; image_update_start_hour?: number; image_update_end_hour?: number;
  builder: Builder; dockerfile_path: string;                      // default "Dockerfile" (relative to root_dir)
  watch_paths: string[];                                          // gitignore-style; empty = always deploy
  auto_deploy: boolean;                                           // deploy automatically on GitHub push
  skip_builds?: boolean;                                          // default false (omitted); reuse a retained matching build image
  // deploy
  start_command: string | null; pre_deploy_command: string | null;
  port: number | null;                // null: use the PORT env (default 8080)
  healthcheck_type: HealthcheckType; healthcheck_path: string; healthcheck_timeout_s: number;
  restart_policy: RestartPolicy; restart_max_retries: number;
  replicas: number;                   // 1..10; Caddy load-balances across them
  cron_schedule: string | null;       // 5-field cron. When set, the service runs as a job, not a long-lived container
  sleep_when_idle: boolean;           // reserved (not implemented in M1, stored only)
  // networking
  domains: Domain[];
  private_domain: string;             // read-only: "<name>.<env>.aven.internal", <name> = private_dns_name or the service name (also resolves as "<name>" inside the same environment)
  private_dns_name?: string;          // extra private name (staged: applies on the next deploy); see "Networking"
  outbound_ipv6?: boolean;            // false: block outbound IPv6 outside fc00::/7 (applies immediately); absent = on
  tcp_proxy: { container_port: number; public_port: number } | null;   // public_port is assigned by the server
  // variables: raw values (may contain references)
  variables: Record<string, string>;
  // state
  status: "idle" | "deploying" | "online" | "crashed" | "failed" | "offline" | "sleeping" | "scheduled";
  current_deployment_id: ID | null;
  canvas: { x: number; y: number } | null;
  updated_at: string;
};

type Domain = { host: string; kind: "generated" | "custom"; target_port: number | null;
  dns: DNSRecord[];                   // records the user needs to set (custom only, or a generated domain served from 2+ nodes)
  status: "active" | "pending_dns" | "proxied" | "error";
  // read-only status fields (see "Networking" > "Domain status engine")
  proxy: "direct" | "cloudflare" | "tunnel" | "none" | "";        // "" = not checked yet
  effective_port: number;             // port the proxy actually routes to (target_port, else the service port)
  verification?: { method: "legacy" | "dns" | "http" | "txt" | ""; verified_at: string | null;
                   txt: { name: string; value: string } };      // "_aven-verify.<host>", "aven-verify=<token>"
  cert?: { status: "valid" | "invalid"; not_after: string | null };
  checked_at?: string; last_error?: string; warnings?: string[]; };

type DNSRecord = { type: "AAAA" | "A" | "CNAME"; name: string; value: string;
  status?: "ok" | "proxied" | "mismatch" | "pending";   // per record, from the last check
  current: string[];                  // values of this type seen in the last check
  alternative?: boolean;              // an A/AAAA alternative to the recommended CNAME
  note?: string };

type Volume = { id: ID; project_id: ID; environment_id: ID; service_instance_id: ID | null;
  name: string; mount_path: string; size_bytes: number | null; created_at: string };
type Backup = { id: ID; volume_id: ID; size_bytes: number; created_at: string; status: "running" | "done" | "failed"; error: string | null };

type DeploymentStatus = "queued" | "initializing" | "building" | "deploying" | "healthchecking" | "pre_deploy"
  | "live" | "failed" | "crashed" | "cancelled" | "removed" | "skipped";
// removed: replaced by a newer deployment (v0 "superseded"). skipped: the push did not match watch_paths.

type Deployment = {
  id: ID; service_id: ID; environment_id: ID; service_instance_id: ID;
  status: DeploymentStatus;
  trigger: "manual" | "redeploy" | "rollback" | "github" | "variables" | "cli" | "template" | "image_update";
  commit_sha: string | null; commit_message: string | null; commit_author: string | null; branch: string | null;
  image: string | null; builder_used: "dockerfile" | "railpack" | "image" | null;
  build_reused_from?: ID;                                          // original deployment that built the reused image
  replicas: { name: string; status: "starting" | "running" | "exited" | "restarting"; restarts: number }[];
  error: string | null;
  created_at: string; started_at: string | null; finished_at: string | null;
};

type CronRun = { id: ID; deployment_id: ID; started_at: string; finished_at: string | null; exit_code: number | null; status: "running" | "succeeded" | "failed" };

type Event = { id: ID; project_id: ID; environment_id: ID | null; service_id: ID | null;
  kind: string;      // "deploy.started" | "deploy.live" | "deploy.failed" | "deploy.crashed" | "service.created" | "variables.updated" | "volume.created" | "backup.done" | ...
  message: string; created_at: string };

type Template = { key: string; name: string; description: string; icon: string;
  services: { name: string; source_type: SourceType; database_kind?: DatabaseKind; image?: string; repo_url?: string }[] };

type MetricPoint = { t: string; cpu_percent: number; memory_bytes: number; memory_limit_bytes: number;
  net_rx_bytes: number; net_tx_bytes: number; disk_bytes: number | null };   // net values are cumulative counters
```

### Variable rules
- References: `${{ VAR }}` (same instance), `${{ shared.VAR }}`, `${{ <service-name>.VAR }}` (another service in the same environment). Spaces are allowed.
- Injected automatically. These can be referenced, and a user value with the same name overrides them:
  `PORT` (when port is set), `AVEN_SERVICE_NAME`, `AVEN_SERVICE_ID`, `AVEN_ENVIRONMENT_NAME`, `AVEN_ENVIRONMENT_ID`,
  `AVEN_PROJECT_NAME`, `AVEN_PROJECT_ID`, `AVEN_PRIVATE_DOMAIN`, `AVEN_PUBLIC_DOMAIN` (first domain),
  `AVEN_DEPLOYMENT_ID`, `AVEN_GIT_COMMIT_SHA`, `AVEN_GIT_BRANCH`, `AVEN_TCP_PROXY_DOMAIN`, `AVEN_TCP_PROXY_PORT`, `AVEN_VOLUME_MOUNT_PATH`.
- Database services generate their variables on creation. Examples: postgres `PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE DATABASE_URL DATABASE_PUBLIC_URL`; redis `REDIS_URL REDISHOST REDISPORT REDISPASSWORD`; mysql `MYSQL_URL MYSQLHOST ...`; mongo `MONGO_URL ...`.
  Other services use them through `${{postgres.DATABASE_URL}}`.
- When variables change, the instance shows "pending changes". They are applied only through a deploy (the `deploy` query in PATCH).

### Config as code
If the repo root (after root_dir) has `aven.json` or `railway.json` (or `.toml`), the build reads it and overrides the settings for that deployment.
Supported keys use the railway.json schema: `build.builder`, `build.dockerfilePath`, `build.watchPatterns`, `deploy.startCommand`,
`deploy.preDeployCommand`, `deploy.healthcheckPath`, `deploy.healthcheckTimeout`, `deploy.restartPolicyType`,
`deploy.restartPolicyMaxRetries`, `deploy.numReplicas`, `deploy.cronSchedule`.

## Endpoints

### Workspaces (per-person separation)
Projects belong to a workspace (`Project.workspace_id`). There are three kinds:
- the **default** shared workspace `ws_default` (the pre-existing shared workspace, now with explicit `workspace_members` like every other workspace),
- one **personal** workspace per user (created on first `GET /workspaces` or `GET /auth/me`; the user is owner),
- **team** workspaces anyone can create.

Instance roles grant no membership in any workspace, including `ws_default`. Workspace owner/admin = project admin on every project of that workspace; member/viewer need direct or enabled group project grants. Access groups and their enforcement are scoped to each workspace. `AVEN_API_TOKEN` retains unrestricted operator access; `AVEN_SUPERADMINS` grants instance operations only. Project names are unique per workspace. New accounts are not automatically added to the default workspace.
- `GET    /workspaces` → `(Workspace & {role})[]` where `Workspace = {id, name, personal, owner_id, default, created_at}`
- `POST   /workspaces` `{name}` → `201` team workspace (caller is owner)
- `PATCH  /workspaces/{wsid}` `{name}` (workspace admin) · `DELETE /workspaces/{wsid}` (owner; team only; `409` while it has projects)
- `GET    /workspaces/{wsid}/members` · `POST /workspaces/{wsid}/members {email, role}` · `PATCH|DELETE /workspaces/{wsid}/members/{uid}` (admin; anyone can remove themselves; the personal owner cannot be removed; at least one owner stays). Non-members get `404`.
- `POST   /workspaces/{wsid}/invites` `{email, role}` (workspace admin) → invite link; creates an instance member account with the requested local role.
- `PUT    /projects/{pid}/workspace` `{workspace_id}` (project admin + member of the target) → moves the project (`409` on a name clash), atomically revokes old project grants, group links, tokens and invitations, clears its GitHub connection, and makes the mover project admin.
- `PUT    /projects/{pid}/github` `{user_id: <own id> | null}` (project admin) → `{github_account}`: which user's GitHub connection the project uses. Only your own connection can be assigned.

### Projects / Environments
- `GET    /projects?workspace=` → `Project[]` (all visible workspaces without the filter)
- `POST   /projects` `{name, description?, workspace_id?}` → `201 Project` (creates a `production` environment automatically; `workspace_id` defaults to `ws_default`, needs member in the target workspace; the creator's GitHub connection becomes the project's)
- `GET    /projects/{pid}` → `Project & { environments: Environment[]; services: Service[]; github_account: {user_id, user_name, login?, connected} | null }`
- `PATCH  /projects/{pid}` `Partial<Project>` → `Project`
- `DELETE /projects/{pid}` → `204` (deletes every service, volume and container)
- `POST   /projects/{pid}/environments` `{name, copy_from?: ID}` → `201 Environment`. copy_from copies instance settings and variables but does not copy volume data.
- `PATCH  /environments/{eid}` `{name?, restricted?}` → `Environment` (`restricted` needs project admin; see "Enterprise access controls")
- `DELETE /environments/{eid}` → `204` (the last remaining environment cannot be deleted)

### Canvas (one call to draw the environment)
- `GET /environments/{eid}/canvas` → `{ environment: Environment; project: Project; services: (Service & { instance: ServiceInstance; latest_deployment: Deployment | null; volumes: Volume[] })[] }`

### Services
- `POST   /projects/{pid}/services` →
  `{ name?, source_type, repo_url?, branch?, image?, database_kind?, environment_id? }` → `201 Service`.
  If name is omitted, it is generated from the repo name, image name or db kind. If environment_id is given, the first deploy starts automatically in that environment.
  A database creates a volume and generates variables automatically.
- `PATCH  /services/{sid}` `{name?, icon?}` → `Service` (renaming changes the private_domain)
- `DELETE /services/{sid}` → `204` (all environments)
- `GET    /services/{sid}/instances/{eid}` → `ServiceInstance`
- `PATCH  /services/{sid}/instances/{eid}` `Partial<ServiceInstance>` → `ServiceInstance`
  - The `variables` field replaces all variables. For partial changes use `variables_patch: Record<string, string | null>` (null deletes).
  - With `?deploy=1`, a deployment starts right after saving (trigger=variables, or manual if settings changed).
- `GET /services/{sid}/instances/{eid}/image-updates` → `ImageUpdate[]` (newest first; max 50)
- `POST /services/{sid}/instances/{eid}/image-updates/check` → `ImageUpdate[]` (member; checks the registry now, ignoring the cache; `502` if the registry check fails)
- `POST /services/{sid}/instances/{eid}/image-updates/{uid}/apply` → `202 Deployment` (member; applies an available or failed update immediately, outside the maintenance window too)
- `POST /services/{sid}/instances/{eid}/image-updates/{uid}/skip` → `ImageUpdate[]` (member; "skip this version"; `409` unless available/failed)
- `PUT    /services/{sid}/instances/{eid}/canvas` `{x, y}` → `204`
- `GET    /services/{sid}/instances/{eid}/variables/resolved` → `Record<string, string>` (references resolved; used for the "show value" UI)

### Domains
- `POST   /services/{sid}/instances/{eid}/domains` `{host?: string, target_port?: number}` → `201 Domain`. Without host, a generated domain is created as `<service>-<env>.<AVEN_BASE_DOMAIN>`, or `<service>.<base>` for production. Returns 400 if `AVEN_BASE_DOMAIN` is not set.
- `PATCH  /services/{sid}/instances/{eid}/domains/{host}` → `Domain`. Fields are in "Domains: path rules, TLS mode, redirect, basic auth" (v1.4); `host` renames a generated domain.
- `POST   /services/{sid}/instances/{eid}/domains/{host}/verify` → `Domain` (re-check DNS, ownership and certificate now)
- `DELETE /services/{sid}/instances/{eid}/domains/{host}` → `204`
- `GET    /services/{sid}/instances/{eid}/tcp-proxy` → `{enabled, host, public_port, container_port, available, reason?}` (viewer)
- `POST   /services/{sid}/instances/{eid}/tcp-proxy` `{container_port}` → `ServiceInstance`
- `PATCH  /services/{sid}/instances/{eid}/tcp-proxy` `{container_port}` → `ServiceInstance` (keeps the public port)
- `DELETE /services/{sid}/instances/{eid}/tcp-proxy` → `204`
- `GET    /services/{sid}/instances/{eid}/networking` → networking overview (viewer)

Status values, ownership checks, ACME gating, node ingress, the TCP proxy on remote nodes, private names and outbound IPv6 are in "Networking" at the end of this document.

### Deployments
- `POST /services/{sid}/instances/{eid}/deploy` `{force_build?: boolean}` → `202 Deployment` (deploys the latest commit; image services pull again)
- `POST /deployments/{did}/redeploy` → `202 Deployment` (redeploys with the same image and current variables)
- `POST /deployments/{did}/rollback` → `202 Deployment` (restores that deployment's image **and its variable snapshot**)
- `POST /deployments/{did}/cancel` → `204`
- `POST /services/{sid}/instances/{eid}/restart` → `204` (restarts the current deployment's containers)
- `POST /services/{sid}/instances/{eid}/stop` → `204` (status offline)
- `GET  /services/{sid}/instances/{eid}/deployments?limit=50` → `Deployment[]` (newest first)
- `GET  /deployments/{did}` → `Deployment`
- `GET  /deployments/{did}/build-logs` → SSE (`log` | `status` | `done`), same as v0
- `GET  /deployments/{did}/deploy-logs?tail=500` → SSE: runtime container logs, all replicas merged. If this is not the current deployment, the last logs are replayed and then `done`.
  - `event: log` `data: {"t": RFC3339, "replica": string, "stream": "stdout"|"stderr", "msg": string}` (**JSON**, includes the timestamp)
- `GET  /deployments/{did}/cron-runs` → `CronRun[]`
- `POST /services/{sid}/instances/{eid}/cron/run` → `202 CronRun` (run now)

#### Skipped builds

- `skip_builds` is an opt-in repo instance setting (default `false`). It takes effect on the next deployment and is not listed in `pending_changes`.
- Every successful repo build records a build cache key: a SHA-256 of the commit SHA, `root_dir`, builder, `dockerfile_path`, `build.buildCommand`, `build_secrets`, Compose build target/args and the resolved values of the user-defined build-time variables. For Dockerfile builds only variables declared with `ARG` and build secrets count; for Railpack builds all user-defined variables count. Injected `AVEN_*` variables (deployment/environment IDs, domains, ...) are ignored unless you define them yourself. Values are never stored, only the hash.
- With `skip_builds` on, a deployment whose key matches an earlier deployment of the same service (any environment) whose image is still on the deployment node skips the build and uses that image. Images are retained as long as image pruning keeps them (the 5 latest successful deployments per instance). Other placement nodes that lack the image build the same commit as usual.
- The deployment has `build_reused_from` set to the deployment that originally built the image; the build log says `빌드 건너뜀 — 배포 <id>의 이미지를 재사용합니다`.
- `POST .../deploy` with `{force_build: true}` always builds (CLI: `aven redeploy --force-build`). `aven up` uploads always build. `POST /deployments/{did}/redeploy` and rollbacks keep reusing that deployment's own image as before.

Build-time variable values are part of the key, so a build that inlines a changed variable is not reused.

### HTTP logs
- `GET /services/{sid}/instances/{eid}/http-logs` → SSE `event: log` `data: {"t","method","host","path","status","duration_ms","bytes","client_ip","user_agent"}`. Comes from Caddy access logs.

### DNS query and network flow logs
DNS and network flow logs. Each node (aven-server for the local node, aven-agent for remote nodes) opens an `AF_PACKET` socket on every environment bridge (`keel-env_*`) and reads packet headers only (passive; containers, DNS settings and routing are unchanged). It needs `CAP_NET_RAW` (run as root); without it, or with `AVEN_NETWORK_LOGS=off` (server env / agent `--network-logs off`), that node records nothing and the streams report it in the `capture` event. Remote nodes stream records with `netlog.follow`; replays after a reconnect are de-duplicated by record ID. Records are kept for 7 days and at most 500,000 rows (oldest first).

- `GET /services/{sid}/instances/{eid}/dns-logs` and `GET /environments/{eid}/dns-logs?services=sid1,sid2` → SSE
  - `event: log` `data: {"id","kind":"dns","t","environment_id","service_id","service","replica","qname","qtype","zone":"internal"|"external","rcode","status":"ok"|"failed","answers":[],"cname_chain":[],"resolver","src_addr","duration_ms"}`
  - `rcode` is the DNS response code (`NOERROR`, `NXDOMAIN`, `SERVFAIL`, `REFUSED`, …) or `TIMEOUT` (no answer in 5 s). `zone` is `internal` for `*.internal`, `*.local` and single-label names.
  - Only lookups that leave the container are visible: Docker's embedded resolver answers same-network aliases (`<svc>`, `<svc>.<env>.aven.internal`) inside the container, so those successful private lookups are not recorded; external names and names Docker does not know (e.g. a misspelled `*.aven.internal`, which come back `NXDOMAIN`) are. If the host's resolver is on loopback and Docker forwards from the host namespace, external lookups are not visible either.
- `GET /services/{sid}/instances/{eid}/network-logs` and `GET /environments/{eid}/network-logs?services=…` → SSE
  - `event: log` `data: {"id","kind":"network","t" (capture end),"capture_start","environment_id","service_id","service","replica","flow_state":"complete"|"active","direction":"egress"|"ingress","protocol":"tcp"|"udp","src_addr","src_port","dst_addr","dst_port","peer_kind":"service"|"internet"|"edge_proxy"|"host"|"private"|"unknown","peer_service_id","peer","bytes_sent","bytes_received","packets","duration_ms","latency_ms","status":"ok"|"refused"|"timeout"|"reset"}`
  - One record per TCP/UDP connection, written when it ends (FIN/RST, unanswered SYN after 10 s, UDP idle 30 s, TCP idle 2 h). Connections that stay open get an `active` snapshot every 5 minutes while they carry traffic. `src` is the side that opened the connection; `direction`, `bytes_sent`/`bytes_received` and `peer` are from `service_id`'s side. A connection between two services is stored once; on the other service's stream (`.../instances/{eid}` of that service, or `services=` with exactly that one ID) it is shown flipped (`ingress`, swapped bytes, the first service as peer). Cross-node traffic shows the other node's service as the peer (`peer_kind: "service"`, via its mesh forwarder). Connections from keel-proxy or a TCP proxy are `edge_proxy`; from the node itself (mesh gateway, wake handler) `host`. Bytes are IP bytes (headers included); DNS packets are not counted as flows.
- Query parameters (both kinds): `tail` (records of history, default 200, max 5000), `q` (filter), `since` / `until` (`30s`, `5m`, `2h`, `1d`, `1w` ago, or RFC3339), `follow=0` (send history then `done`; `until` implies it). Bad times → `400`.
- Events: `capture` first, `{"active_nodes","inactive_nodes","reason"}` (the first error of a node that cannot capture); then `log` records oldest first; `done` `{"count"}` when not following.
- `q` uses the log filter syntax (words, `"phrase"`, `-exclude`, AND) plus `@field:value` conditions on the record: strings match exactly (case-insensitive), numbers also accept `>=n`, `>n`, `<=n`, `<n` and `a..b`. Aliases: `@domain` (the name or a subdomain of it), `@port` (source or destination port), `@host` (source or destination address), `@src`, `@dst`, `@peer` (peer name or ID), `@dropped:true` (`refused` or `timeout`). Examples: `@rcode:NXDOMAIN @zone:internal`, `@peer:postgres @port:5432 -@status:ok`.
- `POST /services/{sid}/instances/{eid}/network-diagnostics` `{host, port?, replica?}` (member) → `{checked_at, service_id, replica, host, port, dns, tcp}` where `dns`/`tcp` are `{ok, available, skipped?, exit_code, duration_ms, output}`. Runs inside a running replica (first by default): `getent ahosts` (or `nslookup`) for the name, then `nc -z -w 3` (or bash `/dev/tcp`) for the port; `port` 0/omitted checks DNS only (`tcp.skipped`). `available: false` means the image has none of those tools. `host` must be a hostname or IP and is passed as an argument, never through a shell string. No running deployment → `409`, unknown replica → `404`.

### Environment logs (Observability > Logs)
- `GET /environments/{eid}/logs?services=sid1,sid2&q=...` → SSE, same JSON as deploy-logs plus a `service_id` field. q supports word, `"phrase"` and `-exclude` filters, applied on the server.

### Metrics
- `GET /services/{sid}/instances/{eid}/metrics?range=1h|6h|24h|7d` → `{ points: MetricPoint[]; step_s: number; replicas: number }` (sampled every 10s, kept 7 days, downsampled)
- `GET /environments/{eid}/metrics?range=...` → `{ services: { service_id: ID; points: MetricPoint[] }[] }`

### Volumes / Backups
- `POST   /environments/{eid}/volumes` `{service_id, mount_path, name?}` → `201 Volume` (takes effect on the next deploy)
- `PATCH  /volumes/{vid}` `{name?, mount_path?, service_instance_id?: ID | null}` → `Volume`
- `DELETE /volumes/{vid}` → `204` (409 if attached)
- `GET    /volumes/{vid}/backups` → `Backup[]`
- `POST   /volumes/{vid}/backups` → `202 Backup` (tar.zst snapshot)
- `POST   /backups/{bid}/restore` → `202` (stops the service, restores, redeploys)
- `DELETE /backups/{bid}` → `204`
- `PUT    /volumes/{vid}/backup-schedule` `{cron: string | null, keep: number}` → `204`

### Shared variables
- `GET /environments/{eid}/shared-variables` → `Record<string,string>`
- `PUT /environments/{eid}/shared-variables` `Record<string,string>` → `204`

### Templates
- `GET  /templates` → `Template[]` (built in: postgres, mysql, redis, mongo, minio, n8n, umami, uptime-kuma, plausible, ghost, ...)
- `POST /projects/{pid}/templates/{key}` `{environment_id}` → `201 Service[]` (variable references are wired up automatically, then deployed)

### Events (activity feed)
- `GET /projects/{pid}/events?environment_id=&limit=50` → `Event[]`
- `GET /projects/{pid}/events/stream` → SSE `event: event` `data: Event` (canvas live updates)

### GitHub
- **GitHub connection (OAuth App), per user.** Set `AVEN_GITHUB_CLIENT_ID` / `AVEN_GITHUB_CLIENT_SECRET` from a GitHub OAuth App whose callback URL is `<AVEN_PUBLIC_URL>/api/github/oauth/callback`. Every user connects their own account in Account › GitHub or Settings › Integrations (or `aven github connect`); the token (scopes `repo admin:repo_hook`) is stored encrypted with the master key in `github_connections`. Repository lists and webhook registration use the caller's own token. Private clones (local and remote nodes, compose repo imports), commit statuses and PR file lookups use the token of the project's GitHub user (`projects.github_user_id`, default: the creator; see `PUT /projects/{pid}/github`) and never fall over to another user's connection. `AVEN_GITHUB_TOKEN` stays the fallback for projects without a connected user. The old global connection is migrated to the user who connected it, and existing projects keep using it.
  - `GET /github/oauth/start` (any user, session; no scoped tokens) → `302` to GitHub's authorize page. The `state` is single-use, expires after 10 minutes and is bound to the user who started it.
  - `GET /github/oauth/callback` → exchanges the code, reads `/user`, stores the caller's connection, then `302 /settings/?section=integrations&github=connected` (or `github=error&message=…`). Audit event `github.connected`.
  - `GET /github/connection` → the caller's `{app_configured, connected, source: "oauth"|"env"|"", env_token, login?, scopes?, connected_at?, callback_url, webhook_url, secret_configured}`. The token is never returned.
  - `DELETE /github/connection` (session; no scoped tokens) → `204`; removes the caller's connection and revokes the grant on GitHub (best effort). Audit `github.disconnected`.
  - `GET /github/repos?q=` → `{full_name, private, default_branch, url, pushed_at, admin}[]` the caller's account can see (most recently pushed first, at most 300). `409` when the caller has not connected.
  - `POST /github/webhooks {repo}` (session; `repo` is `owner/name` or a github.com URL) → creates or updates the repository webhook with the caller's token (`<AVEN_PUBLIC_URL>/api/webhooks/github`, JSON, `AVEN_GITHUB_WEBHOOK_SECRET`, events push, pull_request, check_suite, workflow_run) → `201`/`200`. Needs an https `AVEN_PUBLIC_URL`, the webhook secret and `admin:repo_hook`; GitHub `401/403/404/422` pass through. Audit `github.webhook_created`.
- `POST /webhooks/github` (no auth; verifies `X-Hub-Signature-256` against `AVEN_GITHUB_WEBHOOK_SECRET`). On a push event, every instance whose repo_url and branch match and that has auto_deploy=true is deployed, after watch_paths matching. Non-matching pushes get status skipped.
- `GET /system/github` → `{ webhook_url: string; secret_configured: boolean; token_configured: boolean }` (`AVEN_GITHUB_TOKEN`: used for private repo clones and commit message lookups)

### CLI upload (aven up)
- `POST /services/{sid}/instances/{eid}/upload` (multipart `source`=tar.gz) → `202 Deployment` (trigger=cli; builds from the uploaded source)

### System
- `GET /system` → `{ version; node; docker; proxy; base_domain: string | null; public_ipv4: string | null; public_ipv6: string | null; disk_free_bytes: number }`

## v1.1 additions

Decided after the frontend review. These are part of the contract.

1. **`ServiceInstance.pending_changes: string[]`** (replaces the implicit "pending changes" flag). It lists the keys that differ from the current deployment's snapshot: `"variables.<KEY>"` for an added, changed or removed variable, and the field name (`"start_command"`, `"replicas"`, `"builder"`, `"port"`, ...) for a setting. The comparison uses the settings the deployment was *requested* with (before config-as-code overrides), so a `railway.json` override does not show up as pending. `domains` and `tcp_proxy` apply immediately and never appear. The array is empty when nothing is pending or the instance was never deployed.
2. **Empty service.** `POST /projects/{pid}/services` accepts `source_type: "image"` with `image: null` (or omitted). The service is named `service` (`service-2`, ...) unless `name` is given, and the instance is `idle`. `POST .../deploy` (and `PATCH ...?deploy=1`) returns `400 {"error":"소스를 먼저 설정하세요"}` until `image` (or `repo_url` for repo services) is set. `aven up` (the upload endpoint) works on an empty service.
3. **`MetricPoint.cpu_percent`** follows the docker stats convention: percent of ONE core, summed over replicas, so it can exceed 100. `GET /system` adds `cpu_cores: number` (host core count).
4. `GET /volumes/{vid}/backup-schedule` → `{ cron: string | null, keep: number }`.
5. `GET /environments/{eid}/volumes` → `Volume[]`, including detached volumes (`service_instance_id: null`). `size_bytes` is refreshed every 5 minutes (null until measured).
6. **Shape change:** `GET /services/{sid}/instances/{eid}/variables/resolved` → `Record<string, { value: string; source: "user" | "shared" | "reference" | "injected" }>`. It includes the injected `AVEN_*` and `PORT` values. `source` is `injected` when the user has no value for the key, `shared` when the user value is exactly one `${{ shared.X }}`, `reference` when it contains any other reference, and `user` otherwise. A reference cycle returns `400 {"error":"variable reference cycle: a.X → b.Y → a.X"}`. `PATCH` rejects variables that would create a cycle with the same message.
7. Build logs keep the v0 line format `HH:MM:SS msg` (UTC). Stage changes are logged as `==> <status>` (`initializing`, `building`, `pre_deploy`, `deploying`, `healthchecking`, `live`). Right before the containers are started the server emits exactly `HH:MM:SS ==> starting`. Pre-deploy and crash output lines are prefixed with `  | `.
8. `Event.message` is server-generated Korean text that the UI shows as-is (for example `pgapp (production) 배포 완료 @ a04cf8c pgapp: initial`). Event kinds: `deploy.started|live|failed|crashed|skipped|rollback`, `service.created|updated|deleted|stopped|restarted`, `variables.updated`, `domain.added`, `tcp_proxy.enabled`, `volume.created`, `volume.backup.failed`, `backup.done|restored|restore_failed`, `cron.started|succeeded|failed`, `environment.created|deleted`, `project.created`, `template.deployed`.
9. Env var **`AVEN_TCP_PROXY_DOMAIN`**: the host shown for TCP proxies (`AVEN_TCP_PROXY_DOMAIN` variable, `*_PUBLIC_URL`). It defaults to `AVEN_BASE_DOMAIN`, then the node's hostname (if it contains a dot), then the public IPv6/IPv4. `GET /system` exposes it as `tcp_proxy_domain: string | null`.
10. `POST /projects/{pid}/templates/{key}` returns `Service[]` with the primary service (the app that gets a domain, for example `umami`) first, followed by its dependencies.
11. Log filtering: `q` stays AND-only (words, `"phrase"`, `-exclude`, `-"phrase"`, case-insensitive). `GET /environments/{eid}/logs` and `GET /deployments/{did}/deploy-logs` also accept `level=error`, which keeps stderr lines and lines containing `error`, `fatal` or `panic` (case-insensitive). `deploy-logs` accepts `q` as well.

Also added (additive, no contract change):
- `GET /cron-runs/{rid}/logs` → `{t, replica, stream, msg}[]`, the stored output of one cron run. `deploy-logs` of a cron deployment replays the last runs.
- `GET /system` also returns `tcp_proxy_domain` and `cpu_cores` (see above).
- Deployment `trigger` can also be `"redeploy"` for the automatic redeploy after a backup restore.

## Deviations

- **Redeploy uses the deployment's effective settings.** `POST /deployments/{did}/redeploy` reuses that deployment's image *and* the settings it ran with (including config-as-code overrides captured at build time), together with the instance's current variables. To pick up changed settings, use `POST .../deploy` or `PATCH ...?deploy=1` (rebuild or re-pull).
- **`PORT` is always injected.** It is `port` when set, otherwise `8080`. If `port` is null and the image `EXPOSE`s a port (and the user did not set `PORT`), the deployment uses the lowest exposed port and `PORT` follows it. References such as `${{api.PORT}}` resolve to the port the live deployment actually uses.
- **Services with volumes deploy with a short downtime.** The old container is stopped before the new one starts so two processes never open the same data directory . If the new deployment fails, the old containers are started again. Services without volumes keep the zero-downtime blue-green cutover.
- **The TCP proxy is a small forwarder container** (`alpine/socat`, label `keel.role=tcpproxy`) that publishes the assigned port and forwards to the service's private alias. It is still a docker port publish on a server-assigned port (20000–29999) that stays stable across deploys, but a forwarder is needed because blue-green cutover and multiple replicas can't share one published host port. The forwarder binds to `AVEN_PROXY_BIND` when set; otherwise it binds to all addresses, IPv6 and IPv4. Databases don't enable a TCP proxy by default, so `DATABASE_PUBLIC_URL` and the other `*_PUBLIC_URL` values have an empty host until one is added.
- **Railpack builds use the docker daemon's built-in BuildKit.** The server runs `railpack prepare` to produce a plan, then `docker buildx build --builder default` with `BUILDKIT_SYNTAX=ghcr.io/railwayapp/railpack-frontend:v0.42.0`. No separate BuildKit container is needed. Variables reach the build as BuildKit secrets (names are added to the plan), so values never appear in argv or image layers. Only `RAILPACK_*`/`NIXPACKS_*` variables are passed to `prepare`. Railpack images carry no `aven.*` labels, so old images are pruned by repository name (`keel/<service-id>`), keeping the last 5 successful deployments per instance.
- **MinIO template** uses `pgsty/minio:RELEASE.2026-08-04T00-00-00Z`. Official `minio/minio` images are no longer published on Docker Hub.
- **Cron** runs on UTC. A run is skipped if the previous run of the same job is still going (`POST .../cron/run` returns `409`). Each run is limited to 24h.
- **Backups** are crash-consistent snapshots of the live volume (tar via a short-lived `busybox` helper container, compressed with zstd by the server), stored under `<data>/backups/<volume-id>/`. Restore stops the containers, replaces the volume contents, and then enqueues a `redeploy` of the current deployment.
- **Custom domain `status`** is checked in the background by the domain status engine (`active`, `proxied` behind Cloudflare or a tunnel, `pending_dns`, `error`); see "Networking". Generated domains served from one node are always `active`.
- `sleep_when_idle` is stored only, as documented. Instance status `sleeping` is never set.
- **Healthcheck tolerates early restarts.** During `healthchecking`, up to 2 container restarts are allowed, for example an app that starts before its database accepts connections. A third restart, or a container that stops for good, fails the deployment right away and prints the last 30 log lines. A crash after the deployment is live sets the instance to `crashed` and emits `deploy.crashed` (when the restart policy gives up, or with `never`). A restart that docker recovers from emits `service.restarted`.

## v1.2 additions: multi-node + remaining gaps

### Nodes
```ts
type Node = { id: ID; name: string; is_local: boolean;          // the node where aven-server runs is always local, and there is exactly one
  status: "online" | "offline" | "draining";
  schedulable: boolean;                                         // false: no new deployments go to this node
  address: { ipv4: string | null; ipv6: string | null; private: string | null };  // private: Tailscale/WireGuard address
  version: string; docker_version: string;
  cpu_cores: number; memory_bytes: number; disk_free_bytes: number;
  labels: Record<string, string>;
  ingress_mode: "direct" | "cloudflare_tunnel";                 // how public traffic reaches this node
  ingress_hostname: string;                                     // CNAME target for custom domains ("" = A/AAAA records)
  tcp_hostname: string;                                         // host shown for TCP proxies ("" = node address / AVEN_TCP_PROXY_DOMAIN)
  ipv6_pool: string;                                            // per-domain public IPv6 pool (CIDR, /64-/120; "" = domains share the node IPv6)
  ipv6_status?: { pool: string; supported: boolean | null; reason?: string; iface?: string;   // GET /nodes only; null without a pool
                  desired: number; present: number; foreign?: string[]; last_error?: string; checked_at?: string } | null;
  last_seen_at: string; created_at: string };
```
- `GET    /nodes` → `Node[]`
- `POST   /nodes/join-tokens` `{name?, ttl_s?: number}` → `{ token: string; expires_at: string; command: string }`
  The command is one line to run on the new server. Example: `curl -fsSL <AVEN_PUBLIC_URL>/install-agent.sh | sudo sh -s -- --server <url> --token <token>`.
  The token is single-use and expires after 1 hour by default.
- `GET    /install-agent.sh` (no auth) → install script. It downloads the `aven-agent` binary from `GET /downloads/aven-agent-linux-{amd64,arm64}`, writes the compatible `keel-agent.service` unit and starts the agent.
- `PATCH  /nodes/{nid}` `{name?, schedulable?, labels?, ingress_mode?, ingress_hostname?, tcp_hostname?, ipv6_pool?}` → `Node` (ingress fields: see "Networking" > "Node ingress"; `ipv6_pool`: "Per-domain IPv6")
- `DELETE /nodes/{nid}` → `204`. Returns 409 if services are still placed on it. With `?force=1` it removes the node anyway, and its instances become `offline`.
- `GET    /nodes/{nid}/metrics?range=` → `{ points: { t; cpu_percent; memory_bytes; disk_free_bytes }[] }`
- `ServiceInstance.node_id: ID` is the node the instance runs on. The default is the local node. Changing it applies on the next deploy, and the volumes must be moved or recreated (400 if attached volumes would be orphaned, unless `?move_volumes=1`).
- `Deployment.node_id: ID` is added.
- `Domain.dns` records point at the IPs of the node the instance runs on (or a CNAME to its `ingress_hostname`).
- **Agent protocol**: the agent dials `wss://<server>/api/agent/connect` and authenticates with a keel-CA-issued node certificate plus a per-connection proof of key possession (see deviations: application-layer mTLS equivalent). Each node runs its own keel-proxy (Caddy), and the agent drives it locally. Builds run on the target node, so no registry is needed. Logs, metrics and healthchecks go over the same connection. When the connection drops, the node becomes `offline` and its running containers keep serving (they keep the restart policy). Reconcile resumes when the node reconnects.
- **Cross-node private networking** is not provided in v1.2. If a reference would resolve to a service on another node, `variables/resolved` marks it `cross_node: true` and resolves `*_PRIVATE`/private domains to the target node's private address plus its TCP proxy port when one exists. Otherwise the deploy fails with a clear message. *(v1.5: superseded by the mesh for nodes with a private address; see "Cross-node private networking (mesh)".)*

### Web/API hardening (v1.2)
- **Client IP.** Rate limiting and logs use the real client address. Forwarded headers are honoured only on trusted paths: the gateway unix socket (`AVEN_LISTEN_UNIX`, and only for requests carrying the gateway secret, see below) and peers listed in `AVEN_TRUSTED_PROXIES`. There, the last `X-Forwarded-For` hop (the peer the proxy itself saw) is used; if that hop is a Cloudflare address (compiled-in list of Cloudflare's published ranges), `CF-Connecting-IP` is used. Any other TCP connection (e.g. the Tailscale listener) uses its socket address and ignores all forwarded headers. IPv6 clients are bucketed per /64.
- **Gateway header spoofing.** Containers on `dokploy-network` can reach the gateway directly and forge `X-Forwarded-*`. The gateway container carries a Traefik middleware (labels, `headers.customrequestheaders.X-Keel-Gateway=<secret>`) on the websecure router, so Traefik overwrites any client-supplied value. aven-server (`AVEN_GATEWAY_SECRET_FILE`, default unset; the file is created 0600 if missing) trusts forwarded headers on the unix socket only when the header matches (constant-time), then removes it; otherwise it strips every forwarded header and treats the request like an untrusted TCP one (so `X-Forwarded-Proto: https` no longer enables enrollment or the Secure cookie). Without `AVEN_GATEWAY_SECRET_FILE` every peer that can open the socket is trusted (a warning is logged). The secret is also visible in the container's Docker labels (root and the Traefik Docker provider can read it). Rotate by deleting the file, recreating the gateway and restarting aven-server. If aven later terminates TLS on 80/443 itself, the gateway, the socket and the secret are simply not configured: `r.TLS` is then what counts.
- **Login brute force.** `POST /auth/login` is limited per client IP (5 failures, then locks of 30 s doubling to 15 min) and for the single admin account across all IPs (20 failures, then 30 s doubling to 10 min); counters are forgotten after an hour without failures and reset by a successful login. While locked the password is not even checked and the response is `429` with `Retry-After`. A distributed attacker can therefore lock the admin out of the UI for at most 10 minutes at a time; the API token (`Authorization: Bearer`) is not affected by the account lock. Wrong Bearer tokens, join tokens and agent authentication failures are limited per IP (10 failures, 30 s doubling to 15 min; revoked-but-validly-signed agents are not counted). All secret comparisons (password, API token, gateway secret) are constant-time on SHA-256 digests; the session cookie uses HMAC with constant-time compare. (v1.5: the account limit is per user account, sessions are server-side; see "Users, roles and tokens".)
- **Headers.** Every response carries `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'`, `Referrer-Policy: same-origin` and a restrictive `Permissions-Policy`; `Strict-Transport-Security: max-age=31536000` (no includeSubDomains/preload) and `Cross-Origin-Opener-Policy: same-origin` only when the request arrived over TLS (directly or through a trusted proxy). There is no `script-src` policy because the static-export UI uses inline scripts.
- **Session cookie** is `HttpOnly`, `SameSite=Strict`, and `Secure` on TLS requests.
- **CSRF.** Cookie-authenticated requests with a method other than GET/HEAD/OPTIONS, and login/logout, are rejected with `403` unless `Sec-Fetch-Site` is `same-origin`/`none`, or (when absent) `Origin` matches the request `Host`; `Origin: null`, other origins and same-site siblings are rejected. Requests with neither header (curl, CLI) are accepted because browsers always send one of them. Requests authenticated with the Bearer API token are not subject to this check.

### Private registries
- `GET  /projects/{pid}/registries` → `{ host: string; username: string; has_password: true }[]`
- `PUT  /projects/{pid}/registries` `{ host, username, password }[]` replaces the whole list. A password of null keeps the existing one. Pulls use a per-deploy DOCKER_CONFIG temp directory; credentials never appear in argv or logs.

### Exact-SHA GitHub deploys
- Push webhooks deploy exactly `after` (the pushed SHA). Implemented as a fetch of that SHA followed by a checkout.

### App sleeping (serverless)
- `sleep_when_idle: true` with `sleep_after_s` (default 600). When no HTTP requests arrive (judged from the access logs) for that long, the containers stop and the status becomes `sleeping`. The Caddy route then points to the aven wake handler, which starts the service on the first request, waits until it is healthy (max 30s), and proxies that request. Later requests go directly to the container.
- `ServiceInstance.sleep_after_s: number` is added.

### Cosmetic
- Event messages format sizes for humans (e.g. "3.3 MB").
- An event for a deleted service or a project-level event carries `service_name: string | null` as a snapshot of the name at event time.
- A deploy started after save-only variable changes gets trigger `variables`.

### v1.2 deviations and gaps (multi-node)

What is implemented and how it differs from the plan above:

- **Agent auth is an application-layer equivalent of mTLS, not TLS client certificates.** Real mTLS cannot work behind Cloudflare/Traefik because they terminate TLS and never forward the client certificate. What is built instead:
  - **Internal CA.** aven-server keeps an ed25519 CA in `AVEN_DATA/agent-ca.key` (0600) and `agent-ca.crt`. It is created on first start; if only one of the two files exists the server refuses to start rather than make a new CA.
  - **Enroll.** The agent generates its own ed25519 key (the private key never leaves the node). `POST /api/agent/enroll` `{token, name?, public_key}` (`public_key` = base64 PKIX DER) exchanges the single-use join token for `{node_id, certificate, ca, expires_at}`. The certificate is signed by the aven CA, valid 30 days, `ExtKeyUsage=clientAuth`, with the node ID bound in the CN and the URI SAN `keel-node://<id>`. Each issued serial is recorded in `node_certs`. The agent stores `{node_id, key, certificate, ca}` in `credentials.json` (0600).
  - **Per-connection proof of possession.** `GET /api/agent/connect` upgrades to a WebSocket with no secret in the HTTP request. Inside the socket the server sends `auth.challenge {nonce}` (fresh 256-bit random per connection). The agent answers `auth.response {certificate, signature}` where the signature is ed25519 over `keel-agent-auth-v1 \n node_id \n server host \n nonce`. The host is the one the agent was configured to dial, not a value the server supplies, so a different server cannot relay the signature. The server checks CA signature, validity, EKU, node-ID binding, that the serial is recorded and not revoked, and the signature, then sends `auth.ok`. Before that, only messages up to 16 KiB are read, with a 10 s deadline and at most 64 concurrent unauthenticated sockets. A stolen request header, certificate or captured handshake is useless without the private key.
  - **Renewal.** Over the authenticated connection the server pushes `cert.renew {certificate}` once less than 10 days of validity remain (checked on connect and hourly). It is a new certificate for the same key; the agent accepts it only if it is signed by the CA pinned at enroll, names the same node and key, then stores it atomically. An agent that is offline for more than the validity must be re-joined (`--reenroll`). The key is not rotated on renewal. A connection whose certificate expires or is revoked while open is closed.
  - **Revocation.** `DELETE /nodes/{nid}` revokes all of the node's certificates (and any legacy secret) and drops an open connection. A deleted or revoked node's certificate is rejected even before it expires. Revoking a certificate without deleting the node is not exposed over the API yet.
  - **Migration.** Agents enrolled before this change hold `{node_id, secret}`. A new aven-agent binary (for example after re-running `install-agent.sh`) generates a key on its next start and calls `POST /api/agent/upgrade` `{node_id, secret, public_key}` over TLS, saves the certificate, and removes the secret from `credentials.json`. The server retires the Bearer secret when the node first connects with the certificate, so an interrupted upgrade cannot lock the node out, and there is no way back to Bearer afterwards. Until the agent binary is updated, old agents keep working with `Authorization: Bearer <node_id>:<secret>` (logged as a warning). `AVEN_AGENT_LEGACY_BEARER=off` makes the server refuse legacy Bearer connections and legacy (no `public_key`) enrollments.
  - **What this does not give.** The channel is TLS to Cloudflare/Traefik, which can read agent traffic, as with any proxied deployment. The proof of possession protects authentication, not confidentiality or integrity against the TLS terminator. A server that is compromised (CA key stolen) can mint node certificates.
- **All agent endpoints (`enroll`, `upgrade`, `connect`) require TLS.** Either serve aven-server directly over TLS (`AVEN_TLS_CERT` + `AVEN_TLS_KEY`), or put it behind a TLS-terminating reverse proxy. For the proxy, either list it in `AVEN_TRUSTED_PROXIES` (comma-separated IPs/CIDRs) or let it reach aven-server only through the unix socket `AVEN_LISTEN_UNIX` (an extra listener serving the same handler; only requests arriving on it trust `X-Forwarded-Proto`, and with `AVEN_GATEWAY_SECRET_FILE` only those carrying the gateway secret). `X-Forwarded-Proto` `https` or `wss` (Traefik uses `wss` on WebSocket upgrades) is honoured only from those sources and never by default. Otherwise the endpoints return `403`. `POST /nodes/join-tokens` returns `503` unless `AVEN_PUBLIC_URL` is an `https://` URL (letters, digits and `.-_:/[]` only, since it goes into a shell command). The agent takes `--ca-file` for a private CA.
- **Session cookie** is `Secure` whenever the request arrived over TLS (directly or via a trusted proxy), even with `AVEN_INSECURE_COOKIE`; that flag only affects plain-HTTP requests.
- **Join command.** `command` is `curl -fsSL <AVEN_PUBLIC_URL>/install-agent.sh | sudo sh -s -- --server <AVEN_PUBLIC_URL> --token '<token>'`, i.e. it embeds the single-use token (returned once; only its hash is stored; never logged by the server). On the new server, sudo may log its command line; the token is useless once consumed and expires after `ttl_s`.
- **Install script.** `GET /install-agent.sh` (no auth) is a POSIX sh script: requires root, Linux amd64/arm64, systemd, curl and a running Docker. It downloads `GET /downloads/aven-agent-linux-<arch>` over HTTPS only (`--proto =https`), verifies it against `GET /downloads/aven-agent-linux-<arch>.sha256`, installs `/usr/local/bin/aven-agent` and writes `keel-agent.service` (the legacy unit name; `EnvironmentFile=/etc/aven-agent/agent.env`, `Restart=always`). The token goes only into that env file (root, 0600), never into the unit or the agent's argv, and is removed from it once `/var/lib/aven-agent/credentials.json` exists. Re-running upgrades the binary and keeps credentials and extra env lines; `--reenroll` joins again. Options: `--ca-file <pem>` (copied to `/etc/aven-agent/ca.pem`, used by curl and the agent), `--env AVEN_X=value` (extra agent settings, e.g. `AVEN_TLS=off`). Without `--token`, it reads `AVEN_JOIN_TOKEN` or prompts on the terminal.
- **CLI downloads.** No auth. `GET /api/cli/downloads` → `{version, install_script: "/install.sh", files: [{name, os, arch, url, size, sha256, updated_at}]}` for the aven CLI binaries present in `AVEN_AGENT_BIN_DIR` (`aven-{linux,darwin}-{amd64,arm64}`, `aven-windows-{amd64,arm64}.exe`; build with `make clis`). Each is served at `GET /downloads/<name>` with `GET /downloads/<name>.sha256`; other names are `404`. `GET /install.sh` is a POSIX sh installer for Linux/macOS (`curl -fsSL <url>/install.sh | sh -s -- --server <url> [--dir DIR]`, https only, checksum verified, no root needed).
- **Agent binaries.** aven-server serves `aven-agent-linux-{amd64,arm64}` from `AVEN_AGENT_BIN_DIR` (default: the directory of the aven-server executable); `make agents` builds both into `bin/`. A missing binary returns `404`.
- **Connection handling.** A newer connection from the same node replaces the old one. The server pings every 15s, so a dead connection becomes `offline` within about 25s. Status writes are serialized per node, and a late disconnect never overwrites a newer connection's `online`. Server→agent calls are bounded: the default is 2 minutes, long-running operations get their own deadline, and the agent is asked to cancel when a call is abandoned. At most 256 calls can be in flight per node, and the agent runs at most 32 at a time.
- **Agent allowlist.** The agent accepts only these operations: ping, image pull/exists/digest/exposed-ports/list/remove (`keel/*` only), env network ensure/remove (`keel-env_*`), volume ensure/remove (`keel-vol-*`), container run/run-to-completion/start/stop/restart/remove/inspect/list/logs/stats/probe, source fetch/cleanup, image build and proxy apply.

- **Agent allowlist.** The agent accepts only these operations: ping, image pull/exists/exposed-ports/list/remove (`keel/*` only), env network ensure/remove (`keel-env_*`), volume ensure/remove (`keel-vol-*`), container run/run-to-completion/start/stop/restart/remove/inspect/list/logs/stats/probe, source fetch/cleanup, image build, proxy apply and the read-only `netlog.follow` stream (DNS/flow records of this node).
  - Run specs are decoded strictly. Unknown fields are rejected, so privileged, capabilities, host/container network modes and bind mounts cannot be expressed. Only aven env networks and aven named volumes are accepted. Port publishing is refused.
  - A spec must carry `keel.managed=true` with role `app` (`predeploy`/`cron` for run-to-completion).
  - Container actions only touch containers labelled `keel.managed=true` with a workload role, never keel-proxy or foreign containers.
  - Repositories must be https/http/ssh (`file://` is refused).
  - Proxy upstreams must be `keel-…:port` (no IPs or unix sockets).
- **Remote deploys** support:
  - image services (with private registry credentials, sent to the agent for the pull)
  - repo services: clone, config-as-code, then a Dockerfile build on the node. Railpack builds: see Remote parity.
  - replicas, named volumes, pre-deploy commands, cron services
  - healthchecks run by the agent on that node
  - blue-green or recreate cutover, rollback/redeploy
  - restart, stop, delete
  - deploy-logs, service metrics
- **Moving a service.** `node_id` changes apply on the next deploy. With volumes attached, `?move_volumes=1` is required (otherwise `400`). The deploy then stops the old containers (the single writer), streams each volume old node → new node over the agent connections (chunks of at most `MaxChunkBytes` = 1 MiB, within `MaxMessageBytes`), starts the service on the new node and, only after cutover, removes the old node's volume. If anything fails (copy, start, healthcheck, old node offline) the old containers are started again and the old node's data is untouched; the half-copied volume on the new node is removed. A TCP forwarder moves with the service. A service deployed from a CLI upload can be moved by redeploying it with the CLI.
- **Offline nodes.** Deploys to an offline node fail with `node … is offline`, and the live deployment keeps serving. Stop/delete while offline remove the database state, and the leftover containers are removed as orphans when the node reconnects.
- **Remote parity.** These work the same on remote nodes as on the local node:
  - **TCP proxy**: the agent runs the forwarder itself (`tcpproxy.ensure`/`tcpproxy.remove`; it builds the container spec, public port must be >= 1024, bind address is the agent's `--proxy-bind`). `AVEN_TCP_PROXY_DOMAIN` is the node's public IPv6 (else IPv4); custom-domain DNS records are the node's IPs.
  - **Sleep/wake**: the sleeping route points to a wake socket served by the node's agent. A request makes the agent ask the server (`wake`, agent-initiated message) which starts the container, health-checks it through the agent and answers with the container to proxy to. A node can only wake its own services. Idle detection uses the node's access logs. A sleeping service on a node that is offline cannot wake.
  - **HTTP access logs**: the agent follows its keel-proxy log (`proxy.accesslog_follow`) and streams it into the same hub as the local proxy.
  - **DNS / network flow logs**: the agent captures its own environment bridges and streams records (`netlog.follow`). Records for a service that is not in that environment's project are dropped by the server.
  - **Volume backups and restore**: `volume.export` streams a tar from the node; restore uploads it in chunks (`upload.begin/chunk/finish`), refused while a running container uses the volume. They return `409` if the node holding the volume is offline.
  - **CLI upload deploys**: the tarball is uploaded to the node, extracted by the agent (path traversal neutralised), and built there (Dockerfile or railpack).
  - **Crash detection**: the agent streams docker events (`events.follow`); `deploy.crashed` and `service.restarted` behave as locally. The 30 s reconcile remains as a safety net.
  - **Private registry pulls**: credentials are sent to the agent for the pull only (never logged).
  - **Railpack**: `aven-agent --railpack auto` (default) downloads the pinned official release (v0.42.0, SHA-256 verified, https only) into `<data>/bin/` on first use. Use a path to supply your own, or `off` to refuse railpack builds. The node's docker needs the buildx plugin and access to ghcr.io for the frontend image.
  - **Rollback/redeploy** work when the image is still on the node (same node). After a node change, rebuild from source instead.
- **Node metrics**: `GET /nodes/{nid}/metrics?range=1h|6h|24h|7d` → `{points:[{t, cpu_percent (whole node, 0..100), memory_bytes (used), disk_free_bytes}], step_s}`. Sampled every 10 s for the local node and for connected remote nodes (`node.stats`), kept like service metrics (10 s for 24 h, then 5 min, 7 days). Unknown node `404`, bad range `400`.
- **`DELETE /nodes/{nid}[?force=1]`**: `409` if instances are placed on the node (or run on it). `force=1` cancels their deployments, removes their containers if the node is connected, sets them `offline`, clears their current deployment and resets their placement to the local node (volumes' data stays on the removed node). The agent connection is closed and the node's credentials/certificates are revoked. A connected node without instances can be deleted without force.
- **Draining**: `POST /nodes/{nid}/drain` `{target_node_id?}` → `202 {node, moved:[{instance_id, service_id, to, deployment_id?}], skipped:[{instance_id, service_id, reason}]}`. The node becomes `draining` (status shown while connected) and unschedulable; each instance gets a fresh deploy (`trigger=drain`) on the target (or the least-loaded schedulable online node). The old node keeps serving until cutover; volumes are copied as in a move. Stopped/never-deployed instances only change placement. Upload-deployed services are `skipped`. `PATCH {schedulable:true}` clears the draining mark.
- **Not implemented:** the `variables/resolved` `cross_node` flag (cross-node private networking is not provided). *(v1.5: both are provided; see "Cross-node private networking (mesh)" in the v1.5 additions.)*
- `Domain.dns` for a custom domain uses the addresses of the node that currently serves the instance. A remote node reports them with `--public-ipv6`/`--public-ipv4`, or they are detected.

## v1.3 additions: Docker Compose stacks

A compose file is imported into one environment and becomes one aven service per compose service (own variables, domains, logs, metrics, deploys).
Services carry `stack_id` and `compose_name` (`Service`), and `GET /environments/{eid}/canvas` returns `stacks: StackView[]` so the UI can draw a group frame.
Parsing and `${VAR}` interpolation use compose-go. Paths below are under `/api/projects/{pid}/environments/{eid}`.

```ts
type ComposeRequest = {
  source: "yaml" | "repo";
  yaml?: string;                       // source=yaml
  repo_url?: string; branch?: string;  // source=repo (shallow clone; build contexts must be inside the repo)
  path?: string;                       // compose file in the repo (default: docker-compose.yml|yaml, compose.yml|yaml in the root)
  name?: string;                       // stack name (default: compose `name:`, else "stack")
  env?: string;                        // .env content: interpolation values and `env_file: .env`
  env_files?: Record<string, string>;  // other env_file contents by path
  allow_bind_mounts?: boolean;         // default false: bind mounts are an error (they break multi-node)
  node_id?: ID;                        // all services of the stack run on this node (default: local, or the node of the existing stack)
  stack_id?: ID; reuse_source?: boolean; // re-import: fill empty fields from the stored source
  // apply only
  domains?: Record<string, { host?: string; target_port?: number }[]>; // compose service → domains to expose (empty host = generated)
  tcp_proxy?: Record<string, number>;  // compose service → container port
  deploy?: boolean;                    // default true
  prune?: boolean;                     // delete services that disappeared from the compose file
};
type ComposeIssue = { severity: "error" | "warning" | "info"; service?: string; code: string; message: string };
type StackView = { id: ID; project_id: ID; name: string; created_at: string; service_ids: ID[];
  levels: string[][];        // deploy order (service names); a level deploys together after the previous one is live
  imported: boolean; deploying: boolean; source: object | null; applied_at: string | null };
```

- `POST /compose/preview` ComposeRequest → `200 { plan, stack|null, exists, diff, node_id, source, ok }`. Changes nothing.
  `plan = { stack, services[], levels, warnings[], errors[], missing_variables[] }`; each service plan lists image/build, port, healthcheck, restart, replicas,
  env keys (no values), volumes, DNS aliases, `depends_on`, `domain_candidates` (HTTP ports, not public by default), `tcp_candidates`, issues.
  `diff[]` = `{ name, compose_name, action: create|update|unchanged|remove|keep, changes[] }`.
- `POST /compose/apply` ComposeRequest → `200 { stack, plan, diff, created, deploy: {started, levels}, notes }`. `422 { error, plan }` when `plan.errors` is not empty.
  Re-importing the same stack (same name or `stack_id`) updates the existing services in place (no duplicates); user-set domains, TCP proxy, canvas position and variables that compose never set are kept.
  Named volumes are created per service; volumes that vanish from the file are kept (data safety).
- `GET /stacks` → `StackView[]`; `GET /stacks/{stid}` → `StackView`.
- `DELETE /stacks/{stid}` → `204` (deletes every service of the stack with its containers, volumes and domains).
- `POST /stacks/{stid}/redeploy` → `202 { started, levels }` (`409` if a stack deploy is already running).

Mapping: `image`/`build` (context inside the repo, dockerfile, target, args) · `command`/`entrypoint`/`user`/`working_dir` · `environment` + `env_file` → variables · `ports`/`expose`/traefik labels → domain candidates
(known TCP ports such as 5432/6379 → `tcp_candidates`) · named volumes → aven volumes · `depends_on` (+`service_healthy`) → deploy order (a healthy dependency must pass its healthcheck, i.e. be live, first) ·
`healthcheck` (http/tcp recognised, e.g. `pg_isready`, `redis-cli`, `curl http://localhost:PORT/path`) · `restart` · `deploy.replicas`/`scale` · non-traefik `labels` ·
networks: all services join the environment network; the raw compose service name and `networks.*.aliases`/`links` resolve as DNS aliases in addition to `<svc>.<env>.aven.internal`.

Errors (`plan.errors`, nothing is imported): bind mounts (unless `allow_bind_mounts`, local node only), `network_mode`, `pid`, `privileged`, `devices`, `cap_add`, `volumes_from`, `secrets`, `configs`, external/driver volumes,
a named volume shared by several services, `build` for pasted YAML, name/DNS collisions with other services of the environment, `depends_on` cycles. Everything else aven cannot honour (resources, `dns`, `tmpfs`, external networks, published host ports, …) is a warning.
Not yet verified/limited: `service_completed_successfully` is treated as started; remote nodes reject `_` in DNS aliases.

## v1.4 additions: Dokploy-migration gaps (routing, TLS, backups, limits, SSH, build secrets)

### Domains: path rules, TLS mode, redirect, basic auth
`Domain` gains optional fields (all absent when unused):

```ts
type PathRule = { path: string; service?: string; target_port?: number; strip_prefix?: boolean };
type Domain = { ...existing,
  paths?: PathRule[];                                  // path rules of this host
  tls?: "auto" | "internal" | "off";                   // absent = server default (AVEN_TLS)
  trust_forwarded_proto?: boolean;                     // only meaningful with tls "off"
  redirect?: { to: string; permanent: boolean };       // host or http(s) URL
  basic_auth?: { username: string; has_password: boolean } };   // the password is never returned
```
- `POST /services/{sid}/instances/{eid}/domains` accepts the same fields next to `host`/`target_port`.
- `PATCH /services/{sid}/instances/{eid}/domains/{host}` `{ target_port?, paths?, tls?, trust_forwarded_proto?, redirect?, basic_auth? }` → `200 Domain`. Only the sent keys change; `null` clears a value. `basic_auth: {username, password}` sets the password (bcrypt); `{username}` alone keeps the stored password; `null` removes auth. `PATCH .../instances/{eid}` with `domains` accepts the same shape.
- **Path rules**: the host's default service gets every request that matches no rule. `service` is the name of another service in the *same environment* on the *same node* (empty = this service). A path ending in `/*` is a prefix rule (`/api/*` matches `/api` and `/api/...`), anything else matches exactly. `strip_prefix` (prefix rules only) removes the prefix before proxying. Ordering is deterministic and most specific first: exact paths, then prefixes by length (longest first), then the catch-all, so `/api/v1/*` beats `/api/*`. A rule whose target is not live is skipped (the request falls to the default service). A host with rules but no live default service answers 502 for unmatched paths.
- **TLS mode**: `auto` is ACME with http→https redirect, `internal` is Caddy's internal CA (also redirects), `off` serves plain HTTP on :80 and never redirects (use it behind a cloudflared tunnel that connects over HTTP, otherwise the tunnel loops on 308). Domains without `tls` follow the global `AVEN_TLS`. Caddy runs one server per scheme: `aven` follows the global mode, `keel-http` / `keel-tls` hold the domains that differ.
- **trust_forwarded_proto** passes the incoming `X-Forwarded-Proto` through to the app (Caddy otherwise overwrites it for untrusted clients). Use it with `tls: "off"` behind a TLS-terminating proxy.
- **redirect**: 308 (`permanent`) or 307, `Location: <to><original path and query>`; `to` without a scheme means `https://<to>`. Cannot be combined with path rules or point at the same host.
- **External upstream** (`upstream: "host:port"`, optional `upstream_paths: ["/api/v1"]`): the host is proxied to something outside aven, e.g. a container of a compose project that stays on the host, regardless of the service's deployment state. Only an instance admin (session or `AVEN_API_TOKEN`, not scoped tokens) with project access can set or change it. The target must be on the allowlist of the node the instance is placed on: `AVEN_EXTERNAL_UPSTREAMS` on aven-server for the local node, `--external-upstreams`/`AVEN_EXTERNAL_UPSTREAMS` on aven-agent (reported in ping, and enforced again by the agent's route validation), so the server can never widen what a node's proxy reaches. Hosts must be a container name or a private IP; localhost, loopback, link-local and `keel-proxy` itself are rejected. keel-proxy joins the docker networks in `AVEN_PROXY_NETWORKS` / `--proxy-networks` at start so container names resolve. With `upstream_paths` only those prefixes are proxied (`/api/v1` matches `/api/v1` and `/api/v1/...`) and other paths get 404. Cannot be combined with `redirect` or `paths`.
- **basic auth** covers the whole host including path rules. The bcrypt hash is stored in the settings table, never in the instance/deployment JSON.

### Resource limits and build secrets (instance settings, `PATCH /services/{sid}/instances/{eid}`)
- `memory_limit_mb` (6..4194304 or null) and `cpu_limit` (0.01..256 or null) become `docker run --memory/--cpus` on every replica. They are settings: a change shows up in `pending_changes` and applies on the next deploy. Remote nodes get the same limits.
- `build_secrets: string[]` lists variable names that are passed to the Dockerfile build as BuildKit secrets (`--secret id=NAME,env=AVEN_SECRET_NAME`) and never as build args. Read them in the Dockerfile with `RUN --mount=type=secret,id=NAME ...`. They do not appear in `docker history`, the image config, argv or logs.
- `skip_builds: boolean` reuses a retained image built from identical source/build inputs on repo services (see Skipped builds). Default false; not a pending change.
- `build_node: "local" | ""` (default `""`, not a pending change; applies to the next deployment). With `"local"`, a repo, function or `aven up` service that deploys to a **remote** node is cloned and built on the aven-server node (like Dokploy's build server), then sent to the deployment node: `docker save | gzip` is streamed over the agent connection (`upload.begin {kind:"image", image}`) and the agent runs `docker load`. The agent only accepts `keel/<service>:<tag>` names and checks the archive's `manifest.json` RepoTags and `index.json` image names before loading, so the server cannot overwrite any other image. The local copy is removed after the transfer unless the deployment also places replicas on the local node; extra placement nodes get the same image. Redeploys and rollbacks re-send a local copy if the deployment node lost the image. Image and database services still pull on the deployment node. Agents older than this reject the upload (`unknown field "image"`); the deployment fails asking to upgrade the agent.

### SSH deploy keys
- `GET /services/{sid}/deploy-key` → `{service_id, public_key, fingerprint, created_at}` or 404. The private key is never returned.
- `POST /services/{sid}/deploy-key` creates an ed25519 key for a repo service (409 if one exists; `?rotate=1` replaces it) → `201`. Add `public_key` as a (read-only) deploy key on the repository.
- `DELETE /services/{sid}/deploy-key` → `204`.
- A service whose `repo_url` is `git@host:path` or `ssh://...` and that has a key clones with it (local and remote nodes). The key is written to a 0600 temp file, handed to git via `GIT_SSH_COMMAND` (only the path is in the command line) and deleted afterwards. Host keys: github.com's published keys are built in; any other host is trusted on first use, recorded, and a changed key later is rejected.

### Database logical backups and S3 destinations
Supported database kinds: postgres (`pg_dump -Fc` / `pg_restore`), mysql and mariadb (`mysqldump` of all user databases, gzip / `mysql`), redis (`redis-cli --rdb`; restore stops the service briefly), mongo (`mongodump --archive --gzip` / `mongorestore --drop`). The dump runs in a one-off container of the database's own image on the environment network and its stdout is streamed to the destination (nothing is buffered in full; S3 uses multipart with 8 MiB parts). Works alongside the volume snapshot backups. Only databases on the local node are supported for now.
```ts
type DbBackup = { id, service_id, environment_id, instance_id, kind, status: "running"|"done"|"failed", trigger: "manual"|"schedule",
                  destination_id: string /* "" = local */, file_name, size_bytes, error: string|null, created_at, finished_at };
type DbBackupConfig = { enabled: boolean; cron: string; retention: number; destination_id: string };
type BackupDestination = { id, name, type: "local"|"s3", endpoint?, region?, bucket?, prefix?, access_key?, has_secret: boolean, created_at };
```
- `GET|POST /services/{sid}/instances/{eid}/db-backups` list / start a manual backup `{destination_id?}` → `202 DbBackup` (409 if another backup or restore of that DB runs).
- `GET|PUT /services/{sid}/instances/{eid}/db-backups/config` schedule (`cron`, default `0 3 * * *`), `retention` (newest N successful backups are kept, older ones are deleted from storage; default 7) and default destination.
- `POST /db-backups/{id}/restore` `{service_id?, environment_id?}` → `202 {status:"running",...}`; target defaults to the backup's own database, or another database service of the same kind (mysql/mariadb are interchangeable) in the same environment. The target's data is overwritten. `GET /db-backups/{id}/restore` returns `running|done|failed` (+`error`) of the last restore since the server started.
- `GET /db-backups/{id}`, `DELETE /db-backups/{id}` (also deletes the stored file), `GET /db-backups/{id}/download`.
- `GET|POST /backup-destinations`, `PATCH|DELETE /backup-destinations/{id}`, `POST /backup-destinations/{id}/test` → `{ok, error?}`. S3 destinations take `{name, type:"s3", endpoint, region?, bucket, prefix?, access_key, secret_key}` (path-style addressing, AWS S3 / MinIO / R2). The secret is never returned (`has_secret` only); a PATCH without `secret_key` keeps the stored one. Deleting a destination that still holds backups returns 409.
- Local backups live in `AVEN_DATA/dbbackups/<service>-<env>/` (files 0600).

### Secrets at rest
One master key, `AVEN_DATA/master.key` (0600, 32 random bytes, generated on first start), encrypts with AES-256-GCM (`enc:v1:` prefix): registry passwords, S3 secret keys and deploy-key private keys. Plaintext registry passwords from older versions are encrypted on the next start. Losing the key makes those secrets unreadable, so include it in your server backup (separately from the database).

## v1.5 additions

### PR preview environments
A project can create one environment per GitHub pull request, forked from a base environment .

```ts
type Project = { ...existing,
  pr_environments: {
    enabled: boolean;                    // default false
    base_environment_id: ID | null;      // null = default_environment_id
    auto_delete: boolean;                // default true: delete the environment when the PR closes (merged or not)
    domain: boolean;                     // default true: generate domains (needs AVEN_BASE_DOMAIN)
    focused: boolean;                    // default false: deploy changed services and their variable-reference dependencies
  };
  pr_from_forks: boolean;                // default false
  pr_from_bots: boolean;                 // default false: create environments for GitHub bot PRs
};
type Environment = { ...existing,
  kind: "standard" | "preview";
  pr_number: number | null;              // null for standard environments
  pr_url: string | null;
  pr_branch: string | null;              // PR head branch
};
```
- `PATCH /projects/{pid}` accepts `pr_environments` (partial object, only the sent keys change; `base_environment_id: null` or `""` resets to the default environment), `pr_from_forks`, and `pr_from_bots`. `base_environment_id` must be a `standard` environment of the project, otherwise `400`. Deleting the base environment resets it to `null`.
- The GitHub webhook (`POST /webhooks/github`) handles `pull_request` with actions `opened`, `reopened`, `synchronize`, `closed`; other actions return `202 {ignored}`. Response: `202 {environments: [{project_id, environment_id?, action: "created"|"deployed"|"deleted"|"kept"|"skipped", reason?, deployments: [{id, status}]}]}`. Invalid payloads (non-hex head SHA, bad branch names) return `400`. Subscribe the webhook to **Pull requests** in addition to **Pushes**.
- **opened / reopened**: for every project with `pr_environments.enabled` whose base environment has a service instance with the PR's base repository (same URL normalization as push) and `branch == pull_request.base.ref`, aven creates environment `pr-<number>` (`kind: "preview"`) with the `copy_from` rules (settings, variables, shared variables; volumes are created new and empty, data is not copied; TCP proxies get new ports). Custom domains are never copied: when `domain` is true and `AVEN_BASE_DOMAIN` is set, each service that had at least one domain in the base gets one generated domain `<service>-pr-<number>.<base>`. Matching repo instances switch to the PR head branch and deploy exactly `head.sha` (trigger `github`). With `focused: true`, aven reads changed files from GitHub's Pull Request Files API, deploys matching repo services and their service dependencies (variable references), and records other services as a `skipped` deployment (shown as 건너뜀 on the canvas). References to a skipped service resolve to that service's fully resolved variables in the PR environment's base environment (the base keeps serving it; private-network hostnames of the base are not reachable from the preview network, use public domains). Skipped PR-repo services still switch to the PR branch, so deploying one manually from the canvas adds it to the preview with the PR code. Watch-path patterns are relative to the repository root; when there are no watch paths, `root_dir` selects changed files under that directory. If the file list cannot be fetched, aven falls back to deploying every service in the preview. `AVEN_GITHUB_TOKEN` is used when set; private repositories need a token with pull request read access. Event `environment.preview_created`. If the preview environment already exists (redelivery, or reopened after a close with `auto_delete: false`) the head SHA is deployed instead. If a standard environment called `pr-<number>` already exists the project is skipped.
- **synchronize**: deploys the new `head.sha` to matching PR repo services (`offline` instances are skipped). With `focused: true`, the current PR file list is fetched again and only matching services are deployed, plus dependencies that have not been deployed in the preview yet. If GitHub does not return a complete file list, aven falls back to the existing full-preview behavior. Skipped when `pr_environments` is disabled meanwhile.
- **closed**: with `auto_delete` the preview environment is deleted through the normal environment delete path (containers, volumes, network); event `environment.preview_deleted`. With `auto_delete: false` it is kept (`action: "kept"`).
- **Pushes to the PR head branch** never deploy instances of preview environments (the push handler skips `kind: "preview"` environments); the `synchronize` event deploys them, so every PR commit deploys once.
- **Forks** (`head.repo` differs from `base.repo`, or was deleted) are only handled when `pr_from_forks` is true, because fork code then runs with the copied variables. A fork's head branch does not exist in the base repository, so its instances keep the base branch and the deploy fetches `head.sha` by SHA from the base repository (GitHub serves PR heads under `refs/pull/<n>/head`).
- **Bot PRs** are ignored by default. Set `pr_from_bots: true` to allow PRs whose webhook user is a GitHub bot; fork PRs still require `pr_from_forks: true` separately.
- PR environments match instances regardless of `auto_deploy`; enabling the project setting is the opt-in.

### GitHub commit statuses
When `AVEN_GITHUB_TOKEN` is set, deployments that have a commit SHA on a `github.com` repository post commit statuses (`POST /repos/{owner}/{repo}/statuses/{sha}`):

| deployment status | state | description |
|---|---|---|
| `waiting` | `pending` | CI 통과를 기다리는 중 |
| `queued` | `pending` | 배포 대기 중 |
| `initializing` (first in-progress step) | `pending` | 배포 중 |
| `live` | `success` | 배포 완료 |
| `failed` | `failure` | 배포 실패 |
| `cancelled` / `removed` (superseded) | `error` | |
| `skipped` because CI failed | `error` | |

- `context` is `keel/<service>-<environment>`; `target_url` is `AVEN_PUBLIC_URL/?project=..&env=..&svc=..&dep=..` when `AVEN_PUBLIC_URL` is set. Deployments skipped by watch paths post nothing. Manual deploys post once the clone has resolved the SHA.
- Posting is asynchronous (one ordered background queue, 10 s timeout per request). Failures are logged and never affect the deployment.
- `AVEN_GITHUB_API_URL` overrides the API base (default `https://api.github.com`, e.g. for GitHub Enterprise).
- The token needs the "Commit statuses: write" permission (fine-grained) or `repo:status`.

### Wait for CI
- Instance setting `wait_for_ci: boolean` (default false, `PATCH /services/{sid}/instances/{eid}`). It applies to the next push right away and never shows up in `pending_changes`.
- New `DeploymentStatus` `"waiting"` (not terminal). A GitHub-triggered deployment (push, or a PR preview deploy of the PR repo instances) of an instance with `wait_for_ci` is created as `waiting` instead of `queued` and is not put in the queue. Event `deploy.waiting`.
- The webhook handles `check_suite` and `workflow_run` with `action: "completed"`: for every `waiting` deployment with that `head_sha` on the same repository, conclusion `success` / `neutral` / `skipped` moves it to `queued` (normal queue from there); `failure` / `timed_out` / `cancelled` / `action_required` / `startup_failure` finishes it as `skipped` with `error: "CI 실패"` (event `deploy.skipped`). Other conclusions (`stale`, null) are ignored. Response `202 {released: [id], skipped: [id]}`. Subscribe the webhook to **Check suites** and/or **Workflow runs**.
- Any newer deployment of the same instance (push, manual, redeploy) finishes older `waiting` deployments as `removed` ("superseded by a newer deployment").
- `waiting` deployments are persisted: a server restart neither fails nor starts them, reconcile never starts them, and the CI webhook releases them later. `POST /deployments/{did}/cancel` cancels a waiting deployment.
- The first completed suite/run decides: with several independent CI workflows, the first one to finish successfully releases the deployment (aven does not aggregate all checks of a commit).

### Config-as-code: environment overrides and new keys
`aven.json` / `aven.toml` / `railway.json` / `railway.toml` (first one found, same order as before) now also understand:

```jsonc
{
  "$schema": "https://railway.com/railway.schema.json",   // accepted and ignored
  "build":  { "buildCommand": "npm run build:prod", "railpackVersion": "0.9.0" },
  "deploy": { "sleepApplication": true, "overlapSeconds": 20, "drainingSeconds": 45,
              "region": "eu-west", "requiredMountPath": "/data" },
  "environments": {
    "staging": { "deploy": { "numReplicas": 1 } },
    "pr":      { "build": { "builder": "RAILPACK" }, "deploy": { "sleepApplication": true } }
  }
}
```
- Unknown keys (in any section, JSON or TOML) are ignored; a known key with the wrong type is still an error that fails the deploy.
- `environments.<name>.{build,deploy}` is merged over the top-level values when deploying to the environment with exactly that name. Only keys that are present (non-null) override. When no block matches the name and the environment is a PR preview environment (`kind: "preview"`), `environments.pr` is used. The build log names the block used.
- `build.buildCommand` → passed to railpack as `RAILPACK_BUILD_CMD` (railpack builds, local and remote). A user variable `RAILPACK_BUILD_CMD` wins. Dockerfile builds ignore it (logged). `""` clears it.
- `build.railpackVersion` is accepted and ignored with a log line (the railpack version follows `AVEN_RAILPACK_BIN` / `AVEN_RAILPACK_FRONTEND`).
- `deploy.sleepApplication` → `sleep_when_idle` for this deployment. When the file changes the value, the current deployment's value decides whether the service sleeps; otherwise the instance setting applies as before.
- `deploy.overlapSeconds` (0..600) / `deploy.drainingSeconds` (0..300) → the instance settings below, for this deployment.
- `deploy.region` (or a `deploy.multiRegionConfig` with exactly one region; its `numReplicas` is used when `numReplicas` is not set) selects a node whose name equals the region (case-insensitive) or that has the label `region=<value>`, and that is schedulable. If the deployment already runs on a matching node nothing happens. Otherwise, only for instances **without volumes** and not for CLI uploads, aven sets the instance's `node_id` to the first connected matching node, creates a new deployment there (same trigger, commit SHA, branch, variables) and finishes the current one as `removed` (`error: "deploy.region: redeploying on another node"`, event `service.updated`). With volumes, no matching node, or no connected matching node it logs a warning and deploys in place. `multiRegionConfig` with several regions is ignored with a warning (no multi-node placement).
- `deploy.requiredMountPath` (absolute path): the deploy fails before pre-deploy/start with `deploy.requiredMountPath: <path> 경로에 마운트된 볼륨이 없습니다…` when no volume of the instance is mounted at exactly that path.
- Snapshot only: `Deployment.spec` may contain `build_command` and `required_mount_path` (set from the file); they are not instance settings.

### Deployment overlap and draining
```ts
type ServiceInstance = { ...existing,
  overlap_seconds: number;         // default 0, 0..600
  draining_seconds: number | null; // default null, 0..300
};
```
- Settable with `PATCH /services/{sid}/instances/{eid}`; out-of-range values return `400`. Both show up in `pending_changes` and apply from the next deployment (the new deployment's values are used when it retires the previous one).
- `overlap_seconds`: after the new deployment is live and the proxy points to it, old containers keep running this long before they are stopped (the minimum drain stays 3 s). Volume services stop the old containers before starting new ones (recreate), so overlap does not apply to them. The deployment worker waits through the overlap, so a following deployment of the same instance starts after it.
- `draining_seconds`: SIGTERM → SIGKILL grace (`docker stop -t`) when old containers are stopped: on cutover, on the recreate stop of volume services, on `stop` and on `restart`. `null` keeps the previous defaults (10 s; 30 s for the recreate stop). Remote nodes use the existing `container.stop/remove/restart` `grace` field (agent limit 0..300, unchanged protocol); the server extends the call timeout for long graces.

### Wildcard custom domains
- `POST /services/{sid}/instances/{eid}/domains` (and `PATCH` of the instance's `domains`) accepts `host: "*.example.com"`. A wildcard matches exactly one label (`a.example.com`, not `a.b.example.com` or `example.com`) and needs at least two labels after `*.`. Wildcards are always `kind: "custom"`, also below `AVEN_BASE_DOMAIN`.
- Routing: specific hosts always take precedence over wildcard hosts (they are ordered first in the Caddy config), across services. HTTP logs, sleeping/wake and `target_port` resolve a request host to the exact domain first, then to the wildcard.
- DNS records: `dns` lists `A`/`AAAA` records named `*.example.com`. `status` is checked by resolving a probe name (`aven-dns-check.example.com`).
- TLS per domain: `internal` issues a wildcard certificate from Caddy's internal CA; `off` serves HTTP; `auto` (or default) uses **on-demand TLS**: no certificate is requested for `*.example.com` itself (that would need a DNS challenge); each concrete subdomain gets its own Let's Encrypt certificate (HTTP-01/TLS-ALPN) on its first TLS handshake. keel-proxy asks permission via Caddy's `http` permission module (Caddy ≥ 2.8) at `http://127.0.0.1:5380/api/internal/tls-ask?domain=<name>`, a listener that exists only inside the keel-proxy container and forwards to the wake unix socket (`/run/keel/wake.sock`) with `Host: keel-tls-ask.invalid`. aven-server (local node) or aven-agent (remote node) answers `200` only when the name equals a configured domain or matches a configured wildcard (the agent checks the routes the server last applied to that node); otherwise `404`/`400`. This endpoint is not reachable from the public listeners or the API port and has no authentication by design.
- Requirement: the wake socket must be available (it is on a default install); without it sleeping services cannot wake and wildcard subdomains cannot obtain certificates.

### Volume size limits (soft)
```ts
type Volume = { ...existing, size_limit_mb: number | null };   // MiB (1 MiB = 1,048,576 bytes), 1..16777216
type NotifyEvent = ...existing | "volume.near_limit" | "volume.limit_exceeded";
```
- Settable on `POST /environments/{eid}/volumes` and `PATCH /volumes/{vid}` (`null` removes it); invalid values return `400`. Changing the limit resets the notification state.
- Enforcement is soft: docker `local` named volumes cannot be capped, so writes are never blocked. The size refresh (every 5 minutes, `docker system df`) compares `size_bytes` with the limit: crossing 90% emits `volume.near_limit`, reaching 100% emits `volume.limit_exceeded`. Each is emitted once per crossing (the state is stored; dropping back below re-arms it).
- Both events can be selected in `notify_events` for the project webhook.
- Volumes on remote nodes have no measured size (same as `size_bytes: null` today), so they get no limit events.

### Service shell (exec)
- `GET /services/{sid}/instances/{eid}/exec?replica=<name>&cmd=<string>&cols=<n>&rows=<n>` → WebSocket upgrade (auth required like every API route).
  - `replica`: a container name from the current deployment's `replicas` (default: the first). Unknown names → `404`; no current deployment / no containers (e.g. cron between runs) → `409`.
  - `cmd`: optional, run as `sh -lc <cmd>` (max 4096 bytes). Default: `sh -lc 'command -v bash >/dev/null && exec bash || exec sh'`. The image needs `sh`.
  - `cols`/`rows`: initial TTY size, clamped to 1..1000 (default 80x24). The session gets `TERM=xterm-256color`.
- Frames: **binary** = raw bytes (client → server: stdin; server → client: TTY output, stdout and stderr merged). **text** = JSON control:
  - client → server `{"type":"resize","cols":n,"rows":n}` (invalid ones are ignored);
  - server → client `{"type":"exit","code":n}` when the process ends (`-1` if unknown), then a normal close (1000); `{"type":"error","message":"..."}` for start failures (followed by close 1011) and the idle timeout (followed by close 1000).
- Limits: 8 concurrent sessions per user (`429` beyond), 64 KiB max client frame, 30 min idle timeout (no input and no output), 12 h hard cap on remote nodes.
- CSRF / cross-site WebSocket hijacking: the upgrade is a GET, so the usual cookie CSRF rule does not apply. Instead the request is rejected with `403` when `Sec-Fetch-Site` is `cross-site`/`same-site`, or when an `Origin` header is present and its host differs from `Host` (`Origin: null` included). Requests without `Origin` (CLI, curl) are accepted.
- Only aven workload containers can be entered: `keel.managed=true` with role `app` or `cron` (checked by aven-server for the local node via the Docker Engine API, and again by aven-agent on remote nodes).
- Audit: each session emits event `service.exec` ("`<service> (<env>)`: admin(세션|API token)이 `<replica>` replica에서 셸을 열었습니다"). What is typed and `cmd` are never logged or stored.
- Local node: aven-server talks to the Docker Engine API over the unix socket (`DOCKER_HOST=unix://...` or `/var/run/docker.sock`; other `DOCKER_HOST` schemes are not supported for exec): `POST /containers/{id}/exec` (Tty) → `POST /exec/{id}/start` hijacked with `Upgrade: tcp` → `POST /exec/{id}/resize`, `GET /exec/{id}/json` for the exit code.
- Remote nodes: new agent operations (allowlisted, strict decoding):
  - `exec.start {session, name, cmd[], cols, rows}`: `session` is a server-chosen 24-hex id. Output is streamed as `DataChunk` stream messages; the final reply is `{exit_code}`. Cancelling the call (server `cancel`, connection loss) closes the session. At most 8 sessions per agent.
  - `exec.input {session, data}` (≤ 64 KiB, sent one at a time in order), `exec.resize {session, cols, rows}`, `exec.close {session}`.
- Closing the WebSocket closes the exec stream; the shell then gets EOF/hang-up on its TTY. Background processes started with `nohup`/`&` may keep running inside the container (same as `docker exec`).
- The CLI equivalent is `aven ssh` (see `docs/cli.md`).

### Environment sync
Bring the changes of one environment into another . Both environments must belong to the same project; `from` equal to the target (this also covers syncing a preview environment into itself) or an environment of another project → `400`.

- `GET /environments/{eid}/sync-preview?from=<source eid>` → what would change in `{eid}` so it matches `from`. Nothing is written.
  ```json
  {
    "from": {"id": "env_…", "name": "production"},
    "to":   {"id": "env_…", "name": "staging"},
    "services": [{
      "service_id": "svc_…", "service_name": "web",
      "action": "update",
      "settings":  [{"field": "start_command", "from": null, "to": "npm start"}],
      "variables": [{"key": "API_KEY", "action": "added", "from": null, "to": "•••"},
                    {"key": "DB", "action": "changed", "from": "•••", "to": "${{db.DATABASE_URL}}"}],
      "volumes":   [{"mount_path": "/data", "size_limit_mb": 512}],
      "protected_variables": []
    }],
    "shared_variables": [{"key": "OLD", "action": "removed", "from": "•••", "to": null}]
  }
  ```
  - Only services with at least one change are listed. `action` is `create` when the target instance was never configured (no current deployment, no `repo_url`/`image`, no variables) while the source instance is; otherwise `update`.
  - `settings` covers these instance fields (`from` = target's current value, `to` = source value, raw JSON): `repo_url, branch, root_dir, image, builder, dockerfile_path, watch_paths, auto_deploy, start_command, pre_deploy_command, port, healthcheck_type, healthcheck_path, healthcheck_timeout_s, restart_policy, restart_max_retries, replicas, cron_schedule, sleep_when_idle, sleep_after_s, memory_limit_mb, cpu_limit, build_secrets, skip_builds, overlap_seconds, draining_seconds, wait_for_ci`.
  - `variables` / `shared_variables`: `added | changed | removed` per key. Values are shown only when they contain a `${{ }}` reference; anything else is masked as `"•••"`.
  - `volumes`: mount paths present on the source instance but not on the target (a new, **empty** volume is created; data is never copied). `size_limit_mb` is copied.
  - Never touched: domains, TCP proxy, node placement (`node_id`), deploy keys, compose runtime settings. For database services (`database_kind` set) the generated password variables (`POSTGRES_PASSWORD`/`PGPASSWORD`, `MYSQL_ROOT_PASSWORD`/`MYSQLPASSWORD`, `REDIS_PASSWORD`/`REDISPASSWORD`, `MONGO_INITDB_ROOT_PASSWORD`/`MONGOPASSWORD`) are neither copied nor removed; they are listed in `protected_variables`. This matches how a new environment generates its own database credentials.
- `POST /environments/{eid}/sync` `{from, services?: [sid], include?: {settings, variables, shared_variables, volumes}, deploy?: bool}` → `200`
  ```json
  {"from": {...}, "to": {...}, "shared_variables": 1,
   "services": [{"service_id": "svc_…", "service_name": "web", "settings": 2, "variables": 1, "volumes": 1,
                 "deployment_id": "dep_…", "deploy_skipped": "…"}]}
  ```
  - `services` limits the service changes to these ids (unknown ids → `400`); omitted = all. `include` omitted = everything; when given, only the categories set to `true` are applied.
  - Changes are staged like normal edits (`PATCH /services/{sid}/instances/{eid}`): they appear in `pending_changes` of instances that have a current deployment. Every change is validated before anything is written (invalid settings or a variable reference cycle → `400`, nothing applied).
  - `deploy: true` enqueues a deployment for every changed instance (`deployment_id`); instances that cannot be deployed (e.g. no source) get `deploy_skipped` with the reason.
  - Emits event `environment.synced` ("환경 `<to>`를 `<from>` 환경에서 동기화했습니다 (서비스 n개[, 공유 변수 n개])[ 및 배포 요청]"). Variable values are not in the message.

### Database data browser
Browse and query database services (`database_kind` postgres, mysql, redis, mongo) from the dashboard. aven runs the database's own client inside the **running** database container through a one-shot (non-TTY) exec, so it works on local and remote nodes and needs no network access to the database. Credentials come from the current deployment's final environment and are passed as exec environment variables only (`PGPASSWORD`, `MYSQL_PWD`, `REDISCLI_AUTH`, `MONGO_URI`), never in argv; query text goes over stdin. Clients connect to `127.0.0.1:<service port>` inside the container.

All routes require auth. Errors: not a database service → `400`; no running deployment → `409`; node offline / agent without `exec.run` → `409`; client failed → `400` with the client's stderr; timeout → `504`; 8 concurrent data requests per user → `429`.

- `GET /services/{sid}/instances/{eid}/data/tables` → `{kind, tables: [{schema, name, kind, rows}], truncated}`
  - postgres: tables, partitioned tables, views, materialized views, foreign tables outside `pg_catalog`/`information_schema`; `rows` = `pg_class.reltuples` (approximate, `null` if never analyzed or a view).
  - mysql/mariadb: `information_schema.tables` without system schemas; `rows` = `table_rows` (approximate for InnoDB).
  - mongo: collections of every database except `admin`/`config`/`local` (`schema` = database name, `kind` `collection`|`view`, `rows` = `estimatedDocumentCount()`).
  - redis: keyspace summary from `INFO keyspace`: `[{name: "db0", kind: "keyspace", rows: <keys>}]`.
- `GET /services/{sid}/instances/{eid}/data/tables/{name}?schema=&limit=50&offset=0` → `{schema, name, columns: [string], rows: [[cell]], limit, offset, has_more, truncated}` (`limit` 1..500, `offset` 0..100000000). Not for redis (`400`).
  - SQL: `SELECT * FROM <schema>.<name> LIMIT limit+1 OFFSET offset`. Names are validated (1..128 bytes, UTF-8, no control characters) and quoted (`"a""b"` / `` `a``b` ``); `schema` defaults to `public` (postgres) or the connection's database (mysql).
  - mongo: `schema` is the database (default `test`); names are embedded as JSON string literals: `db.getSiblingDB(<db>).getCollection(<name>).find({}).skip(offset).limit(limit+1).toArray()`.
  - Cells are strings, `null` for SQL NULL, or JSON values for mongo (relaxed Extended JSON, e.g. `{"$oid": "…"}`). Mongo columns are `_id` first, then top-level keys in first-seen document order (keys of one document sorted by name).
- `GET /services/{sid}/instances/{eid}/data/keys?pattern=*&cursor=0&db=0` (redis) → `{cursor, keys: [{key, type, ttl}], db}`. One `SCAN <cursor> MATCH <pattern> COUNT 200` step plus `TYPE`/`TTL` per key; continue with the returned `cursor` until it is `"0"`. `pattern` ≤ 1024 bytes, `db` 0..1000.
- `GET /services/{sid}/instances/{eid}/data/keys/{key}?db=0` (redis) → `{key, type, ttl, db, length, value, truncated}`; unknown key → `404`. Type-aware preview: `string` first 64 KiB (`value` string), `list` first 100 items, `set` one `SSCAN` page (≈100), `zset` first 100 `[{member, score}]`, `hash` one `HSCAN` page `[{field, value}]`, `stream` last 50 entries `[{id, fields: [k, v, …]}]`. `truncated` = more items than shown.
- `POST /services/{sid}/instances/{eid}/data/query` `{query, db?}` → `{kind: "table"|"text", columns, rows, text, truncated, row_count, duration_ms}`
  - Runs the operator's query as is (any authenticated admin): SQL for postgres/mysql, a mongosh expression for mongo (end cursors with `.toArray()` to get a table), redis commands one per line in `redis-cli` syntax (`db` selects the logical database). Max 64 KiB of query text.
  - Limits: 30 s (postgres also sets `statement_timeout=30s`; the client is started under `timeout -s KILL 40` when the image's `timeout` supports it), 1 MiB of client output, 1000 rows. `truncated: true` when a limit cut the result.
  - postgres output is `psql -X -q -v ON_ERROR_STOP=1 --csv` (NULL printed as a private marker so it differs from `''`); mysql output is `--batch` TSV (escaped `\t \n \\ \0`; `NULL` → null, so the string `'NULL'` is indistinguishable); mongo output is `mongosh --quiet --json=relaxed`; redis output is `redis-cli --no-raw` and returned as text. Results that are not a single table (several statements, DDL, no output) come back as `kind: "text"`.
  - Audit: each query emits `database.query` ("`<service> (<env>)`: admin(세션|API token)이 쿼리를 실행했습니다: `<first 200 characters, whitespace collapsed>`"). The query prefix is recorded because it is the operator's own action; literals in that prefix (e.g. a password in an `ALTER USER`) end up in the activity feed, so prefer the CLI for such statements. Results are never logged or stored. Browsing (`tables`, `keys`) emits no event.
- Agent: new allowlisted operation `exec.run {name, cmd[], env?[], stdin?, max_output?, timeout_s?}` → `{stdout, stderr, exit_code, truncated}` (strict decoding; `env` entries `KEY=VALUE` with `[A-Za-z_][A-Za-z0-9_]*` keys, ≤ 32 entries / 64 KiB, no NUL; `stdin` ≤ 256 KiB; `max_output` ≤ 2 MiB; `timeout_s` 1..120; stderr kept up to 64 KiB). Same container check as `exec.start` (role `app`/`cron`, `keel.managed=true`). Agents without it reply "operation not allowed" and aven-server returns `409` asking to upgrade the agent. The existing TTY `exec.start` is unchanged.

### Users, roles and tokens
Multi-user auth with separate instance and workspace roles, project membership, TOTP 2FA, server-side sessions and scoped API tokens. `AVEN_ADMIN_PASSWORD` authenticates the built-in instance owner; `AVEN_API_TOKEN` remains an unrestricted operator credential.

**Built-in owner.** Migration `migrateUsers` creates user `usr_admin` (`admin@local`, name `admin`, role `owner`, `builtin: true`). It has no stored password: `POST /auth/login {password}` without `email` (or with `email: "admin@local"`) is compared constant-time with `AVEN_ADMIN_PASSWORD`, so ops rotate it through the env var. Its sessions store an HMAC fingerprint of the env password and stop working when the env var changes. It cannot be deleted, its role is always `owner`, and its password cannot be changed through the API; another owner can disable it (that also blocks password login; `AVEN_API_TOKEN` keeps working because it is an operator credential — unset it to disable it).

**Sessions.** The `keel_session` cookie (same flags: HttpOnly, SameSite=Strict, Secure on HTTPS; same CSRF rules) now holds a random 256-bit token; the server stores only its SHA-256 in `sessions`. Lifetime is 30 days sliding (extended and the cookie re-sent at most once a minute). Logout deletes the session; changing the password deletes the user's other sessions; disabling a user or an admin password reset deletes all of them. **Breaking:** pre-v1.5 HMAC cookies are invalid after the upgrade; users log in again.

**Login.** `POST /auth/login {email?, password, totp_code?}` → `204` + cookie.
- Wrong credentials → `401` (`비밀번호가 올바르지 않습니다` without email, `이메일 또는 비밀번호가 올바르지 않습니다` with email; unknown emails take the same time). Disabled account (correct password) → `403`.
- 2FA enabled and no `totp_code` → `401 {"error":"totp_required"}` (not counted as a failure). `totp_code` is a 6-digit TOTP code or a recovery code (`xxxxx-xxxxx`, case/space/hyphen-insensitive). Wrong codes count as failures.
- Brute-force limits as before (per IP 5, per account 20, keyed by email or `admin`); they also cover TOTP codes, `PATCH /auth/me` current-password checks and 2FA enable/disable (separate account keys).
- Passwords: argon2id (`m=19456 KiB, t=2, p=1`, 16-byte salt, PHC string), any non-empty password up to 256 bytes (no minimum length).

**Instance roles** (`users.role`): `owner` > `admin` > `member` > `viewer`. Owner/admin manage instance operations and accounts; these roles grant no workspace/project access. Only instance owners can grant or manage the instance owner role.

**Workspace roles** (`workspace_members.role`): `owner` > `admin` > `member` > `viewer`. Owner/admin see and manage every project in that workspace. Member/viewer need project grants; members may create projects (they become project admin). Only workspace owners can grant or manage local owner roles. The last active workspace owner cannot be demoted, removed, disabled or deleted.

**Project roles** (`project_members.role`): `admin`, `member`, `viewer`. Workspace membership is required. Effective project role = `admin` for workspace owner/admin, otherwise the highest direct or enabled group role.

Permission matrix (project routes; "–" = 403, invisible projects are 404 for everyone below):

| Action | viewer | member | admin | ws admin/owner |
|---|---|---|---|---|
| Read project, canvas, instances, deployments, logs, metrics, events, volumes, backups list, stacks, members | ✓ (variable values masked) | ✓ | ✓ | ✓ |
| Read raw/resolved variable values, shared variable values, notify webhook URL | – (`"•••"`) | ✓ | ✓ | ✓ |
| Deploy/redeploy/rollback/cancel/restart/stop, upload, edit services/instances/variables/domains/TCP proxy/volumes/deploy keys, compose, templates, env create/rename/sync, cron run, backups create/restore/delete | – | ✓ | ✓ | ✓ |
| Shell (exec), data browser (tables/keys/query), DB backup download | – | ✓ | ✓ | ✓ |
| Project settings (`PATCH /projects/{pid}`), delete project, delete environment, manage members | – | – | ✓ | ✓ |
| Project registries | – | – | ✓ | ✓ |

Nodes (write), backup destinations, users, invites list and audit require instance admin independently of the table above. GitHub connections are personal.

Always allowed for any signed-in user: `GET /auth/me`, `/system`, `/templates` (filtered), `/nodes` (read), `/nodes/{nid}/metrics`, own account/sessions/2FA/tokens. `GET /users` needs instance admin. `GET /projects` and `/auth/me.projects` list only visible projects.

Viewer masking: `GET /services/{sid}/instances/{eid}` (and the instances inside `/environments/{eid}/canvas`) return every `variables` value as `"•••"` (keys kept); `/variables/resolved` returns `value: "•••"` (keys and `source` kept); `GET /environments/{eid}/shared-variables` masks values; `notify_webhook_url` is `"•••"` in project responses. Sync previews already mask non-reference values.

Enforcement is central: every authenticated route is registered with a declared permission (`internal/api/api.go`, `authz.go`). Project routes derive the project (and environment) from the path: `{pid}`, `{eid}` → environment, `{sid}` → service (+ `{eid}` must be in the same project), `{did}` deployment, `{rid}` cron run, `{vid}` volume, `{bid}` backup, `/db-backups/{id}`. A table-driven test walks every registered route.

**API tokens.** `keel_<40 base32 chars>`, shown once; only SHA-256 stored.
```ts
type ApiToken = { id, user_id, name, prefix /* "keel_" + 8 chars */, scope: "instance"|"workspace"|"project"|"environment",
  workspace_id: ID|null, project_id: ID|null, environment_id: ID|null, role: "admin"|"member"|"viewer", expires_at: string|null, last_used_at: string|null, created_at };
```
- Bearer auth checks `AVEN_API_TOKEN` first (legacy, owner, unchanged), then `api_tokens`. Effective permission on every request = min(creator's current permission, token role), restricted to the scope: project tokens see only their project; environment tokens see only their project, are read-only on project-level resources (project, service definitions, members, project events) and can act (max `member`) only on resources of their environment; other environments are 404. Workspace-level routes need a `workspace` token.
- Workspace tokens bind to exactly one workspace. Project/environment tokens also pin that workspace; moving a project revokes them. Instance tokens authorize instance operations only and cannot read tenant projects.
- Scoped tokens cannot mutate accounts, sessions, 2FA, tokens, users, invites or members (`403`). Instance tokens may read the instance user directory.
- `GET /tokens` (own; `?all=1` for instance admins), `POST /tokens {name, scope?, workspace_id?, project_id?, environment_id?, role? = "member", expires_in_days?: 1..3650|null}` → `201 ApiToken & {token}` (role above the creator's local role → `403`; environment tokens: `member`/`viewer` only; `scope: "instance"` needs instance admin and no resource IDs; max 100 per user), `DELETE /tokens/{tid}` (own, or any for instance admins) → `204`.

**Account.**
- `GET /auth/me` → `{user /* display name, v1-compatible ("admin" for the built-in account) */, id, email, name, role, totp_enabled, builtin, auth: "session"|"token"|"env_token", token: {id,name,scope,workspace_id,project_id,environment_id,role}|null, instance_role, instance_admin, workspaces, workspace_admin /* default workspace only */, can_create_projects, projects: {[pid]: "admin"|"member"|"viewer"}}`.
- `PATCH /auth/me {name?, current_password?, new_password?}` → `User` (password change needs `current_password`; `400` for the built-in account).
- `GET /auth/sessions` → `[{id, user_id, created_at, expires_at, last_seen, ip, user_agent, current}]`; `DELETE /auth/sessions/{sesid}` → `204`.
- 2FA (RFC 6238 TOTP, HMAC-SHA1, 30 s, 6 digits, ±1 step; a step can be used once per user):
  - `POST /auth/2fa/setup` → `{secret /* base32 */, otpauth_url}` (`409` if enabled). The secret is stored encrypted with the store secret box.
  - `POST /auth/2fa/enable {code}` → `{recovery_codes: string[10]}` (one-time, stored hashed; shown once).
  - `POST /auth/2fa/disable {code | password}` → `204` (code may be a recovery code).
  - `POST /auth/2fa/recovery-codes {code}` → new `{recovery_codes}` (TOTP code required).

**Users** (instance admin): `GET /users` → `User[]`, `GET /users/{uid}`, `PATCH /users/{uid} {name?, role?, disabled?, password?, reset_2fa?}` → `User`, `DELETE /users/{uid}` → `204` (not the built-in account, not yourself).
```ts
type User = { id, email, name, role: "owner"|"admin"|"member"|"viewer", totp_enabled, builtin, disabled, created_at, last_login_at: string|null };
```

**Invites** (no email is sent; the UI shows the link):
- `POST /invites {email, role? = "member", project_id?, project_role? = "member"}` → `201 {invite, token, url}` where `url` is `<AVEN_PUBLIC_URL or request origin>/invite/#token=<token>` (fragment, so it never reaches server logs). 7-day expiry, one use, only the hash is stored. Instance admins may invite an instance role up to their own (owner only by owners). Project admins may invite instance member/viewer accounts into their project, with workspace viewer membership. `/workspaces/{wsid}/invites` grants the requested workspace role separately and always creates an instance member account. Existing email → `409`.
- `GET /invites` (pending), `DELETE /invites/{iid}` (instance admin).
- `POST /auth/accept-invite {token, name, password}` (no auth; same-origin; IP rate-limited) → `201 User` + session cookie. Invalid/expired/used token → `400`; email already registered → `409` (invite kept).

**Project members**: `GET /projects/{pid}/members` (viewer+) → `[{project_id, user_id, role, created_at, email, name, workspace_role}]`; `PUT /projects/{pid}/members/{uid} {role}` → member; `DELETE /projects/{pid}/members/{uid}` → `204` (project admin; not with scoped tokens). Emit project events `member.added` / `member.removed`.

**Audit** (`GET /audit?limit=100`, instance admin, max 1000; kept 1 year) → `[{id, actor_id, actor, kind, target, message, ip, created_at}]`. Kinds: `auth.login`, `user.created`, `user.updated` (profile, password, 2FA on/off, role, disable, reset), `user.deleted`, `invite.created`, `invite.revoked`, `token.created`, `token.revoked`, `member.added`, `member.removed`.

**Event actor.** `Event` gains `actor: string | null` — the signed-in user's display name (`"<name> (토큰 <token name>)"` for API tokens, `"admin (API token)"` for `AVEN_API_TOKEN`) for events emitted while handling an API request (variables, settings, domains, exec, database queries, rollback, …); `null` for system events (webhooks, schedulers, crash detection). New event `deploy.requested` ("`<service> (<env>)`: `<dep>` 배포를 요청했습니다 (`<trigger>`)") records who triggered `POST …/deploy` and `POST /deployments/{did}/redeploy`. The `service.exec` / `database.query` messages now name the user instead of `admin(세션|API token)`.

**Migrations.** `migrateUsers` adds tables `users`, `sessions`, `project_members`, `invites`, `api_tokens`, `recovery_codes`, `audit_log`, column `events.actor`, and the built-in owner row.

### Notifications, metric alerts and usage

**Event kinds.** Everything the event bus emits can be delivered to channels and inboxes: `project.created`, `environment.{created,deleted,synced,preview_created,preview_deleted}`, `service.{created,updated,deleted,restarted,stopped,offline,sleeping,woke,exec}`, `variables.updated`, `domain.added`, `tcp_proxy.enabled`, `deploykey.created`, `template.deployed`, `stack.{applied,deleted,redeploy}`, `member.{added,removed}`, `deploy.{requested,waiting,skipped,started,live,failed,crashed,rollback}`, `cron.{started,succeeded,failed}`, `volume.{created,near_limit,limit_exceeded}`, `backup.{done,failed,restored,restore_failed}`, `volume.backup.failed`, `dbbackup.{done,failed,restored,restore_failed}`, `database.query`, `alert.{firing,resolved}`, `usage.threshold_reached` (and `notification.test` for test sends). `GET /auth/notification-preferences` returns this list as `available`. Subscription patterns are exact kinds or globs where `*` matches any characters including dots (`deploy.*`, `*.failed`, `*`); allowed characters `[a-z0-9_.*]`, ≤ 100 patterns.

`Event` gains `deployment_id: ID | null` (set for `deploy.started/live/failed/crashed`). Column `events.deployment_id`.

**Channels.**
```ts
type NotificationChannel = { id, project_id: ID | null /* null = workspace-wide */, kind: "webhook"|"slack"|"discord"|"email", name,
  url: string | null /* "•••" below project member */, secret_set: boolean, recipients: string[], events: string[], enabled: boolean, created_at };
type Delivery = { id, channel_id, event_id, event_kind, status: "ok"|"failed", attempts, status_code: number|null, error, duration_ms, created_at };
```
- Project: `GET /projects/{pid}/notification-channels` (viewer+), `POST` (project admin) `{kind, name, url?, secret?, recipients?, events? = ["deploy.failed","deploy.crashed","alert.*","volume.limit_exceeded","usage.threshold_reached"], enabled? = true}` → `201`, `PATCH /projects/{pid}/notification-channels/{cid}` (same fields; `kind` is fixed; `secret: ""` removes the signing key) → `200`, `DELETE …/{cid}` → `204`, `POST …/{cid}/test` → `{ok, attempts, status_code, duration_ms, error}` (sends `notification.test` synchronously), `GET …/{cid}/deliveries` (member+) → last 100 `Delivery`.
- Instance (instance admin): the same under `/notification-channels` and `/notification-channels/{cid}`. Instance channels receive the events of every project. A project channel is `404` through the workspace routes and vice versa.
- Config (`url`, `secret`, `recipients`) is stored as one JSON blob encrypted with the store secret box; `secret` is never returned. Max 50 channels per project / workspace; email channels need 1–20 plain addresses; HTTP channels need an `http(s)` URL without userinfo (≤ 2000 chars).
- Bodies: `webhook` → `{id, kind, message, project: {id, name}, environment: {id, name}|null, service: {id, name}|null, deployment_id, created_at, actor}`; `slack` → `{text}`; `discord` → `{content}` (≤ 1900 chars). Headers `X-Keel-Event`, `X-Keel-Delivery` (event id), and when a secret is set `X-Keel-Signature: sha256=<hex HMAC-SHA256(secret, raw body)>` (verify with a constant-time compare; the body contains `id`/`created_at` for replay checks).
- Delivery: 10 s timeout per attempt, up to 3 attempts (backoff 2 s, 8 s) on network errors, `5xx` and `429`; other `4xx` and blocked destinations are not retried. Redirects are not followed (a `3xx` is a failure). Every channel delivery (including tests) is logged; the log keeps the last 100 per channel.
- **SSRF protection.** Destinations are resolved at send time; if any resolved address is loopback, private (RFC 1918, ULA), link-local (incl. `169.254.169.254`), CGNAT, unspecified, multicast, documentation/benchmark ranges, NAT64/6to4 or IPv4-mapped forms of these, the send is refused. The dialer also checks the socket address actually connected (`net.Dialer.Control`), so DNS rebinding between the check and the connect is blocked; environment HTTP proxies are not used. Literal private IPs and `localhost` are rejected already at create/patch time (`400`). `AVEN_NOTIFY_ALLOW_PRIVATE=1` disables these checks (e.g. for an internal n8n).
- **Legacy project webhook.** `notify_webhook_url` / `notify_events` on projects stay readable and writable with the same values and semantics. They are not migrated into a row; the notifier treats them as an implicit channel (Discord+Slack body `{content, text}`, legacy kind names such as `deploy.success` for `deploy.live`), now with the same retries and SSRF protection (**behaviour change:** a legacy webhook pointing to a private address needs `AVEN_NOTIFY_ALLOW_PRIVATE=1`). It has no delivery log.

**Email (SMTP).** Configured from env: `AVEN_SMTP_HOST` (enables mail), `AVEN_SMTP_PORT` (default 587 / 465 / 25 by TLS mode), `AVEN_SMTP_USERNAME`, `AVEN_SMTP_PASSWORD` (AUTH PLAIN, only over TLS or to localhost), `AVEN_SMTP_FROM` (required), `AVEN_SMTP_TLS` = `starttls` (default; refuses to send if the server lacks STARTTLS) | `tls` (implicit TLS) | `none`. Messages are plain text UTF-8 (base64 body, RFC 2047 subject) in Korean. `GET /system` adds `smtp_configured` and `pricing`.
- `POST /system/smtp/test {to?}` (instance admin; default `to` = your email) → `200 {ok, to}` | `503` not configured | `502 {error}`.
- `POST /invites` accepts `send_email: true`; the response adds `email_sent: boolean` and `email_error?` (the invite is created either way). The link uses `AVEN_PUBLIC_URL` when set.

**In-app notifications.** Each event is copied into the inbox of every active user who can see the project (workspace owner/admin, or a project member) and whose preferences match the kind (last 200 per user).
- `GET /auth/notification-preferences` → `{events, defaults, available}`; `PUT /auth/notification-preferences {events: string[]}` → same. Default `["deploy.failed","deploy.crashed","volume.limit_exceeded","alert.*"]`. (Session or `AVEN_API_TOKEN`; scoped tokens `403`.)
- `GET /notifications?unread=1&limit=50` → `{notifications: [{id, event_id, project_id, environment_id, service_id, kind, message, created_at, read_at}], unread}` (items of projects you can no longer see are omitted; `unread` counts all).
- `POST /notifications/read {ids: string[]} | {all: true}` → `{updated}`.

**Metric alert rules.**
```ts
type AlertRule = { id, project_id, environment_id: ID|null, service_id: ID|null, name, metric, comparator: ">"|"<", threshold: number,
  for_seconds: number /* 0..86400 */, enabled, last_state: "ok"|"pending"|"firing", last_value: number|null,
  pending_since, last_fired_at, last_evaluated_at: string|null, created_at };
```
- `GET /projects/{pid}/alert-rules` (viewer+), `POST` (project admin; `metric` and `threshold` required, `comparator` default `>`), `PATCH /projects/{pid}/alert-rules/{arid}` (`environment_id`/`service_id` may be `null` to widen the scope; any change resets the state to `ok`), `DELETE …/{arid}` → `204`. Max 100 per project.
- Metrics: `cpu_percent` (sum over replicas, 100 = one core), `memory_percent` (of the memory limit), `memory_bytes`, `disk_bytes` (attached volumes), `restarts` (sum of container restart counts of the current deployment), `http_5xx_rate` (% of requests in the last 5 min), `http_p95_ms` (nearest-rank p95 of Caddy access log `duration` over the last 5 min), `volume_percent` (largest attached volume vs its `size_limit_mb`).
- Scope: service+environment = one instance; service only = all its environments; environment only = all services there; neither = the whole project. With several instances the worst value is used (max for `>`, min for `<`).
- Evaluated every 30 s. `ok → pending` on the first breach; `pending → firing` after `for_seconds` of continuous breach (immediately when 0) and emits `alert.firing`; a non-breaching value returns to `ok` (from `firing` emits `alert.resolved`). Missing data (no sample in the last 2 minutes, no HTTP requests) leaves the state unchanged. Messages: `알림 발생: <name or condition> — <service> (<env>) (현재 <value>, 기준 <condition>)` / `알림 해제: …`.

**Usage.** Hourly rollups (`usage_hourly`, kept 400 days) are computed from the metric samples (kept 7 days) per service × environment: `cpu_core_seconds` (Σ cpu%/100 × sample duration), `memory_gb_seconds` (Σ bytes/10⁹ × duration), `net_egress_bytes` (increase of the containers' transmit counter; a counter drop counts the new value; includes private-network traffic), `disk_gb_hours` (volume bytes/10⁹ × hours). The job runs at start and at ~5 minutes past each hour, rolls every finished hour since the last run (watermark in `settings.usage_rollup_until`), so missed hours are backfilled while samples exist; re-rolling an hour overwrites it (idempotent).
- `GET /projects/{pid}/usage?from=&to=&group=service|environment|day` (viewer+) → `{project_id, from, to, group, totals, groups: [{key, name, ...Totals}], series: [{t: "YYYY-MM-DD", ...Totals}], pricing, usage_alert}`. `from`/`to`: RFC3339 or `YYYY-MM-DD` (UTC; a date `to` means the end of that day); default this calendar month; whole hours; max 400 days.
- `GET /usage?from=&to=` (instance admin) → `{from, to, group: "project", totals, projects: Group[], series, pricing}`.
- `Totals = {cpu_core_hours, memory_gb_hours, net_egress_gb, disk_gb_hours, estimated_cost?: {cpu, memory, egress, disk, total}}`. `estimated_cost` appears when any of `AVEN_PRICE_VCPU_HOUR`, `AVEN_PRICE_GB_RAM_HOUR`, `AVEN_PRICE_GB_EGRESS`, `AVEN_PRICE_GB_DISK_MONTH` (non-negative floats; disk month = 730 h) is set.
- Project field `usage_alert: {cpu_core_hours?, memory_gb_hours?, egress_gb?} | null` (`PATCH /projects/{pid}`, project admin; `null` or `{}` clears). After each rollup the month-to-date (UTC calendar month) totals are compared; `usage.threshold_reached` is emitted once per metric per month at 80% and once at 100%.

**Migration** `migrateObservability`: tables `notification_channels`, `notification_deliveries`, `user_notifications`, `alert_rules`, `usage_hourly`; columns `users.notification_events`, `events.deployment_id`, `projects.usage_alert`, `projects.usage_alert_state`.

**Web.** Bell with unread count in the shell (polls `/notifications` every 30 s, dropdown with mark-as-read), alert rules section on the Observability page, `/usage` page, usage alert thresholds and notification channels in project settings. CLI: `aven usage` (see `docs/cli.md`).

### Templates

Built-in templates are now JSON definitions embedded in the server (`internal/catalog/templates/*.json`) and use the same engine and format as user templates. Deploying a built-in creates the same services, settings, variables, volumes and generated domains as before.

#### Definition format (`"version": 1`)

```json
{
  "version": 1,
  "key": "acme-stack",                 // optional slug; [a-z0-9-], max 64
  "name": "Acme stack", "description": "…", "icon": "node", "category": "starter", "tags": ["node"],
  "readme": "# Markdown…",             // max 64KB
  "primary": "api",                     // optional; default: first service with a domain, else the first
  "inputs": [
    {"key": "ADMIN_EMAIL", "label": "Admin email", "description": "", "default": "",
     "required": true, "secret": false, "pattern": "[^@\\s]+@[^@\\s]+"}
  ],
  "services": [
    {"name": "db", "source": {"database": "postgres"}},
    {"name": "api", "icon": "node",
     "source": {"image": "ghcr.io/acme/api:1"},   // or {"repo": {"url", "branch", "root_dir"}} or {"database": kind}
     "variables": {
       "DATABASE_URL": "${{db.DATABASE_URL}}",
       "ADMIN_EMAIL": "${{ inputs.ADMIN_EMAIL }}",
       "SESSION_SECRET": "${{ secret(32) }}"
     },
     "volumes": [{"mount_path": "/data", "size_limit_mb": 1024}],
     "domains": [{"generate": true, "target_port": 3000}],
     "tcp_proxy": {"container_port": 9229},
     "canvas": {"x": 0, "y": 0},
     "port": 3000, "replicas": 2, "healthcheck_path": "/health", "cron_schedule": "*/5 * * * *"}
  ]
}
```

- **Instance settings** sit flat on the service and use the instance `PATCH` names: `port`, `start_command`, `pre_deploy_command`, `builder`, `dockerfile_path`, `watch_paths`, `auto_deploy`, `healthcheck_type`, `healthcheck_path`, `healthcheck_timeout_s`, `restart_policy`, `restart_max_retries`, `replicas`, `cron_schedule`, `sleep_when_idle`, `sleep_after_s`, `memory_limit_mb`, `cpu_limit`, `build_secrets`, `wait_for_ci`, `overlap_seconds`, `draining_seconds` (same ranges as `PATCH`). The source (image/repo) is set with `source`, not with `image`/`repo_url`.
- **Database services** (`source.database`: postgres, mysql, redis, mongo) get the usual image, port, data volume and connection variables; a new password is generated per environment. `variables`, settings and `volumes` (for example a `size_limit_mb` on the data path) are applied on top.
- **Expressions** (evaluated once per deploy, never written back into the template):
  - `${{ secret(n) }}`: `n` (1..512) random characters from `abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789`; `${{ secret(n, "abcdef0123") }}`: from the given charset (2..256 unique printable ASCII). Uniform (rejection sampling, crypto/rand).
  - `${{ randomInt(min, max) }}`: inclusive bounds, 64-bit.
  - `${{ uuid() }}`: random UUID v4.
  - `${{ inputs.KEY }}`: the deploy input (or its `default`; a default may itself use the functions above). Inputs and functions are also allowed in `source.image`, `source.repo.url/branch`, `start_command` and `pre_deploy_command`; variable references only in `variables`.
  - Other `${{ … }}` are normal variable references (`${{VAR}}`, `${{svc.VAR}}`, `${{shared.VAR}}`), resolved at runtime as usual.
- **Validation** is strict: unknown fields, trailing data, wrong types, `version` ≠ 1, more than 256KB, more than 20 services / 50 inputs / 200 variables per service are errors. Service names follow the service name rules (not `shared`/`inputs`, unique). Every reference must point to a service of the template and a variable it defines (database default variables and injected `AVEN_*`/`PORT` count; `shared.*` is not checked); reference cycles are rejected. Only generated domains are allowed. Errors are `400 {"error", "problems": [..]}`.

#### Endpoints

- `GET /templates?q=&category=&tag=&sort=popular|new` → `Template[]`: built-ins plus user templates visible to the caller. `Template` gained `category`, `tags`, `inputs`, `builtin`, `visibility` (built-ins: `public`), `owner_user_id`, `deploy_count`, `created_at`/`updated_at` (null for built-ins), `can_edit`; `services[]` gained `icon` (and `repo_url` for repo sources). Default order: built-ins by key, then user templates by name. `popular` sorts by deploy count, `new` by creation (built-ins last).
- `GET /templates/{key}` → `Template` + `readme`, `definition` (JSON).
- `GET /templates/{key}/export` → the definition as a JSON download (`Content-Disposition: attachment; filename="<key>.json"`).
- `POST /templates` (target workspace member+) `{workspace_id?, definition, key?, name?, description?, readme?, icon?, category?, tags?, visibility?}` → `201 Template`. Metadata fields override the definition's and are written into the stored definition. `key` defaults to the definition's key, then to a slug of the name; built-in keys and existing keys → `409`. `visibility`: `private` (default; owner and workspace admins), `workspace` (members of its workspace), `public` (all signed-in users).
- `PATCH /templates/{key}` (owner or workspace admin) same fields; `key` cannot change. A new `definition` keeps the current metadata where it has none. `DELETE /templates/{key}` → `204`. Built-ins → `400`; others' private templates → `404`; others' visible templates → `403`.
- `POST /projects/{pid}/templates/{key}` (project member) `{environment_id?, inputs?: {KEY: value}}` → `201 Service[]` (primary first). New: user templates, `inputs`. Input problems → `400 {"error", "missing": [..], "invalid": [..], "unknown": [..]}` (pattern is a full match). Invalid settings → `400` before anything is created. Name collisions are renamed (`db-2`) and references rewritten, as before. Each deploy increments `deploy_count`.
- `POST /templates/{key}/deploy` `{project_id, environment_id?, inputs?}`: same, for callers that start from the gallery. Needs member on the target project/environment (`404` if not visible, `403` if viewer).
- `POST /projects/{pid}/environments/{eid}/export-template?secrets=generate|inputs` (project member; it reads variables) → `200 {definition, warnings: [..]}`. Nothing is saved; review and `POST /templates`.
  - Exported: services with a source (database kind, repo url/branch/root_dir, image), settings that differ from what the source gives by default, variables (references kept), volumes with `size_limit_mb` (a database's own data volume only when it has a limit), generated domains as `{generate: true, target_port}`, `tcp_proxy.container_port`, canvas positions.
  - Never exported: custom (and wildcard) domains, the TCP proxy public port, deploy keys, registry credentials, node placement, domain auth/paths, compose stack services, database credential variables (regenerated by the database source), database variables equal to their defaults.
  - Secret-looking values — the key matches `/(SECRET|PASSWORD|TOKEN|KEY|PRIVATE)/i` and the value has no reference, or the value is a URL with a password (`scheme://user:pass@`) — become `${{ secret(32) }}` (`secrets=generate`, default) or a required secret input `<SERVICE>_<KEY>` referenced as `${{ inputs.<SERVICE>_<KEY> }}` (`secrets=inputs`; no default). Variables that reference a service outside the export are dropped with a warning.
- RBAC: list/get/export are open to any signed-in user (the handler filters by visibility); create/patch/delete/deploy-by-key need workspace member+ (ownership and target-project checks in the handler); the project routes keep `member`.
- CLI: `aven templates [list|show|deploy|publish|export|export-env]` (see `docs/cli.md`).

### Cross-node private networking (mesh)

Supersedes the v1.2 "cross-node private networking is not provided" deviation for nodes that have a private address. Inside an environment that runs on more than one node, every service is reachable from every node of that environment at its usual private names (`<svc>` and `<svc>.<env>.aven.internal`). Nodes without a usable private address keep the v1.2 behaviour (see "Limits").

**How it works**

- **Mesh gateway.** Each node runs a gateway inside the aven process (aven-agent on remote nodes, aven-server on the local node; both run on the host, not in a container). It listens on **one port on the node's private address only**: `--private-address`/`AVEN_PRIVATE_ADDRESS` `:` `--mesh-port`/`AVEN_MESH_PORT` (default `7443`). The address must be a private IP (RFC1918, ULA `fc00::/7` or CGNAT `100.64.0.0/10`, which covers Tailscale). This is the single exception to "agents publish no ports". The agent picks the listen address from its own settings; the server cannot change it.
- **mTLS with node certificates.** Connections use TLS 1.3 with client certificates and ALPN `keel-mesh/1`. Both sides present the keel-CA node certificate (the same one used for the agent connection). Each side checks that the peer certificate is (a) signed by the aven CA, valid, and bound to a node ID (URI SAN `keel-node://<id>` = CN, `clientAuth` EKU), (b) from a node in the server-pushed peer list, and (c) has a serial in that node's list of active certificates. Revoked or deleted nodes drop out of the list on the next sync; node deletion syncs right away. The dialing side also checks that the peer is the node it meant to reach. New and renewed node certificates also carry `serverAuth`. Older certificates (`clientAuth` only) are accepted, because the mesh checks `clientAuth` as the node-identity usage in both directions. The local node gets an in-memory key and keel-CA certificate (`node_local`) at startup, renewed before it expires. It is never written to disk.
- **Request header.** After the handshake the client sends one JSON line (≤ 1 KiB, unknown fields rejected): `{"v":1,"op":"dial","env":"env_…","service":"<name>","port":N}` or `{"v":1,"op":"ping"}`. The gateway answers `{"ok":true,"node":"…"}` and then relays bytes, or answers `{"ok":false,"error":"…"}`. A dial is allowed only if the service is exported by this node for that environment, the requesting node hosts part of the same environment, and `port` is one of the service's private ports. The gateway then connects to a running `role=app` container of that service (found by `keel.environment`/`keel.service` labels, IP on the `keel-<env>` network, IPv4 first, cached for 5 s). A host process can reach bridge container IPs directly.
- **Consumer side.** For each service of the environment that runs on another node, the node runs a forwarder container `keel-mesh-<env>-<svc>` (label `keel.role=meshproxy`, image `alpine/socat:1.8.0.3`, **no published ports, no volumes, no extra capabilities**). It joins `keel-<env>` with the aliases `<svc>` and `<svc>.<env>.aven.internal` and listens on the service's private ports. Each port forwards to a local listener that the gateway opens on that network's **bridge gateway IP** (host side), one listener per (environment, service, port). The listener only accepts connections whose source address is inside that environment network's subnets. It forwards only to that one (environment, service, port) on the owning node. The agent builds the forwarder spec itself, and the server cannot send an image, command or ports beyond validated numbers. Listener ports are reused across restarts (read back from the forwarder's `keel.mesh` label), so forwarders are not recreated needlessly.
- **Desired state** is computed from placements (the node of the instance's current deployment). For every environment with deployed, non-stopped, non-cron services on two or more mesh-capable nodes, each such node exports its services of that environment to the other nodes of that environment and imports the rest. All mesh nodes are peers of each other for health checks. Sync runs on the 30 s reconcile loop, when a node connects, after a deployment goes live, when `port`/`private_ports` change, before an environment network is removed, and when a node is deleted. Agent op: **`mesh.ensure`** (payload: `{peers:[{node_id,address,serials}], exports:[{env_id,service_id,service,ports,peers}], imports:[{env_id,env_name,service,ports,node}]}`; strict decoding; peer addresses must be private IP:port ≥ 1024; ≤ 16 ports per service; ≤ 256 peers). Result: the mesh status below. Agents without this op, without `--private-address`, with `--mesh=off` or with only a legacy bearer secret report `mesh_port` 0 in ping and are left out.
- **Health.** Every gateway pings every peer over mTLS every 30 s (and right after a config change) and records `status`, `last_ok_at` and `rtt_ms`.

**Threat model (who can reach what)**

- The mesh port answers only TLS clients with a keel-CA node certificate whose node is a current peer with an active serial. Anything else fails the handshake. This includes the internet (if the private address is somehow reachable), other Tailscale devices, other CAs, and revoked or deleted nodes.
- An enrolled node can reach only services of environments it hosts itself, and only their private ports. It cannot use the gateway to reach other addresses on the remote host or network: the target is always a container IP of that environment's service.
- On a node, containers of environment A cannot use the forwarders of environment B. B's forwarder is on B's network only (docker DNS answers per network), and B's host-side listeners reject sources outside B's subnets. Docker containers can reach the host's bridge IPs of other networks, so the source-subnet check is the barrier. Spoofing another subnet's source address cannot complete a TCP handshake through the bridge.
- A compromised node is trusted for the environments it hosts, the same as the same-node private network today. Traffic between nodes is encrypted by TLS even when the private network itself is not.
- Container IPs on the host are reachable by any host process; this was already true and is not changed.

**API**

- `GET /api/nodes`: every node gains `mesh: {enabled, listen: string|null, peers: [{node_id, status: "ok"|"unreachable"|"pending", last_ok_at, rtt_ms, error?}], error?, reason?}`. When `enabled` is false, `reason` explains why (no private address, not a private IP, agent without mesh, `AVEN_MESH=off`, no node certificate). Remote node status is the result of the last `mesh.ensure` (at most about 30 s old). The local node status is live.
- `GET /api/system` gains `mesh: {mode: "on"|"off"|"auto", port, private_address, enabled}` for the local node.
- Instance setting **`private_ports: number[]`** (`PATCH /services/{sid}/instances/{eid}`; ≤ 16 distinct ports 1..65535; absent or `[]` = `[PORT]`, i.e. `port`, or the port the live deployment uses). These are the ports exported to other nodes. Within one node every port stays reachable as before. It applies immediately, without a redeploy, and is not listed in `pending_changes`.
- `GET …/variables/resolved`: a variable that references, directly or through a shared variable, a service whose instance runs on another node gets `cross_node: true` (informational), `mesh: true|false` (whether both nodes are on the mesh, i.e. the private name resolves) and `cross_node_services: [..]`. Other variables are unchanged. Deploy logs list mesh-connected cross-node references, and warn about references that the mesh cannot reach.

**Settings**

- Server: `AVEN_PRIVATE_ADDRESS` (private IP of the local node; shown as the local node's `address.private`, falling back to the hostname as before), `AVEN_MESH=on|off|auto` (default `auto`: on when `AVEN_PRIVATE_ADDRESS` is a private IP; `on` without one logs a warning and stays off), `AVEN_MESH_PORT` (default 7443).
- Agent: `--private-address`/`AVEN_PRIVATE_ADDRESS`, `--mesh`/`AVEN_MESH` (`auto` default), `--mesh-port`/`AVEN_MESH_PORT`. Tailscale with `--tun=userspace-networking` (e.g. an unprivileged LXC container) has no interface for the private address: set `--mesh-listen`/`AVEN_MESH_LISTEN=127.0.0.1` (tailscaled forwards incoming tailnet connections to loopback; the node still advertises `--private-address`) and `--mesh-proxy`/`AVEN_MESH_PROXY=127.0.0.1:1055` (tailscaled's `--outbound-http-proxy-listen`; peers are dialed with HTTP `CONNECT` and mTLS runs inside the tunnel). Both accept only loopback or private addresses. With the install script: `--env AVEN_PRIVATE_ADDRESS=100.x.y.z`. If the private interface comes up after the agent starts, the gateway retries binding every 10 s.
- Firewalls: allow TCP `AVEN_MESH_PORT` between nodes on the private network. On each node, allow traffic from docker bridge subnets to the host's bridge gateway IPs (the forwarders' host-side listeners use ephemeral ports). Hosts with a default-deny `INPUT` policy (for example ufw) need a rule such as `ufw allow in on br-+`, or an equivalent for the `keel-env_*` bridges.

**Limits**

- A node without a private IP address (or with mesh off, an old agent, or a legacy bearer-only identity) is not on the mesh. References from or to services on it keep the v1.2 behaviour: they resolve to the private name, which does not resolve on the other node. The deploy log warns and `variables/resolved` shows `mesh: false`. The deploy does not fail (it never did in practice).
- Only the two standard aliases are provided across nodes. Compose `networks.*.aliases`/`links` aliases resolve only on the service's own node.
- TCP only (no UDP). Forwarders listen on IPv4 inside the environment network (like the TCP proxy forwarder), so clients that only try the AAAA record of a cross-node alias need an IPv4 fallback.
- An agent that was upgraded from a legacy bearer secret starts its gateway after its next restart.

### Autoscaling and multi-node replicas

Horizontal scaling (replica changes apply live) plus "multi-region" replicas across aven nodes.

**Instance settings** (`PATCH /services/{sid}/instances/{eid}`)
- **`autoscale`**: `{enabled, min_replicas (1..10, default 1), max_replicas (min_replicas..10), target_cpu_percent?: number|null (0 < x ≤ 1000), target_memory_percent?: number|null (0 < x ≤ 100), target_rps_per_replica?: number|null (> 0), scale_up_cooldown_s (default 60), scale_down_cooldown_s (default 300)}` or `null`. Enabled needs at least one target. It applies immediately and is never listed in `pending_changes`. The object is replaced as a whole (send every field you want to keep).
- **`placement`**: `[{node_id, replicas (1..10)}]` or `null`/`[]`. It spreads replicas over several nodes. The first entry is the primary node, and `node_id` is set to it. `replicas` becomes the sum (at most 10). Node IDs must exist and be distinct. Sending `replicas` alone while a placement is set, or a `node_id` other than the first entry, returns `400`. With more than one node, these are refused with `400`: attached volumes (and attaching a volume to such an instance returns `409`), `cron_schedule`, `sleep_when_idle`, and `autoscale.min_replicas` below the number of nodes. `placement` is a pending change: it applies on the next deploy. A single-entry placement is the same as `node_id` + `replicas`.
- `autoscale` and `placement` are omitted from instance responses (and deployment snapshots) while unset, so existing clients and templates see the same shapes as before. Treat a missing key as `null`.
- `pending_changes` now compares the keys present on either side, so clearing an optional setting (for example `placement` or `memory_limit_mb`) is also listed until the next deploy.
- Instance view additions: `current_replicas` (replicas running now across all nodes; autoscaling and `/scale` change it, while `replicas` stays the user's baseline) and `autoscale_status` (`null` unless autoscaling is enabled): `{desired: number|null, last_scale_at, last_reason (why the last change happened), reason (outcome of the latest evaluation, including skip reasons), evaluated_at, metrics: {cpu, memory, rps}}`. The metrics are per replica.
- Deployment `replicas[]` entries gain `node_id`.

**`POST /services/{sid}/instances/{eid}/scale`** `{replicas: 1..10}` (member+)
- Changes the running replica count of the live deployment **without rebuilding or redeploying**. New replicas use the same image and environment. They are started (named after the next free replica number), healthchecked with the instance's healthcheck, and only then added to the proxy upstreams. If a new replica fails its healthcheck, only the new containers are removed. To scale down, replicas are removed from the proxy first, then (after about 2 s) stopped with `draining_seconds` (default 10 s) and removed. The request waits for the healthchecks.
- The instance's `replicas` (and, with a placement, the per-node counts in the new proportion) and the current deployment's snapshot are updated, so nothing shows up in `pending_changes`.
- With a placement, `n` is distributed proportionally to the per-node counts, with at least 1 per node (`400` if `n` is lower than the node count).
- `200 {from, to, nodes: [{node_id, replicas}], instance}`. `409` while autoscaling is enabled, when nothing is live (never deployed, stopped, sleeping, cron, crashed), or while a deployment is queued or in progress. `500` when a new replica fails to start or pass its healthcheck (nothing changes).
- Event **`service.scaled`**: message `"<svc> (<env>): replica <from> → <to> (<reason>)"`. The reason is `수동` or the autoscaler's metric summary. A line is also appended to the live deployment's build log.

**Autoscaler** (`aven-server`, every 30 s)
- Kubernetes HPA formula: `desired = ceil(current × max(metric / target))` over the configured targets. Each metric is the average of the last 2 minutes. CPU is summed container CPU ÷ replicas, in percent of one core, or of `cpu_limit` when set. Memory is used ÷ limit (the host memory when no limit is set). RPS is requests in the last 2 minutes from the HTTP access log hub of every node, ÷ 120 ÷ current replicas. Request counting is separate from the 500-entry log ring, so high request rates are counted fully.
- A ratio within 1 ± 0.1 means no change. The result is clamped to `[min, max]`. If the current count is outside the range, it is corrected right away. Scale-up waits `scale_up_cooldown_s` after the last change. Scale-down waits `scale_down_cooldown_s` after the last change, and only goes down to the highest recommendation of the last `scale_down_cooldown_s` (stabilization window).
- Skipped, with the reason in `autoscale_status.reason`, while: a deployment is queued or in progress; the instance is sleeping, stopped, crashed, cron or never deployed; the service has volumes (multiple writers); there are no metrics yet.
- With a placement, the desired total is split by the per-node weights, with at least one replica per node.
- A redeploy of an autoscaled instance starts with the current replica count (clamped to `[min, max]`) instead of the baseline. Autoscaled instances never sleep.
- History and status survive a server restart (see "Autoscaler state persistence" below).

**Multi-node deploys** (`placement` with 2+ nodes)
- **Images:** aven has no registry, so every placement node gets the image itself. Image and database services are pulled on each node. Repo services are built on the primary node first. That build applies config-as-code and resolves the commit. The other nodes then clone **the same commit SHA**, in parallel, and build it with the same builder and the same image tag (`keel/<svc>:<dep>`). The agent's fetch result must report the same SHA, otherwise the deploy fails. CLI uploads send the same tarball to each node. Trade-offs: builds take about two build times (primary, then the rest in parallel), and the images are reproducible only as far as the build is deterministic (unpinned base images or dependencies can differ between nodes).
- **Cutover:** all replicas on all nodes must pass the healthcheck before the deployment goes live. Any failure fails the deployment and removes the new containers on every node, and the previous deployment keeps serving. Pre-deploy commands run once, on the primary node.
- **Routing:** each node's keel-proxy routes the service's domains (and path rules) only to its local replicas. Redirect domains are installed on every node.
- **DNS:** the `dns` list of every domain (custom, and also generated ones when the instance serves from 2+ nodes) contains one `A`/`AAAA` record per node: the primary uses the server's public IPs, remote nodes the addresses reported by the agent. Clients get DNS round-robin "multi-region". Custom domain `status` is `active` when the host resolves to any of these addresses. There is no health-aware DNS: if a node goes down, clients that resolve to it fail until you remove its record.
- **TCP proxy:** only the primary node publishes the TCP proxy port.
- **Mesh:** each node with replicas exports them. A node that has its own replica of a service does not import it (the local replica answers the private domain alias). Other nodes import it from one node, the first in placement order.
- Logs, exec (`?replica=` accepts any replica), restart, stop, reconcile (recreating missing replicas) and crash detection work per node. Draining a node (`POST /nodes/{nid}/drain`) still moves only instances whose primary node is the drained node.

**Config-as-code**
- `deploy.multiRegionConfig` with **two or more regions** is now applied (it used to be ignored with a warning): each region is mapped to a node with the existing region rule (node name or `region=<value>` label; the deploy node if it matches, otherwise the first connected match). The `numReplicas` values (default 1, total ≤ 10) become that deployment's placement. Regions without a matching connected node are skipped with a warning. Two regions on the same node are merged. Ignored with a warning for services with volumes or a cron schedule. If the deploy node matches no region, it only builds and runs no replicas. The mapping is stored in the deployment snapshot only; the instance's `placement` setting is not changed. A single region keeps the v1.5 `deploy.region` behaviour.
- `autoscale` is not configurable from config-as-code .

**CLI**
- `aven scale` shows replicas, placement and autoscale status. `aven scale <n>` calls `/scale`. `aven scale --auto --max 5 [--min 1] [--cpu 70] [--memory 80] [--rps 50] [--up-cooldown 60] [--down-cooldown 300]` enables autoscaling. `aven scale --off` sets `autoscale: null`. All forms accept `-s/-e`.

**Limits**
- Not verified on real multi-host Docker. The orchestration (start → healthcheck → proxy, drain ordering, rollback on all nodes, per-node routes, mesh imports, DNS lists) is covered by tests with fake local and remote drivers. Actual cross-host traffic, round-robin DNS behaviour, and build reproducibility were not exercised.
- Scaling requires every affected node to be connected. A replica on a disconnected node is left alone, and reconcile fixes it when the node returns.

### Buckets (S3-compatible object storage)

A bucket is a database service with `database_kind: "bucket"`: `POST /projects/{pid}/services {"source_type":"database","database_kind":"bucket"}` (or the shorthand `{"source_type":"bucket"}`; the stored `source_type` is still `database`). Templates use `"source": {"database": "bucket"}`. CLI: `aven add --database bucket`.

**What gets created** (per environment): image `pgsty/minio:RELEASE.2026-08-04T00-00-00Z` (same pinned image as the `minio` template), port 9000, http healthcheck `/minio/health/live`, volume at `/data`, and a generated domain when `AVEN_BASE_DOMAIN` is set. Variables:

| Variable | Value |
|---|---|
| `BUCKET` | `bucket-<10 hex>` (generated) |
| `ACCESS_KEY_ID` | `KEEL<16 hex>` (generated) |
| `SECRET_ACCESS_KEY` | 32 random characters |
| `REGION` | `us-east-1` |
| `ENDPOINT` | `http://${{AVEN_PRIVATE_DOMAIN}}:9000` (private network) |
| `PUBLIC_ENDPOINT` | `https://${{AVEN_PUBLIC_DOMAIN}}`; only set when the service was created with a domain |
| `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` / `MINIO_SITE_REGION` | references to the three above (what MinIO uses) |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `AWS_DEFAULT_REGION`, `AWS_ENDPOINT_URL`, `AWS_S3_BUCKET_NAME` | aliases |

`BUCKET` and `ACCESS_KEY_ID` are derived from the secret with HMAC-SHA256, so every environment gets its own three values. They are treated like database passwords: environment sync and template export never copy them. Use path-style addressing (`forcePathStyle: true` in AWS SDKs).

**Default bucket.** The start command runs `minio server` and then creates `$BUCKET` with the `mc` binary that ships in the image (`mc mb --ignore-existing` against `127.0.0.1:9000` inside the container, credentials passed via `MC_HOST_*` in the environment, not argv). This runs on every start and works the same on local and remote nodes. The browser endpoints below also create the bucket if it is missing (S3 `CreateBucket`).

**Browser endpoints.** aven-server signs S3 requests (SigV4) with the running container's `MINIO_ROOT_*` credentials. On the local node it connects to the container IP on the environment network (port 9000, same path as healthchecks). On a remote node it uses the instance's public domain over https, because aven-server cannot reach container IPs on other nodes. A remote bucket without a domain returns 409. All return 409 when the bucket is not running and 400 for non-bucket services.

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/services/{sid}/instances/{eid}/bucket` | viewer | `{bucket, region, endpoint, public_endpoint, access_key_id, secret_access_key, masked, running, max_upload_bytes}`. For viewers both keys are `"•••"` and `masked` is true |
| GET | `/services/{sid}/instances/{eid}/bucket/objects?prefix=&cursor=&limit=` | viewer | One level under `prefix` (ListObjectsV2, delimiter `/`). `limit` 1..1000 (default 200). Response `{bucket, prefix, prefixes: ["dir/"], objects: [{key, size, last_modified, etag}], next_cursor}`; pass `next_cursor` back as `cursor` |
| GET | `/services/{sid}/instances/{eid}/bucket/objects/{key...}` | member | Streams the object as an attachment (`Content-Type: application/octet-stream`, `Content-Disposition: attachment`). 404 when missing |
| PUT | `/services/{sid}/instances/{eid}/bucket/objects` | member | `multipart/form-data`: optional `prefix` field (before the files), then one or more `file` parts (max 50). Each file goes to `prefix + filename`. Body limit 100 MiB (+1 MiB multipart overhead), otherwise 413. Streams to S3 in 8 MiB parts. → 201 `{uploaded: [{key, size}]}`. Event `bucket.upload` |
| DELETE | `/services/{sid}/instances/{eid}/bucket/objects/{key...}` | member | → 204 (also when the key does not exist). Event `bucket.delete` |

Object keys: valid UTF-8, at most 1024 bytes, no control characters, no leading `/`, no `.` or `..` segments (otherwise 400). In URLs, encode each segment and keep `/` as the separator.

### Sealed variables and raw editor

**Sealed variables.** A sealed variable's value is stored like any other variable but never returned by any API again. It is still injected into containers and builds at deploy time and can be referenced by other variables and services. Sealing cannot be undone: to unseal, delete the variable and create it again (a recreated key with a different value is a normal variable). Changing a sealed variable's value keeps it sealed. Seals are recorded per (owner, key, value hash) in the `sealed_variables` table (migration `migrateSealedVariables`), so a sealed value that comes back through rollback or sync stays sealed.

- Instance: `PATCH /services/{sid}/instances/{eid}` accepts `"sealed_variables": ["KEY", ...]` (member; max 500; every key must exist after the patch, otherwise 400). It can be combined with `variables`/`variables_patch`. Event `variables.updated`.
- Shared: `PUT /environments/{eid}/shared-variables` accepts `{"values": {...}, "sealed": ["KEY"]}` in addition to the old plain string map. `GET ...?detail=1` returns `{values, sealed}`; without `detail` the old map is returned.
- Instance views (`GET/PATCH` instance, canvas, rollback/move responses) include `sealed_variables: [names]`, and the value of each sealed key is `"<sealed>"` for every role. Sending `"<sealed>"` back as the value (full `variables` replace, `variables_patch`, shared PUT) keeps the stored value.
- `GET .../variables/resolved`: values that are sealed or derived from a sealed value (own, `${{ shared.X }}`, `${{ svc.X }}`) are `"<sealed>"` with `"sealed": true`. `aven run`/`aven shell` skip them.
- Never exposed: environment sync preview (sealed keys listed by name in `sealed_variables` / `sealed_shared_variables`, never synced or deleted), template export (`${{ secret(32) }}` or an input instead, with a warning), raw editor GET, bucket info/objects (`"<sealed>"` for fields derived from sealed values), CLI output. Environment duplication (`copy_from`) does not copy sealed variables.

**Raw editor.** Member access (viewers get 403).

| Method | Path | Notes |
|---|---|---|
| GET | `/services/{sid}/instances/{eid}/variables/raw?format=env\|json` | `{format, content, sealed_variables}`. `env` (default; `dotenv` accepted) is a sorted `.env` file; sealed variables appear only as `# KEY=<sealed>` comments. `json` is an object of the non-sealed variables. Other formats → 400 |
| PUT | `/services/{sid}/instances/{eid}/variables/raw` | Body `{format, content}`. Replaces all non-sealed variables with the parsed content (missing keys are deleted). Sealed variables not present in the content (or set to `"<sealed>"`) are kept; a sealed key with a new value is replaced and stays sealed. Validation and reference-cycle checks as for PATCH; parse errors → 400 `line N: ...`. Returns the instance view; changes are pending until the next deploy. Event `variables.updated` |

`.env` rules (server, CLI `--import` and web editor share them): blank lines and `#` comment lines are ignored; an optional `export ` prefix is stripped; `KEY=VALUE` with names matching `[A-Za-z_][A-Za-z0-9_.-]*`; unquoted values are trimmed and ` #` starts a comment; `'...'` and `` `...` `` are literal; `"..."` unescapes `\n \r \t \" \\ \$ \' \``; quoted values may span lines; the last duplicate wins; `${VAR}` is not interpolated so `${{ }}` references survive. Output quotes values so that parsing returns them unchanged.

### Remote backups, volume sizes and mesh hardening

**Logical database backups on remote nodes.** `POST .../db-backups`, scheduled backups and `POST /db-backups/{id}/restore` now work for databases on remote nodes, with the same API, storage (local or S3 destination), retention and events as on the local node.
- New agent operations (allowlisted, strict decoding): `db.backup` (stream of `DataChunk`, final `{bytes}`) and `upload.begin {kind:"db.restore", db:{...}}` followed by `upload.chunk`/`upload.finish` (the data is fed to the restore tool's stdin on finish). Both take `{kind, container, network, port, image, job, user?, database?, password?, volume?, mount_path?}`.
- aven-server never sends a script or container spec. The agent checks that `container` is a aven `app` container on that node, in `network` (its own environment network), running (stopped instead for a redis RDB restore, which must also mount `volume`). It picks the dump/restore script from a fixed table built into the binary (`pg_dump`/`pg_restore`, `mysqldump`/`mysql`, `redis-cli --rdb`/RDB placement, `mongodump`/`mongorestore`), connects to the container's own address, and runs a one-shot helper (`keel-dbb-<job>`/`keel-dbr-<job>`, role `helper`, same project/env/service labels, no ports). Credentials are passed only as environment variables.
- aven-server still does gzip compression/decompression (mysql) and writes to or reads from local/S3 storage, so remote backups are interchangeable with local ones (restore a remote backup into a local database and vice versa).
- An agent that predates these operations makes the backup or restore fail with `이 노드의 aven-agent가 원격 DB 백업/복원(db.backup)을 지원하지 않습니다. agent를 업그레이드하세요` (recorded as the backup `error` / `dbbackup.failed` or `dbbackup.restore_failed` event). The previous generic `db.dump` operation and `upload kind "dbrestore"` were removed.
- When the node holding the data is offline: `POST /volumes/{vid}/backups` returns `503` (`the node holding this data is offline ...`, previously `501`); volume restore keeps `409`; database backup/restore keep `400` with the same message.

**Remote volume sizes.** New agent operation `volume.sizes` → `{sizes: {"keel-vol-...": bytes}}` (only aven volumes; `docker system df -v` on the node). The 5-minute size refresh asks every connected node, so `Volume.size_bytes`, `volume.near_limit` / `volume.limit_exceeded`, the `volume_percent` alert metric and the `disk` part of usage now work for volumes on remote nodes. A node that cannot be measured this round (offline, old agent) keeps its last known sizes. This replaces the "remote volumes have no measured size" limitation above.

**Mesh hardening.**
- Handshake limits: at most 64 unauthenticated mesh connections (TLS handshake + header) at a time per node, and per source IP a token bucket of 20 new connections/s (burst 40). The addresses of current mesh peers are exempt from the per-IP limit. Over a limit the TCP connection is closed before TLS.
- At most 512 concurrent tunnels per authenticated peer node; beyond that the dial is answered with `too many tunnels from this node`. `ping` is not counted.
- IPv6: when an environment network also has an IPv6 subnet, the gateway opens a second local port on the network's IPv6 gateway and the forwarder container listens on IPv6 too (`TCP6-LISTEN ... ipv6only=1`), so containers that resolve the private domain to AAAA reach the service.
- Failover: `mesh.Import` gains `nodes: string[]` (all nodes serving the service, primary first; omitted when there is only one). `node` is kept (the primary). Each forwarded connection tries candidates with status `ok` in ascending RTT, then `pending`, and skips `unreachable` ones unless no other candidate is left. A failed dial marks the peer `unreachable` until the next successful probe. An agent that rejects the `nodes` field is sent the same config without it.
- Offline nodes: `DesiredMesh` leaves out nodes whose agent is not connected (they are neither peers nor import targets), and `Domain.dns` leaves out the records of offline serving nodes (unless every serving node is offline, in which case all records stay).
- Event `node.unavailable` (project scoped, one per affected service): emitted once when a remote node that was connected is seen disconnected by the 30 s mesh/reconcile pass, for each service that has replicas on it. It is listed in the notification kinds.

### Functions (single-file Bun services)

A function is a service with `source_type: "function"`: one TypeScript/JavaScript file (`index.ts`) edited in the dashboard and run with Bun. The code is stored per instance (service × environment), so each environment has its own working copy. Migration `migrateFunctions` (tables `function_code`, `deployment_function_code`).

- **Create:** `POST /projects/{pid}/services {"source_type":"function", "name"?: string, "code"?: string, "environment_id"?}` (member). `name` defaults to `function` (made unique), `icon` is `"function"`. Every environment instance gets `code` (default: a `Bun.serve` hello world on `PORT`). Code larger than 1 MiB (1,048,576 bytes) → `413`. New environments (`copy_from`, PR previews) copy the source environment's code, or the default code.
- **Build and run:** a deployment snapshots the instance's current code. The build context is generated from the snapshot (`Dockerfile` + `index.ts`) and built with the Dockerfile builder on the deploy node (`FROM oven/bun:1.2-alpine`, `CMD ["bun","run","index.ts"]`, `EXPOSE 8080`). The instance's `root_dir`, `builder`, `dockerfile_path` and `build_command` are ignored for functions. Redeploy/rollback reuse the image and the snapshot of the deployment that built it. All other instance settings work as for other services: variables, domains, healthcheck, replicas, `cron_schedule` (the file then runs on the schedule as a cron job), sleep, resource limits.
- **`pending_changes`** lists `"function.code"` while the working copy differs from the current deployment's snapshot (sorted together with the other keys). Reverting the code removes it.

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/services/{sid}/instances/{eid}/function` | member | `{code, runtime: "bun", sha256, updated_at: string\|null, deployed_sha256: string\|null, max_bytes}`. `deployed_sha256` is the hash of the current deployment's snapshot (null before the first deploy). `400` for non-function services |
| PUT | `/services/{sid}/instances/{eid}/function[?deploy=1]` | member | Body `{code: string, runtime?: "bun"}`. Saves the working copy (`code` required → `400`; other runtimes → `400`; over 1 MiB → `413`). Event `service.updated` when the code changed. `?deploy=1` starts a deployment right after saving (trigger `manual`). Returns the same shape as GET |

Web: the create palette has a **Function** entry; function services get a **코드** tab (editor with save, save-and-deploy, `Ctrl+S` / `Ctrl+Shift+S`).
Limits: one file and one runtime (Bun 1.2); no package installs (Bun auto-installs imports at runtime when the container has network access); environment sync and template export do not carry function code; the CLI has no function commands.

### Canvas service groups

Named, colored frames around services on an environment's canvas . A service belongs to at most one group per environment; adding it to a group removes it from the others. Migration `migrateCanvasGroupsDashboards` (table `canvas_groups`, cascades with the environment).

```ts
type CanvasGroup = {
  id: string; environment_id: string;
  name: string;            // 1..60 chars, no tabs/newlines
  color: string;           // gray|red|orange|yellow|green|teal|blue|purple|pink or "#rrggbb" (default gray)
  service_ids: string[];   // services of this project (max 200); deleted services are left out of responses
  rect: { x: number; y: number; w: number; h: number } | null; // frame of an empty group (finite, |v| ≤ 1e6, w/h ≥ 0, rounded)
  created_at: string; updated_at: string;
};
```

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/environments/{eid}/groups` | viewer | Groups in creation order |
| POST | `/environments/{eid}/groups` | member | Body `{name, color?, service_ids?, rect?}` → `201`. Max 50 groups per environment (`400`) |
| PATCH | `/environments/{eid}/groups/{gid}` | member | Any of `name`, `color`, `service_ids` (replaces the list), `rect`. `404` for a group of another environment |
| DELETE | `/environments/{eid}/groups/{gid}` | member | Ungroups (services and positions stay) → `204` |

Validation errors are `400`; `service_ids` outside the project → `400`. `GET /environments/{eid}/canvas` gains `groups: CanvasGroup[]`. Services keep their own canvas positions (`PUT .../canvas`); the web draws a group's frame around its members' cards, and uses `rect` only while the group is empty. Moving a group in the web moves every member (one position update per card).
Web: Shift/Ctrl+click or Shift+drag on the background selects cards (Shift+Enter on a focused card), then "그룹 만들기". The frame header drags the group (Alt+arrow keys move it by one grid step, Shift for four) and opens a menu (Enter/right-click): select members, add selected, remove a service, rename, color, ungroup.

### Observability dashboards

Project-level custom dashboards for the Observability page. Migration `migrateCanvasGroupsDashboards` (table `dashboards`, cascades with the project).

```ts
type Dashboard = { id: string; project_id: string; name: string /* 1..60 */; widgets: Widget[] /* max 30 */; created_at: string; updated_at: string };
type Widget = {
  id: string;              // generated when missing or duplicated
  type: "metric" | "logs" | "http" | "events";
  title: string;           // ≤ 100 chars; "" = derived from type and target
  service_id?: string;     // metric/logs: optional (omit = every service of the environment); http: required
  environment_id?: string; // required for metric, logs and http; optional for events (omit = all environments)
  metric?: "cpu_percent" | "memory_bytes" | "net_rx_bytes" | "net_tx_bytes" | "disk_bytes"; // metric only (required)
  range: "1h" | "6h" | "24h" | "7d"; // default "6h"
  x: number; y: number;    // grid position: x 0..11, y ≥ 0 (row); display order is y, then x
  w: number; h: number;    // columns 1..12 (default 6), rows 1..8 (default 3)
};
```

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/projects/{pid}/dashboards` | viewer | In creation order |
| POST | `/projects/{pid}/dashboards` | member | Body `{name, widgets?}` → `201`. Max 50 per project (`400`) |
| GET | `/projects/{pid}/dashboards/{dbid}` | viewer | `404` for another project's dashboard |
| PATCH | `/projects/{pid}/dashboards/{dbid}` | member | `name` and/or `widgets` (replaces the whole list) |
| DELETE | `/projects/{pid}/dashboards/{dbid}` | member | → `204` |

Widget `service_id`/`environment_id` must belong to the project (`400`). Widgets only store what to show; their data comes from the existing endpoints (`.../metrics`, `/environments/{eid}/metrics`, `/environments/{eid}/logs`, `.../http-logs`, `/projects/{pid}/events`) with the viewer's own permissions.
Web: Observability shows "개요" (the built-in overview) plus one tab per dashboard (`?dash=<id>`), "새 대시보드", rename/delete, and "위젯 추가". Widgets flow left to right in a 12-column grid; reorder by dragging the handle or with arrow keys / Home / End on the focused handle (announced to screen readers); width (1/3, 1/2, 2/3, full) and height presets from the widget menu. Every change is saved immediately with PATCH.

### Hard usage limits

A monthly (UTC calendar month) hard limit per project, separate from the soft `usage_alert`. Migration `migrateUsageLimits` (table `project_usage_limits`).

- **Setting:** `PATCH /projects/{pid}` (project admin) accepts `usage_limit: {cpu_core_hours?, memory_gb_hours?, egress_gb?, action?: "stop"|"notify"} | null`. Values must be in `(0, 1e9]`; `action` defaults to `"stop"`; `null`, `{}` or an object without any value turns the limit off. Invalid → `400`. The limit is checked again right away, so lowering it below the current usage suspends at once and raising/clearing it lifts the suspension.
- `GET /projects/{pid}` and the PATCH response gain `usage_limit` and `usage_suspension: {month, reason, suspended_at, until, stopped_instances: string[], lifted_at: string|null, active: boolean} | null`. `GET /projects/{pid}/usage` gains the same two fields.
- **Checking:** `aven-server` checks after every hourly roll-up and every 5 minutes in between. Month-to-date usage = rolled-up hours + the not yet rolled-up part of the current hour computed from metric samples, so the overshoot is bounded by about 5 minutes of usage. Egress counts container transmit bytes as in usage. Usage ≥ limit counts as reached.
- **On reaching a limit:** event `usage.limit_reached` once per metric per month (in the default events of new notification channels). With `action: "stop"` the project is suspended: every running (or sleeping, scheduled, crashed-with-a-live-deployment) instance is stopped like `POST .../stop` (deployments, volumes and data are kept), its ID recorded in `stopped_instances`, and until the suspension ends:
  - new deployments return `409 project suspended by usage limit: ...` (deploy, redeploy, rollback, `PATCH ?deploy=1`, `aven up`, GitHub pushes, template deploys, environment sync with deploy, function save-and-deploy, preview environments; everything that goes through the deployment queue),
  - `POST .../restart`, `POST .../scale` and `POST .../cron/run` return `409` with the same message.
- **End of a suspension:** the suspension is lifted (`lifted_at` set, `active: false`, event `usage.unsuspended`) when the month changes, the limit is raised or cleared, or `action` becomes `"notify"`. Stopped services are **not** restarted automatically.
- **`GET /projects/{pid}/usage/limit`** (viewer): `{project_id, month: "YYYY-MM", usage_limit, usage_suspension, month_to_date: {cpu_core_hours, memory_gb_hours, net_egress_gb, disk_gb_hours, estimated_cost?}, exceeded: ("cpu_core_hours"|"memory_gb_hours"|"egress_gb")[]}`.
- **`POST /projects/{pid}/usage/resume`** (project admin): clears the suspension record and redeploys every instance in `stopped_instances` that is still stopped (current image and settings, trigger `redeploy`; a manual deploy when there is no image). → `200 {restarted: string[], failed: {instanceId: reason}}`, event `usage.resumed`. `409` when there is no suspension record, or when the suspension is still active and usage is still over a `stop` limit (raise or clear the limit first).
- Web: project settings › 사용량 알림·한도 (limit form with action), and the usage page shows the month-to-date progress per limit, a suspension banner and **서비스 다시 시작** (admins).
- Limits: a project admin can raise or clear the limit and so lift the suspension (there is no workspace-only override). Disk usage is not limited. Instances on a remote node that is offline at suspension time cannot be stopped; they are retried on the next check while the suspension is active.

### Autoscaler state persistence

The autoscaler's per-instance state is stored in table `autoscale_state` (migration `migrateAutoscaleState`; `instance_id` primary key, cascades with the instance): configuration signature, last scale time (cooldowns), the stabilization-window recommendations and the last `autoscale_status`. It is loaded lazily the first time an instance is evaluated or its status is read, so after a `aven-server` restart `autoscale_status` (including `last_scale_at`) is shown immediately and cooldowns and the scale-down stabilization window continue where they were. The row is written when the state changes and at least once a minute while it does not (so the restored window is at most about a minute stale). Changing the autoscale configuration resets the history as before; disabling autoscaling (or deleting the instance) deletes the row. No API change.

### Data browser: RESP3, timeouts, column types and row editing

- **Column types.** `GET /services/{sid}/instances/{eid}/data/tables/{name}` (postgres/mysql/mariadb) first reads the table's columns (postgres `pg_attribute` + `pg_index.indisprimary`, `format_type()`; mysql `information_schema.COLUMNS.COLUMN_TYPE`, `COLUMN_KEY='PRI'`) and adds `column_types: (string|null)[]` (aligned with `columns`, `null` when a result column is not a table column), `primary_key: string[]` and `editable: boolean` (SQL table with a primary key). mongo returns `column_types: null`, `primary_key: []`, `editable: false`. An empty mysql table now returns its column names. Missing tables: postgres `400` (regclass error), mysql an empty column list.
- **Row editing** (project member, same as the rest of the data browser; postgres/mysql/mariadb only; `?schema=` like browse, postgres default `public`):
  - `PATCH …/data/tables/{name}/rows {pk: {col: value}, set: {col: value}}` → `200 {affected}`. `pk` must contain exactly the table's primary-key columns (non-null); `set` columns must exist. postgres `affected: 0` → `404`; mysql counts changed rows (`ROW_COUNT()`), so an update to the same value is `200 {affected: 0}`.
  - `POST …/rows {set}` → `201 {affected}` (empty `set` inserts a row of defaults).
  - `DELETE …/rows {pk}` → `200 {affected: 1}`, `404` when no row matched.
  - Values: `null` → `NULL`, booleans → `TRUE`/`FALSE`, numbers and strings → string literals (the column type converts them), objects/arrays → JSON text. Identifiers are quoted (`"…"` / `` `…` ``); postgres literals are `E'…'`, mysql literals `'…'` (`\` doubled, `'` doubled) and the script first removes `NO_BACKSLASH_ESCAPES` from the session `sql_mode`. NUL and invalid UTF-8 are rejected. UPDATE/DELETE use the full primary key (mysql adds `LIMIT 1`); postgres statements run as `WITH r AS (… RETURNING 1) SELECT count(*)`.
  - `400` for tables without a primary key (update/delete; use the query tab), unknown columns, partial/extra key columns, non-SQL databases.
  - Audit: event `database.edit` ("`<service> (<env>)`: `<user>`이 표 `<schema.table>`의 행(`id=7`)의 열 `name`를 바꿨습니다" / 행을 넣었습니다 / 행을 지웠습니다). Column names and primary-key values (first 200 characters) are recorded, new values are not. Added to the notification event kinds.
- **Timeouts.** Each data request gets a random tag. The client process inside the container carries `AVEN_DATA_TAG=<tag>`; postgres connects with `application_name=keel-data-<tag>`; mysql/mariadb statements are prefixed with `/* keel-data:<tag> */` and the client runs with `--comments` (not added when the script uses `DELIMITER`; only the first statement of a multi-statement query carries the tag). When the 30 s exec deadline passes (`504`), aven runs a cleanup exec (10 s limit) in the same container: TERM to processes whose environment has the tag (found through `/proc/*/environ`), then postgres `pg_cancel_backend(pid)` for `application_name = 'keel-data-<tag>'`, mysql `SELECT ID FROM information_schema.PROCESSLIST WHERE INFO LIKE '%keel-data:<tag>%'` followed by `KILL QUERY <id>` for each match. Cleanup failures are logged only. mongo browse uses `.maxTimeMS(25000)`.
- **RESP3.** The `redis-cli --no-raw` parser reads RESP3 replies: maps (`1# key => value`, `type: "map"`, keys and values alternate in `elems`), sets (`1~`), doubles, booleans, big numbers (`(big number) …`), nulls, `(empty hash)`/`(empty set)`, and verbatim strings (printed raw by redis-cli; inside an aggregate the lines up to the next element are joined, `type: "verbatim"`; at top level a multi-line verbatim string cannot be told apart from separate status replies). `POST …/data/query` accepts `resp3: true` for redis (runs `redis-cli -3`, needs Redis 6+); when the output is a single map reply the result is `kind: "table"` with columns `key`, `value` (other replies stay text).
- Web: the data tab shows column types and a key marker in the header; for editable tables a cell is edited by double-click/Enter (Enter saves, Esc cancels, **NULL** sets NULL), **행 추가** opens a form (blank fields use the column default) and each row has a delete button with a confirmation. The redis query editor has a **RESP3** toggle; map values are shown as key/value tables.

### Template export: secret literals

`POST /projects/{pid}/environments/{eid}/export-template` also scans every exported variable value (whatever its name) and the `start_command` / `pre_deploy_command` settings for literal secrets: tokens starting with `sk_`, `ghp_`, `github_pat_`, `xox?-`, `AKIA`/`ASIA` (+16), PEM blocks (`-----BEGIN …-----` through the matching `-----END …-----`), and tokens of at least 24 characters with Shannon entropy ≥ 3.5 bits/char (at least 90 % alphanumeric, two digits, not a path; `${{ … }}` expressions are skipped). Only the matched part is replaced: `secrets=generate` → `${{ secret(32) }}`, `secrets=inputs` → a required secret input `${{ inputs.<SERVICE>_<VAR|START_COMMAND|PRE_DEPLOY_COMMAND> }}`. Each replacement adds a warning naming the service, the field, the count and the reason (never the value). Variables whose names look secret are handled as before.
### Inbox stream (SSE)
`GET /api/notifications/stream` (any signed-in user, session cookie or API token; no migration) streams new items of the caller's own inbox as Server-Sent Events. Items are pushed by the same fan-out that writes `/api/notifications` (project visibility and the account's notification preferences apply), so a user never receives another user's items; items of projects the caller can no longer see (removed membership, scoped token) are skipped like in the list API.
- `retry: 5000` first, then `event: notification` with `data: {event_id, project_id, environment_id, service_id, kind, message, created_at}`. The inbox row `id` and the unread count are not included; refetch `GET /api/notifications` (the web bell does) to get them.
- `: ping` comment every 25 s as a heartbeat.
- The stream re-checks its credentials every 10 s and immediately after a logout, a session delete (`DELETE /auth/sessions/{sesid}`), a password change or a user being disabled. When the session/token is no longer valid it sends `event: revoked` with `data: {"reason": "..."}` and closes; clients should not reconnect after `revoked`.
- Delivery is best effort: a slow client's buffer (32 items) drops new items instead of blocking the fan-out, and items produced while disconnected are only in the list API.
- Web: the bell uses `EventSource` with exponential backoff (1 s → 30 s) and refetches the inbox on each item; while the stream is down it falls back to polling every 30 s (every 5 min while connected).

### SAML SSO

Workspace-wide SAML 2.0 single sign-on , built on `github.com/crewjam/saml` v0.5.1. Migration `migrateSAMLSSO` (tables `saml_config` (single row), `saml_login_requests`, `saml_assertion_replays`; column `sessions.saml_authenticated`).

- **SP endpoints (public):**
  - `GET /api/auth/saml/metadata` → SP metadata XML (`application/samlmetadata+xml`). The SP entity ID is this URL; the ACS is `POST /api/auth/saml/acs` (HTTP-POST binding). Both are derived from `AVEN_PUBLIC_URL` (or the request host). `404` until IdP metadata is configured.
  - `GET /api/auth/saml/login[?next=/path]` → SP-initiated login: `302` to the IdP (HTTP-Redirect binding) or an auto-submitting form when the IdP only offers HTTP-POST. A one-time `RelayState` (stored hashed, 10 min) binds the response to the request (`InResponseTo` and the bearer `SubjectConfirmationData` must match) and carries `next` (same-origin relative paths only).
  - `POST /api/auth/saml/acs` → validates the response, starts a session and returns a small HTML page that navigates to `next` (not a `303`, so the `SameSite=Strict` session cookie is sent after the IdP's cross-site POST). IdP-initiated responses (no or unknown `RelayState`) are accepted and land on `/`.
  - `GET /api/auth/saml/status` → `{configured, enforce_sso}` for the login page (`configured` = IdP metadata and at least one verified domain).
- **Validation:** XML signature on the response or assertion against the IdP metadata's signing certificates (SHA-1 signature/digest algorithms rejected), issuer = IdP entity ID, audience = SP entity ID, `Destination`/`Recipient` = ACS URL, `NotBefore`/`NotOnOrAfter` with ±180 s clock skew, `IssueInstant` at most 90 s old, and every assertion ID accepted only once (replay table, 24 h). Failures return `401` without detail.
- **Identity:** the email comes from the email `NameID` or an email/mail/UPN attribute (exactly one distinct value, otherwise `403`); the name from `displayName`/`name`. Only emails on a **verified domain** can sign in. An existing user with that email is signed in (the built-in `admin@local` never); otherwise the user is created (JIT) with the configured default role. Disabled users get `403`. Aven TOTP is not asked (the IdP is responsible for MFA). Audit `auth.saml_login`.
- **Settings (instance admin, session only):**
  - `GET /api/saml/settings` → `{config: {metadata_url, domains: [{domain, token, verified}], default_role, enforce_sso, configured, updated_at}, sp_metadata_url, acs_url, login_url, idp: {entity_id, sso_url, cert_not_after} | null}`. The stored IdP XML is not returned.
  - `PUT /api/saml/settings {metadata_url?, metadata_xml?, clear_metadata?, domains?: string[], default_role?: "admin"|"member"|"viewer", enforce_sso?}` → `{config}`. `metadata_url` is fetched immediately (10 s, 1 MiB, no redirects, loopback/link-local targets refused) and re-fetched on every save that sends it; `metadata_xml` is an uploaded file. Metadata must have an HTTP-Redirect/POST SSO endpoint and a signing certificate. `default_role` cannot be `owner` or above the caller's role. Audit `saml.connected`, `saml.disconnected`, `saml.enforcement_updated`, `saml.settings_updated`.
  - `POST /api/saml/domains/{domain}/verify` → looks up TXT `_keel-sso-verification.<domain>` and marks the domain verified when one value equals its `token`. Removing a domain from `domains` drops its verification; re-adding creates a new token.
- **Enforce SSO:** requires IdP metadata and a verified domain, and *turning it on* requires an admin whose current session was created by SAML on a verified domain . While enforced: password login for emails on a verified domain returns `403 {"error":"sso_required"}`, and existing non-SAML sessions of those users get `401 sso_required` on the next request (re-authentication). The built-in `admin@local` is the break-glass account and is never affected; users on other domains are unaffected; `keel_` API tokens and `AVEN_API_TOKEN` keep working (create a token in the dashboard after an SSO login for CLI use).
- Web: settings › SAML SSO (SP URLs to copy, metadata URL or XML upload, IdP summary with certificate expiry warning, domains with DNS TXT instructions, default role, enforcement toggle); the login page shows **조직 SSO로 로그인** once SSO is configured.
- Not implemented: single logout (SLO), encrypted assertions, signed AuthnRequests, SCIM, per-project SSO, automatic metadata refresh/certificate-expiry notifications.

### Image auto updates

Image services can check registries for newer image versions and redeploy them on a UTC maintenance schedule . The settings are saved immediately and do not appear in `pending_changes`.

- `image_update_policy`: `disabled` (default; stored as an omitted field), `patch` (patches only), or `minor` (minor updates and patches). For a stable `MAJOR.MINOR.PATCH` tag (optional `v` prefix), `patch` stays within the configured minor version and `minor` stays within the configured major version; the major version is never changed. Aven updates the configured tag (`:v1.2.3` → `:v1.2.4`).
- Non-semver tags (`:latest`, `:canary`, a major-only pin such as `:1`, …) keep their name; Aven redeploys when a different digest is pushed to that tag. Digest-pinned references (`image@sha256:…`) are immutable and never checked.
- `image_update_window`: `anytime` (default), `weekends` (Saturday/Sunday), `night` (02:00–06:00), or `custom`. Custom schedules use `image_update_days` (Sunday = 0 through Saturday = 6, at least one), `image_update_start_hour` (0–23) and `image_update_end_hour` (1–24, exclusive, greater than the start). All times are UTC; one custom range per instance.
- Registry checks run every 5 minutes for instances with a policy, with a 30-minute cache per instance. Aven talks to the Docker/OCI Registry API (Docker Hub, GHCR and any other registry) and uses the matching project registry credentials (`/projects/{pid}/registries`) for private registries. The deployment pulls the discovered manifest digest, so it runs exactly the version that was checked; the instance keeps the tag.
- `ImageUpdate`: `{id, instance_id, current_image, target_image, digest, digest_image, status, automatic, detected_at, started_at, finished_at, deployment_id?, error}`. `status`: `available`, `deploying`, `applied`, `failed`, `skipped`, `superseded` (replaced by a newer version, by a manual image change, or already rolled out by a regular deploy). A digest is recorded once; failed and skipped versions are not offered for automatic updates again.
- Automatic updates run only while the instance is online, the policy is enabled and the window is active. Before an automatic update Aven backs up every volume attached to the instance (a normal volume backup, subject to the volume's retention); if a backup fails, the update is marked failed and not deployed. If the deployment fails, Aven restores the previous configured image unless it was changed in the meantime.
- Events: `image.updated` (service, environment, previous and new image, automatic/manual) and `image.update_failed`. Subscribe to them in notification settings (`image.*`). The deployment trigger is `image_update`.
- Dashboard: Settings → Source → **이미지 자동 업데이트** (update preference, maintenance window, **업데이트 확인**), an "update available" banner with **지금 업데이트** / **이 버전 건너뛰기**, and the update history.
- This feature is managed through the dashboard and API.

### Public GraphQL API

`POST /graphql/v2` is a Railway-compatible GraphQL API over these REST routes. Same auth and permissions, because each resolver calls the REST route in-process with the caller's credentials. `GET /graphql/v2` serves a playground. Schema, mapping and gaps are described in [`docs/graphql.md`](graphql.md). CLI: `aven api`.

### Feature flags

Typed runtime configuration per project : each flag has a name and type for the whole project, and a default value plus targeting rules **per environment**. Migration `migrateFeatureFlags` (tables `feature_flags`, `feature_flag_values`; cascade with the project/environment). Expression engine: `internal/featureflags`.

```ts
type FeatureFlagType = "bool" | "string" | "number" | "json";
type FeatureFlagRule = { id: string /* [A-Za-z0-9][A-Za-z0-9._-]{0,63}, default "rule-<n>" */; when: string /* expression ≤ 2048 bytes */; value: unknown /* of the flag type */ };
type FeatureFlag = {
  id: string; project_id: string;
  name: string;                 // [A-Za-z0-9][A-Za-z0-9._-]{0,127}, unique per project
  type: FeatureFlagType;        // fixed; changing it needs force and clears rules in every environment
  environment_id: string;       // the environment these values belong to
  default_value: unknown;
  rules: FeatureFlagRule[];     // max 100
  configured: boolean;          // false: this environment has no value yet (created after the flag) and
                                // inherits the default environment's default_value, without rules
  updated_at: string;
};
type FeatureFlagSnapshot = { project_id: string; environment_id: string; flags: FeatureFlag[] };
```

**Evaluation.** Every rule whose `when` matches the context is collected. If at least one matches and all matching rules serve the same value, that value is served (`reason` `TARGETING_MATCH`, or `SPLIT` when a matching rule uses `bucket()`); if they disagree → default (`CONFLICT`); none → default (`NO_MATCH`). `trace` lists every rule with `matched`. A missing attribute never matches (also under `!`), so a rollout without the key attribute serves the default. `key` in the context is also exposed as `targetingKey`.

**Expressions** (CEL-style, same grammar in the server and the SDK): literals (`"str"`, numbers, `true`/`false`/`null`, lists `[...]`), attributes, `==`, `!=`, `<`, `<=`, `>`, `>=` (numbers or strings), `in` (list), `contains` (substring or list element), `matches` (RE2/JS regex), `&&`, `||`, `!`, parentheses, and `bucket(attr)`, which is a stable value in `[0,1)`: FNV-1a 64 of `flagName + "\0" + attr`, mixed with the murmur3 `fmix64` finalizer, top 53 bits / 2^53. So `bucket(key) < 0.25` is a 25 % rollout, stable per subject and independent per flag. Expressions are validated on save (`400`).

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/projects/{pid}/flags[?environment_id=]` | viewer | `FeatureFlagSnapshot`. Default: the token's environment (environment tokens) or the project's default environment. Environment tokens can only read their environment (`404`). Sends `ETag`; `If-None-Match` → `304` |
| POST | `/projects/{pid}/flags` | admin | Body `{name, type, environment_id, default_value, rules?}` → `201`. The default is seeded into every environment; `rules` only into `environment_id`. Duplicate name → `409`. Audit `feature_flag.created` |
| PUT | `/projects/{pid}/flags/{name}` | admin | Body `{environment_id (required), default_value?, rules? (replaces the list), type?, force?}`. Omitted fields keep the environment's current values. A different `type` without `force: true` → `409`; with it, every environment is reset to the type's zero value without rules, then this environment gets the given values. Audit `feature_flag.updated` |
| DELETE | `/projects/{pid}/flags/{name}` | admin | Deletes the flag in every environment → `204`. Audit `feature_flag.deleted` |
| GET | `/feature-flags[?environment_id=]` | project/environment token (viewer+) | SDK polling: the project (and, for environment tokens, the environment) is inferred from the token. Same response, `ETag`/`304` as above. Session cookies and workspace tokens → `403` |
| POST | `/feature-flags/evaluate` | project/environment token | Body `{name, environment_id?, context?: object}` → `{name, environment_id, value, reason, trace: [{rule_id, when, matched}]}`. Unknown flag → `404` |

Web: Settings → 기능 플래그 (environment picker, list with a toggle for bool defaults, create/edit dialog with a rule builder: attribute comparison, rollout % on a key attribute, or a free expression for `&&`/`||`). Admins edit, viewers read.
CLI: `aven flag` (docs/cli.md). SDK: `sdk/` (TypeScript, `aven-flags`): keeps the snapshot in memory, polls with `If-None-Match` (default 30 s, timer `unref`'d), evaluates locally with synchronous `getBoolean/getString/getNumber/getJson` and `evaluate*` (value, reason, trace). The fallback argument is returned only when the flag is missing, not yet loaded or of another type (`NOT_FOUND`/`TYPE_MISMATCH`). Token: `token` option, then `AVEN_TOKEN` (project/environment token, project inferred), then `AVEN_API_TOKEN` (account token; needs `scope.projectId` or `AVEN_PROJECT_ID`). Environment: `scope.environmentId`, then the injected `AVEN_ENVIRONMENT_ID`.
Differences: values and rules are per environment; no MCP tools; there is no separate project-token page, use an API token with project or environment scope.

### Passkeys, recovery codes and 2FA enforcement

Multi-factor authentication (authenticator app or passkeys, shared recovery codes) and two-factor enforcement. Migration `migrateMFA` adds `passkeys` (credential JSON encrypted with the store secret box, unique per `(rp_id, credential_id)`) and `webauthn_challenges` (one-use, 5 min, at most 10 000 pending). WebAuthn uses [go-webauthn](https://github.com/go-webauthn/webauthn) `v0.17.4`.

**Relying party.** RP ID = the host name of the request (`Origin` must match `Host`; `localhost` or a DNS name, IP addresses are rejected by WebAuthn), origin = the request origin. Passkeys are bound to the host they were registered on: reaching aven under another host name needs its own passkey. Every passkey requires a discoverable credential (`residentKey: required`) and user verification (`userVerification: required`); attestation is not checked (`none`). A sign counter that goes backwards rejects the login (cloned authenticator).

**Account** (browser session only; API tokens get `403`):
- `GET /auth/passkeys` → `[{id, rp_id, name, created_at, last_used_at}]` (no key material).
- `POST /auth/passkeys/register/begin {name? = "패스키" (≤ 80 chars), password}` → `{challenge_id, options /* PublicKeyCredentialCreationOptions JSON, base64url */}`. The current password is required (same rate limit as other password checks); max 20 passkeys per account.
- `POST /auth/passkeys/register/{challenge_id}/finish` body = the `PublicKeyCredential` JSON (`id, rawId, type, response.{clientDataJSON, attestationObject, transports?}`) → `{recovery_codes}`: 10 new codes when the account had none (first second factor), otherwise `[]`. The challenge is bound to the session that started it. Duplicate authenticator → `409`.
- `DELETE /auth/passkeys/{id} {password}` → `204`. Removing the last second factor also deletes the recovery codes.
- Recovery codes are shared by TOTP and passkeys. `POST /auth/2fa/enable` still issues a fresh set. `POST /auth/2fa/disable` keeps them while a passkey remains. `POST /auth/2fa/recovery-codes {code}` (TOTP accounts) or `{password}` (passkey-only accounts) regenerates them.
- Admin `PATCH /users/{uid} {reset_2fa: true}` removes TOTP, every passkey and the recovery codes.
- `GET /auth/me` adds `passkey_count`, `mfa_enabled` (TOTP or a passkey), `mfa_enforced`, `mfa_setup_required`. `GET /users` / `GET /users/{uid}` add `passkey_count`.

**Login** (no auth, same-origin only, same brute-force limits as `POST /auth/login`):
- `POST /auth/login` for an account with TOTP and/or passkeys and no `totp_code` → `401 {"error":"totp_required","methods":["totp"?,"passkey"?,"recovery_code"]}` (the error string is unchanged for old clients). Passkey-only accounts can send a recovery code as `totp_code`. A 6-digit code is only checked against an *enabled* TOTP secret.
- Second factor: `POST /auth/passkeys/login/begin {email?, password}` (checks the password like login) → `{challenge_id, flow: "login_factor", options}` (allowed credentials = the user's passkeys for this RP); then `POST /auth/passkeys/login/factor/{challenge_id}/finish` with the assertion JSON → `204` + session cookie.
- Passwordless: `POST /auth/passkeys/login/begin {}` → `{challenge_id, flow: "login_discoverable", options}`; `POST /auth/passkeys/login/discoverable/{challenge_id}/finish` → `204` + session cookie. The user is found from the credential ID and user handle (`SHA-256("keel-webauthn-user\0" + user id)`). Failed assertions count per IP (and per account once it is known). Disabled accounts, and the built-in owner while `AVEN_ADMIN_PASSWORD` is unset, cannot log in.
- Audit: `auth.login` ("… 패스키 로그인"), `user.updated` ("… 패스키 추가/삭제").

**Enforcement:**
- `GET /instance/security` (instance admin) → `{require_2fa}`; `PATCH /instance/security {require_2fa}` (instance admin, session only; `/workspace/security` remains an alias) → `{require_2fa}`. Turning it on needs the caller to have 2FA (`400` otherwise). Audit `workspace.security.updated`.
- While on, a **browser session** (dashboard, `aven login` CLI) of a user without TOTP or passkey gets `403 {"error":"mfa_setup_required"}` on every route except `GET /auth/me`, `POST /auth/2fa/setup`, `POST /auth/2fa/enable`, `GET /auth/passkeys`, `POST /auth/passkeys/register/begin` and `…/register/{id}/finish` (plus the unauthenticated login/logout routes). Logging in and accepting invites still work, so members are sent to the setup screen on their next visit and are unblocked as soon as a factor exists. API tokens (`keel_…`, `AVEN_API_TOKEN`) are not affected (automation remains available).
- While on, users cannot disable TOTP or delete their last passkey if it is their last factor (`409`). An admin `reset_2fa` puts the member back into the setup flow.
- Web: the dashboard shows a "2단계 인증을 설정하세요" screen outside Account → Security; the login page offers **패스키로 로그인** (passwordless) and **패스키로 확인** in the second step.

### Infrastructure as Code (project config)
Project-level IaC: one definition describes the services of an environment; the server plans it against the live state and applies it through the normal staged-changes model. The CLI evaluates `.keel/aven.ts` (TypeScript DSL) or reads `.keel/aven.json` / `aven.yaml` and sends the JSON below; see `docs/cli.md` (`aven config`). New table `iac_ownership(environment_id, address, partial)` (migration `migrateIAC`).

Definition (`"version": 1`, unknown fields → error):
```ts
type Definition = {
  version: 1;
  project_name?: string;      // informational; a different name is a plan warning, never a rename
  partial?: string;           // named partial (1-64 of A-Z a-z 0-9 . _ -); empty = whole project
  services: {
    name: string;             // service name ([a-z0-9-], not "shared"); address "service.<name>"
    source_type?: "repo" | "image" | "database" | "bucket" | "function";  // required to create; immutable
    database_kind?: "postgres" | "mysql" | "redis" | "mongo" | "bucket";  // required for new databases; immutable
    settings?: { [specKey]: any };   // PATCH /services/{sid}/instances/{eid} keys (repo_url, start_command, replicas, domains, …)
    variables?: { [KEY]: string | { preserve: true } };
  }[];
};
```
- **Settings are additive**: an omitted setting is left untouched (so the export omits values equal to the platform defaults and a clean import plans no changes). `domains` lists the **custom** domains (strings or `{host, target_port, tls, paths, redirect, basic_auth{username}}`); generated domains are kept as they are and never exported. `tcp_proxy` (server-assigned port) and compose runtime settings are not managed. basic auth passwords cannot be set from IaC (set them in the dashboard; an existing password is kept).
- **Variables are a whole map** once `variables` is present: listed keys are set, missing keys are removed (destructive). `{"preserve": true}` keeps the stored value (a plan error if there is none). Sealed variables can only be preserved or removed. Generated database passwords (`catalog.CredentialKeys`) are kept even when omitted. Omit `variables` to leave them alone.
- **Whole project** (no `partial`): every IaC-managed service of the environment that the file omits is **destroyed**. A service exists in every environment, so destroy deletes it everywhere (`destructive`, detail "deletes the service in every environment"). Compose stack services are never managed (declaring one is an error).
- **Named partials**: a partial manages only what it declares; omitting a service destroys it only if this partial owns it. Declaring a service owned by another partial → `cannot manage service "x": already managed by partial "y"`. A whole-project definition is rejected while any named ownership exists. Every apply from a partial claims ownership of the services it declares (`claims`), even when nothing else changes. Ownership rows of services deleted elsewhere are ignored.

Endpoints (all under `/projects/{pid}/environments/{eid}/config`; env-scoped tokens work for their environment):
- `GET …/config[?include_variables=1]` (viewer) → `Definition` of the environment. Variable values are `{"preserve": true}` except values that are a single `${{ ref }}`; `include_variables=1` (member+, else `403`) inlines non-sealed values.
- `POST …/config/plan {definition, show_values?}` (member) → plan; nothing is written.
  ```json
  {"environment_id": "env_…", "environment": "production", "partial": "api",
   "config_etag": "…", "plan_hash": "…", "destructive": true,
   "summary": {"create": 1, "change": 1, "destroy": 1},
   "changes": [
     {"action": "create", "resource": "service.worker", "detail": "image"},
     {"action": "change", "resource": "service.web", "field": "replicas", "from": 1, "to": 2},
     {"action": "change", "resource": "service.web", "field": "variables.OLD", "from": "«hidden»", "destructive": true},
     {"action": "destroy", "resource": "service.old", "destructive": true, "detail": "deletes the service in every environment"}],
   "claims": ["service.web"], "errors": [], "warnings": []}
  ```
  Variable values are `«hidden»` unless they contain a `${{ }}` reference or `show_values: true` (member+; sealed values stay hidden). Field changes cover every settable key plus `node_id` (placement). Validation problems (invalid settings, sealed literal, unknown source, reference cycle, duplicate domain, ownership conflicts) are listed in `errors` with `200`.
  `config_etag` hashes the environment's IaC state (services, settings, variables, node, ownership); `plan_hash` = hash(config_etag + definition).
- `POST …/config/apply {definition, plan_hash, confirm_destructive?, deploy?}` (member) → `200 {plan, staged, services: [{service, action: "created"|"updated"|"deleted", deployment_id?, deploy_skipped?}], claimed}`. The server re-plans under a lock: `422 {error, plan}` when the plan has errors, `409` when `plan_hash` differs (definition or environment changed since the plan; a dashboard edit or another apply also changes it), `409` for destructive plans without `confirm_destructive`. Creating or deleting services needs project-level member access (environment tokens get `403`).
  - New services are created like `POST /projects/{pid}/services` (instances in every environment, generated domain, database passwords, volumes, function code), then the declared settings/variables are saved on the target environment's instance.
  - Changes are staged like normal edits and show up in `pending_changes`; `deploy: true` enqueues deployments for created/changed instances. Destroy runs after the configuration writes.
  - Emits `environment.config_applied` ("`<env>` 환경에 코드형 인프라 설정을 적용했습니다 (생성 n, 변경 n, 삭제 n)…"); deletions also emit `service.deleted`.
- `GET …/config/partials` (viewer) → `{environment_id, environment_name, config_etag, iac_partials: {address: partial}, whole_project_available}`.
- `POST …/config/partials/release {partial, resources?, dry_run?, base_config_etag?}` / `POST …/config/partials/transfer {partial, to, resources?, dry_run?, base_config_etag?}` (admin) → `{affected_resources, iac_partials, whole_project_available, dry_run, base_config_etag}`. Omitted `resources` = everything the partial owns; an address owned by someone else, or a partial that owns nothing → `400`; `base_config_etag` mismatch → `409`. Only ownership metadata changes (event `environment.config_ownership`); it also invalidates saved plans.

Per-service config-as-code (`aven.json`/`railway.toml`) is unchanged: it is read from the repository at deploy time and still overrides the instance settings that IaC saved.

### Aven Agent (AI assistant, bring your own model)

Aven Agent is a chat assistant in the dashboard (top bar ✦ button) and in `aven agent` that reads projects, services, deployments, logs and metrics, and proposes or performs restart / redeploy / variable changes / rollback. It runs on a model the instance admin connects : any **OpenAI-compatible Chat Completions** endpoint with function calling (OpenAI, OpenRouter, Ollama, vLLM, LiteLLM, ...). There is no built-in model. Migration `migrateAgent` (table `agent_threads`, cascades with the user); the model settings live in the `settings` row `agent_model` with the API key sealed by `internal/secret` (`AVEN_DATA/master.key`).

**Settings**

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/ai/settings` | any user | `{endpoint, model, key_set, key_hint?, configured}`. `endpoint` and `key_hint` (last 4 chars of keys ≥ 12 chars) are blank for non-admins. The key is never returned |
| PUT | `/ai/settings` | instance admin | Body `{endpoint, model, api_key?, clear_api_key?}`. `endpoint` must be an absolute `http(s)` URL without credentials/query (trailing `/` is trimmed; `/chat/completions` is appended unless already there), `model` 1..200 chars. Omitted/blank `api_key` keeps the stored key. `{endpoint:"", model:""}` disconnects (also clears the key). Audit `agent_model.updated` |
| POST | `/ai/settings/test` | instance admin | Sends one short prompt with the saved settings → `{ok: true, reply}` or `{ok: false, error}` (`200` either way); `503` when not configured |

**Conversations** (per user; nobody else, including admins, can read them)

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/ai/threads[?project_id=]` | any user | Own threads, newest first (max 50): `{id, project_id?, title, created_at, updated_at}` |
| GET | `/ai/threads/{id}` | any user | Thread + `messages: [{role: "user"\|"assistant"\|"tool", content?, tool?}]` (tool entries only carry the tool name) and `pending_action?: {call_id, name, arguments, summary}` |
| DELETE | `/ai/threads/{id}` | any user | `204`; `404` for other users' threads |
| POST | `/ai/chat` | any user | Body `{prompt (≤ 20000 bytes), thread_id?, project_id?, environment_id?, service_id?}`. The ids describe what the user is looking at and are added to the system prompt (only if visible to the caller; a hidden `project_id` → `404`). Without `thread_id` a thread is created (title = prompt). `409` while the thread has a pending action or is already answering; `503` when no model is configured. Response: SSE stream (below) |
| POST | `/ai/confirm` | any user | Body `{thread_id, call_id, approve}`. Approves or declines the pending action, then continues the conversation (SSE). The pending action is claimed atomically: a second confirm → `404` |

SSE events of `/ai/chat` and `/ai/confirm` (`event:` / JSON `data:`):

- `thread {thread_id}` first.
- `token {text}` streamed assistant text.
- `tool {call_id, name, arguments, result?, is_error, declined?}` after each tool call (`result` only for confirmed actions).
- `confirmation_required {thread_id, call_id, name, arguments, summary}` — the run stops here until `/ai/confirm`.
- `done {thread_id, response}` or `error {message}` (model/HTTP errors are reported here; the status code is already `200`).

**Tools.** Every tool runs with the caller's permissions (`projectLevel`: role, project membership and API-token scope/environment apply exactly as for the REST API), and IDs the caller cannot see behave as not found.

| Tool | Kind | Needs |
|---|---|---|
| `list_projects` (with environments), `list_services` (status per environment), `list_deployments`, `get_logs` (runtime or build, current or given deployment, ≤ 200 lines), `get_metrics` (1h/6h/24h/7d, last 60 points), `list_variables` (names only, never values) | read | viewer |
| `restart_service`, `redeploy_service` (current config, trigger `manual`/`variables`), `set_variables` (`set`/`unset`, staged — applied on the next deploy; sealed variables keep their value when re-sent masked), `rollback_deployment` (image + variables of a live deployment) | action | member, **user confirmation** |

Actions are validated and permission-checked before the confirmation is shown (a viewer gets an error back in the conversation, never a prompt) and again when approved. Approved actions emit the usual events (`service.restarted`, `deploy.requested`, `variables.updated`, `deploy.rollback`, actor = the confirming user) and run to completion even if the client disconnects. The model gets at most 8 rounds of tool calls per request; history keeps the system prompt plus the last 80 messages (trimmed at a user message so tool results stay paired).

Web: ✦ in the top bar opens the panel (suggested prompts, streaming answer, tool chips, confirmation card with summary/arguments and 승인하고 실행 / 거절, history menu, stop button); failed/crashed deployments have **Aven Agent로 진단** in their menu. Settings > **Aven Agent** (instance admins): provider presets, endpoint, model, key (masked), 연결 테스트, 연결 해제.
Limitations: no hosted model or token billing; no PR creation, service creation, Agent Connectors/MCP or Slack bots; one workspace-wide model (no per-message model picker); logs sent to the model are not redacted (the settings page says so).

### OAuth 2.0 / OpenID Connect provider ("Login with Aven")

Aven is an OAuth 2.0 authorization server and OpenID Connect provider. Migration `migrateOAuthProvider` (tables `oauth_apps`, `oauth_client_secrets`, `oauth_pending_requests`, `oauth_authorization_codes`, `oauth_grants`, `oauth_access_tokens`, `oauth_refresh_tokens`).

- **Issuer:** `<AVEN_PUBLIC_URL>/oauth` (or `<scheme>://<Host>/oauth` when `AVEN_PUBLIC_URL` is unset; set it in production). Discovery: `GET /oauth/.well-known/openid-configuration` (also `/.well-known/openid-configuration`). JWKS: `GET /oauth/jwks`. ID tokens are signed with **EdDSA (Ed25519)**; the key is derived from `AVEN_DATA/session.key` (rotating that file rotates the key and logs everyone out).
- **Endpoints** (no `/api` prefix; token/userinfo/revoke/discovery send `Access-Control-Allow-Origin: *` and answer CORS preflight, no cookies):
  - `GET /oauth/auth` (alias `/oauth/authorize`): `response_type=code` (only), `client_id`, `redirect_uri` (byte-for-byte match with a registered URI, otherwise `400 invalid_redirect_uri` and **no redirect**), `scope` (space separated, `openid` required, unknown → `invalid_scope`), `state`, `nonce`, `code_challenge` + `code_challenge_method=S256` (required for public apps, recommended for confidential; `plain` is rejected), `prompt=consent|none`. Repeated parameters → `invalid_request`; `request`/`request_uri` (PAR, request objects) → `*_not_supported`. Errors after the redirect URI is verified go back as `?error=…&error_description=…&state=…&iss=…`.
  - Not signed in → `303 /login/?next=<authorize URL>` (`prompt=none` → `error=login_required`). Because the session cookie is `SameSite=Strict`, a cross-site navigation (`Sec-Fetch-Site: cross-site`) without a cookie first gets a tiny same-origin meta-refresh page so already signed-in users are recognized.
  - **Automatic consent:** if the user's active authorization for the app already covers every requested scope and `prompt` is not `consent`, a code is issued immediately (the previously shared projects are kept). `prompt=none` without such an authorization → `error=consent_required`. Otherwise → `303 /oauth/consent/#request=keel_req_…` (the request handle is in the fragment so it never reaches server logs; valid 10 minutes, bound to the signed-in user).
  - `offline_access` is honored only with `prompt=consent` (OIDC Core §11); without it the scope is silently dropped and no refresh token is issued.
  - Success: `?code=keel_code_…&state=…&iss=<issuer>` (RFC 9207). Codes are single use and valid 5 minutes. Deny → `error=access_denied`.
  - `POST /oauth/token` (`application/x-www-form-urlencoded` body only, no query parameters, no repeated parameters). Client authentication: `client_secret_basic` (form-urlencoded ID/secret, RFC 6749 §2.3.1) or `client_secret_post` for confidential apps (using both → `401 invalid_client`); public apps send only `client_id` and fail if they send a secret. Repeated client-auth failures lock the IP like bad API tokens (`429`).
    - `grant_type=authorization_code`: `code`, `redirect_uri` (must equal the authorize request), `code_verifier` (required if a challenge was sent; **rejected** if no challenge was sent). A wrong verifier/redirect burns the code. Reusing a code revokes the authorization it created and all its tokens (RFC 6749 §4.1.2).
    - `grant_type=refresh_token`: `refresh_token`, optional `scope` (must be a subset of the authorization; nothing is consumed when it is not). Refresh tokens rotate on every use (the old one fails afterwards), live 1 year from issuance, and each authorization keeps at most 100 active refresh tokens (oldest revoked first).
    - Response (`Cache-Control: no-store`): `{access_token: "keel_at_…", token_type: "Bearer", expires_in: 3600, scope, id_token, refresh_token?}`. ID token claims: `iss, sub (user ID), aud (client_id), iat, exp (1 h), nonce?`, plus `email`/`name` when those scopes were granted.
  - `GET|POST /oauth/me` (alias `/oauth/userinfo`, OIDC userinfo): `Authorization: Bearer keel_at_…` → `{sub, email?, name?}` per granted scopes. Invalid/expired → `401` with `WWW-Authenticate: Bearer error="invalid_token"`.
  - `POST /oauth/revoke` (RFC 7009, client authentication as above): `token=` an access or refresh token of that app. A refresh token revokes itself and every access token of the authorization; an access token revokes only itself. Unknown tokens → `200`.
- **Scopes → RBAC** (effective permission = min(user's real Aven role, scope)):
  - `openid` (required), `email`, `profile`, `offline_access`.
  - `workspace:viewer|member|admin`: the workspace-level level for every route and every project the user can see (Aven has one workspace per instance, so there is nothing to select).
  - `project:viewer|member`: only the projects the user selects on the consent screen (can be changed later under 승인된 앱); others are `404`.
  - OAuth access tokens never reach account, user, member or token management (`NoToken` routes → `403`), notifications, or templates; of the "any signed-in user" routes only `GET /api/auth/me` (returns just the granted claims, `auth: "oauth"`, `scopes`), `GET /api/projects` (with a workspace or project scope) and `GET /api/system`/`/api/nodes` (workspace scope) are allowed. A disabled user's tokens stop working immediately.
- **App management** (instance admin, session only):
  - `GET|POST /api/oauth/apps`: `POST {name, description?, client_type: "confidential"|"public", redirect_uris: string[1..10]}` → `201 {app, client_secret: "keel_secret_…"|null}`; the secret is shown once. Redirect URIs: absolute, no fragment/userinfo/wildcards; `https://…`, `http://` only for `localhost`/loopback (RFC 8252 §7.3), public apps may also use reverse-domain custom schemes (`com.example.app:/cb`).
  - `GET|PATCH|DELETE /api/oauth/apps/{appid}` (`PATCH {name?, description?, redirect_uris?}`; client ID and type are fixed). Deleting an app deletes every authorization and token it issued.
  - `GET|POST /api/oauth/apps/{appid}/secrets`, `DELETE …/secrets/{secretid}`: client secrets don't expire; at most 10 active; revoke after rotating.
- **Authorized apps** (the signed-in user, session only): `GET /api/oauth/authorized-apps` → `[{grant_id, app_id, client_id, name, description, scopes, project_ids, created_at, updated_at, last_used_at}]`; `PATCH /api/oauth/authorized-apps/{grantid} {project_ids}` (project-scope authorizations only); `DELETE …/{grantid}` revokes it and all its tokens at once. Re-authorizing afterwards is a fresh consent.
- **Consent API** (used by the web page; session cookie of the same user only): `GET /api/oauth/requests/{request}` → `{app: {name, description, client_id, redirect_host}, user, scopes: [{name, description}], projects, previous_project_ids, expires_at}`; `POST … {decision: "approve"|"deny", scopes?, project_ids?}` → `{redirect_uri}`. The user may approve a subset of the requested scopes (`openid` required); project scopes need at least one visible project.
- **Storage/security:** client secrets, request handles, codes, access and refresh tokens are `prefix<id>.<32 random bytes>`; only the SHA-256 of the random part is stored and compared in constant time. Expired requests/codes/tokens are pruned by the hourly maintenance loop. Audit events: `oauth.app.created|updated|deleted`, `oauth.secret.created|revoked`, `oauth.consent`, `oauth.grant.updated|revoked`.
- **Web:** 계정 › 승인된 앱 (everyone) and 계정 › OAuth 앱 (instance admins: create dialog, client ID/secret, redirect URIs, secrets, delete, and the endpoint URLs from discovery); consent screen at `/oauth/consent/`.
- **Not implemented:** dynamic client registration (`/oauth/register`), pushed authorization requests, app logos/`picture` claim, and a GraphQL `externalWorkspaces` query (use `GET /api/projects` with the token instead). There is no CLI command.

### PostgreSQL: PgBouncer connection pooling and major version upgrades

Database connection pooling and major version upgrades for `database_kind: "postgres"` services (other services get `400`). State is stored per instance in table `postgres_states` (migration `migratePostgresOperations`, cascades with the instance). Long-running work happens in the background; the mutating calls return `202` and the GET endpoints report progress. Only one PostgreSQL operation per instance runs at a time (`409` otherwise). If `aven-server` restarts mid-operation, the next GET picks up the deployment it was waiting for and finishes the operation.

```ts
type PostgresPooling = { status: "off"|"deploying"|"updating"|"enabled"|"failed", enabled: boolean,
  pool_mode: "transaction"|"session"|"statement", default_pool_size: number, max_client_conn: number,
  max_prepared_statements: number|null /* null = PgBouncer default (200) */, pooler_service_id?, pooler_service_name?,
  deployment_id?, error?, variables: string[] /* connection variables currently on the database service */ };
type PostgresCheck = { key, label, severity: "blocker"|"warning"|"info", passed: boolean, message };
type PostgresPreflight = { ready: boolean, current_major, target_major, available_targets: number[], data_size_bytes: number|null,
  checks: PostgresCheck[], checked_at };
type PostgresUpgrade = { status: "none"|"preparing"|"upgrading"|"succeeded"|"failed"|"reverting"|"reverted"|"revert_failed",
  current_major?, from_major?, target_major?, image?, from_image?, backup_id? /* volume backup */, post_backup_id? /* DbBackup */,
  deployment_id?, error?, can_revert: boolean, revert_deadline?, started_at?, finished_at?, in_progress: boolean, available_targets: number[] };
```

**Connection pooling** (`GET` viewer, mutations member):
- `GET /services/{sid}/instances/{eid}/postgres/pooling` → `PostgresPooling`.
- `PUT …/postgres/pooling {pool_mode?, default_pool_size?, max_client_conn?, max_prepared_statements?, reset_max_prepared_statements?}` → `202`. The first call creates an image service `<db>-pgbouncer` (`edoburu/pgbouncer:v1.25.2-p0`, port 5432, tcp healthcheck, in every environment like any service; placed next to the database on the canvas) and deploys it in this environment. Its variables reference the database (`DB_HOST=${{db.PGHOST}}`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `AUTH_TYPE=scram-sha-256`, `POOL_MODE`, `DEFAULT_POOL_SIZE` (default 20), `MAX_CLIENT_CONN` (1000), `MAX_PREPARED_STATEMENTS`); other variables added by the user are kept. If the database has a TCP proxy, PgBouncer gets one too. Only after the PgBouncer deployment is live are the database's variables switched:
  - `DATABASE_URL` → PgBouncer, private network (`${{<db>-pgbouncer.AVEN_PRIVATE_DOMAIN}}:5432`)
  - `DATABASE_PUBLIC_URL` → PgBouncer TCP proxy (only while PgBouncer is public)
  - `DATABASE_UNPOOLED_URL` → the previous `DATABASE_URL` (PostgreSQL directly; migrations, `CREATE INDEX CONCURRENTLY`, `LISTEN/NOTIFY`, advisory locks)
  - `DATABASE_PUBLIC_UNPOOLED_URL` → the previous `DATABASE_PUBLIC_URL`
  
  Services that reference `${{<db>.DATABASE_URL}}` / `${{<db>.DATABASE_PUBLIC_URL}}` therefore follow automatically; the ones that are online in this environment are redeployed (same image) so they pick up the new value. A failed first deployment leaves the variables untouched (`status: "failed"`, `error`; PUT again to retry). Later PUTs only change and redeploy PgBouncer (`status: "updating"` → `enabled`; the database is not restarted). `{"enabled": false}` is the same as DELETE.
- `DELETE …/postgres/pooling` → `200 PostgresPooling`: restores `DATABASE_URL`/`DATABASE_PUBLIC_URL` from the `*_UNPOOLED_URL` variables, removes those, deletes the PgBouncer service (or only stops its instance here if another environment still uses it) and redeploys the referencing services.
- Events: `postgres.pooling.deploying|enabled|updated|failed|removed`.
- Not implemented: PgBouncer replica scaling from this API (scale the PgBouncer service like any service) and live pool statistics.

**Major version upgrade** (in place, with `pgautoupgrade/pgautoupgrade:<major>-alpine`, the official image plus an automatic `pg_upgrade` on start):
- `GET …/postgres/upgrade/preflight[?to=18]` → `PostgresPreflight` (read-only; default target is the newest supported major). Blockers: image must be official `postgres:<major>[.<minor>]-alpine` (pinned major, not `latest`/debian), source major 14–17, target above it and ≤ 18, service online with a live deployment and one replica, port 5432, no custom start command, a single volume at `/var/lib/postgresql/data` with `PGDATA=/var/lib/postgresql/data/pgdata` (aven default, or the post-upgrade layout below), no operation in progress; plus SQL checks in the running container (psql, like the data browser): server major matches the image, no `reg*` OID columns, no `aclitem` columns when crossing 16, no open prepared transactions, every extension shipped by the image (contrib; `adminpack`/`old_snapshot` gone in 17). Warnings: logical replication slots (not carried over), roles with md5 passwords, event triggers, volume size limit below twice the data size. Info: data size. `reg*`/`aclitem` columns are checked in the service's own database (`PGDATABASE`).
- `POST …/postgres/upgrade {to_major?}` → `202 PostgresUpgrade`, `409 {error, preflight}` when there are blockers. Phases: `preparing` (stop the database, synchronous volume backup = `backup_id`), `upgrading` (volume moved to `/var/lib/postgresql` with `PGDATA=/var/lib/postgresql/pgdata` – the same files – image set to `pgautoupgrade/pgautoupgrade:<to>-alpine`, deployed with a 3600 s healthcheck timeout so `pg_upgrade` can finish; the instance setting itself is unchanged), then the new server is checked (major = target and user tables still present) and a logical backup is taken (`post_backup_id`, trigger `postgres-upgrade`) → `succeeded`. Any failure after the backup restores it automatically (`reverting` → `reverted` with `error` describing the failure, or `revert_failed`). A failed backup restarts the database on the old image (`failed`, no revert).
- `GET …/postgres/upgrade` → `PostgresUpgrade`.
- `POST …/postgres/upgrade/revert` → `202`: stops the database, restores the pre-upgrade volume backup, restores image, `PGDATA` and mount path and redeploys (`reverting` → `reverted`). Allowed for 7 days after a successful upgrade (`revert_deadline`), and after `failed`/`revert_failed`, while the backup still exists (volume backup retention can prune it). Writes made after the upgrade are lost.
- Events: `postgres.upgrade.started|succeeded|failed|reverting|reverted|revert_failed`.
- Changing the image tag by hand does not migrate the data directory; use this flow.

### Point-in-time recovery (Postgres WAL / MySQL binlog archiving)

Point-in-time recovery: a database's write-ahead log is archived continuously to the backup storage (local `AVEN_DATA/dbbackups/pitr/…` or an S3 destination, see above) and the database can be restored to any second inside the retention window, into a **new service** (default, the source is never touched) or **in place**. Postgres (`postgres` kind) and MySQL 8 (`mysql` kind) only; mariadb, redis and mongo return `400`. Works on local and remote nodes (remote agents need this version).

How archiving works:
- **Postgres.** Enabling runs, inside the database container, `ALTER SYSTEM SET wal_level=replica, archive_mode=on, archive_timeout=60, archive_command='… cp %p <volume>/keel-pitr/wal/%f …'` and appends `host replication all all <method>` (the method of the image's `host all all all` line) to `pg_hba.conf` for base backups. If `archive_mode` was off the database is restarted once. `PGDATA` must be a subdirectory of the data volume (the catalog default `/var/lib/postgresql/data/pgdata` is); the archive directory sits next to it on the same volume. Every minute, if the WAL position moved, aven commits a tiny transaction (a timestamped commit record, so recovery can stop at the target) and calls `pg_switch_wal()`; a helper container of the database image mounts the volume read-only and streams the finished WAL files, the server gzips and uploads each one and only then deletes it from the volume. Archiver failures (`pg_stat_archiver`) are reported as `last_error`. Base backups: `pg_basebackup -Ft -z -X none` once a day.
- **MySQL.** Uses the binary log (on by default in MySQL 8; `log_bin=0` is refused). Every minute, if the active binlog grew, aven runs `FLUSH BINARY LOGS`, fetches the closed binlogs with `mysqlbinlog --read-from-remote-server --raw`, uploads them gzipped and then `PURGE BINARY LOGS TO` the first file that is not archived yet (binlogs from before the first base backup are purged too). Base backups: `mysqldump --single-transaction --source-data=2` of the user databases (the binlog position is read from the dump header) once a day.
- **Retention.** `retention_days` (1..90, default 7): the newest base backup older than the horizon and everything after it is kept, older base backups and logs are deleted from storage. The window starts at the first base backup after enabling (not retroactive).
- Disabling stops archiving (Postgres: `archive_command='/bin/true'`, `archive_mode` reset for the next restart, the archive directory is removed) and keeps the stored archive, which stays restorable until `DELETE …/pitr/archive`. A backup destination holding PITR files can't be deleted (409).

```ts
type PitrConfig = { enabled: boolean; retention_days: number; destination_id: string /* "" = local */; epoch: number;
                    enabled_at?; last_base_at?; last_sync_at?; last_error? };
type PitrRestore = { status: "running"|"done"|"failed"; step?; error?; target_time; target_service_id; target_instance_id; in_place: boolean; started_at; finished_at };
type PitrStatus = { supported: boolean; reason?; kind; config: PitrConfig;
                    window: { earliest: string|null; latest: string|null; bases: number; logs: number; bytes: number };
                    running: ("base"|"restore"|"sync")[]; restore: PitrRestore|null /* last restore of this source since the server started */ };
```
- `GET /services/{sid}/instances/{eid}/pitr` → `PitrStatus` (viewer). `earliest` is the oldest base backup's completion time, `latest` the newest archived log.
- `PUT …/pitr {enabled?, retention_days?, destination_id?}` → `{config, warning?}` (member). Enabling needs the database online. `warning` is set when the settings were saved but the restart for `archive_mode` failed.
- `POST …/pitr/base` → `202` start a base backup now (409 if one runs).
- `POST …/pitr/restore {target_time, in_place?: false, new_service_name?}` → `202 PitrRestore`. `target_time` is RFC3339 (or `YYYY-MM-DD HH:MM[:SS]` UTC), truncated to the second; it must lie in the window (if it is after `latest` and the source is online, the pending logs are archived first). Restore uses the newest base backup at or before the target and the logs after it of the same lifetime (`epoch`, bumped by every in-place restore); a missing MySQL binlog in the sequence is refused up front.
  - New service (default): creates `<source>-restored-YYYYMMDD-HHMM` (or `new_service_name`) from the catalog with the source's variables (the restored data has the source's users/passwords) and image, deploys it, waits until it is online, then restores into it. It does not archive.
  - `in_place: true`: overwrites the source. Postgres stops the database, replaces `PGDATA` with the base backup, writes `restore_command`/`recovery_target_time`/`recovery_target_action='promote'` and `recovery.signal`, starts it and waits until `pg_is_in_recovery()` is false. MySQL drops the user databases, loads the dump and replays the binlogs with `mysqlbinlog --start-position --stop-datetime` (both with `sql_log_bin=0`). Archiving continues in a new epoch with a fresh base backup.
  - MySQL targets have one-second granularity (state at the start of that second); Postgres includes transactions committed up to the target.
- `GET …/pitr/restore` → last `PitrRestore` (404 if none since the server started). `DELETE …/pitr/archive` → `{deleted}` (PITR must be off).
- Events: `pitr.enabled`, `pitr.restart`, `pitr.base_done`, `pitr.base_failed`, `pitr.fork`, `pitr.restored`, `pitr.restore_failed`.
- Agent protocol: `db.backup` / `upload db.restore` take `action: "pitr-base"|"pitr-wal"|"pitr-binlog"|"pitr-restore"` (+ `files`, `target_time`, `data_dir`, `keep_archive`, `base_file`, `base_position`); scripts are fixed in `internal/agentprotocol/dbpitr.go`. WAL/binlog files are streamed as frames `KEELPITR <name> <size>\n<bytes>`.
- Web: database service → **백업** tab → **시점 복구**: the "꺼져 있습니다" banner with **시점 복구 켜기** (storage + retention), status (last base backup / last sync / archive size / error), **기준 백업 지금**, **끄기**, and the restore box with the window, a date-time picker (browser time zone), **새 서비스** / **이 서비스에 덮어쓰기**, the new service name and **이 시점으로 복원**.

### Tracing (OpenTelemetry)

Tracing: a per-service, per-environment switch. Migration `migrateTracing` (tables `tracing_settings`, `trace_spans`; both cascade with the instance / environment).

- **Edge spans.** When a service is traced, its routes in `keel-proxy` get Caddy's `tracing` handler (Caddy **2.11+**, which supports `span_attributes` and `OTEL_EXPORTER_OTLP_PROTOCOL`). Each request to a public domain gets a server span (component `edge`) with `keel.project.id`, `keel.environment.id`, `keel.service.id`, `keel.service.name`, `keel.deployment.id`, `keel.replica.id` (upstream container) attributes, a W3C `traceparent` header is forwarded to the container, and the response carries `X-Keel-Trace-Id`. A client `traceparent` is continued and its sampled flag is followed (parent-based sampler): `-01` forces a trace, `-00` suppresses it. Routes apply immediately (no redeploy). If the running Caddy rejects the handler (older image), aven loads the routes without tracing and logs a warning, so routing never breaks; `docker pull caddy:2-alpine` and restart aven-server. Edge spans are produced by the server node's proxy only.
- **Provided variables.** On the next container start (deploy, restart, scale) of a traced service aven adds, unless already set: `OTEL_EXPORTER_OTLP_ENDPOINT` (`AVEN_OTLP_ENDPOINT`, default `http://host.docker.internal:4318`; the container gets `--add-host host.docker.internal:host-gateway`), `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_EXPORTER_OTLP_HEADERS=X-Keel-Trace-Token=<token>`, `OTEL_SERVICE_NAME` (service name), `OTEL_SERVICE_VERSION` (commit SHA, else deployment id), `OTEL_METRICS_EXPORTER=none`, `OTEL_LOGS_EXPORTER=none`, `OTEL_PROPAGATORS=tracecontext,baggage`, and `aven.*` entries in `OTEL_RESOURCE_ATTRIBUTES` (merged with yours). If the service sets `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` itself, none are added (the edge still traces). Variables are injected at container start and are not stored in the deployment's variable snapshot.
- **Receiver.** aven-server listens on `AVEN_OTLP_LISTEN` (default `:4318`, `off` disables) for OTLP/HTTP `POST /v1/traces` (`application/x-protobuf` or `application/json`, optional `Content-Encoding: gzip`, max 4 MiB, 10 000 spans) and OTLP/gRPC `TraceService/Export` over h2c (`grpc-encoding: gzip` supported). The same HTTP endpoint is also mounted on the API listener (`/v1/traces`) so containers on other nodes can export to `AVEN_PUBLIC_URL` (set `AVEN_OTLP_ENDPOINT` accordingly). `/v1/metrics` and `/v1/logs` return `404`. Every export needs `X-Keel-Trace-Token` (or `Authorization: Bearer`): a per-instance token (generated on first enable, kept across disable/enable, rejected while disabled) or the server's proxy token (setting `tracing_proxy_token`, used only by keel-proxy). A service token fixes project/environment/service, so an app cannot write spans for another service; deployment and replica come from the `keel.deployment.id`/`keel.replica.id` resource attributes. Limits: 1 000 spans per 10 s per replica (excess → OTLP partial success `rejected_spans`), 128 attributes/events/links per span (excess counted in `keel.dropped_count`), values cut at 16 KiB. Spans are kept for `AVEN_TRACE_RETENTION` (Go duration, default `168h`), pruned at start-up and hourly.
- **Not implemented:** eBPF automatic instrumentation, sampling under load, a separate regional proxy span.

```ts
type TracingSetting = { instance_id: string; service_id: string; service_name: string; enabled: boolean; has_public_domain: boolean;
  updated_at: string | null; last_edge_span_at: string | null; last_service_span_at: string | null };
type TraceSummary = { trace_id: string; started_at: string; duration_ms: number; root_name: string; service_id?: string; service_name?: string;
  span_count: number; error_count: number };
type TraceSpan = { trace_id: string; span_id: string; parent_span_id?: string; project_id: string; environment_id: string;
  service_id?: string; service_name?: string; deployment_id?: string; replica_id?: string; component: "edge" | "service";
  name: string; kind: "server" | "client" | "internal" | "producer" | "consumer"; status: "ok" | "error" | "unset";
  start_time: string; end_time: string; duration_ms: number; attributes: Record<string, unknown>; resource_attributes: Record<string, unknown>;
  events: { name: string; time: string; attributes?: Record<string, unknown> }[]; links: { trace_id: string; span_id: string; attributes?: Record<string, unknown> }[] };
```

| Method | Path | Access | Notes |
|---|---|---|---|
| GET | `/environments/{eid}/tracing` | viewer | `TracingSetting[]` for every non-database service of the environment, by name |
| PUT | `/environments/{eid}/tracing` | member | `{enabled, service_ids?}` (omit = all non-database services) → the list. Unknown/database id → `404` |
| GET | `/services/{sid}/instances/{eid}/tracing` | viewer | One `TracingSetting` |
| PUT | `/services/{sid}/instances/{eid}/tracing` | member | `{enabled}` → `TracingSetting`. Databases → `400`. Proxy routes are re-applied immediately |
| GET | `/environments/{eid}/traces` | viewer | `TraceSummary[]`, newest first. `since`/`until`: `30m`, `2h`, `1d`, `1w` or RFC 3339 (default last hour → now); `service=<id>` (traces with a span of that service); `filter=<query>`; `errors=true` (adds `@status:error`); `limit` 1–500 (default 100) |
| GET | `/environments/{eid}/traces/{id}` | viewer | `TraceSpan[]` ordered by start. `{id}` is a 32-hex trace id or a whole `traceparent`. `max_spans` 1–2000 (default 1000). Another environment's trace → `404` |
| GET | `/environments/{eid}/trace-attributes` | viewer | Attribute and resource attribute keys seen in recent spans (`since`, default 1h; filter autocomplete) |

Filter syntax (same as logs): keywords match span names; `@field:value` with `@trace`, `@span`, `@name` (`*` suffix = prefix match), `@serviceName` (`service.name` of the exporter), `@service` (aven service name or id), `@deployment`, `@replica`, `@component` (`edge`/`service`), `@kind`, `@status`, `@duration` (ms; `>N`, `<N`, `>=N`, `<=N`, `N-M`); any other `@key` looks up span attributes then resource attributes (numbers support the same comparisons). `AND` (or a space), `OR`, `-`/`NOT`, parentheses and `"quoted values"`. A trace matches when any of its spans in the window matches.

`GET .../http-logs` lines carry `trace_id` when the edge traced the request.

Web: a **트레이스** page in the side rail (environment-scoped) with a trace ID box (accepts a `traceparent`), filter with attribute-key suggestions, time range, live refresh, error-only toggle, a histogram and the trace list; a trace opens in a waterfall (nested spans, timing bars, a color per service) with a span detail panel (attributes, resource attributes, events, links, status). The open trace is in the URL (`/traces/?trace=<id>`). **트레이싱 설정** lists the services with a **Traced** switch and when the edge and the app last sent a span; it opens by itself while the environment has no traces. HTTP log rows with a trace id link to the trace.

### Edge: CDN caching, WAF and edge rules

The built-in proxy supports CDN, WAF, and edge rules. Settings are per instance (service × environment), stored as JSON in table `edge_settings` (migration `migrateEdgeSettings`, cascades with the instance), and applied to every node without a redeploy. They apply to the instance's non-redirect domains (a path rule that sends a path to another service still uses the host owner's settings).

**Request path.** Hosts with at least one edge feature on (CDN, deny list, rate limit, managed rules, Under Attack Mode or an enabled rule) are proxied by keel-proxy (after TLS and basic auth) to the node's edge engine (`internal/edge`, inside aven-server / aven-agent) over `unix//run/keel/edge.sock`. The engine applies WAF and rules, answers from its cache, and otherwise forwards to a Caddy origin server on `unix//run/keel/edge-origin.sock` that has the host's normal path rules and replica balancing. Both sockets live in the private `keel-proxy-wake` volume. keel-proxy overwrites `X-Keel-Client-Ip`/`X-Keel-Client-Proto` before the engine and turns them back into `X-Forwarded-For`/`X-Forwarded-Proto` for the container. Other hosts are routed exactly as before. If the engine socket cannot be opened at startup the node logs a warning and ignores edge settings. Order inside the engine: allow list (skips the WAF checks) → deny list (403) → managed rules (403) → rate limit (429 + `Retry-After`) → Under Attack Mode → edge rules → cache → origin. WAF/rule responses carry `X-Keel-Edge: waf-ip|waf-managed|waf-rate-limit|challenge|rule` and `Cache-Control: no-store`.

```ts
type EdgeSettings = {
  cdn: {
    enabled: boolean;                     // default false
    html_caching: "auto" | "force" | "never";  // default auto
    default_ttl_seconds: 1800 | 3600 | 7200 | 14400 | 43200 | 86400;  // default 7200
    honor_swr: boolean;                   // default true
    purge_on_deploy: "off" | "html" | "all";   // default html
    html_generation: number; all_generation: number;          // read-only purge counters
    last_purged_html: string | null; last_purged_all: string | null;
  };
  waf: {
    allow_ips: string[]; deny_ips: string[];      // IPv4/IPv6 addresses or CIDRs, ≤ 256 each (normalized, de-duplicated)
    rate_limit: { requests: number; window_seconds: number } | null;  // 1..1e6 per 1..3600 s, per client IP (IPv6 per /64)
    managed_rules: boolean;               // path traversal, sensitive files (.env, .git), SQLi, XSS, Log4Shell, scanner user agents
    under_attack: { since: string; until: string | null } | null;     // read-only here; until null = until disabled
  };
  rules: Ruleset;                         // Railway-compatible ruleset JSON, see below
  available: boolean;                     // the instance has a non-redirect domain
  domains: string[];
  under_attack_active: boolean;
};
```

Endpoints (`GET` = project viewer, the rest = project member; every change emits a `service.updated` event and re-syncs all proxies):

- `GET /services/{sid}/instances/{eid}/edge` → `EdgeSettings`.
- `PATCH …/edge {cdn?: {...}, waf?: {...}}` → `EdgeSettings`. Only the given keys change; read-only fields are ignored. Enabling CDN without a domain → `400`.
- `PUT …/edge/rules Ruleset` → `EdgeSettings`. Replaces the whole ruleset after validating all of it; on error `400 {error}` names the field (`rules[2].if.and[0].value: …`) and nothing is saved. Rules are sorted by `priority`, missing `id`s get `rul_…`. Needs a domain when the ruleset is not empty.
- `POST …/edge/cdn/purge {scope: "html" | "all"}` → `EdgeSettings`. `html` drops cached HTML, `all` everything; nodes drop the entries when the routes arrive (normally within a second).
- `POST …/edge/waf/under-attack {duration?: "1h" | "3h" | "12h" | "24h" | "forever"}` (empty = forever; needs a domain) and `DELETE …/edge/waf/under-attack` → `EdgeSettings`. Expiry is checked per request, so no resync is needed when it ends.

**CDN** (in-memory LRU per node: `AVEN_EDGE_CACHE_MB`, default 256; largest entry `AVEN_EDGE_CACHE_MAX_ITEM_MB`, default 32). Eligible requests: only `GET`/`HEAD` without `Authorization` (and not WebSocket upgrades) are eligible; `x-cache: HIT | STALE | MISS | DYNAMIC` (no header when not eligible or bypassed) and `age` on cached answers. Not stored: `no-store`, `private`, `Set-Cookie`, `Vary: *`/`Vary: Cookie`, `text/event-stream`, redirects with `Content-Length: 0`, bodies over the item limit, statuses other than 200/203/204/300/301/302/307/308/404/410. TTL: rule `cache_override` → `s-maxage` → `max-age` (`no-cache` = 0) → default TTL for static content types (Static-content `Content-Type` list: `image/*`, `font/*`, `text/css`, `text/javascript`, `application/wasm`, …) and for HTML in `force` mode; other responses (JSON, auto-mode HTML) need an explicit max-age. `stale-while-revalidate` serves `STALE` and refreshes in the background once per key (when `honor_swr`); `stale-if-error` serves the stale copy on origin errors/5xx regardless. Cache key = instance + host + path + sorted query + encoding class (`br`, `gzip` or `identity`; only that value is sent upstream) + the `Vary` request headers. `If-None-Match` matching the cached `ETag` (weak comparison, lists, `*`) → `304` from the cache. HEAD misses are fetched as GET so they fill the cache. Purge on deploy: when an instance's current deployment becomes live, `html` bumps the HTML generation and `all` both (the first deployment seen after enabling CDN only records itself). `GET /.keel/cdn-trace[?json]` on a CDN host reports the node, host, client IP, time and cache size.

**Under Attack Mode / challenge.** Browser navigations (`GET` with `Accept` containing `text/html`) without clearance get a `403` interstitial (Korean) whose script solves a small proof-of-work and posts it to `/.keel/challenge`; the engine answers `303` back to the page with cookie `__keel_clearance` (HMAC of host, expiry and User-Agent with a per-instance secret that the API never returns; `HttpOnly`, `SameSite=Lax`, `Secure` on HTTPS) valid 24 h or until the mode ends. Other requests without clearance get a plain `403`. The same check backs the `challenge` rule action.

**Edge rules** use Railway-compatible JSON (`{"version":1,"rules":[{id?, description?, priority, enabled?, if, then}]}`). Conditions: `{attr, op, value, key?}` or nested `and`/`or`/`not` (≤ 4 levels, ≤ 32 clauses per rule). Attributes and operators: `ipv4.src` (`eq`, `neq`, `in`, `not_in`; IP or CIDR; IPv6 clients never match), `http.host` (`eq`, `neq`, `in`, `matches`; lower-cased), `http.path` (`eq`, `neq`, `contains`, `matches`; case-sensitive, no query), `http.header` (same ops, `key` = header name); aven additions `ip.src` (like `ipv4.src` but IPv6 too) and `http.method` (`eq`, `neq`, `in`, `not_in`). `in`/`not_in` take arrays (≤ 64), others a string (≤ 256 chars); `matches` uses `*` wildcards (≤ 8). A negative comparison against a missing header/IP is false. Actions: `block {status? 400..499 (403), body? ≤ 4096}`, `allow` (skip later rules), `challenge`, `redirect {host | location, status? 301|302|307|308 (302), preserve_path?, preserve_query?}` — terminal; `cache_override {ttl_seconds 1..2592000 | bypass: true}` (needs CDN) and aven additions `rewrite {path}` (changes the path/query sent upstream; later rules see the new path) and `set_header {name, value ("" deletes), target?: "response" | "request"}` (not `Host`, `Content-Length`, hop-by-hop or `X-Keel-*`) — non-terminal. Limits: 100 rules, 64 KiB.

Web: service **Settings > 네트워킹** has CDN 캐싱 (toggle, HTML caching, default TTL, SWR, purge on deploy, purge buttons with last purge times), 공격 방어 모드 (duration select / activate, active state with time left / deactivate), 방화벽(WAF) (allow/deny lists, rate limit, managed rules) and 엣지 규칙 (list with enable toggle, reorder, builder dialog for flat and/or conditions, JSON editor for the whole ruleset). CLI: `aven cdn`, `aven waf`, `aven edge rules` (docs/cli.md).

Tests: `internal/edge` covers the engine (cache statuses, SWR/stale-if-error, purge generations, WAF, challenge flow, rules); `TestEdgeWithRealCaddy` in `internal/proxy` runs the whole socket path against a real Caddy binary when `AVEN_CADDY_BIN` is set.

Limitations: one edge per node instead of a global POP network (caches are per node; purges reach a node with its next route sync), no edge compression, clearance cookies are per host , no plan rule limits, and WAF IP lists / rate limits / managed rules are aven additions.

## Enterprise access controls (access groups, environment RBAC, guardrails)

Access groups, environment RBAC, and guardrails, always available on a self-hosted server. Every rule is enforced by the server, so the same checks apply to the dashboard, CLI and API. Changes are written to the audit log (`access_group.*`, `access_groups.enforcement`, `guardrail.*`, `environment.restricted`).

### Access groups

A group belongs to one workspace and has one project role (`admin|member|viewer`), a set of people and a set of projects. You can create groups while enforcement is off; they grant nothing until `access_groups_enabled` is on. Once enabled, a group grant is added on top of direct project membership, and the highest role wins. That means enabling groups never takes away access anyone already has. Workspace owners/admins can still see every project. A group `admin` is a project admin only: it gets no workspace administration and does not bypass guardrails. API tokens are capped by their creator's effective role, including roles that come from groups. Group members also receive project notifications.

- `GET /access-controls` (instance admin) → `{ access_groups_enabled, restrict_generated_domains, restrict_tcp_proxy, restrict_deployment_sources: boolean; approved_github_orgs: string[] }`
- `PATCH /access-controls` (instance admin, session only) with any subset of those fields → the same object. `approved_github_orgs` holds GitHub organization names (≤100, lower-cased). `400` if `restrict_deployment_sources` would be on with no approved organizations, which also means the last organization can't be removed while the policy is on.
- `GET /access-controls/policies` (any user) → the four booleans plus `instance_admin` (`workspace_admin` is a deprecated alias here; no organization list), so UIs can show banners and disable blocked actions.
- `GET /workspaces/{wsid}/access-groups` (workspace admin) → `AccessGroup[]` (`{ id, workspace_id, name, role, created_at, user_ids, project_ids }`)
- `POST /workspaces/{wsid}/access-groups` / `PUT /workspaces/{wsid}/access-groups/{gid}` (workspace admin, session only) `{ name, role, user_ids, project_ids }` → `201`/`200` `AccessGroup`. Names are unique within the workspace (case-insensitive, `409`). Unknown or cross-workspace IDs → `400`.
- `DELETE /workspaces/{wsid}/access-groups/{gid}` → `204`. Direct project permissions are not affected.
- `GET /projects/{pid}/access-groups` (project viewer) → `{ enabled, groups: {id,name,role,user_count}[], group_grants: {user_id,group_id,group_name,role}[] }`
- `PUT /projects/{pid}/access-groups` (workspace admin, session only) `{ group_ids }` sets which groups the project belongs to (the project's "Add to group").

`GET|PATCH /workspaces/{wsid}/access-group-settings` controls `{access_groups_enabled}` per workspace. The old `/access-groups` routes address `ws_default` only; the legacy enforcement field in `/access-controls` also refers to `ws_default` and changing it requires default-workspace admin in addition to instance admin.

### Environment RBAC (restricted environments)

`PATCH /environments/{eid} {"restricted": true}` (project admin) restricts an environment. `Environment.restricted` is returned everywhere environments are listed. Only project admins can reach a restricted environment's resources, and that includes group admins and workspace admins. A project admin's token counts too, so a project admin's environment token with the `member` role keeps working. Other project members can still see the environment in `GET /projects/{pid}`, but every environment-scoped route returns `403` for them: canvas, instances, variables, logs, metrics, deployments, volumes, backups, domains, data, exec and sync. People outside the project still get `404`. Members also can't copy a restricted environment's settings into a new environment (`copy_from`) or sync from it (`from`), and they can't create a service with `environment_id` set to a restricted environment. An environment forked from a restricted one is restricted too, including PR previews. Git push (webhook) deployments still run.

### Guardrails

| Policy | Restricts | Applies to |
|---|---|---|
| `restrict_generated_domains` | Generating domains under `AVEN_BASE_DOMAIN` (`POST …/domains` without a host, hosts under the base domain, new generated entries in `PATCH …/instances/{eid}` `domains`, compose apply) | Everyone except instance admins with project access (workspace/project/environment tokens do not bypass the policy) |
| `restrict_tcp_proxy` | Creating TCP proxies (`POST …/tcp-proxy` when none exists, compose apply) | Everyone except instance admins with project access |
| `restrict_deployment_sources` | Any source that isn't a `github.com` repo owned by an approved organization | Everyone, including admins |

While a public networking policy is on, automatic creation stops for everyone: templates, new databases and new or forked environments get no generated domain or TCP proxy. Custom domains and private networking still work.

The deployment source policy is checked when a service is created with a source, when a source changes (`repo_url`/`image` in `PATCH …/instances/{eid}`, environment sync, compose import, template deploy, PR previews), and when any new deployment is created (deploy, redeploy, rollback, git push, function save). It blocks Docker images, functions, CLI uploads (`aven up`), other repositories (including personal ones) and non-GitHub hosts. Built-in databases are allowed only with their official image (any tag). Services with no source and disconnecting a source (`repo_url: null`) are always allowed. Existing deployments keep running. Blocked requests return `403` with a reason.

Limitations: repositories are matched by owner name, not GitHub organization ID, so renaming an organization requires updating the allowlist. There is no organization picker backed by the GitHub app. Identity-aware proxy authentication is not implemented. Project-level activity events still list event titles from restricted environments.

### Sandboxes

Ephemeral isolated Linux containers scoped to an environment: create one, run commands and move files in and out, fork or checkpoint its filesystem, destroy it. They run as Docker containers on a Aven node (role `sandbox`, label `keel.sandbox=<id>`, name `keel-sbx-<id>`), through the same local driver / `aven-agent` as services. Tables `sandboxes` and `sandbox_checkpoints` (migration `migrateSandboxes`, cascade with the project/environment). Permissions: list/show need project viewer; everything else (including reading files, which runs a command) needs project member.

`Sandbox` = `{id, project_id, environment_id, node_id, container_name, network_mode: "isolated"|"private", image, status: "creating"|"running", memory_mb, cpu_cores, idle_timeout_minutes, expires_at|null, last_activity_at, parent_id|null, checkpoint_name|null, created_at}`. `SandboxCheckpoint` = `{id, project_id, environment_id, node_id, name, created_at}`.

| Endpoint | |
|---|---|
| `GET /environments/{eid}/sandboxes` | Newest first |
| `POST /environments/{eid}/sandboxes {image?, checkpoint?, node_id?, idle_timeout_minutes?, memory_mb?, cpu_cores?, network?, variables?}` | `201 Sandbox` once the container is running. `image` default `ubuntu:24.04` (any image with `/bin/sh`; `keel/*` is reserved). `checkpoint` = name or id (not combined with `image`; runs on the checkpoint's node). `node_id` default the server's node (must be schedulable, remote nodes online). `idle_timeout_minutes` default 30, `0` = never, max 10080. `memory_mb` 128..16384 (default 1024), `cpu_cores` 0.1..16 (default 1). `network`: `isolated` (default, own bridge network: outbound only, no route to services or the proxy) or `private` (joins the environment network; services reach it as `sbx-<id-with-dash>`). `variables` (≤ 64, ≤ 64 KiB) are set on the container for every command; they are never returned by the API. At most 50 sandboxes per environment (`409`) |
| `GET /sandboxes/{sbid}` / `DELETE /sandboxes/{sbid}` | Show / destroy (`204`; removes the container, its isolated network and, for forks, its private image). If the remote node is offline the record is removed and the container is deleted when the node reconnects |
| `POST /sandboxes/{sbid}/heartbeat` | Reset the idle timer without running anything → `Sandbox` |
| `POST /sandboxes/{sbid}/exec {cmd?: string[], command?: string, cwd?, env?, stdin?, timeout_s?, max_output?}` | `cmd` runs as argv, `command` as `sh -lc`. `stdin` base64 (≤ 256 KiB), `env` ≤ 32 entries layered over the container's variables, `cwd` absolute (fails if missing), `timeout_s` 1..110 (default 110; the command gets SIGTERM then SIGKILL via `timeout(1)` when the image has it), `max_output` stdout cap (≤ 2 MiB; stderr ≤ 64 KiB). → `{stdout, stderr (base64), exit_code, truncated, timed_out}`; a non-zero exit is still `200`. Timed out → `timed_out: true, exit_code: -1` |
| `GET /sandboxes/{sbid}/files?path=/abs` | `{path, content_base64, size, truncated}` (≤ 2 MiB). Missing file `404` |
| `PUT /sandboxes/{sbid}/files {path, content_base64, mode?}` | Create/overwrite (≤ 256 KiB), creating parent directories; mode default `0644` → `{path, size}` |
| `GET /sandboxes/{sbid}/files/list?path=/dir` | `{path, entries: [{name, path, type: file|dir|symlink, size, mod_time}], truncated}` (uses `stat -c`) |
| `GET /sandboxes/{sbid}/shell?cmd=&cols=&rows=` | Interactive shell WebSocket, same protocol and limits as the service shell. Keeps the sandbox alive while connected. Event `sandbox.shell` |
| `POST /sandboxes/{sbid}/fork {idle_timeout_minutes?, memory_mb?, cpu_cores?, network?, variables?}` | `docker commit` of the running source into `keel/sandbox:<new-id>`, then a new sandbox from it on the same node (`parent_id` set). Files are kept, processes are not; settings and variables are not inherited |
| `POST /sandboxes/{sbid}/checkpoints {name}` | Save the running sandbox's filesystem (`docker commit`, the container is paused for the commit) as a named checkpoint. Names: 1–64 of `a-z 0-9 . _ -`. Reusing a name replaces the snapshot (same id; the old image is removed). At most 50 per environment |
| `GET /environments/{eid}/sandbox-checkpoints` | Newest first |
| `PATCH /sandbox-checkpoints/{cpid} {name}` / `DELETE /sandbox-checkpoints/{cpid}` | Rename (`409` if taken) / delete with its image |

Lifecycle: every exec, file call, heartbeat and open shell updates `last_activity_at`, and `expires_at = last_activity_at + idle_timeout_minutes`. `aven-server` checks every minute and destroys expired sandboxes (event `sandbox.expire`); every 5 minutes it also removes sandbox containers that have no record (orphans) on the local node and online agents, and records stuck in `creating` for more than 10 minutes. Deleting an environment or project destroys its sandboxes and checkpoints first. Events: `sandbox.create`, `sandbox.fork`, `sandbox.checkpoint`, `sandbox.destroy`, `sandbox.expire`, `sandbox.shell`.

Agent protocol: `network.ensure` accepts `keel-sbx-<id>` networks only with matching `keel.sandbox`/project/environment labels and does not attach `keel-proxy` to them; `container.run` and `exec.run`/`exec` accept role `sandbox`; new op `sandbox.commit {name, image}` (image must be `keel/sandbox:*`, container must be a running Aven sandbox). Older agents reject fork/checkpoint on their node.

Current limitations: sandboxes are containers (Docker isolation, not VMs); exec is a single HTTP request (no streaming output, detach/reattach sessions or writable stdin) limited to 110 s; no templates, public sandbox domains, port forwarding or `${{ }}` reference resolution in `variables`; checkpoints are images on one node, so restoring is pinned to that node. A TypeScript client is in `sdk/sandbox/` (`sdk/sandbox/README.md`). Dashboard: **샌드박스** in the left rail (list, create, detail with terminal, command runner, file browser, fork, checkpoints, destroy).

### Database high availability
Convert one database instance (postgres, redis, mongo) into a replicated cluster in **one environment**. The root service is adopted in place (data, volume, service ID and connection variables survive); new member services exist only in that environment. Member nodes are spread over schedulable, connected nodes (least-used first, the root's node counts as used).

| Engine | Cluster | Failover | Stable endpoint (private network) |
| --- | --- | --- | --- |
| postgres | root + streaming replicas (`pg_basebackup`, `wal_keep_size=1GB`) + HAProxy edge | **Aven control plane**: probes every node every 10 s; when the primary fails 3 probes in a row (30 s) and auto failover is on, the healthy replica with the highest WAL position is promoted | `${{<root>-ha.DATABASE_URL}}` (HAProxy routes to the primary only; Aven repoints and redeploys it) |
| redis | root + replicas, a Sentinel in every data node (odd voter count) + HAProxy edge | Sentinel (majority vote); Aven records the new primary | `${{<root>-ha.REDIS_URL}}` (HAProxy `tcp-check` on `role:master`) |
| mongo | replica set (`--replSet`, keyfile); Aven runs `rs.initiate`/`rs.add`/`rs.remove` one change at a time | MongoDB elections | `${{<root>.MONGO_URL}}` now lists every member with `replicaSet=` (drivers find the primary) |

Endpoints (`R` = viewer, others member):
- `GET /services/{sid}/instances/{eid}/ha` → `{eligible, problems: string[], companion?, cluster}`. Works for the root and for any member. `companion` = `{key, name, description, version, supported_versions, selectors: [{role, label, nodeLabel, description, options, default}]}`. Conversion requires (Prerequisites): a supported image repository (official `postgres` / `redis` / `mongo`), a numeric version tag in `supportedImageMajorVersions` (`:latest` refused; redis needs `major.minor`), no custom start command (the template default is fine), one replica, a data volume, and an online deployment.
- `POST …/ha {replicas?, internal?, edge?, auto_failover? = true, skip_backup? = false}` → `202 {cluster, migrated_services}`. Counts must be selector options (defaults otherwise). Aven creates the members, sets the root's start command/private ports/wiring variables (old values are saved for revert), moves an existing TCP proxy to the edge, rewrites `${{<root>.KEY}}` → `${{<edge>.KEY}}` for the edge's variable keys in other services and shared variables of the environment (staged: redeploy them), backs up the root volumes, then deploys root → replicas → edge. `409 {error, problems}` when not eligible or already a member.
- `PATCH …/ha {auto_failover?, replicas?}` → `{cluster}`. Scaling adds replicas (deployed at once) or removes the highest-numbered non-primary replicas (volumes kept) and rewires the cluster.
- `POST …/ha/switchover {to}` (`to` = member service id or name) → `202 {cluster}`. "Make Leader": postgres redeploys the old primary as a standby (clean shutdown, `rejoin=standby`), waits until it stopped being primary, then promotes the target by redeploying it as `role=primary`; redis lowers the target's `replica-priority` and sends `SENTINEL FAILOVER`; mongo raises the target's priority (priority takeover).
- `DELETE …/ha` → `204`. Revert to standalone, only while the original root is the primary (`409` otherwise). Deletes replica/edge/internal services but **keeps their volumes** (detached; a later conversion suffixes new volume names), migrates references back, restores the root's start command/variables/TCP proxy and redeploys it. Mongo is first force-reconfigured to a one-member set.
- `GET /environments/{eid}/ha` (R) → clusters of the environment (canvas badges).

Cluster view: `{id, kind, status: provisioning|active|switching, auto_failover, keel_failover, busy, message, primary_service_id, root_service_id, counts, replica_options, endpoint: {service_id, name, private_host, url_variable}, members: [{service_id, name, role: root|replica|internal|edge, node_id, instance_status, db_role: primary|replica|uninitialized|recovering|unknown, primary, healthy, streaming, lag_bytes?, error?, checked_at?}]}`. Probes run inside the member containers with the one-shot exec of the data browser (`psql`, `redis-cli --raw INFO replication`, `mongosh db.hello()`); secrets go through environment variables only.

Fencing: if a postgres node that is not the recorded primary reports itself primary while the recorded primary is healthy (e.g. a node came back with its old container), Aven redeploys it; its wiring says `role=replica, rejoin=reclone`, so it moves its data dir aside (`$PGDATA.demoted-<time>`, never deleted) and re-clones from the new primary.

Events: `database.ha.converted`, `database.ha.failover`, `database.ha.switchover`, `database.ha.scaled`, `database.ha.reverted`.

**Templates.** HA is declared in template metadata with a two-template model (`internal/catalog/ha.go`): the standalone root service names `haTemplateCode`; the companion's services all declare `clusterRole` (`root`, `replica`, `internal`, `edge`, one prototype per role, copied N times), and its root declares `revertsToTemplateCode`, `haConversionConfig {description, replica|internal|edge: {label, nodeLabel, description, options, defaultValue}, supportedImageMajorVersions, pinToMinorVersion, supportedImages}` and `clusterWiring`. Wiring fields: `internalNodeNameVariable`, `internalNodesVariable`/`internalNodeFormat`, `coordinatorHostsVariable`/`coordinatorPort`/`coordinatorHostFormat`, `replicaNodeNameVariable`, `dataNodesVariable`/`dataNodeFormat`, plus Aven's `roleVariable`, `primaryHostVariable`/`primaryHostFormat`, `peerHostsVariable`/`peerHostFormat`, `quorumVariable`, `rejoinVariable`, `edgePrimaryOnly`. Formats take `{host}` (→ `${{<member>.AVEN_PRIVATE_DOMAIN}}`), `{name}`, `{rootName}`, `{port}` and must not contain references. Data-node members use the root's exact image. Built-in companions: `postgres-ha`, `redis-ha`, `mongo-ha` (hidden from the template list; deploying them directly returns `400`). User templates can publish their own companions.

Gaps: MySQL Group Replication is not implemented (`problems` says so). Postgres has no etcd/Patroni quorum: the single Aven control plane decides failover, so a control-plane outage pauses automatic failover (clusters keep serving). Creating a new environment from one with a cluster copies the member services without a cluster record. Renaming members after conversion breaks MongoDB replica-set host names. Replica volumes are not resized/limited automatically.

## Networking

Settings > Networking provides domain status and ownership checks, per-node ingress, TCP proxy on nodes with a public address, editable private names and an outbound IPv6 toggle. Existing domains remain supported.

### Networking overview
- `GET /services/{sid}/instances/{eid}/networking` (viewer, read-only) → everything the Settings > Networking page needs in one call:
  ```ts
  { detected_ports: { port: number; source: "listening" | "expose" | "env" | "loopback" }[];
    port_detection: { source: "proc" | "unavailable"; reason?: string; node?: string; containers: number };
    effective_port: number;
    domains: Domain[];
    tcp_proxy: { enabled: boolean; host: string; public_port: number | null; container_port: number | null; available: boolean; reason?: string };
    private: { dns_name: string; fqdn: string; aliases: string[]; ipv4: string[]; ipv6: string[]; mesh_ports: number[] };
    outbound: { ipv6_enabled: boolean; node_ipv6_egress: boolean | null; egress_reason?: string;
                enforce_supported: boolean | null; enforce_reason?: string; node: string;
                static_ips: { ipv4: string | null; ipv6: string | null } } }
  ```
- `detected_ports` merges the TCP ports the running containers listen on, the image `EXPOSE` ports and a user-set `PORT` variable; one entry per port, ordered `listening`, `expose`, `env`, then `loopback` (bound only to 127.0.0.1/::1, which the proxy cannot reach). A real socket wins over config when the same port appears twice.
- Listening ports are read from the host via `/proc/<container pid>/net/tcp{,6}` (no exec, works on distroless images; aven-server and aven-agent run as root). Remote nodes use the agent op `ports.listening {instance_id}`; agents that do not know it (or offline/slow nodes, 3 s budget) give `port_detection.source = "unavailable"` with a reason, and the rest of the response is unchanged.
- `effective_port` is the port the proxy routes to when a domain has no `target_port` (same rule as routing).
- `private` and `outbound` are described below. `outbound` is read from the node that runs the containers (the home node if nothing runs), with a 2 s budget; on timeout the booleans are `null` and the `*_reason` fields say why.

### Domain status engine
- A background checker (`internal/netcheck`) re-checks every domain every 60 s; verified domains back off up to 10 min. Results are stored per host in `domain_states` (which is also the host claim: two services cannot add the same host, even concurrently). The API reads stored results only, so `GET` never blocks on DNS.
- One check: resolve A, AAAA and CNAME (a wildcard `*.x` is checked via `aven-dns-check.x`), classify the answer, then look for proof of ownership:
  - `proxy: "direct"`: the records point at a serving node's address or its `ingress_hostname` → `status: "active"`.
  - `proxy: "cloudflare"`: the records are in Cloudflare's edge ranges (orange cloud) → `status: "proxied"`. Before this redesign these were reported as `pending_dns` although they worked.
  - `proxy: "tunnel"`: the serving node is a Cloudflare tunnel node, or the CNAME points at `*.cfargotunnel.com` → `status: "proxied"`.
  - `proxy: "none"`: no usable records → `status: "pending_dns"`. Lookup failures → `status: "error"` with `last_error`.
- `verification.method` is how ownership was first proven and is then kept: `legacy` (the host existed before the redesign; all 18 production domains), `dns` (direct), `http` (behind a proxy, the check route below answered with the token) or `txt` (`_aven-verify.<host>` TXT contains `aven-verify=<token>`). The token is `HMAC(master key, "domain:"+host)`, so `verification.txt` is stable and can be added before the DNS switch. `""` means not verified yet.
- `dns[]` holds the records to set, each with `status` (`ok` | `proxied` | `mismatch` | `pending`) and `current` (the values seen). For a node with `ingress_hostname` the recommended record is a `CNAME` to it, with `A`/`AAAA` listed as `alternative: true` (apex hosts get a note about CNAME flattening). For a tunnel node the record is the tunnel CNAME with a note. A node without IPv4 adds a warning that IPv4 clients need the Cloudflare proxy.
- `cert` is the certificate seen on `https://<host>` (not checked for `tls: "off"` domains). `POST …/domains/{host}/verify` runs a check now (30 s budget) and returns the updated `Domain`.

### Domain check route and ACME gating
- Every service domain route answers `GET /.well-known/aven/domain-check` with `aven-domain-check=<token>` (plain text). It sits before basic auth, redirects, edge rules and path 404s, on both the TLS and the plain HTTP server, so it works behind Cloudflare and tunnels and for `upstream` hosts with `upstream_paths`. Agents that reject the new route field get the routes again without it (that node is remembered), so mixed versions keep serving.
- **ACME gating applies to new custom domains only.** `legacy` and verified hosts stay in Caddy's managed ACME policy exactly as before. A new, unverified `tls: auto` custom host is put on the on-demand policy instead, and the on-demand `ask` answers `200` only when the host is verified or its DNS points at this node (`status: "active"`). Hosts without a claim row are allowed as before. This stops aven from requesting certificates for names nobody pointed at it (Let's Encrypt rate limits). Generated domains (below `AVEN_BASE_DOMAIN`) are operator-owned and never gated.

### Generated domain rename
- `PATCH /services/{sid}/instances/{eid}/domains/{host}` `{host: "<label>.<AVEN_BASE_DOMAIN>"}` renames a **generated** domain. The suffix cannot change and the label must be one DNS label (`[a-z0-9-]`, ≤ 63). Spec, basic auth hash and the claim move in one transaction. Custom domains cannot be renamed (`400`; add the new one and delete the old one). `basic_auth` cannot be changed in the same request. A taken host is `409`. `target_port` may be sent together with `host`.

### Node ingress
- `Node.ingress_mode` is `direct` (default) or `cloudflare_tunnel`; `ingress_hostname` is the name custom domains should CNAME to (e.g. `main.edge.keel.example.com`); `tcp_hostname` is the host shown for TCP proxies on that node. Set them with `PATCH /nodes/{nid}`; `""` or `null` clears a value (`ingress_mode` back to `direct`). Hostnames are validated and wildcards are rejected.
- For the local node the server's environment overrides the stored values when set: `AVEN_INGRESS_MODE`, `AVEN_INGRESS_HOSTNAME`, `AVEN_TCP_PROXY_DOMAIN` (→ `tcp_hostname`).
- New domains on a `cloudflare_tunnel` node are created with `tls: "off", trust_forwarded_proto: true` explicitly (the tunnel terminates TLS; otherwise it loops on 308). Existing domains are not changed.

### TCP proxy
- `GET …/tcp-proxy` → `{enabled, host, public_port, container_port, available, reason?}`. `host` is, for the local node, `AVEN_TCP_PROXY_DOMAIN` → node `tcp_hostname` → the previous default; for a remote node, its `tcp_hostname` → its public address (IPv6 first).
- `POST …/tcp-proxy` `{container_port}` opens a proxy (or changes the port of an existing one). On a node without a public address (Cloudflare tunnel node, or no address) opening a new proxy returns **`409` "이 노드는 공개 IP가 없어 TCP 프록시를 열 수 없습니다"**; `available` is `false` with the same `reason`. An existing proxy can still be retargeted.
- `PATCH …/tcp-proxy` `{container_port}` changes only the container port; the public port and host stay the same. `404` if no proxy is enabled.
- Public ports come from `AVEN_TCP_PORT_MIN..MAX` (20000-29999) and are unique across all nodes. For a remote node the server asks the agent whether the candidate is free (agent op `port.free`, at most 64 candidates); agents that do not know the op get the first unused candidate as before.
- **Firewall caveat**: the proxy is a published Docker port, and Docker's publish rules bypass ufw. A proxy port is reachable from the whole internet (IPv4 and IPv6) regardless of ufw. Restrict it with `DOCKER-USER` rules if needed; the optional `AVEN_TCP_PORT_ALLOW_CIDRS` from the design is not implemented.

### Private DNS names
- Inside an environment network a service always resolves as `<service>` and `<service>.<env>.aven.internal`. `PATCH /services/{sid}/instances/{eid}` `{private_dns_name: "<name>"}` adds `<name>` and `<name>.<env>.aven.internal`. It is a staged change (`pending_changes` contains `private_dns_name`) that applies on the next deploy; `""` or the service name resets it. `private_domain` and the injected `AVEN_PRIVATE_DOMAIN` use the custom name. Cross-node mesh imports carry the extra aliases (older agents get them without aliases).
- Name rules: lowercase letters, digits and `-`, no leading/trailing `-`, ≤ 40 characters; reserved names (`shared`, `localhost`, `local`, `aven`, `host`, `gateway`, `internal`, `docker`, …) and the prefixes `keel-` and `sbx-` are rejected. The name must not collide, inside the environment, with another service's name, its `private_dns_name` (configured or live) or its compose aliases. Errors are `400 "private_dns_name: <reason>"`.
- `GET /services/{sid}/instances/{eid}/private-dns/available?name=` → `{available, reason?}` (viewer). The service's own name and `""` are always available.
- `GET /environments/{eid}/private-dns/available?name=` → `{available, reason?}` (viewer): the same check without a service, so every service name in the environment counts as taken and `""` is unavailable. Used by GraphQL `privateNetworkEndpointNameAvailable` without `serviceId`.
- `networking.private`: `dns_name` (the custom name or the service name), `fqdn`, `aliases` (all names that resolve, from the configured value), `ipv4`/`ipv6` (addresses of the first replica on the environment network; empty when nothing runs) and `mesh_ports` (ports open to other nodes). The environment network is dual-stack, but cross-node mesh forwarding is IPv4 only.

### Outbound IPv6
- **Status** (`networking.outbound`): `node_ipv6_egress` is whether containers of this environment on that node can reach IPv6 destinations: the environment network has an IPv6 subnet, running containers have an IPv6 default route, the host forwards IPv6 and has a default route, and a ULA subnet has Docker's NAT66 rule. `false` when one of these is definitely missing (with `egress_reason`), `null` when it could not be read (old agent, offline node, timeout). Nothing is sent to the outside; node results are cached for 30 s. `static_ips` are the node's public addresses.
- **Toggle**: `PATCH /services/{sid}/instances/{eid}` `{outbound_ipv6: false | true | null}`. Unlike most settings it is applied immediately (not staged, not in `pending_changes`); `null`/`true` restore the default (on, not stored). Turning it off returns `409` when any node the service may run on cannot enforce it (`enforce_supported: false`: no ip6tables or no `DOCKER-USER` chain, old agent, offline node), with the node and reason in the message. Turning it on always succeeds.
- **Enforcement**: for each container of the service the node adds one rule to the ip6tables `DOCKER-USER` chain: source `<container IPv6>/128`, destination not `fc00::/7`, conntrack state not `RELATED,ESTABLISHED` → `REJECT` (`icmp6-adm-prohibited`), commented `keel:egress6:<instanceID>`. aven only adds and removes rules with that comment; it never flushes or creates the chain and leaves Docker's and other tools' rules alone. Rules are applied right after the PATCH and reconciled every 30 s (new replicas, redeploys, node restarts).
- **Limits**: only new outbound IPv6 connections are blocked. IPv4 egress, IPv6 inside the environment, the mesh and other ULA destinations (`fc00::/7`) are unaffected, as are inbound connections and their replies. Containers that are not on the environment network (host networking) are not covered. Between a replica starting and the next reconcile (≤ 30 s) a new container may briefly have IPv6 egress.

### Per-domain IPv6
- A node can have an IPv6 pool (`Node.ipv6_pool`, CIDR). Every public domain served by that node then gets its own `/128` from the pool instead of sharing the node's single IPv6 (`Domain.ipv6`). Nodes without a pool behave exactly as before.
- **Set the pool**: `PATCH /nodes/{nid}` `{ipv6_pool: "2001:db8:1:d::/80"}` (instance admin). `""`/`null` removes it. Validation:
  - `400`: not an IPv6 CIDR, not global unicast (ULA, link-local and multicast are rejected), size outside `/64`..`/120`, or not written as the network address.
  - `409`: the pool contains the node's main public IPv6, contains an address that is already on the host and was not assigned by aven, overlaps a host subnet smaller than `/64` (e.g. a WireGuard `/112`), or overlaps another node's pool. The host is asked live (local: `ip -j -6 addr`; remote: agent op `ipv6.addrs`); an offline node or an agent without the op gives `409` too.
  - For the local node `AVEN_IPV6_POOL` overrides the stored value (and `AVEN_IPV6_IFACE` picks the interface; default: the one holding `AVEN_PUBLIC_IPV6`, else the IPv6 default route's).
- **Allocation**: the serving node is the instance's home node (the node of the current deployment, else `node_id`). Addresses are allocated in one store transaction, lowest free first, skipping the pool's network address and `::1` (so the first domain gets `<pool>::2`). They stay stable across restarts and renames, are released when the domain is deleted, and are re-allocated from the new node's pool when the service moves. Every host in `domain_states` is covered: custom, generated and `upstream` domains, including legacy ones. A full pool leaves the extra domains on the shared address. `POST …/domains` returns the new domain with `ipv6` already set.
- **On the host**: aven runs `ip -6 addr replace <addr>/128 dev <iface> preferred_lft 0 valid_lft forever nodad` for every assigned address and `ip -6 addr del` for aven addresses in the pool that are no longer assigned. `preferred_lft 0` (deprecated) keeps the host from using the address as an outbound source. Addresses outside the pool are never touched. Inside the pool only `/128` deprecated addresses are removed; anything else is reported in `ipv6_status.foreign`. keel-proxy is published on `[::]:80/443`, so a new address is served by Caddy (SNI) without any proxy or container change. Reconciled at startup, after domain changes and deploys, on agent reconnect and every 60 s (systemd-networkd may drop foreign addresses when it reconfigures). Changing or removing a pool also removes aven's addresses from the old pool.
- **Remote nodes**: agent op `ipv6.ensure {iface?, pool, addresses[]}` (the agent re-checks the pool and that every address is inside it, and refuses a pool containing its own public IPv6; `--ipv6-iface`/`AVEN_IPV6_IFACE` on the agent wins over `iface`). Agents that do not know the op are skipped and reported as `ipv6_status.supported: false`. Replicas on other nodes are not given addresses; their node addresses are listed as extra `AAAA` records.
- **DNS records**: with an address the recommended `AAAA` is that address (`A` stays the node IPv4). With an `ingress_hostname` the CNAME stays the recommended record and the dedicated `AAAA` is the alternative. The status engine treats the dedicated address and the node's default addresses as "this node", so DNS that still points at the shared address stays `active`; that `AAAA` record shows `status: "ok"` with the note "지금은 노드 공용 IPv6를 가리킵니다(정상 동작). 전용 IPv6로 바꾸면 도메인별 주소를 씁니다." GraphQL `dnsRecords` and `GET …/networking` show the same records.

### GraphQL and CLI
- GraphQL: `customDomain.status { dnsRecords { currentValue status } cdnProvider certificateStatus }`, `tcpProxies`/`tcpProxyCreate`/`tcpProxyDelete`, `privateNetworkEndpoint`, `privateNetworkEndpointNameAvailable`, `privateNetworkEndpointRename`, `serviceInstance.ipv6EgressEnabled` and `serviceInstanceUpdate(input: {ipv6EgressEnabled})` (docs/graphql.md).
- CLI: `aven domain list|status|port|verify|rename|rm`, `aven tcp-proxy [status|enable|port|disable]`, `aven networking` (docs/cli.md).
