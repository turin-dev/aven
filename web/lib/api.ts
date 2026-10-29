// Typed client for the aven HTTP API v1 (docs/api-v1.md).
import { t } from "@/lib/i18n";

export type ID = string;

export type FeatureFlagType = "bool" | "string" | "number" | "json";
export type FeatureFlagRule = { id: string; when: string; value: unknown };
export type FeatureFlag = {
  id: ID;
  project_id: ID;
  name: string;
  type: FeatureFlagType;
  environment_id: ID;
  default_value: unknown;
  rules: FeatureFlagRule[];
  configured: boolean;
  updated_at: string;
};
export type FeatureFlagSnapshot = { project_id: ID; environment_id: ID; flags: FeatureFlag[] };

export type NotifyEvent =
  | "deploy.success"
  | "deploy.failed"
  | "deploy.crashed"
  | "volume.backup.failed"
  /** v1.5: volume size passed 90% / 100% of size_limit_mb. */
  | "volume.near_limit"
  | "volume.limit_exceeded";
export const NOTIFY_EVENTS: NotifyEvent[] = [
  "deploy.success",
  "deploy.failed",
  "deploy.crashed",
  "volume.backup.failed",
  "volume.near_limit",
  "volume.limit_exceeded",
];

export type Project = {
  id: ID;
  name: string;
  description: string;
  created_at: string;
  /** v1.6: the workspace this project belongs to (ws_default = shared "Team" workspace). */
  workspace_id: ID;
  /** v1.6: which user's GitHub connection the project uses for private clones / commit statuses (null = server AVEN_GITHUB_TOKEN). */
  github_user_id: ID | null;
  default_environment_id: ID;
  notify_webhook_url: string | null;
  notify_events: NotifyEvent[];
  /** v1.5: GitHub PR preview environments. */
  pr_environments?: PREnvironmentSettings;
  pr_from_forks?: boolean;
  pr_from_bots?: boolean;
  /** v1.5: monthly usage alert thresholds (null = off). */
  usage_alert?: UsageAlert | null;
  /** v1.5: monthly hard usage limit (null = off) and the suspension it caused (GET/PATCH project only). */
  usage_limit?: UsageLimit | null;
  usage_suspension?: UsageSuspension | null;
};

/** OAuth app registered in the instance ("Login with Aven"). Managed by instance admins. */
export type OAuthApp = {
  id: ID;
  client_id: string;
  created_by: ID;
  name: string;
  description: string;
  /** confidential = web app with client secret; public = native / SPA (PKCE only). */
  client_type: "confidential" | "public";
  redirect_uris: string[];
  created_at: string;
  updated_at: string;
};
export type OAuthClientSecret = { id: ID; app_id: ID; created_at: string; revoked_at: string | null };
/** An app the current user authorized (account → 승인된 앱). */
export type OAuthAuthorizedApp = {
  grant_id: ID;
  app_id: ID;
  client_id: string;
  name: string;
  description: string;
  scopes: string[];
  project_ids: ID[];
  created_at: string;
  updated_at: string;
  last_used_at: string | null;
};
export type OAuthConsentRequest = {
  app: { name: string; description: string; client_id: string; redirect_host: string };
  user: { email: string; name: string; role: InstanceRole };
  scopes: { name: string; description: string }[];
  projects: { id: ID; name: string; role: ProjectRole }[];
  previous_project_ids: ID[];
  expires_at: string;
};

/** v1.5: hard monthly usage limit. action "stop" stops running services and blocks deploys until resumed or next month. */
export type UsageLimit = { cpu_core_hours?: number; memory_gb_hours?: number; egress_gb?: number; action: "stop" | "notify" };
/** active = false: the suspension was lifted but services stopped by it have not been restarted yet. */
export type UsageSuspension = {
  month: string;
  reason: string;
  suspended_at: string;
  until: string;
  stopped_instances: ID[];
  lifted_at: string | null;
  active: boolean;
};

export type PREnvironmentSettings = {
  enabled: boolean;
  /** null = the project's default environment. */
  base_environment_id: ID | null;
  auto_delete: boolean;
  domain: boolean;
  focused: boolean;
};

export type EnvironmentKind = "standard" | "preview";

export type Environment = {
  id: ID;
  project_id: ID;
  name: string;
  created_at: string;
  /** v1.5: preview = created for a GitHub pull request (pr_* are null for standard environments). */
  kind?: EnvironmentKind;
  pr_number?: number | null;
  pr_url?: string | null;
  pr_branch?: string | null;
  /** Environment RBAC: visible to project members, but its resources require project admin. */
  restricted?: boolean;
};

export type AccessControlSettings = {
  access_groups_enabled: boolean;
  restrict_generated_domains: boolean;
  restrict_tcp_proxy: boolean;
  restrict_deployment_sources: boolean;
  approved_github_orgs: string[];
};

export type AccessGroup = {
  id: ID;
  name: string;
  role: ProjectRole;
  created_at: string;
  user_ids: ID[];
  project_ids: ID[];
};

export type AccessGroupInput = Pick<AccessGroup, "name" | "role" | "user_ids" | "project_ids">;

/** GET /access-controls/policies: guardrail summary every user can read (UI banners, disabled buttons). */
export type AccessPolicies = Omit<AccessControlSettings, "approved_github_orgs"> & { instance_admin: boolean; workspace_admin: boolean };

/** GET /projects/{pid}/access-groups: groups the project belongs to and who gets access through them. */
export type ProjectAccessGroups = {
  enabled: boolean;
  groups: { id: ID; name: string; role: ProjectRole; user_count: number }[];
  group_grants: { user_id: ID; group_id: ID; group_name: string; role: ProjectRole }[];
};

/** v1.6: which user's GitHub connection a project uses for private clones / commit statuses. */
export type ProjectGithubAccount = { user_id: ID; user_name: string; login?: string; connected: boolean };

export type ProjectDetail = Project & {
  environments: Environment[];
  services: Service[];
  /** v1.6: the GitHub account the project clones with (null = server AVEN_GITHUB_TOKEN). */
  github_account?: ProjectGithubAccount | null;
  /** The caller is a server owner without project membership, so deploys use someone else's GitHub connection (allowed, audited). */
  github_borrowed?: boolean;
  /** A server owner suspended this project (or its workspace): deploys, restarts and scaling are blocked. */
  owner_hold?: OwnerHold | null;
};
export type OwnerHoldScope = "project" | "workspace" | "service" | "deploy";
export type OwnerHold = { scope: OwnerHoldScope; target_id: ID; reason: string; created_by: string; created_at: string; stopped_instances?: ID[] };
export type PreviousOwnerAction = "keep" | "demote" | "remove";

/** v1.6: a workspace groups projects. `ws_default` is the shared "Team" workspace (default=true);
 *  every user also has a personal workspace (personal=true). `role` is the caller's role in it. */
export type Workspace = {
  id: ID;
  name: string;
  personal: boolean;
  owner_id: ID | null;
  created_at: string;
  default: boolean;
  role: WorkspaceRole;
};

/** v1.6: a member of a (non-default) workspace. */
export type WorkspaceMember = {
  disabled: boolean;
  workspace_id: ID;
  user_id: ID;
  role: WorkspaceRole;
  created_at: string;
  email: string;
  name: string;
};

export type IACVariable = string | { preserve: true };
export type IACService = {
  name: string;
  source_type?: SourceType | "bucket";
  database_kind?: DatabaseKind;
  settings?: Record<string, unknown>;
  variables?: Record<string, IACVariable>;
};
/** Project-level Infrastructure as Code definition (aven config import/plan/apply). */
export type IACDefinition = { version: 1; project_name?: string; partial?: string; services: IACService[] };
export type IACChange = {
  action: "create" | "change" | "destroy";
  resource: string;
  field?: string;
  from?: unknown;
  to?: unknown;
  destructive?: boolean;
  detail?: string;
};
export type IACPlan = {
  environment_id: ID;
  environment: string;
  partial?: string;
  config_etag: string;
  plan_hash: string;
  changes: IACChange[];
  claims: string[];
  errors: string[];
  warnings: string[];
  destructive: boolean;
  summary: { create: number; change: number; destroy: number };
};
export type IACApplyResult = {
  plan: IACPlan;
  staged: boolean;
  services: { service: string; action: "created" | "updated" | "deleted"; deployment_id?: ID; deploy_skipped?: string }[];
  claimed: string[];
};
export type IACOwnership = {
  environment_id: ID;
  environment_name: string;
  config_etag: string;
  iac_partials: Record<string, string>;
  whole_project_available: boolean;
};
export type IACOwnershipChange = IACOwnership & { affected_resources: string[]; dry_run: boolean; base_config_etag: string };

/** v1.5: "function" = single-file Bun service whose code is edited in the dashboard. */
export type SourceType = "repo" | "image" | "database" | "function";
export type DatabaseKind = "postgres" | "mysql" | "redis" | "mongo" | "bucket";

export type Service = {
  id: ID;
  project_id: ID;
  name: string;
  icon: string | null;
  source_type: SourceType;
  database_kind: DatabaseKind | null;
  template: string | null;
  /** compose stack this service was imported from (null otherwise). */
  stack_id?: ID | null;
  compose_name?: string | null;
  created_at: string;
};

export type RestartPolicy = "always" | "on_failure" | "never";
export type HealthcheckType = "http" | "tcp" | "none";
export type Builder = "auto" | "dockerfile" | "railpack";
export type ImageUpdatePolicy = "disabled" | "patch" | "minor";
export type ImageUpdateWindow = "anytime" | "weekends" | "night" | "custom";

/** v1.6 image auto updates: one detected version and what happened to it. */
export type ImageUpdate = {
  id: ID;
  instance_id: ID;
  current_image: string;
  target_image: string;
  digest: string;
  digest_image: string;
  status: "available" | "deploying" | "applied" | "failed" | "skipped" | "superseded";
  automatic: boolean;
  detected_at: string;
  started_at: string | null;
  finished_at: string | null;
  deployment_id?: ID;
  error: string | null;
};

export type InstanceStatus = "idle" | "deploying" | "online" | "crashed" | "failed" | "offline" | "sleeping" | "scheduled";

/**
 * A DNS record the user should create. P1: `status` compares the record with what resolves now
 * (`current` = the value seen, absent when nothing resolves).
 */
export type DnsRecord = {
  type: "AAAA" | "A" | "CNAME" | "TXT";
  name: string;
  value: string;
  /** proxied: behind Cloudflare/tunnel, so the value differs but the domain works. */
  status?: "ok" | "proxied" | "pending" | "mismatch";
  /** Values seen at the last check (older servers sent a single string). */
  current?: string[] | string | null;
  /** An alternative to the recommended (CNAME) record. */
  alternative?: boolean;
  note?: string;
};
export type DomainStatus = "active" | "pending_dns" | "proxied" | "error";
/** P1: ownership verification. `txt` is always present so the UI can show the `_aven-verify` record. */
export type DomainVerification = {
  method: "legacy" | "txt" | "http" | "dns" | "";
  verified_at?: string | null;
  txt: { name: string; value: string };
};

export type PathRule = { path: string; service?: string; target_port?: number | null; strip_prefix?: boolean };
export type DomainTLS = "" | "auto" | "internal" | "off";
export type Domain = {
  host: string;
  kind: "generated" | "custom";
  target_port: number | null;
  dns: DnsRecord[];
  /** P1: `proxied` = resolves to Cloudflare / a tunnel and the check route answered (treated as working). */
  status: DomainStatus;
  /** P1 fields (absent from older servers). */
  verification?: DomainVerification;
  proxy?: "cloudflare" | "tunnel" | null;
  /** Port the proxy actually sends traffic to (target_port → service port → PORT → 8080). */
  effective_port?: number;
  /** §7: this domain's own public IPv6 (from the serving node's pool). Absent when the node has no pool. */
  ipv6?: string;
  cert?: { status: "valid" | "pending" | "error" | "none" | string; not_after?: string | null } | null;
  /** v1.3: path rules (most specific path wins), per-domain TLS mode, redirect, basic auth. The password is never returned. */
  paths?: PathRule[];
  tls?: DomainTLS;
  trust_forwarded_proto?: boolean;
  redirect?: { to: string; permanent: boolean } | null;
  basic_auth?: { username: string; has_password: boolean } | null;
};
export type DomainInput = {
  /** P3: rename a generated domain (same suffix). */
  host?: string;
  target_port?: number | null;
  paths?: PathRule[] | null;
  tls?: DomainTLS;
  trust_forwarded_proto?: boolean;
  redirect?: { to: string; permanent: boolean } | null;
  basic_auth?: { username: string; password?: string } | null;
};

