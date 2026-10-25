"use client";

/* v1.5 custom observability dashboards: selector, add/edit widget dialog, 12-column grid with
   drag-and-drop and keyboard reorder (handle + arrow keys), widgets built from the existing chart/log/http/event views. */

import Link from "next/link";
import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { GripVertical, LayoutDashboard, MoreHorizontal, PencilLine, Plus, Trash2 } from "lucide-react";
import { METRIC_RANGES, api, sse, type CanvasService, type EnvLogLine, type Environment, type HttpLogLine, type MetricPoint, type MetricRange } from "@/lib/api";
import {
  DASHBOARD_COLUMNS,
  MAX_WIDGETS,
  WIDGET_METRICS,
  WIDGET_TYPES,
  layoutApi,
  layoutKeys,
  type Dashboard,
  type Widget,
  type WidgetMetric,
  type WidgetType,
} from "@/lib/layouts";
import { qk, useCanvas, useEnvMetrics, useEvents, useMetrics } from "@/lib/queries";
import { fmtMem, fmtPct, fmtRate, pts, rate } from "@/lib/metrics";
import { fmtRelative, fmtTime } from "@/lib/format";
import { canvasHref } from "@/lib/nav";
import { t } from "@/lib/i18n";
import { toast } from "@/lib/uiState";
import { useLogStream, useParsedLines } from "@/lib/useLogStream";
import { Legend, LineChart, SERIES_COLORS, type Series } from "./charts/LineChart";
import { eventIcon } from "./canvas/ActivityPanel";
import { RANGE_LABEL } from "./panel/MetricsTab";
import { HttpLogTable } from "./HttpLogTable";
import { LogTable, type LogLine } from "./LogTable";
import { Button, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, Menu, MenuItem, Segmented, Select, Spinner, cx, useConfirm } from "./ui";

const ROW_PX = 84;

export function useDashboards(pid: string | null | undefined) {
  return useQuery({ queryKey: layoutKeys.dashboards(pid ?? ""), queryFn: () => layoutApi.dashboards(pid!), enabled: !!pid });
}

/** Widgets in display order (row, then column). */
export function ordered(ws: Widget[]): Widget[] {
  return [...ws].sort((a, b) => a.y - b.y || a.x - b.x);
}

/** Flows widgets left to right into 12-column rows; y becomes the row index. */
export function reflow(ws: Widget[]): Widget[] {
  let x = 0;
  let y = 0;
  return ws.map((w) => {
    const width = Math.min(DASHBOARD_COLUMNS, Math.max(1, w.w));
    if (x + width > DASHBOARD_COLUMNS) {
      x = 0;
      y++;
    }
    const out = { ...w, x, y, w: width };
    x += width;
    return out;
  });
}

/** Moves the widget at `from` to `to` and reflows. */
export function moveWidget(ws: Widget[], from: number, to: number): Widget[] {
  const list = ordered(ws);
  if (from < 0 || from >= list.length) return list;
  const dst = Math.max(0, Math.min(list.length - 1, to));
  const [it] = list.splice(from, 1);
  list.splice(dst, 0, it);
  return reflow(list);
}

/* ── selector ──────────────────────────────────────────────────────── */

