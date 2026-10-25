"use client";

import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { sse, type KeelEvent } from "./api";
import { qk } from "./queries";

/** Subscribes to the project's event stream and refreshes the affected queries (canvas live updates). */
export function useProjectEvents(pid: string | null | undefined) {
  const qc = useQueryClient();
  useEffect(() => {
    if (!pid) return;
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let closed = false;
    const connect = () => {
      if (closed) return;
      es = new EventSource(sse.events(pid), { withCredentials: true });
      es.onopen = () => (attempt = 0);
      es.addEventListener("event", (m) => {
        let ev: KeelEvent;
        try {
          ev = JSON.parse((m as MessageEvent).data) as KeelEvent;
        } catch {
          return;
        }
        const prepend = (old: KeelEvent[] | undefined) => (old && !old.some((e) => e.id === ev.id) ? [ev, ...old] : old);
        qc.setQueryData<KeelEvent[]>(qk.events(pid), prepend);
        if (ev.environment_id) qc.setQueryData<KeelEvent[]>(qk.events(pid, ev.environment_id), prepend);
        else qc.invalidateQueries({ queryKey: ["events", pid] });
        if (ev.environment_id) {
          qc.invalidateQueries({ queryKey: qk.canvas(ev.environment_id) });
          if (ev.service_id) qc.invalidateQueries({ queryKey: qk.deployments(ev.service_id, ev.environment_id) });
        } else {
          qc.invalidateQueries({ queryKey: ["canvas"] });
        }
        if (/^(service|environment)\./.test(ev.kind)) qc.invalidateQueries({ queryKey: qk.project(pid) });
        if (ev.kind.startsWith("backup.")) qc.invalidateQueries({ queryKey: ["backups"] });
      });
      es.onerror = () => {
        es?.close();
        if (closed) return;
        attempt++;
        retry = setTimeout(connect, Math.min(1000 * 2 ** attempt, 30000));
      };
    };
    connect();
    return () => {
      closed = true;
      es?.close();
      if (retry) clearTimeout(retry);
    };
  }, [pid, qc]);
}