export type ServiceInstance = {
  id: ID;
  service_id: ID;
  environment_id: ID;
  repo_url: string | null;
  branch: string;
  root_dir: string;
  image: string | null;
  image_update_policy?: ImageUpdatePolicy;
  image_update_window?: ImageUpdateWindow;
  image_update_days?: number[];
  image_update_start_hour?: number;
  image_update_end_hour?: number;
  builder: Builder;
  dockerfile_path: string;
  watch_paths: string[];
  auto_deploy: boolean;
  start_command: string | null;
  pre_deploy_command: string | null;
  port: number | null;
  healthcheck_type: HealthcheckType;
  healthcheck_path: string;
  healthcheck_timeout_s: number;
  restart_policy: RestartPolicy;
  restart_max_retries: number;
  replicas: number;
  cron_schedule: string | null;
  sleep_when_idle: boolean;
  sleep_after_s: number;
  /** v1.3: resource limits (null/absent = unlimited) and variable names passed to builds as BuildKit secrets. */
  memory_limit_mb?: number | null;
  cpu_limit?: number | null;
  build_secrets?: string[];
  /** v1.6: reuse a retained image when source and build inputs match. */
  skip_builds?: boolean;
  /** "local": build on the aven-server node and send the image to the deployment node; absent/"": build on the deployment node. */
  build_node?: "" | "local";
  /** v1.5: hold GitHub-triggered deployments as "waiting" until the commit's CI succeeds. */
  wait_for_ci?: boolean;
  /** v1.5: seconds old containers keep running after the new deployment is live (0..600). */
  overlap_seconds?: number;
  /** v1.5: SIGTERM → SIGKILL grace when stopping old containers (0..300). null = default (10 s, 30 s for volume services). */
  draining_seconds?: number | null;
  /** v1.5: ports exported to other nodes over the mesh (absent/empty = [PORT]). Applies immediately. */
  private_ports?: number[];
  /** v1.2: node the instance runs on (changes apply on the next deploy). */
  node_id: ID;
  domains: Domain[];
  private_domain: string;
  tcp_proxy: { container_port: number; public_port: number } | null;
  /** P5: custom private DNS name (`<name>.<env>.aven.internal`, staged until the next deploy). Empty = service name. */
  private_dns_name?: string | null;
  /** P8: outbound IPv6 (applies immediately, never a pending change). null/absent = node default (on). */
  outbound_ipv6?: boolean | null;
  /** A server admin stopped this service (scope "service") or blocked its deploys (scope "deploy"). */
  admin_hold?: OwnerHold | null;
  variables: Record<string, string>;
  status: InstanceStatus;
  current_deployment_id: ID | null;
  canvas: { x: number; y: number } | null;
  /** Keys that differ from the current deployment: "variables.<KEY>" or a setting name (v1.1). */
  pending_changes: string[];
  updated_at: string;
  /** v1.5: horizontal autoscaling (null = off). Applies immediately, never a pending change. */
  autoscale?: Autoscale | null;
  /** v1.5: replicas spread over several nodes (first entry = primary node = node_id). Pending until the next deploy. */
  placement?: Placement[] | null;
  /** v1.5: replicas running now across all nodes (autoscaling/scale changes it; `replicas` stays the baseline). */
  current_replicas?: number;
  /** v1.5: present only while autoscaling is enabled. */
  autoscale_status?: AutoscaleStatus | null;
  /** v1.5: sealed variable names; their `variables` value is always SEALED_VALUE. */
  sealed_variables?: string[];
  /** Present only when this service is aven-server itself (Aven on Aven, docs/selfhost.md). */
  self?: SelfServerInfo;
};
/** How the running aven-server is run: container = swapped by deployments; systemd = the next deployment moves it into a container. */
export type SelfServerInfo = {
  mode: "container" | "systemd" | "process"; unit?: string; image?: string; version: string;
  /** Result of the last update check (new commits on the repository branch). */
  update_available?: boolean; update_behind?: number;
};
export type SelfUpdateCommit = { sha: string; message: string; author: string; at: string };
/** Last automatic update check of the aven-server repository (every 30 minutes by default). */
export type SelfUpdate = {
  checked_at: string; repo: string; branch: string; latest?: SelfUpdateCommit; current_sha?: string;
  /** Commits ahead of the running deployment (0 when unknown). */
  behind: number; commits?: SelfUpdateCommit[]; error?: string; available: boolean; interval: string;
};
/** One AVEN_* setting of aven-server itself (GET /api/owner/server-config). Secret values are never sent. */
export type ServerConfigEntry = {
  name: string;
  group: "general" | "network" | "github" | "smtp" | "advanced" | "fixed" | "other";
  secret: boolean;
  /** Cannot be changed from the dashboard (data paths, values the container overrides). */
  fixed: boolean;
  set: boolean;
  value?: string;
  /** The file differs from the running server; applied by the next aven-server deployment. */
  pending: boolean;
};
export type ServerConfig = { file: string; writable: boolean; pending: number; entries: ServerConfigEntry[]; server: SelfServerInfo };
/** GET /api/owner/self-service */
export type SelfServiceStatus = {
  enabled: boolean;
  /** Set by AVEN_SELF_SERVICE: the dashboard cannot change it. */
  from_env: boolean;
  service_id?: ID; service_name?: string; project_id?: ID; workspace_id?: ID; environment_id?: ID; repo_url?: string; branch?: string;
  server: SelfServerInfo;
  update?: SelfUpdate;
};

/** v1.5: placeholder the API returns instead of a sealed value. Sending it back keeps the stored value. */
export const SEALED_VALUE = "<sealed>";

/** v1.5: GET/PUT .../variables/raw. */
export type RawVariables = { format: "env" | "json"; content: string; sealed_variables: string[] };

export type Placement = { node_id: ID; replicas: number };

export type Autoscale = {
  enabled: boolean;
  min_replicas: number;
  max_replicas: number;
  target_cpu_percent: number | null;
  target_memory_percent: number | null;
  target_rps_per_replica: number | null;
  scale_up_cooldown_s: number;
  scale_down_cooldown_s: number;
};

export type AutoscaleStatus = {
  desired: number | null;
  last_scale_at: string | null;
  last_reason: string;
  reason: string;
  evaluated_at: string | null;
  metrics: { cpu: number | null; memory: number | null; rps: number | null };
};

export type ScaleResult = { from: number; to: number; nodes: Placement[]; instance: ServiceInstance };

/** Fields a client may PATCH on an instance. */
export type InstancePatch = Partial<
  Pick<
    ServiceInstance,
    | "repo_url"
    | "branch"
    | "root_dir"
    | "image"
    | "image_update_policy"
    | "image_update_window"
    | "image_update_days"
    | "image_update_start_hour"
    | "image_update_end_hour"
    | "builder"
    | "dockerfile_path"
    | "watch_paths"
    | "auto_deploy"
    | "start_command"
    | "pre_deploy_command"
    | "port"
    | "healthcheck_type"
    | "healthcheck_path"
    | "healthcheck_timeout_s"
    | "restart_policy"
    | "restart_max_retries"
    | "replicas"
    | "cron_schedule"
    | "sleep_when_idle"
    | "sleep_after_s"
    | "memory_limit_mb"
    | "cpu_limit"
    | "build_secrets"
    | "skip_builds"
    | "build_node"
    | "wait_for_ci"
    | "overlap_seconds"
    | "draining_seconds"
    | "private_ports"
    | "private_dns_name"
    | "outbound_ipv6"
    | "node_id"
    | "variables"
    | "autoscale"
    | "placement"
  >
> & { variables_patch?: Record<string, string | null>; /** v1.5: names to seal (irreversible). */ sealed_variables?: string[] };

/* ── P4: Settings > Networking (GET …/networking) ───────────────────── */

/** Where a port was seen: listening socket in the container, image EXPOSE, PORT variable, or a socket bound to 127.0.0.1/::1 only. */
export type DetectedPortSource = "listening" | "expose" | "env" | "loopback";
export type DetectedPort = { port: number; source: DetectedPortSource };
/**
 * TCP proxy as the networking view reports it: always present. `enabled` = a proxy exists (then the ports are set);
 * `available` = this node can host one (false on a tunnel node without a public IP, with `reason`).
 */
export type TcpProxyView = {
  enabled: boolean;
  host: string;
  public_port: number | null;
  container_port: number | null;
  available: boolean;
  reason?: string;
};
/** Whether listening ports could be read (`proc`) or not (`unavailable`: old agent, node offline, nothing running…). */
export type PortDetection = { source: "proc" | "unavailable"; reason?: string; node?: string; containers: number };
export type PrivateNetworkView = {
  /** Endpoint label (custom private_dns_name or the service name). */
  dns_name: string;
  /** `<dns_name>.<env>.aven.internal` */
  fqdn: string;
  /** Every name that resolves to the service inside the environment (service name, fqdn, custom names). */
  aliases: string[];
  /** Container addresses on the environment network (one per replica). */
  ipv4: string[] | null;
  ipv6: string[] | null;
  /** Ports exported to other nodes over the mesh (private_ports or [PORT]). */
  mesh_ports: number[];
};
export type OutboundView = {
  ipv6_enabled: boolean;
  /** Whether containers in this env network on the node can reach IPv6 destinations (null = could not check, see egress_reason). */
  node_ipv6_egress: boolean | null;
  egress_reason?: string;
  /** Whether the node can block IPv6 egress per service (ip6tables DOCKER-USER). null = unknown (old agent, offline). */
  enforce_supported?: boolean | null;
  enforce_reason?: string;
  /** Node the status was read from. */
  node?: string;
  static_ips: { ipv4: string | null; ipv6: string | null };
};
export type InstanceNetworking = {
  detected_ports: DetectedPort[];
  port_detection?: PortDetection;
  effective_port: number;
  domains: Domain[];
  tcp_proxy: TcpProxyView;
  private: PrivateNetworkView;
  outbound: OutboundView;
};

export type Volume = {
  id: ID;
  project_id: ID;
  environment_id: ID;
  service_instance_id: ID | null;
  name: string;
  mount_path: string;
  size_bytes: number | null;
  /** v1.5: soft size limit in MiB (null = none). Exceeding it only emits events; writes are not blocked. */
  size_limit_mb?: number | null;
  created_at: string;
};

export type Backup = {
  id: ID;
  volume_id: ID;
  size_bytes: number;
  created_at: string;
  status: "running" | "done" | "failed";
  error: string | null;
};

export type DeploymentStatus =
  | "waiting"
  | "queued"
  | "initializing"
  | "building"
  | "deploying"
  | "healthchecking"
  | "pre_deploy"
  | "live"
  | "failed"
  | "crashed"
  | "cancelled"
  | "removed"
  | "skipped";

export type DeploymentTrigger = "manual" | "redeploy" | "rollback" | "github" | "variables" | "cli" | "template";

export type Replica = {
  name: string;
  status: "starting" | "running" | "exited" | "restarting";
  restarts: number;
  /** v1.5: node the replica runs on. */
  node_id?: ID;
};

export type Deployment = {
  id: ID;
  service_id: ID;
  environment_id: ID;
  service_instance_id: ID;
  status: DeploymentStatus;
  trigger: DeploymentTrigger;
  commit_sha: string | null;
  commit_message: string | null;
  commit_author: string | null;
  branch: string | null;
  image: string | null;
  builder_used: "dockerfile" | "railpack" | "image" | null;
  /** Deployment ID whose retained image this build reused. */
  build_reused_from?: ID | null;
  replicas: Replica[];
  node_id: ID;
  error: string | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
};

export type CronRun = {
  id: ID;
  deployment_id: ID;
  started_at: string;
  finished_at: string | null;
  exit_code: number | null;
  status: "running" | "succeeded" | "failed";
};

export type AvenEvent = {
  id: ID;
  project_id: ID;
  environment_id: ID | null;
  service_id: ID | null;
  kind: string;
  message: string;
  /** v1.2: name snapshot at event time (set for deleted services). */
  service_name?: string | null;
  created_at: string;
  /** v1.5: who caused the event (null for system events such as webhooks or schedulers). */
  actor?: string | null;
  /** v1.5: deployment of deploy.* events. */
  deployment_id?: ID | null;
};

/* ── v1.5: notifications, alert rules, usage ────────────────────────── */

export type ChannelKind = "webhook" | "slack" | "discord" | "email";
export type NotificationChannel = {
  id: ID;
  /** null = workspace-wide channel. */
  project_id: ID | null;
  kind: ChannelKind;
  name: string;
  /** "•••" for project viewers; null for email channels. */
  url: string | null;
  secret_set: boolean;
  recipients: string[];
  /** Exact kinds or globs ("deploy.*", "*"). */
  events: string[];
  enabled: boolean;
  created_at: string;
};
export type NotificationChannelInput = {
  kind?: ChannelKind;
  name?: string;
  url?: string;
  /** "" removes the signing key. */
  secret?: string;
  recipients?: string[];
  events?: string[];
  enabled?: boolean;
};
export type NotificationDelivery = {
  id: ID;
  channel_id: ID;
  event_id: ID;
  event_kind: string;
  status: "ok" | "failed";
  attempts: number;
  status_code: number | null;
  error: string;
  duration_ms: number;
  created_at: string;
};
export type ChannelTestResult = { ok: boolean; attempts: number; status_code: number | null; duration_ms: number; error: string | null };
export type UserNotification = {
  id: ID;
  event_id: ID;
  project_id: ID;
  environment_id: ID | null;
  service_id: ID | null;
  kind: string;
  message: string;
  created_at: string;
  read_at: string | null;
};
export type NotificationPrefs = { events: string[]; defaults: string[]; available: string[] };

