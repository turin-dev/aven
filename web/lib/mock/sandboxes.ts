/* Dev-only fake sandbox API for the mock backend (NEXT_PUBLIC_AVEN_MOCK=1). */

import type { Sandbox, SandboxCheckpoint } from "../sandboxes";
import { HttpError, route } from "./server";

type Fs = Map<string, string>;
const boxes = new Map<string, { s: Sandbox; fs: Fs }>();
const checkpoints = new Map<string, { c: SandboxCheckpoint; fs: Fs }>();
let n = 0;
const rid = (p: string) => `${p}_${(Date.now().toString(36) + (n++).toString(36)).slice(-13).padStart(13, "a")}`;
const b64 = (s: string) => btoa(unescape(encodeURIComponent(s)));
const unb64 = (s: string) => decodeURIComponent(escape(atob(s)));

function baseFs(): Fs {
  return new Map([
    ["/etc/os-release", 'PRETTY_NAME="Ubuntu 24.04 LTS"\n'],
    ["/root/.bashrc", "# ~/.bashrc\n"],
  ]);
}

function expiry(s: Sandbox) {
  s.last_activity_at = new Date().toISOString();
  s.expires_at = s.idle_timeout_minutes ? new Date(Date.now() + s.idle_timeout_minutes * 60_000).toISOString() : null;
}

function get(id: string) {
  const b = boxes.get(id);
  if (!b) throw new HttpError(404, "not found");
  return b;
}

type Opts = { idle_timeout_minutes?: number; memory_mb?: number; cpu_cores?: number; network?: "isolated" | "private" };

function make(eid: string, o: Opts, image: string, fs: Fs, extra: Partial<Sandbox> = {}): Sandbox {
  const id = rid("sbx");
  const s: Sandbox = {
    id,
    project_id: "prj_mock",
    environment_id: eid,
    node_id: "node_local",
    container_name: `aven-sbx-${id.replace("_", "-")}`,
    network_mode: o.network ?? "isolated",
    image,
    status: "running",
    memory_mb: o.memory_mb || 1024,
    cpu_cores: o.cpu_cores || 1,
    idle_timeout_minutes: o.idle_timeout_minutes ?? 30,
    expires_at: null,
    last_activity_at: "",
    parent_id: null,
    checkpoint_name: null,
    created_at: new Date().toISOString(),
    ...extra,
  };
  expiry(s);
  boxes.set(id, { s, fs: new Map(fs) });
  return s;
}