/** Tabs: built-in overview + the project's dashboards + "new". `value` null = overview. */
export function DashboardSelector({
  pid,
  value,
  onChange,
  canEdit,
}: {
  pid: string;
  value: string | null;
  onChange: (id: string | null) => void;
  canEdit: boolean;
}) {
  const qc = useQueryClient();
  const list = useDashboards(pid);
  const [naming, setNaming] = useState<null | { id: string | null; name: string }>(null);
  const { confirm, node } = useConfirm();
  const cur = list.data?.find((d) => d.id === value);
  const tab = (id: string | null, label: string) => (
    <button
      key={id ?? "overview"}
      type="button"
      role="tab"
      aria-selected={value === id}
      onClick={() => onChange(id)}
      className={cx(
        "relative -ml-px flex h-[34px] shrink-0 cursor-pointer items-center whitespace-nowrap border px-3 text-sm first:ml-0 first:rounded-l-md last:rounded-r-md hover:z-10 hover:border-gray-300 hover:bg-secondaryBg focus:outline-none focus-visible:z-20 focus-visible:ring-2 focus-visible:ring-pink-500",
        value === id ? "z-10 border-gray-300 bg-secondaryBg text-pink-500" : "border-gray-200 text-gray-600 hover:text-foreground",
      )}
    >
      {label}
    </button>
  );
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      {node}
      <div role="tablist" aria-label={t("대시보드")} className="flex min-w-0 items-center overflow-x-auto">
        {tab(null, t("개요"))}
        {(list.data ?? []).map((d) => tab(d.id, d.name))}
      </div>
      {canEdit && (
        <Button variant="ghost" onClick={() => setNaming({ id: null, name: t("대시보드 {n}", { n: (list.data?.length ?? 0) + 1 }) })}>
          <Plus className="size-3.5" /> {t("새 대시보드")}
        </Button>
      )}
      {canEdit && cur && (
        <Menu
          label={t("대시보드 작업")}
          align="start"
          trigger={(p) => (
            <IconButton {...p} label={t("{name} 대시보드 작업", { name: cur.name })}>
              <MoreHorizontal className="size-4" />
            </IconButton>
          )}
        >
          {(close) => (
            <>
              <MenuItem
                icon={<PencilLine className="size-3.5" />}
                onSelect={() => {
                  close();
                  setNaming({ id: cur.id, name: cur.name });
                }}
              >
                {t("이름 바꾸기")}
              </MenuItem>
              <MenuItem
                danger
                icon={<Trash2 className="size-3.5" />}
                onSelect={() => {
                  close();
                  confirm({
                    title: t("{name} 삭제", { name: cur.name }),
                    message: t("대시보드와 위젯 배치를 지웁니다. 지표와 로그는 그대로 남습니다."),
                    confirmLabel: t("삭제"),
                    danger: true,
                    run: async () => {
                      try {
                        await layoutApi.deleteDashboard(pid, cur.id);
                        onChange(null);
                        await qc.invalidateQueries({ queryKey: layoutKeys.dashboards(pid) });
                      } catch (e) {
                        toast(e instanceof Error ? e.message : String(e), "error");
                      }
                    },
                  });
                }}
              >
                {t("삭제")}
              </MenuItem>
            </>
          )}
        </Menu>
      )}
      {naming && (
        <NameDialog
          title={naming.id ? t("대시보드 이름 바꾸기") : t("새 대시보드")}
          initial={naming.name}
          submitLabel={naming.id ? t("저장") : t("만들기")}
          onClose={() => setNaming(null)}
          onSubmit={async (name) => {
            if (naming.id) {
              await layoutApi.updateDashboard(pid, naming.id, { name });
            } else {
              const d = await layoutApi.createDashboard(pid, { name, widgets: [] });
              onChange(d.id);
            }
            await qc.invalidateQueries({ queryKey: layoutKeys.dashboards(pid) });
          }}
        />
      )}
    </div>
  );
}

function NameDialog({
  title,
  initial,
  submitLabel,
  onClose,
  onSubmit,
}: {
  title: string;
  initial: string;
  submitLabel: string;
  onClose: () => void;
  onSubmit: (name: string) => Promise<void>;
}) {
  const [name, setName] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const ok = name.trim() !== "" && [...name.trim()].length <= 60;
  const submit = async () => {
    if (!ok || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit(name.trim());
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={title}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!ok} onClick={submit}>
            {submitLabel}
          </Button>
        </>
      }
    >
      <form
        className="space-y-3 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Field label={t("이름")}>
          <Input autoFocus value={name} maxLength={60} onChange={(e) => setName(e.target.value)} />
        </Field>
        <ErrorBox error={error} />
      </form>
    </Dialog>
  );
}

/* ── widget dialog ─────────────────────────────────────────────────── */

const WIDTHS = [
  { value: 4, label: "1/3" },
  { value: 6, label: "1/2" },
  { value: 8, label: "2/3" },
  { value: 12, label: t("전체") },
];
const HEIGHTS = [
  { value: 2, label: t("낮게") },
  { value: 3, label: t("보통") },
  { value: 5, label: t("높게") },
];