export type AlertMetric =
  | "cpu_percent"
  | "memory_percent"
  | "memory_bytes"
  | "disk_bytes"
  | "restarts"
  | "http_5xx_rate"
  | "http_p95_ms"
  | "volume_percent";
export const ALERT_METRICS: { value: AlertMetric; label: string }[] = [
  { value: "cpu_percent", label: "CPU %" },
  { value: "memory_percent", label: t("메모리 % (상한 대비)") },
  { value: "memory_bytes", label: t("메모리 bytes") },
  { value: "disk_bytes", label: t("볼륨 bytes") },
  { value: "volume_percent", label: t("볼륨 % (상한 대비)") },
  { value: "restarts", label: t("재시작 횟수") },
  { value: "http_5xx_rate", label: "HTTP 5xx %" },
  { value: "http_p95_ms", label: "HTTP p95 ms" },
];
export type AlertState = "ok" | "pending" | "firing";
export type AlertRule = {
  id: ID;
  project_id: ID;
  environment_id: ID | null;
  service_id: ID | null;
  name: string;
  metric: AlertMetric;
  comparator: ">" | "<";
  threshold: number;
  for_seconds: number;
  enabled: boolean;
  last_state: AlertState;
  last_value: number | null;
  pending_since: string | null;
  last_fired_at: string | null;
  last_evaluated_at: string | null;
  created_at: string;
};
export type AlertRuleInput = {
  name?: string;
  environment_id?: ID | null;
  service_id?: ID | null;
  metric?: AlertMetric;
  comparator?: ">" | "<";
  threshold?: number;
  for_seconds?: number;
  enabled?: boolean;
};

export type UsageCost = { cpu: number; memory: number; egress: number; disk: number; total: number };
export type UsageTotals = {
  cpu_core_hours: number;
  memory_gb_hours: number;
  net_egress_gb: number;
  disk_gb_hours: number;
  estimated_cost?: UsageCost;
};
export type UsageGroup = UsageTotals & { key: string; name: string };
export type UsageSeriesPoint = UsageTotals & { t: string };
export type UsagePricing = { vcpu_hour: number; gb_ram_hour: number; gb_egress: number; gb_disk_month: number };
export type UsageAlert = { cpu_core_hours?: number; memory_gb_hours?: number; egress_gb?: number };
export type UsageGroupBy = "service" | "environment" | "day";
export type ProjectUsage = {
  project_id: ID;
  from: string;
  to: string;
  group: UsageGroupBy;
  totals: UsageTotals;
  groups: UsageGroup[];
  series: UsageSeriesPoint[];
  pricing: UsagePricing | null;
  usage_alert: UsageAlert | null;
  usage_limit?: UsageLimit | null;
  usage_suspension?: UsageSuspension | null;
};
export type WorkspaceUsage = {
  from: string;
  to: string;
  group: "project";
  totals: UsageTotals;
  projects: UsageGroup[];
  series: UsageSeriesPoint[];
  pricing: UsagePricing | null;
};

/* ── v1.5: users, roles and tokens ──────────────────────────────────── */

export type AdminWorkspace = Omit<Workspace, "role"> & {
  members: number; projects: number; services: number; instances: number; unhealthy_instances: number;
};
export type InstanceOverview = {
  users: { total: number; active: number; disabled: number; admins: number };
  pending_invites: number;
  workspaces: AdminWorkspace[];
  nodes: Pick<AvenNode, "id" | "name" | "status" | "is_local" | "schedulable">[];
  generated_at: string;
};

export type ResourceLimits = { cpu_cores: number | null; memory_mb: number | null; volume_mb: number | null };
export type WorkspaceResources = {
  workspace: Omit<Workspace, "role">;
  limits: ResourceLimits;
  allocated: {
    cpu_cores: number; memory_mb: number; volume_mb: number;
    unlimited_cpu: number; unlimited_memory: number; unlimited_volume: number;
    instances: number; sandboxes: number; volumes: number;
  };
};

export type InstanceRole = "owner" | "admin" | "member" | "viewer";
export type WorkspaceRole = "owner" | "admin" | "member" | "viewer";
export type ProjectRole = "admin" | "member" | "viewer";

export type Me = {
  /** v1 compatible display name ("admin" for the built-in account). */
  user: string;
  id: ID;
  email: string;
  name: string;
  /** Compatibility alias for instance_role. */
  role: InstanceRole;
  totp_enabled: boolean;
  /** Passkeys (WebAuthn) registered for this account. */
  passkey_count?: number;
  /** TOTP or at least one passkey. */
  mfa_enabled?: boolean;
  /** The instance requires 2FA for every account. */
  mfa_enforced?: boolean;
  /** Enforcement is on and this session has no second factor yet: everything except the 2FA setup endpoints returns 403 mfa_setup_required. */
  mfa_setup_required?: boolean;
  builtin: boolean;
  auth: "session" | "token" | "env_token" | "oauth";
  token: { id: ID; name: string; scope: TokenScope; workspace_id: ID | null; project_id: ID | null; environment_id: ID | null; role: ProjectRole } | null;
  instance_admin: boolean;
  instance_role: InstanceRole;
  /** Deprecated: administration of the default workspace only. */
  workspace_admin: boolean;
  can_create_projects: boolean;
  /** v1.6: workspaces the user can see (default first, then own personal, then others). */
  workspaces?: Workspace[];
  /** Effective role per visible project. */
  projects: Record<ID, ProjectRole>;
  /** Server owner (users.role=owner, AVEN_SUPERADMINS or the built-in account) using a session/env token. */
  instance_owner?: boolean;
  /** Admin panel features this account may use (all for owners; owners can withhold some from server admins). */
  admin_caps?: AdminCapID[];
  /** The server has a GitHub OAuth App (AVEN_GITHUB_CLIENT_ID/SECRET). */
  github_app_configured?: boolean;
  /** This user has connected their own GitHub account (connections are per user). */
  github_connected?: boolean;
  /** A server owner is viewing the dashboard as this user (read-only) until `until`. */
  view_as?: { by: string; by_id: ID; until: string } | null;
};

export type User = {
  id: ID;
  email: string;
  name: string;
  role: InstanceRole;
  totp_enabled: boolean;
  passkey_count?: number;
  builtin: boolean;
  disabled: boolean;
  created_at: string;
  last_login_at: string | null;
};

export type Passkey = { id: ID; rp_id: string; name: string; created_at: string; last_used_at: string | null };
export type WebAuthnOptions = { publicKey: Record<string, unknown>; mediation?: string };

export type Member = {
  project_id: ID;
  user_id: ID;
  role: ProjectRole;
  created_at: string;
  email: string;
  name: string;
  workspace_role: WorkspaceRole;
};

export type Invite = {
  workspace_id?: ID | null;
  workspace_role?: WorkspaceRole | null;
  id: ID;
  email: string;
  role: InstanceRole;
  project_id: ID | null;
  project_role: ProjectRole | null;
  created_by: ID;
  created_at: string;
  expires_at: string;
};

export type SessionInfo = {
  id: ID;
  user_id: ID;
  created_at: string;
  expires_at: string;
  last_seen: string;
  ip: string;
  user_agent: string;
  current: boolean;
};

export type TokenScope = "instance" | "workspace" | "project" | "environment";

export type ApiToken = {
  id: ID;
  user_id: ID;
  name: string;
  prefix: string;
  scope: TokenScope;
  workspace_id: ID | null;
  project_id: ID | null;
  environment_id: ID | null;
  role: ProjectRole;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
};

/* ── Server owner only (docs/permissions.md "서버 owner 전용 관리") ── */

export type AdminCapID = "users" | "workspaces" | "resources" | "nodes" | "security" | "sso" | "oauth_apps" | "notifications" | "audit" | "agent" | "backups" | "usage" | "service_controls";
export type MaintenanceState = { enabled: boolean; message: string; block_logins: boolean; pause_webhooks: boolean; started_at: string | null; started_by: string };
export type AnnouncementLevel = "info" | "warning" | "critical";
export type Announcement = { enabled: boolean; message: string; level: AnnouncementLevel; updated_at: string | null };
export type InstanceNotice = {
  announcement: Announcement | null;
  maintenance: { enabled: boolean; message: string; block_logins: boolean; started_at: string | null };
  /** The caller is a server owner and is not blocked by maintenance. */
  exempt: boolean;
};
export type OwnerAccount = {
  id: ID; email: string; name: string; builtin: boolean; disabled: boolean; totp_enabled: boolean; superadmin: boolean;
  /** role = users.role owner, builtin = AVEN_ADMIN_PASSWORD account, env = AVEN_SUPERADMINS only. */
  source: "role" | "builtin" | "env";
  last_login_at: string | null; created_at: string;
};
/** First-run checklist for server owners (GET /api/owner/setup). Copy lives in the UI, keyed by id. */
export type SetupItemID = "public_url" | "base_domain" | "owner_account" | "mfa" | "github" | "smtp" | "backup_destination" | "node" | "first_project" | "self_service";
export type SetupStatus = {
  items: { id: SetupItemID; done: boolean; optional: boolean }[];
  required: number;
  required_done: number;
  complete: boolean;
  /** An owner closed the first-login prompt; the checklist stays in the admin panel. */
  dismissed: boolean;
  dismissed_at: string | null;
  server: { public_url: string; base_domain: string; github_callback_url: string; github_webhook_url: string };
};
export type OwnerOverview = {
  owners: OwnerAccount[];
  superadmins: string[];
  maintenance: MaintenanceState;
  announcement: Announcement;
  admin_caps: { id: AdminCapID; label: string; allowed: boolean }[];
  counts: { sessions: number; api_tokens: number; users: number };
  server: {
    version: string; node: string; go_version: string; pid: number; started_at: string; uptime_seconds: number;
    systemd: boolean; data_dir: string; db_name?: string; db_bytes: number; public_url: string;
    admin_password_set: boolean; legacy_api_token_set: boolean; github_webhook_secret_set: boolean; github_oauth_configured: boolean;
    legacy_agents_disabled: boolean; secure_cookie: boolean; trusted_proxies: number; smtp_configured: boolean;
  };
};
export type OwnerSession = Omit<SessionInfo, "current"> & {
  current: boolean; saml_authenticated: boolean; email: string; name: string; user_role: InstanceRole; builtin: boolean; disabled: boolean;
};
export type OwnerToken = ApiToken & { email: string; user_name: string; user_role: InstanceRole | "" };
export type AvenBackup = { name: string; bytes: number; modified_at: string };

export type AuditEntry = { id: ID; actor_id: ID; actor: string; kind: string; target: string; message: string; ip: string; created_at: string };

/** Service panel "어드민" tab (server admins only): IDs, placement, live containers, holds and related audit entries. */
export type ServiceAdminContainer = {
  name: string;
  role: string;
  deployment_id: string;
  replica: string;
  status: string;
  running: boolean;
  exit_code: number;
  restart_count: number;
  started_at: string | null;
  ips: Record<string, string[]> | null;
  volumes: string[];
};
export type ServiceAdmin = {
  workspace: { id: ID; name: string };
  project: { id: ID; name: string };
  environment: { id: ID; name: string };
  service_id: ID;
  instance_id: ID;
  current_deployment_id: ID | null;
  node: AvenNode | null;
  containers: ServiceAdminContainer[];
  containers_error?: string;
  holds: OwnerHold[];
  audit: AuditEntry[];
  audit_hidden: boolean;
  /** This account may stop the service or block its deploys (server owners can withhold it from server admins). */
  can_control: boolean;
};

export type SAMLDomain = { domain: string; token: string; verified: boolean };
export type SAMLConfig = {
  metadata_url: string;
  domains: SAMLDomain[];
  default_role: InstanceRole;
  enforce_sso: boolean;
  configured: boolean;
  updated_at?: string;
};
export type SAMLIdPInfo = { entity_id: string; sso_url: string; cert_not_after?: string };
export type SAMLSettings = { config: SAMLConfig; sp_metadata_url: string; acs_url: string; login_url: string; idp: SAMLIdPInfo | null };

export type TemplateService = {
  name: string;
  source_type: SourceType;
  database_kind?: DatabaseKind;
  image?: string;
  repo_url?: string;
  icon?: string;
};

/** v1.5: deploy-time input of a template, referenced as ${{ inputs.KEY }}. `pattern` must match the whole value. */
export type TemplateInput = {
  key: string;
  label?: string;
  description?: string;
  default?: string;
  required?: boolean;
  secret?: boolean;
  pattern?: string;
};

export type TemplateVisibility = "private" | "workspace" | "public";

export type Template = {
  key: string;
  name: string;
  description: string;
  icon: string;
  services: TemplateService[];
  /** v1.5 marketplace fields. */
  category: string;
  tags: string[];
  inputs: TemplateInput[];
  builtin: boolean;
  /** Built-ins are "public". */
  visibility: TemplateVisibility;
  owner_user_id: ID | null;
  deploy_count: number;
  created_at: string | null;
  updated_at: string | null;
  can_edit: boolean;
};

