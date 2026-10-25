import type { MetricPoint } from "./api";
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

export const fmtPct = (v: number) => (v === 0 ? "0%" : v < 1 ? `${v.toFixed(2)}%` : v < 10 ? `${v.toFixed(1)}%` : `${Math.round(v)}%`);
export const fmtMem = (v: number) => fmtBytes(v);
export const fmtRate = (v: number) => `${fmtBytes(v)}/s`;
