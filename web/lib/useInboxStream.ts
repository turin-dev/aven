"use client";

import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { sse } from "./api";
import { qk } from "./queries";

/**
 * v1.5: subscribes to GET /api/notifications/stream and refreshes the inbox when a new item arrives.
 * Reconnects with exponential backoff (1 s → 30 s). Returns whether the stream is connected so the
 * caller can fall back to 30 s polling while it is not. On `revoked` (logout / session revoked
 * elsewhere) it stops and refetches /auth/me so the app can go back to the login screen.
 */
export function useInboxStream(enabled = true): boolean {
  const qc = useQueryClient();
  const [live, setLive] = useState(false);
  useEffect(() => {
    if (!enabled || typeof window === "undefined" || typeof EventSource === "undefined") return;
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let closed = false;
    const connect = () => {
      if (closed) return;
      es = new EventSource(sse.notifications(), { withCredentials: true });
      es.onopen = () => {
        // Items that arrived while disconnected are only visible via the list API.
        if (attempt > 0) qc.invalidateQueries({ queryKey: qk.notifications });
        attempt = 0;
        setLive(true);
      };
      es.addEventListener("notification", () => {
        qc.invalidateQueries({ queryKey: qk.notifications });
      });
      es.addEventListener("revoked", () => {
        closed = true;
        es?.close();
        setLive(false);
        qc.invalidateQueries({ queryKey: qk.me });
      });
      es.onerror = () => {
        es?.close();
        setLive(false);
        if (closed) return;
        attempt++;
        retry = setTimeout(connect, Math.min(1000 * 2 ** attempt, 30_000));
      };
    };
    connect();
    return () => {
      closed = true;
      es?.close();
      if (retry) clearTimeout(retry);
    };
  }, [enabled, qc]);
  return live;
}
