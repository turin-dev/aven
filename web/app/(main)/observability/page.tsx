"use client";

import Link from "next/link";
import { Suspense, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, ArrowUpRight } from "lucide-react";
import { METRIC_RANGES, isTerminal, sse, type CanvasService, type EnvLogLine, type MetricPoint, type MetricRange } from "@/lib/api";
import { useCanvas, useCaps, useEnvMetrics, useEvents } from "@/lib/queries";
import { fmtRelative, fmtTime } from "@/lib/format";
import { fmtMem, fmtPct, pts } from "@/lib/metrics";
import { canvasHref, ctxFrom, useCtxHref } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { useLogStream, useParsedLines } from "@/lib/useLogStream";
import { Legend, LineChart, OTHER_SERIES, SERIES_COLORS, type Series } from "@/components/charts/LineChart";
import { eventIcon } from "@/components/canvas/ActivityPanel";
import { RANGE_LABEL } from "@/components/panel/MetricsTab";
import { BrandIcon, iconKey } from "@/components/ServiceIcon";
import { StatusDot } from "@/components/StatusBadge";
import { AlertRulesSection } from "@/components/AlertRules";
import { DashboardSelector, DashboardView, useDashboards } from "@/components/Dashboards";
import { ErrorBox, Segmented, Spinner, cx } from "@/components/ui";
import { useRouter, useSearchParams } from "next/navigation";
import { t } from "@/lib/i18n";

function Widget({ title, aside, children, className }: { title: string; aside?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("flex min-w-0 flex-col rounded-lg border border-gray-200 bg-background dark:bg-secondaryBg", className)}>
      <header className="flex h-11 shrink-0 items-center gap-2 px-4">
        <h2 className="min-w-0 truncate text-sm font-semibold">{title}</h2>
        <span className="ml-auto shrink-0 text-xs text-gray-500">{aside}</span>
      </header>
      <div className="min-h-0 flex-1 px-4 pb-4">{children}</div>
    </section>
  );
}

const TILE_DOT = { danger: "bg-red-500", warning: "bg-yellow-500", success: "bg-green-500" } as const;

/** One cell of the stat strip: label with a status dot, big number. */
function Tile({ label, value, tone }: { label: string; value: ReactNode; tone?: "danger" | "warning" | "success" }) {
  return (
    <div className="min-w-0 px-4 py-3">
      <div className="flex items-center gap-1.5 text-sm text-gray-600">
        <span className={cx("size-1.5 shrink-0 rounded-full", tone ? TILE_DOT[tone] : "bg-gray-300")} aria-hidden="true" />
        {label}
      </div>
      <div className={cx("mt-1 text-2xl font-semibold tabular-nums leading-8", tone === "danger" && "text-danger")}>{value}</div>
    </div>
  );
}

const avg = (ps: MetricPoint[], f: (p: MetricPoint) => number) => (ps.length ? ps.reduce((a, p) => a + f(p), 0) / ps.length : 0);

/** One series per service in a fixed color order; beyond five, the smallest fold into "기타". */
function perService(data: { service_id: string; points: MetricPoint[] }[], services: CanvasService[], f: (p: MetricPoint) => number): Series[] {
  const order = [...services].sort((a, b) => a.name.localeCompare(b.name));
  const color = new Map(order.map((s, i) => [s.id, SERIES_COLORS[i % SERIES_COLORS.length]]));
  const name = new Map(order.map((s) => [s.id, s.name]));
  const rows = data.filter((d) => name.has(d.service_id));
  if (rows.length <= SERIES_COLORS.length)
    return order
      .map((s) => rows.find((r) => r.service_id === s.id))
      .filter((r): r is NonNullable<typeof r> => !!r)
      .map((r) => ({ name: name.get(r.service_id)!, color: color.get(r.service_id)!, points: pts(r.points, f) }));
  const ranked = [...rows].sort((a, b) => avg(b.points, f) - avg(a.points, f));
  const top = ranked.slice(0, 5);
  const rest = ranked.slice(5);
  const other = new Map<number, number>();
  for (const r of rest) for (const p of pts(r.points, f)) other.set(p.t, (other.get(p.t) ?? 0) + p.v);
  return [
    ...top.map((r, i) => ({ name: name.get(r.service_id)!, color: SERIES_COLORS[i], points: pts(r.points, f) })),
    { name: t("기타 {count}개", { count: rest.length }), color: OTHER_SERIES, points: [...other].sort((a, b) => a[0] - b[0]).map(([ts, v]) => ({ t: ts, v })) },
  ];
}

