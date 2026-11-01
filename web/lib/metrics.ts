import type { MetricPoint, NodeMetricPoint } from "./api";
import { fmtBytes } from "./format";

export const pts = (ps: MetricPoint[], f: (p: MetricPoint) => number | null) =>
  ps.filter((p) => f(p) !== null).map((p) => ({ t: Date.parse(p.t), v: f(p) as number }));

/** Network counters are cumulative; convert to bytes per second between samples. */
export function rate(ps: MetricPoint[], f: (p: MetricPoint) => number) {
  const out: { t: number; v: number }[] = [];
  for (let i = 1; i < ps.length; i++) {
    const dt = (Date.parse(ps[i].t) - Date.parse(ps[i - 1].t)) / 1000;
    const dv = f(ps[i]) - f(ps[i - 1]);
    out.push({ t: Date.parse(ps[i].t), v: dt > 0 && dv >= 0 ? dv / dt : 0 });
  }
  return out;
}

export const fmtPct = (v: number) =>
  !Number.isFinite(v) ? "—" : v === 0 ? "0%" : v < 1 ? `${v.toFixed(2)}%` : v < 10 ? `${v.toFixed(1)}%` : `${Math.round(v)}%`;
export const fmtMem = (v: number) => fmtBytes(v);
export const fmtRate = (v: number) => `${fmtBytes(v)}/s`;

/* ── node (host) usage ──────────────────────────────────────────────── */

/** Clamp to 0..100; non-finite values become null so callers show "—" instead of "NaN%". */
export function clampPct(v: number | null | undefined): number | null {
  if (v === null || v === undefined || !Number.isFinite(v)) return null;
  return Math.min(100, Math.max(0, v));
}

/** Share of `total` used, in percent (null when the total is unknown). */
export function usedPct(used: number | null | undefined, total: number | null | undefined): number | null {
  if (!total || total <= 0 || used === null || used === undefined) return null;
  return clampPct((used / total) * 100);
}

export type UsageLevel = "ok" | "warn" | "danger";

/** Thresholds shared by the node gauges and the admin overview. */
export const USAGE_WARN = 75;
export const USAGE_DANGER = 90;
/** Free disk below this is flagged (same threshold as 어드민 › 시스템 상태). */
export const LOW_DISK_BYTES = 10e9;
export const CRITICAL_DISK_BYTES = 2e9;

export function usageLevel(pct: number | null | undefined): UsageLevel {
  if (pct === null || pct === undefined) return "ok";
  return pct >= USAGE_DANGER ? "danger" : pct >= USAGE_WARN ? "warn" : "ok";
}

export function diskLevel(free: number | null | undefined): UsageLevel {
  if (free === null || free === undefined || !Number.isFinite(free)) return "ok";
  return free < CRITICAL_DISK_BYTES ? "danger" : free < LOW_DISK_BYTES ? "warn" : "ok";
}

/**
 * Latest node sample. `cpu_percent` of node metrics is whole-node utilisation (0..100, the same scale
 * aven-server's sysstat and aven-agent's node.stats report), so it must not be divided by the core count.
 */
export function nodeUsage(points: NodeMetricPoint[] | undefined, memoryTotal: number) {
  const last = points?.length ? points[points.length - 1] : undefined;
  if (!last) return null;
  return {
    at: last.t,
    cpu: clampPct(last.cpu_percent),
    memory: last.memory_bytes,
    memoryPct: usedPct(last.memory_bytes, memoryTotal),
    diskFree: last.disk_free_bytes,
  };
}
