// Types for the Aven Infrastructure as Code DSL (the module filename is retained for compatibility).

export type Preserved = { preserve: true };
export type EnvValue = string | number | boolean | Preserved;
export type Source = { readonly type: "repo" | "image"; readonly settings: Record<string, unknown> };
export type DomainInput = string | { domain: string; port?: number } | { host: string; [key: string]: unknown };

export interface ServiceConfig {
  source?: Source;
  /** start_command */
  start?: string | null;
  /** pre_deploy_command */
  preDeploy?: string | null;
  /** healthcheck_path */
  healthcheck?: string;
  healthcheckType?: "http" | "tcp" | "none";
  /** healthcheck_timeout_s */
  healthcheckTimeout?: number;
  replicas?: number;
  placement?: { node_id: string; replicas: number }[];
  port?: number | null;
  builder?: "auto" | "dockerfile" | "railpack";
  dockerfilePath?: string;
  watchPaths?: string[];
  autoDeploy?: boolean;
  restartPolicy?: "always" | "on_failure" | "never";
  restartMaxRetries?: number;
  /** cron_schedule */
  cron?: string | null;
  sleepWhenIdle?: boolean;
  /** sleep_after_s */
  sleepAfter?: number;
  memoryLimitMB?: number | null;
  cpuLimit?: number | null;
  buildSecrets?: string[];
  waitForCI?: boolean;
  overlapSeconds?: number;
  drainingSeconds?: number | null;
  privatePorts?: number[];
  autoscale?: Record<string, unknown> | null;
  /** Custom domains. Generated domains are managed by Aven and never listed. */
  domains?: DomainInput[];
  /** Exactly these variables (omitting a key removes it). Omit `env` to leave variables untouched. */
  env?: Record<string, EnvValue>;
  /** Raw Aven settings (store.Spec JSON names), applied last. */
  settings?: Record<string, unknown>;
}

export interface Service {
  readonly name: string;
  /** References: `db.env.DATABASE_URL` is "${{db.DATABASE_URL}}". */
  readonly env: Record<string, string>;
}
export interface Group {
  readonly name: string;
  readonly resources: Resource[];
}
export type Resource = Service | Group;

export interface AvenContext {
  command: "plan" | "apply" | "import";
  projectId: string;
  projectName: string;
  environmentId: string;
  environment: string;
  environmentName: string;
  isEnvironment(name: string): boolean;
  /** `ctx.shared.NAME` is "${{shared.NAME}}" (an existing shared variable). */
  shared: Record<string, string>;
}

export interface ProjectDefinition {
  version: 1;
  project_name: string;
  services: unknown[];
}

export declare const FIELDS: Readonly<Record<string, string>>;
export declare function preserve(): Preserved;
export declare function github(repo: string, opts?: { branch?: string; rootDirectory?: string }): Source;
export declare const git: typeof github;
export declare function image(ref?: string): Source;
export declare function service(name: string, cfg?: ServiceConfig): Service;
export declare function database(kind: "postgres" | "mysql" | "redis" | "mongo" | "bucket", name?: string, cfg?: ServiceConfig): Service;
export declare function postgres(name?: string, cfg?: ServiceConfig): Service;
export declare function mysql(name?: string, cfg?: ServiceConfig): Service;
export declare function redis(name?: string, cfg?: ServiceConfig): Service;
export declare function mongo(name?: string, cfg?: ServiceConfig): Service;
export declare function bucket(name?: string, cfg?: ServiceConfig): Service;
export declare function functionService(name: string, cfg?: ServiceConfig): Service;
export declare function group(name: string, resources: Resource[]): Group;
export declare function project(name: string, opts?: { resources?: Resource[] }): ProjectDefinition;
/** @deprecated Use AvenContext. */
export type KeelContext = AvenContext;
export declare function defineAven(
  fn: (ctx: AvenContext) => ProjectDefinition | Promise<ProjectDefinition>,
): (ctx: AvenContext) => ProjectDefinition | Promise<ProjectDefinition>;
/** @deprecated Use defineAven. */
export declare const defineKeel: typeof defineAven;
