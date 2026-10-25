/* Dev-only fake tracing API for the mock backend (NEXT_PUBLIC_KEEL_MOCK=1). Shapes follow lib/tracing.ts. */

import type { CanvasData } from "../api";
import type { TraceSpan, TraceSummary, TracingSetting } from "../tracing";
import { HttpError, handle, route } from "./server";

type Svc = { id: string; name: string; instance_id: string; public: boolean };
type Stored = TraceSummary & { seed: number; services: Svc[] };

const enabled = new Map<string, boolean>(); // `${eid}/${sid}`
const updated = new Map<string, string>();
const store = new Map<string, Stored[]>();

function rng(seed: number) {
  let s = seed >>> 0 || 1;
  return () => {
    s = (Math.imul(s ^ (s >>> 15), 2246822507) + 0x6d2b79f5) >>> 0;
    s ^= s >>> 13;
    return (s >>> 0) / 4294967296;
  };
}
const hex = (r: () => number, n: number) => Array.from({ length: n }, () => "0123456789abcdef"[Math.floor(r() * 16)]).join("");
const iso = (t: number) => new Date(t).toISOString();

function services(eid: string): Svc[] {
  const res = handle("GET", `/environments/${eid}/canvas`, new URLSearchParams(), undefined);
  if (res.status !== 200) throw new HttpError(res.status, "environment not found");
  const c = res.body as CanvasData;
  return c.services
    .filter((s) => !s.database_kind)
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((s) => ({ id: s.id, name: s.name, instance_id: s.instance.id, public: s.instance.domains.length > 0 }));
}

const isOn = (eid: string, s: Svc, i: number) => enabled.get(`${eid}/${s.id}`) ?? i < 2;

const ROUTES = ["GET /", "GET /api/me", "GET /api/posts", "GET /api/posts/{id}", "POST /api/login", "POST /api/posts", "GET /healthz", "DELETE /api/posts/{id}"];

function traces(eid: string): Stored[] {
  let list = store.get(eid);
  if (list) return list;
  const svcs = services(eid);
  const traced = svcs.filter((s, i) => isOn(eid, s, i));
  list = [];
  if (traced.length) {
    const r = rng(eid.length * 7919 + 17);
    const t1 = Date.now();
    // Denser in the last hour, a thin tail back to seven days.
    for (let i = 0; i < 260; i++) {
      const age = i < 150 ? r() * 3600e3 : i < 210 ? r() * 24 * 3600e3 : r() * 7 * 24 * 3600e3;
      const svc = traced[Math.floor(r() * traced.length)];
      const name = ROUTES[Math.floor(r() * ROUTES.length)];
      const err = r() < 0.07;
      const slow = r() < 0.1;
      const spanCount = 2 + Math.floor(r() * 9);
      list.push({
        trace_id: hex(r, 32),
        started_at: iso(t1 - age),
        duration_ms: Math.round((slow ? 500 + r() * 1800 : 3 + r() * 180) * 100) / 100,
        root_name: name,
        service_id: svc.id,
        service_name: svc.name,
        span_count: spanCount,
        error_count: err ? 1 + Math.floor(r() * 2) : 0,
        seed: Math.floor(r() * 2 ** 31),
        services: svcs,
      });
    }
    list.sort((a, b) => b.started_at.localeCompare(a.started_at));
  }
  store.set(eid, list);
  return list;
}

const SINCE: Record<string, number> = { "15m": 15 * 60e3, "1h": 3600e3, "6h": 6 * 3600e3, "24h": 24 * 3600e3, "7d": 7 * 24 * 3600e3 };

function matches(t: Stored, filter: string) {
  return filter
    .split(/\s+/)
    .filter(Boolean)
    .every((tok) => {
      const m = /^@([^:]+):(.*)$/.exec(tok);
      if (!m) return `${t.root_name} ${t.service_name}`.toLowerCase().includes(tok.toLowerCase());
      const [, k, v] = m;
      if (k === "status") return v === "error" ? t.error_count > 0 : t.error_count === 0;
      if (k === "duration") {
        const n = Number(v.replace(/^[<>]/, ""));
        return v.startsWith("<") ? t.duration_ms < n : t.duration_ms > n;
      }
      if (k === "service") return (t.service_name ?? "").includes(v);
      if (k === "http.route" || k === "name") return t.root_name.includes(v);
      return true;
    });
}

