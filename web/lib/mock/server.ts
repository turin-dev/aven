/* In-memory fake aven v1 backend used only by `next dev` with NEXT_PUBLIC_AVEN_MOCK=1. */

import type {
  AvenNode,
  DetectedPort,
  DnsRecord,
  InstanceNetworking,
  NodeMetricPoint,
  Backup,
  CanvasService,
  CronRun,
  DatabaseKind,
  Deployment,
  DeploymentStatus,
  DeploymentTrigger,
  Domain,
  Environment,
  AvenEvent,
  MetricPoint,
  MetricRange,
  PREnvironmentSettings,
  Project,
  Service,
  ServiceInstance,
  SourceType,
  Template,
  TemplateDetail,
  TemplateSaveInput,
  TemplateService,
  Volume,
  DataCell,
  DataQueryResult,
  DataRows,
  SyncInclude,
  SyncPlan,
  SyncResult,
  SyncServiceDiff,
  SyncVarChange,
  ApiToken,
  AuditEntry,
  Invite,
  Me,
  Member,
  ProjectRole,
  SessionInfo,
  TokenScope,
  User,
  WorkspaceRole,
  Workspace,
  WorkspaceMember,
  ResourceLimits,
  WorkspaceResources,
  InstanceOverview,
  ProjectGithubAccount,
  AlertRule,
  AlertRuleInput,
  NotificationChannel,
  NotificationChannelInput,
  NotificationDelivery,
  UsageGroup,
  UsageSeriesPoint,
  UsageTotals,
  UserNotification,
} from "../api";
import { SEALED_VALUE } from "../api";
import { parseRaw, renderRaw } from "../dotenv";
import type { AdminCapID, AnnouncementLevel, CanvasGroup, AvenBackup, UsageLimit, UsageSuspension } from "../api";
import type { Dashboard, Widget } from "../layouts";
import type { BackupDestination, BackupDestinationInput } from "../api";
import type { PostgresCheck, PostgresPooling, PostgresPoolingInput, PostgresUpgrade } from "../postgres";
import type { EdgeCDN, EdgePatch, EdgeRuleset, EdgeSettings, EdgeWAF } from "../edge";

/* ── helpers ─────────────────────────────────────────────────────────── */

const now = () => Date.now();
const iso = (t: number) => new Date(t).toISOString();
const ago = (min: number) => iso(now() - min * 60_000);
let seq = 1000;
const rid = (p: string) => `${p}_${(seq++).toString(36)}${Math.random().toString(36).slice(2, 7)}`;
const sha = () => Array.from({ length: 40 }, () => "0123456789abcdef"[Math.floor(Math.random() * 16)]).join("");
const pick = <T,>(a: T[]) => a[Math.floor(Math.random() * a.length)];
const hms = (t: number) => new Date(t).toISOString().slice(11, 19);

function hash(s: string) {
  let h = 2166136261;
  for (const c of s) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0;
  return h;
}

export class HttpError extends Error {
  constructor(
    public status: number,
    message: string,
    /** Extra fields merged into the error body (structured errors such as template input problems). */
    public extra?: Record<string, unknown>,
  ) {
    super(message);
  }
}

/* ── state ───────────────────────────────────────────────────────────── */

type Store = {
  projects: Project[];
  envs: Environment[];
  services: Service[];
  instances: ServiceInstance[];
  deployments: Deployment[];
  volumes: Volume[];
  backups: Backup[];
  shared: Record<string, Record<string, string>>;
  events: AvenEvent[];
  cronRuns: CronRun[];
  buildLogs: Record<string, string[]>;
  runtimeLogs: Record<string, { t: string; replica: string; stream: "stdout" | "stderr"; msg: string }[]>;
};

const S: Store = {
  projects: [],
  envs: [],
  services: [],
  instances: [],
  deployments: [],
  volumes: [],
  backups: [],
  shared: {},
  events: [],
  cronRuns: [],
  buildLogs: {},
  runtimeLogs: {},
};

const NODES: AvenNode[] = [
  {
    id: "node_local",
    name: "local-node",
    is_local: true,
    status: "online",
    schedulable: true,
    address: { ipv4: "192.0.2.10", ipv6: "2001:db8:1::10", private: "100.64.0.3" },
    ipv6_pool: "2001:db8:1:d::/80",
    version: "1.2.0",
    docker_version: "28.4.0",
    cpu_cores: 12,
    memory_bytes: 64e9,
    disk_free_bytes: 182.4e9,
    labels: { region: "region-a", role: "main" },
    last_seen_at: new Date().toISOString(),
    created_at: new Date(Date.now() - 40 * 864e5).toISOString(),
  },
  {
    id: "node_database",
    name: "database-node",
    is_local: false,
    status: "online",
    schedulable: true,
    address: { ipv4: "192.0.2.11", ipv6: "2001:db8:3::1", private: "100.64.0.7" },
    version: "1.2.0",
    docker_version: "28.4.0",
    cpu_cores: 4,
    memory_bytes: 16e9,
    disk_free_bytes: 71.2e9,
    labels: { region: "region-a", disk: "nvme" },
    last_seen_at: new Date().toISOString(),
    created_at: new Date(Date.now() - 9 * 864e5).toISOString(),
  },
  {
    id: "node_edge",
    name: "edge-node",
    is_local: false,
    status: "offline",
    schedulable: false,
    address: { ipv4: "192.0.2.12", ipv6: null, private: "100.64.0.9" },
    version: "1.1.3",
    docker_version: "27.5.1",
    cpu_cores: 2,
    memory_bytes: 4e9,
    disk_free_bytes: 18.1e9,
    labels: { region: "region-b" },
    last_seen_at: new Date(Date.now() - 3 * 3600e3).toISOString(),
    created_at: new Date(Date.now() - 20 * 864e5).toISOString(),
  },
];
// remote: no public IP, reachable only through a Cloudflare tunnel (P2 ingress_mode=cloudflare_tunnel).
NODES.push({
  id: "node_remote",
  name: "remote",
  is_local: false,
  status: "online",
  schedulable: true,
  address: { ipv4: null, ipv6: null, private: "100.64.0.1" },
  version: "1.2.0",
  docker_version: "28.4.0",
  cpu_cores: 4,
  memory_bytes: 8e9,
  disk_free_bytes: 96.3e9,
  labels: { region: "region-remote" },
  last_seen_at: new Date().toISOString(),
  created_at: new Date(Date.now() - 30 * 864e5).toISOString(),
});
/** P2 node ingress (mock): direct nodes get `<node>.edge.<base>`, tunnel nodes a cfargotunnel CNAME. */
const NODE_INGRESS: Record<string, { mode: "direct" | "cloudflare_tunnel"; hostname: string }> = {
  node_local: { mode: "direct", hostname: "node-one.edge.example.com" },
  node_database: { mode: "direct", hostname: "node-two.edge.example.com" },
  node_edge: { mode: "direct", hostname: "node-three.edge.example.com" },
  node_remote: { mode: "cloudflare_tunnel", hostname: "00000000-0000-4000-8000-000000000000.cfargotunnel.com" },
};
const registries: Record<string, { host: string; username: string; password: string }[]> = {
  prj_sample: [{ host: "ghcr.io", username: "demo-user", password: "x" }],
};

type Listener = (e: AvenEvent) => void;
const eventListeners = new Set<Listener>();
type DepListener = (d: Deployment, line?: string) => void;
const depListeners = new Map<string, Set<DepListener>>();

export function onEvent(fn: Listener) {
  eventListeners.add(fn);
  return () => eventListeners.delete(fn);
}
export function onDeployment(id: string, fn: DepListener) {
  let set = depListeners.get(id);
  if (!set) depListeners.set(id, (set = new Set()));
  set.add(fn);
  return () => set!.delete(fn);
}

function emit(e: Omit<AvenEvent, "id" | "created_at"> & { created_at?: string }) {
  const ev: AvenEvent = { id: rid("evt"), created_at: iso(now()), ...e };
  S.events.unshift(ev);
  eventListeners.forEach((fn) => fn(ev));
  return ev;
}

/* ── seed ────────────────────────────────────────────────────────────── */

const BASE = "example.com";

const DB_IMAGES: Record<DatabaseKind, string> = {
  postgres: "postgres:17-alpine",
  mysql: "mysql:9",
  redis: "redis:8-alpine",
  mongo: "mongo:8",
  bucket: "pgsty/minio:RELEASE.2026-08-04T00-00-00Z",
};
const DB_MOUNT: Record<DatabaseKind, string> = {
  postgres: "/var/lib/postgresql/data",
  mysql: "/var/lib/mysql",
  redis: "/data",
  mongo: "/data/db",
  bucket: "/data",
};
const DB_PORT: Record<DatabaseKind, number> = { postgres: 5432, mysql: 3306, redis: 6379, mongo: 27017, bucket: 9000 };

function secret(n = 24) {
  const a = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  return Array.from({ length: n }, () => a[Math.floor(Math.random() * a.length)]).join("");
}

function dbVars(kind: DatabaseKind, name: string): Record<string, string> {
  const pw = secret();
  const tcp = "${{ AVEN_TCP_PROXY_DOMAIN }}:${{ AVEN_TCP_PROXY_PORT }}";
  switch (kind) {
    case "postgres":
      return {
        PGHOST: "${{ AVEN_PRIVATE_DOMAIN }}",
        PGPORT: "5432",
        PGUSER: "postgres",
        PGPASSWORD: pw,
        PGDATABASE: "aven",
        POSTGRES_PASSWORD: "${{ PGPASSWORD }}",
        DATABASE_URL: "postgresql://${{ PGUSER }}:${{ PGPASSWORD }}@${{ PGHOST }}:${{ PGPORT }}/${{ PGDATABASE }}",
        DATABASE_PUBLIC_URL: `postgresql://\${{ PGUSER }}:\${{ PGPASSWORD }}@${tcp}/\${{ PGDATABASE }}`,
      };
    case "mysql":
      return {
        MYSQLHOST: "${{ AVEN_PRIVATE_DOMAIN }}",
        MYSQLPORT: "3306",
        MYSQLUSER: "root",
        MYSQLPASSWORD: pw,
        MYSQLDATABASE: "aven",
        MYSQL_ROOT_PASSWORD: "${{ MYSQLPASSWORD }}",
        MYSQL_URL: "mysql://${{ MYSQLUSER }}:${{ MYSQLPASSWORD }}@${{ MYSQLHOST }}:${{ MYSQLPORT }}/${{ MYSQLDATABASE }}",
        MYSQL_PUBLIC_URL: `mysql://\${{ MYSQLUSER }}:\${{ MYSQLPASSWORD }}@${tcp}/\${{ MYSQLDATABASE }}`,
      };
    case "redis":
      return {
        REDISHOST: "${{ AVEN_PRIVATE_DOMAIN }}",
        REDISPORT: "6379",
        REDISUSER: "default",
        REDISPASSWORD: pw,
        REDIS_URL: "redis://${{ REDISUSER }}:${{ REDISPASSWORD }}@${{ REDISHOST }}:${{ REDISPORT }}",
        REDIS_PUBLIC_URL: `redis://\${{ REDISUSER }}:\${{ REDISPASSWORD }}@${tcp}`,
      };
    case "mongo":
      return {
        MONGOHOST: "${{ AVEN_PRIVATE_DOMAIN }}",
        MONGOPORT: "27017",
        MONGOUSER: "mongo",
        MONGOPASSWORD: pw,
        MONGO_INITDB_ROOT_USERNAME: "${{ MONGOUSER }}",
        MONGO_INITDB_ROOT_PASSWORD: "${{ MONGOPASSWORD }}",
        MONGO_URL: "mongodb://${{ MONGOUSER }}:${{ MONGOPASSWORD }}@${{ MONGOHOST }}:${{ MONGOPORT }}",
        MONGO_PUBLIC_URL: `mongodb://\${{ MONGOUSER }}:\${{ MONGOPASSWORD }}@${tcp}`,
      };
    case "bucket":
      return mockBucketVars(pw);
  }
  void name;
}

const STANDARD_ENV = { kind: "standard" as const, pr_number: null, pr_url: null, pr_branch: null };
const PR_DEFAULTS: PREnvironmentSettings = { enabled: false, base_environment_id: null, auto_delete: true, domain: true, focused: false };

/**
 * Fills `env` with one instance per project service. With `from`, settings/variables/shared vars are copied
 * (volumes are new and empty). `preview` applies the PR-preview domain rule: one generated domain for every
 * service that had any domain in the source.
 */
function forkMockEnv(pid: string, env: Environment, from: string | undefined, preview = false) {
  const p = getProject(pid);
  S.shared[env.id] = from ? { ...(S.shared[from] ?? {}) } : {};
  for (const svc of S.services.filter((s) => s.project_id === pid)) {
    const src = from ? S.instances.find((i) => i.service_id === svc.id && i.environment_id === from) : null;
    const domains = !src
      ? []
      : preview
        ? src.domains.length && p.pr_environments?.domain !== false
          ? [genDomain(generatedHost(svc.name, env, p), src.domains[0].target_port)]
          : []
        : src.domains.filter((d) => d.kind === "generated").map(() => genDomain(generatedHost(svc.name, env, p)));
    const inst = newInstance(
      svc,
      env,
      src ? { ...clone(src), id: rid("si"), environment_id: env.id, status: "idle", current_deployment_id: null, private_domain: `${svc.name}.${env.name}.aven.internal`, tcp_proxy: null, domains } : {},
    );
    S.instances.push(inst);
    if (svc.database_kind)
      S.volumes.push({ id: rid("vol"), project_id: pid, environment_id: env.id, service_instance_id: inst.id, name: `${svc.name}-volume`, mount_path: DB_MOUNT[svc.database_kind], size_bytes: 0, created_at: iso(now()) });
  }
}

function generatedHost(svc: string, env: Environment, project: Project) {
  return env.id === project.default_environment_id ? `${svc}.${BASE}` : `${svc}-${env.name}.${BASE}`;
}

function newInstance(svc: Service, env: Environment, over: Partial<ServiceInstance> = {}): ServiceInstance {
  return {
    id: rid("si"),
    service_id: svc.id,
    environment_id: env.id,
    repo_url: null,
    branch: "main",
    root_dir: "/",
    image: null,
    builder: "auto",
    dockerfile_path: "Dockerfile",
    watch_paths: [],
    auto_deploy: true,
    start_command: null,
    pre_deploy_command: null,
    port: null,
    healthcheck_type: "http",
    healthcheck_path: "/",
    healthcheck_timeout_s: 60,
    restart_policy: "on_failure",
    restart_max_retries: 10,
    replicas: 1,
    cron_schedule: null,
    sleep_when_idle: false,
    domains: [],
    private_domain: `${svc.name}.${env.name}.aven.internal`,
    tcp_proxy: null,
    variables: {},
    status: "idle",
    current_deployment_id: null,
    canvas: null,
    updated_at: iso(now()),
    ...over,
    pending_changes: over.pending_changes ?? [],
    sleep_after_s: over.sleep_after_s ?? 600,
    overlap_seconds: over.overlap_seconds ?? 0,
    draining_seconds: over.draining_seconds ?? null,
    node_id: over.node_id ?? "node_local",
    autoscale: over.autoscale ?? null,
    placement: over.placement ?? null,
    current_replicas: over.current_replicas ?? 0,
    autoscale_status: over.autoscale_status ?? null,
  };
}

/** v1.5: 자동 확장 상태(모의): 레플리카당 CPU를 그럴듯하게 만들어 목표와 비교한다. */
function mockAutoscaleStatus(inst: ServiceInstance): ServiceInstance["autoscale_status"] {
  const a = inst.autoscale;
  if (!a?.enabled) return null;
  const cur = inst.current_replicas ?? inst.replicas;
  const cpu = 35 + Math.random() * 30;
  const desired = a.target_cpu_percent ? Math.min(a.max_replicas, Math.max(a.min_replicas, Math.ceil((cur * cpu) / a.target_cpu_percent))) : cur;
  return {
    desired,
    last_scale_at: null,
    last_reason: "",
    reason: inst.status !== "online" ? "서비스가 실행 중이 아닙니다" : desired === cur ? "목표 범위 안입니다" : "다음 평가에서 조정합니다",
    evaluated_at: iso(now()),
    metrics: { cpu, memory: 20 + Math.random() * 10, rps: Math.random() * 5 },
  };
}

function genDomain(host: string, port: number | null = null): Domain {
  return {
    host,
    kind: "generated",
    target_port: port,
    dns: [],
    status: "active",
    proxy: null,
    verification: { method: "legacy", verified_at: iso(now()), txt: verifyTxt(host) },
    cert: { status: "valid", not_after: iso(now() + 80 * 864e5) },
  };
}

/** P1 verification token (mock: a stable hash instead of HMAC(master key)). */
function verifyTxt(host: string) {
  let h = 0x811c9dc5;
  let out = "";
  for (let round = 0; round < 4; round++) {
    for (const c of `${round}:domain:${host}`) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return { name: `_aven-verify.${host}`, value: `aven-verify=${out}` };
}

const COMMITS: [string, string][] = [
  ["feat: 요청 로깅 미들웨어 추가", "aven-dev"],
  ["fix: 세션 만료 시 401 대신 500이 나던 문제", "aven-dev"],
  ["chore(deps): bump next from 16.3.1 to 16.4.2", "dependabot[bot]"],
  ["refactor: db 커넥션 풀 설정 분리", "minji"],
  ["feat(api): /v1/reports 엔드포인트", "aven-dev"],
  ["docs: README 배포 방법 갱신", "minji"],
  ["perf: 홈 화면 쿼리 캐시", "aven-dev"],
  ["fix: healthz가 DB 연결을 확인하도록", "aven-dev"],
  ["style: 린트 경고 정리", "minji"],
  ["feat: 다크 모드 토글", "aven-dev"],
];

function buildLogFor(svc: Service, inst: ServiceInstance, d: Deployment, failed?: string): string[] {
  const t0 = new Date(d.started_at ?? d.created_at).getTime();
  const L: string[] = [];
  let t = t0;
  const add = (s: string, dt = 400) => {
    t += dt;
    L.push(`${hms(t)} ${s}`);
  };
  if (svc.source_type === "repo") {
    add(`==> cloning ${inst.repo_url} (${d.branch ?? inst.branch})`, 200);
    add(`    commit ${d.commit_sha?.slice(0, 7)} ${d.commit_message ?? ""}`);
    add(`==> detected ${d.builder_used === "railpack" ? "no Dockerfile; using railpack" : "Dockerfile"}`, 300);
    add("#0 building with \"default\" instance using docker driver");
    add("#1 [internal] load build definition from Dockerfile");
    add("#1 transferring dockerfile: 612B done");
    add("#2 [internal] load metadata for docker.io/library/node:22-alpine", 900);
    add("#3 [internal] load .dockerignore");
    add("#4 [1/6] FROM docker.io/library/node:22-alpine@sha256:9f3c…", 300);
    add("#5 [2/6] WORKDIR /app");
    add("#6 [3/6] COPY package.json package-lock.json ./");
    add("#7 [4/6] RUN npm ci --omit=dev", 300);
    add("#7 12.41 added 214 packages, and audited 215 packages in 11s", 2400);
    add("#7 12.43 found 0 vulnerabilities");
    add("#8 [5/6] COPY . .");
    if (failed) {
      add("#9 [6/6] RUN npm run build", 300);
      add("#9 4.112 > billing-api@0.4.0 build");
      add("#9 4.113 > tsc -p tsconfig.json", 200);
      add("#9 9.870 src/stripe.ts(42,18): error TS2339: Property 'customerId' does not exist on type 'Invoice'.", 1800);
      add("#9 ERROR: process \"/bin/sh -c npm run build\" did not complete successfully: exit code: 2");
      add("==> build failed (exit code 2)");
      return L;
    }
    add("#9 [6/6] RUN npm run build", 300);
    add("#9 8.204 ✓ Compiled successfully", 3000);
    add("#10 exporting to image");
    add("#10 exporting layers 1.9s done", 1900);
    add(`#10 naming to aven/${svc.name}:${d.commit_sha?.slice(0, 7)} done`);
    add("==> build complete in 24s");
  } else {
    add(`==> pulling ${inst.image}`, 200);
    add(`${(inst.image ?? "").split(":")[1] ?? "latest"}: Pulling from library/${(inst.image ?? "").split(":")[0]}`);
    add("Digest: sha256:4f1b4c0a6b0b2e7c…", 1600);
    add(`Status: Image is up to date for ${inst.image}`);
  }
  if (inst.pre_deploy_command) {
    add(`==> pre-deploy: ${inst.pre_deploy_command}`, 300);
    add("    migrations: 3 applied, 0 pending", 900);
  }
  add(`==> starting ${inst.replicas} replica${inst.replicas > 1 ? "s" : ""}`, 500);
  if (inst.healthcheck_type !== "none") add(`==> healthcheck ${inst.healthcheck_type === "http" ? inst.healthcheck_path : "tcp"}`, 600);
  if (d.status === "crashed") {
    add("==> container exited with code 1 (restarts=3)", 1200);
    return L;
  }
  add("==> healthcheck OK", 900);
  add("==> 배포 완료");
  return L;
}

const RUNTIME: Record<string, string[]> = {
  hello: [
    "GET / 200 3.1ms",
    "GET /api/me 200 11.4ms",
    "GET /healthz 200 0.4ms",
    "POST /api/login 200 84.2ms",
    "GET /static/app.4f2a1c.js 304 0.8ms",
    "cache hit ratio 0.93 (last 60s)",
    "warn: slow query 412ms SELECT * FROM sessions WHERE user_id = $1",
    "GET /api/posts?page=2 200 23.9ms",
    "error: redis ECONNRESET, retrying in 200ms",
  ],
  postgres: [
    "LOG:  checkpoint starting: time",
    "LOG:  checkpoint complete: wrote 42 buffers (0.3%); 0 WAL file(s) added",
    "LOG:  connection received: host=10.0.3.4 port=51234",
    "LOG:  connection authorized: user=postgres database=aven",
    "LOG:  automatic vacuum of table \"keel.public.sessions\": index scans: 1",
    "ERROR:  duplicate key value violates unique constraint \"users_email_key\"",
  ],
  redis: [
    "1:M * 100 changes in 300 seconds. Saving...",
    "1:M * Background saving started by pid 31",
    "31:C * DB saved on disk",
    "1:M * Background saving terminated with success",
  ],
  whoami: ["Starting up on port 80", "GET / 200 from 172.18.0.1", "GET /health 200"],
  "nightly-report": [
    "report: collecting yesterday's orders",
    "report: 1,284 orders, ₩38,201,000",
    "report: posted summary to webhook",
    "report: done in 4.2s",
  ],
  "billing-api": ["listening on :8080", "error: STRIPE_KEY is not set"],
};

function runtimeFor(svc: Service, d: Deployment, n = 60) {
  const lines = RUNTIME[svc.name] ?? RUNTIME.whoami;
  const out: Store["runtimeLogs"][string] = [];
  const start = new Date(d.finished_at ?? d.created_at).getTime();
  const reps = d.replicas.length ? d.replicas.map((r) => r.name) : [`${svc.name}-1`];
  const span = Math.max(60_000, Math.min(now() - start, 3 * 3600_000));
  for (let i = 0; i < n; i++) {
    const msg = lines[(i * 7 + (hash(d.id) % 5)) % lines.length];
    out.push({
      t: iso(now() - span + (span * i) / n),
      replica: reps[i % reps.length],
      stream: /error|warn/.test(msg) ? "stderr" : "stdout",
      msg,
    });
  }
  return out;
}

type SeedSvc = {
  name: string;
  source_type: SourceType;
  database_kind?: DatabaseKind;
  icon?: string;
  template?: string;
  inst: Partial<ServiceInstance>;
  deps: { st: DeploymentStatus; min: number; trig?: DeploymentTrigger; error?: string }[];
  status: ServiceInstance["status"];
  pos: { x: number; y: number };
  sleeping?: boolean;
};

function seed() {
  const p: Project = {
    id: "prj_sample",
    name: "example.com",
    description: "개인 서비스와 사이드 프로젝트",
    created_at: ago(60 * 24 * 40),
    workspace_id: "ws_default",
    github_user_id: null,
    default_environment_id: "env_prod",
    notify_webhook_url: "https://discord.com/api/webhooks/1290000000000000000/xxxxxxxx",
    notify_events: ["deploy.failed", "deploy.crashed", "volume.backup.failed"],
    pr_environments: { enabled: true, base_environment_id: "env_stg", auto_delete: true, domain: true, focused: false },
    pr_from_forks: false,
    pr_from_bots: false,
  };
  const p2: Project = {
    id: "prj_sandbox",
    name: "sandbox",
    description: "실험용",
    created_at: ago(60 * 24 * 3),
    workspace_id: "ws_personal_admin",
    github_user_id: null,
    default_environment_id: "env_sb_prod",
    notify_webhook_url: null,
    notify_events: [],
    pr_environments: { ...PR_DEFAULTS },
    pr_from_forks: false,
    pr_from_bots: false,
  };
  S.projects.push(p, p2);
  const prod: Environment = { id: "env_prod", project_id: p.id, name: "production", created_at: p.created_at, ...STANDARD_ENV };
  const stg: Environment = { id: "env_stg", project_id: p.id, name: "staging", created_at: ago(60 * 24 * 12), ...STANDARD_ENV };
  const sb: Environment = { id: "env_sb_prod", project_id: p2.id, name: "production", created_at: p2.created_at, ...STANDARD_ENV };
  S.envs.push(prod, stg, sb);
  S.shared[prod.id] = { APP_NAME: "aven-demo", SLACK_WEBHOOK: "https://example.com/webhooks/slack", TZ: "Asia/Seoul" };
  S.shared[stg.id] = { APP_NAME: "aven-demo-staging", SLACK_WEBHOOK: "https://example.com/webhooks/slack-staging", TZ: "Asia/Seoul" };
  S.shared[sb.id] = {};

  const svcs: SeedSvc[] = [
    {
      name: "hello",
      source_type: "repo",
      icon: "github",
      inst: {
        repo_url: "https://github.com/example-org/hello",
        port: 3000,
        replicas: 2,
        healthcheck_path: "/healthz",
        pre_deploy_command: "npm run migrate",
        watch_paths: ["src/**", "package.json"],
        variables: {
          DATABASE_URL: "${{postgres.DATABASE_URL}}",
          REDIS_URL: "${{ redis.REDIS_URL }}",
          APP_NAME: "${{shared.APP_NAME}}",
          NODE_ENV: "production",
          SESSION_SECRET: "s3cr3t-3f9a1c0b7e",
          LOG_LEVEL: "info",
        },
      },
      deps: [
        { st: "live", min: 95, trig: "github" },
        { st: "skipped", min: 300, trig: "github" },
        { st: "removed", min: 60 * 26, trig: "github" },
        { st: "failed", min: 60 * 27, trig: "github", error: "healthcheck /healthz did not return 2xx within 60s" },
        { st: "removed", min: 60 * 50, trig: "variables" },
        { st: "removed", min: 60 * 24 * 4, trig: "manual" },
      ],
      status: "online",
      pos: { x: 0, y: 0 },
    },
    {
      name: "postgres",
      source_type: "database",
      database_kind: "postgres",
      icon: "postgres",
      template: "postgres",
      inst: { image: DB_IMAGES.postgres, healthcheck_type: "tcp", restart_policy: "always", port: 5432, node_id: "node_database" },
      deps: [{ st: "live", min: 60 * 24 * 9, trig: "template" }],
      status: "online",
      pos: { x: 440, y: -30 },
    },
    {
      name: "redis",
      source_type: "database",
      database_kind: "redis",
      icon: "redis",
      template: "redis",
      inst: { image: DB_IMAGES.redis, healthcheck_type: "tcp", restart_policy: "always", port: 6379 },
      deps: [{ st: "live", min: 60 * 24 * 9, trig: "template" }],
      status: "online",
      pos: { x: 440, y: 260 },
    },
    {
      name: "whoami",
      source_type: "image",
      icon: "docker",
      inst: { image: "traefik/whoami:v1.10", port: 80, healthcheck_path: "/health", variables: { WHOAMI_NAME: "demo" }, sleep_when_idle: true, sleep_after_s: 900 },
      deps: [
        { st: "live", min: 60 * 5, trig: "manual" },
        { st: "removed", min: 60 * 24 * 2, trig: "manual" },
      ],
      status: "online",
      pos: { x: 0, y: 300 },
      sleeping: true,
    } as SeedSvc,
    {
      name: "nightly-report",
      source_type: "repo",
      icon: "github",
      inst: {
        repo_url: "https://github.com/example-org/reports",
        root_dir: "/jobs/nightly",
        builder: "railpack",
        cron_schedule: "0 18 * * *",
        start_command: "node dist/nightly.js",
        healthcheck_type: "none",
        restart_policy: "never",
        variables: { DATABASE_URL: "${{postgres.DATABASE_URL}}", WEBHOOK_URL: "${{shared.SLACK_WEBHOOK}}", TZ: "${{shared.TZ}}" },
      },
      deps: [{ st: "live", min: 60 * 24 * 6, trig: "github" }],
      status: "scheduled",
      pos: { x: 880, y: 20 },
    },
    {
      name: "billing-api",
      source_type: "repo",
      icon: "github",
      inst: {
        repo_url: "https://github.com/example-org/billing-api",
        branch: "develop",
        port: 8080,
        variables: { DATABASE_URL: "${{postgres.DATABASE_URL}}", STRIPE_KEY: "sk_test_example" },
      },
      deps: [
        { st: "failed", min: 18, trig: "github", error: "build failed: exit code 2 (npm run build)" },
        { st: "crashed", min: 60 * 3, trig: "manual", error: "컨테이너가 시작 직후 종료되었습니다 (exit=1, restarts=3)" },
      ],
      status: "failed",
      pos: { x: 880, y: 300 },
    },
  ];

  for (const def of svcs) {
    const svc: Service = {
      id: `svc_${def.name.replace(/-/g, "")}`,
      project_id: p.id,
      name: def.name,
      icon: def.icon ?? null,
      source_type: def.source_type,
      database_kind: def.database_kind ?? null,
      template: def.template ?? null,
      created_at: ago(60 * 24 * 10),
    };
    S.services.push(svc);
    const varsBase = def.database_kind ? dbVars(def.database_kind, def.name) : {};
    for (const env of [prod, stg]) {
      const isProd = env === prod;
      const inst = newInstance(svc, env, {
        ...def.inst,
        variables: { ...varsBase, ...(def.inst.variables ?? {}), ...(isProd ? {} : def.name === "hello" ? { NODE_ENV: "staging", LOG_LEVEL: "debug" } : {}) },
        branch: isProd ? (def.inst.branch ?? "main") : "develop",
        replicas: isProd ? (def.inst.replicas ?? 1) : 1,
        canvas: def.pos,
        status: isProd ? (def.sleeping ? "sleeping" : def.status) : def.name === "billing-api" ? "offline" : def.status === "scheduled" ? "scheduled" : "online",
      });
      if (def.inst.port && def.source_type !== "database" && def.name !== "billing-api")
        inst.domains = [genDomain(generatedHost(def.name, env, p))];
      if (def.name === "hello" && isProd)
        inst.domains.push(
          // Cloudflare-proxied custom domain: verified over HTTP through the proxy.
          {
            host: "hello.example.org",
            kind: "custom",
            target_port: null,
            status: "proxied",
            proxy: "cloudflare",
            verification: { method: "http", verified_at: iso(now() - 3 * 864e5), txt: verifyTxt("hello.example.org") },
            cert: { status: "valid", not_after: iso(now() + 71 * 864e5) },
            dns: [
              { type: "CNAME", name: "hello", value: "node-one.edge.example.com", status: "ok", current: "192.0.2.21, 192.0.2.22 (Cloudflare)" },
              { type: "TXT", name: "_aven-verify.hello", value: verifyTxt("hello.example.org").value, status: "ok", current: verifyTxt("hello.example.org").value },
            ],
          },
          // Pending custom domain: CNAME still points at the old host, TXT not created yet.
          {
            host: "hello.example.com",
            kind: "custom",
            target_port: 3000,
            status: "pending_dns",
            proxy: null,
            verification: { method: "", verified_at: null, txt: verifyTxt("hello.example.com") },
            cert: { status: "pending" },
            dns: [
              { type: "CNAME", name: "hello", value: "node-one.edge.example.com", status: "mismatch", current: "previous.example.net" },
              { type: "TXT", name: "_aven-verify.hello", value: verifyTxt("hello.example.com").value, status: "pending", current: null },
            ],
          },
        );
      if (def.name === "whoami" && isProd) {
        // remote tunnel service: no generated domain (no public IP), custom domain served through the tunnel.
        inst.node_id = "node_remote";
        inst.domains = [
          {
            host: "whoami.example.net",
            kind: "custom",
            target_port: null,
            status: "proxied",
            proxy: "tunnel",
            tls: "off",
            trust_forwarded_proto: true,
            verification: { method: "http", verified_at: iso(now() - 12 * 864e5), txt: verifyTxt("whoami.example.net") },
            cert: { status: "valid", not_after: iso(now() + 55 * 864e5) },
            dns: [
              { type: "CNAME", name: "whoami", value: NODE_INGRESS.node_remote.hostname, status: "ok", current: "192.0.2.23, 192.0.2.24 (Cloudflare)" },
              { type: "TXT", name: "_aven-verify.whoami", value: verifyTxt("whoami.example.net").value, status: "pending", current: null },
            ],
          },
        ];
      }
      if (def.database_kind === "postgres" && isProd) inst.tcp_proxy = { container_port: 5432, public_port: 34521 };
      S.instances.push(inst);

      // volumes
      if (def.database_kind) {
        S.volumes.push({
          id: `vol_${def.name}_${env.name}`,
          project_id: p.id,
          environment_id: env.id,
          service_instance_id: inst.id,
          name: `${def.name}-volume`,
          mount_path: DB_MOUNT[def.database_kind],
          size_bytes: def.database_kind === "postgres" ? (isProd ? 1.42e9 : 2.1e8) : 3.4e7,
          // v1.5 soft limit: production postgres sits just over 90% of its limit
          size_limit_mb: def.database_kind === "postgres" && isProd ? 1500 : null,
          created_at: svc.created_at,
        });
      }

      // deployments
      const deps = isProd ? def.deps : def.deps.slice(0, 1).map((d) => ({ ...d, min: d.min + 30, st: def.name === "billing-api" ? ("failed" as const) : d.st }));
      let ci = hash(def.name + env.name) % COMMITS.length;
      deps.forEach((dd, i) => {
        const created = now() - dd.min * 60_000;
        const [msg, author] = COMMITS[ci++ % COMMITS.length];
        const isRepo = def.source_type === "repo";
        const d: Deployment = {
          id: `dep_${def.name.replace(/-/g, "").slice(0, 6)}${env.name.slice(0, 1)}${i}`,
          service_id: svc.id,
          environment_id: env.id,
          service_instance_id: inst.id,
          node_id: inst.node_id,
          status: dd.st,
          trigger: dd.trig ?? "manual",
          commit_sha: isRepo ? sha() : null,
          commit_message: isRepo ? msg : null,
          commit_author: isRepo ? author : null,
          branch: isRepo ? inst.branch : null,
          image: dd.st === "failed" && dd.error?.startsWith("build") ? null : isRepo ? `aven/${def.name}:${i}` : inst.image,
          builder_used: isRepo ? (inst.builder === "railpack" ? "railpack" : "dockerfile") : "image",
          replicas:
            dd.st === "live"
              ? Array.from({ length: inst.replicas }, (_, k) => ({
                  name: `${def.name}-${k + 1}`,
                  status: "running" as const,
                  restarts: def.name === "hello" && k === 1 ? 1 : 0,
                }))
              : [],
          error: dd.error ?? null,
          created_at: iso(created),
          started_at: dd.st === "skipped" ? null : iso(created + 2000),
          finished_at: iso(created + (dd.st === "skipped" ? 0 : 38_000 + (hash(def.name + i) % 40_000))),
        };
        if (dd.st === "live") {
          inst.current_deployment_id = d.id;
          inst.current_replicas = d.replicas.length;
        }
        S.deployments.push(d);
        if (dd.st !== "skipped")
          S.buildLogs[d.id] = buildLogFor(svc, inst, d, dd.error?.startsWith("build") ? dd.error : undefined);
        if (dd.st === "live" || dd.st === "removed" || dd.st === "crashed") S.runtimeLogs[d.id] = runtimeFor(svc, d, dd.st === "live" ? 80 : 25);
        if (dd.st === "crashed")
          S.runtimeLogs[d.id] = [
            { t: d.finished_at!, replica: `${def.name}-1`, stream: "stdout", msg: "listening on :8080" },
            { t: d.finished_at!, replica: `${def.name}-1`, stream: "stderr", msg: "Error: STRIPE_KEY must start with sk_live_ in production" },
            { t: d.finished_at!, replica: `${def.name}-1`, stream: "stderr", msg: "    at loadConfig (/app/dist/config.js:18:11)" },
            { t: d.finished_at!, replica: `${def.name}-1`, stream: "stderr", msg: "process exited with code 1" },
          ];
      });

      // cron runs
      if (def.inst.cron_schedule && inst.current_deployment_id) {
        for (let k = 0; k < 8; k++) {
          const start = new Date();
          start.setUTCHours(18, 0, 0, 0);
          if (start.getTime() > now()) start.setUTCDate(start.getUTCDate() - 1);
          start.setUTCDate(start.getUTCDate() - k);
          const fail = k === 2 && isProd;
          S.cronRuns.push({
            id: rid("run"),
            deployment_id: inst.current_deployment_id,
            started_at: iso(start.getTime()),
            finished_at: iso(start.getTime() + 4200 + k * 300),
            exit_code: fail ? 1 : 0,
            status: fail ? "failed" : "succeeded",
          });
        }
      }
    }
  }

  // PR preview environment forked from staging (GitHub PR #42 on example-org/hello)
  const pr: Environment = {
    id: "env_pr42",
    project_id: p.id,
    name: "pr-42",
    created_at: ago(90),
    kind: "preview",
    pr_number: 42,
    pr_url: "https://github.com/example-org/hello/pull/42",
    pr_branch: "feat/dark-mode",
  };
  S.envs.push(pr);
  forkMockEnv(p.id, pr, stg.id, true);
  for (const i of S.instances.filter((x) => x.environment_id === pr.id && x.repo_url === "https://github.com/example-org/hello")) i.branch = "feat/dark-mode";

  // sandbox project
  const toy: Service = {
    id: "svc_toy",
    project_id: p2.id,
    name: "toy",
    icon: "docker",
    source_type: "image",
    database_kind: null,
    template: null,
    created_at: p2.created_at,
  };
  S.services.push(toy);
  S.instances.push(newInstance(toy, sb, { image: "nginx:alpine", port: 80, status: "offline", canvas: { x: 0, y: 0 } }));

  // backups
  const pgVol = S.volumes.find((v) => v.id === "vol_postgres_production")!;
  for (let k = 0; k < 4; k++)
    S.backups.push({
      id: `bak_${k}`,
      volume_id: pgVol.id,
      size_bytes: 3.1e8 - k * 1.2e7,
      created_at: ago(60 * 24 * k + 200),
      status: k === 1 ? "failed" : "done",
      error: k === 1 ? "tar: ./pg_wal/000000010000000000000042: file changed as we read it" : null,
    });

  // events
  const name = (id: string) => S.services.find((s) => s.id === id)?.name ?? "";
  for (const d of [...S.deployments].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 18)) {
    const kind =
      d.status === "live" || d.status === "removed"
        ? "deploy.live"
        : d.status === "failed"
          ? "deploy.failed"
          : d.status === "crashed"
            ? "deploy.crashed"
            : d.status === "skipped"
              ? "deploy.skipped"
              : "deploy.started";
    S.events.push({
      id: rid("evt"),
      project_id: p.id,
      environment_id: d.environment_id,
      service_id: d.service_id,
      kind,
      message: { "deploy.live": "배포 성공", "deploy.failed": "배포 실패", "deploy.crashed": "크래시", "deploy.skipped": "배포 건너뜀", "deploy.started": "배포 시작" }[kind]!,
      created_at: d.finished_at ?? d.created_at,
    });
    void name;
  }
  S.events.push(
    { id: rid("evt"), project_id: p.id, environment_id: "env_prod", service_id: "svc_hello", kind: "variables.updated", message: "변수 2개 변경", created_at: ago(97) },
    { id: rid("evt"), project_id: p.id, environment_id: "env_prod", service_id: "svc_postgres", kind: "backup.done", message: "백업 완료 (296 MB)", created_at: ago(200) },
    { id: rid("evt"), project_id: p.id, environment_id: "env_stg", service_id: null, kind: "environment.created", message: "staging 환경 생성", created_at: ago(60 * 24 * 12) },
    { id: rid("evt"), project_id: p.id, environment_id: "env_prod", service_id: "svc_whoami", kind: "service.slept", message: "whoami (production): 15분 동안 요청이 없어 슬립", created_at: ago(22) },
    { id: rid("evt"), project_id: p.id, environment_id: "env_prod", service_id: "svc_whoami", kind: "service.woke", message: "whoami (production): 요청이 들어와 깨어남 (1.8초)", created_at: ago(70) },
    { id: rid("evt"), project_id: p.id, environment_id: "env_prod", service_id: "svc_oldapi", service_name: "old-api", kind: "service.deleted", message: "서비스 old-api 삭제", created_at: ago(400) },
  );
  S.events.sort((a, b) => b.created_at.localeCompare(a.created_at));
}

