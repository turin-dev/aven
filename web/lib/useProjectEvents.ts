"use client";

import { useEffect } from "react";
import { useQueryClient, type QueryKey } from "@tanstack/react-query";
import { sse, type AvenEvent } from "./api";
import { qk } from "./queries";

/** Events arriving within this window share one refetch per query (a deploy emits several in a burst). */
const COALESCE_MS = 200;

/** Subscribes to the project's event stream and refreshes the affected queries (canvas live updates). */
export function useProjectEvents(pid: string | null | undefined) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!pid) return;
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let closed = false;
    let lost = false;
    const dirty = new Map<string, QueryKey>();
    const invalidate = (queryKey: QueryKey) => {
      dirty.set(JSON.stringify(queryKey), queryKey);
      if (!flushTimer) flushTimer = setTimeout(flush, COALESCE_MS);
    };
    const flush = () => {
      flushTimer = undefined;
      const keys = [...dirty.values()];
      dirty.clear();
      for (const queryKey of keys) qc.invalidateQueries({ queryKey });
    };
    const connect = () => {
      if (closed) return;
      es = new EventSource(sse.events(pid), { withCredentials: true });
      es.onopen = () => {
        attempt = 0;
        // Events sent while we were disconnected are gone: resync everything the stream would have refreshed.
        if (lost) {
          lost = false;
          invalidate(["events", pid]);
          invalidate(["canvas"]);
          invalidate(qk.project(pid));
        }
      };
      es.addEventListener("event", (m) => {
        let ev: AvenEvent;
        try {
          ev = JSON.parse((m as MessageEvent).data) as AvenEvent;
        } catch {
          return;
        }
        if (!ev || typeof ev.id !== "string" || typeof ev.kind !== "string") return;
        const prepend = (old: AvenEvent[] | undefined) => (old && !old.some((e) => e.id === ev.id) ? [ev, ...old] : old);
        qc.setQueryData<AvenEvent[]>(qk.events(pid), prepend);
        if (ev.environment_id) qc.setQueryData<AvenEvent[]>(qk.events(pid, ev.environment_id), prepend);
        else invalidate(["events", pid]);
        if (ev.environment_id) {
          invalidate(qk.canvas(ev.environment_id));
          if (ev.service_id) invalidate(qk.deployments(ev.service_id, ev.environment_id));
        } else {
          invalidate(["canvas"]);
        }
        if (/^(service|environment)\./.test(ev.kind)) invalidate(qk.project(pid));
        if (ev.kind.startsWith("backup.")) invalidate(["backups"]);
      });
      es.onerror = () => {
        es?.close();
        if (closed) return;
        lost = true;
        attempt++;
        retry = setTimeout(connect, Math.min(1000 * 2 ** attempt, 30000));
      };
    };
    connect();
    return () => {
      closed = true;
      es?.close();
      if (retry) clearTimeout(retry);
      if (flushTimer) clearTimeout(flushTimer);
    };
  }, [pid, qc]);
}
