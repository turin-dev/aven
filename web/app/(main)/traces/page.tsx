"use client";

import { Suspense, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AtSign, Radio, Settings2, Waypoints } from "lucide-react";
import { useCanvas, useCaps } from "@/lib/queries";
import { useCtxHref } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { fmtSpanMs, isoMs, parseTraceId, useTraceAttributeKeys, useTraces, useTracingSettings, type TraceSummary } from "@/lib/tracing";
import { HistogramBar, LogFilter, LogToolbar } from "@/components/LogTable";
import { iconKey } from "@/components/ServiceIcon";
import { TraceView } from "@/components/traces/TraceView";
import { TracingSetup } from "@/components/traces/TracingSetup";
import { Button, EmptyState, ErrorBox, Input, Menu, Segmented, Spinner, cx } from "@/components/ui";
import { t, tr } from "@/lib/i18n";

const RANGES = [
  { value: "15m", label: t("15분"), ms: 15 * 60e3 },
  { value: "1h", label: t("1시간"), ms: 3600e3 },
  { value: "6h", label: t("6시간"), ms: 6 * 3600e3 },
  { value: "24h", label: t("24시간"), ms: 24 * 3600e3 },
  { value: "7d", label: t("7일"), ms: 7 * 24 * 3600e3 },
] as const;
type Range = (typeof RANGES)[number]["value"];

const p2 = (n: number) => String(n).padStart(2, "0");
function fmtStart(iso: string) {
  const d = new Date(iso);
  return `${d.getMonth() + 1}/${d.getDate()} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, "0")}`;
}

function TraceHistogram({ items, rangeMs, buckets = 60 }: { items: TraceSummary[]; rangeMs: number; buckets?: number }) {
  const data = useMemo(() => {
    const t1 = Date.now();
    const t0 = t1 - rangeMs;
    const bins = Array.from({ length: buckets }, () => ({ n: 0, err: 0 }));
    for (const it of items) {
      const i = Math.floor(((isoMs(it.started_at) - t0) / rangeMs) * buckets);
      if (i < 0 || i >= buckets) continue;
      bins[i].n++;
      if (it.error_count > 0) bins[i].err++;
    }
    return { t0, t1, bins, max: Math.max(...bins.map((b) => b.n), 1) };
  }, [items, rangeMs, buckets]);
  const label = (ms: number) => {
    const d = new Date(ms);
    return rangeMs > 24 * 3600e3 ? `${d.getMonth() + 1}/${d.getDate()}` : `${p2(d.getHours())}:${p2(d.getMinutes())}`;
  };
  return (
    <div className="shrink-0 bg-secondaryBg px-6 pb-2" aria-hidden="true">
      <div className="flex h-9 items-end gap-px">
        {data.bins.map((b, i) => (
          <HistogramBar key={i} n={b.n} err={b.err} max={data.max} />
        ))}
      </div>
      <div className="mt-0.5 flex justify-between text-xs tabular-nums text-neutral-secondary">
        <span>{label(data.t0)}</span>
        <span>{label(data.t1)}</span>
      </div>
    </div>
  );
}

/** Outline toolbar switch (errors only, live): selected state matches the Segmented selection. */
function ToggleChip({ on, onToggle, children }: { on: boolean; onToggle: () => void; children: ReactNode }) {
  return (
    <Button
      variant="outline"
      role="switch"
      aria-checked={on}
      onClick={onToggle}
      className={on ? "!border-gray-300 bg-secondaryBg text-pink-500" : "text-gray-600 hover:text-foreground"}
    >
      {children}
    </Button>
  );
}