/* ── templates ───────────────────────────────────────────────────────── */

type MockTemplate = TemplateDetail;

/** Mock of internal/catalog/templates/*.json (shape only; the real definitions live on the server). */
function builtinTemplate(
  key: string,
  name: string,
  description: string,
  icon: string,
  category: string,
  tags: string[],
  services: TemplateService[],
  deploys: number,
  readme: string,
): MockTemplate {
  return {
    key,
    name,
    description,
    icon,
    category,
    tags,
    services,
    inputs: [],
    builtin: true,
    visibility: "public",
    owner_user_id: null,
    deploy_count: deploys,
    created_at: null,
    updated_at: null,
    can_edit: false,
    readme,
    definition: {
      version: 1,
      key,
      name,
      description,
      icon,
      category,
      tags,
      readme,
      services: services.map((s) => ({ name: s.name, icon: s.icon, source: s.source_type === "database" ? { database: s.database_kind } : { image: s.image } })),
    },
  };
}

const pg = (name: string): TemplateService => ({ name, source_type: "database", database_kind: "postgres", icon: "postgres" });

export const TEMPLATES: MockTemplate[] = [
  builtinTemplate("postgres", "PostgreSQL", "가장 많이 쓰는 오픈소스 관계형 데이터베이스", "postgres", "databases", ["database", "sql"], [pg("postgres")], 41, "# PostgreSQL\n\nPostgreSQL 17과 영구 볼륨. 환경마다 비밀번호를 새로 만듭니다.\n\n다른 서비스에서 `${{postgres.DATABASE_URL}}`로 연결합니다."),
  builtinTemplate("mysql", "MySQL", "널리 쓰이는 관계형 데이터베이스", "mysql", "databases", ["database", "sql"], [{ name: "mysql", source_type: "database", database_kind: "mysql", icon: "mysql" }], 9, "# MySQL\n\nMySQL 8.4와 영구 볼륨."),
  builtinTemplate("redis", "Redis", "인메모리 키-값 저장소, 캐시와 큐", "redis", "databases", ["database", "cache"], [{ name: "redis", source_type: "database", database_kind: "redis", icon: "redis" }], 27, "# Redis\n\nAOF 영속화와 비밀번호가 있는 Redis 7.4."),
  builtinTemplate("mongo", "MongoDB", "문서 지향 NoSQL 데이터베이스", "mongo", "databases", ["database", "nosql"], [{ name: "mongo", source_type: "database", database_kind: "mongo", icon: "mongo" }], 6, "# MongoDB\n\nMongoDB 8.0과 영구 볼륨."),
  builtinTemplate("minio", "MinIO", "S3 호환 오브젝트 스토리지", "minio", "storage", ["s3", "object-storage"], [{ name: "minio", source_type: "image", image: "pgsty/minio:RELEASE.2026-08-04T00-00-00Z", icon: "minio" }], 12, "# MinIO\n\nAPI는 9000번(공개 도메인), 콘솔은 9001번(사설 네트워크)입니다.\n\n- 루트 비밀번호는 배포할 때 생성합니다 (`MINIO_ROOT_PASSWORD`)"),
  builtinTemplate("n8n", "n8n", "노코드 워크플로 자동화", "n8n", "automation", ["workflow", "automation"], [{ name: "n8n", source_type: "image", image: "n8nio/n8n:1.112.0", icon: "n8n" }], 18, "# n8n\n\nSQLite를 볼륨에 두는 워크플로 자동화. **N8N_ENCRYPTION_KEY**는 배포할 때 생성합니다."),
  builtinTemplate("umami", "Umami", "쿠키 없는 웹 분석", "umami", "analytics", ["analytics", "privacy"], [{ name: "umami", source_type: "image", image: "ghcr.io/umami-software/umami:postgresql-v2.19.0", icon: "umami" }, pg("postgres")], 15, "# Umami\n\nPostgreSQL을 쓰는 웹 분석. 기본 로그인은 `admin` / `umami`입니다."),
  builtinTemplate("uptime-kuma", "Uptime Kuma", "셀프 호스팅 업타임 모니터", "uptime-kuma", "monitoring", ["monitoring", "uptime"], [{ name: "uptime-kuma", source_type: "image", image: "louislam/uptime-kuma:1.23.16", icon: "uptime-kuma" }], 22, "# Uptime Kuma\n\n처음 접속할 때 관리자 계정을 만듭니다."),
  builtinTemplate("plausible", "Plausible", "가벼운 개인정보 친화 분석", "plausible", "analytics", ["analytics", "privacy"], [{ name: "plausible", source_type: "image", image: "ghcr.io/plausible/community-edition:v2.1.5", icon: "plausible" }, pg("postgres"), { name: "clickhouse", source_type: "image", image: "clickhouse/clickhouse-server:24.12-alpine", icon: "clickhouse" }], 4, "# Plausible CE\n\nPostgreSQL과 ClickHouse를 함께 씁니다."),
  builtinTemplate("ghost", "Ghost", "뉴스레터와 블로그 플랫폼", "ghost", "cms", ["blog", "cms"], [{ name: "ghost", source_type: "image", image: "ghost:5.130-alpine", icon: "ghost" }, { name: "mysql", source_type: "database", database_kind: "mysql", icon: "mysql" }], 7, "# Ghost\n\n생성된 도메인의 `/ghost`에서 소유자 계정을 만듭니다."),
];

/** User templates (marketplace). */
const USER_TEMPLATES: MockTemplate[] = [
  {
    key: "acme-starter",
    name: "Acme 스타터",
    description: "Node API + PostgreSQL, 관리자 이메일을 입력받습니다",
    icon: "docker",
    category: "starter",
    tags: ["node", "postgres"],
    services: [{ name: "api", source_type: "image", image: "ghcr.io/acme/api:1", icon: "docker" }, pg("db")],
    inputs: [
      { key: "ADMIN_EMAIL", label: "관리자 이메일", required: true, pattern: "[^@\\s]+@[^@\\s]+", description: "첫 관리자 계정으로 만듭니다" },
      { key: "APP_REGION", label: "지역", default: "eu" },
      { key: "STRIPE_KEY", label: "Stripe 키", secret: true },
    ],
    builtin: false,
    visibility: "workspace",
    owner_user_id: "usr_admin",
    deploy_count: 3,
    created_at: ago(60 * 24 * 3),
    updated_at: ago(60 * 5),
    can_edit: true,
    readme: "# Acme 스타터\n\n1. 관리자 이메일을 입력합니다\n2. 배포가 끝나면 생성된 도메인을 엽니다\n\n세션 비밀은 `${{ secret(32) }}`로 배포할 때 만듭니다.",
    definition: {
      version: 1,
      key: "acme-starter",
      name: "Acme 스타터",
      services: [
        { name: "db", source: { database: "postgres" } },
        { name: "api", source: { image: "ghcr.io/acme/api:1" }, port: 3000, variables: { DATABASE_URL: "${{db.DATABASE_URL}}", ADMIN_EMAIL: "${{ inputs.ADMIN_EMAIL }}", SESSION_SECRET: "${{ secret(32) }}" }, domains: [{ generate: true }] },
      ],
    },
  },
];

const allTemplates = (): MockTemplate[] => [...TEMPLATES, ...USER_TEMPLATES.filter((t) => t.visibility !== "private" || t.owner_user_id === me().id)];
const templateView = (t: MockTemplate): Template => {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { readme, definition, ...v } = t;
  return v;
};

/* ── lookups ─────────────────────────────────────────────────────────── */

function getProject(id: string) {
  const p = S.projects.find((x) => x.id === id);
  if (!p) throw new HttpError(404, "project not found");
  return p;
}
function getEnv(id: string) {
  const e = S.envs.find((x) => x.id === id);
  if (!e) throw new HttpError(404, "environment not found");
  return e;
}
function getService(id: string) {
  const s = S.services.find((x) => x.id === id);
  if (!s) throw new HttpError(404, "service not found");
  return s;
}
function getInstance(sid: string, eid: string) {
  const i = S.instances.find((x) => x.service_id === sid && x.environment_id === eid);
  if (!i) throw new HttpError(404, "service instance not found");
  return i;
}
function getDeployment(id: string) {
  const d = S.deployments.find((x) => x.id === id);
  if (!d) throw new HttpError(404, "deployment not found");
  return d;
}
function getVolume(id: string) {
  const v = S.volumes.find((x) => x.id === id);
  if (!v) throw new HttpError(404, "volume not found");
  return v;
}

/* ── variable resolution ─────────────────────────────────────────────── */

function injected(inst: ServiceInstance): Record<string, string> {
  const svc = getService(inst.service_id);
  const env = getEnv(inst.environment_id);
  const p = getProject(svc.project_id);
  const cur = inst.current_deployment_id ? S.deployments.find((d) => d.id === inst.current_deployment_id) : null;
  const vol = S.volumes.find((v) => v.service_instance_id === inst.id);
  const out: Record<string, string> = {
    AVEN_SERVICE_NAME: svc.name,
    AVEN_SERVICE_ID: svc.id,
    AVEN_ENVIRONMENT_NAME: env.name,
    AVEN_ENVIRONMENT_ID: env.id,
    AVEN_PROJECT_NAME: p.name,
    AVEN_PROJECT_ID: p.id,
    AVEN_PRIVATE_DOMAIN: inst.private_domain,
  };
  if (inst.port) out.PORT = String(inst.port);
  if (inst.domains[0]) out.AVEN_PUBLIC_DOMAIN = inst.domains[0].host;
  if (cur) out.AVEN_DEPLOYMENT_ID = cur.id;
  if (cur?.commit_sha) out.AVEN_GIT_COMMIT_SHA = cur.commit_sha;
  if (cur?.branch) out.AVEN_GIT_BRANCH = cur.branch;
  if (inst.tcp_proxy) {
    out.AVEN_TCP_PROXY_DOMAIN = BASE;
    out.AVEN_TCP_PROXY_PORT = String(inst.tcp_proxy.public_port);
  }
  if (vol) out.AVEN_VOLUME_MOUNT_PATH = vol.mount_path;
  return out;
}

const REF = /\$\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}/g;

type RV = { value: string; source: "user" | "shared" | "reference" | "injected"; cross_node?: boolean; mesh?: boolean; sealed?: boolean };
function resolveTyped(inst: ServiceInstance): Record<string, RV> {
  const flat = resolveAll(inst);
  const out: Record<string, RV> = {};
  const svcProject = getService(inst.service_id).project_id;
  const crossNode = (raw: string | undefined) =>
    !!raw &&
    [...raw.matchAll(REF)].some((m) => {
      const ns = m[1].split(".")[0];
      if (!m[1].includes(".") || ns === "shared") return false;
      const o = S.services.find((x) => x.name === ns && x.project_id === svcProject);
      const oi = o && S.instances.find((i) => i.service_id === o.id && i.environment_id === inst.environment_id);
      return !!oi && oi.node_id !== inst.node_id;
    });
  for (const [k, value] of Object.entries(flat)) {
    const raw = inst.variables[k];
    const source = raw === undefined ? "injected" : /^\$\{\{\s*shared\.[A-Za-z0-9_]+\s*\}\}$/.test(raw) ? "shared" : /\$\{\{/.test(raw) ? "reference" : "user";
    // v1.5 mock: mesh가 모든 mesh 노드를 잇는다고 본다(이 노드가 mesh 노드면 닿는다).
    const here = NODES.find((n) => n.id === inst.node_id);
    out[k] = crossNode(raw) ? { value, source, cross_node: true, mesh: meshCapable(here) } : { value, source };
    // 봉인 값(또는 그것을 참조한 값)은 가린다. mock은 봉인 값을 "<sealed>"로만 들고 있다.
    if (value.includes(SEALED_VALUE)) out[k] = { ...out[k], value: SEALED_VALUE, sealed: true };
  }
  return out;
}

function resolveAll(inst: ServiceInstance, depth = 0): Record<string, string> {
  const own = { ...injected(inst), ...inst.variables };
  const shared = S.shared[inst.environment_id] ?? {};
  const resolveOne = (v: string, seen: Set<string>): string =>
    v.replace(REF, (m, ref: string) => {
      const dot = ref.indexOf(".");
      if (dot < 0) {
        if (seen.has(ref) || !(ref in own)) return "";
        return resolveOne(own[ref], new Set([...seen, ref]));
      }
      const ns = ref.slice(0, dot);
      const key = ref.slice(dot + 1);
      if (ns === "shared") return shared[key] ?? "";
      if (depth > 3) return "";
      const other = S.services.find((s) => s.name === ns && s.project_id === getService(inst.service_id).project_id);
      if (!other) return "";
      const oi = S.instances.find((i) => i.service_id === other.id && i.environment_id === inst.environment_id);
      if (!oi) return "";
      return resolveAll(oi, depth + 1)[key] ?? "";
    });
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(own)) out[k] = resolveOne(v, new Set([k]));
  return out;
}

/* ── deployment simulation ───────────────────────────────────────────── */

function pushLog(d: Deployment, line: string) {
  (S.buildLogs[d.id] ??= []).push(`${hms(now())} ${line}`);
  depListeners.get(d.id)?.forEach((fn) => fn(d, S.buildLogs[d.id][S.buildLogs[d.id].length - 1]));
}
function setStatus(d: Deployment, st: DeploymentStatus) {
  d.status = st;
  depListeners.get(d.id)?.forEach((fn) => fn(d));
}

function startDeployment(inst: ServiceInstance, trigger: DeploymentTrigger, from?: Deployment): Deployment {
  const svc = getService(inst.service_id);
  const isRepo = svc.source_type === "repo";
  const [msg, author] = pick(COMMITS);
  const d: Deployment = {
    id: rid("dep"),
    service_id: svc.id,
    environment_id: inst.environment_id,
    service_instance_id: inst.id,
    node_id: inst.node_id,
    status: "queued",
    trigger,
    commit_sha: from?.commit_sha ?? (isRepo ? sha() : null),
    commit_message: from?.commit_message ?? (isRepo ? msg : null),
    commit_author: from?.commit_author ?? (isRepo ? author : null),
    branch: isRepo ? inst.branch : null,
    image: from?.image ?? (isRepo ? null : inst.image),
    builder_used: isRepo ? (inst.builder === "railpack" ? "railpack" : "dockerfile") : "image",
    replicas: [],
    error: null,
    created_at: iso(now()),
    started_at: null,
    finished_at: null,
  };
  // An in-progress deployment for the same instance is cancelled.
  for (const o of S.deployments)
    if (o.service_instance_id === inst.id && !["live", "failed", "crashed", "cancelled", "removed", "skipped"].includes(o.status)) {
      setStatus(o, "cancelled");
      o.finished_at = iso(now());
    }
  S.deployments.push(d);
  S.buildLogs[d.id] = [];
  inst.status = "deploying";
  emit({ project_id: svc.project_id, environment_id: inst.environment_id, service_id: svc.id, kind: "deploy.started", message: "배포 시작" });

  const skipBuild = !isRepo || trigger === "rollback" || trigger === "redeploy";
  const script: [number, () => void][] = [];
  let t = 700;
  const at = (dt: number, fn: () => void) => script.push([(t += dt), fn]);
  at(0, () => {
    setStatus(d, "initializing");
    d.started_at = iso(now());
  });
  const full = buildLogFor(svc, inst, { ...d, started_at: iso(now()) });
  const buildPart = full.filter((l) => !/==> (pre-deploy|starting|healthcheck|배포)/.test(l.slice(9)) && !/migrations:/.test(l));
  if (skipBuild && isRepo) {
    at(500, () => pushLog(d, `==> reusing image ${d.image ?? `aven/${svc.name}`} (빌드 생략)`));
  } else {
    at(400, () => setStatus(d, isRepo ? "building" : "deploying"));
    for (const l of buildPart) at(isRepo ? 260 : 400, () => pushLog(d, l.slice(9)));
  }
  if (inst.pre_deploy_command) {
    at(400, () => {
      setStatus(d, "pre_deploy");
      pushLog(d, `==> pre-deploy: ${inst.pre_deploy_command}`);
    });
    at(1200, () => pushLog(d, "    migrations: 0 applied, 0 pending"));
  }
  at(500, () => {
    setStatus(d, "deploying");
    if (!d.image) d.image = isRepo ? `aven/${svc.name}:${d.commit_sha?.slice(0, 7)}` : inst.image;
    const nodesOf = (inst.placement ?? [{ node_id: inst.node_id, replicas: inst.replicas }]).flatMap((p) => Array.from({ length: p.replicas }, () => p.node_id));
    const count = inst.autoscale?.enabled ? Math.min(inst.autoscale.max_replicas, Math.max(inst.autoscale.min_replicas, inst.current_replicas || inst.replicas)) : inst.replicas;
    d.replicas = Array.from({ length: count }, (_, k) => ({ name: `${svc.name}-${k + 1}`, status: "starting" as const, restarts: 0, node_id: nodesOf[k % nodesOf.length] }));
    pushLog(d, `==> starting ${inst.replicas} replica${inst.replicas > 1 ? "s" : ""}`);
  });
  if (inst.healthcheck_type !== "none")
    at(900, () => {
      setStatus(d, "healthchecking");
      pushLog(d, `==> healthcheck ${inst.healthcheck_type === "http" ? inst.healthcheck_path : "tcp"}`);
    });
  at(1500, () => {
    if (d.status === "cancelled") return;
    pushLog(d, "==> healthcheck OK");
    pushLog(d, "==> 배포 완료");
    d.replicas = d.replicas.map((r) => ({ ...r, status: "running" }));
    d.finished_at = iso(now());
    const prev = inst.current_deployment_id ? S.deployments.find((x) => x.id === inst.current_deployment_id) : null;
    if (prev && prev.status === "live") prev.status = "removed";
    if (prev) prev.replicas = [];
    inst.current_deployment_id = d.id;
    inst.current_replicas = inst.cron_schedule ? 0 : d.replicas.length;
    inst.autoscale_status = mockAutoscaleStatus(inst);
    inst.pending_changes = [];
    inst.status = inst.cron_schedule ? "scheduled" : "online";
    S.runtimeLogs[d.id] = runtimeFor(svc, d, 12);
    setStatus(d, "live");
    emit({ project_id: svc.project_id, environment_id: inst.environment_id, service_id: svc.id, kind: "deploy.live", message: "배포 성공" });
  });
  for (const [ms, fn] of script)
    setTimeout(() => {
      if (d.status === "cancelled") return;
      fn();
    }, ms);
  return d;
}

/* ── metrics ─────────────────────────────────────────────────────────── */

const RANGE: Record<MetricRange, { span: number; step: number }> = {
  "1h": { span: 3600, step: 30 },
  "6h": { span: 6 * 3600, step: 300 },
  "24h": { span: 24 * 3600, step: 900 },
  "7d": { span: 7 * 24 * 3600, step: 7200 },
};

function metricsFor(inst: ServiceInstance, range: MetricRange): MetricPoint[] {
  const svc = getService(inst.service_id);
  const { span, step } = RANGE[range];
  const seedv = hash(svc.name + inst.environment_id);
  const end = Math.floor(now() / 1000 / step) * step;
  const offline = inst.status === "offline" || inst.status === "failed" || !inst.current_deployment_id;
  const cpuBase = { hello: 9, postgres: 4, redis: 1.2, whoami: 0.3, "nightly-report": 0.1, "billing-api": 0 }[svc.name] ?? 2;
  const memBase = { hello: 3.1e8, postgres: 2.2e8, redis: 2.6e7, whoami: 7e6, "nightly-report": 1.5e7, "billing-api": 0 }[svc.name] ?? 6e7;
  const pts: MetricPoint[] = [];
  let rx = 1e9 * ((seedv % 7) + 1);
  let tx = 4e8 * ((seedv % 5) + 1);
  const vol = S.volumes.find((v) => v.service_instance_id === inst.id);
  for (let t = end - span; t <= end; t += step) {
    const day = Math.sin(((t / 3600 + (seedv % 24)) / 24) * Math.PI * 2);
    const n1 = Math.sin(t / 377 + seedv) * 0.5 + Math.sin(t / 91 + seedv * 3) * 0.3;
    const spike = (hash(String(Math.floor(t / step)) + svc.name) % 37) === 0 ? 3.5 : 1;
    const cron = svc.name === "nightly-report" && new Date(t * 1000).getUTCHours() === 18 && new Date(t * 1000).getUTCMinutes() < 10;
    const cpu = offline ? 0 : cron ? 38 + n1 * 6 : Math.max(0, cpuBase * (1 + 0.35 * day + 0.25 * n1) * spike);
    const mem = offline ? 0 : memBase * (1 + 0.05 * day + 0.03 * n1) + (cron ? 1.8e8 : 0);
    const rate = offline ? 0 : Math.max(0, (cpuBase * 9000 + 500) * (1 + 0.5 * day + 0.3 * n1) * spike);
    rx += rate * step;
    tx += rate * 2.4 * step;
    pts.push({
      t: iso(t * 1000),
      cpu_percent: Math.round(cpu * 100) / 100,
      memory_bytes: Math.round(mem),
      memory_limit_bytes: 8 * 1024 ** 3,
      net_rx_bytes: Math.round(rx),
      net_tx_bytes: Math.round(tx),
      disk_bytes: vol ? Math.round((vol.size_bytes ?? 1e8) * (0.97 + 0.03 * ((t - (end - span)) / span))) : null,
    });
  }
  return pts;
}

/* ── canvas projection ───────────────────────────────────────────────── */

function canvasService(svc: Service, inst: ServiceInstance): CanvasService {
  const deps = S.deployments
    .filter((d) => d.service_instance_id === inst.id)
    .sort((a, b) => b.created_at.localeCompare(a.created_at));
  return {
    ...svc,
    instance: inst,
    latest_deployment: deps[0] ?? null,
    volumes: S.volumes.filter((v) => v.service_instance_id === inst.id),
  };
}

const schedules: Record<string, { cron: string | null; keep: number }> = { vol_postgres_production: { cron: "0 4 * * *", keep: 7 } };

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

/* ── routes ──────────────────────────────────────────────────────────── */

type Req = { method: string; path: string; query: URLSearchParams; body: unknown };
export type Handler = (m: string[], r: Req) => unknown;
const routes: [string, RegExp, Handler][] = [];
export const route = (method: string, pattern: string, h: Handler) =>
  routes.push([method, new RegExp("^" + pattern.replace(/\{[a-z]+\}/g, "([^/]+)") + "$"), h]);

function nameFrom(input: { repo_url?: string; image?: string; database_kind?: DatabaseKind }): string {
  if (input.database_kind) return input.database_kind;
  if (input.repo_url) return (input.repo_url.replace(/\.git$/, "").split("/").pop() ?? "app").toLowerCase();
  if (input.image) return (input.image.split("/").pop() ?? "image").split(":")[0].toLowerCase();
  return "service";
}
function uniqueName(pid: string, base: string) {
  const clean = base.replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "") || "service";
  let n = clean;
  let i = 2;
  while (S.services.some((s) => s.project_id === pid && s.name === n)) n = `${clean}-${i++}`;
  return n;
}

