"use client";

import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, type Deployment, type InstancePatch } from "./api";
import { qk } from "./queries";
import { useCanvasState } from "./nav";

/**
 * Mutations for one service instance (service × environment). Errors surface through `error`.
 * Results that land after the pane switched to another service are ignored (no stray error, no tab jump),
 * and a second click on an action that is still running reuses the in-flight request.
 */
export function useServiceActions(sid: string, eid: string) {
  const qc = useQueryClient();
  const { go } = useCanvasState();
  const key = `${sid}:${eid}`;
  const [err, setErr] = useState<{ key: string; error: unknown } | null>(null);
  const current = useRef(key);
  current.current = key;
  const inflight = useRef(new Map<string, Promise<unknown>>());

  const error = err && err.key === key ? err.error : null;
  const setError = (e: unknown, k = key) => setErr(e === null ? null : { key: k, error: e });

  const refreshFor = (s: string, e: string) => {
    qc.invalidateQueries({ queryKey: qk.canvas(e) });
    qc.invalidateQueries({ queryKey: qk.deployments(s, e) });
    qc.invalidateQueries({ queryKey: qk.instance(s, e) });
    qc.invalidateQueries({ queryKey: ["events"] });
  };
  const refresh = () => refreshFor(sid, eid);
  const still = (k: string) => current.current === k;

  /** Runs `fn` once per (instance, action) at a time; errors are stored for the instance that started it. */
  const run = <T,>(name: string, fn: () => Promise<T>, after?: (v: T) => void): Promise<T | undefined> => {
    const k = key;
    const s = sid;
    const e = eid;
    const id = `${k}:${name}`;
    const running = inflight.current.get(id) as Promise<T | undefined> | undefined;
    if (running) return running;
    setError(null);
    const p = (async () => {
      try {
        const v = await fn();
        if (still(k)) after?.(v);
        return v;
      } catch (x) {
        if (still(k)) setError(x, k);
        return undefined;
      } finally {
        inflight.current.delete(id);
        refreshFor(s, e);
      }
    })();
    inflight.current.set(id, p);
    return p;
  };
  const toDeployments = (d: unknown) => {
    if (d) go({ tab: "deployments", dep: null }, "replace");
  };

  return {
    error,
    clearError: () => setErr(null),
    refresh,
    deploy: () => run("deploy", () => api.deploy(sid, eid), toDeployments),
    forceBuild: () => run("deploy", () => api.deploy(sid, eid, true), toDeployments),
    redeploy: (d: Deployment) => run(`redeploy:${d.id}`, () => api.redeploy(d.id), toDeployments),
    rollback: (d: Deployment) => run(`rollback:${d.id}`, () => api.rollback(d.id), toDeployments),
    cancel: (did: string) =>
      run(`cancel:${did}`, async () => {
        await api.cancel(did);
        qc.invalidateQueries({ queryKey: qk.deployment(did) });
      }),
    restart: () => run("restart", () => api.restart(sid, eid)),
    stop: () => run("stop", () => api.stop(sid, eid)),
    runCron: () => run("cron", () => api.runCron(sid, eid)),
    /** PATCH the instance; with deploy=true a deployment starts right after saving. Throws on failure. */
    apply: async (patch: InstancePatch, deploy: boolean, moveVolumes = false) => {
      const k = key;
      const s = sid;
      const e = eid;
      setError(null);
      try {
        const next = await api.updateInstance(s, e, patch, deploy, moveVolumes);
        qc.setQueryData(qk.instance(s, e), next);
        qc.invalidateQueries({ queryKey: qk.resolved(s, e) });
        if (deploy && still(k)) go({ tab: "deployments", dep: null }, "replace");
        return next;
      } finally {
        refreshFor(s, e);
      }
    },
    remove: async () => {
      const k = key;
      const s = sid;
      const e = eid;
      setError(null);
      try {
        await api.deleteService(s);
        qc.invalidateQueries({ queryKey: ["project"] });
        refreshFor(s, e);
        if (still(k)) go({ svc: null, dep: null, tab: "deployments" }, "replace");
      } catch (x) {
        if (still(k)) setError(x, k);
      }
    },
  };
}

export type ServiceActions = ReturnType<typeof useServiceActions>;