function TracesInner() {
  const ws = useWorkspace();
  const eid = ws.env?.id;
  const caps = useCaps(ws.pid);
  const canvas = useCanvas(eid);
  const params = useSearchParams();
  const router = useRouter();
  const href = useCtxHref();
  const traceId = params.get("trace");
  const range = (RANGES.find((r) => r.value === params.get("range"))?.value ?? "1h") as Range;
  const rangeMs = RANGES.find((r) => r.value === range)!.ms;
  const errorsOnly = params.get("errors") === "1";
  const [q, setQ] = useState(params.get("q") ?? "");
  const [query, setQuery] = useState(q);
  const [idInput, setIdInput] = useState("");
  const [idError, setIdError] = useState(false);
  const [live, setLive] = useState(false);
  const [setupOpen, setSetupOpen] = useState(false);
  const autoOpened = useRef(false);

  const go = (patch: Record<string, string | null>) => {
    const cur = { trace: traceId, range: range === "1h" ? null : range, errors: errorsOnly ? "1" : null, q: query || null };
    router.replace(href("/traces/", { ...cur, ...patch }), { scroll: false });
  };

  useEffect(() => {
    const timer = setTimeout(() => setQuery(q), 400);
    return () => clearTimeout(timer);
  }, [q]);
  useEffect(() => {
    if ((params.get("q") ?? "") !== query) go({ q: query || null });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

  const traces = useTraces(eid, { since: range, filter: query, errors: errorsOnly }, live);
  const settings = useTracingSettings(eid);
  const attrKeys = useTraceAttributeKeys(eid, range);
  const items = useMemo(() => [...(traces.data ?? [])].reverse(), [traces.data]);
  const icons = useMemo(() => new Map((canvas.data?.services ?? []).map((s) => [s.id, iconKey(s)])), [canvas.data]);

  // The setup panel opens by itself while the environment has never received a span.
  useEffect(() => {
    if (autoOpened.current || !settings.data || traces.isPending || traceId) return;
    const never = settings.data.every((s) => !s.last_edge_span_at && !s.last_service_span_at);
    if (never && (traces.data?.length ?? 0) === 0) {
      autoOpened.current = true;
      setSetupOpen(true);
    }
  }, [settings.data, traces.data, traces.isPending, traceId]);

  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = listRef.current;
    if (el && !traceId) el.scrollTop = el.scrollHeight;
  }, [items.length, traceId]);

  const openId = (e: React.FormEvent) => {
    e.preventDefault();
    const id = parseTraceId(idInput);
    setIdError(!id);
    if (id) {
      go({ trace: id });
      setIdInput("");
    }
  };
  const tracedCount = settings.data?.filter((s) => s.enabled).length ?? 0;

  return (
    <div className="flex h-full flex-col bg-background">
      <LogToolbar>
        <form onSubmit={openId} className="w-[250px] shrink-0">
          <Input
            size="sm"
            mono
            invalid={idError}
            value={idInput}
            onChange={(e) => {
              setIdInput(e.target.value);
              setIdError(false);
            }}
            placeholder={t("Trace ID 또는 traceparent")}
            aria-label="Trace ID"
            aria-invalid={idError}
            className="h-[34px] px-3 placeholder:font-sans"
          />
        </form>
        <LogFilter value={q} onChange={setQ} className="flex-1" placeholder={t("필터: @status:error @http.route:/api @duration:>500")} />
        <Menu
          label={t("속성 키")}
          trigger={(p) => (
            <Button variant="outline" {...p} aria-label={t("속성 키로 필터")} title={t("속성 키로 필터")} className="w-[34px] px-0 text-gray-500">
              <AtSign />
            </Button>
          )}
        >
          {(close) => (
            <div className="w-[280px]">
              {[
                "status:error",
                "component:edge",
                "kind:client",
                "duration:>500",
                "service:",
                "name:",
                ...(attrKeys.data ?? []).map((k) => `${k}:`),
              ].map((k) => (
                <button
                  key={k}
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setQ((cur) => (cur.trim() ? `${cur.trim()} @${k}` : `@${k}`));
                    close();
                  }}
                  className="mb-0.5 block h-8 w-full cursor-pointer truncate rounded px-3 text-left font-mono text-xs text-neutral-secondary last:mb-0 hover:bg-neutral-surface-hover hover:text-foreground focus:bg-neutral-surface-hover focus:outline-none"
                >
                  @{k}
                </button>
              ))}
            </div>
          )}
        </Menu>
      </LogToolbar>
      <div className="flex shrink-0 flex-wrap items-center gap-3 bg-secondaryBg px-6 py-2">
        <Segmented label={t("기간")} value={range} options={RANGES.map((r) => ({ value: r.value, label: r.label }))} onChange={(v) => go({ range: v === "1h" ? null : v })} />
        <ToggleChip on={errorsOnly} onToggle={() => go({ errors: errorsOnly ? null : "1" })}>
          <span className={cx("size-1.5 rounded-full", errorsOnly ? "bg-red-500" : "bg-gray-400")} aria-hidden="true" />
          {t("오류만")}
        </ToggleChip>
        <ToggleChip on={live} onToggle={() => setLive((v) => !v)}>
          <Radio className={cx(live && "animate-soft-pulse")} />
          {t("실시간")}
        </ToggleChip>
        <span className="text-xs tabular-nums text-gray-500">
          {traces.data ? t("trace {count}개{hint}", { count: traces.data.length, hint: traces.data.length >= 500 ? t(" (최대 500개, 필터나 기간을 좁히세요)") : "" }) : ""}
        </span>
        <Button variant="outline" className="ml-auto" onClick={() => setSetupOpen(true)}>
          <Settings2 />
          {t("트레이싱 설정")}
          {settings.data && <span className="text-xs tabular-nums text-gray-500">{tracedCount}/{settings.data.length}</span>}
        </Button>
      </div>
      <TraceHistogram items={traces.data ?? []} rangeMs={rangeMs} />
      {ws.error || traces.error ? (
        <div className="px-6 py-3">
          <ErrorBox error={ws.error ?? traces.error} />
        </div>
      ) : ws.isPending || traces.isPending ? (
        <div className="flex flex-1 items-center justify-center">
          <Spinner />
        </div>
      ) : (
        <div className={cx("flex min-h-0 flex-col", traceId ? "max-h-[38%] shrink-0" : "flex-1")}>
          <div
            role="row"
            className="relative flex shrink-0 items-center gap-3 bg-secondaryBg px-6 py-1.5 text-xs font-medium text-neutral-secondary after:absolute after:inset-x-0 after:bottom-0 after:border-b after:border-b-neutral after:content-['']"
          >
            <span className="w-[150px] shrink-0">{t("시작")}</span>
            <span className="min-w-0 flex-1">{t("루트 span")}</span>
            <span className="hidden w-[140px] shrink-0 md:block">{t("서비스")}</span>
            <span className="w-[72px] shrink-0 text-right">{t("소요")}</span>
            <span className="w-[52px] shrink-0 text-right">span</span>
            <span className="w-[52px] shrink-0 text-right">{t("오류")}</span>
          </div>
          <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto" role="table" aria-label={t("trace 목록")}>
            {items.length === 0 ? (
              <div className="px-6 py-6">
                <EmptyState icon={<Waypoints className="size-6" />} title={t("trace가 없습니다")}>
                  {query || errorsOnly
                    ? t("조건에 맞는 trace가 없습니다. 필터나 기간을 바꿔 보세요.")
                    : tracedCount === 0
                      ? t("trace하는 서비스가 없습니다. 트레이싱 설정에서 서비스를 켜세요.")
                      : t("이 기간에 들어온 trace가 없습니다. 공개 도메인으로 요청을 보내 보세요.")}
                </EmptyState>
              </div>
            ) : (
              items.map((row) => (
                <button
                  key={row.trace_id}
                  type="button"
                  role="row"
                  aria-selected={row.trace_id === traceId}
                  onClick={() => go({ trace: row.trace_id === traceId ? null : row.trace_id })}
                  className={cx(
                    "flex min-h-[28px] w-full cursor-pointer items-center gap-3 px-6 text-left font-mono text-xs hover:bg-gray-50",
                    row.trace_id === traceId && "bg-gray-100",
                  )}
                >
                  <span className="w-[150px] shrink-0 tabular-nums text-neutral-secondary">{fmtStart(row.started_at)}</span>
                  <span className="min-w-0 flex-1 truncate text-foreground" title={row.root_name}>
                    {row.root_name || t("(이름 없음)")}
                  </span>
                  <span className="hidden w-[140px] shrink-0 truncate text-gray-500 md:block">{row.service_name || "—"}</span>
                  <span className={cx("w-[72px] shrink-0 text-right tabular-nums", row.duration_ms >= 500 ? "text-warn" : "text-gray-600")}>
                    {fmtSpanMs(row.duration_ms)}
                  </span>
                  <span className="w-[52px] shrink-0 text-right tabular-nums text-gray-600">{row.span_count}</span>
                  <span className={cx("w-[52px] shrink-0 text-right tabular-nums", row.error_count ? "font-semibold text-danger" : "text-gray-400")}>
                    {row.error_count}
                  </span>
                </button>
              ))
            )}
          </div>
        </div>
      )}
      {traceId && <TraceView eid={eid} traceId={traceId} onClose={() => go({ trace: null })} />}
      <TracingSetup
        open={setupOpen}
        onClose={() => setSetupOpen(false)}
        eid={eid}
        envName={ws.env?.name ?? ""}
        canEdit={caps.canEdit}
        icons={icons}
      />
    </div>
  );
}

export default function TracesPage() {
  return (
    <Suspense fallback={<div className="h-full bg-background" />}>
      <TracesInner />
    </Suspense>
  );
}