function WidgetDialog({
  initial,
  envs,
  defaultEnv,
  onClose,
  onSubmit,
}: {
  initial: Widget | null;
  envs: Environment[];
  defaultEnv: string;
  onClose: () => void;
  onSubmit: (w: Partial<Widget>) => Promise<void>;
}) {
  const [type, setType] = useState<WidgetType>(initial?.type ?? "metric");
  const [title, setTitle] = useState(initial?.title ?? "");
  const [env, setEnv] = useState(initial?.environment_id ?? defaultEnv);
  const [svc, setSvc] = useState(initial?.service_id ?? "");
  const [metric, setMetric] = useState<WidgetMetric>((initial?.metric as WidgetMetric) || "cpu_percent");
  const [range, setRange] = useState<MetricRange>(initial?.range ?? "6h");
  const [w, setW] = useState(initial?.w ?? 6);
  const [h, setH] = useState(initial?.h ?? 3);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const canvas = useCanvas(type === "events" ? null : env);
  const services = useMemo(() => [...(canvas.data?.services ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [canvas.data]);
  const needSvc = type === "http";
  const ok = (!needSvc || !!svc) && [...title.trim()].length <= 100;
  const submit = async () => {
    if (!ok || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit({
        type,
        title: title.trim(),
        environment_id: type === "events" ? (env || null) : env,
        service_id: svc || null,
        metric: type === "metric" ? metric : "",
        range,
        w,
        h,
      });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const id = (s: string) => `wd-${s}`;
  return (
    <Dialog
      open
      wide
      onClose={() => !busy && onClose()}
      title={initial ? t("위젯 편집") : t("위젯 추가")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!ok} onClick={submit}>
            {initial ? t("저장") : t("추가")}
          </Button>
        </>
      }
    >
      <form
        className="space-y-4 pt-1"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <fieldset>
          <legend className="mb-2 text-sm font-medium text-foreground">{t("종류")}</legend>
          <div role="radiogroup" aria-label={t("위젯 종류")} className="grid gap-2 sm:grid-cols-2">
            {WIDGET_TYPES.map((wt) => (
              <label
                key={wt.value}
                className={cx(
                  "flex cursor-pointer flex-col rounded-md border px-3 py-2 transition-colors",
                  type === wt.value ? "border-pink-500 bg-pink-50" : "border-gray-200 hover:border-gray-300 hover:bg-gray-100",
                )}
              >
                <span className="flex items-center gap-2 text-sm font-medium text-foreground">
                  <input type="radio" name="widget-type" className="accent-pink-500" checked={type === wt.value} onChange={() => setType(wt.value)} />
                  {wt.label}
                </span>
                <span className="mt-0.5 pl-5 text-xs text-gray-500">{wt.desc}</span>
              </label>
            ))}
          </div>
        </fieldset>
        <Field label={t("제목")} hint={t("비우면 종류와 대상으로 정합니다")}>
          <Input value={title} maxLength={100} onChange={(e) => setTitle(e.target.value)} placeholder={t("예: API 메모리")} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("환경")} hint={type === "events" ? t("비우면 모든 환경") : undefined}>
            <Select
              aria-label={t("환경")}
              id={id("env")}
              value={env}
              onChange={(e) => {
                setEnv(e.target.value);
                setSvc("");
              }}
            >
              {type === "events" && <option value="">{t("모든 환경")}</option>}
              {envs.map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
            </Select>
          </Field>
          {type !== "events" && (
            <Field label={t("서비스")} hint={needSvc ? t("필수") : t("비우면 환경의 모든 서비스")}>
              <Select aria-label={t("서비스")} value={svc} onChange={(e) => setSvc(e.target.value)} required={needSvc}>
                <option value="">{needSvc ? t("서비스 선택") : t("모든 서비스")}</option>
                {services.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          {type === "metric" && (
            <>
              <Field label={t("지표")}>
                <Select aria-label={t("지표")} value={metric} onChange={(e) => setMetric(e.target.value as WidgetMetric)}>
                  {WIDGET_METRICS.map((m) => (
                    <option key={m.value} value={m.value}>
                      {m.label}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label={t("기간")}>
                <Select aria-label={t("기간")} value={range} onChange={(e) => setRange(e.target.value as MetricRange)}>
                  {METRIC_RANGES.map((r) => (
                    <option key={r} value={r}>
                      {RANGE_LABEL[r]}
                    </option>
                  ))}
                </Select>
              </Field>
            </>
          )}
          <Field label={t("너비")}>
            <Select aria-label={t("너비")} value={w} onChange={(e) => setW(Number(e.target.value))}>
              {WIDTHS.map((x) => (
                <option key={x.value} value={x.value}>
                  {x.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("높이")}>
            <Select aria-label={t("높이")} value={h} onChange={(e) => setH(Number(e.target.value))}>
              {HEIGHTS.map((x) => (
                <option key={x.value} value={x.value}>
                  {x.label}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <ErrorBox error={error} />
      </form>
    </Dialog>
  );
}

/* ── widget bodies ─────────────────────────────────────────────────── */

const avg = (ps: { v: number }[]) => (ps.length ? ps.reduce((a, p) => a + p.v, 0) / ps.length : 0);

function metricFns(m: WidgetMetric): { series: (ps: MetricPoint[]) => { t: number; v: number }[]; format: (v: number) => string; minMax: number } {
  switch (m) {
    case "cpu_percent":
      return { series: (ps) => pts(ps, (p) => p.cpu_percent), format: fmtPct, minMax: 1 };
    case "memory_bytes":
      return { series: (ps) => pts(ps, (p) => p.memory_bytes), format: fmtMem, minMax: 50e6 };
    case "disk_bytes":
      return { series: (ps) => pts(ps, (p) => p.disk_bytes), format: fmtMem, minMax: 50e6 };
    case "net_rx_bytes":
      return { series: (ps) => rate(ps, (p) => p.net_rx_bytes), format: fmtRate, minMax: 1000 };
    case "net_tx_bytes":
      return { series: (ps) => rate(ps, (p) => p.net_tx_bytes), format: fmtRate, minMax: 1000 };
  }
}

function MetricBody({ w, height, services }: { w: Widget; height: number; services: CanvasService[] }) {
  const fns = metricFns((w.metric || "cpu_percent") as WidgetMetric);
  const one = useMetrics(w.service_id ?? "", w.service_id ? (w.environment_id ?? "") : "", w.range);
  const all = useEnvMetrics(w.service_id ? null : w.environment_id, w.range);
  const series: Series[] = useMemo(() => {
    if (w.service_id) {
      const name = services.find((s) => s.id === w.service_id)?.name ?? t("서비스");
      return [{ name, color: SERIES_COLORS[0], points: fns.series(one.data?.points ?? []) }];
    }
    const names = new Map(services.map((s) => [s.id, s.name]));
    const rows = (all.data?.services ?? []).filter((r) => names.has(r.service_id)).map((r) => ({ name: names.get(r.service_id)!, points: fns.series(r.points) }));
    rows.sort((a, b) => avg(b.points) - avg(a.points));
    return rows.slice(0, SERIES_COLORS.length).map((r, i) => ({ ...r, color: SERIES_COLORS[i] }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [w.service_id, w.metric, one.data, all.data, services]);
  const q = w.service_id ? one : all;
  if (q.error) return <ErrorBox error={q.error} />;
  if (q.isPending) return <Spinner />;
  return (
    <div className="flex h-full flex-col">
      <LineChart series={series} format={fns.format} minMax={fns.minMax} height={Math.max(90, height - (series.length > 1 ? 40 : 8))} area={series.length === 1} />
      {series.length > 1 && (
        <div className="mt-2">
          <Legend series={series} />
        </div>
      )}
    </div>
  );
}

function LogsBody({ w, services }: { w: Widget; services: CanvasService[] }) {
  const url = w.environment_id ? sse.envLogs(w.environment_id, w.service_id ? [w.service_id] : [], "") : null;
  const stream = useLogStream(url, { replay: true, maxLines: 500 });
  const parsed = useParsedLines<EnvLogLine>(stream.lines);
  const names = useMemo(() => new Map(services.map((s) => [s.id, s.name])), [services]);
  const lines: LogLine[] = useMemo(
    () => parsed.map((l) => ({ text: l.msg, at: Date.parse(l.t), service: names.get(l.service_id) ?? l.service_id, replica: l.replica, stream: l.stream })),
    [parsed, names],
  );
  return (
    <LogTable
      className="h-full min-h-0 rounded-md border border-gray-200"
      lines={lines}
      state={stream.state}
      onReconnect={stream.reconnect}
      showService={!w.service_id}
      emptyText={t("아직 출력된 로그가 없습니다")}
    />
  );
}

function HttpBody({ w }: { w: Widget }) {
  const url = w.service_id && w.environment_id ? sse.httpLogs(w.service_id, w.environment_id) : null;
  const stream = useLogStream(url, { maxLines: 500 });
  const lines = useParsedLines<HttpLogLine>(stream.lines);
  return <HttpLogTable className="h-full rounded-md border border-gray-200" lines={lines} state={stream.state} filter="" emptyText={t("아직 요청이 없습니다. 도메인이 있는 서비스만 기록됩니다.")} />;
}

function EventsBody({ w, pid, names, open }: { w: Widget; pid: string; names: Map<string, string>; open: (sid: string) => string }) {
  const events = useEvents(pid, w.environment_id ?? null, 30);
  if (events.error) return <ErrorBox error={events.error} />;
  if (events.isPending) return <Spinner />;
  const list = events.data ?? [];
  if (!list.length) return <div className="flex h-full items-center justify-center text-sm text-gray-500">{t("이벤트가 없습니다")}</div>;
  return (
    <ul className="-mx-2 h-full overflow-y-auto" data-noscroll>
      {list.map((e) => (
        <li key={e.id}>
          <Link href={e.service_id ? open(e.service_id) : "#"} className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-gray-100">
            {eventIcon(e.kind)}
            <span className="w-[100px] shrink-0 truncate text-sm font-medium">{(e.service_id && names.get(e.service_id)) || e.service_name || t("프로젝트")}</span>
            <span className="min-w-0 flex-1 truncate text-sm text-gray-600">{e.message}</span>
            <span className="shrink-0 text-xs tabular-nums text-gray-400" title={fmtTime(e.created_at)}>
              {fmtRelative(e.created_at)}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function widgetTitle(w: Widget, svcName: string | undefined, envName: string | undefined): string {
  if (w.title) return w.title;
  const kind = w.type === "metric" ? (WIDGET_METRICS.find((m) => m.value === w.metric)?.label ?? t("지표")) : WIDGET_TYPES.find((wt) => wt.value === w.type)!.label;
  const target = svcName ?? (w.type === "events" ? (envName ?? t("프로젝트")) : t("{env} 전체", { env: envName ?? "" }));
  return `${kind} · ${target}`;
}

/* ── grid ──────────────────────────────────────────────────────────── */

/** Width / height picker inside the widget menu. */
function SizeChoice({ label, options, value, onPick }: { label: string; options: { value: number; label: string }[]; value: number; onPick: (v: number) => void }) {
  return (
    <div className="px-1.5 pb-1 pt-1.5">
      <div className="px-1.5 pb-1.5 text-xs font-medium text-gray-500">{label}</div>
      <Segmented
        label={t("위젯 {label}", { label })}
        value={String(value)}
        onChange={(v) => onPick(Number(v))}
        options={options.map((x) => ({ value: String(x.value), label: x.label }))}
      />
    </div>
  );
}

function WidgetCard({
  w,
  index,
  count,
  title,
  aside,
  canEdit,
  dragging,
  handleRef,
  onMove,
  onEdit,
  onResize,
  onRemove,
  onDragStart,
  onDragEnd,
  onDropAt,
  children,
}: {
  w: Widget;
  index: number;
  count: number;
  title: string;
  aside?: ReactNode;
  canEdit: boolean;
  dragging: boolean;
  handleRef: (el: HTMLButtonElement | null) => void;
  onMove: (to: number) => void;
  onEdit: () => void;
  onResize: (p: { w?: number; h?: number }) => void;
  onRemove: () => void;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDropAt: () => void;
  children: ReactNode;
}) {
  const [over, setOver] = useState(false);
  return (
    <section
      aria-label={title}
      onDragOver={(e) => {
        if (!canEdit) return;
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        onDropAt();
      }}
      className={cx(
        "flex min-w-0 flex-col rounded-lg border bg-background transition-colors dark:bg-secondaryBg lg:col-span-(--w)",
        over ? "border-pink-500" : "border-gray-200",
        dragging && "opacity-50",
      )}
      style={{ "--w": w.w } as CSSProperties}
    >
      <header className={cx("flex h-11 shrink-0 items-center gap-1.5 pr-2", canEdit ? "pl-2" : "pl-4")}>
        {canEdit && (
          <button
            ref={handleRef}
            type="button"
            draggable
            onDragStart={(e) => {
              e.dataTransfer.effectAllowed = "move";
              e.dataTransfer.setData("text/plain", w.id);
              onDragStart();
            }}
            onDragEnd={onDragEnd}
            onKeyDown={(e) => {
              const to =
                e.key === "ArrowLeft" || e.key === "ArrowUp" ? index - 1 : e.key === "ArrowRight" || e.key === "ArrowDown" ? index + 1 : e.key === "Home" ? 0 : e.key === "End" ? count - 1 : null;
              if (to === null) return;
              e.preventDefault();
              if (to !== index && to >= 0 && to < count) onMove(to);
            }}
            aria-label={t("{title} 위젯 옮기기, {count}개 중 {index}번째. 방향키로 순서를 바꾸거나 끌어서 놓습니다.", { title, count, index: index + 1 })}
            title={t("끌거나 방향키로 순서 바꾸기")}
            className="-ml-1 flex size-[26px] shrink-0 cursor-grab items-center justify-center rounded-md text-gray-400 hover:bg-gray-100 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600 active:cursor-grabbing"
          >
            <GripVertical className="size-4" />
          </button>
        )}
        <h2 className="min-w-0 flex-1 truncate text-sm font-semibold">{title}</h2>
        {aside && <span className="shrink-0 text-xs text-gray-500">{aside}</span>}
        {canEdit && (
          <Menu
            label={t("위젯 작업")}
            trigger={(p) => (
              <IconButton {...p} size="sm" label={t("{title} 위젯 작업", { title })}>
                <MoreHorizontal className="size-4" />
              </IconButton>
            )}
          >
            {(close) => (
              <>
                <MenuItem icon={<PencilLine className="size-3.5" />} onSelect={() => (close(), onEdit())}>
                  {t("편집")}
                </MenuItem>
                <SizeChoice label={t("너비")} options={WIDTHS} value={w.w} onPick={(v) => (close(), onResize({ w: v }))} />
                <SizeChoice label={t("높이")} options={HEIGHTS} value={w.h} onPick={(v) => (close(), onResize({ h: v }))} />
                <div className="my-2 h-px w-full bg-gray-100" />
                <MenuItem danger icon={<Trash2 className="size-3.5" />} onSelect={() => (close(), onRemove())}>
                  {t("삭제")}
                </MenuItem>
              </>
            )}
          </Menu>
        )}
      </header>
      <div className="min-h-0 px-4 pb-4" style={{ height: w.h * ROW_PX }}>
        {children}
      </div>
    </section>
  );
}

/** One custom dashboard. Changes (add, edit, reorder, resize, remove) are saved right away. */
export function DashboardView({
  dashboard,
  pid,
  envs,
  currentEnv,
  canEdit,
  ctx,
}: {
  dashboard: Dashboard;
  pid: string;
  envs: Environment[];
  currentEnv: string;
  canEdit: boolean;
  ctx: { project?: string | null; env?: string | null };
}) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Widget | "new" | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [announce, setAnnounce] = useState("");
  const handles = useRef(new Map<string, HTMLButtonElement>());
  const focusAfter = useRef<string | null>(null);
  const widgets = useMemo(() => ordered(dashboard.widgets), [dashboard.widgets]);
  const envName = useMemo(() => new Map(envs.map((e) => [e.id, e.name])), [envs]);
  // Service names of every environment used by a widget (one canvas query per environment, shared with other pages).
  const envIds = useMemo(() => [...new Set(widgets.map((w) => w.environment_id).filter((x): x is string => !!x))], [widgets]);
  const canvases = useQueries({ queries: envIds.map((eid) => ({ queryKey: qk.canvas(eid), queryFn: () => api.canvas(eid), staleTime: 10_000 })) });
  const canvasKey = canvases.map((c) => c.dataUpdatedAt).join(",");
  const servicesByEnv = useMemo(() => {
    const m = new Map<string, CanvasService[]>();
    for (const c of canvases) if (c.data) m.set(c.data.environment.id, c.data.services);
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canvasKey]);
  const allNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const list of servicesByEnv.values()) for (const s of list) m.set(s.id, s.name);
    return m;
  }, [servicesByEnv]);

  useEffect(() => {
    const id = focusAfter.current;
    if (id) {
      focusAfter.current = null;
      handles.current.get(id)?.focus();
    }
  }, [dashboard.widgets]);

  const save = async (next: Widget[], msg?: string) => {
    const key = layoutKeys.dashboards(pid);
    const prev = qc.getQueryData<Dashboard[]>(key);
    qc.setQueryData<Dashboard[]>(key, (l) => l?.map((d) => (d.id === dashboard.id ? { ...d, widgets: next } : d)));
    if (msg) setAnnounce(msg);
    try {
      const d = await layoutApi.updateDashboard(pid, dashboard.id, { widgets: next });
      qc.setQueryData<Dashboard[]>(key, (l) => l?.map((x) => (x.id === d.id ? d : x)));
    } catch (e) {
      qc.setQueryData(key, prev);
      toast(e instanceof Error ? e.message : String(e), "error");
      throw e;
    }
  };
  const move = (from: number, to: number) => {
    const w = widgets[from];
    focusAfter.current = w.id;
    save(moveWidget(widgets, from, to), t("{title}: {count}개 중 {index}번째로 옮겼습니다", { title: widgetTitle(w, w.service_id ? allNames.get(w.service_id) : undefined, w.environment_id ? envName.get(w.environment_id) : undefined), count: widgets.length, index: to + 1 })).catch(() => {});
  };
  const open = (sid: string) => canvasHref({ svc: sid }, ctx);

  return (
    <div>
      <div aria-live="polite" className="sr-only">
        {announce}
      </div>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <span className="text-sm tabular-nums text-gray-500">
          {t("위젯 {count}/{max}", { count: widgets.length, max: MAX_WIDGETS })}
          {canEdit && widgets.length > 1 && ` · ${t("손잡이를 끌거나 손잡이에서 방향키로 순서를 바꿉니다")}`}
        </span>
        {canEdit && (
          <Button variant="primary" className="ml-auto" disabled={widgets.length >= MAX_WIDGETS} onClick={() => setEditing("new")}>
            <Plus className="size-3.5" /> {t("위젯 추가")}
          </Button>
        )}
      </div>
      {widgets.length === 0 ? (
        <EmptyState
          icon={<LayoutDashboard className="size-6" aria-hidden />}
          title={t("아직 위젯이 없습니다")}
          action={
            canEdit && (
              <Button onClick={() => setEditing("new")}>
                <Plus /> {t("위젯 추가")}
              </Button>
            )
          }
        >
          {canEdit ? t("지표 차트, 로그, HTTP 로그, 이벤트를 골라 배치하세요.") : undefined}
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
          {widgets.map((w, i) => {
            const svcs = (w.environment_id && servicesByEnv.get(w.environment_id)) || [];
            const title = widgetTitle(w, w.service_id ? allNames.get(w.service_id) : undefined, w.environment_id ? envName.get(w.environment_id) : undefined);
            return (
              <WidgetCard
                key={w.id}
                w={w}
                index={i}
                count={widgets.length}
                title={title}
                aside={w.type === "metric" ? RANGE_LABEL[w.range] : w.environment_id && w.type !== "events" ? envName.get(w.environment_id) : undefined}
                canEdit={canEdit}
                dragging={dragId === w.id}
                handleRef={(el) => (el ? handles.current.set(w.id, el) : handles.current.delete(w.id))}
                onMove={(to) => move(i, to)}
                onEdit={() => setEditing(w)}
                onResize={(p) => save(reflow(widgets.map((x) => (x.id === w.id ? { ...x, ...p } : x)))).catch(() => {})}
                onRemove={() => save(reflow(widgets.filter((x) => x.id !== w.id)), t("{title} 위젯을 지웠습니다", { title })).catch(() => {})}
                onDragStart={() => setDragId(w.id)}
                onDragEnd={() => setDragId(null)}
                onDropAt={() => {
                  const from = widgets.findIndex((x) => x.id === dragId);
                  setDragId(null);
                  if (from >= 0 && from !== i) move(from, i);
                }}
              >
                {w.type === "metric" ? (
                  <MetricBody w={w} height={w.h * ROW_PX - 24} services={svcs} />
                ) : w.type === "logs" ? (
                  <LogsBody w={w} services={svcs} />
                ) : w.type === "http" ? (
                  <HttpBody w={w} />
                ) : (
                  <EventsBody w={w} pid={pid} names={allNames} open={open} />
                )}
              </WidgetCard>
            );
          })}
        </div>
      )}
      {editing && (
        <WidgetDialog
          initial={editing === "new" ? null : editing}
          envs={envs}
          defaultEnv={currentEnv}
          onClose={() => setEditing(null)}
          onSubmit={async (p) => {
            if (editing === "new") {
              await save(reflow([...widgets, { id: "", x: 0, y: 0, ...p } as Widget]), t("위젯을 추가했습니다"));
            } else {
              await save(reflow(widgets.map((x) => (x.id === editing.id ? { ...x, ...p } : x))), t("위젯을 저장했습니다"));
            }
          }}
        />
      )}
    </div>
  );
}
