export type FlagType = "bool" | "string" | "number" | "json";
export type EvaluationReason = "TARGETING_MATCH" | "SPLIT" | "NO_MATCH" | "CONFLICT" | "NOT_FOUND" | "TYPE_MISMATCH";
export type Context = Record<string, string | number | boolean | null | undefined | unknown[]>;
export type Evaluation<T> = { value: T; reason: EvaluationReason; trace: { rule_id: string; when: string; matched: boolean }[] };

export type FlagRule = { id: string; when: string; value: unknown };
export type FlagDefinition = {
  id: string;
  project_id: string;
  name: string;
  type: FlagType;
  environment_id: string;
  default_value: unknown;
  rules: FlagRule[];
  configured: boolean;
};
export type FlagSnapshot = { project_id: string; environment_id: string; flags: FlagDefinition[] };

export type FlagClientOptions = {
  /** Aven server URL. Defaults to AVEN_URL (KEEL_URL still works). */
  baseUrl?: string;
  /** API token. Defaults to AVEN_TOKEN (project token), then AVEN_API_TOKEN (account token). KEEL_TOKEN and KEEL_API_TOKEN still work. */
  token?: string;
  /** "project-token" infers the project from the token; "account-token" needs scope.projectId or AVEN_PROJECT_ID (KEEL_PROJECT_ID still works). */
  authType?: "project-token" | "account-token";
  /** Project and environment to read. environmentId defaults to AVEN_ENVIRONMENT_ID (KEEL_ENVIRONMENT_ID still works), then the project's default environment. */
  scope?: { projectId?: string; environmentId?: string };
  /** Background refresh interval (default 30s, minimum 1s). 0 disables polling. */
  pollIntervalMs?: number;
  /** Called when a background refresh fails; the last good snapshot keeps serving reads. */
  onError?: (error: unknown) => void;
  /** Custom fetch (tests, proxies). Defaults to globalThis.fetch. */
  fetch?: typeof fetch;
};

type Expr =
  | { kind: "literal"; value: unknown }
  | { kind: "identifier"; name: string }
  | { kind: "bucket"; name: string }
  | { kind: "not"; left: Expr }
  | { kind: "binary"; op: string; left: Expr; right: Expr }
  | { kind: "list"; items: Expr[] };
type Token = { kind: "value" | "identifier" | "operator" | "punct" | "eof"; text: string; value?: unknown };
const MISSING = Symbol("missing");

class ExpressionParser {
  private readonly tokens: Token[];
  private index = 0;

  constructor(source: string) {
    if (!source.trim() || source.length > 2048) throw new Error("invalid targeting expression length");
    this.tokens = tokenize(source);
  }

  parse(): Expr {
    const result = this.parseOr();
    if (this.peek().kind !== "eof") throw new Error(`unexpected token ${this.peek().text}`);
    return result;
  }

  private peek() { return this.tokens[this.index]; }
  private take(text: string) {
    if (this.peek().text !== text) return false;
    this.index++;
    return true;
  }

  private parseOr(): Expr {
    let left = this.parseAnd();
    while (this.take("||")) left = { kind: "binary", op: "||", left, right: this.parseAnd() };
    return left;
  }

  private parseAnd(): Expr {
    let left = this.parseUnary();
    while (this.take("&&")) left = { kind: "binary", op: "&&", left, right: this.parseUnary() };
    return left;
  }

  private parseUnary(): Expr {
    if (this.take("!")) return { kind: "not", left: this.parseUnary() };
    return this.parseCompare();
  }

  private parseCompare(): Expr {
    const left = this.parsePrimary();
    if (this.peek().kind === "operator" && ["==", "!=", "<", "<=", ">", ">=", "in", "contains", "matches"].includes(this.peek().text)) {
      const op = this.peek().text;
      this.index++;
      return { kind: "binary", op, left, right: this.parsePrimary() };
    }
    return left;
  }

