"use client";

import { useEffect, useMemo, useState } from "react";
import { AlertCircle, ChevronDown, ChevronRight, X } from "lucide-react";
import type { ID } from "@/lib/api";
import { ApiError } from "@/lib/api";
import { fmtSpanMs, isoMs, serviceColor, spanTree, useTrace, type TraceSpan } from "@/lib/tracing";
import { t } from "@/lib/i18n";
import { CopyButton, ErrorBox, IconButton, Spinner, cx } from "@/components/ui";

function fmtValue(v: unknown): string {
  if (typeof v === "string") return v;
  return JSON.stringify(v);
}

function AttrTable({ title, attrs }: { title: string; attrs: Record<string, unknown> | undefined }) {
  const entries = Object.entries(attrs ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (!entries.length) return null;
  return (
    <section className="mt-4">
      <h4 className="mb-1.5 text-xs font-semibold text-gray-600">{title}</h4>
      <dl className="grid grid-cols-[minmax(120px,40%)_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-xs">
        {entries.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="truncate text-gray-500" title={k}>
              {k}
            </dt>
            <dd className="break-all text-foreground">{fmtValue(v)}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function SpanDetail({ span, t0, onClose }: { span: TraceSpan; t0: number; onClose: () => void }) {
  const offset = isoMs(span.start_time) - t0;
  return (
    <div className="min-h-0 overflow-y-auto border-t border-gray-200 bg-secondaryBg px-5 py-4 lg:border-l lg:border-t-0">
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="break-all text-sm font-semibold text-foreground">{span.name || t("(이름 없음)")}</div>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
            <span className="flex items-center gap-1.5">
              <span className="size-2 rounded-full" style={{ background: serviceColor(span.service_id || span.service_name || span.component) }} />
              {span.service_name || "—"}
            </span>
            <span>{span.component}</span>
            <span>{span.kind}</span>
            <span className="tabular-nums">{fmtSpanMs(span.duration_ms)}</span>
            <span className="tabular-nums">+{fmtSpanMs(offset)}</span>
            <span
              className={cx(
                "rounded-md px-1.5 py-0.5 font-medium",
                span.status === "error"
                  ? "bg-red-100 text-red-500 dark:bg-red-200 dark:text-red-700"
                  : span.status === "ok"
                    ? "bg-green-100 text-green-500 dark:bg-green-200 dark:text-green-700"
                    : "bg-gray-200 text-gray-500",
              )}
            >
              {span.status}
            </span>
          </div>
        </div>
        <IconButton label={t("span 닫기")} size="sm" onClick={onClose}>
          <X />
        </IconButton>
      </div>
      <dl className="mt-3 grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-xs">
        <dt className="text-gray-500">span</dt>
        <dd className="flex items-center gap-1 text-foreground">
          {span.span_id}
          <CopyButton value={span.span_id} label={t("span ID 복사")} />
        </dd>
        {span.parent_span_id && (
          <>
            <dt className="text-gray-500">parent</dt>
            <dd className="text-foreground">{span.parent_span_id}</dd>
          </>
        )}
        {span.deployment_id && (
          <>
            <dt className="text-gray-500">deployment</dt>
            <dd className="break-all text-foreground">{span.deployment_id}</dd>
          </>
        )}
        {span.replica_id && (
          <>
            <dt className="text-gray-500">replica</dt>
            <dd className="break-all text-foreground">{span.replica_id}</dd>
          </>
        )}
        <dt className="text-gray-500">start</dt>
        <dd className="text-foreground">{new Date(span.start_time).toLocaleString()}</dd>
      </dl>
      <AttrTable title={t("속성")} attrs={span.attributes} />
      <AttrTable title={t("리소스 속성")} attrs={span.resource_attributes} />
      {span.events.length > 0 && (
        <section className="mt-4">
          <h4 className="mb-1.5 text-xs font-semibold text-gray-600">{t("이벤트 {count}", { count: span.events.length })}</h4>
          <ul className="space-y-2">
            {span.events.map((e, i) => (
              <li key={i} className="rounded-md border border-gray-200 p-2">
                <div className="flex items-center gap-2 text-xs">
                  <span className={cx("font-medium", e.name === "exception" ? "text-danger" : "text-foreground")}>{e.name}</span>
                  <span className="tabular-nums text-gray-500">+{fmtSpanMs(isoMs(e.time) - t0)}</span>
                </div>
                {e.attributes && Object.keys(e.attributes).length > 0 && (
                  <dl className="mt-1 grid grid-cols-[minmax(100px,35%)_minmax(0,1fr)] gap-x-3 gap-y-0.5 font-mono text-xs">
                    {Object.entries(e.attributes).map(([k, v]) => (
                      <div key={k} className="contents">
                        <dt className="truncate text-gray-500">{k}</dt>
                        <dd className="whitespace-pre-wrap break-all text-foreground">{fmtValue(v)}</dd>
                      </div>
                    ))}
                  </dl>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {span.links.length > 0 && (
        <section className="mt-4">
          <h4 className="mb-1.5 text-xs font-semibold text-gray-600">{t("링크 {count}", { count: span.links.length })}</h4>
          <ul className="space-y-1 font-mono text-xs">
            {span.links.map((l, i) => (
              <li key={i} className="break-all text-foreground">
                {l.trace_id} / {l.span_id}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

/** Trace waterfall: spans nested under their parents with timing bars, a color per service, and a span detail panel. */
export function TraceView({ eid, traceId, onClose }: { eid: ID | undefined; traceId: string; onClose: () => void }) {
  const trace = useTrace(eid, traceId);
  const spans = useMemo(() => trace.data ?? [], [trace.data]);
  const rows = useMemo(() => spanTree(spans), [spans]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [sel, setSel] = useState<string | null>(null);
  useEffect(() => {
    setSel(null);
    setCollapsed(new Set());
  }, [traceId]);

  const { t0, total } = useMemo(() => {
    if (!spans.length) return { t0: 0, total: 1 };
    const s = Math.min(...spans.map((x) => isoMs(x.start_time)));
    const e = Math.max(...spans.map((x) => isoMs(x.end_time)));
    return { t0: s, total: Math.max(e - s, 0.001) };
  }, [spans]);

  // Hide descendants of collapsed spans.
  const visible = useMemo(() => {
    const out: typeof rows = [];
    let hideBelow: number | null = null;
    for (const r of rows) {
      if (hideBelow !== null && r.depth > hideBelow) continue;
      hideBelow = collapsed.has(r.span.span_id) ? r.depth : null;
      out.push(r);
    }
    return out;
  }, [rows, collapsed]);
  const selected = spans.find((s) => s.span_id === sel) ?? null;
  const services = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of spans) m.set(s.service_id || s.service_name || s.component, s.service_name || s.component);
    return [...m.entries()];
  }, [spans]);
  const errors = spans.filter((s) => s.status === "error").length;
  const ticks = [0, 0.25, 0.5, 0.75, 1];

  return (
    <div className="flex min-h-0 flex-1 flex-col border-t border-gray-200 bg-background">
      <div className="flex shrink-0 items-center gap-3 border-b border-gray-200 px-6 py-2.5">
        <span className="text-sm font-semibold text-foreground">Trace</span>
        <span className="truncate font-mono text-xs text-gray-600">{traceId}</span>
        <CopyButton value={traceId} label={t("trace ID 복사")} />
        {spans.length > 0 && (
          <span className="flex flex-wrap items-center gap-x-3 text-xs text-gray-500">
            <span className="tabular-nums">{fmtSpanMs(total)}</span>
            <span className="tabular-nums">{t("span {count}개", { count: spans.length })}</span>
            {errors > 0 && <span className="tabular-nums text-danger">{t("오류 {count}", { count: errors })}</span>}
            {services.map(([k, name]) => (
              <span key={k} className="flex items-center gap-1">
                <span className="size-2 rounded-full" style={{ background: serviceColor(k) }} />
                {name}
              </span>
            ))}
          </span>
        )}
        <IconButton label={t("trace 닫기")} size="sm" onClick={onClose} className="ml-auto">
          <X />
        </IconButton>
      </div>
      {trace.isPending ? (
        <div className="flex flex-1 items-center justify-center">
          <Spinner />
        </div>
      ) : trace.error ? (
        <div className="px-6 py-4">
          {trace.error instanceof ApiError && trace.error.status === 404 ? (
            <div className="flex items-center gap-2 text-sm text-gray-500">
              <AlertCircle className="size-4" />{t("이 환경에서 trace를 찾지 못했습니다. 다른 환경에서 보낸 span이거나 보관 기간이 지났을 수 있습니다.")}
            </div>
          ) : (
            <ErrorBox error={trace.error} />
          )}
        </div>
      ) : (
        <div className={cx("grid min-h-0 flex-1", selected ? "grid-rows-[minmax(0,1fr)_minmax(0,1fr)] lg:grid-cols-[minmax(0,1fr)_400px] lg:grid-rows-1" : "grid-rows-1")}>
          <div className="min-h-0 overflow-y-auto" role="tree" aria-label={t("span 워터폴")}>
            <div className="sticky top-0 z-10 flex h-7 items-center border-b border-gray-100 bg-background text-xs text-gray-400">
              <div className="w-[38%] min-w-[220px] shrink-0 px-6">span</div>
              <div className="relative h-full flex-1 pr-6">
                {ticks.map((f) => (
                  <span key={f} className="absolute top-1.5 -translate-x-1/2 whitespace-nowrap tabular-nums first:translate-x-0 last:-translate-x-full" style={{ left: `${f * 100}%` }}>
                    {fmtSpanMs(total * f)}
                  </span>
                ))}
              </div>
            </div>
            {visible.map(({ span, depth, hasChildren }) => {
              const left = ((isoMs(span.start_time) - t0) / total) * 100;
              const width = Math.max((span.duration_ms / total) * 100, 0.3);
              const color = serviceColor(span.service_id || span.service_name || span.component);
              const isCollapsed = collapsed.has(span.span_id);
              return (
                <div
                  key={span.span_id}
                  role="treeitem"
                  aria-selected={sel === span.span_id}
                  aria-level={depth + 1}
                  aria-expanded={hasChildren ? !isCollapsed : undefined}
                  tabIndex={0}
                  onClick={() => setSel(span.span_id === sel ? null : span.span_id)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      setSel(span.span_id === sel ? null : span.span_id);
                    }
                  }}
                  className={cx(
                    "flex h-8 cursor-pointer items-center text-xs hover:bg-gray-50 focus:outline-none focus-visible:bg-gray-100",
                    sel === span.span_id && "bg-gray-100",
                  )}
                >
                  <div className="flex w-[38%] min-w-[220px] shrink-0 items-center gap-1 overflow-hidden px-6" style={{ paddingLeft: 24 + depth * 14 }}>
                    {hasChildren ? (
                      <button
                        type="button"
                        aria-label={isCollapsed ? t("펼치기") : t("접기")}
                        onClick={(e) => {
                          e.stopPropagation();
                          setCollapsed((c) => {
                            const n = new Set(c);
                            if (n.has(span.span_id)) n.delete(span.span_id);
                            else n.add(span.span_id);
                            return n;
                          });
                        }}
                        className="flex size-4 shrink-0 cursor-pointer items-center justify-center rounded text-gray-500 hover:bg-gray-200"
                      >
                        {isCollapsed ? <ChevronRight className="size-3" /> : <ChevronDown className="size-3" />}
                      </button>
                    ) : (
                      <span className="size-4 shrink-0" />
                    )}
                    <span className="size-2 shrink-0 rounded-full" style={{ background: color }} aria-hidden="true" />
                    {span.status === "error" && <AlertCircle className="size-3.5 shrink-0 text-danger" aria-label={t("오류")} />}
                    <span className="truncate text-foreground" title={span.name}>
                      {span.name || t("(이름 없음)")}
                    </span>
                    <span className="ml-1 shrink-0 text-gray-400">{span.service_name || span.component}</span>
                  </div>
                  <div className="relative h-full flex-1 pr-6">
                    <div className="absolute inset-y-0 left-0 right-6">
                      <div
                        className={cx("absolute top-2 h-4 rounded-sm", span.status === "error" && "ring-1 ring-red-500")}
                        style={{ left: `${left}%`, width: `${width}%`, background: color, opacity: 0.85 }}
                      />
                      <span
                        className="absolute top-2 whitespace-nowrap pl-1 text-xs leading-4 tabular-nums text-gray-500"
                        style={left + width > 80 ? { right: `${100 - left}%`, paddingRight: 4 } : { left: `${left + width}%` }}
                      >
                        {fmtSpanMs(span.duration_ms)}
                      </span>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
          {selected && <SpanDetail span={selected} t0={t0} onClose={() => setSel(null)} />}
        </div>
      )}
    </div>
  );
}
