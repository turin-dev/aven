"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, type Deployment, type InstancePatch } from "./api";
import { qk } from "./queries";
import { useCanvasState } from "./nav";

/** Mutations for one service instance (service × environment). Errors surface through `error`. */
export function useServiceActions(sid: string, eid: string) {
  const qc = useQueryClient();
  const { go } = useCanvasState();
  const [error, setError] = useState<unknown>(null);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: qk.canvas(eid) });
    qc.invalidateQueries({ queryKey: qk.deployments(sid, eid) });
    qc.invalidateQueries({ queryKey: qk.instance(sid, eid) });
    qc.invalidateQueries({ queryKey: ["events"] });
  };
  const wrap = async <T,>(fn: () => Promise<T>): Promise<T | undefined> => {
    setError(null);
    try {
      return await fn();
    } catch (e) {
      setError(e);
      return undefined;
    } finally {
      refresh();
    }
  };
  const toDeployments = (d: unknown) => {
    if (d) go({ tab: "deployments", dep: null }, "replace");
    return d;
  };

  return {
    error,
    clearError: () => setError(null),
    refresh,
    deploy: async () => toDeployments(await wrap(() => api.deploy(sid, eid))),
    forceBuild: async () => toDeployments(await wrap(() => api.deploy(sid, eid, true))),
    redeploy: async (d: Deployment) => toDeployments(await wrap(() => api.redeploy(d.id))),
    rollback: async (d: Deployment) => toDeployments(await wrap(() => api.rollback(d.id))),
    cancel: (did: string) =>
      wrap(async () => {
        await api.cancel(did);
        qc.invalidateQueries({ queryKey: qk.deployment(did) });
      }),
    restart: () => wrap(() => api.restart(sid, eid)),
    stop: () => wrap(() => api.stop(sid, eid)),
    runCron: () => wrap(() => api.runCron(sid, eid)),
    /** PATCH the instance; with deploy=true a deployment starts right after saving. */
    apply: async (patch: InstancePatch, deploy: boolean, moveVolumes = false) => {
      setError(null);
      try {
        const next = await api.updateInstance(sid, eid, patch, deploy, moveVolumes);
        qc.setQueryData(qk.instance(sid, eid), next);
        qc.invalidateQueries({ queryKey: qk.resolved(sid, eid) });
        if (deploy) go({ tab: "deployments", dep: null }, "replace");
        return next;
      } finally {
        refresh();
      }
    },
    remove: async () => {
      setError(null);
      try {
        await api.deleteService(sid);
        qc.invalidateQueries({ queryKey: ["project"] });
        refresh();
        go({ svc: null, dep: null, tab: "deployments" }, "replace");
      } catch (e) {
        setError(e);
      }
    },
  };
}

export type ServiceActions = ReturnType<typeof useServiceActions>;