  private parsePrimary(): Expr {
    const token = this.peek();
    if (token.kind === "value") {
      this.index++;
      return { kind: "literal", value: token.value };
    }
    if (token.kind === "identifier") {
      this.index++;
      if (token.text === "bucket" && this.take("(")) {
        const attribute = this.peek();
        if (attribute.kind !== "identifier") throw new Error("bucket() expects an attribute name");
        this.index++;
        if (!this.take(")")) throw new Error("bucket() expects a closing parenthesis");
        return { kind: "bucket", name: attribute.text };
      }
      return { kind: "identifier", name: token.text };
    }
    if (token.text === "(") {
      this.index++;
      const result = this.parseOr();
      if (!this.take(")")) throw new Error("missing closing parenthesis");
      return result;
    }
    if (token.text === "[") {
      this.index++;
      const items: Expr[] = [];
      if (this.take("]")) return { kind: "list", items };
      while (true) {
        items.push(this.parsePrimary());
        if (this.take("]")) break;
        if (!this.take(",")) throw new Error("expected ',' or ']' in list");
      }
      return { kind: "list", items };
    }
    throw new Error(`expected a value near ${token.text}`);
  }
}

function tokenize(source: string): Token[] {
  const tokens: Token[] = [];
  for (let i = 0; i < source.length;) {
    const c = source[i];
    if (/\s/.test(c)) { i++; continue; }
    if (c === '"') {
      const start = i++;
      let escaped = false;
      while (i < source.length) {
        const x = source[i++];
        if (escaped) escaped = false;
        else if (x === "\\") escaped = true;
        else if (x === '"') break;
      }
      if (source[i - 1] !== '"') throw new Error("unterminated string");
      const text = source.slice(start, i);
      tokens.push({ kind: "value", text, value: JSON.parse(text) });
      continue;
    }
    const number = source.slice(i).match(/^-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/);
    if (number) {
      tokens.push({ kind: "value", text: number[0], value: Number(number[0]) });
      i += number[0].length;
      continue;
    }
    const identifier = source.slice(i).match(/^[A-Za-z_][A-Za-z0-9_]*/);
    if (identifier) {
      const text = identifier[0];
      i += text.length;
      if (text === "true" || text === "false" || text === "null") {
        tokens.push({ kind: "value", text, value: text === "null" ? null : text === "true" });
      } else if (["in", "contains", "matches"].includes(text)) {
        tokens.push({ kind: "operator", text });
      } else {
        tokens.push({ kind: "identifier", text });
      }
      continue;
    }
    const op = ["&&", "||", "==", "!=", "<=", ">="].find((candidate) => source.startsWith(candidate, i));
    if (op) {
      tokens.push({ kind: "operator", text: op });
      i += op.length;
      continue;
    }
    if (["!", "<", ">"].includes(c)) {
      tokens.push({ kind: "operator", text: c });
      i++;
      continue;
    }
    if (["(", ")", "[", "]", ","].includes(c)) {
      tokens.push({ kind: "punct", text: c });
      i++;
      continue;
    }
    throw new Error(`unexpected character ${c}`);
  }
  tokens.push({ kind: "eof", text: "" });
  return tokens;
}

export function stableBucket(flagName: string, key: string): number {
  let hash = 14695981039346656037n;
  const prime = 1099511628211n;
  const mask = (1n << 64n) - 1n;
  for (const byte of new TextEncoder().encode(`${flagName}\0${key}`)) {
    hash ^= BigInt(byte);
    hash = (hash * prime) & mask;
  }
  // murmur3 fmix64, same as the server (internal/featureflags.StableBucket).
  hash ^= hash >> 33n;
  hash = (hash * 0xff51afd7ed558ccdn) & mask;
  hash ^= hash >> 33n;
  hash = (hash * 0xc4ceb9fe1a85ec53n) & mask;
  hash ^= hash >> 33n;
  return Number(hash >> 11n) / 9007199254740992;
}

