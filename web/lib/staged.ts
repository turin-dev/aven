"use client";

// Staged (not yet deployed) changes per service instance, shared by the variables, settings and deployments tabs.

import { useCallback, useMemo, useSyncExternalStore } from "react";
import type { InstancePatch, ServiceInstance } from "./api";

export type SettingsPatch = Omit<InstancePatch, "variables" | "variables_patch">;
export type Staged = { vars: Record<string, string | null>; settings: SettingsPatch };

const KEY = "aven.staged.v1";
const LEGACY_KEY = "keel.staged.v1";
const EMPTY: Staged = { vars: {}, settings: {} };

let state: Record<string, Staged> = {};
let loaded = false;
const subs = new Set<() => void>();

function load() {
  if (loaded || typeof window === "undefined") return;
  loaded = true;
  try {
    const raw = window.sessionStorage.getItem(KEY) ?? window.sessionStorage.getItem(LEGACY_KEY);
    state = JSON.parse(raw ?? "{}") as Record<string, Staged>;
  } catch {
    state = {};
  }
}

function commit(next: Record<string, Staged>) {
  state = next;
  try {
    window.sessionStorage.setItem(KEY, JSON.stringify(state));
  } catch {
    /* ignore */
  }
  subs.forEach((f) => f());
}

const keyOf = (sid: string, eid: string) => `${sid}:${eid}`;

export function stagedCount(s: Staged | undefined): number {
  if (!s) return 0;
  return Object.keys(s.vars).length + Object.keys(s.settings).length;
}

/** Drops staged entries that already equal the server value. */
export function prune(s: Staged, inst: ServiceInstance): Staged {
  const vars: Staged["vars"] = {};
  for (const [k, v] of Object.entries(s.vars)) {
    if (v === null ? k in inst.variables : inst.variables[k] !== v) vars[k] = v;
  }
  const settings: SettingsPatch = {};
  for (const [k, v] of Object.entries(s.settings) as [keyof SettingsPatch, unknown][]) {
    if (JSON.stringify(inst[k as keyof ServiceInstance]) !== JSON.stringify(v)) (settings as Record<string, unknown>)[k] = v;
  }
  return { vars, settings };
}

export function toPatch(s: Staged): InstancePatch {
  const p: InstancePatch = { ...s.settings };
  if (Object.keys(s.vars).length) p.variables_patch = { ...s.vars };
  return p;
}

export function useStaged(sid: string, eid: string) {
  load();
  const k = keyOf(sid, eid);
  const staged = useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => subs.delete(f);
    },
    () => state[k] ?? EMPTY,
    () => EMPTY,
  );

  const set = useCallback(
    (fn: (s: Staged) => Staged) => {
      const next = fn(state[k] ?? EMPTY);
      const copy = { ...state };
      if (stagedCount(next) === 0) delete copy[k];
      else copy[k] = next;
      commit(copy);
    },
    [k],
  );

  const setVar = useCallback(
    (name: string, value: string | null, server: Record<string, string>) =>
      set((s) => {
        const vars = { ...s.vars };
        if (value === null ? !(name in server) : server[name] === value) delete vars[name];
        else vars[name] = value;
        return { ...s, vars };
      }),
    [set],
  );

  const setSetting = useCallback(
    <K extends keyof SettingsPatch>(name: K, value: SettingsPatch[K], inst: ServiceInstance) =>
      set((s) => {
        const settings = { ...s.settings };
        if (JSON.stringify(inst[name as keyof ServiceInstance]) === JSON.stringify(value)) delete settings[name];
        else settings[name] = value;
        return { ...s, settings };
      }),
    [set],
  );

  const clear = useCallback((part?: "vars" | "settings") => set((s) => (part ? { ...s, [part]: part === "vars" ? {} : {} } : EMPTY)), [set]);

  return { staged, count: stagedCount(staged), set, setVar, setSetting, clear };
}

/** Unsaved local edits plus changes saved on the server but not deployed yet (`pending_changes`). */
export function pendingSummary(inst: ServiceInstance, s: Staged) {
  const local = [...Object.keys(s.vars).map((k) => `variables.${k}`), ...Object.keys(s.settings)];
  const server = inst.pending_changes ?? [];
  const all = [...new Set([...local, ...server])];
  return { local, server, all, count: all.length };
}

/** Every service's local edits in one environment (sid → staged), for the environment-wide "변경 사항 적용" bar. */
export function useStagedEnv(eid: string): Record<string, Staged> {
  load();
  const all = useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => subs.delete(f);
    },
    () => state,
    () => EMPTY_ALL,
  );
  return useMemo(() => {
    const out: Record<string, Staged> = {};
    const suffix = `:${eid}`;
    for (const [k, v] of Object.entries(all)) if (k.endsWith(suffix)) out[k.slice(0, -suffix.length)] = v;
    return out;
  }, [all, eid]);
}
const EMPTY_ALL: Record<string, Staged> = {};

/** Drops the local edits of one service instance (outside a component). */
export function clearStaged(sid: string, eid: string) {
  load();
  const k = keyOf(sid, eid);
  if (!(k in state)) return;
  const copy = { ...state };
  delete copy[k];
  commit(copy);
}
