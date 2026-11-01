"use client";

import { useEffect, useSyncExternalStore } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { sse } from "./api";
import { qk } from "./queries";

/**
 * v1.5: subscribes to GET /api/notifications/stream and refreshes the inbox when a new item arrives.
 *
 * One EventSource is shared by every subscriber of the page (reference counted), so several bells or
 * remounts never open parallel streams. The stream is closed while the tab is hidden and reopened (with an
 * inbox refetch) when it becomes visible again, so background tabs do not hold a connection slot.
 * Reconnects with exponential backoff (1 s → 30 s). The hook returns whether the stream is connected so
 * the caller can fall back to polling while it is not. On `revoked` (logout / session revoked elsewhere)
 * it stops for good and refetches /auth/me so the app can go back to the login screen.
 */

type Stream = {
  subscribers: number;
  live: boolean;
  revoked: boolean;
  es: EventSource | null;
  retry: ReturnType<typeof setTimeout> | undefined;
  attempt: number;
  /** Whether a connection was ever open; a reopen then refetches what was missed. */
  opened: boolean;
  client: QueryClient | null;
};

const state: Stream = { subscribers: 0, live: false, revoked: false, es: null, retry: undefined, attempt: 0, opened: false, client: null };
const listeners = new Set<() => void>();

function setLive(v: boolean) {
  if (state.live === v) return;
  state.live = v;
  listeners.forEach((l) => l());
}

function disconnect() {
  if (state.retry) clearTimeout(state.retry);
  state.retry = undefined;
  if (state.es) {
    state.es.onopen = null;
    state.es.onerror = null;
    state.es.close();
    state.es = null;
  }
  setLive(false);
}

function shouldRun() {
  return state.subscribers > 0 && !state.revoked && document.visibilityState !== "hidden";
}

function connect() {
  if (state.es || !shouldRun()) return;
  const es = new EventSource(sse.notifications(), { withCredentials: true });
  state.es = es;
  es.onopen = () => {
    // Items that arrived while disconnected are only visible via the list API.
    if (state.opened) state.client?.invalidateQueries({ queryKey: qk.notifications });
    state.opened = true;
    state.attempt = 0;
    setLive(true);
  };
  es.addEventListener("notification", () => {
    state.client?.invalidateQueries({ queryKey: qk.notifications });
  });
  es.addEventListener("revoked", () => {
    state.revoked = true;
    disconnect();
    state.client?.invalidateQueries({ queryKey: qk.me });
  });
  es.onerror = () => {
    // Close and retry ourselves: the browser's own retry has no backoff and ignores visibility.
    disconnect();
    if (!shouldRun()) return;
    state.attempt++;
    state.retry = setTimeout(() => {
      state.retry = undefined;
      connect();
    }, Math.min(1000 * 2 ** state.attempt, 30_000));
  };
}

function onVisibility() {
  if (document.visibilityState === "hidden") disconnect();
  else {
    state.attempt = 0;
    connect();
  }
}

function acquire(client: QueryClient): () => void {
  state.client = client;
  if (state.subscribers++ === 0) {
    document.addEventListener("visibilitychange", onVisibility);
    connect();
  }
  return () => {
    if (--state.subscribers > 0) return;
    document.removeEventListener("visibilitychange", onVisibility);
    disconnect();
    // A later sign-in in the same page starts fresh.
    state.revoked = false;
    state.attempt = 0;
    state.opened = false;
    state.client = null;
  };
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}
const getLive = () => state.live;
const getServerLive = () => false;

export function useInboxStream(enabled = true): boolean {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof window === "undefined" || typeof EventSource === "undefined") return;
    return acquire(qc);
  }, [enabled, qc]);
  const live = useSyncExternalStore(subscribe, getLive, getServerLive);
  return enabled && live;
}
