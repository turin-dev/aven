// Aven sandbox SDK: dependency-free TypeScript client for /api/.../sandboxes (docs/api-v1.md#sandboxes).

export type NetworkMode = "isolated" | "private";

export type SandboxInfo = {
  id: string;
  project_id: string;
  environment_id: string;
  node_id: string;
  container_name: string;
  network_mode: NetworkMode;
  image: string;
  status: "creating" | "running";
  memory_mb: number;
  cpu_cores: number;
  /** 0 = never destroyed for being idle. */
  idle_timeout_minutes: number;
  expires_at: string | null;
  last_activity_at: string;
  parent_id: string | null;
  checkpoint_name: string | null;
  created_at: string;
};

export type SandboxCheckpoint = {
  id: string;
  project_id: string;
  environment_id: string;
  node_id: string;
  name: string;
  created_at: string;
};

export type ExecResult = {
  /** -1 when the command timed out or the exit code is unknown. */
  exitCode: number;
  stdout: string;
  stderr: string;
  stdoutBytes: Uint8Array;
  stderrBytes: Uint8Array;
  truncated: boolean;
  timedOut: boolean;
};

export type FileEntry = { name: string; path: string; type: "file" | "dir" | "symlink"; size: number; mod_time: string };

export type ClientOptions = {
  /** Aven server URL (default: KEEL_URL). */
  url?: string;
  /** API token (default: KEEL_TOKEN). */
  token?: string;
  /** Environment ID (default: KEEL_ENVIRONMENT_ID). */
  environmentId?: string;
};

export type SandboxOptions = ClientOptions & {
  /** Minutes without activity before the sandbox is destroyed. 0 disables it. Default 30. */
  idleTimeoutMinutes?: number;
  resources?: { cpu?: number; memoryMB?: number };
  networkIsolation?: "ISOLATED" | "PRIVATE";
  /** Variables available to every command for the sandbox's lifetime. */
  env?: Record<string, string>;
};

export type CreateOptions = SandboxOptions & {
  /** Container image (needs /bin/sh). Default ubuntu:24.04. */
  image?: string;
  /** Node ID. Default: the server's own node. */
  nodeId?: string;
};

export type ExecOptions = {
  cwd?: string;
  env?: Record<string, string>;
  stdin?: string | Uint8Array;
  /** Seconds, 1..110 (default 110). The command gets SIGTERM at the deadline. */
  timeoutSec?: number;
  /** Cap on captured stdout bytes (max 2 MiB). */
  maxOutputBytes?: number;
};

export class AvenError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "AvenError";
  }
}

export class SandboxNotFoundError extends AvenError {
  constructor(message: string) {
    super(message, 404);
    this.name = "SandboxNotFoundError";
  }
}

type Env = Record<string, string | undefined>;
const processEnv = (): Env => (globalThis as { process?: { env?: Env } }).process?.env ?? {};

export class AvenClient {
  readonly url: string;
  readonly environmentId: string;
  private readonly token: string;

  constructor(options: ClientOptions = {}) {
    const env = processEnv();
    const url = options.url ?? env.KEEL_URL;
    const token = options.token ?? env.KEEL_TOKEN;
    if (!url) throw new Error("Aven URL is required (options.url or KEEL_URL)");
    if (!token) throw new Error("Aven API token is required (options.token or KEEL_TOKEN)");
    this.url = url.replace(/\/+$/, "");
    this.token = token;
    this.environmentId = options.environmentId ?? env.KEEL_ENVIRONMENT_ID ?? "";
  }