/** Template definition JSON (`"version": 1`, see docs/api-v1.md "Templates"). Kept loose: the server validates it. */
export type TemplateDefinition = {
  version: number;
  key?: string;
  name?: string;
  description?: string;
  icon?: string;
  category?: string;
  tags?: string[];
  readme?: string;
  primary?: string;
  inputs?: TemplateInput[];
  services: Record<string, unknown>[];
};

export type TemplateDetail = Template & { readme: string; definition: TemplateDefinition };

export type TemplateExport = { definition: TemplateDefinition; warnings: string[] };

export type TemplateSaveInput = {
  workspace_id?: ID;
  definition: TemplateDefinition;
  key?: string;
  name?: string;
  description?: string;
  readme?: string;
  icon?: string;
  category?: string;
  tags?: string[];
  visibility?: TemplateVisibility;
};

/** 400 body of a template deploy with bad inputs (ApiError.data). */
export type TemplateInputProblems = { error: string; missing: string[]; invalid: string[]; unknown: string[] };

export type TemplateListQuery = { q?: string; category?: string; tag?: string; sort?: "popular" | "new" };

export type MetricPoint = {
  t: string;
  cpu_percent: number;
  memory_bytes: number;
  memory_limit_bytes: number;
  net_rx_bytes: number;
  net_tx_bytes: number;
  disk_bytes: number | null;
};

export type MetricRange = "1h" | "6h" | "24h" | "7d";
export const METRIC_RANGES: MetricRange[] = ["1h", "6h", "24h", "7d"];

export type ServiceMetrics = { points: MetricPoint[]; step_s: number; replicas: number };
export type EnvMetrics = { services: { service_id: ID; points: MetricPoint[] }[] };

export type CanvasService = Service & {
  instance: ServiceInstance;
  latest_deployment: Deployment | null;
  volumes: Volume[];
};

export type StackView = {
  id: ID;
  project_id: ID;
  name: string;
  created_at: string;
  service_ids: ID[];
  /** Deploy order as aven service names; services in one level deploy together. */
  levels: string[][];
  imported: boolean;
  deploying: boolean;
  applied_at: string | null;
};

/** v1.5: named canvas frame around services. rect is where an empty group is drawn (members define the frame otherwise). */
export type CanvasGroup = {
  id: ID;
  environment_id: ID;
  name: string;
  /** gray|red|orange|yellow|green|teal|blue|purple|pink or #rrggbb */
  color: string;
  service_ids: ID[];
  rect: { x: number; y: number; w: number; h: number } | null;
  created_at: string;
  updated_at: string;
};

export type CanvasData = { environment: Environment; project: Project; services: CanvasService[]; stacks?: StackView[]; groups?: CanvasGroup[] };

export type ComposeIssue = { severity: "error" | "warning" | "info"; service?: string; code: string; message: string };
export type ComposeServicePlan = {
  name: string;
  compose_name: string;
  source_type: "image" | "repo";
  icon: string;
  image?: string;
  build?: { context: string; dockerfile: string; target?: string };
  port: number | null;
  healthcheck_type: string;
  restart_policy: string;
  replicas: number;
  env_keys: string[];
  volumes: { name: string; mount_path: string }[];
  aliases: string[];
  depends_on: { service: string; condition: string }[];
  domain_candidates: { port: number; host?: string; source?: string }[];
  tcp_candidates: number[];
  issues: ComposeIssue[];
};
export type ComposePlan = {
  stack: string;
  services: ComposeServicePlan[];
  levels: string[][];
  warnings: ComposeIssue[];
  errors: ComposeIssue[];
  missing_variables: string[];
};
export type ComposeDiff = { name: string; compose_name: string; action: "create" | "update" | "unchanged" | "remove" | "keep"; changes: string[] };
export type ComposeRequest = {
  source: "yaml" | "repo";
  yaml?: string;
  repo_url?: string;
  branch?: string;
  path?: string;
  name?: string;
  env?: string;
  allow_bind_mounts?: boolean;
  node_id?: ID;
  stack_id?: ID;
  prune?: boolean;
  deploy?: boolean;
  domains?: Record<string, { host?: string; target_port?: number }[]>;
  tcp_proxy?: Record<string, number>;
};
export type ComposePreview = { plan: ComposePlan; exists: boolean; diff: ComposeDiff[]; node_id: ID; ok: boolean };

export type SystemStatus = {
  version: string;
  node: string;
  docker: string;
  proxy: string;
  base_domain: string | null;
  public_ipv4: string | null;
  public_ipv6: string | null;
  disk_free_bytes: number;
  cpu_cores: number;
  tcp_proxy_domain: string | null;
  /** v1.5 */
  smtp_configured?: boolean;
  pricing?: UsagePricing | null;
  /** v1.5: local node mesh settings (AVEN_MESH, AVEN_MESH_PORT, AVEN_PRIVATE_ADDRESS). */
  mesh?: { mode: "on" | "off" | "auto"; port: number; private_address: string | null; enabled: boolean };
};

export type ResolvedVar = {
  value: string;
  source: "user" | "shared" | "reference" | "injected";
  /** v1.2: the reference points at a service on another node. */
  cross_node?: boolean;
  /** v1.5: present with cross_node — true when both nodes are on the mesh (the private domain resolves). */
  mesh?: boolean;
  cross_node_services?: string[];
  /** v1.5: the value comes from a sealed variable; `value` is SEALED_VALUE. */
  sealed?: boolean;
};

/** v1.5: cross-node private networking (mesh gateway) status of a node. */
export type MeshPeerStatus = {
  node_id: ID;
  status: "ok" | "unreachable" | "pending";
  last_ok_at: string | null;
  rtt_ms: number | null;
  error?: string;
};
export type MeshStatus = {
  enabled: boolean;
  listen: string | null;
  peers: MeshPeerStatus[];
  error?: string;
  reason?: string;
};

export type AvenNode = {
  id: ID;
  name: string;
  is_local: boolean;
  status: "online" | "offline" | "draining";
  schedulable: boolean;
  address: { ipv4: string | null; ipv6: string | null; private: string | null };
  version: string;
  docker_version: string;
  cpu_cores: number;
  memory_bytes: number;
  disk_free_bytes: number;
  labels: Record<string, string>;
  /** 공개 진입 방식: direct(공인 주소) | cloudflare_tunnel(공인 주소 없이 Cloudflare 터널). 로컬 노드는 AVEN_INGRESS_MODE가 우선. */
  ingress_mode?: NodeIngressMode;
  /** 커스텀 도메인이 CNAME으로 가리킬 이름(비우면 A/AAAA 안내). 로컬 노드는 AVEN_INGRESS_HOSTNAME이 우선. */
  ingress_hostname?: string;
  /** TCP 프록시 접속 호스트(비우면 기존 방식). 로컬 노드는 AVEN_TCP_PROXY_DOMAIN이 우선. */
  tcp_hostname?: string;
  /** §7 도메인별 공개 IPv6 풀(CIDR, /64~/120). 비면 도메인이 노드 기본 IPv6를 같이 쓴다. 로컬 노드는 AVEN_IPV6_POOL이 우선. */
  ipv6_pool?: string;
  /** §7 (GET /nodes only) 풀의 주소를 노드에 붙인 상태. 풀이 없으면 null. */
  ipv6_status?: NodeIPv6Status | null;
  last_seen_at: string;
  created_at: string;
  /** v1.5 (GET /nodes only) */
  mesh?: MeshStatus;
};
export type NodeIngressMode = "direct" | "cloudflare_tunnel";
export type NodeIngressInput = { ingress_mode?: NodeIngressMode | ""; ingress_hostname?: string; tcp_hostname?: string; ipv6_pool?: string };
export type NodeIPv6Status = {
  pool: string;
  /** true: 반영함, false: 이 노드에서 쓸 수 없음(reason, 예: 옛 agent), null: 아직 확인 전이거나 오프라인. */
  supported: boolean | null;
  reason?: string;
  iface?: string;
  desired: number;
  present: number;
  /** 풀 안에 있지만 aven이 붙인 모양이 아니라 건드리지 않은 주소. */
  foreign?: string[];
  last_error?: string;
  checked_at?: string | null;
};
export type NodeMetricPoint = { t: string; cpu_percent: number; memory_bytes: number; disk_free_bytes: number };
export type JoinToken = { token: string; expires_at: string; command: string };
export type Registry = { host: string; username: string; has_password: boolean };
export type RegistryInput = { host: string; username: string; password: string | null };
export type BackupSchedule = { cron: string | null; keep: number };
export type DeployKey = { service_id: ID; public_key: string; fingerprint: string; created_at: string };
export type DbBackup = {
  id: ID;
  service_id: ID;
  environment_id: ID;
  instance_id: ID;
  kind: string;
  status: "running" | "done" | "failed";
  trigger: string;
  destination_id: string;
  file_name: string;
  size_bytes: number;
  error: string | null;
  created_at: string;
  finished_at: string | null;
};
export type DbBackupConfig = { enabled: boolean; cron: string; retention: number; destination_id: string };
export type DbRestoreStatus = { status: "running" | "done" | "failed"; error?: string; target_instance_id: ID };
export type PitrConfig = {
  enabled: boolean;
  retention_days: number;
  destination_id: string;
  epoch: number;
  enabled_at?: string;
  last_base_at?: string;
  last_sync_at?: string;
  last_error?: string;
};
export type PitrRestore = {
  status: "running" | "done" | "failed";
  step?: string;
  error?: string;
  target_time: string;
  target_service_id: ID;
  target_instance_id: ID;
  in_place: boolean;
  started_at: string;
  finished_at: string | null;
};
export type PitrStatus = {
  supported: boolean;
  reason?: string;
  kind: string;
  config: PitrConfig;
  window: { earliest: string | null; latest: string | null; bases: number; logs: number; bytes: number };
  running: string[];
  restore: PitrRestore | null;
};
export type BackupDestination = {
  id: ID;
  name: string;
  type: "local" | "s3";
  endpoint?: string;
  region?: string;
  bucket?: string;
  prefix?: string;
  access_key?: string;
  has_secret: boolean;
  created_at: string;
};
export type BackupDestinationInput = {
  name?: string;
  type?: "local" | "s3";
  endpoint?: string;
  region?: string;
  bucket?: string;
  prefix?: string;
  access_key?: string;
  /** Omit or leave empty to keep the stored secret. Never returned by the API. */
  secret_key?: string;
};

export type GithubStatus = { webhook_url: string; secret_configured: boolean; token_configured: boolean; connected?: boolean };

/** GitHub connection (OAuth App). The token is never returned. */
export type GithubConnection = {
  app_configured: boolean;
  connected: boolean;
  /** where clone/API tokens come from: the connection, AVEN_GITHUB_TOKEN, or nothing */
  source: "oauth" | "env" | "";
  login?: string;
  scopes?: string;
  connected_at?: string;
  /** set when GitHub issues expiring user tokens (8h); aven refreshes them before they expire. null for non-expiring tokens. */
  expires_at?: string | null;
  refresh_expires_at?: string | null;
  /** the token expired and can no longer be refreshed: the user has to reconnect */
  expired?: boolean;
  callback_url: string;
  webhook_url: string;
  secret_configured: boolean;
};
export type GithubRepo = { full_name: string; private: boolean; default_branch: string; url: string; pushed_at: string; admin: boolean };

/** SSE payloads */
export type DeployLogLine = { t: string; replica: string; stream: "stdout" | "stderr"; msg: string };
export type EnvLogLine = DeployLogLine & { service_id: ID };
export type HttpLogLine = {
  t: string;
  method: string;
  host: string;
  path: string;
  status: number;
  duration_ms: number;
  bytes: number;
  client_ip: string;
  user_agent: string;
  /** Set when the edge traced the request (tracing on for the service). */
  trace_id?: string;
};

/** One DNS lookup made by a service container (`GET .../dns-logs`). */
export type DNSLogLine = {
  id: ID;
  kind: "dns";
  t: string;
  environment_id: ID;
  service_id: ID;
  service: string;
  replica?: string;
  qname: string;
  qtype: string;
  zone: "internal" | "external";
  rcode: string;
  status: "ok" | "failed";
  answers: string[];
  cname_chain?: string[];
  resolver: string;
  src_addr: string;
  duration_ms: number;
};

/** One connection seen from `service_id`'s side (`GET .../network-logs`). */
export type NetworkFlowLogLine = {
  id: ID;
  kind: "network";
  t: string;
  capture_start: string;
  environment_id: ID;
  service_id: ID;
  service: string;
  replica?: string;
  flow_state: "complete" | "active";
  direction: "ingress" | "egress";
  protocol: "tcp" | "udp" | "unknown";
  src_addr: string;
  src_port: number;
  dst_addr: string;
  dst_port: number;
  peer_kind: "service" | "internet" | "edge_proxy" | "host" | "private" | "unknown";
  peer_service_id?: ID;
  peer: string;
  bytes_sent: number;
  bytes_received: number;
  packets: number;
  duration_ms: number;
  latency_ms?: number;
  status: "ok" | "refused" | "timeout" | "reset";
};