function scalarKey(value: unknown): string | null {
  if (typeof value === "string") return value;
  if (typeof value === "boolean") return String(value);
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  return null;
}

function equal(a: unknown, b: unknown): boolean {
  if (typeof a === "number" && typeof b === "number") return a === b;
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => equal(x, b[i]));
  if (a && b && typeof a === "object" && typeof b === "object") {
    const ak = Object.keys(a).sort();
    const bk = Object.keys(b).sort();
    return ak.length === bk.length && ak.every((key, i) => key === bk[i] && equal((a as Record<string, unknown>)[key], (b as Record<string, unknown>)[key]));
  }
  return false;
}

function compare(a: unknown, op: string, b: unknown): boolean {
  if (op === "==") return equal(a, b);
  if (op === "!=") return !equal(a, b);
  if (op === "in") return Array.isArray(b) && b.some((item) => equal(a, item));
  if (op === "contains") {
    if (typeof a === "string" && typeof b === "string") return a.includes(b);
    if (Array.isArray(a)) return a.some((item) => equal(item, b));
    return false;
  }
  if (op === "matches") {
    if (typeof a !== "string" || typeof b !== "string") return false;
    try { return new RegExp(b).test(a); } catch { return false; }
  }
  if (typeof a === "number" && typeof b === "number") {
    if (op === "<") return a < b;
    if (op === "<=") return a <= b;
    if (op === ">") return a > b;
    if (op === ">=") return a >= b;
  }
  if (typeof a === "string" && typeof b === "string") {
    if (op === "<") return a < b;
    if (op === "<=") return a <= b;
    if (op === ">") return a > b;
    if (op === ">=") return a >= b;
  }
  return false;
}

const parsed = new Map<string, Expr>();
function parseCached(source: string): Expr {
  let expr = parsed.get(source);
  if (!expr) {
    expr = new ExpressionParser(source).parse();
    if (parsed.size > 1000) parsed.clear();
    parsed.set(source, expr);
  }
  return expr;
}

function evalExpr(expr: Expr, flagName: string, context: Context): unknown | typeof MISSING {
  if (expr.kind === "literal") return expr.value;
  if (expr.kind === "identifier") return context[expr.name] == null ? MISSING : context[expr.name];
  if (expr.kind === "bucket") {
    const key = scalarKey(context[expr.name]);
    return key == null ? MISSING : stableBucket(flagName, key);
  }
  if (expr.kind === "list") {
    const values = expr.items.map((item) => evalExpr(item, flagName, context));
    return values.some((value) => value === MISSING) ? MISSING : values;
  }
  if (expr.kind === "not") {
    const value = evalExpr(expr.left, flagName, context);
    return value === MISSING || typeof value !== "boolean" ? MISSING : !value;
  }
  const left = evalExpr(expr.left, flagName, context);
  if (expr.op === "&&" || expr.op === "||") {
    if (left !== MISSING && typeof left !== "boolean") return MISSING;
    if (expr.op === "&&" && (left === false || left === MISSING)) return false;
    if (expr.op === "||" && left === true) return true;
    const right = evalExpr(expr.right, flagName, context);
    if (right !== MISSING && typeof right !== "boolean") return MISSING;
    if (expr.op === "&&") return left === true && right === true;
    return left === true || right === true;
  }
  const right = evalExpr(expr.right, flagName, context);
  // A comparison on a missing attribute is itself missing, so !(missing == x) does not match either (same as the server).
  return left === MISSING || right === MISSING ? MISSING : compare(left, expr.op, right);
}

export function evaluateFlag(flag: FlagDefinition, context: Context): Evaluation<unknown> {
  let selected: unknown;
  let count = 0;
  let conflict = false;
  let split = false;
  const trace = flag.rules.map((rule) => {
    let matched = false;
    try { matched = evalExpr(parseCached(rule.when), flag.name, context) === true; } catch { /* malformed cached rule fails closed */ }
    if (matched) {
      if (count === 0) selected = rule.value;
      else if (!equal(selected, rule.value)) conflict = true;
      count++;
      if (rule.when.includes("bucket(")) split = true;
    }
    return { rule_id: rule.id, when: rule.when, matched };
  });
  if (count > 0 && !conflict) return { value: selected, reason: split ? "SPLIT" : "TARGETING_MATCH", trace };
  return { value: flag.default_value, reason: conflict ? "CONFLICT" : "NO_MATCH", trace };
}

