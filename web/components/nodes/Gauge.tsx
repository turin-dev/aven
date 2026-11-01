"use client";

import type { ReactNode } from "react";
import { cx } from "../ui";
import { usageLevel, type UsageLevel } from "@/lib/metrics";

const BAR: Record<UsageLevel, string> = { ok: "bg-pink-500", warn: "bg-yellow-500", danger: "bg-red-500" };
const TEXT: Record<UsageLevel, string> = { ok: "text-foreground", warn: "text-warn", danger: "text-danger" };

/**
 * Horizontal usage meter (CPU, memory, …). `pct` is 0..100 or null when unknown; `value` is the text
 * next to the label (with its unit), `sub` the smaller capacity text. `level` overrides the colour
 * derived from `pct` (e.g. disk free space, where a lower value is worse).
 */
export function Gauge({
  icon,
  label,
  pct,
  value,
  sub,
  level,
  bar = true,
  className,
}: {
  icon?: ReactNode;
  label: string;
  pct: number | null;
  value: ReactNode;
  sub?: ReactNode;
  level?: UsageLevel;
  /** Hide the bar when there is no meaningful total (e.g. free disk without a disk size). */
  bar?: boolean;
  className?: string;
}) {
  const lv = level ?? usageLevel(pct);
  const width = pct === null ? 0 : Math.max(pct > 0 ? 2 : 0, Math.min(100, pct));
  return (
    <div className={cx("min-w-0", className)}>
      <div className="flex items-baseline gap-1.5 text-xs text-gray-500">
        {icon ? <span className="flex shrink-0 items-center self-center">{icon}</span> : null}
        <span className="truncate">{label}</span>
        <span className={cx("ml-auto shrink-0 text-sm font-medium tabular-nums", TEXT[lv])}>{value}</span>
      </div>
      {bar ? <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct === null ? undefined : Math.round(pct)}
        aria-valuetext={typeof value === "string" ? value : undefined}
        className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-gray-100"
      >
        <div className={cx("h-full rounded-full transition-[width] duration-500 motion-reduce:transition-none", BAR[lv])} style={{ width: `${width}%` }} />
      </div> : null}
      {sub ? <div className="mt-1 truncate text-xs tabular-nums text-gray-500">{sub}</div> : null}
    </div>
  );
}