/** First SSE event of a DNS/network log stream: whether nodes can capture. */
export type NetworkCaptureStatus = { active_nodes: number; inactive_nodes: number; reason: string };

export type NetworkDiagnosticCheck = { ok: boolean; available: boolean; skipped?: boolean; exit_code: number; duration_ms: number; output: string };
export type NetworkDiagnosticResult = {
  checked_at: string;
  service_id: ID;
  replica: string;
  host: string;
  port: number;
  dns: NetworkDiagnosticCheck;
  tcp: NetworkDiagnosticCheck;
};

export type CreateServiceInput = {
  name?: string;
  source_type: SourceType;
  repo_url?: string;
  branch?: string;
  image?: string;
  database_kind?: DatabaseKind;
  environment_id?: ID;
  /** v1.5: initial code of a function service (default: a Bun HTTP hello world). */
  code?: string;
};

export const TERMINAL_DEPLOYMENT_STATUSES: DeploymentStatus[] = [
  "live",
  "failed",
  "crashed",
  "cancelled",
  "removed",
  "skipped",
];

export function isTerminal(s: DeploymentStatus): boolean {
  return TERMINAL_DEPLOYMENT_STATUSES.includes(s);
}

/** Variables aven injects into every instance (docs/api-v1.md, "Variable rules"). */
export const INJECTED_VARS = [
  "PORT",
  "AVEN_SERVICE_NAME",
  "AVEN_SERVICE_ID",
  "AVEN_ENVIRONMENT_NAME",
  "AVEN_ENVIRONMENT_ID",
  "AVEN_PROJECT_NAME",
  "AVEN_PROJECT_ID",
  "AVEN_PRIVATE_DOMAIN",
  "AVEN_PUBLIC_DOMAIN",
  "AVEN_DEPLOYMENT_ID",
  "AVEN_GIT_COMMIT_SHA",
  "AVEN_GIT_BRANCH",
  "AVEN_TCP_PROXY_DOMAIN",
  "AVEN_TCP_PROXY_PORT",
  "AVEN_VOLUME_MOUNT_PATH",
  // Copied from AVEN_* so existing ${{KEEL_*}} references keep resolving.
  "KEEL_SERVICE_NAME",
  "KEEL_SERVICE_ID",
  "KEEL_ENVIRONMENT_NAME",
  "KEEL_ENVIRONMENT_ID",
  "KEEL_PROJECT_NAME",
  "KEEL_PROJECT_ID",
  "KEEL_PRIVATE_DOMAIN",
  "KEEL_PUBLIC_DOMAIN",
  "KEEL_DEPLOYMENT_ID",
  "KEEL_GIT_COMMIT_SHA",
  "KEEL_GIT_BRANCH",
  "KEEL_TCP_PROXY_DOMAIN",
  "KEEL_TCP_PROXY_PORT",
  "KEEL_VOLUME_MOUNT_PATH",
] as const;

export class ApiError extends Error {
  status: number;
  /** Parsed JSON error body (for structured errors such as template input problems). */
  data?: unknown;
  constructor(status: number, message: string, data?: unknown) {
    super(message);
    this.status = status;
    this.data = data;
  }
}

function redirectToLogin() {
  if (typeof window === "undefined") return;
  if (window.location.pathname.startsWith("/login")) return;
  const next = window.location.pathname + window.location.search;
  window.location.href = `/login/?next=${encodeURIComponent(next)}`;
}

export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: { noRedirect?: boolean } = {},
): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    credentials: "include",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401 && !opts.noRedirect) {
    redirectToLogin();
    throw new ApiError(401, t("인증이 필요합니다"));
  }
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    let data: unknown;
    try {
      const j = (await res.json()) as { error?: string };
      data = j;
      if (j?.error) msg = j.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg, data);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

const enc = encodeURIComponent;
const inst = (sid: ID, eid: ID) => `/services/${enc(sid)}/instances/${enc(eid)}`;

/** WebSocket URL of the service shell (v1.5). Binary frames = bytes, text frames = JSON control messages. */
export function execUrl(sid: ID, eid: ID, opts: { replica?: string; cmd?: string; cols?: number; rows?: number } = {}): string {
  const q = new URLSearchParams();
  if (opts.replica) q.set("replica", opts.replica);
  if (opts.cmd) q.set("cmd", opts.cmd);
  if (opts.cols) q.set("cols", String(opts.cols));
  if (opts.rows) q.set("rows", String(opts.rows));
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  const qs = q.toString();
  return `${proto}//${window.location.host}/api${inst(sid, eid)}/exec${qs ? `?${qs}` : ""}`;
}

/** Server → client control frame of the service shell. */
export type ExecControl = { type: "exit"; code: number } | { type: "error"; message: string };

/* ── v1.5: environment sync ─────────────────────────────────────────── */

/** Values without a ${{ }} reference are masked as "•••". */
export type SyncVarChange = { key: string; action: "added" | "changed" | "removed"; from: string | null; to: string | null };
export type SyncFieldChange = { field: string; from: unknown; to: unknown };
export type SyncServiceDiff = {
  service_id: ID;
  service_name: string;
  /** create = the target instance was never configured or deployed. */
  action: "create" | "update";
  settings: SyncFieldChange[];
  variables: SyncVarChange[];
  volumes: { mount_path: string; size_limit_mb: number | null }[];
  /** Generated database passwords that are never synced. */
  protected_variables: string[];
};
export type SyncPlan = {
  from: { id: ID; name: string };
  to: { id: ID; name: string };
  services: SyncServiceDiff[];
  shared_variables: SyncVarChange[];
};
export type SyncInclude = { settings: boolean; variables: boolean; shared_variables: boolean; volumes: boolean };
export type SyncResult = {
  from: { id: ID; name: string };
  to: { id: ID; name: string };
  services: {
    service_id: ID;
    service_name: string;
    settings: number;
    variables: number;
    volumes: number;
    deployment_id: ID | null;
    deploy_skipped?: string;
  }[];
  shared_variables: number;
};

/* ── v1.5: database data browser ────────────────────────────────────── */

export type DataTable = {
  schema: string;
  name: string;
  kind: "table" | "view" | "materialized_view" | "foreign_table" | "collection" | "keyspace";
  /** Approximate row/document/key count (null when unknown). */
  rows: number | null;
};
/** Cells are strings, null (SQL NULL) or JSON values (mongo). */
export type DataCell = string | number | boolean | null | Record<string, unknown> | unknown[];
export type DataRows = {
  schema: string;
  name: string;
  columns: string[];
  rows: DataCell[][];
  limit: number;
  offset: number;
  has_more: boolean;
  truncated: boolean;
  /** SQL column types aligned with `columns` (null when unknown; null for mongo). */
  column_types?: (string | null)[] | null;
  /** Primary-key columns (SQL; empty when the table has none). */
  primary_key?: string[];
  /** Rows can be edited through data/tables/{name}/rows (SQL table with a primary key). */
  editable?: boolean;
};
/** Row edit body values: strings, numbers, booleans or null (SQL NULL). */
export type DataRowValues = Record<string, string | number | boolean | null>;
export type DataQueryResult = {
  kind: "table" | "text";
  columns: string[];
  rows: DataCell[][];
  text: string;
  truncated: boolean;
  row_count: number;
  duration_ms: number;
};
export type RedisKeyType = "string" | "list" | "set" | "zset" | "hash" | "stream" | string;
export type RedisKey = { key: string; type: RedisKeyType; ttl: number };
export type RedisKeyDetail = {
  key: string;
  type: RedisKeyType;
  /** seconds; -1 = no expiry. */
  ttl: number;
  db: number;
  length: number | null;
  value: unknown;
  /** true when only the first part is shown. */
  truncated: boolean;
};

/** aven CLI binary served by this aven-server (GET /api/cli/downloads, no auth). */
export type CliDownload = { name: string; os: "linux" | "darwin" | "windows"; arch: "amd64" | "arm64"; url: string; size: number; sha256: string; updated_at: string };
export type CliDownloads = { version: string; files: CliDownload[]; install_script: string };