function RecentErrors({ envId, names, onOpen }: { envId: string; names: Map<string, string>; onOpen: (sid: string) => void }) {
  const stream = useLogStream(sse.envLogs(envId, [], "", "error"), { replay: true, maxLines: 400 });
  const parsed = useParsedLines<EnvLogLine>(stream.lines);
  const recent = parsed.slice(-12).reverse();
  return recent.length === 0 ? (
    <div className="flex h-full min-h-[120px] items-center justify-center text-sm text-gray-500">
      {stream.state === "connecting" ? <Spinner /> : t("최근 오류가 없습니다")}
    </div>
  ) : (
    <ul className="-mx-2 space-y-0.5">
      {recent.map((l, i) => (
        <li key={i}>
          <button
            type="button"
            onClick={() => onOpen(l.service_id)}
            className="flex w-full cursor-pointer items-start gap-2.5 rounded-md px-2 py-1.5 text-left hover:bg-gray-100"
          >
            <span className="mt-[3px] h-[14px] w-[3px] shrink-0 rounded-full bg-red-500" />
            <span className="w-[86px] shrink-0 truncate text-xs font-medium text-gray-600">{names.get(l.service_id) ?? "?"}</span>
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-danger" title={l.msg}>
              {l.msg}
            </span>
            <span className="shrink-0 text-xs tabular-nums text-gray-400" title={fmtTime(l.t)}>
              {fmtRelative(l.t)}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function ObservabilityInner() {
  const ws = useWorkspace();
  const envId = ws.env?.id;
  const canvas = useCanvas(envId);
  const [range, setRange] = useState<MetricRange>("6h");
  const metrics = useEnvMetrics(envId, range);
  const events = useEvents(ws.pid, envId, 50);
  const href = useCtxHref();
  const params = useSearchParams();
  const router = useRouter();
  const services = useMemo(() => canvas.data?.services ?? [], [canvas.data]);
  const names = useMemo(() => new Map(services.map((s) => [s.id, s.name])), [services]);
  const open = (sid: string, extra: { dep?: string } = {}) => canvasHref({ svc: sid, dep: extra.dep ?? null }, ctxFrom(params));
  // v1.5 custom dashboards: ?dash=<id> (none = the built-in overview below).
  const caps = useCaps(ws.pid);
  const dashboards = useDashboards(ws.pid);
  const dashId = params.get("dash");
  const dash = dashboards.data?.find((d) => d.id === dashId) ?? null;
  const setDash = (id: string | null) => router.replace(href("/observability/", { dash: id }), { scroll: false });

  const cpu = useMemo(() => perService(metrics.data?.services ?? [], services, (p) => p.cpu_percent), [metrics.data, services]);
  const mem = useMemo(() => perService(metrics.data?.services ?? [], services, (p) => p.memory_bytes), [metrics.data, services]);
  const deployEvents = (events.data ?? []).filter((e) => e.kind.startsWith("deploy.")).slice(0, 12);

  const count = (f: (s: CanvasService) => boolean) => services.filter(f).length;
  const online = count((s) => s.instance.status === "online" || s.instance.status === "scheduled");
  const failing = count((s) => s.instance.status === "failed" || s.instance.status === "crashed");
  const deploying = count((s) => s.instance.status === "deploying" || (!!s.latest_deployment && !isTerminal(s.latest_deployment.status)));

  if (ws.isPending || canvas.isPending)
    return (
      <div className="flex h-full items-center justify-center bg-background">
        <Spinner />
      </div>
    );
  if (ws.error || canvas.error)
    return (
      <div className="p-6">
        <ErrorBox error={ws.error ?? canvas.error} />
      </div>
    );

  return (
    <div className="min-h-full bg-background px-4 pb-10 pt-5 sm:px-6">
      <div className="mx-auto max-w-[1400px]">
        <div className="mb-5 flex flex-wrap items-center gap-3">
          <h1 className="text-xl font-semibold">{t("관측")}</h1>
          <span className="text-sm text-gray-500">{t("{env} 환경", { env: ws.env?.name ?? "" })}</span>
          <span className="h-5 w-px bg-gray-200" aria-hidden="true" />
          {ws.pid && <DashboardSelector pid={ws.pid} value={dash?.id ?? null} onChange={setDash} canEdit={caps.canEdit} />}
          {!dash && (
            <div className="ml-auto">
              <Segmented label={t("기간")} value={range} onChange={setRange} options={METRIC_RANGES.map((r) => ({ value: r, label: RANGE_LABEL[r] }))} />
            </div>
          )}
        </div>

        {dash && ws.pid && envId ? (
          <DashboardView dashboard={dash} pid={ws.pid} envs={ws.envs} currentEnv={envId} canEdit={caps.canEdit} ctx={ctxFrom(params)} />
        ) : (
        <>
        <div className="mb-4 grid grid-cols-2 divide-gray-200 rounded-lg border border-gray-200 bg-background dark:bg-secondaryBg max-lg:[&>*:nth-child(n+3)]:border-t max-lg:[&>*:nth-child(even)]:border-l lg:grid-cols-4 lg:divide-x">
          <Tile label={t("서비스|plural")} value={services.length} />
          <Tile label={t("온라인")} value={online} tone={online ? "success" : undefined} />
          <Tile label={t("배포 중")} value={deploying} tone={deploying ? "warning" : undefined} />
          <Tile
            label={t("실패 · 크래시")}
            value={
              <span className="flex items-center gap-2">
                {failing}
                {failing > 0 && <AlertTriangle className="size-4" />}
              </span>
            }
            tone={failing ? "danger" : undefined}
          />
        </div>

        <div className="grid gap-4 lg:grid-cols-2">
          <Widget title={t("CPU 사용률")} aside={t("서비스별 · 100% = 1코어")}>
            {metrics.error ? (
              <ErrorBox error={metrics.error} />
            ) : (
              <>
                <LineChart series={cpu} format={fmtPct} minMax={1} height={220} area={false} />
                <div className="mt-3">
                  <Legend series={cpu} />
                </div>
              </>
            )}
          </Widget>
          <Widget title={t("메모리")} aside={t("서비스별")}>
            {metrics.error ? (
              <ErrorBox error={metrics.error} />
            ) : (
              <>
                <LineChart series={mem} format={fmtMem} minMax={50e6} height={220} area={false} />
                <div className="mt-3">
                  <Legend series={mem} />
                </div>
              </>
            )}
          </Widget>
          <Widget
            title={t("최근 오류")}
            aside={
              <Link href={href("/logs/", { q: "error" })} className="inline-flex items-center gap-1 hover:text-foreground">
                {t("로그에서 보기")} <ArrowUpRight className="size-3.5" />
              </Link>
            }
          >
            {envId && <RecentErrors envId={envId} names={names} onOpen={(sid) => router.push(open(sid))} />}
          </Widget>
          <Widget title={t("배포 이벤트")} aside={t("최근 12개")}>
            {events.isPending ? (
              <Spinner />
            ) : deployEvents.length === 0 ? (
              <div className="flex min-h-[120px] items-center justify-center text-sm text-gray-500">{t("아직 배포가 없습니다")}</div>
            ) : (
              <ul className="-mx-2">
                {deployEvents.map((e) => (
                  <li key={e.id}>
                    <Link href={e.service_id ? open(e.service_id) : href("/")} className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-gray-100">
                      {eventIcon(e.kind)}
                      <span className="w-[110px] shrink-0 truncate text-sm font-medium">{(e.service_id && names.get(e.service_id)) || e.service_name || (e.service_id ? t("삭제된 서비스") : t("프로젝트"))}</span>
                      <span className="min-w-0 flex-1 truncate text-sm text-gray-600">{e.message}</span>
                      <span className="shrink-0 text-xs tabular-nums text-gray-400" title={fmtTime(e.created_at)}>
                        {fmtRelative(e.created_at)}
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Widget>
          <Widget title={t("서비스 상태")} aside={t("{count}개", { count: services.length })} className="lg:col-span-2">
            <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
              {services.map((s) => {
                const ps = metrics.data?.services.find((m) => m.service_id === s.id)?.points ?? [];
                const last = ps[ps.length - 1];
                return (
                  <Link key={s.id} href={open(s.id)} className="flex items-center gap-3 rounded-md border border-gray-200 px-3 py-2.5 hover:bg-gray-100">
                    <BrandIcon name={iconKey(s)} className="size-4" />
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">{s.name}</span>
                    <span className="shrink-0 text-xs tabular-nums text-gray-500">{last ? `${fmtPct(last.cpu_percent)} · ${fmtMem(last.memory_bytes)}` : "—"}</span>
                    <StatusDot status={s.instance.status} />
                  </Link>
                );
              })}
            </div>
          </Widget>
          {ws.pid && <AlertRulesSection pid={ws.pid} envs={ws.envs} services={ws.detail?.services ?? []} />}
        </div>
        </>
        )}
      </div>
    </div>
  );
}

export default function ObservabilityPage() {
  return (
    <Suspense fallback={<div className="h-full bg-background" />}>
      <ObservabilityInner />
    </Suspense>
  );
}
