"use client";

/* Sandboxes (docs/api-v1.md#sandboxes): typed calls and react-query hooks. Kept out of api.ts to stay self-contained. */

import { useQuery } from "@tanstack/react-query";
import { request, type ID } from "./api";

export type SandboxNetwork = "isolated" | "private";

export type Sandbox = {
  id: ID;
  project_id: ID;
  environment_id: ID;
  node_id: ID;
  container_name: string;
  network_mode: SandboxNetwork;
  image: string;
  status: "creating" | "running";
  memory_mb: number;
  cpu_cores: number;
  /** 0 = never destroyed for being idle. */
  idle_timeout_minutes: number;
  expires_at: string | null;
  last_activity_at: string;
  parent_id: ID | null;
  checkpoint_name: string | null;
  created_at: string;
};

export type SandboxCheckpoint = { id: ID; project_id: ID; environment_id: ID; node_id: ID; name: string; created_at: string };

export type SandboxOptions = {
  idle_timeout_minutes?: number;
  memory_mb?: number;
  cpu_cores?: number;
  network?: SandboxNetwork;
  variables?: Record<string, string>;
};

export type SandboxCreate = SandboxOptions & { image?: string; checkpoint?: string; node_id?: string };

/** stdout/stderr are base64 (Go []byte). */
export type SandboxExecResult = { stdout: string | null; stderr: string | null; exit_code: number; truncated: boolean; timed_out: boolean };

export type SandboxFileEntry = { name: string; path: string; type: "file" | "dir" | "symlink"; size: number; mod_time: string };

const enc = encodeURIComponent;

export const sandboxApi = {
  list: (eid: ID) => request<Sandbox[]>("GET", `/environments/${enc(eid)}/sandboxes`),
  create: (eid: ID, input: SandboxCreate) => request<Sandbox>("POST", `/environments/${enc(eid)}/sandboxes`, input),
  get: (id: ID) => request<Sandbox>("GET", `/sandboxes/${enc(id)}`),
  destroy: (id: ID) => request<void>("DELETE", `/sandboxes/${enc(id)}`),
  heartbeat: (id: ID) => request<Sandbox>("POST", `/sandboxes/${enc(id)}/heartbeat`),
  fork: (id: ID, input: SandboxOptions) => request<Sandbox>("POST", `/sandboxes/${enc(id)}/fork`, input),
  exec: (id: ID, input: { command?: string; cmd?: string[]; cwd?: string; timeout_s?: number }) =>
    request<SandboxExecResult>("POST", `/sandboxes/${enc(id)}/exec`, input),
  readFile: (id: ID, path: string) =>
    request<{ path: string; content_base64: string; size: number; truncated: boolean }>("GET", `/sandboxes/${enc(id)}/files?path=${enc(path)}`),
  writeFile: (id: ID, path: string, contentBase64: string, mode?: number) =>
    request<{ path: string; size: number }>("PUT", `/sandboxes/${enc(id)}/files`, { path, content_base64: contentBase64, mode }),
  listFiles: (id: ID, path: string) =>
    request<{ path: string; entries: SandboxFileEntry[]; truncated: boolean }>("GET", `/sandboxes/${enc(id)}/files/list?path=${enc(path)}`),
  checkpoints: (eid: ID) => request<SandboxCheckpoint[]>("GET", `/environments/${enc(eid)}/sandbox-checkpoints`),
  checkpoint: (id: ID, name: string) => request<SandboxCheckpoint>("POST", `/sandboxes/${enc(id)}/checkpoints`, { name }),
  renameCheckpoint: (cpid: ID, name: string) => request<SandboxCheckpoint>("PATCH", `/sandbox-checkpoints/${enc(cpid)}`, { name }),
  deleteCheckpoint: (cpid: ID) => request<void>("DELETE", `/sandbox-checkpoints/${enc(cpid)}`),
};

/** WebSocket URL of a sandbox shell (same protocol as the service shell). */
export function sandboxShellUrl(id: ID, cols: number, rows: number): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/sandboxes/${enc(id)}/shell?cols=${cols}&rows=${rows}`;
}

export const sbxKeys = {
  list: (eid: ID) => ["sandboxes", eid] as const,
  checkpoints: (eid: ID) => ["sandbox-checkpoints", eid] as const,
  files: (id: ID, path: string) => ["sandbox-files", id, path] as const,
};

export function useSandboxes(eid: ID | null | undefined) {
  return useQuery({
    queryKey: sbxKeys.list(eid ?? ""),
    queryFn: () => sandboxApi.list(eid!),
    enabled: !!eid,
    refetchInterval: (q) => (q.state.data?.some((s) => s.status === "creating") ? 2000 : 15_000),
  });
}

export function useSandboxCheckpoints(eid: ID | null | undefined) {
  return useQuery({ queryKey: sbxKeys.checkpoints(eid ?? ""), queryFn: () => sandboxApi.checkpoints(eid!), enabled: !!eid });
}

export function useSandboxFiles(id: ID, path: string, enabled: boolean) {
  return useQuery({ queryKey: sbxKeys.files(id, path), queryFn: () => sandboxApi.listFiles(id, path), enabled, retry: false });
}

export function b64ToBytes(s: string | null | undefined): Uint8Array {
  if (!s) return new Uint8Array();
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

export function b64ToText(s: string | null | undefined): string {
  return new TextDecoder().decode(b64ToBytes(s));
}

export function bytesToB64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

/** Splits `KEY=VALUE` lines (comments and blanks ignored). Returns the bad lines too. */
export function parseVariableLines(text: string): { vars: Record<string, string>; bad: string[] } {
  const vars: Record<string, string> = {};
  const bad: string[] = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const i = line.indexOf("=");
    const key = i > 0 ? line.slice(0, i).trim() : "";
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
      bad.push(line);
      continue;
    }
    vars[key] = line.slice(i + 1);
  }
  return { vars, bad };
}