export const api = {
  // auth. Without email the password is checked against AVEN_ADMIN_PASSWORD (built-in owner).
  // 401 {"error":"totp_required"} → ask for a 2FA code and send it as totp_code.
  login: (input: { email?: string; password: string; totp_code?: string }) =>
    request<void>("POST", "/auth/login", input, { noRedirect: true }),
  cliDownloads: () => request<CliDownloads>("GET", "/cli/downloads", undefined, { noRedirect: true }),
  samlStatus: () => request<{ configured: boolean; enforce_sso: boolean }>("GET", "/auth/saml/status", undefined, { noRedirect: true }),
  samlSettings: () => request<SAMLSettings>("GET", "/saml/settings"),
  saveSAMLSettings: (input: { metadata_url?: string; metadata_xml?: string; clear_metadata?: boolean; domains: string[]; default_role: WorkspaceRole; enforce_sso: boolean }) =>
    request<{ config: SAMLConfig }>("PUT", "/saml/settings", input),
  verifySAMLDomain: (domain: string) => request<{ domain: SAMLDomain }>("POST", `/saml/domains/${enc(domain)}/verify`, {}),
  logout: () => request<void>("POST", "/auth/logout"),
  me: () => request<Me>("GET", "/auth/me"),
  updateMe: (input: { name?: string; current_password?: string; new_password?: string }) => request<User>("PATCH", "/auth/me", input),
  sessions: () => request<SessionInfo[]>("GET", "/auth/sessions"),
  deleteSession: (id: ID) => request<void>("DELETE", `/auth/sessions/${enc(id)}`),
  totpSetup: () => request<{ secret: string; otpauth_url: string }>("POST", "/auth/2fa/setup"),
  totpEnable: (code: string) => request<{ recovery_codes: string[] }>("POST", "/auth/2fa/enable", { code }),
  totpDisable: (input: { code?: string; password?: string }) => request<void>("POST", "/auth/2fa/disable", input),
  totpRecoveryCodes: (input: string | { code?: string; password?: string }) =>
    request<{ recovery_codes: string[] }>("POST", "/auth/2fa/recovery-codes", typeof input === "string" ? { code: input } : input, { noRedirect: true }),
  passkeys: () => request<Passkey[]>("GET", "/auth/passkeys"),
  beginPasskeyRegistration: (name: string, password: string) =>
    request<{ challenge_id: string; options: WebAuthnOptions }>("POST", "/auth/passkeys/register/begin", { name, password }, { noRedirect: true }),
  finishPasskeyRegistration: (challenge: ID, credential: Record<string, unknown>) =>
    request<{ recovery_codes: string[] }>("POST", `/auth/passkeys/register/${enc(challenge)}/finish`, credential),
  deletePasskey: (id: ID, password: string) => request<void>("DELETE", `/auth/passkeys/${enc(id)}`, { password }, { noRedirect: true }),
  beginPasskeyLogin: (input: { email?: string; password?: string } = {}) =>
    request<{ challenge_id: string; flow: "login_factor" | "login_discoverable"; options: WebAuthnOptions }>("POST", "/auth/passkeys/login/begin", input, { noRedirect: true }),
  finishPasskeyLogin: (flow: "login_factor" | "login_discoverable", challenge: ID, credential: Record<string, unknown>) =>
    request<void>("POST", `/auth/passkeys/login/${flow === "login_factor" ? "factor" : "discoverable"}/${enc(challenge)}/finish`, credential, { noRedirect: true }),
  workspaceSecurity: () => request<{ require_2fa: boolean }>("GET", "/instance/security"),
  instanceOverview: () => request<InstanceOverview>("GET", "/instance/overview"),
  /** 서버 관리자 작업 공간 삭제: 안의 프로젝트도 함께 영구 삭제된다. confirm은 작업 공간 이름. */
  adminDeleteWorkspace: (wsid: ID, confirm: string) => request<void>("DELETE", `/instance/workspaces/${enc(wsid)}`, { confirm }),
  instanceNotice: () => request<InstanceNotice>("GET", "/instance/notice"),

  // server owner only (session required)
  ownerOverview: () => request<OwnerOverview>("GET", "/owner"),
  setupStatus: () => request<SetupStatus>("GET", "/owner/setup"),
  setSetupDismissed: (dismissed: boolean) => request<{ dismissed: boolean }>("PUT", "/owner/setup", { dismissed }),
  selfService: () => request<SelfServiceStatus>("GET", "/owner/self-service"),
  createSelfService: (body: { repo_url?: string; branch?: string } = {}) => request<SelfServiceStatus>("POST", "/owner/self-service", body),
  unlinkSelfService: () => request<SelfServiceStatus>("DELETE", "/owner/self-service"),
  checkSelfUpdate: () => request<SelfServiceStatus>("POST", "/owner/self-service/check"),
  serverConfig: () => request<ServerConfig>("GET", "/owner/server-config"),
  updateServerConfig: (body: { set?: Record<string, string>; unset?: string[] }) => request<ServerConfig>("PUT", "/owner/server-config", body),
  /** Server owner: active users with a GitHub connection (to assign someone else's connection to a project). */
  ownerGithubConnections: () => request<{ user_id: ID; user_name: string; email: string; login: string }[]>("GET", "/owner/github-connections"),
  /** Server owner: force a workspace owner (bypasses personal-owner and last-owner rules; personal → team). */
  forceWorkspaceOwner: (wsid: ID, userID: ID, previous: PreviousOwnerAction) =>
    request<{ previous_owners: ID[] | null; converted: boolean }>("PUT", `/owner/workspaces/${enc(wsid)}/owner`, { user_id: userID, previous }),
  /** Server owner: suspend/resume a project or workspace (stops services, blocks deploys until released). */
  ownerHolds: () => request<OwnerHold[]>("GET", "/owner/holds"),
  createOwnerHold: (scope: OwnerHoldScope, targetID: ID, reason: string) =>
    request<{ hold: OwnerHold; failed: Record<ID, string> }>("POST", "/owner/holds", { scope, target_id: targetID, reason }),
  releaseOwnerHold: (scope: OwnerHoldScope, targetID: ID) =>
    request<{ restarted: ID[]; failed: Record<ID, string> }>("DELETE", `/owner/holds/${scope}/${enc(targetID)}`),
  /** Server owner: view the dashboard as another user (read-only, default 30 minutes). */
  startViewAs: (userID: ID, minutes?: number) => request<{ user_id: ID; until: string }>("POST", "/owner/view-as", { user_id: userID, minutes }),
  stopViewAs: () => request<void>("POST", "/auth/view-as/stop"),
  setAdminCaps: (disabled: AdminCapID[]) => request<{ disabled: AdminCapID[] }>("PUT", "/owner/admin-caps", { disabled }),
  setMaintenance: (input: { enabled: boolean; message: string; block_logins: boolean; pause_webhooks: boolean }) =>
    request<MaintenanceState>("PUT", "/owner/maintenance", input),
  setAnnouncement: (input: { enabled: boolean; message: string; level: AnnouncementLevel }) => request<Announcement>("PUT", "/owner/announcement", input),
  ownerSessions: () => request<OwnerSession[]>("GET", "/owner/sessions"),
  ownerDeleteSession: (id: ID) => request<void>("DELETE", `/owner/sessions/${enc(id)}`),
  ownerRevokeSessions: (input: { user_id: ID } | { scope: "non_owners" }) => request<{ revoked?: number }>("POST", "/owner/sessions/revoke", input),
  ownerTokens: () => request<OwnerToken[]>("GET", "/owner/tokens"),
  ownerDeleteToken: (id: ID) => request<void>("DELETE", `/owner/tokens/${enc(id)}`),
  ownerRevokeTokens: (input: { user_id: ID } | { scope: "non_owners" }) => request<{ revoked?: number }>("POST", "/owner/tokens/revoke", input),
  transferOwnership: (input: { user_id: ID; stay_owner: boolean }) => request<{ owner: ID; still_owner: boolean }>("POST", "/owner/transfer", input),
  lockdown: (message: string) => request<{ sessions_revoked: number; tokens_revoked: number }>("POST", "/owner/lockdown", { message }),
  avenBackups: () => request<AvenBackup[]>("GET", "/owner/backups"),
  createAvenBackup: () => request<AvenBackup>("POST", "/owner/backups"),
  deleteAvenBackup: (name: string) => request<void>("DELETE", `/owner/backups/${enc(name)}`),
  avenBackupUrl: (name: string) => `/api/owner/backups/${enc(name)}`,
  restartServer: () => request<{ restarting: boolean }>("POST", "/owner/restart"),
  resourceLimits: () => request<WorkspaceResources[]>("GET", "/instance/resource-limits"),
  workspaceResources: (wsid: ID) => request<WorkspaceResources>("GET", `/workspaces/${enc(wsid)}/resource-limits`),
  setResourceLimits: (wsid: ID, limits: ResourceLimits) => request<WorkspaceResources>("PUT", `/instance/resource-limits/${enc(wsid)}`, limits),
  setWorkspaceSecurity: (require_2fa: boolean) => request<{ require_2fa: boolean }>("PATCH", "/instance/security", { require_2fa }),
  acceptInvite: (input: { token: string; name: string; password: string }) =>
    request<User>("POST", "/auth/accept-invite", input, { noRedirect: true }),

  // users / invites / tokens / members / audit
  users: () => request<User[]>("GET", "/users"),
  updateUser: (uid: ID, input: { name?: string; role?: WorkspaceRole; disabled?: boolean; password?: string; reset_2fa?: boolean }) =>
    request<User>("PATCH", `/users/${enc(uid)}`, input),
  deleteUser: (uid: ID) => request<void>("DELETE", `/users/${enc(uid)}`),
  invites: () => request<Invite[]>("GET", "/invites"),
  createInvite: (input: { email: string; role?: InstanceRole; project_id?: ID; project_role?: ProjectRole; send_email?: boolean }) =>
    request<{ invite: Invite; token: string; url: string; email_sent?: boolean; email_error?: string }>("POST", "/invites", input),
  deleteInvite: (id: ID) => request<void>("DELETE", `/invites/${enc(id)}`),
  tokens: () => request<ApiToken[]>("GET", "/tokens"),
  createToken: (input: { name: string; scope?: TokenScope; workspace_id?: ID; project_id?: ID; environment_id?: ID; role?: ProjectRole; expires_in_days?: number | null }) =>
    request<ApiToken & { token: string }>("POST", "/tokens", input),
  deleteToken: (id: ID) => request<void>("DELETE", `/tokens/${enc(id)}`),
  oauthApps: () => request<OAuthApp[]>("GET", "/oauth/apps"),
  createOAuthApp: (input: { name: string; description?: string; client_type: OAuthApp["client_type"]; redirect_uris: string[] }) =>
    request<{ app: OAuthApp; client_secret: string | null }>("POST", "/oauth/apps", input),
  updateOAuthApp: (id: ID, input: { name?: string; description?: string; redirect_uris?: string[] }) =>
    request<OAuthApp>("PATCH", `/oauth/apps/${enc(id)}`, input),
  deleteOAuthApp: (id: ID) => request<void>("DELETE", `/oauth/apps/${enc(id)}`),
  oauthClientSecrets: (id: ID) => request<OAuthClientSecret[]>("GET", `/oauth/apps/${enc(id)}/secrets`),
  createOAuthClientSecret: (id: ID) => request<{ secret: string; record: OAuthClientSecret }>("POST", `/oauth/apps/${enc(id)}/secrets`, {}),
  revokeOAuthClientSecret: (id: ID, secretID: ID) => request<void>("DELETE", `/oauth/apps/${enc(id)}/secrets/${enc(secretID)}`),
  oauthAuthorizedApps: () => request<OAuthAuthorizedApp[]>("GET", "/oauth/authorized-apps"),
  updateOAuthGrant: (id: ID, input: { project_ids: ID[] }) => request<void>("PATCH", `/oauth/authorized-apps/${enc(id)}`, input),
  revokeOAuthGrant: (id: ID) => request<void>("DELETE", `/oauth/authorized-apps/${enc(id)}`),
  oauthConsentRequest: (requestID: string) => request<OAuthConsentRequest>("GET", `/oauth/requests/${enc(requestID)}`),
  finishOAuthConsent: (requestID: string, input: { decision: "approve" | "deny"; scopes?: string[]; project_ids?: ID[] }) =>
    request<{ redirect_uri: string }>("POST", `/oauth/requests/${enc(requestID)}`, input),
  members: (pid: ID) => request<Member[]>("GET", `/projects/${enc(pid)}/members`),
  setMember: (pid: ID, uid: ID, role: ProjectRole) => request<Member>("PUT", `/projects/${enc(pid)}/members/${enc(uid)}`, { role }),
  removeMember: (pid: ID, uid: ID) => request<void>("DELETE", `/projects/${enc(pid)}/members/${enc(uid)}`),
  audit: (limit = 100) => request<AuditEntry[]>("GET", `/audit?limit=${limit}`),

  // projects / environments
  listProjects: (workspace?: ID) => request<Project[]>("GET", `/projects${workspace ? `?workspace=${enc(workspace)}` : ""}`),
  createProject: (input: { name: string; description?: string; workspace_id?: ID }) => request<Project>("POST", "/projects", input),
  getProject: (pid: ID) => request<ProjectDetail>("GET", `/projects/${enc(pid)}`),
  updateProject: (pid: ID, input: Partial<Project>) => request<Project>("PATCH", `/projects/${enc(pid)}`, input),
  deleteProject: (pid: ID) => request<void>("DELETE", `/projects/${enc(pid)}`),
  /** v1.6: move a project to another workspace (project admin + member+ in target). */
  moveProject: (pid: ID, workspaceID: ID) => request<Project>("PUT", `/projects/${enc(pid)}/workspace`, { workspace_id: workspaceID }),
  /** v1.6: set which user's GitHub connection the project clones with (only your own id, or null for the server token). */
  setProjectGithub: (pid: ID, userID: ID | null) =>
    request<{ github_account: ProjectGithubAccount | null }>("PUT", `/projects/${enc(pid)}/github`, { user_id: userID }),

  workspaceGroupSettings: (wsid: ID) => request<{ access_groups_enabled: boolean }>("GET", `/workspaces/${enc(wsid)}/access-group-settings`),
  setWorkspaceGroupSettings: (wsid: ID, enabled: boolean) => request<{ access_groups_enabled: boolean }>("PATCH", `/workspaces/${enc(wsid)}/access-group-settings`, { access_groups_enabled: enabled }),
  inviteWorkspaceMember: (wsid: ID, input: { email: string; role: WorkspaceRole; send_email?: boolean }) => request<{ invite: Invite; token: string; url: string; email_sent?: boolean; email_error?: string }>("POST", `/workspaces/${enc(wsid)}/invites`, input),
  // workspaces
  workspaces: () => request<Workspace[]>("GET", "/workspaces"),
  createWorkspace: (name: string) => request<Workspace>("POST", "/workspaces", { name }),
  renameWorkspace: (wsid: ID, name: string) => request<Workspace>("PATCH", `/workspaces/${enc(wsid)}`, { name }),
  deleteWorkspace: (wsid: ID) => request<void>("DELETE", `/workspaces/${enc(wsid)}`),
  workspacePeople: (wsid: ID) => request<WorkspaceMember[]>("GET", `/workspaces/${enc(wsid)}/members`).then((members) => members.map((m) => ({ ...m, id: m.user_id }))),
  workspaceMembers: (wsid: ID) => request<WorkspaceMember[]>("GET", `/workspaces/${enc(wsid)}/members`),
  addWorkspaceMember: (wsid: ID, input: { email: string; role: WorkspaceRole }) =>
    request<WorkspaceMember>("POST", `/workspaces/${enc(wsid)}/members`, input),
  setWorkspaceMember: (wsid: ID, uid: ID, role: WorkspaceRole) =>
    request<WorkspaceMember>("PATCH", `/workspaces/${enc(wsid)}/members/${enc(uid)}`, { role }),
  removeWorkspaceMember: (wsid: ID, uid: ID) => request<void>("DELETE", `/workspaces/${enc(wsid)}/members/${enc(uid)}`),

  featureFlags: (pid: ID, environmentID: ID) =>
    request<FeatureFlagSnapshot>("GET", `/projects/${enc(pid)}/flags?environment_id=${enc(environmentID)}`),
  createFeatureFlag: (pid: ID, input: { name: string; type: FeatureFlagType; environment_id: ID; default_value: unknown; rules?: FeatureFlagRule[] }) =>
    request<FeatureFlag>("POST", `/projects/${enc(pid)}/flags`, input),
  updateFeatureFlag: (pid: ID, name: string, input: { environment_id: ID; default_value?: unknown; rules?: FeatureFlagRule[] }) =>
    request<FeatureFlag>("PUT", `/projects/${enc(pid)}/flags/${enc(name)}`, input),
  deleteFeatureFlag: (pid: ID, name: string) => request<void>("DELETE", `/projects/${enc(pid)}/flags/${enc(name)}`),
  iacExport: (pid: ID, eid: ID, includeVariables = false) =>
    request<IACDefinition>("GET", `/projects/${enc(pid)}/environments/${enc(eid)}/config${includeVariables ? "?include_variables=1" : ""}`),
  iacPlan: (pid: ID, eid: ID, input: { definition: IACDefinition; show_values?: boolean }) =>
    request<IACPlan>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/config/plan`, input),
  iacApply: (pid: ID, eid: ID, input: { definition: IACDefinition; plan_hash: string; confirm_destructive: boolean; deploy?: boolean }) =>
    request<IACApplyResult>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/config/apply`, input),
  iacPartials: (pid: ID, eid: ID) => request<IACOwnership>("GET", `/projects/${enc(pid)}/environments/${enc(eid)}/config/partials`),
  iacReleasePartial: (pid: ID, eid: ID, input: { partial: string; resources?: string[]; dry_run?: boolean; base_config_etag?: string }) =>
    request<IACOwnershipChange>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/config/partials/release`, input),
  iacTransferPartial: (pid: ID, eid: ID, input: { partial: string; to: string; resources?: string[]; dry_run?: boolean; base_config_etag?: string }) =>
    request<IACOwnershipChange>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/config/partials/transfer`, input),
  createEnvironment: (pid: ID, input: { name: string; copy_from?: ID }) =>
    request<Environment>("POST", `/projects/${enc(pid)}/environments`, input),
  renameEnvironment: (eid: ID, name: string) => request<Environment>("PATCH", `/environments/${enc(eid)}`, { name }),
  updateEnvironment: (eid: ID, input: { name?: string; restricted?: boolean }) =>
    request<Environment>("PATCH", `/environments/${enc(eid)}`, input),
  deleteEnvironment: (eid: ID) => request<void>("DELETE", `/environments/${enc(eid)}`),

  // enterprise access controls (access groups, guardrails)
  accessControls: () => request<AccessControlSettings>("GET", "/access-controls"),
  updateAccessControls: (input: Partial<AccessControlSettings>) => request<AccessControlSettings>("PATCH", "/access-controls", input),
  accessPolicies: () => request<AccessPolicies>("GET", "/access-controls/policies"),
  accessGroups: (wsid: ID = "ws_default") => request<AccessGroup[]>("GET", `/workspaces/${enc(wsid)}/access-groups`),
  createAccessGroup: (input: AccessGroupInput, wsid: ID = "ws_default") => request<AccessGroup>("POST", `/workspaces/${enc(wsid)}/access-groups`, input),
  updateAccessGroup: (id: ID, input: AccessGroupInput, wsid: ID = "ws_default") => request<AccessGroup>("PUT", `/workspaces/${enc(wsid)}/access-groups/${enc(id)}`, input),
  deleteAccessGroup: (id: ID, wsid: ID = "ws_default") => request<void>("DELETE", `/workspaces/${enc(wsid)}/access-groups/${enc(id)}`),
  projectAccessGroups: (pid: ID) => request<ProjectAccessGroups>("GET", `/projects/${enc(pid)}/access-groups`),
  setProjectAccessGroups: (pid: ID, groupIds: ID[]) =>
    request<ProjectAccessGroups>("PUT", `/projects/${enc(pid)}/access-groups`, { group_ids: groupIds }),
  syncPreview: (eid: ID, from: ID) => request<SyncPlan>("GET", `/environments/${enc(eid)}/sync-preview?from=${enc(from)}`),
  syncEnvironment: (eid: ID, input: { from: ID; services?: ID[]; include?: SyncInclude; deploy?: boolean }) =>
    request<SyncResult>("POST", `/environments/${enc(eid)}/sync`, input),

  // canvas
  canvas: (eid: ID) => request<CanvasData>("GET", `/environments/${enc(eid)}/canvas`),
  composePreview: (pid: ID, eid: ID, input: ComposeRequest) =>
    request<ComposePreview>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/compose/preview`, input),
  composeApply: (pid: ID, eid: ID, input: ComposeRequest) =>
    request<{ stack: StackView; created: string[]; notes: string[]; deploy: { started: boolean } }>(
      "POST",
      `/projects/${enc(pid)}/environments/${enc(eid)}/compose/apply`,
      input,
    ),
  redeployStack: (pid: ID, eid: ID, stid: ID) =>
    request<{ started: boolean }>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/stacks/${enc(stid)}/redeploy`),
  deleteStack: (pid: ID, eid: ID, stid: ID) =>
    request<void>("DELETE", `/projects/${enc(pid)}/environments/${enc(eid)}/stacks/${enc(stid)}`),
  setCanvasPos: (sid: ID, eid: ID, pos: { x: number; y: number }) => request<void>("PUT", `${inst(sid, eid)}/canvas`, pos),

  // services
  createService: (pid: ID, input: CreateServiceInput) => request<Service>("POST", `/projects/${enc(pid)}/services`, input),
  updateService: (sid: ID, input: { name?: string; icon?: string | null }) =>
    request<Service>("PATCH", `/services/${enc(sid)}`, input),
  deleteService: (sid: ID) => request<void>("DELETE", `/services/${enc(sid)}`),
  getInstance: (sid: ID, eid: ID) => request<ServiceInstance>("GET", inst(sid, eid)),
  serviceAdmin: (sid: ID, eid: ID) => request<ServiceAdmin>("GET", `${inst(sid, eid)}/admin`),
  createServiceHold: (sid: ID, scope: "service" | "deploy", reason: string) =>
    request<{ hold: OwnerHold; failed: Record<ID, string> }>("POST", `/services/${enc(sid)}/admin/holds`, { scope, reason }),
  releaseServiceHold: (sid: ID, scope: "service" | "deploy") =>
    request<{ restarted: ID[]; failed: Record<ID, string> }>("DELETE", `/services/${enc(sid)}/admin/holds/${scope}`),
  imageUpdates: (sid: ID, eid: ID) => request<ImageUpdate[]>("GET", `${inst(sid, eid)}/image-updates`),
  applyImageUpdate: (sid: ID, eid: ID, uid: ID) =>
    request<Deployment>("POST", `${inst(sid, eid)}/image-updates/${enc(uid)}/apply`),
  skipImageUpdate: (sid: ID, eid: ID, uid: ID) =>
    request<ImageUpdate[]>("POST", `${inst(sid, eid)}/image-updates/${enc(uid)}/skip`),
  checkImageUpdates: (sid: ID, eid: ID) => request<ImageUpdate[]>("POST", `${inst(sid, eid)}/image-updates/check`),
  networkDiagnostics: (sid: ID, eid: ID, input: { host: string; port: number; replica?: string }) =>
    request<NetworkDiagnosticResult>("POST", `${inst(sid, eid)}/network-diagnostics`, input),
  updateInstance: (sid: ID, eid: ID, patch: InstancePatch, deploy = false, moveVolumes = false) => {
    const q = new URLSearchParams();
    if (deploy) q.set("deploy", "1");
    if (moveVolumes) q.set("move_volumes", "1");
    const qs = q.toString();
    return request<ServiceInstance>("PATCH", `${inst(sid, eid)}${qs ? `?${qs}` : ""}`, patch);
  },
  resolvedVariables: (sid: ID, eid: ID) => request<Record<string, ResolvedVar>>("GET", `${inst(sid, eid)}/variables/resolved`),
  rawVariables: (sid: ID, eid: ID, format: "env" | "json" = "env") =>
    request<RawVariables>("GET", `${inst(sid, eid)}/variables/raw?format=${format}`),
  putRawVariables: (sid: ID, eid: ID, format: "env" | "json", content: string) =>
    request<ServiceInstance>("PUT", `${inst(sid, eid)}/variables/raw`, { format, content }),

  // database data browser (v1.5)
  dataTables: (sid: ID, eid: ID) =>
    request<{ kind: DatabaseKind; tables: DataTable[]; truncated: boolean }>("GET", `${inst(sid, eid)}/data/tables`),
  dataRows: (sid: ID, eid: ID, t: { schema: string; name: string }, limit = 50, offset = 0) =>
    request<DataRows>(
      "GET",
      `${inst(sid, eid)}/data/tables/${enc(t.name)}?schema=${enc(t.schema)}&limit=${limit}&offset=${offset}`,
    ),
  dataKeys: (sid: ID, eid: ID, opts: { pattern?: string; cursor?: string; db?: number } = {}) => {
    const q = new URLSearchParams();
    if (opts.pattern) q.set("pattern", opts.pattern);
    if (opts.cursor) q.set("cursor", opts.cursor);
    if (opts.db) q.set("db", String(opts.db));
    return request<{ cursor: string; keys: RedisKey[]; db: number }>("GET", `${inst(sid, eid)}/data/keys?${q.toString()}`);
  },
  dataKey: (sid: ID, eid: ID, key: string, db = 0) =>
    request<RedisKeyDetail>("GET", `${inst(sid, eid)}/data/keys/${enc(key)}${db ? `?db=${db}` : ""}`),
  dataQuery: (sid: ID, eid: ID, query: string, db?: number, resp3?: boolean) =>
    request<DataQueryResult>("POST", `${inst(sid, eid)}/data/query`, { query, ...(db ? { db } : {}), ...(resp3 ? { resp3 } : {}) }),
  // row editing (member+; SQL tables with a primary key)
  dataRowUpdate: (sid: ID, eid: ID, t: { schema: string; name: string }, pk: DataRowValues, set: DataRowValues) =>
    request<{ affected: number }>("PATCH", `${inst(sid, eid)}/data/tables/${enc(t.name)}/rows?schema=${enc(t.schema)}`, { pk, set }),
  dataRowInsert: (sid: ID, eid: ID, t: { schema: string; name: string }, set: DataRowValues) =>
    request<{ affected: number }>("POST", `${inst(sid, eid)}/data/tables/${enc(t.name)}/rows?schema=${enc(t.schema)}`, { set }),
  dataRowDelete: (sid: ID, eid: ID, t: { schema: string; name: string }, pk: DataRowValues) =>
    request<{ affected: number }>("DELETE", `${inst(sid, eid)}/data/tables/${enc(t.name)}/rows?schema=${enc(t.schema)}`, { pk }),

  // domains / tcp
  addDomain: (sid: ID, eid: ID, input: { host?: string } & Omit<DomainInput, "target_port"> & { target_port?: number }) =>
    request<Domain>("POST", `${inst(sid, eid)}/domains`, input),
  patchDomain: (sid: ID, eid: ID, host: string, input: DomainInput) =>
    request<Domain>("PATCH", `${inst(sid, eid)}/domains/${enc(host)}`, input),
  deleteDomain: (sid: ID, eid: ID, host: string) => request<void>("DELETE", `${inst(sid, eid)}/domains/${enc(host)}`),
  addTcpProxy: (sid: ID, eid: ID, container_port: number) =>
    request<ServiceInstance>("POST", `${inst(sid, eid)}/tcp-proxy`, { container_port }),
  deleteTcpProxy: (sid: ID, eid: ID) => request<void>("DELETE", `${inst(sid, eid)}/tcp-proxy`),
  /** P6: change the container port behind the TCP proxy (public port stays). */
  patchTcpProxy: (sid: ID, eid: ID, container_port: number) =>
    request<ServiceInstance>("PATCH", `${inst(sid, eid)}/tcp-proxy`, { container_port }),
  /** P1: re-check DNS/TXT/HTTP verification now. */
  verifyDomain: (sid: ID, eid: ID, host: string) => request<Domain>("POST", `${inst(sid, eid)}/domains/${enc(host)}/verify`, {}),
  /** P4: everything Settings > Networking shows, in one call. */
  networking: (sid: ID, eid: ID) => request<InstanceNetworking>("GET", `${inst(sid, eid)}/networking`),
  /** P5: is `<name>.<env>.aven.internal` free in this environment? */
  privateDnsAvailable: (sid: ID, eid: ID, name: string) =>
    request<{ available: boolean; reason?: string }>("GET", `${inst(sid, eid)}/private-dns/available?name=${enc(name)}`),

  // deployments
  deploy: (sid: ID, eid: ID, forceBuild = false) =>
    request<Deployment>("POST", `${inst(sid, eid)}/deploy`, forceBuild ? { force_build: true } : undefined),
  redeploy: (did: ID) => request<Deployment>("POST", `/deployments/${enc(did)}/redeploy`),
  rollback: (did: ID) => request<Deployment>("POST", `/deployments/${enc(did)}/rollback`),
  cancel: (did: ID) => request<void>("POST", `/deployments/${enc(did)}/cancel`),
  restart: (sid: ID, eid: ID) => request<void>("POST", `${inst(sid, eid)}/restart`),
  stop: (sid: ID, eid: ID) => request<void>("POST", `${inst(sid, eid)}/stop`),
  /** v1.5: change the running replica count now (no redeploy). 409 while autoscaling is on or nothing is live. */
  scale: (sid: ID, eid: ID, replicas: number) => request<ScaleResult>("POST", `${inst(sid, eid)}/scale`, { replicas }),
  listDeployments: (sid: ID, eid: ID, limit = 50) =>
    request<Deployment[]>("GET", `${inst(sid, eid)}/deployments?limit=${limit}`),
  getDeployment: (did: ID) => request<Deployment>("GET", `/deployments/${enc(did)}`),
  cronRuns: (did: ID) => request<CronRun[]>("GET", `/deployments/${enc(did)}/cron-runs`),
  cronRunLogs: (rid: ID) => request<DeployLogLine[]>("GET", `/cron-runs/${enc(rid)}/logs`),
  runCron: (sid: ID, eid: ID) => request<CronRun>("POST", `${inst(sid, eid)}/cron/run`),

  // metrics
  metrics: (sid: ID, eid: ID, range: MetricRange) => request<ServiceMetrics>("GET", `${inst(sid, eid)}/metrics?range=${range}`),
  envMetrics: (eid: ID, range: MetricRange) => request<EnvMetrics>("GET", `/environments/${enc(eid)}/metrics?range=${range}`),

  // volumes / backups
  createVolume: (eid: ID, input: { service_id: ID; mount_path: string; name?: string; size_limit_mb?: number | null }) =>
    request<Volume>("POST", `/environments/${enc(eid)}/volumes`, input),
  updateVolume: (vid: ID, input: { name?: string; mount_path?: string; service_instance_id?: ID | null; size_limit_mb?: number | null }) =>
    request<Volume>("PATCH", `/volumes/${enc(vid)}`, input),
  deleteVolume: (vid: ID) => request<void>("DELETE", `/volumes/${enc(vid)}`),
  envVolumes: (eid: ID) => request<Volume[]>("GET", `/environments/${enc(eid)}/volumes`),
  backupSchedule: (vid: ID) => request<BackupSchedule>("GET", `/volumes/${enc(vid)}/backup-schedule`),
  listBackups: (vid: ID) => request<Backup[]>("GET", `/volumes/${enc(vid)}/backups`),
  createBackup: (vid: ID) => request<Backup>("POST", `/volumes/${enc(vid)}/backups`),
  restoreBackup: (bid: ID) => request<void>("POST", `/backups/${enc(bid)}/restore`),
  deleteBackup: (bid: ID) => request<void>("DELETE", `/backups/${enc(bid)}`),
  setBackupSchedule: (vid: ID, input: { cron: string | null; keep: number }) =>
    request<void>("PUT", `/volumes/${enc(vid)}/backup-schedule`, input),

  // SSH deploy key (private key never leaves the server)
  deployKey: (sid: ID) => request<DeployKey>("GET", `/services/${enc(sid)}/deploy-key`),
  createDeployKey: (sid: ID, rotate = false) => request<DeployKey>("POST", `/services/${enc(sid)}/deploy-key${rotate ? "?rotate=1" : ""}`),
  deleteDeployKey: (sid: ID) => request<void>("DELETE", `/services/${enc(sid)}/deploy-key`),

  // database logical backups
  dbBackups: (sid: ID, eid: ID) => request<DbBackup[]>("GET", `${inst(sid, eid)}/db-backups`),
  createDbBackup: (sid: ID, eid: ID, destination_id?: string) =>
    request<DbBackup>("POST", `${inst(sid, eid)}/db-backups`, destination_id === undefined ? {} : { destination_id }),
  dbBackupConfig: (sid: ID, eid: ID) => request<DbBackupConfig>("GET", `${inst(sid, eid)}/db-backups/config`),
  setDbBackupConfig: (sid: ID, eid: ID, input: Partial<DbBackupConfig>) =>
    request<DbBackupConfig>("PUT", `${inst(sid, eid)}/db-backups/config`, input),
  restoreDbBackup: (id: ID, target?: { service_id?: ID; environment_id?: ID }) =>
    request<DbRestoreStatus>("POST", `/db-backups/${enc(id)}/restore`, target ?? {}),
  dbRestoreStatus: (id: ID) => request<DbRestoreStatus>("GET", `/db-backups/${enc(id)}/restore`),
  deleteDbBackup: (id: ID) => request<void>("DELETE", `/db-backups/${enc(id)}`),
  dbBackupDownloadUrl: (id: ID) => `/api/db-backups/${enc(id)}/download`,
  // point-in-time recovery (continuous WAL / binlog archiving)
  pitr: (sid: ID, eid: ID) => request<PitrStatus>("GET", `${inst(sid, eid)}/pitr`),
  setPitr: (sid: ID, eid: ID, input: { enabled?: boolean; retention_days?: number; destination_id?: string }) =>
    request<{ config: PitrConfig; warning?: string }>("PUT", `${inst(sid, eid)}/pitr`, input),
  pitrBase: (sid: ID, eid: ID) => request<{ status: string }>("POST", `${inst(sid, eid)}/pitr/base`),
  pitrRestore: (sid: ID, eid: ID, input: { target_time: string; in_place?: boolean; new_service_name?: string }) =>
    request<PitrRestore>("POST", `${inst(sid, eid)}/pitr/restore`, input),
  deletePitrArchive: (sid: ID, eid: ID) => request<{ deleted: number }>("DELETE", `${inst(sid, eid)}/pitr/archive`),
  backupDestinations: () => request<BackupDestination[]>("GET", "/backup-destinations"),
  createBackupDestination: (input: BackupDestinationInput) => request<BackupDestination>("POST", "/backup-destinations", input),
  updateBackupDestination: (id: ID, input: BackupDestinationInput) => request<BackupDestination>("PATCH", `/backup-destinations/${enc(id)}`, input),
  deleteBackupDestination: (id: ID) => request<void>("DELETE", `/backup-destinations/${enc(id)}`),
  testBackupDestination: (id: ID) => request<{ ok: boolean; error?: string }>("POST", `/backup-destinations/${enc(id)}/test`),

  // shared variables
  sharedVariables: (eid: ID) => request<Record<string, string>>("GET", `/environments/${enc(eid)}/shared-variables`),
  setSharedVariables: (eid: ID, vars: Record<string, string>) =>
    request<void>("PUT", `/environments/${enc(eid)}/shared-variables`, vars),

  // templates
  templates: (q: TemplateListQuery = {}) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v) p.set(k, v);
    const qs = p.toString();
    return request<Template[]>("GET", `/templates${qs ? `?${qs}` : ""}`);
  },
  template: (key: string) => request<TemplateDetail>("GET", `/templates/${enc(key)}`),
  /** Bad inputs → ApiError 400 with `data: TemplateInputProblems`. */
  deployTemplate: (pid: ID, key: string, environment_id: ID, inputs?: Record<string, string>) =>
    request<Service[]>("POST", `/projects/${enc(pid)}/templates/${enc(key)}`, inputs ? { environment_id, inputs } : { environment_id }),
  createTemplate: (input: TemplateSaveInput) => request<TemplateDetail>("POST", "/templates", input),
  updateTemplate: (key: string, input: Partial<TemplateSaveInput>) => request<TemplateDetail>("PATCH", `/templates/${enc(key)}`, input),
  deleteTemplate: (key: string) => request<void>("DELETE", `/templates/${enc(key)}`),
  templateExportUrl: (key: string) => `/api/templates/${enc(key)}/export`,
  /** Generates (does not save) a definition from an environment. */
  exportEnvironmentTemplate: (pid: ID, eid: ID, secrets: "generate" | "inputs" = "generate") =>
    request<TemplateExport>("POST", `/projects/${enc(pid)}/environments/${enc(eid)}/export-template?secrets=${secrets}`),

  // events
  events: (pid: ID, environment_id?: ID, limit = 50) =>
    request<AvenEvent[]>(
      "GET",
      `/projects/${enc(pid)}/events?${environment_id ? `environment_id=${enc(environment_id)}&` : ""}limit=${limit}`,
    ),

  // nodes (v1.2)
  nodes: () => request<AvenNode[]>("GET", "/nodes"),
  createJoinToken: (input: { name?: string; ttl_s?: number }) => request<JoinToken>("POST", "/nodes/join-tokens", input),
  updateNode: (nid: ID, input: { name?: string; schedulable?: boolean; labels?: Record<string, string> } & NodeIngressInput) =>
    request<AvenNode>("PATCH", `/nodes/${enc(nid)}`, input),
  deleteNode: (nid: ID, force = false) => request<void>("DELETE", `/nodes/${enc(nid)}${force ? "?force=1" : ""}`),
  nodeMetrics: (nid: ID, range: MetricRange) => request<{ points: NodeMetricPoint[] }>("GET", `/nodes/${enc(nid)}/metrics?range=${range}`),

  // private registries (v1.2)
  registries: (pid: ID) => request<Registry[]>("GET", `/projects/${enc(pid)}/registries`),
  setRegistries: (pid: ID, list: RegistryInput[]) => request<Registry[]>("PUT", `/projects/${enc(pid)}/registries`, list),

  // system
  system: () => request<SystemStatus>("GET", "/system"),
  github: () => request<GithubStatus>("GET", "/system/github"),
  githubConnection: () => request<GithubConnection>("GET", "/github/connection"),
  githubDisconnect: () => request<void>("DELETE", "/github/connection"),
  githubRepos: (q = "") => request<GithubRepo[]>("GET", `/github/repos${q ? `?q=${encodeURIComponent(q)}` : ""}`),
  githubWebhook: (repo: string) => request<{ repo: string; hook_id: number; created: boolean; url: string }>("POST", "/github/webhooks", { repo }),
  testSmtp: (to?: string) => request<{ ok: boolean; to: string }>("POST", "/system/smtp/test", to ? { to } : {}),

  // notifications (v1.5). pid = null → workspace-wide channels (workspace admin).
  channels: (pid: ID | null) =>
    request<NotificationChannel[]>("GET", pid ? `/projects/${enc(pid)}/notification-channels` : "/notification-channels"),
  createChannel: (pid: ID | null, input: NotificationChannelInput) =>
    request<NotificationChannel>("POST", pid ? `/projects/${enc(pid)}/notification-channels` : "/notification-channels", input),
  updateChannel: (pid: ID | null, cid: ID, input: NotificationChannelInput) =>
    request<NotificationChannel>("PATCH", `${channelBase(pid)}/${enc(cid)}`, input),
  deleteChannel: (pid: ID | null, cid: ID) => request<void>("DELETE", `${channelBase(pid)}/${enc(cid)}`),
  testChannel: (pid: ID | null, cid: ID) => request<ChannelTestResult>("POST", `${channelBase(pid)}/${enc(cid)}/test`),
  channelDeliveries: (pid: ID | null, cid: ID) =>
    request<NotificationDelivery[]>("GET", `${channelBase(pid)}/${enc(cid)}/deliveries`),
  notifications: (unread = false, limit = 30) =>
    request<{ notifications: UserNotification[]; unread: number }>("GET", `/notifications?limit=${limit}${unread ? "&unread=1" : ""}`),
  readNotifications: (input: { ids: ID[] } | { all: true }) => request<{ updated: number }>("POST", "/notifications/read", input),
  notificationPrefs: () => request<NotificationPrefs>("GET", "/auth/notification-preferences"),
  setNotificationPrefs: (events: string[]) => request<NotificationPrefs>("PUT", "/auth/notification-preferences", { events }),

  // metric alert rules (v1.5)
  alertRules: (pid: ID) => request<AlertRule[]>("GET", `/projects/${enc(pid)}/alert-rules`),
  createAlertRule: (pid: ID, input: AlertRuleInput) => request<AlertRule>("POST", `/projects/${enc(pid)}/alert-rules`, input),
  updateAlertRule: (pid: ID, id: ID, input: AlertRuleInput) =>
    request<AlertRule>("PATCH", `/projects/${enc(pid)}/alert-rules/${enc(id)}`, input),
  deleteAlertRule: (pid: ID, id: ID) => request<void>("DELETE", `/projects/${enc(pid)}/alert-rules/${enc(id)}`),

  // usage (v1.5)
  projectUsage: (pid: ID, opts: { from?: string; to?: string; group?: UsageGroupBy } = {}) =>
    request<ProjectUsage>("GET", `/projects/${enc(pid)}/usage?${usageQuery(opts)}`),
  workspaceUsage: (opts: { from?: string; to?: string } = {}) => request<WorkspaceUsage>("GET", `/usage?${usageQuery(opts)}`),
};

