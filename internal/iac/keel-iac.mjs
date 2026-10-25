// Aven Infrastructure as Code DSL (written by `aven config import`; commit it next to aven.ts).
// `aven config plan|apply` evaluates .keel/aven.ts with bun or node and sends the JSON it returns.


const KIND = Symbol.for("keel.iac.kind");

// Friendly option → Aven setting (store.Spec JSON name). Anything else goes in `settings`.
export const FIELDS = Object.freeze({
  start: "start_command",
  preDeploy: "pre_deploy_command",
  healthcheck: "healthcheck_path",
  healthcheckType: "healthcheck_type",
  healthcheckTimeout: "healthcheck_timeout_s",
  replicas: "replicas",
  placement: "placement",
  port: "port",
  builder: "builder",
  dockerfilePath: "dockerfile_path",
  watchPaths: "watch_paths",
  autoDeploy: "auto_deploy",
  restartPolicy: "restart_policy",
  restartMaxRetries: "restart_max_retries",
  cron: "cron_schedule",
  sleepWhenIdle: "sleep_when_idle",
  sleepAfter: "sleep_after_s",
  memoryLimitMB: "memory_limit_mb",
  cpuLimit: "cpu_limit",
  buildSecrets: "build_secrets",
  waitForCI: "wait_for_ci",
  overlapSeconds: "overlap_seconds",
  drainingSeconds: "draining_seconds",
  privatePorts: "private_ports",
  autoscale: "autoscale",
});

const OPTIONS = new Set([...Object.keys(FIELDS), "source", "domains", "env", "settings"]);

/** Keep the value already stored in Aven (imported secrets, generated passwords). */
export function preserve() {
  return { preserve: true };
}

function sourceOf(type, settings) {
  return { [KIND]: "source", type, settings };
}

/** A git repository. "owner/repo" is shorthand for https://github.com/owner/repo. */
export function github(repo, opts = {}) {
  const url = /^[\w.-]+\/[\w.-]+$/.test(repo) ? `https://github.com/${repo}` : repo;
  const settings = { repo_url: url };
  if (opts.branch !== undefined) settings.branch = opts.branch;
  if (opts.rootDirectory !== undefined) settings.root_dir = opts.rootDirectory;
  return sourceOf("repo", settings);
}

/** Any git URL (https://, ssh://, git@). */
export const git = github;

/** A Docker image. Omit the reference for an image service whose image is set later. */
export function image(ref) {
  return sourceOf("image", ref === undefined ? {} : { image: ref });
}

function envProxy(name) {
  return new Proxy({}, { get: (_, key) => (typeof key === "string" ? `\${{${name}.${key}}}` : undefined) });
}

function resource(name, cfg, sourceType, databaseKind) {
  if (typeof name !== "string" || name === "") throw new Error("a service needs a name");
  for (const key of Object.keys(cfg ?? {})) {
    if (!OPTIONS.has(key)) throw new Error(`service "${name}": unknown option "${key}" (use settings: { ... } for raw Aven settings)`);
  }
  return { [KIND]: "service", name, cfg: cfg ?? {}, sourceType, databaseKind, env: envProxy(name) };
}

/** A service. `x.env.KEY` is a reference (${{x.KEY}}) for other services' env. */
export function service(name, cfg) {
  return resource(name, cfg);
}

/** A database or bucket from the Aven catalog: postgres, mysql, redis, mongo, bucket. */
export function database(kind, name = kind, cfg) {
  return resource(name, cfg, "database", kind);
}
export const postgres = (name = "postgres", cfg) => database("postgres", name, cfg);
export const mysql = (name = "mysql", cfg) => database("mysql", name, cfg);
export const redis = (name = "redis", cfg) => database("redis", name, cfg);
export const mongo = (name = "mongo", cfg) => database("mongo", name, cfg);
export const bucket = (name = "bucket", cfg) => database("bucket", name, cfg);

/** A single-file Bun function service (its code is edited in the dashboard). */
export function functionService(name, cfg) {
  return resource(name, cfg, "function");
}

/** Groups are structural only: they are flattened into the project's services. */
export function group(name, resources) {
  return { [KIND]: "group", name, resources };
}

function compileService(s) {
  const c = s.cfg;
  const out = { name: s.name };
  const settings = {};
  let sourceType = s.sourceType;
  if (c.source !== undefined) {
    if (!c.source || c.source[KIND] !== "source") throw new Error(`service "${s.name}": source must be github(...), git(...) or image(...)`);
    sourceType = c.source.type;
    Object.assign(settings, c.source.settings);
  }
  if (sourceType) out.source_type = sourceType;
  if (s.databaseKind) out.database_kind = s.databaseKind;
  for (const [key, field] of Object.entries(FIELDS)) {
    if (c[key] !== undefined) settings[field] = c[key];
  }
  if (c.domains !== undefined) {
    settings.domains = c.domains.map((d) => {
      if (typeof d === "string") return { host: d };
      if (d && typeof d.domain === "string") {
        const out = { host: d.domain };
        if (d.port !== undefined) out.target_port = d.port;
        return out;
      }
      return d;
    });
  }
  if (c.settings !== undefined) Object.assign(settings, c.settings);
  if (Object.keys(settings).length > 0) out.settings = settings;
  if (c.env !== undefined) {
    out.variables = {};
    for (const [key, value] of Object.entries(c.env)) {
      if (value === undefined) continue;
      out.variables[key] = value && typeof value === "object" && value.preserve === true ? { preserve: true } : String(value);
    }
  }
  return out;
}

/** The project definition. `resources` lists services, databases and groups. */
export function project(name, { resources = [] } = {}) {
  const seen = new Set();
  const services = [];
  const walk = (list) => {
    for (const r of list) {
      if (!r || seen.has(r)) continue;
      seen.add(r);
      if (r[KIND] === "group") walk(r.resources ?? []);
      else if (r[KIND] === "service") services.push(compileService(r));
      else throw new Error("project resources must be services, databases or groups");
    }
  };
  walk(resources);
  return { version: 1, project_name: name, services };
}

/** Wraps the definition function. It receives the environment context from the CLI. */
export function defineAven(fn) {
  return fn;
}

// Keep imports generated by earlier CLI versions working.
export const defineKeel = defineAven;