function createService(
  pid: string,
  input: { name?: string; source_type: SourceType; repo_url?: string; branch?: string; image?: string; database_kind?: DatabaseKind; environment_id?: string },
  template: string | null = null,
): Service {
  const p = getProject(pid);
  if (input.name && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(input.name)) throw new HttpError(400, "name must match [a-z0-9-]");
  if (input.name && S.services.some((s) => s.project_id === pid && s.name === input.name))
    throw new HttpError(409, `service "${input.name}" already exists`);
  const name = input.name ?? uniqueName(pid, nameFrom(input));
  const kind = input.database_kind ?? null;
  const svc: Service = {
    id: rid("svc"),
    project_id: pid,
    name,
    icon: kind ?? (input.source_type === "repo" ? "github" : input.source_type === "image" ? "docker" : null),
    source_type: input.source_type,
    database_kind: kind,
    template: template ?? (kind ? kind : null),
    created_at: iso(now()),
  };
  S.services.push(svc);
  const vars = kind ? dbVars(kind, name) : {};
  const envs = S.envs.filter((e) => e.project_id === pid);
  const others = S.instances.filter((i) => i.environment_id === (input.environment_id ?? p.default_environment_id));
  const maxY = Math.max(-120, ...others.map((i) => i.canvas?.y ?? 0));
  for (const env of envs) {
    const inst = newInstance(svc, env, {
      repo_url: input.repo_url ?? null,
      branch: input.branch ?? "main",
      image: kind ? DB_IMAGES[kind] : (input.image ?? null),
      variables: { ...vars },
      port: kind ? DB_PORT[kind] : null,
      healthcheck_type: kind ? "tcp" : "http",
      restart_policy: kind ? "always" : "on_failure",
      canvas: { x: 0, y: maxY + 300 },
    });
    S.instances.push(inst);
    if (kind)
      S.volumes.push({
        id: rid("vol"),
        project_id: pid,
        environment_id: env.id,
        service_instance_id: inst.id,
        name: `${name}-volume`,
        mount_path: DB_MOUNT[kind],
        size_bytes: 0,
        created_at: iso(now()),
      });
    if (env.id === input.environment_id && (input.repo_url || input.image || kind)) setTimeout(() => startDeployment(inst, template ? "template" : "manual"), 300);
  }
  emit({ project_id: pid, environment_id: input.environment_id ?? null, service_id: svc.id, kind: "service.created", message: "서비스 생성" });
  return svc;
}

/* ── v1.5 users / roles / tokens (single signed-in owner; enough to exercise the UI) ── */

const USERS: User[] = [
  { id: "usr_admin", email: "admin@local", name: "admin", role: "owner", totp_enabled: false, builtin: true, disabled: false, created_at: ago(60 * 24 * 30), last_login_at: ago(1) },
  { id: "usr_mina", email: "mina@example.com", name: "Mina", role: "member", totp_enabled: true, builtin: false, disabled: false, created_at: ago(60 * 24 * 7), last_login_at: ago(90) },
  { id: "usr_joon", email: "joon@example.com", name: "Joon", role: "viewer", totp_enabled: false, builtin: false, disabled: false, created_at: ago(60 * 24 * 2), last_login_at: null },
];
const MEMBERS: { project_id: string; user_id: string; role: ProjectRole; created_at: string }[] = [
  { project_id: "prj_sample", user_id: "usr_mina", role: "member", created_at: ago(60 * 24 * 7) },
  { project_id: "prj_sample", user_id: "usr_joon", role: "viewer", created_at: ago(60 * 24 * 2) },
];

/* ── v1.6: workspaces ─────────────────────────────────────────────────── */

type MockWorkspace = { id: string; name: string; personal: boolean; owner_id: string | null; created_at: string; default: boolean };
const WORKSPACES: MockWorkspace[] = [
  { id: "ws_default", name: "Team", personal: false, owner_id: null, created_at: ago(60 * 24 * 60), default: true },
  { id: "ws_personal_admin", name: "admin (개인)", personal: true, owner_id: "usr_admin", created_at: ago(60 * 24 * 60), default: false },
  { id: "ws_team_acme", name: "Acme", personal: false, owner_id: "usr_admin", created_at: ago(60 * 24 * 10), default: false },
];
const WS_MEMBERS: { workspace_id: string; user_id: string; role: WorkspaceRole; created_at: string }[] = [
  ...USERS.map((u) => ({ workspace_id: "ws_default", user_id: u.id, role: u.role, created_at: u.created_at })),
  { workspace_id: "ws_personal_admin", user_id: "usr_admin", role: "owner", created_at: ago(60 * 24 * 60) },
  { workspace_id: "ws_team_acme", user_id: "usr_admin", role: "owner", created_at: ago(60 * 24 * 10) },
  { workspace_id: "ws_team_acme", user_id: "usr_mina", role: "member", created_at: ago(60 * 24 * 5) },
];
/** The caller's role comes only from explicit membership in this workspace. */
function wsRole(w: MockWorkspace): WorkspaceRole | "" {
  const m = WS_MEMBERS.find((x) => x.workspace_id === w.id && x.user_id === me().id);
  return m ? m.role : "";
}
function wsView(w: MockWorkspace): Workspace {
  return { id: w.id, name: w.name, personal: w.personal, owner_id: w.owner_id, created_at: w.created_at, default: w.default, role: wsRole(w) || "viewer" };
}
/** default first, then own personal, then others. */
function visibleWorkspaces(): Workspace[] {
  const list = WORKSPACES.filter(
    (w) => WS_MEMBERS.some((m) => m.workspace_id === w.id && m.user_id === me().id),
  );
  const rank = (w: MockWorkspace) => (w.default ? 0 : w.personal && w.owner_id === me().id ? 1 : 2);
  return [...list].sort((a, b) => rank(a) - rank(b) || a.created_at.localeCompare(b.created_at)).map(wsView);
}
function getWorkspace(id: string): MockWorkspace {
  const w = WORKSPACES.find((x) => x.id === id);
  if (!w) throw new HttpError(404, "workspace not found");
  return w;
}

/* ── v1.6: per-user GitHub connections ────────────────────────────────── */

type MockGithubConn = { login: string; connected_at: string };
const GITHUB_CONN: Record<string, MockGithubConn> = {
  usr_mina: { login: "mina-gh", connected_at: ago(60 * 24 * 3) },
};
function githubAccountFor(userID: string | null): ProjectGithubAccount | null {
  if (!userID) return null;
  const u = USERS.find((x) => x.id === userID);
  const conn = GITHUB_CONN[userID];
  return { user_id: userID, user_name: u?.name ?? userID, login: conn?.login, connected: !!conn };
}

const INVITES: Invite[] = [];
const TOKENS: ApiToken[] = [];
const AUDIT: AuditEntry[] = [];
const SESSIONS: SessionInfo[] = [
  { id: "ses_current", user_id: "usr_admin", created_at: ago(60), expires_at: iso(now() + 30 * 86400_000), last_seen: ago(0), ip: "127.0.0.1", user_agent: "Mozilla/5.0 (this browser)", current: true },
  { id: "ses_cli", user_id: "usr_admin", created_at: ago(60 * 24), expires_at: iso(now() + 29 * 86400_000), last_seen: ago(300), ip: "127.0.0.1", user_agent: "Go-http-client/1.1", current: false },
];
let pendingTotp: string | null = null;
const me = () => USERS[0];
const audit = (kind: string, target: string, message: string) =>
  AUDIT.unshift({ id: rid("aud"), actor_id: me().id, actor: me().name, kind, target, message, ip: "127.0.0.1", created_at: iso(now()) });
const getUser = (id: string) => {
  const u = USERS.find((x) => x.id === id);
  if (!u) throw new HttpError(404, "not found");
  return u;
};
const recoveryCodes = () => Array.from({ length: 10 }, () => `${secret(5).toLowerCase()}-${secret(5).toLowerCase()}`);

route("GET", "/auth/me", () => {
  const u = me();
  return {
    user: u.name,
    id: u.id,
    email: u.email,
    name: u.name,
    role: u.role,
    totp_enabled: u.totp_enabled,
    builtin: u.builtin,
    auth: "session",
    token: null,
    instance_admin: true,
    instance_role: u.role,
    instance_owner: u.role === "owner",
    admin_caps: u.role === "owner" ? OWNER_CAPS.map((c) => c.id) : OWNER_CAPS.filter((c) => !OWNER.disabled.includes(c.id)).map((c) => c.id),
    workspace_admin: true,
    can_create_projects: true,
    workspaces: visibleWorkspaces(),
    projects: Object.fromEntries(S.projects.map((p) => [p.id, "admin"])),
    github_app_configured: true,
    github_connected: !!GITHUB_CONN[u.id],
  } satisfies Me;
});
route("POST", "/auth/login", (_, r) => {
  const b = (r.body ?? {}) as { email?: string; totp_code?: string };
  if (me().totp_enabled && !b.totp_code) throw new HttpError(401, "totp_required");
  return undefined;
});
route("POST", "/auth/logout", () => undefined);
route("PATCH", "/auth/me", (_, r) => {
  const b = (r.body ?? {}) as { name?: string; new_password?: string };
  if (b.new_password && me().builtin) throw new HttpError(400, "기본 admin 계정의 비밀번호는 AVEN_ADMIN_PASSWORD 환경변수로 바꿉니다");
  if (b.name !== undefined) me().name = b.name.trim();
  return me();
});
route("GET", "/auth/sessions", () => SESSIONS);
route("DELETE", "/auth/sessions/{id}", ([id]) => {
  const i = SESSIONS.findIndex((s) => s.id === id && !s.current);
  if (i < 0) throw new HttpError(404, "not found");
  SESSIONS.splice(i, 1);
  return 204;
});
route("POST", "/auth/2fa/setup", () => {
  if (me().totp_enabled) throw new HttpError(409, "2단계 인증이 이미 켜져 있습니다");
  pendingTotp = secret(32).toUpperCase().replace(/[^A-Z2-7]/g, "A");
  return { secret: pendingTotp, otpauth_url: `otpauth://totp/aven:${me().email}?secret=${pendingTotp}&issuer=aven&algorithm=SHA1&digits=6&period=30` };
});
route("POST", "/auth/2fa/enable", (_, r) => {
  const code = ((r.body ?? {}) as { code?: string }).code ?? "";
  if (!pendingTotp) throw new HttpError(400, "먼저 POST /api/auth/2fa/setup으로 비밀을 만드세요");
  if (!/^\d{6}$/.test(code)) throw new HttpError(401, "인증 코드가 올바르지 않습니다");
  me().totp_enabled = true;
  pendingTotp = null;
  return { recovery_codes: recoveryCodes() };
});
route("POST", "/auth/2fa/disable", () => {
  if (!me().totp_enabled) throw new HttpError(400, "2단계 인증이 꺼져 있습니다");
  me().totp_enabled = false;
  return 204;
});
route("POST", "/auth/2fa/recovery-codes", () => ({ recovery_codes: recoveryCodes() }));
// 패스키는 브라우저 WebAuthn이 필요해 mock에서는 목록만 비어 있게 보여 준다.
route("GET", "/auth/passkeys", () => []);
let require2FA = false;
route("GET", "/instance/security", () => ({ require_2fa: require2FA }));
route("PATCH", "/instance/security", (_, r) => {
  const v = !!((r.body ?? {}) as { require_2fa?: boolean }).require_2fa;
  if (v && !me().totp_enabled) throw new HttpError(400, "2단계 인증을 켠 뒤 작업 공간 요구 사항을 활성화하세요");
  require2FA = v;
  return { require_2fa: v };
});
route("POST", "/auth/accept-invite", () => [201, me()]);

route("GET", "/users", () => USERS);
route("GET", "/users/{uid}", ([uid]) => getUser(uid));
route("PATCH", "/users/{uid}", ([uid], r) => {
  const u = getUser(uid);
  const b = (r.body ?? {}) as { name?: string; role?: WorkspaceRole; disabled?: boolean; reset_2fa?: boolean };
  if (u.builtin && b.role && b.role !== "owner") throw new HttpError(400, "기본 admin 계정은 항상 owner입니다");
  if (b.name !== undefined) u.name = b.name;
  if (b.role) u.role = b.role;
  if (b.disabled !== undefined) u.disabled = b.disabled;
  if (b.reset_2fa) u.totp_enabled = false;
  audit("user.updated", u.id, `${u.email} 변경`);
  return u;
});
route("DELETE", "/users/{uid}", ([uid]) => {
  const u = getUser(uid);
  if (u.builtin) throw new HttpError(400, "기본 admin 계정은 지울 수 없습니다(비활성화는 할 수 있습니다)");
  USERS.splice(USERS.indexOf(u), 1);
  for (let i = MEMBERS.length - 1; i >= 0; i--) if (MEMBERS[i].user_id === uid) MEMBERS.splice(i, 1);
  audit("user.deleted", uid, `${u.email} 삭제`);
  return 204;
});
route("GET", "/invites", () => INVITES);
route("POST", "/invites", (_, r) => {
  const b = (r.body ?? {}) as { email?: string; role?: WorkspaceRole; project_id?: string; project_role?: ProjectRole };
  const email = (b.email ?? "").trim().toLowerCase();
  if (!email.includes("@")) throw new HttpError(400, "올바른 이메일이 필요합니다");
  if (USERS.some((u) => u.email === email)) throw new HttpError(409, "이미 있는 사용자입니다");
  const inv: Invite = {
    id: rid("inv"),
    email,
    role: b.role ?? "member",
    project_id: b.project_id ?? null,
    project_role: b.project_id ? (b.project_role ?? "member") : null,
    created_by: me().id,
    created_at: iso(now()),
    expires_at: iso(now() + 7 * 86400_000),
  };
  INVITES.unshift(inv);
  audit("invite.created", inv.id, `${email}를 ${inv.role}(으)로 초대`);
  const token = secret(43);
  return [201, { invite: inv, token, url: `${location.origin}/invite/#token=${token}` }];
});
route("DELETE", "/invites/{id}", ([id]) => {
  const i = INVITES.findIndex((x) => x.id === id);
  if (i < 0) throw new HttpError(404, "not found");
  INVITES.splice(i, 1);
  return 204;
});
route("GET", "/audit", () => AUDIT);
route("GET", "/tokens", () => TOKENS);
route("POST", "/tokens", (_, r) => {
  const b = (r.body ?? {}) as { name?: string; scope?: TokenScope; workspace_id?: string; project_id?: string; environment_id?: string; role?: ProjectRole; expires_in_days?: number | null };
  if (!b.name?.trim()) throw new HttpError(400, "name은 1..100자여야 합니다");
  const env = b.environment_id ? getEnv(b.environment_id) : null;
  const scope: TokenScope = b.scope === "instance" ? "instance" : env ? "environment" : b.project_id ? "project" : "workspace";
  if (scope === "environment" && b.role === "admin") throw new HttpError(400, "environment 토큰의 역할은 member 또는 viewer입니다");
  const token = `aven_${secret(40).toLowerCase()}`;
  const t: ApiToken = {
    id: rid("tok"),
    user_id: me().id,
    name: b.name.trim(),
    prefix: token.slice(0, 13),
    scope,
    workspace_id: scope === "instance" ? null : b.workspace_id ?? S.projects.find((p) => p.id === (env?.project_id ?? b.project_id))?.workspace_id ?? "ws_default",
    project_id: env?.project_id ?? b.project_id ?? null,
    environment_id: env?.id ?? null,
    role: b.role ?? "member",
    expires_at: b.expires_in_days ? iso(now() + b.expires_in_days * 86400_000) : null,
    last_used_at: null,
    created_at: iso(now()),
  };
  TOKENS.unshift(t);
  audit("token.created", t.id, `토큰 ${t.name} 생성`);
  return [201, { ...t, token }];
});
route("DELETE", "/tokens/{id}", ([id]) => {
  const i = TOKENS.findIndex((x) => x.id === id);
  if (i < 0) throw new HttpError(404, "not found");
  audit("token.revoked", id, `토큰 ${TOKENS[i].name} 폐기`);
  TOKENS.splice(i, 1);
  return 204;
});
route("GET", "/projects/{pid}/members", ([pid]) => {
  getProject(pid);
  return MEMBERS.filter((m) => m.project_id === pid).map((m) => {
    const u = getUser(m.user_id);
    return { ...m, email: u.email, name: u.name, workspace_role: u.role } satisfies Member;
  });
});
route("PUT", "/projects/{pid}/members/{uid}", ([pid, uid], r) => {
  getProject(pid);
  const u = getUser(uid);
  const role = ((r.body ?? {}) as { role?: ProjectRole }).role;
  if (role !== "admin" && role !== "member" && role !== "viewer") throw new HttpError(400, "role must be admin, member or viewer");
  const m = MEMBERS.find((x) => x.project_id === pid && x.user_id === uid);
  if (m) m.role = role;
  else MEMBERS.push({ project_id: pid, user_id: uid, role, created_at: iso(now()) });
  emit({ project_id: pid, environment_id: null, service_id: null, kind: "member.added", message: `${u.name}님을 ${role}(으)로 지정했습니다`, actor: me().name });
  return { project_id: pid, user_id: uid, role, created_at: iso(now()), email: u.email, name: u.name, workspace_role: u.role } satisfies Member;
});
route("DELETE", "/projects/{pid}/members/{uid}", ([pid, uid]) => {
  const i = MEMBERS.findIndex((x) => x.project_id === pid && x.user_id === uid);
  if (i < 0) throw new HttpError(404, "not found");
  MEMBERS.splice(i, 1);
  emit({ project_id: pid, environment_id: null, service_id: null, kind: "member.removed", message: `${getUser(uid).name}님을 프로젝트에서 내보냈습니다`, actor: me().name });
  return 204;
});

// Resource-policy mock for the admin UI. Runtime admission is tested in Go.
const RESOURCE_LIMITS: Record<string, ResourceLimits> = {};
function mockWorkspaceResources(w: MockWorkspace): WorkspaceResources {
  const projects = new Set(S.projects.filter((p) => p.workspace_id === w.id).map((p) => p.id));
  const services = new Set(S.services.filter((s) => projects.has(s.project_id)).map((s) => s.id));
  const instances = S.instances.filter((i) => services.has(i.service_id));
  const volumes = S.volumes.filter((v) => projects.has(v.project_id));
  const allocated: WorkspaceResources["allocated"] = {
    cpu_cores: 0, memory_mb: 0, volume_mb: 0, unlimited_cpu: 0, unlimited_memory: 0, unlimited_volume: 0,
    instances: instances.length, sandboxes: 0, volumes: volumes.length,
  };
  for (const i of instances) {
    const n = Math.max(1, i.replicas, i.current_replicas ?? 0, i.placement?.reduce((n, p) => n + p.replicas, 0) ?? 0, i.autoscale?.enabled ? i.autoscale.max_replicas : 0);
    if (i.cpu_limit && i.cpu_limit > 0) allocated.cpu_cores += i.cpu_limit * n;
    else allocated.unlimited_cpu++;
    if (i.memory_limit_mb && i.memory_limit_mb > 0) allocated.memory_mb += i.memory_limit_mb * n;
    else allocated.unlimited_memory++;
  }
  for (const v of volumes) {
    if (v.size_limit_mb && v.size_limit_mb > 0) allocated.volume_mb += v.size_limit_mb;
    else allocated.unlimited_volume++;
  }
  return { workspace: w, limits: RESOURCE_LIMITS[w.id] ?? { cpu_cores: null, memory_mb: null, volume_mb: null }, allocated };
}
function requireInstanceAdmin() {
  if (me().role !== "owner" && me().role !== "admin") throw new HttpError(403, "need instance admin role");
}
// ── 서버 owner 전용(api/owner.go) ──
const rel = (ms: number) => iso(now() + ms);
const OWNER_CAPS: { id: AdminCapID; label: string }[] = [
  { id: "users", label: "사용자·초대" }, { id: "resources", label: "자원 제한" }, { id: "nodes", label: "서버 노드" },
  { id: "security", label: "보안(2단계 인증 정책, 가드레일)" }, { id: "sso", label: "SAML SSO" }, { id: "oauth_apps", label: "OAuth 앱" },
  { id: "notifications", label: "전체 알림·SMTP" }, { id: "audit", label: "감사 기록" }, { id: "agent", label: "Aven Agent 설정" },
  { id: "backups", label: "백업 저장소" }, { id: "usage", label: "서버 사용량" },
];
const OWNER = {
  disabled: [] as AdminCapID[],
  maintenance: { enabled: false, message: "", block_logins: false, pause_webhooks: false, started_at: null as string | null, started_by: "" },
  announcement: { enabled: false, message: "", level: "info" as AnnouncementLevel, updated_at: null as string | null },
  backups: [{ name: "aven-pre-permissions-20261022-094224.db", bytes: 261222400, modified_at: "2026-10-22T09:42:24Z" }] as AvenBackup[],
};
function requireOwner() {
  if (me().role !== "owner") throw new HttpError(403, "권한이 없습니다");
}
route("GET", "/instance/notice", () => ({
  announcement: OWNER.announcement.enabled ? OWNER.announcement : null,
  maintenance: OWNER.maintenance, exempt: me().role === "owner",
}));
route("GET", "/owner", () => {
  requireOwner();
  return {
    owners: USERS.filter((u) => u.role === "owner").map((u) => ({ ...u, superadmin: false, source: u.builtin ? "builtin" : "role", last_login_at: u.created_at })),
    superadmins: [], maintenance: OWNER.maintenance, announcement: OWNER.announcement,
    admin_caps: OWNER_CAPS.map((c) => ({ ...c, allowed: !OWNER.disabled.includes(c.id) })),
    counts: { sessions: USERS.length, api_tokens: 2, users: USERS.length },
    server: {
      version: "mock", node: "eu", go_version: "go1.26", pid: 4242, started_at: rel(-86400_000 * 2), uptime_seconds: 86400 * 2 + 3600,
      systemd: true, data_dir: "/var/lib/aven", db_name: "aven.db", db_bytes: 261222400, public_url: "https://aven.example.com",
      admin_password_set: true, legacy_api_token_set: false, github_webhook_secret_set: true, github_oauth_configured: true,
      legacy_agents_disabled: false, secure_cookie: true, trusted_proxies: 1, smtp_configured: true,
    },
  };
});
const SETUP = { dismissed_at: null as string | null };
const SELF = { enabled: false };
const selfView = () => ({
  enabled: SELF.enabled, from_env: false,
  ...(SELF.enabled ? { service_id: "svc_aven", service_name: "aven-server", project_id: "prj_aven", environment_id: "env_aven", repo_url: "https://github.com/turin-dev/aven", branch: "main" } : {}),
  server: { mode: "systemd", unit: "aven-server", version: "mock" },
});
route("GET", "/owner/self-service", () => { requireOwner(); return selfView(); });
route("POST", "/owner/self-service", () => { requireOwner(); SELF.enabled = true; return selfView(); });
route("DELETE", "/owner/self-service", () => { requireOwner(); SELF.enabled = false; return selfView(); });
route("GET", "/owner/setup", () => {
  requireOwner();
  const items = [
    { id: "public_url", done: true, optional: false }, { id: "base_domain", done: true, optional: false },
    { id: "owner_account", done: USERS.some((u) => u.role === "owner" && !u.builtin), optional: false },
    { id: "mfa", done: Boolean(me().totp_enabled), optional: false },
    { id: "github", done: true, optional: true }, { id: "smtp", done: true, optional: true },
    { id: "backup_destination", done: false, optional: true }, { id: "node", done: false, optional: true },
    { id: "first_project", done: true, optional: false },
    { id: "self_service", done: SELF.enabled, optional: true },
  ];
  const required = items.filter((i) => !i.optional);
  const done = required.filter((i) => i.done).length;
  return {
    items, required: required.length, required_done: done, complete: done === required.length,
    dismissed: SETUP.dismissed_at !== null, dismissed_at: SETUP.dismissed_at,
    server: { public_url: "https://aven.example.com", base_domain: "example.com",
      github_callback_url: "https://aven.example.com/api/github/oauth/callback", github_webhook_url: "https://aven.example.com/api/webhooks/github" },
  };
});
route("PUT", "/owner/setup", (_p, r) => {
  requireOwner();
  const dismissed = Boolean((r.body as { dismissed?: boolean } | undefined)?.dismissed);
  SETUP.dismissed_at = dismissed ? rel(0) : null;
  return { dismissed };
});
route("PUT", "/owner/admin-caps", (_p, r) => { requireOwner(); OWNER.disabled = ((r.body ?? {}) as { disabled?: AdminCapID[] }).disabled ?? []; return { disabled: OWNER.disabled }; });
route("PUT", "/owner/maintenance", (_p, r) => {
  requireOwner();
  const b = r.body as typeof OWNER.maintenance;
  OWNER.maintenance = { ...b, started_at: b.enabled ? OWNER.maintenance.started_at ?? rel(0) : null, started_by: b.enabled ? me().name || me().email : "" };
  return OWNER.maintenance;
});
route("PUT", "/owner/announcement", (_p, r) => { requireOwner(); OWNER.announcement = { ...(r.body as typeof OWNER.announcement), updated_at: rel(0) }; return OWNER.announcement; });
route("GET", "/owner/sessions", () => {
  requireOwner();
  return USERS.filter((u) => !u.disabled).map((u, i) => ({
    id: `ses_${u.id}`, user_id: u.id, created_at: rel(-3600_000 * (i + 2)), expires_at: rel(86400_000), last_seen: rel(-60_000 * (i + 1)),
    ip: `203.0.113.${i + 10}`, user_agent: "Mozilla/5.0", current: u.id === me().id, saml_authenticated: false,
    email: u.email, name: u.name, user_role: u.role, builtin: u.builtin, disabled: u.disabled,
  }));
});
route("DELETE", "/owner/sessions/{sesid}", () => { requireOwner(); return undefined; });
route("POST", "/owner/sessions/revoke", () => { requireOwner(); return { revoked: USERS.length - 1 }; });
route("GET", "/owner/tokens", () => {
  requireOwner();
  return USERS.slice(0, 2).map((u, i) => ({
    id: `tok_${i}`, user_id: u.id, name: i ? "github-actions" : "ops", prefix: `aven_${i}ab3`, scope: i ? "workspace" : "instance", workspace_id: null,
    project_id: null, environment_id: null, role: "admin", expires_at: null, last_used_at: rel(-7200_000), created_at: rel(-86400_000 * 9),
    email: u.email, user_name: u.name, user_role: u.role,
  }));
});
route("DELETE", "/owner/tokens/{tid}", () => { requireOwner(); return undefined; });
route("POST", "/owner/tokens/revoke", () => { requireOwner(); return { revoked: 1 }; });
route("POST", "/owner/transfer", (_p, r) => {
  requireOwner();
  const b = r.body as { user_id: string; stay_owner: boolean };
  const u = USERS.find((x) => x.id === b.user_id);
  if (!u) throw new HttpError(404, "not found");
  u.role = "owner";
  return { owner: u.id, still_owner: true };
});
route("POST", "/owner/lockdown", (_p, r) => {
  requireOwner();
  OWNER.maintenance = { enabled: true, message: ((r.body ?? {}) as { message?: string }).message || "보안 점검을 위해 서버를 잠갔습니다. 서버 owner에게 문의하세요.", block_logins: true, pause_webhooks: true, started_at: rel(0), started_by: me().name };
  return { sessions_revoked: USERS.length - 1, tokens_revoked: 1 };
});
route("GET", "/owner/backups", () => { requireOwner(); return OWNER.backups; });
route("POST", "/owner/backups", () => {
  requireOwner();
  const b = { name: `aven-owner-${new Date().toISOString().replace(/[-:]/g, "").slice(0, 15).replace("T", "-")}.db`, bytes: 262000000, modified_at: rel(0) };
  OWNER.backups = [b, ...OWNER.backups];
  return b;
});
route("DELETE", "/owner/backups/{name}", ([name]) => { requireOwner(); OWNER.backups = OWNER.backups.filter((b) => b.name !== name); return undefined; });
route("POST", "/owner/restart", () => { requireOwner(); return { restarting: true }; });

route("GET", "/instance/overview", () => {
  requireInstanceAdmin();
  return {
    users: {
      total: USERS.length, active: USERS.filter((u) => !u.disabled).length, disabled: USERS.filter((u) => u.disabled).length,
      admins: USERS.filter((u) => !u.disabled && (u.role === "owner" || u.role === "admin")).length,
    },
    pending_invites: INVITES.filter((i) => Date.parse(i.expires_at) > now()).length,
    workspaces: WORKSPACES.map((w) => {
      const projects = S.projects.filter((p) => p.workspace_id === w.id);
      const ids = new Set(projects.map((p) => p.id));
      const services = S.services.filter((s) => ids.has(s.project_id));
      const sids = new Set(services.map((s) => s.id));
      const instances = S.instances.filter((i) => sids.has(i.service_id));
      return { ...w, members: WS_MEMBERS.filter((m) => m.workspace_id === w.id).length, projects: projects.length, services: services.length,
        instances: instances.length, unhealthy_instances: instances.filter((i) => i.status === "failed" || i.status === "crashed").length };
    }),
    nodes: NODES.map(({ id, name, status, is_local, schedulable }) => ({ id, name, status, is_local, schedulable })),
    generated_at: iso(now()),
  } satisfies InstanceOverview;
});
route("GET", "/instance/resource-limits", () => {
  requireInstanceAdmin();
  return WORKSPACES.map(mockWorkspaceResources);
});
route("PUT", "/instance/resource-limits/{wsid}", ([wsid], r) => {
  requireInstanceAdmin();
  const w = getWorkspace(wsid);
  const body = (r.body ?? {}) as Partial<ResourceLimits>;
  const limits: ResourceLimits = { cpu_cores: body.cpu_cores ?? null, memory_mb: body.memory_mb ?? null, volume_mb: body.volume_mb ?? null };
  const { allocated: a } = mockWorkspaceResources(w);
  for (const [key, used, unlimited, min, max, integer] of [
    ["cpu_cores", a.cpu_cores, a.unlimited_cpu, .01, 65536, false],
    ["memory_mb", a.memory_mb, a.unlimited_memory, 6, 2 ** 30, true],
    ["volume_mb", a.volume_mb, a.unlimited_volume, 1, 2 ** 30, true],
  ] as const) {
    const v = limits[key];
    if (v === null) continue;
    if (!Number.isFinite(v) || v < 0 || v > max || (v > 0 && v < min) || (integer && !Number.isInteger(v))) throw new HttpError(400, `${key}: 잘못된 한도입니다`);
    if (unlimited || used > v) throw new HttpError(409, "현재 할당량보다 낮거나 개별 제한이 없는 항목이 있습니다");
  }
  RESOURCE_LIMITS[wsid] = limits;
  audit("workspace.resource_limits.updated", wsid, "작업 공간 자원 한도를 변경했습니다");
  return mockWorkspaceResources(w);
});
route("GET", "/workspaces/{wsid}/resource-limits", ([wsid]) => {
  const w = getWorkspace(wsid);
  if (!wsRole(w)) throw new HttpError(404, "workspace not found");
  return mockWorkspaceResources(w);
});

route("GET", "/workspaces", () => visibleWorkspaces());
route("POST", "/workspaces", (_, r) => {
  const name = ((r.body ?? {}) as { name?: string }).name?.trim();
  if (!name) throw new HttpError(400, "name is required");
  const w: MockWorkspace = { id: rid("ws"), name, personal: false, owner_id: me().id, created_at: iso(now()), default: false };
  WORKSPACES.push(w);
  WS_MEMBERS.push({ workspace_id: w.id, user_id: me().id, role: "owner", created_at: iso(now()) });
  return [201, wsView(w)];
});
route("PATCH", "/workspaces/{wsid}", ([wsid], r) => {
  const w = getWorkspace(wsid);
  if (wsRole(w) !== "owner" && wsRole(w) !== "admin") throw new HttpError(403, "need admin role");
  const name = ((r.body ?? {}) as { name?: string }).name?.trim();
  if (name) w.name = name;
  return wsView(w);
});
route("DELETE", "/workspaces/{wsid}", ([wsid]) => {
  const w = getWorkspace(wsid);
  if (w.default || w.personal) throw new HttpError(400, "cannot delete the default or a personal workspace");
  if (wsRole(w) !== "owner") throw new HttpError(403, "need owner role");
  if (S.projects.some((p) => p.workspace_id === wsid)) throw new HttpError(409, "workspace still has projects; move or delete them first");
  const i = WORKSPACES.findIndex((x) => x.id === wsid);
  WORKSPACES.splice(i, 1);
  for (let j = WS_MEMBERS.length - 1; j >= 0; j--) if (WS_MEMBERS[j].workspace_id === wsid) WS_MEMBERS.splice(j, 1);
  return 204;
});
route("GET", "/workspaces/{wsid}/members", ([wsid]) => {
  const w = getWorkspace(wsid);
  return WS_MEMBERS.filter((m) => m.workspace_id === wsid).map((m) => {
    const u = getUser(m.user_id);
    return { ...m, email: u.email, name: u.name, disabled: u.disabled } satisfies WorkspaceMember;
  });
});
route("POST", "/workspaces/{wsid}/members", ([wsid], r) => {
  const w = getWorkspace(wsid);
  if (wsRole(w) !== "owner" && wsRole(w) !== "admin") throw new HttpError(403, "need admin role");
  const b = (r.body ?? {}) as { email?: string; role?: WorkspaceRole };
  const email = b.email?.trim().toLowerCase();
  const role = b.role ?? "member";
  if (!email) throw new HttpError(400, "email is required");
  const u = USERS.find((x) => x.email.toLowerCase() === email);
  if (!u) throw new HttpError(404, "no aven user with that email; invite them first");
  if (WS_MEMBERS.some((m) => m.workspace_id === wsid && m.user_id === u.id)) throw new HttpError(409, "already a member");
  const m = { workspace_id: wsid, user_id: u.id, role, created_at: iso(now()) };
  WS_MEMBERS.push(m);
  return [201, { ...m, email: u.email, name: u.name, disabled: u.disabled } satisfies WorkspaceMember];
});
route("PATCH", "/workspaces/{wsid}/members/{uid}", ([wsid, uid], r) => {
  const w = getWorkspace(wsid);
  if (wsRole(w) !== "owner" && wsRole(w) !== "admin") throw new HttpError(403, "need admin role");
  const role = ((r.body ?? {}) as { role?: WorkspaceRole }).role;
  if (role !== "admin" && role !== "member" && role !== "viewer") throw new HttpError(400, "role must be admin, member or viewer");
  const m = WS_MEMBERS.find((x) => x.workspace_id === wsid && x.user_id === uid);
  if (!m) throw new HttpError(404, "not found");
  m.role = role;
  const u = getUser(uid);
  return { ...m, email: u.email, name: u.name, disabled: u.disabled } satisfies WorkspaceMember;
});
route("DELETE", "/workspaces/{wsid}/members/{uid}", ([wsid, uid]) => {
  const w = getWorkspace(wsid);
  const self = uid === me().id;
  if (!self && wsRole(w) !== "owner" && wsRole(w) !== "admin") throw new HttpError(403, "need admin role");
  if (w.personal && w.owner_id === uid) throw new HttpError(400, "cannot remove the personal workspace owner");
  const i = WS_MEMBERS.findIndex((x) => x.workspace_id === wsid && x.user_id === uid);
  if (i < 0) throw new HttpError(404, "not found");
  WS_MEMBERS.splice(i, 1);
  return 204;
});

