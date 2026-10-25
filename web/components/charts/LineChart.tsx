"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { cx } from "../ui";
import { t } from "@/lib/i18n";

export type Series = { name: string; color: string; points: { t: number; v: number }[] };

export const SERIES_COLORS = ["var(--pink-500)", "var(--blue-500)", "var(--green-500)", "var(--red-300)", "var(--yellow-400)", "var(--cyan-500)"];
export const OTHER_SERIES = "var(--gray-400)";
export const ACCENT_LINE = "var(--pink-500)";

/** d3 `tickIncrement`-style step (1/2/5 × 10^n) for about `count` intervals over [0, max]. */
function tickStep(max: number, count: number) {
  const raw = max / count;
  const power = Math.floor(Math.log10(raw));
  const err = raw / 10 ** power;
  return (err >= Math.sqrt(50) ? 10 : err >= Math.sqrt(10) ? 5 : err >= Math.SQRT2 ? 2 : 1) * 10 ** power;
}
const range = (top: number, step: number) => Array.from({ length: Math.round(top / step) + 1 }, (_, i) => +(i * step).toPrecision(12));

const T_STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 180, 360, 720, 1440, 2880, 10080].map((m) => m * 60_000);
function timeTicks(t0: number, t1: number) {
  const span = t1 - t0;
  const step = T_STEPS.find((s) => span / s <= 5) ?? T_STEPS[T_STEPS.length - 1];
  // Align to local wall-clock multiples (14:00, 14:05, …; midnight for day steps).
  const off = new Date(t0).getTimezoneOffset() * 60_000;
  const out: number[] = [];
  for (let ms = Math.ceil((t0 - off) / step) * step + off; ms <= t1; ms += step) out.push(ms);
  return out;
}

const p2 = (n: number) => String(n).padStart(2, "0");
function timeLabel(ms: number, span: number) {
  const d = new Date(ms);
  if (span > 2 * 24 * 3600_000) return `${d.getMonth() + 1}/${d.getDate()}`;
  return `${p2(d.getHours())}:${p2(d.getMinutes())}`;
}
function tipTime(ms: number) {
  const d = new Date(ms);
  return t("{month}월 {day}일 {time}",{ month: d.getMonth() + 1, day: d.getDate(), time: `${p2(d.getHours())}:${p2(d.getMinutes())}` });
}

const PAD = { l: 46, r: 0, t: 0, b: 23 };

