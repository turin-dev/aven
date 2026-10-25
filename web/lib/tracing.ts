// Tracing API client and hooks (docs/api-v1.md, "Tracing (OpenTelemetry)").

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, type ID } from "./api";

export type TracingSetting = {
  instance_id: ID;
  service_id: ID;
  service_name: string;
  enabled: boolean;
  has_public_domain: boolean;
  updated_at: string | null;
  last_edge_span_at: string | null;
  last_service_span_at: string | null;
};

export type TraceSummary = {
  trace_id: string;
  started_at: string;
  duration_ms: number;
  root_name: string;
  service_id?: ID;
  service_name?: string;
  span_count: number;
  error_count: number;
};

export type TraceSpan = {
  trace_id: string;
  span_id: string;
  parent_span_id?: string;
  project_id: ID;
  environment_id: ID;
  service_id?: ID;
  service_name?: string;
  deployment_id?: ID;
  replica_id?: string;
  component: "edge" | "service" | string;
  name: string;
  kind: "server" | "client" | "internal" | "producer" | "consumer" | string;
  status: "ok" | "error" | "unset" | string;
  start_time: string;
  end_time: string;
  duration_ms: number;
  attributes: Record<string, unknown>;
  resource_attributes: Record<string, unknown>;
  events: { name: string; time: string; attributes?: Record<string, unknown> }[];
  links: { trace_id: string; span_id: string; attributes?: Record<string, unknown> }[];
};

export type TraceListQuery = { since: string; filter?: string; errors?: boolean; service?: ID | null; limit?: number };

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    credentials: "include",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (j?.error) msg = j.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg);
  }
  return (await res.json()) as T;
}

const enc = encodeURIComponent;

export const tracingApi = {
  settings: (eid: ID) => call<TracingSetting[]>("GET", `/environments/${enc(eid)}/tracing`),
  setService: (sid: ID, eid: ID, enabled: boolean) =>
    call<TracingSetting>("PUT", `/services/${enc(sid)}/instances/${enc(eid)}/tracing`, { enabled }),
  setAll: (eid: ID, enabled: boolean) => call<TracingSetting[]>("PUT", `/environments/${enc(eid)}/tracing`, { enabled }),
  list: (eid: ID, q: TraceListQuery) => {
    const p = new URLSearchParams({ since: q.since, limit: String(q.limit ?? 500) });
    if (q.filter?.trim()) p.set("filter", q.filter.trim());
    if (q.errors) p.set("errors", "true");
    if (q.service) p.set("service", q.service);
    return call<TraceSummary[]>("GET", `/environments/${enc(eid)}/traces?${p}`);
  },
  get: (eid: ID, traceId: string) => call<TraceSpan[]>("GET", `/environments/${enc(eid)}/traces/${enc(traceId)}`),
  attributeKeys: (eid: ID, since: string) => call<string[]>("GET", `/environments/${enc(eid)}/trace-attributes?since=${enc(since)}`),
};

export const tracingKeys = {
  settings: (eid: ID) => ["tracing", eid] as const,
  list: (eid: ID, q: TraceListQuery) => ["traces", eid, q] as const,
  trace: (eid: ID, id: string) => ["trace", eid, id] as const,
  attrs: (eid: ID, since: string) => ["trace-attrs", eid, since] as const,
};

export function useTracingSettings(eid: ID | null | undefined, live = false) {
  return useQuery({
    queryKey: tracingKeys.settings(eid ?? ""),
    queryFn: () => tracingApi.settings(eid!),
    enabled: !!eid,
    refetchInterval: live ? 5_000 : 30_000,
  });
}

export function useTraces(eid: ID | null | undefined, q: TraceListQuery, live: boolean) {
  return useQuery({
    queryKey: tracingKeys.list(eid ?? "", q),
    queryFn: () => tracingApi.list(eid!, q),
    enabled: !!eid,
    refetchInterval: live ? 5_000 : false,
    placeholderData: (prev) => prev,
  });
}

export function useTrace(eid: ID | null | undefined, id: string | null) {
  return useQuery({
    queryKey: tracingKeys.trace(eid ?? "", id ?? ""),
    queryFn: () => tracingApi.get(eid!, id!),
    enabled: !!eid && !!id,
    retry: (n, e) => !(e instanceof ApiError && (e.status === 404 || e.status === 400)) && n < 2,
  });
}

export function useTraceAttributeKeys(eid: ID | null | undefined, since: string) {
  return useQuery({
    queryKey: tracingKeys.attrs(eid ?? "", since),
    queryFn: () => tracingApi.attributeKeys(eid!, since),
    enabled: !!eid,
    staleTime: 60_000,
  });
}

export function useSetTracing(eid: ID | null | undefined) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { sid: ID | "all"; enabled: boolean }) =>
      v.sid === "all" ? tracingApi.setAll(eid!, v.enabled) : tracingApi.setService(v.sid, eid!, v.enabled).then((s) => [s]),
    onSuccess: () => qc.invalidateQueries({ queryKey: tracingKeys.settings(eid ?? "") }),
  });
}

/** 32-hex trace id from an id or a whole traceparent header; null if invalid. */
export function parseTraceId(v: string): string | null {
  let s = v.trim().toLowerCase();
  const parts = s.split("-");
  if (parts.length === 4 && parts[1].length === 32) s = parts[1];
  return /^[0-9a-f]{32}$/.test(s) && !/^0+$/.test(s) ? s : null;
}

export function fmtSpanMs(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(ms >= 10000 ? 1 : 2)}s`;
  if (ms >= 10) return `${Math.round(ms)}ms`;
  if (ms >= 1) return `${ms.toFixed(1)}ms`;
  return `${Math.round(ms * 1000)}µs`;
}

/** Stable per-service color for waterfall bars. */
const PALETTE = ["#a855f7", "#3b82f6", "#10b981", "#f59e0b", "#ec4899", "#06b6d4", "#84cc16", "#f97316", "#6366f1", "#14b8a6"];
export function serviceColor(key: string): string {
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return PALETTE[h % PALETTE.length];
}

/** Epoch milliseconds of an RFC 3339 time, keeping the sub-millisecond fraction (Go emits up to 9 digits). */
export function isoMs(iso: string): number {
  const m = /^(.*T\d\d:\d\d:\d\d)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)$/.exec(iso);
  if (!m) return Date.parse(iso);
  const frac = m[2] ? Number(`0.${m[2]}`) * 1000 : 0;
  return Date.parse(m[1] + m[3]) + frac;
}

export type SpanRow = { span: TraceSpan; depth: number; hasChildren: boolean };

/** Orders spans depth-first under their parents (orphans become roots), each level by start time. */
export function spanTree(spans: TraceSpan[]): SpanRow[] {
  const ids = new Set(spans.map((s) => s.span_id));
  const kids = new Map<string, TraceSpan[]>();
  const roots: TraceSpan[] = [];
  for (const s of spans) {
    if (s.parent_span_id && ids.has(s.parent_span_id)) {
      const l = kids.get(s.parent_span_id) ?? [];
      l.push(s);
      kids.set(s.parent_span_id, l);
    } else roots.push(s);
  }
  const byStart = (a: TraceSpan, b: TraceSpan) => isoMs(a.start_time) - isoMs(b.start_time);
  const out: SpanRow[] = [];
  const walk = (s: TraceSpan, depth: number) => {
    const c = (kids.get(s.span_id) ?? []).sort(byStart);
    out.push({ span: s, depth, hasChildren: c.length > 0 });
    for (const k of c) walk(k, depth + 1);
  };
  roots.sort(byStart).forEach((r) => walk(r, 0));
  return out;
}