route("GET", "/projects", (_, r) => {
  const wsid = r.query.get("workspace");
  return wsid ? S.projects.filter((p) => p.workspace_id === wsid) : S.projects;
});
route("POST", "/projects", (_, r) => {
  const b = r.body as { name: string; description?: string; workspace_id?: string };
  if (!b?.name?.trim()) throw new HttpError(400, "name is required");
  const wsid = b.workspace_id?.trim() || "ws_default";
  const w = WORKSPACES.find((x) => x.id === wsid);
  if (!w) throw new HttpError(404, "not a member of that workspace");
  if (!wsRole(w)) throw new HttpError(404, "workspace not found");
  if (wsRole(w) === "viewer") throw new HttpError(403, "need member role in the target workspace to create projects");
  if (S.projects.some((x) => x.workspace_id === wsid && x.name === b.name.trim())) throw new HttpError(409, "a project with that name already exists in this workspace");
  const p: Project = {
    id: rid("prj"),
    name: b.name.trim(),
    description: b.description ?? "",
    created_at: iso(now()),
    workspace_id: wsid,
    github_user_id: null,
    default_environment_id: "",
    notify_webhook_url: null,
    notify_events: [],
    pr_environments: { ...PR_DEFAULTS },
    pr_from_forks: false,
    pr_from_bots: false,
  };
  const env: Environment = { id: rid("env"), project_id: p.id, name: "production", created_at: p.created_at, ...STANDARD_ENV };
  p.default_environment_id = env.id;
  S.projects.push(p);
  S.envs.push(env);
  S.shared[env.id] = {};
  return [201, p];
});
route("GET", "/projects/{pid}", ([pid]) => ({
  ...mockProjectView(getProject(pid)),
  environments: S.envs.filter((e) => e.project_id === pid),
  services: S.services.filter((s) => s.project_id === pid),
  github_account: githubAccountFor(getProject(pid).github_user_id),
}));
route("PUT", "/projects/{pid}/workspace", ([pid], r) => {
  const p = getProject(pid);
  const wsid = ((r.body ?? {}) as { workspace_id?: string }).workspace_id?.trim();
  if (!wsid) throw new HttpError(400, "workspace_id is required");
  const w = getWorkspace(wsid);
  if (!wsRole(w)) throw new HttpError(404, "workspace not found");
  if (wsRole(w) === "viewer") throw new HttpError(403, "need member role in the target workspace");
  if (S.projects.some((x) => x.id !== pid && x.workspace_id === wsid && x.name === p.name)) throw new HttpError(409, "a project with that name already exists in the target workspace");
  p.workspace_id = wsid;
  return mockProjectView(p);
});
route("PUT", "/projects/{pid}/github", ([pid], r) => {
  const p = getProject(pid);
  const userID = ((r.body ?? {}) as { user_id?: string | null }).user_id ?? null;
  if (userID !== null && userID !== me().id) throw new HttpError(403, "you can only use your own GitHub connection");
  if (userID !== null && !GITHUB_CONN[userID]) throw new HttpError(409, "GitHub is not connected");
  p.github_user_id = userID;
  return { github_account: githubAccountFor(userID) };
});
route("PATCH", "/projects/{pid}", ([pid], r) => {
  const p = getProject(pid);
  const b = { ...(r.body as Partial<Project>) };
  if (b.pr_environments) {
    const next = { ...(p.pr_environments ?? PR_DEFAULTS), ...b.pr_environments };
    if (next.base_environment_id) {
      const base = S.envs.find((e) => e.id === next.base_environment_id);
      if (!base || base.project_id !== pid || base.kind === "preview")
        throw new HttpError(400, "pr_environments.base_environment_id must be a standard environment of this project");
    }
    b.pr_environments = next;
  }
  if ("usage_limit" in b) mockSetUsageLimit(pid, b.usage_limit); // v1.5 hard limit (mock section at the end)
  delete b.usage_limit;
  delete b.usage_suspension;
  return mockProjectView(Object.assign(p, b, { id: pid }));
});
route("DELETE", "/projects/{pid}", ([pid]) => {
  getProject(pid);
  S.projects = S.projects.filter((p) => p.id !== pid);
  const sids = new Set(S.services.filter((s) => s.project_id === pid).map((s) => s.id));
  S.services = S.services.filter((s) => s.project_id !== pid);
  S.instances = S.instances.filter((i) => !sids.has(i.service_id));
  S.envs = S.envs.filter((e) => e.project_id !== pid);
  S.volumes = S.volumes.filter((v) => v.project_id !== pid);
  return 204;
});
route("POST", "/projects/{pid}/environments", ([pid], r) => {
  getProject(pid);
  const b = r.body as { name: string; copy_from?: string };
  const name = b?.name?.trim();
  if (!name || !/^[a-z0-9-]+$/.test(name)) throw new HttpError(400, "environment name must match [a-z0-9-]");
  if (S.envs.some((e) => e.project_id === pid && e.name === name)) throw new HttpError(409, `environment "${name}" already exists`);
  const env: Environment = { id: rid("env"), project_id: pid, name, created_at: iso(now()), ...STANDARD_ENV };
  S.envs.push(env);
  forkMockEnv(pid, env, b.copy_from);
  emit({ project_id: pid, environment_id: env.id, service_id: null, kind: "environment.created", message: `${name} 환경 생성` });
  return [201, env];
});
route("PATCH", "/environments/{eid}", ([eid], r) => {
  const env = getEnv(eid);
  const name = (r.body as { name: string }).name?.trim();
  if (!name || !/^[a-z0-9-]+$/.test(name)) throw new HttpError(400, "environment name must match [a-z0-9-]");
  if (S.envs.some((e) => e.project_id === env.project_id && e.name === name && e.id !== eid)) throw new HttpError(409, `environment "${name}" already exists`);
  env.name = name;
  for (const i of S.instances.filter((x) => x.environment_id === eid)) i.private_domain = `${getService(i.service_id).name}.${name}.aven.internal`;
  return env;
});
route("DELETE", "/environments/{eid}", ([eid]) => {
  const env = getEnv(eid);
  if (S.envs.filter((e) => e.project_id === env.project_id).length <= 1) throw new HttpError(409, "마지막 환경은 삭제할 수 없습니다");
  const p = getProject(env.project_id);
  S.envs = S.envs.filter((e) => e.id !== eid);
  S.instances = S.instances.filter((i) => i.environment_id !== eid);
  S.volumes = S.volumes.filter((v) => v.environment_id !== eid);
  if (p.default_environment_id === eid) p.default_environment_id = S.envs.find((e) => e.project_id === p.id)!.id;
  return 204;
});

route("GET", "/environments/{eid}/canvas", ([eid]) => {
  const env = getEnv(eid);
  const project = getProject(env.project_id);
  const services = S.services
    .filter((s) => s.project_id === project.id)
    .map((s) => {
      const inst = S.instances.find((i) => i.service_id === s.id && i.environment_id === eid);
      return inst ? canvasService(s, inst) : null;
    })
    .filter(Boolean);
  return { environment: env, project: mockProjectView(project), services, groups: mockGroups(eid) };
});

route("POST", "/projects/{pid}/services", ([pid], r) => [201, mockCreateService(pid, r.body as Parameters<typeof createService>[1] & { code?: string })]);
route("PATCH", "/services/{sid}", ([sid], r) => {
  const svc = getService(sid);
  const b = r.body as { name?: string; icon?: string | null };
  if (b.name !== undefined) {
    if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(b.name)) throw new HttpError(400, "name must match [a-z0-9-]");
    if (S.services.some((s) => s.project_id === svc.project_id && s.name === b.name && s.id !== sid)) throw new HttpError(409, `service "${b.name}" already exists`);
    svc.name = b.name;
    for (const i of S.instances.filter((x) => x.service_id === sid)) i.private_domain = `${b.name}.${getEnv(i.environment_id).name}.aven.internal`;
  }
  if (b.icon !== undefined) svc.icon = b.icon;
  return svc;
});
route("DELETE", "/services/{sid}", ([sid]) => {
  const svc = getService(sid);
  const iids = new Set(S.instances.filter((i) => i.service_id === sid).map((i) => i.id));
  S.services = S.services.filter((s) => s.id !== sid);
  S.instances = S.instances.filter((i) => i.service_id !== sid);
  S.volumes = S.volumes.filter((v) => !v.service_instance_id || !iids.has(v.service_instance_id));
  emit({ project_id: svc.project_id, environment_id: null, service_id: null, kind: "service.deleted", message: `${svc.name} 삭제` });
  return 204;
});
route("GET", "/services/{sid}/instances/{eid}", ([sid, eid]) => getInstance(sid, eid));
route("GET", "/services/{sid}/instances/{eid}/admin", ([sid, eid]) => {
  const svc = getService(sid);
  const inst = getInstance(sid, eid);
  const p = getProject(svc.project_id);
  const env = getEnv(eid);
  const ws = WORKSPACES.find((w) => w.id === p.workspace_id);
  const running = inst.status === "online";
  return {
    workspace: { id: p.workspace_id, name: ws?.name ?? "" },
    project: { id: p.id, name: p.name },
    environment: { id: env.id, name: env.name },
    service_id: svc.id,
    instance_id: inst.id,
    current_deployment_id: inst.current_deployment_id,
    node: null,
    containers: inst.current_deployment_id
      ? [{
          name: `aven-${svc.name}-${inst.current_deployment_id.slice(-6)}`, role: "app", deployment_id: inst.current_deployment_id, replica: "0",
          status: running ? "running" : "exited", running, exit_code: running ? 0 : 1, restart_count: 0,
          started_at: inst.updated_at, ips: { "aven-net": ["fd00:6b::12", "10.30.0.12"] }, volumes: [],
        }]
      : [],
    holds: MOCK_SERVICE_HOLDS.filter((h) => h.target_id === sid),
    audit: [],
    audit_hidden: false,
    can_control: true,
  };
});
const MOCK_SERVICE_HOLDS: { scope: "service" | "deploy"; target_id: string; reason: string; created_by: string; created_at: string; stopped_instances: string[] }[] = [];
route("POST", "/services/{sid}/admin/holds", ([sid], r) => {
  const b = (r.body ?? {}) as { scope: "service" | "deploy"; reason?: string };
  if (MOCK_SERVICE_HOLDS.some((h) => h.target_id === sid && h.scope === b.scope)) throw new HttpError(409, "이미 배포가 막혀 있습니다");
  const hold = { scope: b.scope, target_id: sid, reason: b.reason ?? "", created_by: "admin", created_at: new Date().toISOString(), stopped_instances: [] };
  MOCK_SERVICE_HOLDS.push(hold);
  for (const i of S.instances) if (i.service_id === sid) (i as { admin_hold?: unknown }).admin_hold = hold;
  return { hold, failed: {} };
});
route("DELETE", "/services/{sid}/admin/holds/{scope}", ([sid, scope]) => {
  const i = MOCK_SERVICE_HOLDS.findIndex((h) => h.target_id === sid && h.scope === scope);
  if (i < 0) throw new HttpError(404, "not found");
  MOCK_SERVICE_HOLDS.splice(i, 1);
  const rest = MOCK_SERVICE_HOLDS.find((h) => h.target_id === sid) ?? null;
  for (const x of S.instances) if (x.service_id === sid) (x as { admin_hold?: unknown }).admin_hold = rest;
  return { restarted: [], failed: {} };
});
route("PATCH", "/services/{sid}/instances/{eid}", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  const b = { ...(r.body as Record<string, unknown>) };
  const vp = b.variables_patch as Record<string, string | null> | undefined;
  delete b.variables_patch;
  // 봉인(sealed) 변수: mock은 값을 "<sealed>"로만 들고 있는다(서버처럼 다시 읽을 수 없다).
  const sealAdd = b.sealed_variables as string[] | undefined;
  delete b.sealed_variables;
  if (sealAdd !== undefined) {
    if (!Array.isArray(sealAdd)) throw new HttpError(400, "sealed_variables must be an array of variable names");
    const nextKeys = new Set(Object.keys((b.variables as Record<string, string> | undefined) ?? inst.variables));
    for (const [k, v] of Object.entries(vp ?? {})) v === null ? nextKeys.delete(k) : nextKeys.add(k);
    for (const k of sealAdd) if (!nextKeys.has(k)) throw new HttpError(400, `cannot seal ${k}: no such variable`);
  }
  const settingsChanged = Object.keys(b).some((k) => k !== "variables");
  if (b.replicas !== undefined && (Number(b.replicas) < 1 || Number(b.replicas) > 10)) throw new HttpError(400, "replicas must be between 1 and 10");
  if (b.sleep_after_s !== undefined && (!Number.isInteger(b.sleep_after_s) || Number(b.sleep_after_s) < 10 || Number(b.sleep_after_s) > 604800))
    throw new HttpError(400, "sleep_after_s must be 10..604800");
  if (b.overlap_seconds !== undefined && (!Number.isInteger(b.overlap_seconds) || Number(b.overlap_seconds) < 0 || Number(b.overlap_seconds) > 600))
    throw new HttpError(400, "overlap_seconds must be 0..600");
  if (b.draining_seconds !== undefined && b.draining_seconds !== null && (!Number.isInteger(b.draining_seconds) || Number(b.draining_seconds) < 0 || Number(b.draining_seconds) > 300))
    throw new HttpError(400, "draining_seconds must be 0..300 or null");
  if (b.node_id !== undefined && b.node_id !== inst.node_id) {
    const n = NODES.find((x) => x.id === b.node_id);
    if (!n) throw new HttpError(400, "unknown node");
    if (!n.schedulable) throw new HttpError(400, `${n.name} 노드는 새 배포를 받지 않습니다`);
    if (S.volumes.some((v) => v.service_instance_id === inst.id) && r.query.get("move_volumes") !== "1")
      throw new HttpError(400, "attached volumes would be orphaned; pass move_volumes=1");
  }
  // v1.5: autoscale(바로 적용)과 placement(다음 배포부터) 검증 — 서버와 같은 규칙.
  if (b.autoscale !== undefined && b.autoscale !== null) {
    const a = { scale_up_cooldown_s: 60, scale_down_cooldown_s: 300, target_cpu_percent: null, target_memory_percent: null, target_rps_per_replica: null, ...(b.autoscale as object) } as NonNullable<ServiceInstance["autoscale"]>;
    if (!a.min_replicas) a.min_replicas = 1;
    if (a.min_replicas < 1 || a.min_replicas > 10) throw new HttpError(400, "autoscale.min_replicas must be 1..10");
    if (a.max_replicas < a.min_replicas || a.max_replicas > 10) throw new HttpError(400, "autoscale.max_replicas must be min_replicas..10");
    if (a.enabled && a.target_cpu_percent == null && a.target_memory_percent == null && a.target_rps_per_replica == null)
      throw new HttpError(400, "autoscale needs at least one of target_cpu_percent, target_memory_percent, target_rps_per_replica");
    b.autoscale = a;
  }
  if (b.placement !== undefined) {
    const ps = (b.placement as { node_id: string; replicas: number }[] | null) ?? [];
    const seen = new Set<string>();
    for (const p of ps) {
      if (!NODES.some((n) => n.id === p.node_id)) throw new HttpError(400, `node ${p.node_id} does not exist`);
      if (seen.has(p.node_id)) throw new HttpError(400, `placement lists node ${p.node_id} twice`);
      seen.add(p.node_id);
      if (!(p.replicas >= 1 && p.replicas <= 10)) throw new HttpError(400, "placement replicas must be 1..10");
    }
    const total = ps.reduce((n, p) => n + p.replicas, 0);
    if (total > 10) throw new HttpError(400, `placement replicas add up to ${total} (max 10)`);
    if (ps.length > 1 && S.volumes.some((v) => v.service_instance_id === inst.id))
      throw new HttpError(400, "services with volumes cannot be placed on several nodes");
    if (ps.length > 1 && (b.cron_schedule ?? inst.cron_schedule)) throw new HttpError(400, "cron services cannot be placed on several nodes");
    if (ps.length > 1 && (b.sleep_when_idle ?? inst.sleep_when_idle)) throw new HttpError(400, "sleep_when_idle cannot be used with a multi-node placement");
    b.placement = ps.length ? ps : null;
    if (ps.length) {
      b.replicas = total;
      b.node_id = ps[0].node_id;
    }
  }
  if (b.private_dns_name !== undefined && b.private_dns_name !== null && b.private_dns_name !== "") {
    const err = privateNameError(inst, String(b.private_dns_name));
    if (err && b.private_dns_name !== getService(sid).name) throw new HttpError(400, `private_dns_name: ${err}`);
  }
  if (b.outbound_ipv6 !== undefined && b.outbound_ipv6 !== null && typeof b.outbound_ipv6 !== "boolean") throw new HttpError(400, "outbound_ipv6 must be a boolean");
  Object.assign(inst, b);
  if (b.autoscale !== undefined) inst.autoscale_status = mockAutoscaleStatus(inst);
  if (vp) for (const [k, v] of Object.entries(vp)) v === null ? delete inst.variables[k] : (inst.variables[k] = v);
  applySeal(inst, sealAdd ?? []);
  const keys = [...Object.keys(b).filter((k) => k !== "variables" && k !== "autoscale" && k !== "outbound_ipv6"), ...Object.keys(vp ?? {}).map((k) => `variables.${k}`)];
  if (inst.current_deployment_id) inst.pending_changes = [...new Set([...inst.pending_changes, ...keys])];
  inst.updated_at = iso(now());
  const svc = getService(sid);
  if (vp || b.variables)
    emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "variables.updated", message: `변수 ${Object.keys(vp ?? b.variables ?? {}).length}개 변경` });
  if (r.query.get("deploy") === "1") {
    const cur = inst.current_deployment_id ? S.deployments.find((d) => d.id === inst.current_deployment_id) : undefined;
    startDeployment(inst, settingsChanged ? "manual" : "variables", !settingsChanged ? cur : undefined);
  }
  return inst;
});
route("PUT", "/services/{sid}/instances/{eid}/canvas", ([sid, eid], r) => {
  const b = r.body as { x: number; y: number };
  getInstance(sid, eid).canvas = { x: Math.round(b.x), y: Math.round(b.y) };
  return 204;
});
route("GET", "/services/{sid}/instances/{eid}/variables/resolved", ([sid, eid]) => resolveTyped(getInstance(sid, eid)));

/* ── v1.5: sealed variables + raw editor ─────────────────────────────── */

/** 봉인 목록을 정리한다: 지운 키는 빠지고, 봉인된 키(새로 봉인한 키 포함)의 값은 "<sealed>"만 남긴다. */
function applySeal(inst: ServiceInstance, add: string[]) {
  const sealed = new Set([...(inst.sealed_variables ?? []), ...add].filter((k) => k in inst.variables));
  for (const k of sealed) inst.variables[k] = SEALED_VALUE;
  inst.sealed_variables = [...sealed].sort();
}
route("GET", "/services/{sid}/instances/{eid}/variables/raw", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  const f = r.query.get("format") || "env";
  if (f !== "env" && f !== "json" && f !== "dotenv") throw new HttpError(400, "format must be env or json");
  const format = f === "json" ? "json" : "env";
  const sealed = inst.sealed_variables ?? [];
  return { format, content: renderRaw(format, inst.variables, sealed), sealed_variables: sealed };
});
route("PUT", "/services/{sid}/instances/{eid}/variables/raw", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  const b = r.body as { format?: string; content?: string };
  const format = b.format === "json" ? "json" : b.format === undefined || b.format === "" || b.format === "env" || b.format === "dotenv" ? "env" : null;
  if (!format) throw new HttpError(400, "format must be env or json");
  let next: Record<string, string>;
  try {
    next = parseRaw(format, b.content ?? "");
  } catch (e) {
    throw new HttpError(400, (e as Error).message);
  }
  // 편집기에 나오지 않는 봉인 변수는 그대로 남는다.
  for (const k of inst.sealed_variables ?? []) if (!(k in next) || next[k] === SEALED_VALUE) next[k] = inst.variables[k];
  const changed = new Set([...Object.keys(next), ...Object.keys(inst.variables)].filter((k) => next[k] !== inst.variables[k]));
  inst.variables = next;
  applySeal(inst, []);
  if (inst.current_deployment_id) inst.pending_changes = [...new Set([...inst.pending_changes, ...[...changed].map((k) => `variables.${k}`)])];
  inst.updated_at = iso(now());
  const svc = getService(sid);
  emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "variables.updated", message: "raw 편집기로 변수를 변경했습니다" });
  return inst;
});

/* ── v1.5: environment sync ─────────────────────────────────────────── */