export function LineChart({
  series,
  format,
  height = 200,
  minMax,
  area,
  bars,
  step,
  grid = true,
  emptyText = t("데이터가 없습니다"),
  className,
}: {
  series: Series[];
  format: (v: number) => string;
  height?: number;
  /** Smallest y-axis maximum, so near-zero series don't fill the plot. */
  minMax?: number;
  area?: boolean;
  /** Draw grouped bars per time slot instead of lines (v1.5 usage page). */
  bars?: boolean;
  step?: boolean;
  grid?: boolean;
  emptyText?: string;
  className?: string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(0);
  const [hover, setHover] = useState<number | null>(null);
  const gid = useId().replace(/:/g, "");

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const all = series.flatMap((s) => s.points);
  const geo = useMemo(() => {
    if (!all.length) return null;
    const t0 = Math.min(...all.map((p) => p.t));
    const t1 = Math.max(...all.map((p) => p.t));
    const vmax = Math.max(minMax ?? 0, Math.max(...all.map((p) => p.v)) * 1.2) || 1;
    const gstep = tickStep(vmax, 10);
    const top = Math.ceil(vmax / gstep - 1e-9) * gstep;
    const ticks = range(top, tickStep(top, 5));
    const gridTicks = range(top, tickStep(top, 10));
    const iw = Math.max(10, w - PAD.l - PAD.r);
    const ih = height - PAD.t - PAD.b;
    // Bars: every time slot gets an equal share of the width and points sit in the middle of their slot.
    const slot = bars ? iw / Math.max(1, new Set(all.map((p) => p.t)).size) : 0;
    const span = iw - slot;
    const x = (ms: number) => PAD.l + slot / 2 + (t1 === t0 ? span / 2 : ((ms - t0) / (t1 - t0)) * span);
    const y = (v: number) => PAD.t + ih - (v / top) * ih;
    return { t0, t1, top, ticks, gridTicks, iw, ih, x, y, slot };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [series, w, height, minMax, bars]);

  const times = useMemo(() => [...new Set(all.map((p) => p.t))].sort((a, b) => a - b), [all]);

  if (!geo || w === 0)
    return (
      <div ref={box} className={cx("relative", className)} style={{ height }}>
        {w > 0 && <div className="flex h-full items-center justify-center text-sm text-gray-500">{emptyText}</div>}
      </div>
    );

  const { x, y, ticks, gridTicks, t0, t1, ih } = geo;
  const xt = t1 > t0 ? timeTicks(t0, t1) : [t0];
  const ht = hover !== null ? times[hover] : null;

  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - r.left;
    if (px < PAD.l - 4 || px > w - PAD.r + 4) return setHover(null);
    const at = t0 + ((px - PAD.l) / geo.iw) * (t1 - t0);
    let best = 0;
    for (let i = 1; i < times.length; i++) if (Math.abs(times[i] - at) < Math.abs(times[best] - at)) best = i;
    setHover(best);
  };

  const tipLeft = ht !== null ? x(ht) : 0;
  const flip = tipLeft > w - 200;

  return (
    <div ref={box} className={cx("relative select-none", className)} style={{ height }}>
      <svg
        width={w}
        height={height - 1}
        onPointerMove={onMove}
        onPointerLeave={() => setHover(null)}
        role="img"
        aria-label={t("{names} 그래프", { names: series.map((s) => s.name).join(", ") })}
        className="block cursor-crosshair touch-none"
      >
        <defs>
          {series.map((s, i) => (
            <linearGradient key={i} id={`${gid}-g${i}`} x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor={s.color} stopOpacity={0.32} />
              <stop offset="100%" stopColor={s.color} stopOpacity={0.02} />
            </linearGradient>
          ))}
        </defs>
        {grid &&
          gridTicks.map((v) => (
            <line key={v} x1={PAD.l} x2={w} y1={y(v)} y2={y(v)} stroke="var(--gray-100)" strokeOpacity={0.4} strokeWidth={1} shapeRendering="crispEdges" />
          ))}
        {xt.map((ms) => (
          <text key={ms} x={x(ms)} y={PAD.t + ih + 1 + 19} textAnchor="middle" fontSize={11} fill="var(--gray-500)" className="tabular-nums">
            {timeLabel(ms, t1 - t0)}
          </text>
        ))}
        <line x1={PAD.l + 0.5} x2={w} y1={PAD.t + ih + 1} y2={PAD.t + ih + 1} stroke="var(--gray-200)" strokeWidth={1} shapeRendering="crispEdges" />
        {ticks.map((v) => (
          <text key={v} x={4} y={y(v) - 4} textAnchor="start" fontSize={11} fill="var(--gray-500)" className="tabular-nums">
            {format(v)}
          </text>
        ))}
        {bars &&
          series.map((s, i) => {
            const group = Math.max(2, geo.slot * 0.72);
            const bw = group / series.length;
            return (
              <g key={s.name}>
                {s.points.map((p) => (
                  <rect
                    key={p.t}
                    x={x(p.t) - group / 2 + i * bw}
                    y={y(p.v)}
                    width={Math.max(1, bw - (series.length > 1 ? 1 : 0))}
                    height={Math.max(0, PAD.t + ih - y(p.v))}
                    rx={Math.min(2, bw / 4)}
                    fill={s.color}
                    opacity={hover !== null && times[hover] !== p.t ? 0.55 : 1}
                  />
                ))}
              </g>
            );
          })}
        {!bars && series.map((s, i) => {
          if (!s.points.length) return null;
          const line = s.points
            .map((p, j) => {
              const px = x(p.t).toFixed(1);
              const py = y(p.v).toFixed(1);
              return j === 0 ? `M${px} ${py}` : step ? `H${px}V${py}` : `L${px} ${py}`;
            })
            .join("");
          const fill = `${line}L${x(s.points[s.points.length - 1].t).toFixed(1)} ${PAD.t + ih}L${x(s.points[0].t).toFixed(1)} ${PAD.t + ih}Z`;
          return (
            <g key={s.name}>
              {area && <path d={fill} fill={`url(#${gid}-g${i})`} />}
              <path d={line} fill="transparent" stroke={s.color} strokeWidth={2} strokeLinecap="round" />
            </g>
          );
        })}
        {ht !== null && (
          <g pointerEvents="none">
            <line x1={x(ht)} x2={x(ht)} y1={PAD.t} y2={PAD.t + ih} stroke="var(--gray-400)" strokeWidth={1} strokeDasharray="3 3" />
            {!bars && series.map((s) => {
              const p = s.points.find((q) => q.t === ht);
              return p ? <circle key={s.name} cx={x(p.t)} cy={y(p.v)} r={4} fill={s.color} strokeWidth={2} className="stroke-background dark:stroke-secondaryBg" /> : null;
            })}
          </g>
        )}
      </svg>
      {ht !== null && (
        <div
          className="pointer-events-none absolute top-1 z-10 min-w-[150px] rounded-md border border-gray-200 bg-gray-50/95 px-3 py-2 text-xs shadow-[0_12px_32px_-8px_rgba(0,0,0,0.12)] dark:shadow-[0_12px_32px_-8px_rgba(0,0,0,0.7)]"
          style={flip ? { right: w - tipLeft + 12 } : { left: tipLeft + 12 }}
        >
          <div className="mb-1 text-gray-500">{tipTime(ht)}</div>
          {series.map((s) => {
            const p = s.points.find((q) => q.t === ht);
            if (!p) return null;
            return (
              <div key={s.name} className="flex items-center gap-2">
                <span className="size-2 shrink-0 rounded-full" style={{ background: s.color }} />
                {series.length > 1 && <span className="min-w-0 flex-1 truncate text-gray-600">{s.name}</span>}
                <span className="ml-auto font-medium tabular-nums text-foreground">{format(p.v)}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

export function Legend({ series, className = "px-4" }: { series: Pick<Series, "name" | "color">[]; className?: string }) {
  return (
    <div className={cx("flex flex-wrap justify-start gap-x-3 gap-y-1.5", series.length > 7 && "[&_span]:text-[10px]", className)}>
      {series.map((s) => (
        <span key={s.name} className="flex shrink-0 select-none items-center space-x-1" style={{ color: s.color }}>
          <span className="h-2 w-2 rounded-full border border-current bg-current" />
          <span className="text-xs text-neutral">{s.name}</span>
        </span>
      ))}
    </div>
  );
}
