"use client";

import Link from "next/link";
import { Suspense, useEffect, useMemo, useState, type ReactNode } from "react";
import { ChartColumn, ChevronDown, Gauge } from "lucide-react";
import type { UsageGroupBy, UsageTotals } from "@/lib/api";
import { useCaps, useProjectUsage, useWorkspaceUsage } from "@/lib/queries";
import { useCtxHref } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { t } from "@/lib/i18n";
import { LineChart, SERIES_COLORS } from "@/components/charts/LineChart";
import { CenterSpinner, EmptyState, ErrorBox, Segmented, Select, cx } from "@/components/ui";
import { UsageLimitBanner } from "@/components/UsageLimit";

type RangeKey = "month" | "last_month" | "30d" | "90d";
const RANGES: { value: RangeKey; label: string }[] = [
  { value: "month", label: t("이번 달") },
  { value: "last_month", label: t("지난 달") },
  { value: "30d", label: t("최근 30일") },
  { value: "90d", label: t("최근 90일") },
];

const day = (d: Date) => d.toISOString().slice(0, 10);

/** from/to as UTC dates; `to` is inclusive (the server reads a date `to` as the end of that day). */
function rangeDates(k: RangeKey): { from: string; to: string } {
  const now = new Date();
  const y = now.getUTCFullYear();
  const m = now.getUTCMonth();
  switch (k) {
    case "last_month":
      return { from: day(new Date(Date.UTC(y, m - 1, 1))), to: day(new Date(Date.UTC(y, m, 0))) };
    case "30d":
      return { from: day(new Date(now.getTime() - 29 * 86400_000)), to: day(now) };
    case "90d":
      return { from: day(new Date(now.getTime() - 89 * 86400_000)), to: day(now) };
  }
  return { from: day(new Date(Date.UTC(y, m, 1))), to: day(now) };
}

type MetricKey = "cpu_core_hours" | "memory_gb_hours" | "net_egress_gb" | "disk_gb_hours";
const METRICS: { value: MetricKey; label: string; unit: string }[] = [
  { value: "cpu_core_hours", label: "CPU", unit: t("코어·시간") },
  { value: "memory_gb_hours", label: t("메모리"), unit: t("GB·시간") },
  { value: "net_egress_gb", label: t("송신"), unit: "GB" },
  { value: "disk_gb_hours", label: t("디스크"), unit: t("GB·시간") },
];

const num = (v: number) => (v === 0 ? "0" : v < 0.01 ? v.toFixed(4) : v < 10 ? v.toFixed(2) : v < 1000 ? v.toFixed(1) : Math.round(v).toLocaleString());
const money = (v: number) => (v < 0.01 && v > 0 ? v.toFixed(4) : v.toFixed(2));

function Tile({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="rounded-lg border border-gray-200 bg-gray-50 px-4 py-5">
      <div className="text-sm text-gray-600">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums leading-8">{value}</div>
      {sub && <div className="mt-0.5 text-xs text-gray-500">{sub}</div>}
    </div>
  );
}