const SYNC_FIELDS = [
  "repo_url", "branch", "root_dir", "image", "builder", "dockerfile_path", "watch_paths", "auto_deploy", "start_command",
  "pre_deploy_command", "port", "healthcheck_type", "healthcheck_path", "healthcheck_timeout_s", "restart_policy",
  "restart_max_retries", "replicas", "cron_schedule", "sleep_when_idle", "sleep_after_s", "memory_limit_mb", "cpu_limit",
  "build_secrets", "overlap_seconds", "draining_seconds", "wait_for_ci",
] as const;
const DB_CREDENTIALS = new Set([
  "POSTGRES_PASSWORD", "PGPASSWORD", "MYSQL_ROOT_PASSWORD", "MYSQLPASSWORD", "REDIS_PASSWORD", "REDISPASSWORD",
  "MONGO_INITDB_ROOT_PASSWORD", "MONGOPASSWORD",
]);
const maskVar = (v: string) => (/\$\{\{/.test(v) ? v : "•••");

function diffVarsMock(src: Record<string, string>, dst: Record<string, string>, prot: Set<string>) {
  const out: SyncVarChange[] = [];
  for (const [k, v] of Object.entries(src)) {
    if (prot.has(k)) continue;
    if (!(k in dst)) out.push({ key: k, action: "added", from: null, to: maskVar(v) });
    else if (dst[k] !== v) out.push({ key: k, action: "changed", from: maskVar(dst[k]), to: maskVar(v) });
  }
  for (const [k, v] of Object.entries(dst)) if (!(k in src) && !prot.has(k)) out.push({ key: k, action: "removed", from: maskVar(v), to: null });
  return out.sort((a, b) => a.key.localeCompare(b.key));
}

function syncPlanMock(eid: string, from: string | null): SyncPlan {
  const dst = getEnv(eid);
  if (!from) throw new HttpError(400, "from is required");
  if (from === eid) throw new HttpError(400, "an environment cannot be synced from itself");
  const src = S.envs.find((e) => e.id === from);
  if (!src || src.project_id !== dst.project_id) throw new HttpError(400, "from must be another environment of the same project");
  const services: SyncServiceDiff[] = [];
  for (const svc of S.services.filter((s) => s.project_id === dst.project_id)) {
    const a = S.instances.find((i) => i.service_id === svc.id && i.environment_id === src.id);
    const b = S.instances.find((i) => i.service_id === svc.id && i.environment_id === dst.id);
    if (!a || !b) continue;
    const prot = svc.database_kind ? DB_CREDENTIALS : new Set<string>();
    const ar = a as unknown as Record<string, unknown>;
    const br = b as unknown as Record<string, unknown>;
    const settings = SYNC_FIELDS.filter((f) => JSON.stringify(ar[f] ?? null) !== JSON.stringify(br[f] ?? null)).map((f) => ({
      field: f,
      from: br[f] ?? null,
      to: ar[f] ?? null,
    }));
    const variables = diffVarsMock(a.variables, b.variables, prot);
    const have = new Set(S.volumes.filter((v) => v.service_instance_id === b.id).map((v) => v.mount_path));
    const volumes = S.volumes
      .filter((v) => v.service_instance_id === a.id && !have.has(v.mount_path))
      .map((v) => ({ mount_path: v.mount_path, size_limit_mb: v.size_limit_mb ?? null }));
    if (!settings.length && !variables.length && !volumes.length) continue;
    const pristine = (i: ServiceInstance) => !i.current_deployment_id && !i.repo_url && !i.image && !Object.keys(i.variables).length;
    services.push({
      service_id: svc.id,
      service_name: svc.name,
      action: pristine(b) && !pristine(a) ? "create" : "update",
      settings,
      variables,
      volumes,
      protected_variables: Object.keys(a.variables).filter((k) => prot.has(k)).sort(),
    });
  }
  return {
    from: { id: src.id, name: src.name },
    to: { id: dst.id, name: dst.name },
    services,
    shared_variables: diffVarsMock(S.shared[src.id] ?? {}, S.shared[dst.id] ?? {}, new Set()),
  };
}

route("GET", "/environments/{eid}/sync-preview", ([eid], r) => syncPlanMock(eid, r.query.get("from")));
route("POST", "/environments/{eid}/sync", ([eid], r) => {
  const b = (r.body ?? {}) as { from?: string; services?: string[]; include?: SyncInclude; deploy?: boolean };
  const plan = syncPlanMock(eid, b.from ?? null);
  const inc = b.include ?? { settings: true, variables: true, shared_variables: true, volumes: true };
  const only = b.services ? new Set(b.services) : null;
  const dst = getEnv(eid);
  const applied: SyncResult["services"] = [];
  for (const d of plan.services) {
    if (only && !only.has(d.service_id)) continue;
    const a = getInstance(d.service_id, b.from!);
    const t = getInstance(d.service_id, eid);
    const keys: string[] = [];
    let nSet = 0;
    let nVars = 0;
    let nVols = 0;
    if (inc.settings)
      for (const s of d.settings) {
        (t as unknown as Record<string, unknown>)[s.field] = clone((a as unknown as Record<string, unknown>)[s.field] ?? null);
        keys.push(s.field);
        nSet++;
      }
    if (inc.variables)
      for (const v of d.variables) {
        if (v.action === "removed") delete t.variables[v.key];
        else t.variables[v.key] = a.variables[v.key];
        keys.push(`variables.${v.key}`);
        nVars++;
      }
    if (inc.volumes)
      for (const v of d.volumes) {
        S.volumes.push({ id: rid("vol"), project_id: dst.project_id, environment_id: eid, service_instance_id: t.id, name: `${d.service_name}-${v.mount_path.split("/").pop() || "data"}`, mount_path: v.mount_path, size_bytes: 0, size_limit_mb: v.size_limit_mb, created_at: iso(now()) });
        nVols++;
      }
    if (!nSet && !nVars && !nVols) continue;
    if (t.current_deployment_id) t.pending_changes = [...new Set([...t.pending_changes, ...keys])];
    let deployment_id: string | null = null;
    let deploy_skipped: string | undefined;
    if (b.deploy) {
      const svc = getService(d.service_id);
      if (svc.source_type !== "database" && !t.repo_url && !t.image) deploy_skipped = "no source configured";
      else deployment_id = startDeployment(t, "manual").id;
    }
    applied.push({ service_id: d.service_id, service_name: d.service_name, settings: nSet, variables: nVars, volumes: nVols, deployment_id, deploy_skipped });
  }
  let shared = 0;
  if (inc.shared_variables && plan.shared_variables.length) {
    S.shared[eid] = { ...(S.shared[b.from!] ?? {}) };
    shared = plan.shared_variables.length;
  }
  if (applied.length || shared)
    emit({ project_id: dst.project_id, environment_id: eid, service_id: null, kind: "environment.synced", message: `환경 ${dst.name}를 ${plan.from.name} 환경에서 동기화했습니다 (서비스 ${applied.length}개)` });
  return { from: plan.from, to: plan.to, services: applied, shared_variables: shared } satisfies SyncResult;
});

/* ── v1.5: database data browser ────────────────────────────────────── */

const MOCK_USERS = Array.from({ length: 137 }, (_, i) => ({
  id: String(i + 1),
  email: `user${i + 1}@example.com`,
  name: pick(["Ann", "Bo", "Chen", "Dana", "Eun", "Farah"]) + " " + pick(["Kim", "Lee", "Park", "Smith"]),
  created_at: ago(60 * 24 * (137 - i)),
  deleted_at: i % 9 === 0 ? ago(60 * i) : null,
}));
const MOCK_ORDERS = Array.from({ length: 64 }, (_, i) => ({
  id: String(1000 + i),
  user_id: String((i * 7) % 137 + 1),
  total: (Math.round(((i * 37) % 500) * 100 + 99) / 100).toFixed(2),
  status: pick(["paid", "pending", "refunded"]),
}));
const MOCK_REDIS: Record<string, { type: string; ttl: number; value: unknown; length: number }> = {
  "session:9f2c": { type: "string", ttl: 3412, value: '{"user":42,"csrf":"x1y2"}', length: 25 },
  "user:42": { type: "hash", ttl: -1, value: [{ field: "name", value: "Ann Kim" }, { field: "plan", value: "pro" }], length: 2 },
  "queue:emails": { type: "list", ttl: -1, value: ["welcome:42", "reset:17"], length: 2 },
  "leaderboard": { type: "zset", ttl: -1, value: [{ member: "ann", score: "120" }, { member: "bo", score: "88" }], length: 2 },
  "tags:post:1": { type: "set", ttl: -1, value: ["go", "infra"], length: 2 },
};

/* ── v1.5 buckets (S3 object browser; contents only live in memory, download is not mocked) ── */

function mockBucketVars(pw: string): Record<string, string> {
  const h = hash(pw).toString(16).padStart(10, "0");
  return {
    BUCKET: `bucket-${h.slice(0, 10)}`,
    ACCESS_KEY_ID: `AVEN${(h + h).toUpperCase().slice(0, 16)}`,
    SECRET_ACCESS_KEY: pw,
    REGION: "us-east-1",
    ENDPOINT: "http://${{ AVEN_PRIVATE_DOMAIN }}:9000",
    PUBLIC_ENDPOINT: "https://${{ AVEN_PUBLIC_DOMAIN }}",
    MINIO_ROOT_USER: "${{ ACCESS_KEY_ID }}",
    MINIO_ROOT_PASSWORD: "${{ SECRET_ACCESS_KEY }}",
    AWS_ACCESS_KEY_ID: "${{ ACCESS_KEY_ID }}",
    AWS_SECRET_ACCESS_KEY: "${{ SECRET_ACCESS_KEY }}",
    AWS_REGION: "${{ REGION }}",
    AWS_DEFAULT_REGION: "${{ REGION }}",
    AWS_ENDPOINT_URL: "${{ ENDPOINT }}",
    AWS_S3_BUCKET_NAME: "${{ BUCKET }}",
  };
}

const BUCKETS = new Map<string, Map<string, { size: number; last_modified: string }>>();

function bucketTarget(sid: string, eid: string) {
  const svc = getService(sid);
  const inst = getInstance(sid, eid);
  if (svc.database_kind !== "bucket") throw new HttpError(400, "bucket 서비스가 아닙니다");
  let objs = BUCKETS.get(inst.id);
  if (!objs) {
    objs = new Map([
      ["avatars/u_1.png", { size: 48213, last_modified: ago(60 * 5) }],
      ["avatars/u_2.png", { size: 51002, last_modified: ago(60 * 3) }],
      ["exports/2026/orders.csv", { size: 1_284_112, last_modified: ago(60 * 24) }],
      ["README.txt", { size: 312, last_modified: ago(60 * 24 * 3) }],
    ]);
    BUCKETS.set(inst.id, objs);
  }
  return { svc, inst, objs };
}

function bucketRunning(inst: { status: string }) {
  if (inst.status !== "online") throw new HttpError(409, "bucket이 실행 중이 아닙니다. 먼저 배포하세요");
}

route("GET", "/services/{sid}/instances/{eid}/bucket", ([sid, eid]) => {
  const { svc, inst } = bucketTarget(sid, eid);
  const env = S.envs.find((e) => e.id === eid);
  const v = inst.variables;
  const host = inst.domains[0]?.host;
  return {
    bucket: v.BUCKET ?? "",
    region: v.REGION ?? "us-east-1",
    endpoint: `http://${svc.name}.${env?.name ?? "production"}.aven.internal:9000`,
    public_endpoint: host ? `https://${host}` : null,
    access_key_id: v.ACCESS_KEY_ID ?? "",
    secret_access_key: v.SECRET_ACCESS_KEY ?? "",
    masked: false,
    running: inst.status === "online",
    max_upload_bytes: 100 << 20,
  };
});

route("GET", "/services/{sid}/instances/{eid}/bucket/objects", ([sid, eid], r) => {
  const { inst, objs } = bucketTarget(sid, eid);
  bucketRunning(inst);
  const prefix = r.query.get("prefix") ?? "";
  if (prefix.startsWith("/")) throw new HttpError(400, "prefix: key는 '/'로 시작할 수 없습니다");
  const limit = Number(r.query.get("limit") ?? 200);
  const start = Number(r.query.get("cursor") ?? 0);
  const entries: ({ prefix: string } | { key: string })[] = [];
  const seen = new Set<string>();
  for (const k of [...objs.keys()].sort()) {
    if (!k.startsWith(prefix)) continue;
    const i = k.slice(prefix.length).indexOf("/");
    if (i >= 0) {
      const p = k.slice(0, prefix.length + i + 1);
      if (!seen.has(p)) entries.push({ prefix: p });
      seen.add(p);
    } else entries.push({ key: k });
  }
  const page = entries.slice(start, start + limit);
  const bucket = inst.variables.BUCKET ?? "";
  return {
    bucket,
    prefix,
    prefixes: page.flatMap((e) => ("prefix" in e ? [e.prefix] : [])),
    objects: page.flatMap((e) => ("key" in e ? [{ key: e.key, ...objs.get(e.key)!, etag: hash(e.key).toString(16) }] : [])),
    next_cursor: start + limit < entries.length ? String(start + limit) : null,
  };
});

route("PUT", "/services/{sid}/instances/{eid}/bucket/objects", ([sid, eid], r) => {
  const { svc, inst, objs } = bucketTarget(sid, eid);
  bucketRunning(inst);
  if (!(r.body instanceof FormData)) throw new HttpError(400, "multipart/form-data 본문이 필요합니다(prefix 필드 다음에 file)");
  let prefix = String(r.body.get("prefix") ?? "");
  if (prefix && !prefix.endsWith("/")) prefix += "/";
  const files = r.body.getAll("file").filter((f): f is File => f instanceof File);
  if (!files.length) throw new HttpError(400, "file 필드가 없습니다");
  if (files.reduce((n, f) => n + f.size, 0) > 100 << 20) throw new HttpError(413, "업로드는 한 번에 100MiB까지입니다");
  const uploaded = files.map((f) => {
    objs.set(prefix + f.name, { size: f.size, last_modified: iso(now()) });
    return { key: prefix + f.name, size: f.size };
  });
  emit({ project_id: svc.project_id, environment_id: eid, service_id: svc.id, kind: "bucket.upload", message: `bucket에 파일 ${uploaded.length}개를 올렸습니다` });
  return [201, { uploaded }];
});

// object key는 '/'를 포함하므로 {key...} 경로를 직접 맞춘다.
routes.push([
  "DELETE",
  /^\/services\/([^/]+)\/instances\/([^/]+)\/bucket\/objects\/(.+)$/,
  ([sid, eid, key]) => {
    const { svc, inst, objs } = bucketTarget(sid, eid);
    bucketRunning(inst);
    if (!key || key.startsWith("/") || key.split("/").some((s) => s === "." || s === "..")) throw new HttpError(400, "잘못된 key입니다");
    objs.delete(key);
    emit({ project_id: svc.project_id, environment_id: eid, service_id: svc.id, kind: "bucket.delete", message: `bucket 객체 ${key}를 지웠습니다` });
    return 204;
  },
]);

function dbService(sid: string, eid: string) {
  const svc = getService(sid);
  const inst = getInstance(sid, eid);
  if (!svc.database_kind) throw new HttpError(400, "데이터 브라우저는 데이터베이스 서비스(postgres, mysql, redis, mongo)에서만 쓸 수 있습니다");
  if (inst.status !== "online") throw new HttpError(409, "데이터베이스가 실행 중이 아닙니다. 먼저 배포하세요");
  return svc.database_kind;
}

function mockTable(kind: DatabaseKind, name: string): Record<string, unknown>[] {
  const rows = name === "orders" ? MOCK_ORDERS : name === "users" ? MOCK_USERS : null;
  if (!rows) throw new HttpError(400, kind === "postgres" ? `relation "${name}" does not exist` : `Table '${name}' doesn't exist`);
  return kind === "mongo" ? rows.map((r) => ({ _id: { $oid: hash(r.id).toString(16).padStart(24, "0").slice(0, 24) }, ...r })) : rows;
}

route("GET", "/services/{sid}/instances/{eid}/data/tables", ([sid, eid]) => {
  const kind = dbService(sid, eid);
  if (kind === "redis") return { kind, truncated: false, tables: [{ schema: "", name: "db0", kind: "keyspace", rows: Object.keys(MOCK_REDIS).length }] };
  const schema = kind === "postgres" ? "public" : kind === "mysql" ? "aven" : "app";
  const k = kind === "mongo" ? "collection" : "table";
  return {
    kind,
    truncated: false,
    tables: [
      { schema, name: "orders", kind: k, rows: MOCK_ORDERS.length },
      { schema, name: "users", kind: k, rows: MOCK_USERS.length },
      ...(kind === "postgres" ? [{ schema, name: "active_users", kind: "view", rows: null }] : []),
    ],
  };
});
route("GET", "/services/{sid}/instances/{eid}/data/tables/{name}", ([sid, eid, name], r) => {
  const kind = dbService(sid, eid);
  if (kind === "redis") throw new HttpError(400, "redis는 data/keys로 탐색하세요");
  const limit = Number(r.query.get("limit") ?? 50);
  const offset = Number(r.query.get("offset") ?? 0);
  if (!(limit >= 1 && limit <= 500) || !(offset >= 0)) throw new HttpError(400, "limit must be 1..500 and offset 0..100000000");
  const rows = mockTable(kind, name === "active_users" ? "users" : name);
  const columns = Object.keys(rows[0]);
  const page = rows.slice(offset, offset + limit).map((x) => columns.map((c) => (x as Record<string, DataCell>)[c] ?? null));
  const sql = kind !== "mongo";
  const view = name === "active_users";
  return {
    schema: r.query.get("schema") ?? "", name, columns, rows: page, limit, offset, has_more: offset + limit < rows.length, truncated: false,
    column_types: sql ? columns.map((c) => MOCK_COLUMN_TYPES[c] ?? "text") : null,
    primary_key: sql && !view ? ["id"] : [],
    editable: sql && !view,
  } satisfies DataRows;
});
const MOCK_COLUMN_TYPES: Record<string, string> = {
  id: "bigint", user_id: "bigint", total: "numeric(10,2)", status: "text", email: "character varying(255)", name: "text",
  created_at: "timestamp with time zone", deleted_at: "timestamp with time zone",
};
// v1.5: row editing (SQL tables with a primary key). Values are strings or null like browse cells.
function mockEditTarget(sid: string, eid: string, name: string) {
  const kind = dbService(sid, eid);
  if (kind !== "postgres" && kind !== "mysql") throw new HttpError(400, "행 편집은 postgres, mysql에서만 쓸 수 있습니다");
  if (name === "active_users") throw new HttpError(400, "기본 키가 없는 표의 행은 바꾸거나 지울 수 없습니다. 쿼리 탭을 쓰세요");
  const rows = mockTable(kind, name) as Record<string, unknown>[];
  const svc = getService(sid);
  const audit = (msg: string) =>
    emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "database.edit", message: `${svc.name}: admin(세션)이 표 ${name}의 ${msg}` });
  const check = (set: Record<string, unknown>) => {
    for (const k of Object.keys(set)) if (!(k in rows[0])) throw new HttpError(400, `알 수 없는 열입니다: ${k}`);
  };
  const find = (pk: Record<string, unknown> | undefined) => {
    if (!pk || Object.keys(pk).join() !== "id") throw new HttpError(400, "pk에는 기본 키 열(id)을 모두, 그 열만 넣어야 합니다");
    return rows.findIndex((x) => x.id === String(pk.id));
  };
  return { rows, audit, check, find };
}
type MockEditBody = { pk?: Record<string, unknown>; set?: Record<string, unknown> };
route("PATCH", "/services/{sid}/instances/{eid}/data/tables/{name}/rows", ([sid, eid, name], r) => {
  const { rows, audit, check, find } = mockEditTarget(sid, eid, name);
  const b = (r.body ?? {}) as MockEditBody;
  if (!b.set || Object.keys(b.set).length === 0) throw new HttpError(400, "set is required");
  check(b.set);
  const i = find(b.pk);
  audit(`행(id=${String(b.pk?.id)})의 열 ${Object.keys(b.set).sort().join(", ")}를 바꿨습니다`);
  if (i < 0) throw new HttpError(404, "행을 찾을 수 없습니다");
  for (const [k, v] of Object.entries(b.set)) rows[i][k] = v === null ? null : String(v);
  return { affected: 1 };
});
route("POST", "/services/{sid}/instances/{eid}/data/tables/{name}/rows", ([sid, eid, name], r) => {
  const { rows, audit, check } = mockEditTarget(sid, eid, name);
  const set = ((r.body ?? {}) as MockEditBody).set ?? {};
  check(set);
  const row: Record<string, unknown> = Object.fromEntries(Object.keys(rows[0]).map((k) => [k, null]));
  row.id = String(Math.max(...rows.map((x) => Number(x.id))) + 1);
  for (const [k, v] of Object.entries(set)) row[k] = v === null ? null : String(v);
  rows.push(row);
  audit(`행을 넣었습니다(열: ${Object.keys(set).sort().join(", ")})`);
  return [201, { affected: 1 }];
});
route("DELETE", "/services/{sid}/instances/{eid}/data/tables/{name}/rows", ([sid, eid, name], r) => {
  const { rows, audit, find } = mockEditTarget(sid, eid, name);
  const pk = ((r.body ?? {}) as MockEditBody).pk;
  const i = find(pk);
  audit(`행(id=${String(pk?.id)})을 지웠습니다`);
  if (i < 0) throw new HttpError(404, "행을 찾을 수 없습니다");
  rows.splice(i, 1);
  return { affected: 1 };
});
route("GET", "/services/{sid}/instances/{eid}/data/keys", ([sid, eid], r) => {
  if (dbService(sid, eid) !== "redis") throw new HttpError(400, "data/keys는 redis에서만 쓸 수 있습니다");
  const pattern = r.query.get("pattern") || "*";
  const re = new RegExp("^" + pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$");
  const keys = Object.entries(MOCK_REDIS)
    .filter(([k]) => re.test(k))
    .map(([key, v]) => ({ key, type: v.type, ttl: v.ttl }));
  return { cursor: "0", keys, db: Number(r.query.get("db") ?? 0) };
});
route("GET", "/services/{sid}/instances/{eid}/data/keys/{key}", ([sid, eid, key]) => {
  if (dbService(sid, eid) !== "redis") throw new HttpError(400, "data/keys는 redis에서만 쓸 수 있습니다");
  const v = MOCK_REDIS[key];
  if (!v) throw new HttpError(404, "key not found");
  return { key, db: 0, truncated: false, ...v };
});
route("POST", "/services/{sid}/instances/{eid}/data/query", ([sid, eid], r) => {
  const kind = dbService(sid, eid);
  const q = String((r.body as { query?: string })?.query ?? "").trim();
  if (!q) throw new HttpError(400, "query is empty");
  const svc = getService(sid);
  emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "database.query", message: `${svc.name}: admin(세션)이 쿼리를 실행했습니다: ${q.replace(/\s+/g, " ").slice(0, 200)}` });
  const started = now();
  let res: Omit<DataQueryResult, "duration_ms">;
  if (kind === "redis") {
    const [cmd, arg] = q.split(/\s+/);
    const v = arg ? MOCK_REDIS[arg] : undefined;
    const text = /^PING$/i.test(cmd) ? "PONG" : /^(GET|HGETALL|LRANGE|SMEMBERS)$/i.test(cmd) ? (v ? JSON.stringify(v.value) : "(nil)") : `(error) ERR unknown command '${cmd}'`;
    res = { kind: "text", columns: [], rows: [], text, truncated: false, row_count: 0 };
    // v1.5: RESP3 (redis-cli -3) — a single map reply comes back as a key/value table.
    if ((r.body as { resp3?: boolean })?.resp3 && /^HGETALL$/i.test(cmd) && v?.type === "hash") {
      const rows = (v.value as { field: string; value: string }[]).map((x) => [x.field, x.value]);
      res = { kind: "table", columns: ["key", "value"], rows, text: "", truncated: false, row_count: rows.length };
    }
  } else {
    const m = /from\s+[`"]?(\w+)[`"]?/i.exec(q) ?? /db\.(\w+)\./.exec(q);
    if (/^(select|db\.)/i.test(q) && m) {
      const rows = mockTable(kind, m[1]);
      const columns = Object.keys(rows[0]);
      const lim = Number(/limit\s*\(?\s*(\d+)/i.exec(q)?.[1] ?? 1000);
      const out = rows.slice(0, Math.min(lim, 1000)).map((x) => columns.map((c) => (x as Record<string, DataCell>)[c] ?? null));
      res = { kind: "table", columns, rows: out, text: "", truncated: false, row_count: out.length };
    } else if (/^select\s+/i.test(q)) {
      res = { kind: "table", columns: ["?column?"], rows: [["1"]], text: "", truncated: false, row_count: 1 };
    } else {
      res = { kind: "text", columns: [], rows: [], text: "(출력 없음)", truncated: false, row_count: 0 };
    }
  }
  return { ...res, duration_ms: now() - started + 3 } satisfies DataQueryResult;
});
route("GET", "/environments/{eid}/volumes", ([eid]) => (getEnv(eid), S.volumes.filter((v) => v.environment_id === eid)));
route("GET", "/volumes/{vid}/backup-schedule", ([vid]) => (getVolume(vid), schedules[vid] ?? { cron: null, keep: 7 }));

route("POST", "/services/{sid}/instances/{eid}/domains", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  const b = (r.body ?? {}) as { host?: string; target_port?: number };
  const svc = getService(sid);
  let d: Domain;
  if (!b.host) {
    const host = generatedHost(svc.name, getEnv(eid), getProject(svc.project_id));
    if (inst.domains.some((x) => x.host === host)) throw new HttpError(409, `${host} is already attached`);
    d = genDomain(host, b.target_port ?? null);
  } else {
    const host = b.host.trim().toLowerCase();
    // v1.5: wildcard hosts (*.example.com) match one label; specific hosts win over the wildcard.
    if (!/^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(host) || (host.startsWith("*.") && !host.slice(2).includes(".")))
      throw new HttpError(400, `invalid domain: ${host}`);
    if (S.instances.some((i) => i.domains.some((x) => x.host === host))) throw new HttpError(409, `${host} is already in use`);
    const tunnel = NODE_INGRESS[inst.node_id]?.mode === "cloudflare_tunnel";
    d = {
      host,
      kind: "custom",
      target_port: b.target_port ?? null,
      status: "pending_dns",
      proxy: tunnel ? "tunnel" : null,
      // Tunnel nodes: the tunnel terminates TLS, so the domain is created HTTP-only and trusts X-Forwarded-Proto.
      ...(tunnel ? { tls: "off" as const, trust_forwarded_proto: true } : {}),
      verification: { method: "", verified_at: null, txt: verifyTxt(host) },
      cert: { status: "pending" },
      dns: customDnsRecords(inst, host),
    };
  }
  inst.domains.push(d);
  return [201, domainView(inst, d)];
});

/* ── P1/P3/P4/P5/P6/P8: networking view (mock) ───────────────────────── */

/** Record name relative to the zone (best effort: the zone is the last two labels). */
const recordName = (host: string, prefix = "") => {
  const labels = host.split(".");
  const rel = labels.length > 2 ? labels.slice(0, -2).join(".") : "@";
  return prefix ? (rel === "@" ? prefix : `${prefix}.${rel}`) : rel;
};

/** Recommended records: CNAME to the node ingress hostname, then the TXT ownership record; A/AAAA as the alternative. */
function customDnsRecords(inst: ServiceInstance, host: string): DnsRecord[] {
  const n = NODES.find((x) => x.id === inst.node_id) ?? NODES[0];
  const ing = NODE_INGRESS[n.id] ?? NODE_INGRESS.node_local;
  const out: DnsRecord[] = [{ type: "CNAME", name: recordName(host), value: ing.hostname, status: "pending", current: null }];
  out.push({ type: "TXT", name: recordName(host, "_aven-verify"), value: verifyTxt(host).value, status: "pending", current: null });
  if (ing.mode === "direct") {
    if (n.address.ipv6) out.push({ type: "AAAA", name: recordName(host), value: n.address.ipv6, status: "pending", current: null });
    if (n.address.ipv4) out.push({ type: "A", name: recordName(host), value: n.address.ipv4, status: "pending", current: null });
  }
  return out;
}

function effectivePort(inst: ServiceInstance): number {
  return inst.port ?? (Number(inst.variables.PORT) || 8080);
}

/* §7 per-domain public IPv6 (mock): nodes with an ipv6_pool give every domain the lowest free address (skipping the network address and ::1). */
const domainIPv6: Record<string, string> = {};
function poolAddr(pool: string, n: number): string {
  const base = pool.split("/")[0].replace(/::$/, "");
  return `${base}::${n.toString(16)}`;
}
function mockDomainIPv6(inst: ServiceInstance, host: string): string | undefined {
  const pool = NODES.find((x) => x.id === inst.node_id)?.ipv6_pool;
  if (!pool) return undefined;
  const cur = domainIPv6[host];
  if (cur && cur.startsWith(pool.split("/")[0].replace(/::$/, ""))) return cur;
  const used = new Set(Object.values(domainIPv6));
  let n = 2;
  while (used.has(poolAddr(pool, n))) n++;
  return (domainIPv6[host] = poolAddr(pool, n));
}

function domainView(inst: ServiceInstance, d: Domain): Domain {
  const ipv6 = mockDomainIPv6(inst, d.host);
  const nodeV6 = NODES.find((x) => x.id === inst.node_id)?.address.ipv6;
  const v = clone(d);
  if (ipv6) {
    // The recommended AAAA is the domain's own address; DNS still pointing at the shared node address counts as working.
    v.dns = v.dns.map((r) =>
      r.type === "AAAA" && r.value === nodeV6
        ? { ...r, value: ipv6, ...(r.status === "ok" ? { note: "지금은 노드 공용 IPv6를 가리킵니다(정상 동작). 전용 IPv6로 바꾸면 도메인별 주소를 씁니다." } : {}) }
        : r,
    );
  }
  return {
    ...v,
    effective_port: d.target_port ?? effectivePort(inst),
    verification: d.verification ?? { method: "legacy", verified_at: null, txt: verifyTxt(d.host) },
    proxy: d.proxy ?? null,
    ...(ipv6 ? { ipv6 } : {}),
  };
}

/** Ports the mock "sees" in the container: the service port listening, PORT, image EXPOSE and a loopback-only debugger. */
function detectedPorts(inst: ServiceInstance): DetectedPort[] {
  const svc = getService(inst.service_id);
  const out: DetectedPort[] = [];
  const add = (port: number, source: DetectedPort["source"]) => {
    if (!out.some((p) => p.port === port)) out.push({ port, source });
  };
  if (inst.status === "online" || inst.status === "sleeping") {
    if (inst.port) add(inst.port, "listening");
    if (svc.name === "hello") add(9229, "loopback");
    if (svc.name === "hello") add(9464, "listening"); // Prometheus exporter
  }
  if (inst.variables.PORT && Number(inst.variables.PORT)) add(Number(inst.variables.PORT), "env");
  if (svc.name === "whoami") add(80, "expose");
  if (svc.database_kind === "postgres") add(5432, "expose");
  if (svc.database_kind === "redis") add(6379, "expose");
  return out;
}

const PRIVATE_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

function privateNameError(inst: ServiceInstance, name: string): string | null {
  if (!PRIVATE_NAME_RE.test(name)) return "소문자, 숫자, 하이픈(-)만 쓸 수 있고 하이픈으로 시작하거나 끝날 수 없습니다";
  const svc = getService(inst.service_id);
  for (const o of S.instances.filter((i) => i.environment_id === inst.environment_id && i.id !== inst.id)) {
    const os = getService(o.service_id);
    if (os.name === name) return `같은 환경의 서비스 ${os.name}이(가) 이미 쓰는 이름입니다`;
    if (o.private_dns_name === name) return `같은 환경의 서비스 ${os.name}이(가) 이미 쓰는 이름입니다`;
  }
  if (name === "aven" || name === "localhost") return "예약된 이름입니다";
  void svc;
  return null;
}

function tcpView(inst: ServiceInstance): InstanceNetworking["tcp_proxy"] {
  const tunnel = NODE_INGRESS[inst.node_id]?.mode === "cloudflare_tunnel";
  const n = NODES.find((x) => x.id === inst.node_id);
  const host = tunnel ? "" : n?.is_local ? `tcp.${BASE}` : (n?.address.ipv4 ?? n?.address.ipv6 ?? "");
  return {
    enabled: !!inst.tcp_proxy,
    host,
    public_port: inst.tcp_proxy?.public_port ?? null,
    container_port: inst.tcp_proxy?.container_port ?? null,
    available: !!host,
    ...(tunnel ? { reason: "이 노드는 공개 IP가 없어 TCP 프록시를 열 수 없습니다" } : {}),
  };
}

route("GET", "/services/{sid}/instances/{eid}/networking", ([sid, eid]) => {
  const inst = getInstance(sid, eid);
  const svc = getService(sid);
  const env = getEnv(eid);
  const n = NODES.find((x) => x.id === inst.node_id);
  const dns = inst.private_dns_name || svc.name;
  const aliases = [...new Set([svc.name, `${svc.name}.${env.name}.aven.internal`, `${svc.name}.${env.name}.keel.internal`, dns, `${dns}.${env.name}.aven.internal`, `${dns}.${env.name}.keel.internal`])];
  const idx = S.instances.filter((i) => i.environment_id === eid).findIndex((i) => i.id === inst.id) + 2;
  return {
    detected_ports: detectedPorts(inst),
    port_detection:
      inst.status === "online" || inst.status === "sleeping"
        ? { source: "proc", node: n?.name, containers: Math.max(1, inst.replicas) }
        : { source: "unavailable", reason: inst.current_deployment_id ? "실행 중인 컨테이너가 없습니다" : "배포된 컨테이너가 없습니다", node: n?.name, containers: 0 },
    effective_port: effectivePort(inst),
    domains: inst.domains.map((d) => domainView(inst, d)),
    tcp_proxy: tcpView(inst),
    private: {
      dns_name: dns,
      fqdn: `${dns}.${env.name}.aven.internal`,
      aliases,
      ipv4: Array.from({ length: Math.max(1, inst.replicas) }, (_, k) => `172.20.${(eid.length * 7) % 200}.${idx + k * 16}`),
      ipv6: Array.from({ length: Math.max(1, inst.replicas) }, (_, k) => `fd4b:6565:6c00:${(eid.length * 13).toString(16)}::${(idx + k * 16).toString(16)}`),
      mesh_ports: inst.private_ports?.length ? inst.private_ports : [effectivePort(inst)],
    },
    outbound: {
      ipv6_enabled: inst.outbound_ipv6 ?? true,
      node_ipv6_egress: n ? !!n.address.ipv6 : null,
      static_ips: { ipv4: n?.address.ipv4 ?? null, ipv6: n?.address.ipv6 ?? null },
    },
  } satisfies InstanceNetworking;
});

route("PATCH", "/services/{sid}/instances/{eid}/domains/{host}", ([sid, eid, rawHost], r) => {
  const inst = getInstance(sid, eid);
  const host = decodeURIComponent(rawHost);
  const d = inst.domains.find((x) => x.host === host);
  if (!d) throw new HttpError(404, `domain ${host} not found`);
  const b = (r.body ?? {}) as Record<string, unknown>;
  const allowed = ["host", "target_port", "paths", "tls", "trust_forwarded_proto", "redirect", "basic_auth"];
  for (const k of Object.keys(b)) if (!allowed.includes(k)) throw new HttpError(400, `unknown field ${k}`);
  if (b.target_port !== undefined && b.target_port !== null && !(Number.isInteger(b.target_port) && Number(b.target_port) >= 1 && Number(b.target_port) <= 65535))
    throw new HttpError(400, "target_port must be 1..65535");
  if (typeof b.host === "string" && b.host !== host) {
    if (d.kind !== "generated") throw new HttpError(400, "only generated domains can be renamed");
    const suffix = host.slice(host.indexOf("."));
    const next = b.host.trim().toLowerCase();
    if (!next.endsWith(suffix) || !PRIVATE_NAME_RE.test(next.slice(0, -suffix.length))) throw new HttpError(400, `host must be <name>${suffix}`);
    if (S.instances.some((i) => i.domains.some((x) => x.host === next))) throw new HttpError(409, `${next} is already in use`);
    d.host = next;
    d.verification = { method: "legacy", verified_at: iso(now()), txt: verifyTxt(next) };
  }
  if (b.target_port !== undefined) d.target_port = (b.target_port as number | null) ?? null;
  if (b.paths !== undefined) d.paths = (b.paths as Domain["paths"]) ?? [];
  if (b.tls !== undefined) d.tls = b.tls as Domain["tls"];
  if (b.trust_forwarded_proto !== undefined) d.trust_forwarded_proto = !!b.trust_forwarded_proto;
  if (b.redirect !== undefined) d.redirect = (b.redirect as Domain["redirect"]) ?? null;
  if (b.basic_auth !== undefined) {
    const a = b.basic_auth as { username: string; password?: string } | null;
    d.basic_auth = a ? { username: a.username, has_password: !!a.password || !!d.basic_auth?.has_password } : null;
  }
  return domainView(inst, d);
});

route("POST", "/services/{sid}/instances/{eid}/domains/{host}/verify", ([sid, eid, rawHost]) => {
  const inst = getInstance(sid, eid);
  const host = decodeURIComponent(rawHost);
  const d = inst.domains.find((x) => x.host === host);
  if (!d) throw new HttpError(404, `domain ${host} not found`);
  // Mock propagation: a re-check after the records were "created" finds them.
  if (d.status === "pending_dns" || d.status === "error") {
    d.dns = d.dns.map((x) => (x.type === "A" || x.type === "AAAA" ? x : { ...x, status: "ok", current: x.value }));
    d.status = d.proxy ? "proxied" : "active";
    d.verification = { ...(d.verification ?? { txt: verifyTxt(host) }), method: "txt", verified_at: iso(now()) };
    d.cert = { status: "valid", not_after: iso(now() + 90 * 864e5) };
  }
  return domainView(inst, d);
});

route("GET", "/services/{sid}/instances/{eid}/private-dns/available", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  const name = (r.query.get("name") ?? "").trim().toLowerCase();
  const err = privateNameError(inst, name);
  return err ? { available: false, reason: err } : { available: true };
});

route("PATCH", "/services/{sid}/instances/{eid}/tcp-proxy", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  if (!inst.tcp_proxy) throw new HttpError(404, "no tcp proxy");
  const p = Number((r.body as { container_port?: number })?.container_port);
  if (!Number.isInteger(p) || p < 1 || p > 65535) throw new HttpError(400, "container_port must be 1..65535");
  inst.tcp_proxy = { ...inst.tcp_proxy, container_port: p };
  return inst;
});
route("DELETE", "/services/{sid}/instances/{eid}/domains/{host}", ([sid, eid, host]) => {
  const inst = getInstance(sid, eid);
  inst.domains = inst.domains.filter((d) => d.host !== decodeURIComponent(host));
  return 204;
});
route("POST", "/services/{sid}/instances/{eid}/tcp-proxy", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  if (NODE_INGRESS[inst.node_id]?.mode === "cloudflare_tunnel") throw new HttpError(409, "이 노드는 공개 IP가 없어 TCP 프록시를 열 수 없습니다");
  if (inst.tcp_proxy) throw new HttpError(409, "tcp proxy already enabled");
  inst.tcp_proxy = { container_port: (r.body as { container_port: number }).container_port, public_port: 30000 + Math.floor(Math.random() * 9999) };
  return inst;
});
route("DELETE", "/services/{sid}/instances/{eid}/tcp-proxy", ([sid, eid]) => {
  getInstance(sid, eid).tcp_proxy = null;
  return 204;
});

route("POST", "/services/{sid}/instances/{eid}/deploy", ([sid, eid]) => [202, startDeployment(mockCheckSuspended(getInstance(sid, eid)), "manual")]);
route("POST", "/deployments/{did}/redeploy", ([did]) => {
  const d = getDeployment(did);
  return [202, startDeployment(S.instances.find((i) => i.id === d.service_instance_id)!, "redeploy", d)];
});
route("POST", "/deployments/{did}/rollback", ([did]) => {
  const d = getDeployment(did);
  if (!d.image) throw new HttpError(409, "이 배포에는 이미지가 없어 롤백할 수 없습니다");
  return [202, startDeployment(S.instances.find((i) => i.id === d.service_instance_id)!, "rollback", d)];
});
route("POST", "/deployments/{did}/cancel", ([did]) => {
  const d = getDeployment(did);
  if (["live", "failed", "crashed", "cancelled", "removed", "skipped"].includes(d.status)) throw new HttpError(409, "이미 끝난 배포입니다");
  d.finished_at = iso(now());
  pushLog(d, "==> cancelled");
  setStatus(d, "cancelled");
  const inst = S.instances.find((i) => i.id === d.service_instance_id)!;
  inst.status = inst.current_deployment_id ? (inst.cron_schedule ? "scheduled" : "online") : "idle";
  return 204;
});
// v1.5: 재배포 없는 레플리카 수 변경(서버는 새 레플리카의 healthcheck를 기다린 뒤 응답한다).
route("POST", "/services/{sid}/instances/{eid}/scale", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  const n = Number((r.body as { replicas?: number } | null)?.replicas);
  if (!Number.isInteger(n) || n < 1 || n > 10) throw new HttpError(400, "replicas must be 1..10");
  if (inst.autoscale?.enabled) throw new HttpError(409, "자동 확장이 켜져 있어 replica 수를 직접 바꿀 수 없습니다(autoscale의 min/max를 바꾸세요)");
  const d = S.deployments.find((x) => x.id === inst.current_deployment_id);
  if (!d || d.status !== "live" || inst.status !== "online" || inst.cron_schedule) throw new HttpError(409, "cannot scale now: 서비스가 실행 중이 아닙니다");
  const placement = inst.placement ?? [{ node_id: inst.node_id, replicas: inst.replicas }];
  if (placement.length > 1 && n < placement.length) throw new HttpError(400, `placement의 노드마다 replica가 하나 이상 있어야 합니다(최소 ${placement.length})`);
  const from = d.replicas.length;
  // 노드별 비율대로 나눈다(노드마다 최소 1).
  const sumW = placement.reduce((s, p) => s + p.replicas, 0);
  const nodes = placement.map((p) => ({ node_id: p.node_id, replicas: 1 }));
  for (let left = n - nodes.length; left > 0; left--) {
    let best = 0;
    let gap = -Infinity;
    placement.forEach((p, i) => {
      const g = (n * p.replicas) / sumW - nodes[i].replicas;
      if (g > gap + 1e-9) [best, gap] = [i, g];
    });
    nodes[best].replicas++;
  }
  const svc = getService(sid);
  d.replicas = nodes.flatMap((p) => Array.from({ length: p.replicas }, () => p.node_id)).map((node_id, k) => ({
    name: `${svc.name}-${k + 1}`,
    status: "running" as const,
    restarts: d.replicas[k]?.restarts ?? 0,
    node_id,
  }));
  inst.replicas = n;
  if (inst.placement) inst.placement = nodes;
  inst.current_replicas = n;
  emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "service.scaled", message: `${svc.name}: replica ${from} → ${n} (수동)` });
  return { from, to: n, nodes, instance: inst };
});
route("POST", "/services/{sid}/instances/{eid}/restart", ([sid, eid]) => {
  const inst = getInstance(sid, eid);
  const d = S.deployments.find((x) => x.id === inst.current_deployment_id);
  if (!d) throw new HttpError(409, "실행 중인 배포가 없습니다");
  d.replicas = d.replicas.map((r) => ({ ...r, status: "restarting" }));
  setTimeout(() => (d.replicas = d.replicas.map((r) => ({ ...r, status: "running", restarts: r.restarts + 1 }))), 2500);
  emit({ project_id: getService(sid).project_id, environment_id: eid, service_id: sid, kind: "deploy.restarted", message: "재시작" });
  return 204;
});
route("POST", "/services/{sid}/instances/{eid}/stop", ([sid, eid]) => {
  const inst = getInstance(sid, eid);
  inst.status = "offline";
  const d = S.deployments.find((x) => x.id === inst.current_deployment_id);
  if (d) d.replicas = d.replicas.map((r) => ({ ...r, status: "exited" }));
  emit({ project_id: getService(sid).project_id, environment_id: eid, service_id: sid, kind: "service.stopped", message: "서비스 중지" });
  return 204;
});
route("GET", "/services/{sid}/instances/{eid}/deployments", ([sid, eid], r) => {
  const inst = getInstance(sid, eid);
  return S.deployments
    .filter((d) => d.service_instance_id === inst.id)
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, Number(r.query.get("limit") ?? 50));
});
// v1.6 image auto updates: the mock registry never has newer versions.
route("GET", "/services/{sid}/instances/{eid}/image-updates", ([sid, eid]) => (getInstance(sid, eid), []));
route("POST", "/services/{sid}/instances/{eid}/image-updates/check", ([sid, eid]) => (getInstance(sid, eid), []));
route("GET", "/deployments/{did}", ([did]) => getDeployment(did));
route("GET", "/deployments/{did}/cron-runs", ([did]) => {
  const d = getDeployment(did);
  const ids = new Set(S.deployments.filter((x) => x.service_instance_id === d.service_instance_id).map((x) => x.id));
  return S.cronRuns.filter((c) => ids.has(c.deployment_id)).sort((a, b) => b.started_at.localeCompare(a.started_at));
});
route("POST", "/services/{sid}/instances/{eid}/cron/run", ([sid, eid]) => {
  const inst = getInstance(sid, eid);
  if (!inst.current_deployment_id) throw new HttpError(409, "먼저 배포하세요");
  const run: CronRun = { id: rid("run"), deployment_id: inst.current_deployment_id, started_at: iso(now()), finished_at: null, exit_code: null, status: "running" };
  S.cronRuns.push(run);
  setTimeout(() => Object.assign(run, { finished_at: iso(now()), exit_code: 0, status: "succeeded" }), 4200);
  return [202, run];
});