  requireEnvironment(): string {
    if (!this.environmentId) throw new Error("environment ID is required (options.environmentId or KEEL_ENVIRONMENT_ID)");
    return this.environmentId;
  }

  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await fetch(`${this.url}/api${path}`, {
      method,
      headers: { Authorization: `Bearer ${this.token}`, ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!res.ok) {
      let message = `${res.status} ${res.statusText}`;
      try {
        message = ((await res.json()) as { error?: string }).error ?? message;
      } catch {
        // not JSON: keep the HTTP status
      }
      throw res.status === 404 ? new SandboxNotFoundError(message) : new AvenError(message, res.status);
    }
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }
}

const enc = (s: string) => encodeURIComponent(s);
const textDecoder = new TextDecoder();

function toBytes(v: string | Uint8Array): Uint8Array {
  return typeof v === "string" ? new TextEncoder().encode(v) : v;
}

function b64encode(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

function b64decode(value: string | null | undefined): Uint8Array {
  if (!value) return new Uint8Array();
  return Uint8Array.from(atob(value), (c) => c.charCodeAt(0));
}

function optionsBody(o: SandboxOptions): Record<string, unknown> {
  return {
    idle_timeout_minutes: o.idleTimeoutMinutes,
    memory_mb: o.resources?.memoryMB,
    cpu_cores: o.resources?.cpu,
    network: o.networkIsolation?.toLowerCase(),
    variables: o.env,
  };
}

/** Files on a running sandbox (`sandbox.files`). */
export class SandboxFiles {
  constructor(private readonly sandbox: Sandbox) {}

  read(path: string): Promise<string>;
  read(path: string, options: { format: "bytes" }): Promise<Uint8Array>;
  async read(path: string, options?: { format: "text" | "bytes" }): Promise<string | Uint8Array> {
    const q = new URLSearchParams({ path });
    const r = await this.sandbox.client.request<{ content_base64: string }>("GET", `/sandboxes/${enc(this.sandbox.id)}/files?${q}`);
    const bytes = b64decode(r.content_base64);
    return options?.format === "bytes" ? bytes : textDecoder.decode(bytes);
  }

  /** Creates or overwrites a file (max 256 KiB), creating parent directories. Mode defaults to 0o644. */
  async write(path: string, content: string | Uint8Array, options: { mode?: number } = {}): Promise<void> {
    await this.sandbox.client.request("PUT", `/sandboxes/${enc(this.sandbox.id)}/files`, {
      path,
      content_base64: b64encode(toBytes(content)),
      mode: options.mode ?? 0o644,
    });
  }

  async list(path: string): Promise<FileEntry[]> {
    const q = new URLSearchParams({ path });
    const r = await this.sandbox.client.request<{ entries: FileEntry[] }>("GET", `/sandboxes/${enc(this.sandbox.id)}/files/list?${q}`);
    return r.entries;
  }
}

export class Sandbox {
  readonly files: SandboxFiles;

  private constructor(
    readonly client: AvenClient,
    private info: SandboxInfo,
  ) {
    this.files = new SandboxFiles(this);
  }

  get id() {
    return this.info.id;
  }
  get status() {
    return this.info.status;
  }
  get networkIsolation(): "ISOLATED" | "PRIVATE" {
    return this.info.network_mode === "private" ? "PRIVATE" : "ISOLATED";
  }
  get expiresAt(): Date | null {
    return this.info.expires_at ? new Date(this.info.expires_at) : null;
  }
  get raw(): SandboxInfo {
    return this.info;
  }

  /**
   * Sandbox.create() → new sandbox; Sandbox.create("name") → from a checkpoint; Sandbox.create(source) → fork.
   */
  static async create(from?: CreateOptions | string | Sandbox, options: CreateOptions = {}): Promise<Sandbox> {
    if (from instanceof Sandbox) return from.fork(options);
    const opts = typeof from === "object" ? from : options;
    const client = new AvenClient(opts);
    const body = { ...optionsBody(opts), image: opts.image, node_id: opts.nodeId, checkpoint: typeof from === "string" ? from : undefined };
    const info = await client.request<SandboxInfo>("POST", `/environments/${enc(client.requireEnvironment())}/sandboxes`, body);
    return new Sandbox(client, info);
  }

  /** Reattaches to an existing sandbox. Throws SandboxNotFoundError if it is gone. */
  static async connect(id: string, options: ClientOptions = {}): Promise<Sandbox> {
    const client = new AvenClient(options);
    return new Sandbox(client, await client.request<SandboxInfo>("GET", `/sandboxes/${enc(id)}`));
  }

  static async list(options: ClientOptions = {}): Promise<Sandbox[]> {
    const client = new AvenClient(options);
    const items = await client.request<SandboxInfo[]>("GET", `/environments/${enc(client.requireEnvironment())}/sandboxes`);
    return items.map((i) => new Sandbox(client, i));
  }

  /** Checkpoints in the environment, newest first. */
  static async checkpoints(options: ClientOptions = {}): Promise<SandboxCheckpoint[]> {
    const client = new AvenClient(options);
    return client.request<SandboxCheckpoint[]>("GET", `/environments/${enc(client.requireEnvironment())}/sandbox-checkpoints`);
  }

  private static async findCheckpoint(client: AvenClient, name: string): Promise<SandboxCheckpoint> {
    const all = await client.request<SandboxCheckpoint[]>("GET", `/environments/${enc(client.requireEnvironment())}/sandbox-checkpoints`);
    const cp = all.find((c) => c.name === name || c.id === name);
    if (!cp) throw new SandboxNotFoundError(`checkpoint ${name} not found`);
    return cp;
  }

  static async renameCheckpoint(from: string, to: string, options: ClientOptions = {}): Promise<SandboxCheckpoint> {
    const client = new AvenClient(options);
    const cp = await Sandbox.findCheckpoint(client, from);
    return client.request<SandboxCheckpoint>("PATCH", `/sandbox-checkpoints/${enc(cp.id)}`, { name: to });
  }

  static async deleteCheckpoint(name: string, options: ClientOptions = {}): Promise<void> {
    const client = new AvenClient(options);
    const cp = await Sandbox.findCheckpoint(client, name);
    await client.request("DELETE", `/sandbox-checkpoints/${enc(cp.id)}`);
  }

  /**
   * Runs a command. A string runs through `sh -lc`; an array runs as argv.
   * Never throws on a non-zero exit code: inspect `exitCode` / `timedOut`.
   */
  async exec(command: string | string[], options: ExecOptions = {}): Promise<ExecResult> {
    const r = await this.client.request<{ stdout: string | null; stderr: string | null; exit_code: number; truncated: boolean; timed_out: boolean }>(
      "POST",
      `/sandboxes/${enc(this.id)}/exec`,
      {
        ...(typeof command === "string" ? { command } : { cmd: command }),
        cwd: options.cwd,
        env: options.env,
        stdin: options.stdin === undefined ? undefined : b64encode(toBytes(options.stdin)),
        timeout_s: options.timeoutSec,
        max_output: options.maxOutputBytes,
      },
    );
    const stdoutBytes = b64decode(r.stdout);
    const stderrBytes = b64decode(r.stderr);
    return {
      exitCode: r.exit_code,
      stdout: textDecoder.decode(stdoutBytes),
      stderr: textDecoder.decode(stderrBytes),
      stdoutBytes,
      stderrBytes,
      truncated: r.truncated,
      timedOut: r.timed_out,
    };
  }

  /** Clones the filesystem into a new sandbox. Settings are not inherited. */
  async fork(options: SandboxOptions = {}): Promise<Sandbox> {
    return new Sandbox(this.client, await this.client.request<SandboxInfo>("POST", `/sandboxes/${enc(this.id)}/fork`, optionsBody(options)));
  }

  /** Saves the filesystem as a named checkpoint (reusing a name replaces it). */
  checkpoint(name: string): Promise<SandboxCheckpoint> {
    return this.client.request<SandboxCheckpoint>("POST", `/sandboxes/${enc(this.id)}/checkpoints`, { name });
  }

  async refresh(): Promise<this> {
    this.info = await this.client.request<SandboxInfo>("GET", `/sandboxes/${enc(this.id)}`);
    return this;
  }

  /** Resets the idle timer without running a command. */
  async heartbeat(): Promise<this> {
    this.info = await this.client.request<SandboxInfo>("POST", `/sandboxes/${enc(this.id)}/heartbeat`);
    return this;
  }

  async destroy(): Promise<void> {
    try {
      await this.client.request("DELETE", `/sandboxes/${enc(this.id)}`);
    } catch (e) {
      if (!(e instanceof SandboxNotFoundError)) throw e;
    }
  }

  /** `await using sandbox = await Sandbox.create()` destroys it when the scope exits. */
  async [Symbol.asyncDispose](): Promise<void> {
    await this.destroy();
  }
}

/** @deprecated Use AvenError. Retained for source compatibility. */
export { AvenError as KeelError, AvenClient as KeelClient };
