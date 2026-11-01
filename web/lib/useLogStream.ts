"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "./api";
import { t } from "./i18n";
import { backoffDelay } from "./logs";

export type StreamState = "connecting" | "open" | "reconnecting" | "done" | "error";

type Options = {
  /** Server replays the full log on each connect; clear buffer on reconnect. */
  replay?: boolean;
  onStatus?: (status: string) => void;
  onDone?: (status: string) => void;
  maxLines?: number;
  /** Extra named SSE events (e.g. network log `capture` status). Read when the stream connects. */
  on?: Record<string, (data: string) => void>;
};

const MAX_RETRIES = 8;
/** A connection must stay up this long before the retry counter resets (stops tight open/error loops). */
const STABLE_MS = 5000;

/**
 * Where a buffer emitted by useLogStream sits in the stream: `gen` changes whenever the buffer restarts
 * (new URL, replaying reconnect) and `dropped` counts lines trimmed from the front. Lets useParsedLines
 * parse only new lines even after the buffer hits maxLines.
 */
type BufMeta = { gen: number; dropped: number };
const meta = new WeakMap<string[], BufMeta>();
let genSeq = 0;

export function useLogStream(url: string | null, opts: Options = {}) {
  const [lines, setLines] = useState<string[]>([]);
  const [state, setState] = useState<StreamState>("connecting");
  const [doneStatus, setDoneStatus] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const optsRef = useRef(opts);
  optsRef.current = opts;

  useEffect(() => {
    const empty: string[] = [];
    meta.set(empty, { gen: ++genSeq, dropped: 0 });
    setLines(empty);
    setDoneStatus(null);
    if (!url) return;
    const max = optsRef.current.maxLines ?? 20000;
    let es: EventSource | null = null;
    let buf: string[] = empty;
    let gen = meta.get(empty)!.gen;
    let dropped = 0;
    let pending: string[] = [];
    let raf = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let stableTimer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let closed = false;
    let failed = false;

    setState("connecting");

    const emit = (next: string[]) => {
      buf = next;
      meta.set(buf, { gen, dropped });
      setLines(buf);
    };
    const reset = () => {
      gen = ++genSeq;
      dropped = 0;
      pending = [];
      emit([]);
    };
    const flush = () => {
      if (raf) cancelAnimationFrame(raf);
      raf = 0;
      if (!pending.length) return;
      let next = buf.concat(pending);
      pending = [];
      if (next.length > max) {
        dropped += next.length - max;
        next = next.slice(next.length - max);
      }
      emit(next);
    };
    const push = (line: string) => {
      pending.push(line);
      // Background tabs pause rAF; keep the backlog bounded until the next frame.
      if (pending.length > max) {
        dropped += pending.length - max;
        pending = pending.slice(pending.length - max);
        if (buf.length) {
          dropped += buf.length;
          buf = [];
        }
      }
      if (!raf) raf = requestAnimationFrame(flush);
    };

    const scheduleRetry = () => {
      if (closed) return;
      attempt++;
      if (attempt > MAX_RETRIES) {
        failed = true;
        setState("error");
        return;
      }
      setState("reconnecting");
      const delay = backoffDelay(attempt);
      // Check auth before reconnecting; api.me redirects to /login on 401.
      api.me().then(
        () => {
          if (!closed && !es) retryTimer = setTimeout(connect, delay);
        },
        (err) => {
          if (closed || es) return;
          if (err instanceof ApiError && err.status === 401) {
            closed = true;
            setState("error");
          } else {
            retryTimer = setTimeout(connect, delay);
          }
        },
      );
    };

    const connect = () => {
      if (closed) return;
      clearTimeout(retryTimer);
      retryTimer = undefined;
      es?.close();
      failed = false;
      const reconnecting = attempt > 0;
      const src = new EventSource(url, { withCredentials: true });
      es = src;
      src.onopen = () => {
        if (es !== src) return;
        if (reconnecting) {
          if (optsRef.current.replay) reset();
          else push(t("── 재연결됨 ──"));
        }
        clearTimeout(stableTimer);
        stableTimer = setTimeout(() => (attempt = 0), STABLE_MS);
        setState("open");
      };
      src.addEventListener("log", (e) => es === src && push((e as MessageEvent).data));
      for (const name of Object.keys(optsRef.current.on ?? {})) {
        src.addEventListener(name, (e) => es === src && optsRef.current.on?.[name]?.((e as MessageEvent).data));
      }
      src.addEventListener("status", (e) => es === src && optsRef.current.onStatus?.((e as MessageEvent).data));
      src.addEventListener("done", (e) => {
        if (es !== src) return;
        const s = (e as MessageEvent).data as string;
        closed = true;
        src.close();
        es = null;
        clearTimeout(stableTimer);
        flush();
        setDoneStatus(s);
        setState("done");
        optsRef.current.onDone?.(s);
      });
      src.onerror = () => {
        if (closed || es !== src) return;
        src.close();
        es = null;
        clearTimeout(stableTimer);
        scheduleRetry();
      };
    };

    // Coming back online (or to a visible tab after giving up) retries at once instead of waiting out the backoff.
    const retryNow = () => {
      if (closed || es) return;
      if (!failed && !retryTimer) return;
      attempt = Math.min(attempt, MAX_RETRIES - 1);
      connect();
    };
    const onVisible = () => {
      if (document.visibilityState === "visible" && failed) retryNow();
    };
    window.addEventListener("online", retryNow);
    document.addEventListener("visibilitychange", onVisible);

    connect();
    return () => {
      closed = true;
      es?.close();
      es = null;
      if (raf) cancelAnimationFrame(raf);
      clearTimeout(retryTimer);
      clearTimeout(stableTimer);
      window.removeEventListener("online", retryNow);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [url, nonce]);

  const reconnect = () => setNonce((n) => n + 1);
  return { lines, state, doneStatus, reconnect };
}

/** Parses JSON SSE lines incrementally; unparsable lines are dropped. */
export function useParsedLines<T>(lines: string[]): T[] {
  const cache = useRef<{ src: string[]; parsed: (T | undefined)[]; out: T[] }>({ src: [], parsed: [], out: [] });
  const c = cache.current;
  if (lines === c.src) return c.out;
  const prev = meta.get(c.src);
  const cur = meta.get(lines);
  let parsed: (T | undefined)[];
  let start: number;
  if (prev && cur && prev.gen === cur.gen && cur.dropped >= prev.dropped && cur.dropped <= prev.dropped + c.src.length) {
    // Same stream: drop what was trimmed, parse only lines past the previous end.
    parsed = c.parsed.slice(cur.dropped - prev.dropped);
    start = prev.dropped + c.src.length - cur.dropped;
  } else if (!cur && c.src.length > 0 && lines.length >= c.src.length && lines[c.src.length - 1] === c.src[c.src.length - 1] && lines[0] === c.src[0]) {
    // Buffers from elsewhere: plain append detection.
    parsed = c.parsed.slice();
    start = c.src.length;
  } else {
    parsed = [];
    start = 0;
  }
  for (let i = start; i < lines.length; i++) {
    try {
      parsed.push(JSON.parse(lines[i]) as T);
    } catch {
      parsed.push(undefined);
    }
  }
  const out = parsed.filter((x): x is T => x !== undefined);
  cache.current = { src: lines, parsed, out };
  return out;
}