route("GET", "/services/{sid}/instances/{eid}/metrics", ([sid, eid], r) => {
  const range = (r.query.get("range") ?? "1h") as MetricRange;
  const inst = getInstance(sid, eid);
  return { points: metricsFor(inst, range), step_s: RANGE[range].step, replicas: inst.replicas };
});
route("GET", "/environments/{eid}/metrics", ([eid], r) => {
  const range = (r.query.get("range") ?? "1h") as MetricRange;
  return { services: S.instances.filter((i) => i.environment_id === eid).map((i) => ({ service_id: i.service_id, points: metricsFor(i, range) })) };
});

route("POST", "/environments/{eid}/volumes", ([eid], r) => {
  const env = getEnv(eid);
  const b = r.body as { service_id: string; mount_path: string; name?: string; size_limit_mb?: number | null };
  checkSizeLimit(b.size_limit_mb);
  if (!b.mount_path?.startsWith("/")) throw new HttpError(400, "mount_path must be absolute");
  const inst = getInstance(b.service_id, eid);
  if (S.volumes.some((v) => v.service_instance_id === inst.id)) throw new HttpError(409, "이 서비스에는 이미 볼륨이 연결되어 있습니다");
  const v: Volume = { id: rid("vol"), project_id: env.project_id, environment_id: eid, service_instance_id: inst.id, name: b.name || `${getService(b.service_id).name}-volume`, mount_path: b.mount_path, size_bytes: 0, size_limit_mb: b.size_limit_mb ?? null, created_at: iso(now()) };
  S.volumes.push(v);
  emit({ project_id: env.project_id, environment_id: eid, service_id: b.service_id, kind: "volume.created", message: "볼륨 생성" });
  return [201, v];
});
/** v1.5: size_limit_mb is a soft limit (MiB): 1..16777216 or null. */
function checkSizeLimit(n: unknown) {
  if (n === undefined || n === null) return;
  if (typeof n !== "number" || !Number.isInteger(n) || n < 1 || n > 16777216) throw new HttpError(400, "size_limit_mb must be 1..16777216 or null");
}
route("PATCH", "/volumes/{vid}", ([vid], r) => {
  const b = r.body as Partial<Volume>;
  checkSizeLimit(b.size_limit_mb);
  return Object.assign(getVolume(vid), b);
});
route("DELETE", "/volumes/{vid}", ([vid]) => {
  const v = getVolume(vid);
  if (v.service_instance_id) throw new HttpError(409, "연결된 볼륨은 삭제할 수 없습니다. 먼저 연결을 해제하세요");
  S.volumes = S.volumes.filter((x) => x.id !== vid);
  return 204;
});
route("GET", "/volumes/{vid}/backups", ([vid]) => S.backups.filter((b) => b.volume_id === vid).sort((a, b) => b.created_at.localeCompare(a.created_at)));
route("POST", "/volumes/{vid}/backups", ([vid]) => {
  const v = getVolume(vid);
  const b: Backup = { id: rid("bak"), volume_id: vid, size_bytes: 0, created_at: iso(now()), status: "running", error: null };
  S.backups.push(b);
  setTimeout(() => {
    Object.assign(b, { status: "done", size_bytes: Math.round((v.size_bytes ?? 1e8) * 0.22) });
    emit({ project_id: v.project_id, environment_id: v.environment_id, service_id: null, kind: "backup.done", message: "백업 완료" });
  }, 3500);
  return [202, b];
});
route("POST", "/backups/{bid}/restore", ([bid]) => {
  const b = S.backups.find((x) => x.id === bid);
  if (!b) throw new HttpError(404, "backup not found");
  const v = getVolume(b.volume_id);
  const inst = S.instances.find((i) => i.id === v.service_instance_id);
  if (inst) setTimeout(() => startDeployment(inst, "redeploy", S.deployments.find((d) => d.id === inst.current_deployment_id)), 500);
  return 202;
});
route("DELETE", "/backups/{bid}", ([bid]) => {
  S.backups = S.backups.filter((b) => b.id !== bid);
  return 204;
});
route("PUT", "/volumes/{vid}/backup-schedule", ([vid], r) => {
  getVolume(vid);
  schedules[vid] = r.body as { cron: string | null; keep: number };
  return 204;
});

// v1.5: 봉인된 공유 변수 이름(값은 "<sealed>"만 들고 있다).
const sharedSealed: Record<string, string[]> = {};
route("GET", "/environments/{eid}/shared-variables", ([eid], r) => {
  getEnv(eid);
  const values = S.shared[eid] ?? {};
  const d = r.query.get("detail");
  return d === "1" || d === "true" ? { values, sealed: sharedSealed[eid] ?? [] } : values;
});
route("PUT", "/environments/{eid}/shared-variables", ([eid], r) => {
  getEnv(eid);
  const body = (r.body ?? {}) as Record<string, unknown>;
  const isNew = body.values !== undefined && (body.values === null || typeof body.values === "object");
  const values = { ...((isNew ? body.values : body) ?? {}) } as Record<string, string>;
  const add = isNew ? ((body.sealed as string[] | undefined) ?? []) : [];
  for (const k of add) if (!(k in values)) throw new HttpError(400, `cannot seal ${k}: no such variable`);
  const sealed = new Set([...(sharedSealed[eid] ?? []), ...add].filter((k) => k in values));
  for (const k of sealed) values[k] = SEALED_VALUE;
  sharedSealed[eid] = [...sealed].sort();
  S.shared[eid] = values;
  return 204;
});