route("GET", "/environments/{eid}/sandboxes", ([eid]) => [...boxes.values()].filter((b) => b.s.environment_id === eid).map((b) => b.s).reverse());
route("POST", "/environments/{eid}/sandboxes", ([eid], r) => {
  const body = (r.body ?? {}) as Opts & { image?: string; checkpoint?: string };
  if (body.checkpoint) {
    const cp = [...checkpoints.values()].find((c) => c.c.environment_id === eid && (c.c.name === body.checkpoint || c.c.id === body.checkpoint));
    if (!cp) throw new HttpError(400, "checkpoint not found in this environment");
    return [201, make(eid, body, `aven/sandbox:${cp.c.id.replace("_", "-")}`, cp.fs, { checkpoint_name: cp.c.name })];
  }
  return [201, make(eid, body, body.image || "ubuntu:24.04", baseFs())];
});
route("GET", "/sandboxes/{id}", ([id]) => get(id).s);
route("DELETE", "/sandboxes/{id}", ([id]) => {
  get(id);
  boxes.delete(id);
  return 204;
});
route("POST", "/sandboxes/{id}/heartbeat", ([id]) => {
  const b = get(id);
  expiry(b.s);
  return b.s;
});
route("POST", "/sandboxes/{id}/fork", ([id], r) => {
  const b = get(id);
  const s = make(b.s.environment_id, (r.body ?? {}) as Opts, "", b.fs, { parent_id: id });
  s.image = `aven/sandbox:${s.id.replace("_", "-")}`;
  return [201, s];
});
route("POST", "/sandboxes/{id}/exec", ([id], r) => {
  const b = get(id);
  expiry(b.s);
  const { command, cmd } = (r.body ?? {}) as { command?: string; cmd?: string[] };
  const line = command ?? (cmd ?? []).join(" ");
  if (/^\s*(ls|find)\b/.test(line)) return { stdout: b64([...b.fs.keys()].join("\n") + "\n"), stderr: null, exit_code: 0, truncated: false, timed_out: false };
  if (/^\s*false\b/.test(line)) return { stdout: null, stderr: b64("failed\n"), exit_code: 1, truncated: false, timed_out: false };
  return { stdout: b64(`$ ${line}\n(mock) ok\n`), stderr: null, exit_code: 0, truncated: false, timed_out: false };
});
route("GET", "/sandboxes/{id}/files", ([id], r) => {
  const b = get(id);
  const p = r.query.get("path") ?? "";
  const v = b.fs.get(p);
  if (v === undefined) throw new HttpError(404, `cat: ${p}: No such file or directory`);
  return { path: p, content_base64: b64(v), size: v.length, truncated: false };
});
route("PUT", "/sandboxes/{id}/files", ([id], r) => {
  const b = get(id);
  const { path, content_base64 } = r.body as { path: string; content_base64: string };
  b.fs.set(path, unb64(content_base64));
  expiry(b.s);
  return { path, size: b.fs.get(path)!.length };
});
route("GET", "/sandboxes/{id}/files/list", ([id], r) => {
  const b = get(id);
  const dir = (r.query.get("path") || "/").replace(/\/+$/, "") || "/";
  const prefix = dir === "/" ? "/" : `${dir}/`;
  const seen = new Map<string, "file" | "dir">();
  for (const p of b.fs.keys()) {
    if (!p.startsWith(prefix)) continue;
    const rest = p.slice(prefix.length);
    const [head, ...tail] = rest.split("/");
    if (head) seen.set(head, tail.length ? "dir" : (seen.get(head) ?? "file"));
  }
  if (dir !== "/" && seen.size === 0) throw new HttpError(404, `can't cd to ${dir}`);
  const entries = [...seen].map(([name, type]) => ({
    name,
    path: prefix + name,
    type,
    size: type === "file" ? (b.fs.get(prefix + name)?.length ?? 0) : 4096,
    mod_time: new Date().toISOString(),
  }));
  return { path: dir, entries, truncated: false };
});
route("POST", "/sandboxes/{id}/checkpoints", ([id], r) => {
  const b = get(id);
  const { name } = r.body as { name: string };
  if (!/^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$/.test(name)) throw new HttpError(400, "name must be 1-64 lowercase letters, digits, '.', '_' or '-'");
  const prev = [...checkpoints.values()].find((c) => c.c.environment_id === b.s.environment_id && c.c.name === name);
  const c: SandboxCheckpoint = prev?.c ?? { id: rid("scp"), project_id: "prj_mock", environment_id: b.s.environment_id, node_id: b.s.node_id, name, created_at: "" };
  c.created_at = new Date().toISOString();
  checkpoints.set(c.id, { c, fs: new Map(b.fs) });
  return [201, c];
});
route("GET", "/environments/{eid}/sandbox-checkpoints", ([eid]) =>
  [...checkpoints.values()].filter((c) => c.c.environment_id === eid).map((c) => c.c).sort((a, b) => b.created_at.localeCompare(a.created_at)),
);
route("PATCH", "/sandbox-checkpoints/{id}", ([id], r) => {
  const cp = checkpoints.get(id);
  if (!cp) throw new HttpError(404, "not found");
  const { name } = r.body as { name: string };
  if ([...checkpoints.values()].some((c) => c.c.id !== id && c.c.environment_id === cp.c.environment_id && c.c.name === name)) throw new HttpError(409, "a checkpoint with that name already exists");
  cp.c.name = name;
  return cp.c;
});
route("DELETE", "/sandbox-checkpoints/{id}", ([id]) => {
  if (!checkpoints.delete(id)) throw new HttpError(404, "not found");
  return 204;
});