function UsageTable({ head, rows, cost }: { head: string; rows: ({ key: string; name: string } & UsageTotals)[]; cost: boolean }) {
  const max = Math.max(1e-9, ...rows.map((r) => r.cpu_core_hours));
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[620px] text-sm">
        <thead>
          <tr className="text-left text-xs text-gray-600">
            <th className="pb-2 font-semibold">{head}</th>
            <th className="pb-2 text-right font-semibold">{t("CPU (코어·h)")}</th>
            <th className="pb-2 text-right font-semibold">{t("메모리 (GB·h)")}</th>
            <th className="pb-2 text-right font-semibold">{t("송신 (GB)")}</th>
            <th className="pb-2 text-right font-semibold">{t("디스크 (GB·h)")}</th>
            {cost && <th className="pb-2 text-right font-semibold">{t("추정 비용")}</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key} className="border-t border-gray-200">
              <td className="py-2 pr-3">
                <div className="truncate font-medium">{r.name || r.key}</div>
                <div className="mt-1 h-[3px] w-full max-w-[160px] rounded-full bg-gray-100" aria-hidden="true">
                  <div className="h-full rounded-full bg-pink-500" style={{ width: `${(r.cpu_core_hours / max) * 100}%` }} />
                </div>
              </td>
              <td className="py-2 text-right tabular-nums">{num(r.cpu_core_hours)}</td>
              <td className="py-2 text-right tabular-nums">{num(r.memory_gb_hours)}</td>
              <td className="py-2 text-right tabular-nums">{num(r.net_egress_gb)}</td>
              <td className="py-2 text-right tabular-nums">{num(r.disk_gb_hours)}</td>
              {cost && <td className="py-2 text-right tabular-nums">{r.estimated_cost ? money(r.estimated_cost.total) : "—"}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Section({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-gray-200 bg-secondaryBg">
      <header className="flex flex-wrap items-center gap-2 border-b border-gray-200 px-4 py-3">
        <h2 className="text-sm font-semibold">{title}</h2>
        <span className="ml-auto">{aside}</span>
      </header>
      <div className="p-4">{children}</div>
    </section>
  );
}

function UsageInner() {
  const ws = useWorkspace();
  const href = useCtxHref();
  const caps = useCaps(ws.pid);
  const [rk, setRk] = useState<RangeKey>("month");
  const [group, setGroup] = useState<UsageGroupBy>("service");
  const [metric, setMetric] = useState<MetricKey>("cpu_core_hours");
  const { from, to } = useMemo(() => rangeDates(rk), [rk]);
  const usage = useProjectUsage(ws.pid, from, to, group);
  const wsUsage = useWorkspaceUsage(from, to, caps.instanceAdmin);
  useEffect(() => {
    document.title = `${t("사용량")} · aven`;
    return () => {
      document.title = "aven";
    };
  }, []);
  const u = usage.data;
  const hasCost = !!u?.totals.estimated_cost;
  const m = METRICS.find((x) => x.value === metric)!;
  const series = useMemo(
    () => [{ name: m.label, color: SERIES_COLORS[0], points: (u?.series ?? []).map((p) => ({ t: Date.parse(p.t), v: p[metric] })) }],
    [u, metric, m.label],
  );
  const alert = u?.usage_alert;
  const pct = (used: number, lim?: number) => (lim ? Math.min(999, Math.round((used / lim) * 100)) : null);

  const [detail, setDetail] = useState(false);
  const [openP, setOpenP] = useState<string | null>(null);
  const rangeLabel = RANGES.find((r) => r.value === rk)?.label ?? "";
  const rowCls = "flex items-center justify-between border-b border-gray-200 py-3 text-sm last:border-0";
  return (
    <div className="min-h-full">
      <div className="mx-auto flex min-h-full max-w-[1120px] flex-col px-6 pt-12 sm:px-12">
        <div className="flex items-center gap-4 border-b border-gray-200 pb-8">
          <h1 className="flex-1 text-h2 font-normal">{t("사용량")}</h1>
          <Select aria-label={t("기간")} value={rk} onChange={(e) => setRk(e.target.value as RangeKey)} className="w-[160px]">
            {RANGES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-8 py-8">
          <div className="flex items-center">
            <h2 className="text-[17px] font-semibold leading-6">{t("{range} 사용량", { range: rangeLabel })}</h2>
            <button type="button" onClick={() => setDetail((d) => !d)} className="ml-auto cursor-pointer text-sm font-medium text-pink-600 hover:underline">
              {detail ? t("상세 숨기기") : t("상세 보기")}
            </button>
          </div>
          {ws.pid && <UsageLimitBanner pid={ws.pid} canAdmin={caps.canAdmin} />}
          <ErrorBox error={usage.error} />
          {!u ? (
            usage.isPending && ws.pid ? <CenterSpinner /> : null
          ) : (
            <>
              <div className="grid gap-6 rounded-lg border border-gray-200 p-6 lg:grid-cols-2">
                <div className="flex flex-col justify-end">
                  <div className={rowCls}>
                    <span className="text-gray-600">CPU</span>
                    <span className="tabular-nums">{t("{value} 코어·시간", { value: num(u.totals.cpu_core_hours) })}</span>
                  </div>
                  <div className={rowCls}>
                    <span className="text-gray-600">{t("메모리")}</span>
                    <span className="tabular-nums">{t("{value} GB·시간", { value: num(u.totals.memory_gb_hours) })}</span>
                  </div>
                  <div className={rowCls}>
                    <span className="text-gray-600">{t("송신 트래픽")}</span>
                    <span className="tabular-nums">{t("{value} GB", { value: num(u.totals.net_egress_gb) })}</span>
                  </div>
                  <div className={rowCls}>
                    <span className="text-gray-600">{t("디스크")}</span>
                    <span className="tabular-nums">{t("{value} GB·시간", { value: num(u.totals.disk_gb_hours) })}</span>
                  </div>
                </div>
                <div className="space-y-4">
                  <div className="grid grid-cols-2 gap-4">
                    <Tile label={hasCost ? t("추정 비용") : "CPU"} value={hasCost ? `$ ${money(u.totals.estimated_cost!.total)}` : num(u.totals.cpu_core_hours)} />
                    <Tile label={t("메모리")} value={num(u.totals.memory_gb_hours)} />
                  </div>
                  {[
                    [t("CPU 알림 기준"), u.totals.cpu_core_hours, alert?.cpu_core_hours],
                    [t("메모리 알림 기준"), u.totals.memory_gb_hours, alert?.memory_gb_hours],
                    [t("송신 알림 기준"), u.totals.net_egress_gb, alert?.egress_gb],
                  ].map(([l, used, lim]) =>
                    lim ? (
                      <div key={l as string}>
                        <div className="mb-1 flex justify-between text-xs">
                          <span className="font-medium">{l as string}</span>
                          <span className="tabular-nums text-gray-500">
                            {num(used as number)} / {num(lim as number)}
                          </span>
                        </div>
                        <div className="h-2 rounded-full bg-gray-100">
                          <div className="h-full rounded-full bg-pink-500" style={{ width: `${Math.min(100, ((used as number) / (lim as number)) * 100)}%` }} />
                        </div>
                      </div>
                    ) : null,
                  )}
                  {caps.canAdmin && (
                    <Link
                      href={href("/settings/", { section: "usage" })}
                      className="flex h-[34px] items-center justify-center gap-2 rounded-md border border-gray-200 bg-gray-100 text-sm font-medium hover:border-gray-300 hover:bg-gray-200"
                    >
                      <Gauge className="size-4" />{t("사용량 한도 설정")}
                    </Link>
                  )}
                </div>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-4 rounded-lg border border-pink-200 px-6 py-5">
                <div>
                  <div className="text-base font-medium">aven {t("셀프호스팅")}</div>
                  <div className="text-xs text-gray-500">{t("서버 자원 기준 사용량입니다.")}</div>
                </div>
                <div className="text-sm tabular-nums text-pink-600">{ws.detail?.name ?? t("프로젝트")} · {from} ~ {to} (UTC)</div>
              </div>
              {detail && (
                <>
                  <Section
                    title={t("일별 사용량")}
                    aside={<Segmented size="sm" label={t("지표")} value={metric} onChange={setMetric} options={METRICS.map((x) => ({ value: x.value, label: x.label }))} />}
                  >
                    <LineChart series={series} bars format={(v) => `${num(v)}`} height={220} emptyText={t("이 기간에 집계된 사용량이 없습니다")} />
                    <p className="mt-2 text-xs text-gray-500">{t("{unit} · 지표 점(7일 보관)을 매시 집계해 400일 동안 보관합니다.", { unit: m.unit })}</p>
                  </Section>
                  <Section
                    title={t("내역")}
                    aside={
                      <Segmented
                        size="sm"
                        label={t("묶음")}
                        value={group}
                        onChange={setGroup}
                        options={[
                          { value: "service", label: t("서비스") },
                          { value: "environment", label: t("환경") },
                          { value: "day", label: t("일") },
                        ]}
                      />
                    }
                  >
                    {u.groups.length === 0 ? (
                      <EmptyState icon={<ChartColumn className="size-5" />} title={t("이 기간에 집계된 사용량이 없습니다")} />
                    ) : (
                      <UsageTable head={group === "service" ? t("서비스") : group === "environment" ? t("환경") : t("날짜")} rows={u.groups} cost={hasCost} />
                    )}
                  </Section>
                </>
              )}
            </>
          )}
          {caps.instanceAdmin && (
            <div>
              <h2 className="mb-4 text-[17px] font-semibold leading-6">{t("프로젝트별 사용량")}</h2>
              <ErrorBox error={wsUsage.error} />
              {wsUsage.isPending ? (
                <CenterSpinner />
              ) : wsUsage.data && wsUsage.data.projects.length > 0 ? (
                <ul className="divide-y divide-gray-200 rounded-lg border border-gray-200 bg-secondaryBg">
                  {wsUsage.data.projects.map((p) => (
                    <li key={p.key}>
                      <button type="button" onClick={() => setOpenP((o) => (o === p.key ? null : p.key))} className="flex w-full cursor-pointer items-center px-4 py-3 text-left hover:bg-gray-100/50">
                        <span className="flex-1 truncate text-base font-medium">{p.name || p.key}</span>
                        <span className="mr-4 text-right">
                          <span className="block text-xs text-gray-500">{p.estimated_cost ? t("현재 비용") : t("CPU 코어·시간")}</span>
                          <span className="text-base font-semibold tabular-nums">{p.estimated_cost ? `$${money(p.estimated_cost.total)}` : num(p.cpu_core_hours)}</span>
                        </span>
                        <ChevronDown className={cx("size-4 text-gray-500 transition-transform", openP === p.key && "rotate-180")} />
                      </button>
                      {openP === p.key && (
                        <div className="px-4 pb-4">
                          <UsageTable head={t("프로젝트")} rows={[p]} cost={!!p.estimated_cost} />
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <EmptyState icon={<ChartColumn className="size-5" />} title={t("이 기간에 집계된 사용량이 없습니다")} />
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

export default function UsagePage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <UsageInner />
    </Suspense>
  );
}