route("GET", "/templates", (_, r) => {
  const q = (r.query.get("q") ?? "").trim().toLowerCase();
  const category = r.query.get("category") ?? "";
  const tag = r.query.get("tag") ?? "";
  const sort = r.query.get("sort") ?? "";
  let list = allTemplates().filter((t) => {
    if (category && t.category !== category) return false;
    if (tag && !t.tags.includes(tag)) return false;
    const hay = [t.key, t.name, t.description, t.category, ...t.tags, ...t.services.map((s) => s.name)].join(" ").toLowerCase();
    return q.split(/\s+/).every((w) => !w || hay.includes(w));
  });
  if (sort === "popular") list = [...list].sort((a, b) => b.deploy_count - a.deploy_count);
  if (sort === "new") list = [...list].sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? ""));
  return list.map(templateView);
});
const findTemplate = (key: string) => {
  const t = allTemplates().find((x) => x.key === key);
  if (!t) throw new HttpError(404, "template not found");
  return t;
};
route("GET", "/templates/{key}", ([key]) => findTemplate(key));
route("POST", "/templates", (_, r) => {
  const b = (r.body ?? {}) as TemplateSaveInput;
  const d = b.definition;
  if (!d || typeof d !== "object") throw new HttpError(400, "definition is required");
  if (d.version !== 1) throw new HttpError(400, "invalid template: version must be 1", { problems: ["version must be 1"] });
  if (!Array.isArray(d.services) || d.services.length === 0) throw new HttpError(400, "invalid template: services must not be empty", { problems: ["services must not be empty"] });
  const name = (b.name ?? d.name ?? "").trim();
  const key = (b.key ?? d.key ?? name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")).trim();
  if (!key || !/^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$/.test(key)) throw new HttpError(400, "key must be lowercase letters, digits and '-' (max 64)");
  if (TEMPLATES.some((t) => t.key === key)) throw new HttpError(409, `template key is used by a built-in template: ${key}`);
  if (USER_TEMPLATES.some((t) => t.key === key)) throw new HttpError(409, `template already exists: ${key}`);
  const services = d.services.map((s) => {
    const src = (s.source ?? {}) as { image?: string; database?: DatabaseKind; repo?: { url: string } };
    return src.database
      ? { name: String(s.name), source_type: "database" as const, database_kind: src.database, icon: src.database }
      : src.repo
        ? { name: String(s.name), source_type: "repo" as const, repo_url: src.repo.url, icon: "github" }
        : { name: String(s.name), source_type: "image" as const, image: src.image, icon: (s.icon as string) || "docker" };
  });
  const t: MockTemplate = {
    key,
    name: name || key,
    description: b.description ?? d.description ?? "",
    icon: b.icon ?? d.icon ?? services[0]?.icon ?? "docker",
    category: b.category ?? d.category ?? "",
    tags: b.tags ?? d.tags ?? [],
    services,
    inputs: d.inputs ?? [],
    builtin: false,
    visibility: b.visibility ?? "private",
    owner_user_id: me().id,
    deploy_count: 0,
    created_at: iso(now()),
    updated_at: iso(now()),
    can_edit: true,
    readme: b.readme ?? d.readme ?? "",
    definition: { ...d, key, name: name || key },
  };
  USER_TEMPLATES.unshift(t);
  audit("template.created", key, `템플릿 ${t.name}(${key}) 등록`);
  return [201, t];
});
const editableTemplate = (key: string) => {
  if (TEMPLATES.some((t) => t.key === key)) throw new HttpError(400, "built-in templates cannot be changed");
  const t = USER_TEMPLATES.find((x) => x.key === key);
  if (!t) throw new HttpError(404, "template not found");
  return t;
};
route("PATCH", "/templates/{key}", ([key], r) => {
  const t = editableTemplate(key);
  const b = (r.body ?? {}) as Partial<TemplateSaveInput>;
  if (b.visibility) t.visibility = b.visibility;
  if (b.name !== undefined) t.name = b.name;
  if (b.description !== undefined) t.description = b.description;
  if (b.readme !== undefined) t.readme = b.readme;
  if (b.tags) t.tags = b.tags;
  if (b.definition) t.definition = { ...b.definition, key };
  t.updated_at = iso(now());
  return t;
});
route("DELETE", "/templates/{key}", ([key]) => {
  const t = editableTemplate(key);
  USER_TEMPLATES.splice(USER_TEMPLATES.indexOf(t), 1);
  return 204;
});
/** Mirrors the server's input checks (required, full-match pattern, unknown keys). */
function checkTemplateInputs(t: MockTemplate, given: Record<string, string>) {
  const missing: string[] = [];
  const invalid: string[] = [];
  const unknown = Object.keys(given).filter((k) => !t.inputs.some((i) => i.key === k));
  for (const i of t.inputs) {
    const v = given[i.key] || i.default || "";
    if (i.required && !v.trim()) missing.push(i.key);
    else if (i.pattern && given[i.key] && !new RegExp(`^(?:${i.pattern})$`).test(given[i.key])) invalid.push(i.key);
  }
  if (missing.length || invalid.length || unknown.length) {
    const msgs = [...missing.map((k) => `${k}: required`), ...invalid.map((k) => `${k}: does not match pattern`), ...unknown.map((k) => `${k}: unknown input`)];
    throw new HttpError(400, `invalid inputs: ${msgs.join("; ")}`, { missing, invalid, unknown });
  }
}
route("POST", "/templates/{key}/deploy", ([key], r) => {
  const b = (r.body ?? {}) as { project_id?: string; environment_id?: string; inputs?: Record<string, string> };
  if (!b.project_id) throw new HttpError(400, "project_id is required");
  return deployMockTemplate(b.project_id, key, b.environment_id, b.inputs ?? {});
});
route("POST", "/projects/{pid}/environments/{eid}/export-template", ([pid, eid], r) => {
  const p = getProject(pid);
  const env = getEnv(eid);
  const mode = r.query.get("secrets") ?? "generate";
  if (mode !== "generate" && mode !== "inputs") throw new HttpError(400, "secrets must be generate or inputs");
  const inputs: { key: string; label: string; required: boolean; secret: boolean }[] = [];
  const services = S.services
    .filter((s) => s.project_id === pid && !s.stack_id)
    .map((s) => {
      const inst = S.instances.find((i) => i.service_id === s.id && i.environment_id === eid);
      const variables: Record<string, string> = {};
      if (inst && !s.database_kind)
        for (const [k, v] of Object.entries(inst.variables)) {
          if (!v.includes("${{") && /(SECRET|PASSWORD|TOKEN|KEY|PRIVATE)/i.test(k)) {
            if (mode === "inputs") {
              const ik = `${s.name}_${k}`.toUpperCase().replace(/[^A-Z0-9_]/g, "_");
              inputs.push({ key: ik, label: `${s.name}: ${k}`, required: true, secret: true });
              variables[k] = `\${{ inputs.${ik} }}`;
            } else variables[k] = "${{ secret(32) }}";
          } else variables[k] = v;
        }
      const source = s.database_kind ? { database: s.database_kind } : inst?.repo_url ? { repo: { url: inst.repo_url, branch: inst.branch } } : { image: inst?.image ?? "nginx:alpine" };
      const domains = (inst?.domains ?? []).filter((d) => d.kind === "generated").map((d) => ({ generate: true, ...(d.target_port ? { target_port: d.target_port } : {}) }));
      return { name: s.name, ...(s.icon ? { icon: s.icon } : {}), source, ...(Object.keys(variables).length ? { variables } : {}), ...(domains.length ? { domains } : {}), ...(inst?.canvas ? { canvas: inst.canvas } : {}) };
    });
  if (!services.length) throw new HttpError(400, "내보낼 서비스가 없습니다");
  return {
    definition: { version: 1, key: `${p.name}-${env.name}`.toLowerCase().replace(/[^a-z0-9]+/g, "-"), name: p.name, description: p.description, ...(inputs.length ? { inputs } : {}), services },
    warnings: S.services.some((s) => s.project_id === pid && s.stack_id) ? ["compose 스택 서비스는 내보내지 않습니다"] : [],
  };
});
route("POST", "/projects/{pid}/templates/{key}", ([pid, key], r) => {
  const b = (r.body ?? {}) as { environment_id?: string; inputs?: Record<string, string> };
  return deployMockTemplate(pid, key, b.environment_id, b.inputs ?? {});
});
function deployMockTemplate(pid: string, key: string, environmentId: string | undefined, given: Record<string, string>): [number, Service[]] {
  const t = findTemplate(key);
  const p = getProject(pid);
  const eid = environmentId || p.default_environment_id;
  checkTemplateInputs(t, given);
  t.deploy_count++;
  const out: Service[] = [];
  const db = t.services.find((s) => s.source_type === "database");
  for (const s of t.services) {
    const svc = createService(pid, { name: uniqueName(pid, s.name), source_type: s.source_type, database_kind: s.database_kind, image: s.image, repo_url: s.repo_url, environment_id: eid }, key);
    out.push(svc);
  }
  if (db && out.length > 1) {
    const app = out.find((s) => s.source_type !== "database")!;
    const dbs = out.find((s) => s.source_type === "database")!;
    const key2 = db.database_kind === "mysql" ? "MYSQL_URL" : "DATABASE_URL";
    for (const i of S.instances.filter((x) => x.service_id === app.id)) i.variables.DATABASE_URL = `\${{${dbs.name}.${key2}}}`;
    for (const i of S.instances.filter((x) => x.service_id === dbs.id && x.environment_id === eid)) {
      const ai = S.instances.find((x) => x.service_id === app.id && x.environment_id === eid)!;
      if (i.canvas && ai.canvas) i.canvas = { x: ai.canvas.x + 420, y: ai.canvas.y };
    }
  }
  if (Object.keys(given).length) {
    for (const i of S.instances.filter((x) => out.some((s) => s.id === x.service_id) && x.environment_id === eid))
      for (const [k, v] of Object.entries(given)) i.variables[k] = v;
  }
  return [201, out];
}

route("GET", "/projects/{pid}/events", ([pid], r) => {
  const eid = r.query.get("environment_id");
  return S.events
    .filter((e) => e.project_id === pid && (!eid || !e.environment_id || e.environment_id === eid))
    .slice(0, Number(r.query.get("limit") ?? 50));
});

route("GET", "/system", () => ({
  version: "1.0.0-dev (mock)",
  node: "example.com",
  docker: "ok",
  proxy: "ok",
  base_domain: BASE,
  public_ipv4: "192.0.2.10",
  public_ipv6: "2001:db8:1::10",
  disk_free_bytes: 182.4e9,
  cpu_cores: 8,
  tcp_proxy_domain: BASE,
  mesh: { mode: "auto", port: 7443, private_address: NODES[0]?.address.private ?? null, enabled: meshCapable(NODES[0]) },
}));
/** v1.5 mock: a node is on the mesh when its private address is a private IPv4 (10/8, 100.64/10, 192.168/16). */
function meshCapable(n?: AvenNode): boolean {
  const p = n?.address.private ?? "";
  return /^(10\.|192\.168\.|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.)/.test(p);
}
route("GET", "/nodes", () => {
  NODES.forEach((n) => n.status === "online" && (n.last_seen_at = iso(now())));
  const on = NODES.filter((n) => meshCapable(n));
  return NODES.map((n) => ({
    ...n,
    ipv6_status: n.ipv6_pool
      ? {
          pool: n.ipv6_pool,
          supported: n.status === "online" ? n.version >= "1.2.0" : null,
          ...(n.status === "online" && n.version < "1.2.0" ? { reason: "이 노드의 aven-agent가 도메인별 IPv6(ipv6.addrs/ipv6.ensure)를 지원하지 않습니다. agent를 업그레이드하세요" } : {}),
          iface: n.is_local ? "eth0" : "eth0",
          desired: Object.values(domainIPv6).filter((a) => a.startsWith(n.ipv6_pool!.split("/")[0].replace(/::$/, ""))).length,
          present: Object.values(domainIPv6).filter((a) => a.startsWith(n.ipv6_pool!.split("/")[0].replace(/::$/, ""))).length,
          checked_at: iso(now()),
        }
      : null,
    mesh: meshCapable(n)
      ? {
          enabled: true,
          listen: `${n.address.private}:7443`,
          peers: on
            .filter((o) => o.id !== n.id)
            .map((o) => ({
              node_id: o.id,
              status: o.status === "online" && n.status === "online" ? "ok" : "unreachable",
              last_ok_at: o.status === "online" ? iso(now()) : null,
              rtt_ms: o.status === "online" ? 3.2 : null,
            })),
        }
      : { enabled: false, listen: null, peers: [], reason: "노드에 사설 주소(--private-address)가 없어 mesh를 쓸 수 없습니다" },
  }));
});
let pendingJoin: { name: string; at: number } | null = null;
route("POST", "/nodes/join-tokens", (_, r) => {
  const b = (r.body ?? {}) as { name?: string; ttl_s?: number };
  const token = `kjt_${secret(32)}`;
  pendingJoin = { name: b.name || `node-${NODES.length + 1}`, at: now() };
  // The fake agent "runs" the command a few seconds later.
  setTimeout(() => {
    if (!pendingJoin) return;
    NODES.push({
      id: rid("node"),
      name: pendingJoin.name,
      is_local: false,
      status: "online",
      schedulable: true,
      address: { ipv4: "192.0.2.13", ipv6: "2001:db8:4::1", private: "100.64.0.12" },
      version: "1.2.0",
      docker_version: "28.4.0",
      cpu_cores: 8,
      memory_bytes: 32e9,
      disk_free_bytes: 220e9,
      labels: {},
      last_seen_at: iso(now()),
      created_at: iso(now()),
    });
    emit({ project_id: "prj_sample", environment_id: null, service_id: null, kind: "node.joined", message: `노드 ${pendingJoin.name} 연결됨` });
    pendingJoin = null;
  }, 9000);
  return [201, { token, expires_at: iso(now() + (b.ttl_s ?? 3600) * 1000), command: `curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token ${token}` }];
});
route("PATCH", "/nodes/{nid}", ([nid], r) => {
  const n = NODES.find((x) => x.id === nid);
  if (!n) throw new HttpError(404, "node not found");
  const b = r.body as { name?: string; schedulable?: boolean; labels?: Record<string, string>; ipv6_pool?: string };
  if (b.name !== undefined && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(b.name)) throw new HttpError(400, "name must match [a-z0-9-]");
  if (b.ipv6_pool !== undefined) {
    const pool = b.ipv6_pool.trim();
    const m = /^([0-9a-f:]+)\/(\d{1,3})$/i.exec(pool);
    if (pool && (!m || !m[1].includes(":") || +m[2] < 64 || +m[2] > 120)) throw new HttpError(400, `IPv6 풀 ${pool}의 크기는 /64 ~ /120여야 합니다`);
    if (pool && n.address.ipv6 && pool.split("/")[0].replace(/::$/, "") === n.address.ipv6.replace(/::[0-9a-f]+$/, ""))
      throw new HttpError(409, `IPv6 풀 ${pool}에 노드의 기본 IPv6 주소 ${n.address.ipv6}가 들어 있습니다`);
    const other = NODES.find((o) => o.id !== n.id && pool && o.ipv6_pool === pool);
    if (other) throw new HttpError(409, `IPv6 풀 ${pool}가 노드 ${other.name}의 풀 ${other.ipv6_pool}와 겹칩니다`);
    b.ipv6_pool = pool;
  }
  Object.assign(n, b);
  return n;
});
route("DELETE", "/nodes/{nid}", ([nid], r) => {
  const n = NODES.find((x) => x.id === nid);
  if (!n) throw new HttpError(404, "node not found");
  if (n.is_local) throw new HttpError(400, "로컬 노드는 삭제할 수 없습니다");
  const placed = S.instances.filter((i) => i.node_id === nid);
  if (placed.length && r.query.get("force") !== "1") throw new HttpError(409, `서비스 ${placed.length}개가 이 노드에 배치되어 있습니다`);
  for (const i of placed) i.status = "offline";
  NODES.splice(NODES.indexOf(n), 1);
  return 204;
});
route("GET", "/nodes/{nid}/metrics", ([nid], r) => {
  const n = NODES.find((x) => x.id === nid);
  if (!n) throw new HttpError(404, "node not found");
  const { span, step } = RANGE[(r.query.get("range") ?? "1h") as MetricRange];
  const end = Math.floor(now() / 1000 / step) * step;
  const sd = hash(nid);
  const points: NodeMetricPoint[] = [];
  for (let t = end - span; t <= end; t += step) {
    if (n.status === "offline" && t > end - 3 * 3600) continue;
    const w = Math.sin(t / 900 + sd) * 0.5 + Math.sin(t / 211 + sd * 7) * 0.25;
    points.push({ t: iso(t * 1000), cpu_percent: Math.max(2, (n.cpu_cores * 100) * (0.08 + 0.05 * w)), memory_bytes: n.memory_bytes * (0.42 + 0.04 * w), disk_free_bytes: n.disk_free_bytes });
  }
  return { points };
});
route("GET", "/projects/{pid}/registries", ([pid]) => (getProject(pid), (registries[pid] ?? []).map((x) => ({ host: x.host, username: x.username, has_password: !!x.password }))));
route("PUT", "/projects/{pid}/registries", ([pid], r) => {
  getProject(pid);
  const list = r.body as { host: string; username: string; password: string | null }[];
  const old = registries[pid] ?? [];
  const next = list.map((x) => ({ host: x.host, username: x.username.trim(), password: x.password ?? old.find((o) => o.host === x.host)?.password ?? "" }));
  if (new Set(next.map((x) => x.host)).size !== next.length) throw new HttpError(400, "duplicate registry host");
  for (const x of next) {
    if (!/^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$/.test(x.host)) throw new HttpError(400, "invalid registry host: " + x.host);
    if (!x.username || !x.password) throw new HttpError(400, "username and password are required for " + x.host);
  }
  registries[pid] = next;
  return next.map((x) => ({ host: x.host, username: x.username, has_password: true }));
});
route("GET", "/system/github", () => ({ webhook_url: "https://aven.example.com/api/webhooks/github", secret_configured: true, token_configured: false }));
route("GET", "/github/connection", () => {
  const conn = GITHUB_CONN[me().id];
  return {
    app_configured: true,
    connected: !!conn,
    source: conn ? "oauth" : "",
    login: conn?.login,
    connected_at: conn?.connected_at,
    callback_url: "https://aven.example.com/api/github/oauth/callback",
    webhook_url: "https://aven.example.com/api/webhooks/github",
    secret_configured: true,
  };
});
route("DELETE", "/github/connection", () => {
  delete GITHUB_CONN[me().id];
  // 이 사용자 연결을 쓰던 프로젝트는 서버 토큰으로 되돌린다.
  for (const p of S.projects) if (p.github_user_id === me().id) p.github_user_id = null;
  return 204;
});
route("GET", "/github/oauth/start", () => ({ url: "https://github.com/login/oauth/authorize?client_id=mock" }));
route("GET", "/github/repos", () => {
  if (!GITHUB_CONN[me().id]) throw new HttpError(409, "GitHub is not connected");
  return [
    { full_name: `${GITHUB_CONN[me().id].login}/example`, private: false, default_branch: "main", url: "https://github.com/example", pushed_at: ago(60), admin: true },
  ];
});
route("POST", "/github/webhooks", (_, r) => {
  if (!GITHUB_CONN[me().id]) throw new HttpError(409, "GitHub is not connected");
  const repo = ((r.body ?? {}) as { repo?: string }).repo ?? "";
  return { repo: repo.replace(/^https?:\/\/github\.com\//, "").replace(/\.git$/, ""), hook_id: 1, created: true, url: "https://github.com/hooks/1" };
});

/* ── v1.5: notifications, alert rules, usage ────────────────────────── */

const CHANNELS: (NotificationChannel & { secret?: string })[] = [];
const DELIVERIES: NotificationDelivery[] = [];
const NOTIFS: UserNotification[] = [];
let notifPrefs = ["deploy.failed", "deploy.crashed", "volume.limit_exceeded", "alert.*"];
const KNOWN_KINDS = [
  "deploy.requested", "deploy.started", "deploy.live", "deploy.failed", "deploy.crashed", "deploy.rollback", "service.restarted", "service.scaled",
  "service.sleeping", "volume.near_limit", "volume.limit_exceeded", "backup.failed", "dbbackup.failed", "cron.failed",
  "alert.firing", "alert.resolved", "usage.threshold_reached", "member.added", "variables.updated",
];
const globMatch = (pats: string[], kind: string) =>
  pats.some((p) => new RegExp("^" + p.replace(/[.]/g, "\\.").replace(/\*/g, ".*") + "$").test(kind));
onEvent((e) => {
  if (!globMatch(notifPrefs, e.kind)) return;
  NOTIFS.unshift({ id: rid("ntf"), event_id: e.id, project_id: e.project_id, environment_id: e.environment_id, service_id: e.service_id,
    kind: e.kind, message: e.message, created_at: e.created_at, read_at: null });
});
NOTIFS.push(
  { id: "ntf_seed1", event_id: "evt_seed1", project_id: "prj_sample", environment_id: null, service_id: null, kind: "alert.firing", message: "알림 발생: cpu_percent > 80 — api (production) (현재 92.40, 기준 cpu_percent > 80 (60초 동안))", created_at: ago(12), read_at: null },
  { id: "ntf_seed2", event_id: "evt_seed2", project_id: "prj_sample", environment_id: null, service_id: null, kind: "deploy.failed", message: "worker (production) 배포 실패: healthcheck timed out after 5m0s", created_at: ago(180), read_at: ago(100) },
);

function channelInput(c: NotificationChannel & { secret?: string }, b: NotificationChannelInput) {
  if (b.name !== undefined) c.name = b.name.trim();
  if (!c.name) throw new HttpError(400, "name is required (max 100 chars)");
  if (b.url !== undefined) c.url = b.url.trim();
  if (b.secret !== undefined) c.secret = b.secret;
  if (b.recipients !== undefined) c.recipients = b.recipients.map((x) => x.trim()).filter(Boolean);
  if (b.events !== undefined) c.events = b.events;
  if (b.enabled !== undefined) c.enabled = b.enabled;
  if (c.kind === "email") {
    c.url = null;
    if (!c.recipients.length) throw new HttpError(400, "email channels need 1-20 recipients");
  } else if (!c.url || !/^https?:\/\/[^/@]+/.test(c.url)) throw new HttpError(400, "url must be an http(s) URL without credentials");
  else if (/^https?:\/\/(localhost|127\.|10\.|192\.168\.|169\.254\.|\[::1\])/.test(c.url))
    throw new HttpError(400, "사설·루프백·링크 로컬 주소로는 알림을 보낼 수 없습니다 (AVEN_NOTIFY_ALLOW_PRIVATE=1로 허용)");
  c.secret_set = !!c.secret;
}
const viewCh = (c: NotificationChannel & { secret?: string }): NotificationChannel => {
  const { secret: _s, ...rest } = c;
  void _s;
  return rest;
};
function findChannel(pid: string | null, cid: string) {
  const c = CHANNELS.find((x) => x.id === cid && x.project_id === pid);
  if (!c) throw new HttpError(404, "not found");
  return c;
}
function channelRoutes(prefix: string, pidOf: (m: string[]) => string | null) {
  route("GET", prefix, (m) => CHANNELS.filter((c) => c.project_id === pidOf(m)).map(viewCh));
  route("POST", prefix, (m, r) => {
    const b = (r.body ?? {}) as NotificationChannelInput;
    if (!b.kind || !["webhook", "slack", "discord", "email"].includes(b.kind)) throw new HttpError(400, "kind must be webhook, slack, discord or email");
    const c: NotificationChannel & { secret?: string } = { id: rid("nch"), project_id: pidOf(m), kind: b.kind, name: "", url: null, secret_set: false,
      recipients: [], events: ["deploy.failed", "deploy.crashed", "alert.*", "volume.limit_exceeded", "usage.threshold_reached"], enabled: true, created_at: iso(now()) };
    channelInput(c, b);
    CHANNELS.push(c);
    return [201, viewCh(c)];
  });
  route("PATCH", prefix + "/{cid}", (m, r) => {
    const c = findChannel(pidOf(m), m[m.length - 1]);
    const b = (r.body ?? {}) as NotificationChannelInput;
    if (b.kind && b.kind !== c.kind) throw new HttpError(400, "kind는 바꿀 수 없습니다");
    channelInput(c, b);
    return viewCh(c);
  });
  route("DELETE", prefix + "/{cid}", (m) => {
    const c = findChannel(pidOf(m), m[m.length - 1]);
    CHANNELS.splice(CHANNELS.indexOf(c), 1);
    return 204;
  });
  route("POST", prefix + "/{cid}/test", (m) => {
    const c = findChannel(pidOf(m), m[m.length - 1]);
    const ok = c.kind !== "email";
    const d: NotificationDelivery = { id: rid("dlv"), channel_id: c.id, event_id: rid("evt"), event_kind: "notification.test", status: ok ? "ok" : "failed",
      attempts: 1, status_code: ok ? 200 : null, error: ok ? "" : "SMTP가 설정되지 않았습니다 (AVEN_SMTP_HOST)", duration_ms: 84, created_at: iso(now()) };
    DELIVERIES.unshift(d);
    return { ok, attempts: 1, status_code: d.status_code, duration_ms: 84, error: ok ? null : d.error };
  });
  route("GET", prefix + "/{cid}/deliveries", (m) => {
    const c = findChannel(pidOf(m), m[m.length - 1]);
    return DELIVERIES.filter((d) => d.channel_id === c.id).slice(0, 100);
  });
}
channelRoutes("/projects/{pid}/notification-channels", (m) => (getProject(m[0]), m[0]));
channelRoutes("/notification-channels", () => null);
route("POST", "/system/smtp/test", () => {
  throw new HttpError(503, "SMTP가 설정되지 않았습니다 (AVEN_SMTP_HOST, AVEN_SMTP_FROM)");
});
route("GET", "/notifications", (_, r) => {
  const unread = r.query.get("unread") === "1";
  const limit = Number(r.query.get("limit") ?? 50);
  return { notifications: NOTIFS.filter((n) => !unread || !n.read_at).slice(0, limit), unread: NOTIFS.filter((n) => !n.read_at).length };
});
route("POST", "/notifications/read", (_, r) => {
  const b = (r.body ?? {}) as { ids?: string[]; all?: boolean };
  if (!b.all && !b.ids?.length) throw new HttpError(400, "ids or all is required");
  let updated = 0;
  for (const n of NOTIFS) {
    if (!n.read_at && (b.all || b.ids?.includes(n.id))) {
      n.read_at = iso(now());
      updated++;
    }
  }
  return { updated };
});
route("GET", "/auth/notification-preferences", () => ({ events: notifPrefs, defaults: ["deploy.failed", "deploy.crashed", "volume.limit_exceeded", "alert.*"], available: KNOWN_KINDS }));
route("PUT", "/auth/notification-preferences", (_, r) => {
  const b = (r.body ?? {}) as { events?: string[] };
  if (!b.events) throw new HttpError(400, "events is required");
  if (b.events.some((e) => !/^[a-z0-9_.*]{1,100}$/.test(e))) throw new HttpError(400, "invalid event pattern");
  notifPrefs = b.events;
  return { events: notifPrefs, defaults: ["deploy.failed", "deploy.crashed", "volume.limit_exceeded", "alert.*"], available: KNOWN_KINDS };
});

const ALERTS: AlertRule[] = [];
function applyAlert(a: AlertRule, b: AlertRuleInput) {
  Object.assign(a, Object.fromEntries(Object.entries(b).filter(([, v]) => v !== undefined)));
  if (a.comparator !== ">" && a.comparator !== "<") throw new HttpError(400, "comparator must be > or <");
  if (!Number.isFinite(a.threshold) || a.threshold < 0) throw new HttpError(400, "threshold must be a non-negative number");
  if (a.for_seconds < 0 || a.for_seconds > 86400) throw new HttpError(400, "for_seconds must be between 0 and 86400");
  a.last_state = "ok";
  a.pending_since = null;
}
route("GET", "/projects/{pid}/alert-rules", ([pid]) => ALERTS.filter((a) => a.project_id === pid));
route("POST", "/projects/{pid}/alert-rules", ([pid], r) => {
  getProject(pid);
  const b = (r.body ?? {}) as AlertRuleInput;
  if (b.threshold === undefined) throw new HttpError(400, "threshold is required");
  const a: AlertRule = { id: rid("alr"), project_id: pid, environment_id: null, service_id: null, name: "", metric: "cpu_percent", comparator: ">",
    threshold: 0, for_seconds: 0, enabled: true, last_state: "ok", last_value: null, pending_since: null, last_fired_at: null, last_evaluated_at: null, created_at: iso(now()) };
  applyAlert(a, b);
  // 모의 평가: 임의의 현재 값으로 상태를 보여 준다.
  a.last_value = Math.round(Math.random() * a.threshold * 1.4 * 100) / 100;
  a.last_evaluated_at = iso(now());
  if (a.last_value > a.threshold && a.comparator === ">") a.last_state = a.for_seconds ? "pending" : "firing";
  ALERTS.push(a);
  return [201, a];
});
route("PATCH", "/projects/{pid}/alert-rules/{id}", ([pid, id], r) => {
  const a = ALERTS.find((x) => x.id === id && x.project_id === pid);
  if (!a) throw new HttpError(404, "not found");
  applyAlert(a, (r.body ?? {}) as AlertRuleInput);
  return a;
});
route("DELETE", "/projects/{pid}/alert-rules/{id}", ([pid, id]) => {
  const i = ALERTS.findIndex((x) => x.id === id && x.project_id === pid);
  if (i < 0) throw new HttpError(404, "not found");
  ALERTS.splice(i, 1);
  return 204;
});

function mockUsage(pid: string, from: Date, to: Date) {
  const rows: { key: string; env: string; day: string; t: UsageTotals }[] = [];
  for (const inst of S.instances) {
    const svc = S.services.find((s) => s.id === inst.service_id);
    if (!svc || svc.project_id !== pid) continue;
    const h = hash(svc.name + inst.environment_id);
    for (let d = new Date(from); d < to; d = new Date(d.getTime() + 86400_000)) {
      const k = 0.6 + ((h >> 3) % 50) / 100;
      rows.push({ key: svc.id, env: inst.environment_id, day: d.toISOString().slice(0, 10),
        t: { cpu_core_hours: ((h % 7) + 1) * 0.8 * k, memory_gb_hours: ((h % 5) + 1) * 3.1 * k, net_egress_gb: ((h % 3) + 1) * 0.12 * k, disk_gb_hours: svc.source_type === "database" ? 24 * 1.4 : 0 } });
    }
  }
  return rows;
}
const addT = (a: UsageTotals, b: UsageTotals) => {
  a.cpu_core_hours += b.cpu_core_hours;
  a.memory_gb_hours += b.memory_gb_hours;
  a.net_egress_gb += b.net_egress_gb;
  a.disk_gb_hours += b.disk_gb_hours;
};
const zeroT = (): UsageTotals => ({ cpu_core_hours: 0, memory_gb_hours: 0, net_egress_gb: 0, disk_gb_hours: 0 });
function usageRange(q: URLSearchParams) {
  const n = new Date();
  const from = q.get("from") ? new Date(q.get("from")!) : new Date(Date.UTC(n.getUTCFullYear(), n.getUTCMonth(), 1));
  let to = q.get("to") ? new Date(q.get("to")!) : n;
  if (q.get("to") && /^\d{4}-\d{2}-\d{2}$/.test(q.get("to")!)) to = new Date(to.getTime() + 86400_000);
  if (isNaN(from.getTime()) || isNaN(to.getTime()) || to <= from) throw new HttpError(400, "to must be after from");
  return { from, to };
}
function summarize(rows: { key: string; day: string; t: UsageTotals }[], names: Record<string, string>) {
  const totals = zeroT();
  const g = new Map<string, UsageGroup>();
  const s = new Map<string, UsageSeriesPoint>();
  for (const r of rows) {
    addT(totals, r.t);
    if (!g.has(r.key)) g.set(r.key, { key: r.key, name: names[r.key] ?? r.key, ...zeroT() });
    addT(g.get(r.key)!, r.t);
    if (!s.has(r.day)) s.set(r.day, { t: r.day, ...zeroT() });
    addT(s.get(r.day)!, r.t);
  }
  return { totals, groups: [...g.values()], series: [...s.values()].sort((a, b) => a.t.localeCompare(b.t)) };
}
route("GET", "/projects/{pid}/usage", ([pid], r) => {
  const p = getProject(pid);
  const { from, to } = usageRange(r.query);
  const group = (r.query.get("group") ?? "service") as "service" | "environment" | "day";
  const names: Record<string, string> = {};
  S.services.forEach((s) => (names[s.id] = s.name));
  S.envs.forEach((e) => (names[e.id] = e.name));
  const rows = mockUsage(pid, from, to).map((x) => ({ ...x, key: group === "environment" ? x.env : group === "day" ? x.day : x.key }));
  return { project_id: pid, from: from.toISOString(), to: to.toISOString(), group, ...summarize(rows, names), pricing: null, usage_alert: p.usage_alert ?? null, usage_limit: mockProjectView(p).usage_limit, usage_suspension: mockProjectView(p).usage_suspension };
});
route("GET", "/usage", (_, r) => {
  const { from, to } = usageRange(r.query);
  const names: Record<string, string> = {};
  S.projects.forEach((p) => (names[p.id] = p.name));
  const rows = S.projects.flatMap((p) => mockUsage(p.id, from, to).map((x) => ({ ...x, key: p.id })));
  const { totals, groups, series } = summarize(rows, names);
  return { from: from.toISOString(), to: to.toISOString(), group: "project", totals, projects: groups, series, pricing: null };
});

export function handle(method: string, path: string, query: URLSearchParams, body: unknown): { status: number; body?: unknown } {
  for (const [m, re, h] of routes) {
    if (m !== method) continue;
    const match = re.exec(path);
    if (!match) continue;
    try {
      const out = h(match.slice(1).map(decodeURIComponent), { method, path, query, body });
      if (typeof out === "number") return { status: out };
      if (Array.isArray(out) && out.length === 2 && typeof out[0] === "number") return { status: out[0], body: clone(out[1]) };
      return { status: 200, body: clone(out) };
    } catch (e) {
      if (e instanceof HttpError) return { status: e.status, body: { error: e.message, ...(e.extra ?? {}) } };
      return { status: 500, body: { error: String(e) } };
    }
  }
  return { status: 404, body: { error: `mock: no route for ${method} ${path}` } };
}

/* ── streams ─────────────────────────────────────────────────────────── */

export type Emit = (type: string, data: string) => void;

function matcher(q: string): (s: string) => boolean {
  const toks = (q.match(/-?"[^"]*"|\S+/g) ?? []).map((t) => {
    const neg = t.startsWith("-") && t.length > 1;
    return { neg, raw: (neg ? t.slice(1) : t).replace(/^"|"$/g, "").toLowerCase() };
  });
  return (s) => toks.every((t) => !t.raw || s.toLowerCase().includes(t.raw) === !t.neg);
}

const PATHS = ["/", "/api/me", "/api/posts", "/api/posts/42", "/healthz", "/static/app.4f2a1c.js", "/favicon.ico", "/api/login", "/robots.txt"];
const METHODS = ["GET", "GET", "GET", "GET", "POST", "PUT", "DELETE"];
function httpLine(host: string, t: number) {
  const path = pick(PATHS);
  const method = path === "/api/login" ? "POST" : pick(METHODS);
  const r = Math.random();
  const status = path === "/favicon.ico" ? 404 : r < 0.03 ? 502 : r < 0.06 ? 500 : r < 0.12 ? 401 : r < 0.2 ? 304 : method === "POST" ? 201 : 200;
  return JSON.stringify({
    t: iso(t),
    method,
    host,
    path,
    status,
    duration_ms: Math.round((status >= 500 ? 800 : 4) + Math.random() * (path.startsWith("/api") ? 180 : 20)),
    bytes: Math.round(Math.random() * 24000),
    client_ip: pick(["2a02:810d:4b3f::1c", "121.134.22.9", "2001:e60:1032::7", "52.79.10.4"]),
    user_agent: pick(["Mozilla/5.0 (Macintosh) Safari/605.1.15", "curl/8.10.1", "Mozilla/5.0 (iPhone) Mobile/15E148", "UptimeKuma/1.23"]),
  });
}

export function openStream(path: string, query: URLSearchParams, send: Emit, done: () => void): () => void {
  const timers: ReturnType<typeof setInterval>[] = [];
  const offs: (() => void)[] = [];
  const cleanup = () => {
    timers.forEach(clearInterval);
    offs.forEach((f) => f());
  };
  let m: RegExpExecArray | null;

  if ((m = /^\/deployments\/([^/]+)\/build-logs$/.exec(path))) {
    const d = S.deployments.find((x) => x.id === m![1]);
    if (!d) return (send("error", "not found"), done(), cleanup);
    for (const l of S.buildLogs[d.id] ?? []) send("log", l);
    send("status", d.status);
    const fin = (st: string) => ["live", "failed", "crashed", "cancelled", "removed", "skipped"].includes(st);
    if (fin(d.status)) {
      send("done", d.status);
      done();
      return cleanup;
    }
    let last = d.status;
    offs.push(
      onDeployment(d.id, (dd, line) => {
        if (line) send("log", line);
        if (dd.status !== last) {
          last = dd.status;
          send("status", dd.status);
          if (fin(dd.status)) {
            send("done", dd.status);
            done();
          }
        }
      }),
    );
    return cleanup;
  }

  if ((m = /^\/deployments\/([^/]+)\/deploy-logs$/.exec(path))) {
    const d = S.deployments.find((x) => x.id === m![1]);
    if (!d) return (send("error", "not found"), done(), cleanup);
    const tail = Number(query.get("tail") ?? 500);
    for (const l of (S.runtimeLogs[d.id] ?? []).slice(-tail)) send("log", JSON.stringify(l));
    const inst = S.instances.find((i) => i.id === d.service_instance_id);
    const current = inst?.current_deployment_id === d.id && d.status === "live" && inst.status !== "offline";
    if (!current && d.status !== "deploying" && d.status !== "healthchecking") {
      send("done", d.status);
      done();
      return cleanup;
    }
    const svc = getService(d.service_id);
    const lines = RUNTIME[svc.name] ?? RUNTIME.whoami;
    timers.push(
      setInterval(() => {
        if (!d.replicas.length) return;
        const msg = pick(lines);
        const l = { t: iso(now()), replica: pick(d.replicas).name, stream: /error|warn/.test(msg) ? ("stderr" as const) : ("stdout" as const), msg };
        (S.runtimeLogs[d.id] ??= []).push(l);
        send("log", JSON.stringify(l));
      }, 1400),
    );
    return cleanup;
  }

  if ((m = /^\/services\/([^/]+)\/instances\/([^/]+)\/http-logs$/.exec(path))) {
    const inst = S.instances.find((i) => i.service_id === m![1] && i.environment_id === m![2]);
    const host = inst?.domains[0]?.host;
    if (!inst || !host || inst.status !== "online") {
      done();
      return cleanup;
    }
    for (let i = 60; i > 0; i--) send("log", httpLine(host, now() - i * 9000));
    timers.push(setInterval(() => send("log", httpLine(host, now())), 1100));
    return cleanup;
  }

  if ((m = /^\/environments\/([^/]+)\/logs$/.exec(path))) {
    const eid = m[1];
    const only = query.get("services")?.split(",").filter(Boolean);
    const match = matcher(query.get("q") ?? "");
    const insts = S.instances.filter((i) => i.environment_id === eid && (!only || only.includes(i.service_id)));
    const backlog: (Store["runtimeLogs"][string][number] & { service_id: string })[] = [];
    for (const i of insts) {
      const d = S.deployments.find((x) => x.id === i.current_deployment_id);
      if (d) for (const l of S.runtimeLogs[d.id] ?? []) backlog.push({ ...l, service_id: i.service_id });
    }
    backlog.sort((a, b) => a.t.localeCompare(b.t));
    for (const l of backlog.slice(-500)) if (match(l.msg)) send("log", JSON.stringify(l));
    timers.push(
      setInterval(() => {
        const live = insts.filter((i) => i.status === "online");
        if (!live.length) return;
        const i = pick(live);
        const svc = getService(i.service_id);
        const d = S.deployments.find((x) => x.id === i.current_deployment_id);
        const msg = pick(RUNTIME[svc.name] ?? RUNTIME.whoami);
        const l = { t: iso(now()), replica: d?.replicas.length ? pick(d.replicas).name : `${svc.name}-1`, stream: /error|warn/.test(msg) ? ("stderr" as const) : ("stdout" as const), msg, service_id: i.service_id };
        if (match(l.msg)) send("log", JSON.stringify(l));
      }, 900),
    );
    return cleanup;
  }

  if ((m = /^\/projects\/([^/]+)\/events\/stream$/.exec(path))) {
    const pid = m[1];
    offs.push(onEvent((e) => e.project_id === pid && send("event", JSON.stringify(e))));
    return cleanup;
  }

  // v1.5: inbox stream stub — mirrors the NOTIFS fan-out (same preference filter) as `notification` events.
  if (path === "/notifications/stream") {
    offs.push(
      onEvent((e) => {
        if (!globMatch(notifPrefs, e.kind)) return;
        send("notification", JSON.stringify({ event_id: e.id, project_id: e.project_id, environment_id: e.environment_id, service_id: e.service_id,
          kind: e.kind, message: e.message, created_at: e.created_at }));
      }),
    );
    return cleanup;
  }

  send("error", "no such stream");
  done();
  return cleanup;
}

seed();

/* ── v1.5: canvas groups, dashboards, hard usage limits, functions ──── */

const GROUPS: CanvasGroup[] = [];
const GROUP_COLOR_SET = new Set(["gray", "red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"]);

/** Groups of an environment with deleted services dropped (like the server's canvas response). */
function mockGroups(eid: string): CanvasGroup[] {
  const live = new Set(S.services.map((s) => s.id));
  return GROUPS.filter((g) => g.environment_id === eid).map((g) => ({ ...g, service_ids: g.service_ids.filter((id) => live.has(id)) }));
}

function applyGroupInput(g: CanvasGroup, b: Partial<CanvasGroup>, pid: string) {
  if (b.name !== undefined) g.name = String(b.name).trim();
  if (!g.name) throw new HttpError(400, "name is required");
  if ([...g.name].length > 60) throw new HttpError(400, "name is too long (max 60 chars)");
  if (b.color !== undefined) g.color = String(b.color).trim() || "gray";
  if (!GROUP_COLOR_SET.has(g.color) && !/^#[0-9a-fA-F]{6}$/.test(g.color)) throw new HttpError(400, "color must be one of gray, red, orange, yellow, green, teal, blue, purple, pink or #rrggbb");
  if (b.rect !== undefined) g.rect = b.rect;
  if (b.service_ids !== undefined) {
    const own = new Set(S.services.filter((s) => s.project_id === pid).map((s) => s.id));
    const ids = [...new Set(b.service_ids)];
    if (ids.some((id) => !own.has(id))) throw new HttpError(400, "service_ids must be services of this project");
    g.service_ids = ids;
    // 서비스는 환경 안에서 그룹 하나에만 속한다.
    for (const o of GROUPS) if (o.environment_id === g.environment_id && o.id !== g.id) o.service_ids = o.service_ids.filter((id) => !ids.includes(id));
  }
}

route("GET", "/environments/{eid}/groups", ([eid]) => (getEnv(eid), mockGroups(eid)));
route("POST", "/environments/{eid}/groups", ([eid], r) => {
  const env = getEnv(eid);
  if (GROUPS.filter((g) => g.environment_id === eid).length >= 50) throw new HttpError(400, "too many groups in this environment (max 50)");
  const g: CanvasGroup = { id: rid("grp"), environment_id: eid, name: "", color: "gray", service_ids: [], rect: null, created_at: iso(now()), updated_at: iso(now()) };
  applyGroupInput(g, (r.body ?? {}) as Partial<CanvasGroup>, env.project_id);
  GROUPS.push(g);
  return [201, g];
});
function getGroup(eid: string, gid: string) {
  const g = GROUPS.find((x) => x.id === gid && x.environment_id === eid);
  if (!g) throw new HttpError(404, "not found");
  return g;
}
route("PATCH", "/environments/{eid}/groups/{gid}", ([eid, gid], r) => {
  const env = getEnv(eid);
  const g = getGroup(eid, gid);
  const draft = { ...g };
  applyGroupInput(draft, (r.body ?? {}) as Partial<CanvasGroup>, env.project_id);
  Object.assign(g, draft, { updated_at: iso(now()) });
  return g;
});
route("DELETE", "/environments/{eid}/groups/{gid}", ([eid, gid]) => {
  const g = getGroup(eid, gid);
  GROUPS.splice(GROUPS.indexOf(g), 1);
  return 204;
});

/* dashboards */

const DASHBOARDS: Dashboard[] = [];
const W_TYPES = ["metric", "logs", "http", "events"];
const W_METRICS = ["cpu_percent", "memory_bytes", "net_rx_bytes", "net_tx_bytes", "disk_bytes"];

function normWidgets(pid: string, ws: Partial<Widget>[]): Widget[] {
  if (ws.length > 30) throw new HttpError(400, "too many widgets (max 30)");
  const own = new Set(S.services.filter((s) => s.project_id === pid).map((s) => s.id));
  const envs = new Set(S.envs.filter((e) => e.project_id === pid).map((e) => e.id));
  const seen = new Set<string>();
  return ws.map((w) => {
    const out: Widget = {
      id: w.id && !seen.has(w.id) ? w.id : rid("wdg"),
      type: (w.type ?? "metric") as Widget["type"],
      title: (w.title ?? "").trim(),
      service_id: w.service_id || null,
      environment_id: w.environment_id || null,
      metric: w.type === "metric" ? (w.metric ?? "") : "",
      range: w.range ?? "6h",
      x: w.x ?? 0,
      y: w.y ?? 0,
      w: w.w || 6,
      h: w.h || 3,
    };
    seen.add(out.id);
    if (!W_TYPES.includes(out.type)) throw new HttpError(400, "widget type must be one of metric, logs, http, events");
    if (out.type === "metric" && !W_METRICS.includes(out.metric ?? "")) throw new HttpError(400, `metric widget needs metric: one of ${W_METRICS.join(", ")}`);
    if (out.type !== "events" && !out.environment_id) throw new HttpError(400, `${out.type} widget needs environment_id`);
    if (out.type === "http" && !out.service_id) throw new HttpError(400, "http widget needs service_id and environment_id");
    if (out.service_id && !own.has(out.service_id)) throw new HttpError(400, "widget service_id must be a service of this project");
    if (out.environment_id && !envs.has(out.environment_id)) throw new HttpError(400, "widget environment_id must be an environment of this project");
    if (out.w < 1 || out.w > 12 || out.h < 1 || out.h > 8) throw new HttpError(400, "widget w must be 1-12 and h 1-8");
    if (out.x < 0 || out.x > 11 || out.y < 0) throw new HttpError(400, "widget x must be 0-11 and y non-negative");
    return out;
  });
}
function dashName(n: unknown) {
  const name = String(n ?? "").trim();
  if (!name) throw new HttpError(400, "name is required");
  if ([...name].length > 60) throw new HttpError(400, "name is too long (max 60 chars)");
  return name;
}
function getDashboard(pid: string, id: string) {
  const d = DASHBOARDS.find((x) => x.id === id && x.project_id === pid);
  if (!d) throw new HttpError(404, "not found");
  return d;
}
route("GET", "/projects/{pid}/dashboards", ([pid]) => (getProject(pid), DASHBOARDS.filter((d) => d.project_id === pid)));
route("POST", "/projects/{pid}/dashboards", ([pid], r) => {
  getProject(pid);
  if (DASHBOARDS.filter((d) => d.project_id === pid).length >= 50) throw new HttpError(400, "too many dashboards in this project (max 50)");
  const b = (r.body ?? {}) as { name?: string; widgets?: Partial<Widget>[] };
  const d: Dashboard = { id: rid("dsh"), project_id: pid, name: dashName(b.name), widgets: normWidgets(pid, b.widgets ?? []), created_at: iso(now()), updated_at: iso(now()) };
  DASHBOARDS.push(d);
  return [201, d];
});
route("GET", "/projects/{pid}/dashboards/{id}", ([pid, id]) => getDashboard(pid, id));
route("PATCH", "/projects/{pid}/dashboards/{id}", ([pid, id], r) => {
  const d = getDashboard(pid, id);
  const b = (r.body ?? {}) as { name?: string; widgets?: Partial<Widget>[] };
  const name = b.name !== undefined ? dashName(b.name) : d.name;
  const widgets = b.widgets !== undefined ? normWidgets(pid, b.widgets) : d.widgets;
  Object.assign(d, { name, widgets, updated_at: iso(now()) });
  return d;
});
route("DELETE", "/projects/{pid}/dashboards/{id}", ([pid, id]) => {
  const d = getDashboard(pid, id);
  DASHBOARDS.splice(DASHBOARDS.indexOf(d), 1);
  return 204;
});

/* hard usage limits (checked on every read; the real server checks every 5 minutes) */

const LIMITS: Record<string, { limit: UsageLimit | null; suspension: Omit<UsageSuspension, "active"> | null }> = {};
const monthKey = (d = new Date()) => d.toISOString().slice(0, 7);

function mockMonthToDate(pid: string): UsageTotals {
  const n = new Date();
  const t = zeroT();
  for (const row of mockUsage(pid, new Date(Date.UTC(n.getUTCFullYear(), n.getUTCMonth(), 1)), n)) addT(t, row.t);
  return t;
}
function mockExceeded(l: UsageLimit | null, t: UsageTotals) {
  const out: ("cpu_core_hours" | "memory_gb_hours" | "egress_gb")[] = [];
  if (!l) return out;
  if (l.cpu_core_hours && t.cpu_core_hours >= l.cpu_core_hours) out.push("cpu_core_hours");
  if (l.memory_gb_hours && t.memory_gb_hours >= l.memory_gb_hours) out.push("memory_gb_hours");
  if (l.egress_gb && t.net_egress_gb >= l.egress_gb) out.push("egress_gb");
  return out;
}
function projectInstances(pid: string) {
  const own = new Set(S.services.filter((s) => s.project_id === pid).map((s) => s.id));
  return S.instances.filter((i) => own.has(i.service_id));
}
/** Applies the limit: stop + suspend when over (action stop), lift when no longer over. */
function mockCheckLimit(pid: string) {
  const st = (LIMITS[pid] ??= { limit: null, suspension: null });
  const t = mockMonthToDate(pid);
  const ex = mockExceeded(st.limit, t);
  const stop = st.limit?.action === "stop" && ex.length > 0;
  const sp = st.suspension;
  const active = !!sp && !sp.lifted_at && sp.month === monthKey();
  if (active && !stop) {
    sp.lifted_at = iso(now());
    emit({ project_id: pid, environment_id: null, service_id: null, kind: "usage.unsuspended", message: `사용량 한도 정지가 풀렸습니다. 멈춘 서비스 ${sp.stopped_instances.length}개는 자동으로 다시 시작하지 않습니다(사용량 페이지에서 다시 시작)` });
  }
  if (stop && !active) {
    const n = new Date();
    const next = { month: monthKey(), reason: `이번 달 사용량 한도에 도달했습니다: ${ex.join(", ")}`, suspended_at: iso(now()), until: new Date(Date.UTC(n.getUTCFullYear(), n.getUTCMonth() + 1, 1)).toISOString(), stopped_instances: [...(sp?.stopped_instances ?? [])], lifted_at: null };
    for (const i of projectInstances(pid)) {
      if (i.status === "offline" || i.status === "idle") continue;
      i.status = "offline";
      if (!next.stopped_instances.includes(i.id)) next.stopped_instances.push(i.id);
    }
    st.suspension = next;
    emit({ project_id: pid, environment_id: null, service_id: null, kind: "usage.limit_reached", message: `이번 달 사용량이 한도에 도달했습니다. 프로젝트를 정지하고 서비스 ${next.stopped_instances.length}개를 멈췄습니다` });
  }
  return { t, ex };
}
function mockProjectView<T extends Project>(p: T): T {
  const st = LIMITS[p.id];
  const sp = st?.suspension;
  return { ...p, usage_limit: st?.limit ?? null, usage_suspension: sp ? { ...sp, active: !sp.lifted_at && sp.month === monthKey() } : null };
}
function mockSetUsageLimit(pid: string, raw: unknown) {
  let l = raw as UsageLimit | null;
  if (l) {
    for (const v of [l.cpu_core_hours, l.memory_gb_hours, l.egress_gb]) if (v !== undefined && v !== null && !(v > 0 && v <= 1e9)) throw new HttpError(400, "usage_limit values must be positive numbers");
    const action = l.action || "stop";
    if (action !== "stop" && action !== "notify") throw new HttpError(400, 'usage_limit.action must be "stop" or "notify"');
    l = { ...l, action };
    if (l.cpu_core_hours == null && l.memory_gb_hours == null && l.egress_gb == null) l = null;
  }
  (LIMITS[pid] ??= { limit: null, suspension: null }).limit = l;
  mockCheckLimit(pid);
}
/** Deploy guard (409 while suspended) + function code snapshot of the new deployment. */
function mockCheckSuspended(inst: ServiceInstance): ServiceInstance {
  const svc = getService(inst.service_id);
  const sp = LIMITS[svc.project_id]?.suspension;
  if (sp && !sp.lifted_at && sp.month === monthKey())
    throw new HttpError(409, `project suspended by usage limit: 이번 달 사용량 한도에 도달해 프로젝트가 정지되었습니다(${sp.reason}). 관리자가 한도를 올리거나 지우면 풀립니다`);
  if (svc.source_type === "function") {
    FN_DEPLOYED[inst.id] = FN_CODE[inst.id]?.sha256 ?? "";
    inst.pending_changes = inst.pending_changes.filter((k) => k !== "function.code");
  }
  return inst;
}
route("GET", "/projects/{pid}/usage/limit", ([pid]) => {
  const p = getProject(pid);
  const { t, ex } = mockCheckLimit(pid);
  const v = mockProjectView(p);
  return { project_id: pid, month: monthKey(), usage_limit: v.usage_limit, usage_suspension: v.usage_suspension, month_to_date: t, exceeded: ex };
});
route("POST", "/projects/{pid}/usage/resume", ([pid]) => {
  getProject(pid);
  const st = LIMITS[pid];
  const sp = st?.suspension;
  if (!sp) throw new HttpError(409, "사용량 한도로 정지된 프로젝트가 아닙니다");
  if (st.limit?.action === "stop" && mockExceeded(st.limit, mockMonthToDate(pid)).length)
    throw new HttpError(409, "이번 달 사용량이 아직 한도를 넘었습니다. 한도를 올리거나 지운 뒤 다시 시도하세요");
  st.suspension = null;
  const restarted: string[] = [];
  for (const id of sp.stopped_instances) {
    const i = S.instances.find((x) => x.id === id);
    if (!i || i.status !== "offline") continue;
    startDeployment(i, "redeploy");
    restarted.push(id);
  }
  emit({ project_id: pid, environment_id: null, service_id: null, kind: "usage.resumed", message: `사용량 한도 정지를 해제하고 서비스 ${restarted.length}개를 다시 시작합니다` });
  return { restarted, failed: {} };
});

/* functions (single-file Bun services) */

const FN_DEFAULT = `// Bun으로 실행되는 function입니다. PORT 환경 변수에서 HTTP 요청을 받습니다.
const port = Number(process.env.PORT ?? 8080);

Bun.serve({
  port,
  fetch(req) {
    const url = new URL(req.url);
    return Response.json({ message: "Hello from aven function", path: url.pathname });
  },
});
`;
const FN_MAX = 1 << 20;
const FN_CODE: Record<string, { code: string; sha256: string; updated_at: string }> = {};
const FN_DEPLOYED: Record<string, string> = {};
const fnSha = (code: string) => (hash(code).toString(16).padStart(8, "0") + hash(`k${code}`).toString(16).padStart(8, "0")).repeat(4);

function mockCreateService(pid: string, input: Parameters<typeof createService>[1] & { code?: string }): Service {
  if (input.source_type !== "function") return createService(pid, input);
  const code = input.code ?? FN_DEFAULT;
  if (code.length > FN_MAX) throw new HttpError(413, "code must be at most 1048576 bytes (1 MiB)");
  const svc = createService(pid, { ...input, name: input.name ?? uniqueName(pid, "function") });
  svc.icon = "function";
  for (const i of S.instances.filter((x) => x.service_id === svc.id)) {
    FN_CODE[i.id] = { code, sha256: fnSha(code), updated_at: iso(now()) };
    i.port = 8080;
    if (i.environment_id === input.environment_id)
      setTimeout(() => {
        try {
          startDeployment(mockCheckSuspended(i), "manual");
        } catch {
          /* suspended: the user deploys after resuming */
        }
      }, 300);
  }
  return svc;
}
function fnView(i: ServiceInstance) {
  const fc = FN_CODE[i.id];
  return {
    code: fc?.code ?? "",
    runtime: "bun",
    sha256: fc?.sha256 ?? fnSha(""),
    updated_at: fc?.updated_at ?? null,
    deployed_sha256: FN_DEPLOYED[i.id] || null,
    max_bytes: FN_MAX,
  };
}
function fnInstance(sid: string, eid: string) {
  const svc = getService(sid);
  if (svc.source_type !== "function") throw new HttpError(400, "function 서비스가 아닙니다");
  return getInstance(sid, eid);
}
route("GET", "/services/{sid}/instances/{eid}/function", ([sid, eid]) => fnView(fnInstance(sid, eid)));
route("PUT", "/services/{sid}/instances/{eid}/function", ([sid, eid], r) => {
  const i = fnInstance(sid, eid);
  const b = (r.body ?? {}) as { code?: string; runtime?: string };
  if (typeof b.code !== "string") throw new HttpError(400, "code is required");
  if (b.runtime !== undefined && b.runtime !== "bun") throw new HttpError(400, 'runtime must be "bun"');
  if (b.code.length > FN_MAX) throw new HttpError(413, "code must be at most 1048576 bytes (1 MiB)");
  const sha = fnSha(b.code);
  if (FN_CODE[i.id]?.sha256 !== sha) {
    FN_CODE[i.id] = { code: b.code, sha256: sha, updated_at: iso(now()) };
    const svc = getService(sid);
    emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "service.updated", message: `${svc.name}: function 코드를 변경했습니다` });
  }
  const others = i.pending_changes.filter((k) => k !== "function.code");
  i.pending_changes = FN_DEPLOYED[i.id] !== undefined && FN_DEPLOYED[i.id] !== sha ? [...others, "function.code"].sort() : others;
  if (r.query.get("deploy") === "1" || r.query.get("deploy") === "true") startDeployment(mockCheckSuspended(i), "manual");
  return fnView(i);
});

// ---- project-level Infrastructure as Code (aven config) — simplified mock of internal/api/iac.go ----
const IAC_KEYS = ["repo_url", "branch", "root_dir", "image", "start_command", "pre_deploy_command", "healthcheck_path", "replicas", "port", "cron_schedule"] as const;
function iacExportMock(pid: string, eid: string, include: boolean) {
  const p = getProject(pid);
  const services = S.services
    .filter((s) => s.project_id === pid && !s.stack_id && S.instances.some((i) => i.service_id === s.id && i.environment_id === eid))
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((s) => {
      const inst = getInstance(s.id, eid) as unknown as Record<string, unknown> & ServiceInstance;
      const settings: Record<string, unknown> = {};
      for (const k of IAC_KEYS) if (inst[k] !== null && inst[k] !== undefined && !(k === "branch" && inst[k] === "main") && !(k === "root_dir" && inst[k] === "/") && !(k === "replicas" && inst[k] === 1) && !(k === "healthcheck_path" && inst[k] === "/")) settings[k] = clone(inst[k]);
      const custom = inst.domains.filter((d) => d.kind !== "generated");
      if (custom.length) settings.domains = clone(custom);
      const sealed = new Set(inst.sealed_variables ?? []);
      const variables = Object.fromEntries(
        Object.entries(inst.variables).map(([k, v]) => [k, !sealed.has(k) && (include || /^\$\{\{\s*[\w.-]+\s*\}\}$/.test(v)) ? v : { preserve: true }]),
      );
      return { name: s.name, source_type: s.source_type, ...(s.database_kind ? { database_kind: s.database_kind } : {}), settings, ...(Object.keys(variables).length ? { variables } : {}) };
    });
  return { version: 1, project_name: p.name, services };
}
type MockIACDef = { version: number; partial?: string; services: { name: string; settings?: Record<string, unknown>; variables?: Record<string, string | { preserve: true }> }[] };
function iacPlanMock(pid: string, eid: string, def: MockIACDef) {
  const env = getEnv(eid);
  const changes: { action: string; resource: string; field?: string; from?: unknown; to?: unknown; destructive?: boolean; detail?: string }[] = [];
  const errors: string[] = [];
  if (def?.version !== 1) errors.push("version must be 1");
  const declared = new Set((def?.services ?? []).map((s) => s.name));
  for (const d of def?.services ?? []) {
    const svc = S.services.find((s) => s.project_id === pid && s.name === d.name);
    if (!svc) {
      changes.push({ action: "create", resource: `service.${d.name}` });
      continue;
    }
    const inst = getInstance(svc.id, eid) as unknown as Record<string, unknown> & ServiceInstance;
    for (const [k, v] of Object.entries(d.settings ?? {})) {
      if (k === "domains") continue;
      if (JSON.stringify(inst[k] ?? null) !== JSON.stringify(v)) changes.push({ action: "change", resource: `service.${d.name}`, field: k, from: inst[k] ?? null, to: v });
    }
    if (d.variables) {
      for (const [k, v] of Object.entries(d.variables)) if (typeof v === "string" && inst.variables[k] !== v) changes.push({ action: "change", resource: `service.${d.name}`, field: `variables.${k}`, ...(k in inst.variables ? { from: "«hidden»" } : {}), to: "«hidden»" });
      for (const k of Object.keys(inst.variables)) if (!(k in d.variables)) changes.push({ action: "change", resource: `service.${d.name}`, field: `variables.${k}`, from: "«hidden»", destructive: true });
    }
  }
  if (!def?.partial)
    for (const s of S.services.filter((s) => s.project_id === pid && !s.stack_id && !declared.has(s.name)))
      changes.push({ action: "destroy", resource: `service.${s.name}`, destructive: true, detail: "deletes the service in every environment" });
  const kinds = new Map<string, string>();
  for (const c of changes) if (!kinds.has(c.resource) || c.action !== "change") kinds.set(c.resource, c.action);
  const count = (a: string) => [...kinds.values()].filter((k) => k === a).length;
  return {
    environment_id: eid, environment: env.name, ...(def?.partial ? { partial: def.partial } : {}), config_etag: "mock", plan_hash: `mock-${JSON.stringify(def).length}`,
    changes, claims: def?.partial ? [...declared].map((n) => `service.${n}`) : [], errors, warnings: [], destructive: changes.some((c) => c.destructive),
    summary: { create: count("create"), change: count("change"), destroy: count("destroy") },
  };
}
route("GET", "/projects/{pid}/environments/{eid}/config", ([pid, eid], r) => iacExportMock(pid, eid, r.query.get("include_variables") === "1"));
route("POST", "/projects/{pid}/environments/{eid}/config/plan", ([pid, eid], r) => iacPlanMock(pid, eid, (r.body as { definition: MockIACDef }).definition));
route("POST", "/projects/{pid}/environments/{eid}/config/apply", ([pid, eid], r) => {
  const b = r.body as { definition: MockIACDef; plan_hash: string; confirm_destructive?: boolean };
  const plan = iacPlanMock(pid, eid, b.definition);
  if (plan.errors.length) throw new HttpError(422, plan.errors.join("; "));
  if (b.plan_hash !== plan.plan_hash) throw new HttpError(409, "the configuration or the environment changed since the plan; run aven config plan again");
  if (plan.destructive && !b.confirm_destructive) throw new HttpError(409, "the plan contains destructive changes; pass confirm_destructive");
  const services: { service: string; action: string }[] = [];
  for (const d of b.definition.services) {
    const svc = S.services.find((s) => s.project_id === pid && s.name === d.name);
    if (!svc || !plan.changes.some((c) => c.resource === `service.${d.name}`)) continue;
    const inst = getInstance(svc.id, eid) as unknown as Record<string, unknown> & ServiceInstance;
    const keys: string[] = [];
    for (const [k, v] of Object.entries(d.settings ?? {})) if (k !== "domains" && JSON.stringify(inst[k] ?? null) !== JSON.stringify(v)) (inst[k] = clone(v)), keys.push(k);
    if (d.variables) {
      const next: Record<string, string> = {};
      for (const [k, v] of Object.entries(d.variables)) next[k] = typeof v === "string" ? v : inst.variables[k];
      for (const k of new Set([...Object.keys(next), ...Object.keys(inst.variables)])) if (next[k] !== inst.variables[k]) keys.push(`variables.${k}`);
      inst.variables = next;
    }
    if (inst.current_deployment_id) inst.pending_changes = [...new Set([...inst.pending_changes, ...keys])];
    services.push({ service: d.name, action: "updated" });
  }
  emit({ project_id: pid, environment_id: eid, service_id: null, kind: "environment.config_applied", message: `코드형 인프라 설정을 적용했습니다 (변경 ${plan.summary.change})` });
  return { plan, staged: true, services, claimed: plan.claims };
});
route("GET", "/projects/{pid}/environments/{eid}/config/partials", ([, eid]) => ({
  environment_id: eid, environment_name: getEnv(eid).name, config_etag: "mock", iac_partials: {}, whole_project_available: true,
}));

/* ── point-in-time recovery (GET/PUT .../pitr, base, restore, archive) — mock of internal/api/dbpitr.go ── */

type MockPitr = {
  config: { enabled: boolean; retention_days: number; destination_id: string; epoch: number; enabled_at?: string; last_base_at?: string; last_sync_at?: string; last_error?: string };
  window: { earliest: string | null; latest: string | null; bases: number; logs: number; bytes: number };
  running: string[];
  restore: {
    status: "running" | "done" | "failed";
    step?: string;
    error?: string;
    target_time: string;
    target_service_id: string;
    target_instance_id: string;
    in_place: boolean;
    started_at: string;
    finished_at: string | null;
  } | null;
};
const PITR: Record<string, MockPitr> = {};
const PITR_KINDS = new Set(["postgres", "mysql"]);

function pitrTarget(sid: string, eid: string) {
  const svc = getService(sid);
  const inst = getInstance(sid, eid);
  if (svc.source_type !== "database" || !svc.database_kind) throw new HttpError(400, "not a database service");
  let st = PITR[inst.id];
  if (!st) {
    // production postgres: archiving for six days already; everything else starts switched off.
    const on = svc.id === "svc_postgres" && eid === "env_prod";
    st = PITR[inst.id] = {
      config: on
        ? { enabled: true, retention_days: 7, destination_id: "", epoch: 1, enabled_at: ago(60 * 24 * 6 + 30), last_base_at: ago(60 * 3 + 12), last_sync_at: ago(1) }
        : { enabled: false, retention_days: 7, destination_id: "", epoch: 0 },
      window: on
        ? { earliest: ago(60 * 24 * 6), latest: ago(1), bases: 6, logs: 8412, bytes: 2.31e9 }
        : { earliest: null, latest: null, bases: 0, logs: 0, bytes: 0 },
      running: [],
      restore: null,
    };
  }
  return { svc, inst, st, kind: svc.database_kind };
}
function pitrView(sid: string, eid: string) {
  const { st, kind } = pitrTarget(sid, eid);
  const supported = PITR_KINDS.has(kind);
  if (st.config.enabled) {
    // The archiver ships a log segment every minute.
    st.config.last_sync_at = ago(0.4);
    st.window.latest = st.config.last_sync_at;
  }
  return { supported, ...(supported ? {} : { reason: "시점 복구는 PostgreSQL과 MySQL만 지원합니다" }), kind, ...clone(st) };
}
function pitrBaseRun(st: MockPitr) {
  st.running = [...new Set([...st.running, "base"])];
  setTimeout(() => {
    st.running = st.running.filter((r) => r !== "base");
    st.config.last_base_at = iso(now());
    st.window.bases += 1;
    st.window.logs += 3;
    st.window.bytes += 3.1e8;
    st.window.earliest ??= iso(now());
    st.window.latest = iso(now());
  }, 4000);
}
route("GET", "/services/{sid}/instances/{eid}/pitr", ([sid, eid]) => pitrView(sid, eid));
route("PUT", "/services/{sid}/instances/{eid}/pitr", ([sid, eid], r) => {
  const { svc, st, kind } = pitrTarget(sid, eid);
  const b = (r.body ?? {}) as { enabled?: boolean; retention_days?: number; destination_id?: string };
  const enabled = b.enabled ?? st.config.enabled;
  if (enabled && !PITR_KINDS.has(kind)) throw new HttpError(400, "point-in-time recovery is only supported for postgres and mysql");
  if (b.destination_id && !DESTS.some((d) => d.id === b.destination_id)) throw new HttpError(400, "unknown destination_id");
  if (b.retention_days !== undefined && !(Number.isInteger(b.retention_days) && b.retention_days >= 1 && b.retention_days <= 90))
    throw new HttpError(400, "retention_days must be between 1 and 90");
  const was = st.config.enabled;
  st.config = {
    ...st.config,
    enabled,
    retention_days: b.retention_days ?? st.config.retention_days,
    destination_id: b.destination_id ?? st.config.destination_id,
  };
  if (enabled && !was) {
    st.config.epoch += 1;
    st.config.enabled_at = iso(now());
    pitrBaseRun(st);
  }
  emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "service.updated", message: `${svc.name}: 시점 복구 ${enabled ? (was ? "설정 변경" : "켜기") : "끄기"}` });
  return { config: clone(st.config) };
});
route("POST", "/services/{sid}/instances/{eid}/pitr/base", ([sid, eid]) => {
  const { st } = pitrTarget(sid, eid);
  if (!st.config.enabled) throw new HttpError(400, "point-in-time recovery is not enabled");
  if (st.running.includes("base")) throw new HttpError(409, "a base backup is already running");
  pitrBaseRun(st);
  return [202, { status: "running" }];
});
route("POST", "/services/{sid}/instances/{eid}/pitr/restore", ([sid, eid], r) => {
  const { svc, inst, st } = pitrTarget(sid, eid);
  const b = (r.body ?? {}) as { target_time?: string; in_place?: boolean; new_service_name?: string };
  const t = Date.parse(b.target_time ?? "");
  if (isNaN(t)) throw new HttpError(400, "target_time must be RFC3339 (e.g. 2026-07-20T12:00:00Z)");
  if (!st.window.earliest || t < Date.parse(st.window.earliest) || t > now()) throw new HttpError(400, "target_time is outside the recoverable window");
  if (st.restore?.status === "running") throw new HttpError(409, "a restore is already running");
  let target = { sid: svc.id, iid: inst.id };
  if (!b.in_place) {
    const fork = createService(svc.project_id, { name: b.new_service_name || undefined, source_type: "database", database_kind: svc.database_kind ?? "postgres", environment_id: eid }, svc.template);
    target = { sid: fork.id, iid: getInstance(fork.id, eid).id };
  }
  const rs: NonNullable<MockPitr["restore"]> = {
    status: "running",
    step: "기준 백업 내려받는 중",
    target_time: iso(t),
    target_service_id: target.sid,
    target_instance_id: target.iid,
    in_place: !!b.in_place,
    started_at: iso(now()),
    finished_at: null,
  };
  st.restore = rs;
  st.running = [...new Set([...st.running, "restore"])];
  setTimeout(() => (rs.step = `${svc.database_kind === "mysql" ? "binlog" : "WAL"} 재생 중`), 2500);
  setTimeout(() => {
    rs.status = "done";
    rs.step = undefined;
    rs.finished_at = iso(now());
    st.running = st.running.filter((x) => x !== "restore");
    emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "backup.restored", message: `${svc.name}: ${new Date(t).toLocaleString("ko-KR")} 시점으로 복원했습니다` });
  }, 6000);
  return [202, clone(rs)];
});
route("DELETE", "/services/{sid}/instances/{eid}/pitr/archive", ([sid, eid]) => {
  const { st } = pitrTarget(sid, eid);
  if (st.config.enabled) throw new HttpError(409, "turn off point-in-time recovery before deleting the archive");
  const n = st.window.bases + st.window.logs;
  st.window = { earliest: null, latest: null, bases: 0, logs: 0, bytes: 0 };
  return { deleted: n };
});