function channelBase(pid: ID | null): string {
  return pid ? `/projects/${enc(pid)}/notification-channels` : "/notification-channels";
}

function usageQuery(opts: { from?: string; to?: string; group?: string }): string {
  const q = new URLSearchParams();
  if (opts.from) q.set("from", opts.from);
  if (opts.to) q.set("to", opts.to);
  if (opts.group) q.set("group", opts.group);
  return q.toString();
}

export const sse = {
  buildLogs: (did: ID) => `/api/deployments/${enc(did)}/build-logs`,
  deployLogs: (did: ID, tail = 500) => `/api/deployments/${enc(did)}/deploy-logs?tail=${tail}`,
  httpLogs: (sid: ID, eid: ID) => `/api${inst(sid, eid)}/http-logs`,
  serviceNetworkLogs: (sid: ID, eid: ID, kind: "dns" | "network", q = "", tail = 500) => {
    const p = new URLSearchParams({ tail: String(tail) });
    if (q.trim()) p.set("q", q.trim());
    return `/api${inst(sid, eid)}/${kind}-logs?${p.toString()}`;
  },
  envNetworkLogs: (eid: ID, services: ID[], kind: "dns" | "network", q: string, tail = 500) => {
    const p = new URLSearchParams({ tail: String(tail) });
    if (services.length) p.set("services", services.join(","));
    if (q.trim()) p.set("q", q.trim());
    return `/api/environments/${enc(eid)}/${kind}-logs?${p.toString()}`;
  },
  envLogs: (eid: ID, services: ID[], q: string, level?: "error") => {
    const p = new URLSearchParams();
    if (services.length) p.set("services", services.join(","));
    if (q.trim()) p.set("q", q.trim());
    if (level) p.set("level", level);
    const qs = p.toString();
    return `/api/environments/${enc(eid)}/logs${qs ? `?${qs}` : ""}`;
  },
  events: (pid: ID) => `/api/projects/${enc(pid)}/events/stream`,
  /** v1.5: new inbox items of the current user (`notification`, `revoked`). */
  notifications: () => `/api/notifications/stream`,
};
