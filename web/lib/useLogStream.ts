"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "./api";
import { t } from "./i18n";

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

export function useLogStream(url: string | null, opts: Options = {}) {
  const [lines, setLines] = useState<string[]>([]);
  const [state, setState] = useState<StreamState>("connecting");
  const [doneStatus, setDoneStatus] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const optsRef = useRef(opts);
  optsRef.current = opts;

  useEffect(() => {
    if (!url) return;
    const max = optsRef.current.maxLines ?? 20000;
    let es: EventSource | null = null;
    let buf: string[] = [];
    let pending: string[] = [];
    let raf = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let closed = false;

    setLines([]);
    setDoneStatus(null);
    setState("connecting");

    const flush = () => {
      raf = 0;
      if (!pending.length) return;
      buf = buf.concat(pending);
      pending = [];
      if (buf.length > max) buf = buf.slice(buf.length - max);
      setLines(buf);
    };
    const push = (line: string) => {
      pending.push(line);
      if (!raf) raf = requestAnimationFrame(flush);
    };

    const connect = () => {
      if (closed) return;
      es = new EventSource(url, { withCredentials: true });
      es.onopen = () => {
        if (attempt > 0) {
          if (optsRef.current.replay) {
            buf = [];
            pending = [];
            setLines([]);
          } else {
            push(t("── 재연결됨 ──"));
          }
        }
        attempt = 0;
        setState("open");
      };
      es.addEventListener("log", (e) => push((e as MessageEvent).data));
      for (const name of Object.keys(optsRef.current.on ?? {})) {
        es.addEventListener(name, (e) => optsRef.current.on?.[name]?.((e as MessageEvent).data));
      }
      es.addEventListener("status", (e) =>
        optsRef.current.onStatus?.((e as MessageEvent).data),
      );
      es.addEventListener("done", (e) => {
        const s = (e as MessageEvent).data as string;
        closed = true;
        es?.close();
        flush();
        setDoneStatus(s);
        setState("done");
        optsRef.current.onDone?.(s);
      });
      es.onerror = () => {
        if (closed) return;
        es?.close();
        es = null;
        attempt++;
        if (attempt > MAX_RETRIES) {
          setState("error");
          return;
        }
        setState("reconnecting");
        const delay = Math.min(1000 * 2 ** (attempt - 1), 15000);
        // Check auth before reconnecting; api.me redirects to /login on 401.
        api.me().then(
          () => {
            retryTimer = setTimeout(connect, delay);
          },
          (err) => {
            if (err instanceof ApiError && err.status === 401) {
              closed = true;
              setState("error");
            } else {
              retryTimer = setTimeout(connect, delay);
            }
          },
        );
      };
    };

    connect();
    return () => {
      closed = true;
      es?.close();
      if (raf) cancelAnimationFrame(raf);
      if (retryTimer) clearTimeout(retryTimer);
    };
  }, [url, nonce]);

  const reconnect = () => setNonce((n) => n + 1);
  return { lines, state, doneStatus, reconnect };
}

/** Parses JSON SSE lines incrementally; unparsable lines are dropped. */
export function useParsedLines<T>(lines: string[]): T[] {
  const cache = useRef<{ src: string[]; out: T[] }>({ src: [], out: [] });
  const c = cache.current;
  // The stream either appends to the same buffer or replaces it (reconnect / trim).
  const appended = lines.length >= c.src.length && c.src.length > 0 && lines[c.src.length - 1] === c.src[c.src.length - 1] && lines[0] === c.src[0];
  if (lines === c.src) return c.out;
  const start = appended ? c.src.length : 0;
  const out = appended ? c.out.slice() : [];
  for (let i = start; i < lines.length; i++) {
    try {
      out.push(JSON.parse(lines[i]) as T);
    } catch {
      /* skip */
    }
  }
  cache.current = { src: lines, out };
  return out;
}