/* ── backup destinations (owner settings + PITR destination picker) ── */

const DESTS: (BackupDestination & { secret_key?: string })[] = [
  {
    id: "bdst_r2",
    name: "r2-backups",
    type: "s3",
    endpoint: "https://account-id.r2.cloudflarestorage.com",
    region: "auto",
    bucket: "aven-backups",
    prefix: "demo/",
    access_key: "EXAMPLE_ACCESS_KEY",
    has_secret: true,
    created_at: ago(60 * 24 * 20),
  },
];
const destView = (d: (typeof DESTS)[number]) => {
  const { secret_key, ...rest } = d;
  void secret_key;
  return rest;
};
route("GET", "/backup-destinations", () => DESTS.map(destView));
route("POST", "/backup-destinations", (_, r) => {
  const b = (r.body ?? {}) as BackupDestinationInput;
  if (!b.name) throw new HttpError(400, "name is required");
  const d = { id: rid("bdst"), name: b.name, type: b.type ?? "s3", endpoint: b.endpoint, region: b.region, bucket: b.bucket, prefix: b.prefix, access_key: b.access_key, has_secret: !!b.secret_key, created_at: iso(now()) };
  DESTS.push(d);
  return [201, destView(d)];
});
route("PATCH", "/backup-destinations/{id}", ([id], r) => {
  const d = DESTS.find((x) => x.id === id);
  if (!d) throw new HttpError(404, "backup destination not found");
  const b = (r.body ?? {}) as BackupDestinationInput;
  Object.assign(d, Object.fromEntries(Object.entries(b).filter(([k, v]) => v !== undefined && k !== "secret_key")));
  if (b.secret_key) d.has_secret = true;
  return destView(d);
});
route("DELETE", "/backup-destinations/{id}", ([id]) => {
  const i = DESTS.findIndex((x) => x.id === id);
  if (i < 0) throw new HttpError(404, "backup destination not found");
  DESTS.splice(i, 1);
  return 204;
});
route("POST", "/backup-destinations/{id}/test", ([id]) => {
  if (!DESTS.some((x) => x.id === id)) throw new HttpError(404, "backup destination not found");
  return { ok: true };
});

/* ── PostgreSQL: PgBouncer pooling and major upgrades (lib/postgres.ts) ── */

const POOLS: Record<string, PostgresPooling> = {};
const UPGRADES: Record<string, PostgresUpgrade> = {};

function pgTarget(sid: string, eid: string) {
  const svc = getService(sid);
  if (svc.database_kind !== "postgres") throw new HttpError(400, "not a postgres service");
  return { svc, inst: getInstance(sid, eid) };
}
const pgMajor = (image: string | null) => Number(/postgres:(\d+)/.exec(image ?? "")?.[1] ?? 0) || 0;
const PG_TARGETS = [16, 17, 18];

function poolOf(sid: string, eid: string) {
  const { inst } = pgTarget(sid, eid);
  return (POOLS[inst.id] ??= { status: "off", enabled: false, pool_mode: "transaction", default_pool_size: 20, max_client_conn: 1000, max_prepared_statements: null, variables: [] });
}
route("GET", "/services/{sid}/instances/{eid}/postgres/pooling", ([sid, eid]) => clone(poolOf(sid, eid)));
route("PUT", "/services/{sid}/instances/{eid}/postgres/pooling", ([sid, eid], r) => {
  const { svc, inst } = pgTarget(sid, eid);
  const p = poolOf(sid, eid);
  const b = (r.body ?? {}) as PostgresPoolingInput;
  if (b.pool_mode && !["transaction", "session", "statement"].includes(b.pool_mode)) throw new HttpError(400, "pool_mode must be transaction, session or statement");
  if (b.enabled === false) throw new HttpError(400, "use DELETE to remove pooling");
  const first = p.status === "off";
  Object.assign(p, {
    enabled: true,
    pool_mode: b.pool_mode ?? p.pool_mode,
    default_pool_size: b.default_pool_size ?? p.default_pool_size,
    max_client_conn: b.max_client_conn ?? p.max_client_conn,
    max_prepared_statements: b.reset_max_prepared_statements ? null : (b.max_prepared_statements ?? p.max_prepared_statements),
    status: first ? "deploying" : "updating",
    pooler_service_name: `${svc.name}-pgbouncer`,
    error: undefined,
  });
  setTimeout(() => {
    p.status = "enabled";
    p.variables = ["DATABASE_URL", "DATABASE_UNPOOLED_URL", ...(inst.tcp_proxy ? ["DATABASE_PUBLIC_URL", "DATABASE_PUBLIC_UNPOOLED_URL"] : [])];
  }, 3500);
  emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "service.updated", message: `${svc.name}: PgBouncer ${first ? "추가" : "설정 변경"} (${p.pool_mode})` });
  return clone(p);
});
route("DELETE", "/services/{sid}/instances/{eid}/postgres/pooling", ([sid, eid]) => {
  const { inst } = pgTarget(sid, eid);
  delete POOLS[inst.id];
  return clone(poolOf(sid, eid));
});

function upgradeOf(sid: string, eid: string) {
  const { inst } = pgTarget(sid, eid);
  const u = (UPGRADES[inst.id] ??= { status: "none", can_revert: false, in_progress: false, available_targets: [] });
  u.current_major = pgMajor(inst.image);
  u.image = inst.image ?? undefined;
  u.available_targets = u.in_progress ? [] : PG_TARGETS.filter((t) => t > (u.current_major ?? 0));
  return u;
}
route("GET", "/services/{sid}/instances/{eid}/postgres/upgrade", ([sid, eid]) => clone(upgradeOf(sid, eid)));
route("GET", "/services/{sid}/instances/{eid}/postgres/upgrade/preflight", ([sid, eid], r) => {
  const u = upgradeOf(sid, eid);
  const to = Number(r.query.get("to")) || u.available_targets[u.available_targets.length - 1] || 0;
  const vol = S.volumes.find((v) => v.service_instance_id === getInstance(sid, eid).id);
  const checks: PostgresCheck[] = [
    { key: "image", label: "공식 postgres alpine 이미지", severity: "blocker", passed: true, message: "" },
    { key: "disk", label: "볼륨 여유 공간 (데이터 크기의 2배 이상)", severity: "blocker", passed: true, message: "" },
    { key: "extensions", label: "확장 호환성", severity: "warning", passed: true, message: "" },
    { key: "replication", label: "논리 복제 슬롯 없음", severity: "warning", passed: true, message: "" },
    { key: "downtime", label: "예상 중단 시간", severity: "info", passed: true, message: "데이터 크기로 보아 1~3분 정도 멈춥니다." },
  ];
  return { ready: to > (u.current_major ?? 0), current_major: u.current_major ?? 0, target_major: to, available_targets: u.available_targets, data_size_bytes: vol?.size_bytes ?? null, checks, checked_at: iso(now()) };
});
route("POST", "/services/{sid}/instances/{eid}/postgres/upgrade", ([sid, eid], r) => {
  const { svc, inst } = pgTarget(sid, eid);
  const u = upgradeOf(sid, eid);
  const to = Number((r.body as { to_major?: number } | null)?.to_major);
  if (u.in_progress) throw new HttpError(409, "an upgrade is already in progress");
  if (!u.available_targets.includes(to)) throw new HttpError(400, `cannot upgrade to PostgreSQL ${to}`);
  Object.assign(u, { status: "preparing", in_progress: true, from_major: u.current_major, target_major: to, from_image: inst.image, started_at: iso(now()), finished_at: undefined, error: undefined, can_revert: false });
  u.available_targets = [];
  setTimeout(() => (u.status = "upgrading"), 3000);
  setTimeout(() => {
    inst.image = `postgres:${to}-alpine`;
    Object.assign(u, { status: "succeeded", in_progress: false, finished_at: iso(now()), can_revert: true, revert_deadline: iso(now() + 7 * 24 * 3600_000) });
    emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "service.updated", message: `${svc.name}: PostgreSQL ${u.from_major} → ${to} 업그레이드 완료` });
  }, 7000);
  return clone(u);
});
route("POST", "/services/{sid}/instances/{eid}/postgres/upgrade/revert", ([sid, eid]) => {
  const { inst } = pgTarget(sid, eid);
  const u = upgradeOf(sid, eid);
  if (!u.can_revert) throw new HttpError(409, "nothing to revert");
  Object.assign(u, { status: "reverting", in_progress: true, can_revert: false });
  setTimeout(() => {
    inst.image = u.from_image ?? inst.image;
    Object.assign(u, { status: "reverted", in_progress: false, finished_at: iso(now()) });
  }, 4000);
  return clone(u);
});

/* ── sandbox project: a bucket and a function, so the Bucket and Code tabs have something to show ── */
{
  const files = createService("prj_sandbox", { name: "files", source_type: "database", database_kind: "bucket" });
  const fn = mockCreateService("prj_sandbox", { name: "hello-fn", source_type: "function" });
  [files, fn].forEach((s, k) => {
    // stable ids so the tabs can be opened by URL (?svc=svc_files / svc_hellofn)
    const id = `svc_${s.name.replace(/-/g, "")}`;
    for (const i of S.instances.filter((q) => q.service_id === s.id)) i.service_id = id;
    for (const e of S.events.filter((e) => e.service_id === s.id)) e.service_id = id;
    s.id = id;
    for (const i of S.instances.filter((q) => q.service_id === s.id)) {
      i.status = "online";
      i.canvas = { x: 420 * (k + 1), y: 0 };
      if (s === fn) FN_DEPLOYED[i.id] = FN_CODE[i.id]?.sha256 ?? "";
    }
    for (const e of S.events.filter((e) => e.service_id === s.id)) e.created_at = ago(60 * 24 * 2);
  });
}

/* ── database high availability: read-only status so the config tab renders (lib/ha.ts) ── */

const HA_COMPANIONS: Record<string, { key: string; name: string; description: string; role: "replica" | "internal"; label: string; versions: number[] }> = {
  postgres: { key: "postgres-ha", name: "PostgreSQL HA", description: "스트리밍 복제 레플리카와 aven 자동 장애 조치", role: "replica", label: "레플리카", versions: [16, 17, 18] },
  redis: { key: "redis-sentinel", name: "Redis Sentinel", description: "레플리카와 Sentinel 3개가 primary를 고릅니다", role: "replica", label: "레플리카", versions: [7, 8] },
  mongo: { key: "mongo-replicaset", name: "MongoDB replica set", description: "3개 노드 replica set", role: "replica", label: "멤버", versions: [7, 8] },
};
route("GET", "/services/{sid}/instances/{eid}/ha", ([sid, eid]) => {
  const svc = getService(sid);
  const inst = getInstance(sid, eid);
  const c = HA_COMPANIONS[svc.database_kind ?? ""];
  if (!c) return { cluster: null, eligible: false, problems: [`${svc.database_kind ?? svc.source_type}은(는) 고가용성 구성을 지원하지 않습니다`] };
  return {
    cluster: null,
    eligible: true,
    problems: [],
    companion: {
      key: c.key,
      name: c.name,
      description: c.description,
      selectors: [{ role: c.role, label: c.label, nodeLabel: c.label, options: [1, 2, 3], default: 2 }],
      supported_versions: c.versions,
      version: /:(\d+)/.exec(inst.image ?? "")?.[1],
    },
  };
});
route("GET", "/environments/{eid}/ha", ([eid]) => (getEnv(eid), []));

/* ── edge: CDN caching, WAF / Under Attack Mode, edge rules (lib/edge.ts) ── */

type MockEdge = { cdn: EdgeCDN; waf: EdgeWAF; rules: EdgeRuleset };
const EDGE: Record<string, MockEdge> = {};
function edgeOf(sid: string, eid: string) {
  const inst = getInstance(sid, eid);
  let e = EDGE[inst.id];
  if (!e) {
    const demo = sid === "svc_hello" && eid === "env_prod";
    e = EDGE[inst.id] = {
      cdn: { enabled: demo, html_caching: "auto", default_ttl_seconds: 3600, honor_swr: true, purge_on_deploy: "html", last_purged_html: demo ? ago(95) : null, last_purged_all: demo ? ago(60 * 24 * 3) : null },
      waf: { allow_ips: [], deny_ips: demo ? ["203.0.113.0/24"] : [], rate_limit: demo ? { requests: 600, window_seconds: 60 } : null, managed_rules: demo, under_attack: null },
      rules: {
        version: 1,
        rules: demo
          ? [
              { id: "r_admin", description: "관리 화면은 사무실 IP만", priority: 10, enabled: true, if: { and: [{ attr: "http.path", op: "matches", value: "^/admin" }, { not: { attr: "ip.src", op: "in", value: ["2001:db8:1::/64", "192.0.2.10"] } }] }, then: { action: "block", params: { status: 403 } } },
              { id: "r_static", description: "정적 파일 1일 캐시", priority: 20, enabled: true, if: { attr: "http.path", op: "matches", value: "^/static/" }, then: { action: "cache_override", params: { ttl_seconds: 86400 } } },
            ]
          : [],
      },
    };
  }
  return { inst, e };
}
function edgeView(sid: string, eid: string): EdgeSettings {
  const { inst, e } = edgeOf(sid, eid);
  const ua = e.waf.under_attack;
  if (ua?.until && Date.parse(ua.until) < now()) e.waf.under_attack = null;
  const domains = inst.domains.filter((d) => !d.redirect).map((d) => d.host);
  return { ...clone(e), available: domains.length > 0, domains, under_attack_active: !!e.waf.under_attack };
}
const edgeEmit = (sid: string, eid: string, msg: string) => {
  const svc = getService(sid);
  emit({ project_id: svc.project_id, environment_id: eid, service_id: sid, kind: "service.updated", message: `${svc.name}: ${msg}` });
};
route("GET", "/services/{sid}/instances/{eid}/edge", ([sid, eid]) => edgeView(sid, eid));
route("PATCH", "/services/{sid}/instances/{eid}/edge", ([sid, eid], r) => {
  const { e } = edgeOf(sid, eid);
  const b = (r.body ?? {}) as EdgePatch;
  if (b.cdn?.default_ttl_seconds !== undefined && ![1800, 3600, 7200, 14400, 43200, 86400].includes(b.cdn.default_ttl_seconds))
    throw new HttpError(400, "default_ttl_seconds must be one of 1800, 3600, 7200, 14400, 43200, 86400");
  const ipOk = (s: string) => /^[0-9a-fA-F:.]+(\/\d{1,3})?$/.test(s);
  for (const ip of [...(b.waf?.allow_ips ?? []), ...(b.waf?.deny_ips ?? [])]) if (!ipOk(ip)) throw new HttpError(400, `invalid IP or CIDR: ${ip}`);
  if (b.waf?.rate_limit && !(b.waf.rate_limit.requests > 0 && b.waf.rate_limit.window_seconds > 0)) throw new HttpError(400, "rate_limit needs positive requests and window_seconds");
  if (b.cdn) Object.assign(e.cdn, b.cdn);
  if (b.waf) Object.assign(e.waf, b.waf);
  edgeEmit(sid, eid, "엣지 설정 변경");
  return edgeView(sid, eid);
});
route("PUT", "/services/{sid}/instances/{eid}/edge/rules", ([sid, eid], r) => {
  const { e } = edgeOf(sid, eid);
  const b = (r.body ?? {}) as EdgeRuleset;
  if (b.version !== 1 || !Array.isArray(b.rules)) throw new HttpError(400, "ruleset must be {version: 1, rules: [...]}");
  if (b.rules.length > 100) throw new HttpError(400, "at most 100 rules");
  e.rules = { version: 1, rules: b.rules.map((x) => ({ ...x, id: x.id ?? rid("r") })) };
  edgeEmit(sid, eid, `엣지 규칙 ${e.rules.rules.length}개 저장`);
  return edgeView(sid, eid);
});
route("POST", "/services/{sid}/instances/{eid}/edge/cdn/purge", ([sid, eid], r) => {
  const { e } = edgeOf(sid, eid);
  const scope = (r.body as { scope?: string } | null)?.scope;
  if (scope !== "html" && scope !== "all") throw new HttpError(400, 'scope must be "html" or "all"');
  if (scope === "html") e.cdn.last_purged_html = iso(now());
  else e.cdn.last_purged_all = e.cdn.last_purged_html = iso(now());
  return edgeView(sid, eid);
});
route("POST", "/services/{sid}/instances/{eid}/edge/waf/under-attack", ([sid, eid], r) => {
  const { e } = edgeOf(sid, eid);
  const d = (r.body as { duration?: string } | null)?.duration ?? "";
  const hours: Record<string, number> = { "1h": 1, "3h": 3, "12h": 12, "24h": 24 };
  if (d && !hours[d]) throw new HttpError(400, 'duration must be "", "1h", "3h", "12h" or "24h"');
  e.waf.under_attack = { since: iso(now()), until: d ? iso(now() + hours[d] * 3600_000) : null };
  edgeEmit(sid, eid, "공격 방어 모드 켜기");
  return edgeView(sid, eid);
});
route("DELETE", "/services/{sid}/instances/{eid}/edge/waf/under-attack", ([sid, eid]) => {
  const { e } = edgeOf(sid, eid);
  e.waf.under_attack = null;
  edgeEmit(sid, eid, "공격 방어 모드 끄기");
  return edgeView(sid, eid);
});