const warnedEnv = new Set<string>();

function environmentValue(suffix: string): string | undefined {
  const root = globalThis as typeof globalThis & { process?: { env?: Record<string, string | undefined> } };
  const env = root.process?.env;
  const next = env?.[`AVEN_${suffix}`];
  if (next) return next;
  const old = env?.[`KEEL_${suffix}`];
  if (old) {
    if (!warnedEnv.has(suffix)) {
      warnedEnv.add(suffix);
      console.warn(`KEEL_${suffix} is set; use AVEN_${suffix} instead (the old name still works)`);
    }
    return old;
  }
  return undefined;
}

/**
 * Keeps an in-memory copy of one environment's flags, refreshed in the background, so reads are synchronous.
 * Rules are evaluated locally with the same semantics (and the same bucket hash) as the server.
 */
export class FlagClient {
  private defaults: FlagClientOptions;
  private flags = new Map<string, FlagDefinition>();
  private projectID: string | null = null;
  private environmentID: string | null = null;
  private etag: string | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private closed = false;
  private url = "";
  private token = "";
  private fetcher: typeof fetch = (...args) => globalThis.fetch(...args);
  private onError: (error: unknown) => void = () => undefined;

  constructor(options: FlagClientOptions = {}) {
    this.defaults = options;
  }

  /** Loads the first snapshot (throws if it fails) and starts polling. Safe to call again with new options. */
  async init(options: FlagClientOptions = {}): Promise<void> {
    const o: FlagClientOptions = { ...this.defaults, ...options, scope: { ...this.defaults.scope, ...options.scope } };
    const baseUrl = o.baseUrl ?? environmentValue("URL");
    if (!baseUrl) throw new Error("aven flags: baseUrl or AVEN_URL is required (KEEL_URL still works)");
    let token = o.token;
    let authType = o.authType;
    if (!token && environmentValue("TOKEN")) {
      token = environmentValue("TOKEN");
      authType ??= "project-token";
    } else if (!token && environmentValue("API_TOKEN")) {
      token = environmentValue("API_TOKEN");
      authType ??= "account-token";
    }
    if (!token) throw new Error("aven flags: token, AVEN_TOKEN or AVEN_API_TOKEN is required (KEEL_TOKEN and KEEL_API_TOKEN still work)");
    authType ??= o.scope?.projectId ? "account-token" : "project-token";
    const environmentId = o.scope?.environmentId ?? environmentValue("ENVIRONMENT_ID");
    const query = environmentId ? `?environment_id=${encodeURIComponent(environmentId)}` : "";
    const base = baseUrl.replace(/\/+$/, "");
    if (authType === "account-token") {
      const projectId = o.scope?.projectId ?? environmentValue("PROJECT_ID");
      if (!projectId) throw new Error("aven flags: an account token needs scope.projectId or AVEN_PROJECT_ID (KEEL_PROJECT_ID still works)");
      this.url = `${base}/api/projects/${encodeURIComponent(projectId)}/flags${query}`;
    } else {
      this.url = `${base}/api/feature-flags${query}`;
    }
    this.token = token;
    if (o.fetch) this.fetcher = o.fetch;
    this.onError = o.onError ?? (() => undefined);
    this.close();
    this.closed = false;
    this.etag = null;
    await this.refresh();
    const interval = o.pollIntervalMs ?? 30_000;
    if (interval > 0) this.schedule(Math.max(1000, interval));
  }

  /** Stops background polling. Cached flags keep serving reads. */
  close(): void {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
  }

