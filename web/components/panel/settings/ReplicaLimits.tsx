"use client";

import type { ServiceInstance } from "@/lib/api";
import { t } from "@/lib/i18n";
import { useNodes } from "@/lib/queries";
import { Hint } from "./primitives";

/**
 * Replica limits, modeled on Railway's dashboard: one bordered card with a CPU and a Memory slider (`border border-gray-200 rounded-md`,
 * rows `p-4 space-y-2`, header `h-5` with value and plan limit). keel's "plan limit" is the node capacity; the slider at
 * its maximum means no limit (the container may use the whole node).
 */
function Slider({
  label,
  value,
  min,
  max,
  step,
  onChange,
  valueText,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  step: number;
  onChange: (v: number) => void;
  valueText: string;
}) {
  const pct = max > min ? Math.min(1, Math.max(0, (value - min) / (max - min))) : 1;
  const left = `calc(${pct * 100}% - ${pct * 8}px)`;
  return (
    <div role="group" aria-label={label} className="relative flex w-full items-center">
      <div className="relative flex h-5 grow cursor-pointer touch-none items-center">
        <div className="h-2 w-full rounded-full bg-black/15 dark:bg-gray-200">
          <div className="h-full rounded-full bg-pink-500 dark:bg-pink-300" style={{ width: left }} />
        </div>
        <div className="pointer-events-none absolute top-1/2 -translate-y-1/2" style={{ left }}>
          <div className="block size-4 -translate-x-1/2 rounded border-2 border-pink-500 bg-background dark:border-white dark:bg-pink-300" />
        </div>
        <input
          type="range"
          min={min}
          max={max}
          step={step}
          value={value}
          aria-label={label}
          aria-valuetext={valueText}
          onChange={(e) => onChange(Number(e.target.value))}
          className="absolute inset-0 h-full w-full cursor-pointer opacity-0 disabled:cursor-not-allowed"
        />
      </div>
    </div>
  );
}

const fmtMem = (mb: number) => (mb >= 1024 ? `${Math.round((mb / 1024) * 10) / 10} GB` : `${mb} MB`);
const fmtCpu = (c: number) => `${Math.round(c * 100) / 100} vCPU`;

export function ReplicaLimits({
  draft,
  setMemory,
  setCpu,
}: {
  draft: ServiceInstance;
  setMemory: (v: number | null) => void;
  setCpu: (v: number | null) => void;
}) {
  const nodes = useNodes();
  const ids = draft.placement?.length ? draft.placement.map((p) => p.node_id) : [draft.node_id];
  const used = (nodes.data ?? []).filter((n) => ids.includes(n.id));
  const cores = used.length ? Math.min(...used.map((n) => n.cpu_cores)) : 8;
  const memMb = used.length ? Math.floor(Math.min(...used.map((n) => n.memory_bytes)) / 1024 / 1024) : 8192;
  const cpuMax = Math.max(1, cores);
  const memMax = Math.max(256, Math.floor(memMb / 128) * 128);
  const cpu = draft.cpu_limit ?? cpuMax;
  const mem = draft.memory_limit_mb ?? memMax;
  const memLow = draft.memory_limit_mb != null && draft.memory_limit_mb < 6;
  return (
    <div className="flex flex-col space-y-4">
      <div className="rounded-md border border-gray-200">
        <div className="flex flex-col space-y-2 rounded-t-md p-4">
          <div className="flex h-5 items-center justify-between">
            <p className="text-sm text-gray-600">
              CPU: <span className="font-semibold text-foreground">{draft.cpu_limit == null ? `${fmtCpu(cpuMax)}` : fmtCpu(cpu)}</span>
            </p>
            <p className="text-sm text-gray-600">{t("노드 한도: {limit}", { limit: fmtCpu(cpuMax) })}</p>
          </div>
          <Slider
            label={t("CPU 제한")}
            min={0.25}
            max={cpuMax}
            step={0.25}
            value={Math.min(cpu, cpuMax)}
            valueText={draft.cpu_limit == null ? t("제한 없음") : fmtCpu(cpu)}
            onChange={(v) => setCpu(v >= cpuMax ? null : v)}
          />
        </div>
        <hr className="my-0 w-full border-t border-gray-100" />
        <div className="flex flex-col space-y-2 rounded-b-md p-4">
          <div className="flex h-5 items-center justify-between">
            <p className="text-sm text-gray-600">
              {t("메모리")}: <span className="font-semibold text-foreground">{fmtMem(Math.min(mem, memMax))}</span>
            </p>
            <p className="text-sm text-gray-600">{t("노드 한도: {limit}", { limit: fmtMem(memMax) })}</p>
          </div>
          <Slider
            label={t("메모리 제한")}
            min={128}
            max={memMax}
            step={128}
            value={Math.min(mem, memMax)}
            valueText={draft.memory_limit_mb == null ? t("제한 없음") : fmtMem(mem)}
            onChange={(v) => setMemory(v >= memMax ? null : v)}
          />
        </div>
      </div>
      {memLow && <Hint tone="danger" className="!mt-0">{t("메모리는 6MiB 이상이어야 컨테이너가 시작됩니다.")}</Hint>}
    </div>
  );
}