function spans(eid: string, t: Stored): TraceSpan[] {
  const r = rng(t.seed);
  const t0 = Date.parse(t.started_at);
  const others = t.services.filter((s) => s.id !== t.service_id);
  const [method, path] = t.root_name.split(" ");
  const base = (o: Partial<TraceSpan> & Pick<TraceSpan, "name" | "start_time" | "end_time" | "duration_ms">): TraceSpan => ({
    trace_id: t.trace_id,
    span_id: hex(r, 16),
    project_id: "",
    environment_id: eid,
    service_id: t.service_id,
    service_name: t.service_name,
    component: "service",
    kind: "internal",
    status: "ok",
    attributes: {},
    resource_attributes: { "service.name": t.service_name, "telemetry.sdk.language": "nodejs", "telemetry.sdk.name": "opentelemetry" },
    events: [],
    links: [],
    ...o,
  });
  const edge = base({
    name: t.root_name,
    component: "edge",
    service_name: "keel-edge",
    service_id: undefined,
    kind: "server",
    status: t.error_count ? "error" : "ok",
    start_time: iso(t0),
    end_time: iso(t0 + t.duration_ms),
    duration_ms: t.duration_ms,
    attributes: { "http.request.method": method, "url.path": path.replace("{id}", "42"), "http.route": path, "http.response.status_code": t.error_count ? 502 : 200, "client.address": "2a02:810d:4b3f::1c" },
    resource_attributes: { "service.name": "keel-edge" },
  });
  const out: TraceSpan[] = [edge];
  const srvStart = t0 + Math.min(1.2, t.duration_ms * 0.05);
  const srvDur = Math.max(0.2, t.duration_ms * 0.9);
  const server = base({
    name: t.root_name,
    parent_span_id: edge.span_id,
    kind: "server",
    status: t.error_count ? "error" : "ok",
    start_time: iso(srvStart),
    end_time: iso(srvStart + srvDur),
    duration_ms: srvDur,
    deployment_id: `dep_${(t.service_name ?? "").replace(/-/g, "").slice(0, 6)}p0`,
    replica_id: "r1",
    attributes: { "http.route": path, "http.response.status_code": t.error_count ? 500 : 200 },
    events: t.error_count
      ? [{ name: "exception", time: iso(srvStart + srvDur * 0.8), attributes: { "exception.type": "Error", "exception.message": "upstream timeout", "exception.stacktrace": "Error: upstream timeout\n    at handler (server.js:88:11)" } }]
      : [],
  });
  out.push(server);
  let cursor = srvStart + srvDur * 0.05;
  const n = Math.max(0, t.span_count - 2);
  for (let i = 0; i < n; i++) {
    const remote = others.length > 0 && r() < 0.35;
    const svc = remote ? others[Math.floor(r() * others.length)] : null;
    const d = Math.max(0.05, (srvDur * 0.8 * (0.3 + r())) / Math.max(1, n));
    const isDb = !remote && r() < 0.6;
    const failed = t.error_count > 0 && i === n - 1;
    out.push(
      base({
        name: remote ? `GET ${svc!.name}.internal/api` : isDb ? pick(r, ["SELECT posts", "SELECT users", "INSERT posts", "pg.connect"]) : pick(r, ["render", "auth.verify", "cache.get"]),
        parent_span_id: server.span_id,
        kind: remote || isDb ? "client" : "internal",
        status: failed ? "error" : "ok",
        start_time: iso(cursor),
        end_time: iso(cursor + d),
        duration_ms: d,
        attributes: isDb ? { "db.system": "postgresql", "db.statement": "SELECT * FROM posts WHERE id = $1" } : remote ? { "server.address": `${svc!.name}.internal` } : {},
        ...(svc ? { service_id: svc.id, service_name: svc.name } : {}),
      }),
    );
    cursor += d * (0.6 + r() * 0.5);
  }
  return out;
}
const pick = <T,>(r: () => number, a: T[]) => a[Math.floor(r() * a.length)];

function setting(eid: string, s: Svc, i: number): TracingSetting {
  const on = isOn(eid, s, i);
  const recent = on && (store.get(eid)?.length ?? 1) > 0;
  return {
    instance_id: s.instance_id,
    service_id: s.id,
    service_name: s.name,
    enabled: on,
    has_public_domain: s.public,
    updated_at: updated.get(`${eid}/${s.id}`) ?? null,
    last_edge_span_at: recent && s.public ? iso(Date.now() - 40_000) : null,
    last_service_span_at: recent ? iso(Date.now() - 65_000) : null,
  };
}

route("GET", "/environments/{eid}/tracing", ([eid]) => services(eid).map((s, i) => setting(eid, s, i)));
route("PUT", "/environments/{eid}/tracing", ([eid], r) => {
  const on = !!(r.body as { enabled?: boolean })?.enabled;
  const list = services(eid);
  for (const s of list) {
    enabled.set(`${eid}/${s.id}`, on);
    updated.set(`${eid}/${s.id}`, iso(Date.now()));
  }
  store.delete(eid);
  return list.map((s, i) => setting(eid, s, i));
});
route("PUT", "/services/{sid}/instances/{eid}/tracing", ([sid, eid], r) => {
  const list = services(eid);
  const i = list.findIndex((s) => s.id === sid);
  if (i < 0) throw new HttpError(404, "service not found");
  enabled.set(`${eid}/${sid}`, !!(r.body as { enabled?: boolean })?.enabled);
  updated.set(`${eid}/${sid}`, iso(Date.now()));
  store.delete(eid);
  return setting(eid, list[i], i);
});
route("GET", "/environments/{eid}/traces", ([eid], r) => {
  const since = SINCE[r.query.get("since") ?? "1h"] ?? 3600e3;
  const limit = Number(r.query.get("limit") ?? 500);
  const filter = r.query.get("filter") ?? "";
  const errors = r.query.get("errors") === "true";
  const svc = r.query.get("service");
  const t0 = Date.now() - since;
  return traces(eid)
    .filter((t) => Date.parse(t.started_at) >= t0 && (!errors || t.error_count > 0) && (!svc || t.service_id === svc) && matches(t, filter))
    .slice(0, limit)
    .map(({ seed: _s, services: _v, ...t }) => t);
});
route("GET", "/environments/{eid}/traces/{tid}", ([eid, tid]) => {
  const t = traces(eid).find((x) => x.trace_id === tid);
  if (!t) throw new HttpError(404, "trace not found");
  return spans(eid, t);
});
route("GET", "/environments/{eid}/trace-attributes", () => ["http.route", "http.request.method", "http.response.status_code", "db.system", "db.statement", "server.address", "client.address"]);