  /** True once a snapshot has loaded. */
  get ready(): boolean { return this.projectID !== null; }
  get projectId(): string | null { return this.projectID; }
  get environmentId(): string | null { return this.environmentID; }
  /** The cached flag definitions. */
  list(): FlagDefinition[] { return [...this.flags.values()]; }

  private schedule(interval: number) {
    if (this.closed) return;
    this.timer = setTimeout(async () => {
      try { await this.refresh(); } catch (error) { this.onError(error); }
      this.schedule(interval);
    }, interval);
    // Polling must not keep a Node/Bun process alive.
    (this.timer as { unref?: () => void }).unref?.();
  }

  /** Fetches the latest snapshot now (uses If-None-Match, so unchanged snapshots cost a 304). */
  async refresh(): Promise<void> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}`, Accept: "application/json" };
    if (this.etag) headers["If-None-Match"] = this.etag;
    const response = await this.fetcher(this.url, { headers });
    if (response.status === 304) return;
    if (!response.ok) throw new Error(`aven flags: refresh failed (${response.status}): ${await response.text()}`);
    const snapshot = await response.json() as FlagSnapshot;
    this.etag = response.headers.get("ETag");
    this.projectID = snapshot.project_id;
    this.environmentID = snapshot.environment_id;
    this.flags = new Map(snapshot.flags.map((flag) => [flag.name, flag]));
  }

  evaluateBoolean(name: string, context: Context = {}, fallback = false): Evaluation<boolean> {
    return this.evaluateTyped(name, "bool", context, fallback, (value): value is boolean => typeof value === "boolean");
  }
  evaluateString(name: string, context: Context = {}, fallback = ""): Evaluation<string> {
    return this.evaluateTyped(name, "string", context, fallback, (value): value is string => typeof value === "string");
  }
  evaluateNumber(name: string, context: Context = {}, fallback = 0): Evaluation<number> {
    return this.evaluateTyped(name, "number", context, fallback, (value): value is number => typeof value === "number" && Number.isFinite(value));
  }
  evaluateJson<T = unknown>(name: string, context: Context = {}, fallback?: T): Evaluation<T> {
    return this.evaluateTyped(name, "json", context, fallback as T, (value): value is T => value !== undefined);
  }

  getBoolean(name: string, context: Context = {}, fallback = false): boolean { return this.evaluateBoolean(name, context, fallback).value; }
  getString(name: string, context: Context = {}, fallback = ""): string { return this.evaluateString(name, context, fallback).value; }
  getNumber(name: string, context: Context = {}, fallback = 0): number { return this.evaluateNumber(name, context, fallback).value; }
  getJson<T = unknown>(name: string, context: Context = {}, fallback?: T): T { return this.evaluateJson<T>(name, context, fallback).value; }

  private evaluateTyped<T>(name: string, type: FlagType, context: Context, fallback: T, accepts: (value: unknown) => value is T): Evaluation<T> {
    const flag = this.flags.get(name);
    if (!flag) return { value: fallback, reason: "NOT_FOUND", trace: [] };
    if (flag.type !== type) return { value: fallback, reason: "TYPE_MISMATCH", trace: [] };
    const result = evaluateFlag(flag, withTargetingKey(context));
    if (!accepts(result.value)) return { value: fallback, reason: "TYPE_MISMATCH", trace: result.trace };
    return { value: result.value, reason: result.reason, trace: result.trace };
  }
}

/** `key` is the conventional stable identity; it is also exposed as `targetingKey` for rules. */
function withTargetingKey(context: Context): Context {
  if (context.key == null || context.targetingKey != null) return context;
  return { ...context, targetingKey: context.key };
}

export function createFlagClient(options: FlagClientOptions = {}): FlagClient {
  return new FlagClient(options);
}

/** Default singleton. Set AVEN_URL and a project AVEN_TOKEN on the service (KEEL_URL and KEEL_TOKEN still work), then `await flags.init()`. */
export const flags = new FlagClient();
