"use client";

import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, RotateCw, Search } from "lucide-react";
import type { StreamState } from "@/lib/useLogStream";
import { cx } from "./ui";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { t } from "@/lib/i18n";

export type LogLine = { text: string; service?: string; at?: number; replica?: string; stream?: "stdout" | "stderr" };

const TS_RE = /^(\d{2}:\d{2}:\d{2}) (.*)$/s;

type Level = "error" | "warn" | "stage" | "ok" | "info";

export function levelOf(body: string): Level {
  if (body.startsWith("==>")) return "stage";
  if (/\berror\b|\bfatal\b|panic:/i.test(body)) return "error";
  if (/^warn(ing)?\b|\bWARN\b/.test(body)) return "warn";
  if (/healthcheck OK|배포 완료/.test(body)) return "ok";
  return "info";
}

const BAR: Record<Level, string> = {
  error: "bg-red-500",
  warn: "bg-yellow-500",
  stage: "bg-pink-500",
  ok: "bg-green-500",
  info: "bg-blue-500",
};
const TEXT: Record<Level, string> = {
  error: "text-red-700",
  warn: "text-yellow-600",
  stage: "font-semibold text-pink-600",
  ok: "text-success",
  info: "text-foreground",
};

/** Tokens split by spaces (quotes keep phrases); "-term" excludes. All tokens must match. */
export function makeFilter(q: string): (s: string) => boolean {
  const toks = (q.match(/-?"[^"]*"|\S+/g) ?? []).map((tok) => {
    const neg = tok.startsWith("-") && tok.length > 1;
    const raw = (neg ? tok.slice(1) : tok).replace(/^"|"$/g, "").toLowerCase();
    return { neg, raw };
  });
  if (!toks.length) return () => true;
  return (s) => {
    const l = s.toLowerCase();
    return toks.every((tok) => (tok.raw ? l.includes(tok.raw) === !tok.neg : true));
  };
}

type Row = { n: number; date: string; carried: boolean; body: string; level: Level; service?: string; replica?: string; stream?: string };
const p2 = (n: number) => String(n).padStart(2, "0");
/** Local "M/DD HH:MM:SS". */
function fmtLocal(d: Date) {
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}
/** Server log prefixes are UTC HH:MM:SS; anchor them to the deployment's UTC date. */
function fromUtcHms(base: Date, hms: string): Date {
  const [h, m, s] = hms.split(":").map(Number);
  const d = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth(), base.getUTCDate(), h, m, s));
  if (d.getTime() < base.getTime() - 3600_000) d.setUTCDate(d.getUTCDate() + 1);
  return d;
}
export function tzLabel(): string {
  try {
    return (
      new Intl.DateTimeFormat("en-US", { timeZoneName: "short" }).formatToParts(new Date()).find((p) => p.type === "timeZoneName")
        ?.value ?? ""
    );
  } catch {
    return "";
  }
}
type Cols = { time: boolean; service: boolean; replica: boolean };
const ROW_BG: Record<Level, string> = {
  error: "bg-red-50",
  warn: "bg-yellow-50",
  stage: "",
  ok: "",
  info: "",
};
const RowView = memo(function RowView({ r, cols }: { r: Row; cols: Cols }) {
  return (
    <div role="row" className="font-mono text-xs hover:bg-gray-50" style={{ display: "grid", gridTemplateColumns: gridCols(cols), contain: "layout" }}>
      <div
        role="cell"
        className={cx(
          "h-full select-none overflow-clip whitespace-nowrap py-1 pl-0 pr-2 tabular-nums",
          r.carried ? "text-gray-400" : "text-neutral-secondary",
          ROW_BG[r.level],
        )}
      >
        <div className="relative flex h-full items-start">
          <span className="group relative flex h-full shrink-0 items-stretch pl-3">
            <span className={cx("h-full w-[4px] rounded-full", BAR[r.level])} />
          </span>
          <span className="whitespace-nowrap pl-2.5 tabular-nums">{r.date}</span>
        </div>
      </div>
      {cols.service && (
        <div role="cell" className={cx("truncate py-1 pl-2 pr-2 text-neutral-secondary", ROW_BG[r.level])}>
          {r.service ?? ""}
        </div>
      )}
      {cols.replica && (
        <div role="cell" className={cx("truncate py-1 pl-2 pr-2 text-neutral-secondary", ROW_BG[r.level])} title={r.stream}>
          {r.replica ?? ""}
          {r.stream === "stderr" && <span className="ml-1 text-danger/80">err</span>}
        </div>
      )}
      <div role="cell" className={cx("min-w-0 whitespace-normal break-all py-1 pl-2 pr-2 tabular-nums", ROW_BG[r.level])}>
        <div className={cx("inline-block select-text whitespace-pre-wrap px-1 text-left", TEXT[r.level])}>{r.body || " "}</div>
      </div>
    </div>
  );
});
function gridCols(c: Cols) {
  return [c.time ? "196px" : "0px", c.service ? "140px" : null, c.replica ? "110px" : null, "minmax(0,1fr)"].filter(Boolean).join(" ");
}

export function LogToolbar({ children }: { children: ReactNode }) {
  return (
    <div className="flex w-full max-w-[100vw] flex-col gap-y-3.5 border-b border-b-neutral bg-secondaryBg pb-2">
      <div className="flex items-stretch gap-3 px-6 pt-2">{children}</div>
    </div>
  );
}

export function LogFilter({
  value,
  onChange,
  onApply,
  className,
  placeholder = t("로그 필터: 단어, \"구절\", -제외"),
}: {
  value: string;
  onChange: (v: string) => void;
  onApply?: () => void;
  className?: string;
  placeholder?: string;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useKeyboardShortcuts([
    { keys: "/", label: t("로그 필터에 포커스"), group: t("로그"), onTrigger: () => ref.current?.focus() },
    { keys: "enter", label: t("로그 필터 적용"), group: t("로그"), enabled: !!onApply },
  ]);
  return (
    <div
      className={cx(
        "group relative z-10 grid min-h-[34px] w-full min-w-0 cursor-text grid-cols-[min-content_minmax(0,auto)_min-content] rounded-md border border-gray-200 bg-transparent font-mono text-xs text-foreground transition-colors duration-150 focus-within:border-pink-400 focus-within:outline-none hover:border-gray-400",
        className,
      )}
      onClick={() => ref.current?.focus()}
    >
      <div className="pointer-events-none relative top-0.5 mx-2.5 my-1.5 flex size-4 items-center justify-center text-neutral-secondary opacity-70 group-focus-within:!text-pink-700" aria-hidden="true">
        <Search className="size-4" />
      </div>
      <div className="relative h-full w-full pt-1.5">
        {!value && (
          <div className="pointer-events-none absolute inset-0 h-full w-full overflow-hidden whitespace-pre-wrap pt-1.5 text-sm" aria-hidden="true">
            <span className="font-sans text-neutral-secondary">{placeholder}</span>
          </div>
        )}
        <input
          ref={ref}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && onApply) {
              e.preventDefault();
              onApply();
            }
          }}
          aria-label={t("로그 필터")}
          spellCheck={false}
          autoComplete="off"
          className="relative m-0 block h-5 w-full resize-none overflow-x-hidden whitespace-pre-wrap bg-transparent font-mono text-sm leading-5 text-foreground caret-foreground focus:outline-none"
        />
      </div>
      <div title={t("입력창 포커스")} className="absolute right-1 top-1 grid h-6 w-6 place-items-center rounded-md border border-neutral-light bg-secondaryBg" aria-hidden="true">
        <span className="block select-none text-sm font-black text-neutral-secondary">/</span>
      </div>
    </div>
  );
}

export function LogEmpty({ title, sub }: { title?: string; sub?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-1 px-6 py-14 text-center font-sans">
      {title && <div className="text-base font-semibold text-neutral">{title}</div>}
      {sub && <div className={cx("max-w-[520px]", title ? "text-xs text-neutral-secondary" : "text-sm text-neutral-secondary")}>{sub}</div>}
    </div>
  );
}

const STATE: Record<StreamState, { text: string; cls: string }> = {
  connecting: { text: t("연결 중"), cls: "bg-gray-400 animate-soft-pulse" },
  open: { text: t("실시간"), cls: "bg-green-600 animate-soft-pulse" },
  reconnecting: { text: t("다시 연결하는 중"), cls: "bg-yellow-600 animate-soft-pulse" },
  done: { text: t("스트림 종료"), cls: "bg-gray-400" },
  error: { text: t("연결 끊김"), cls: "bg-red-600" },
};

export function LogTable({
  lines,
  state,
  filter = "",
  baseDate,
  onReconnect,
  emptyText = t("로그가 없습니다"),
  footer,
  className,
  showService,
  showReplica,
  showTime = true,
}: {
  lines: LogLine[];
  state?: StreamState;
  filter?: string;
  /** Date used for lines that carry only HH:MM:SS. */
  baseDate?: string | null;
  onReconnect?: () => void;
  emptyText?: string;
  footer?: ReactNode;
  className?: string;
  showService?: boolean;
  showReplica?: boolean;
  showTime?: boolean;
}) {
  const cols = useMemo(() => ({ time: showTime, service: !!showService, replica: !!showReplica }), [showTime, showService, showReplica]);
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);
  const [tz, setTz] = useState(t("현지"));
  useEffect(() => setTz(tzLabel() || t("현지")), []);
  const stickRef = useRef(stick);
  stickRef.current = stick;
  const rows = useMemo(() => {
    const base = baseDate ? new Date(baseDate) : new Date();
    const out: Row[] = [];
    let last = "";
    lines.forEach((l, i) => {
      const m = TS_RE.exec(l.text);
      let date: string;
      let carried = false;
      if (m) date = last = fmtLocal(fromUtcHms(base, m[1]));
      else if (l.at) date = last = fmtLocal(new Date(l.at));
      else {
        date = last;
        carried = true;
      }
      const body = m ? m[2] : l.text;
      const lv = levelOf(body);
      out.push({ n: i + 1, date, carried, body, level: lv === "info" && l.stream === "stderr" && /\bwarn/i.test(body) ? "warn" : lv, service: l.service, replica: l.replica, stream: l.stream });
    });
    return out;
  }, [lines, baseDate]);
  const pred = useMemo(() => makeFilter(filter), [filter]);
  const shown = useMemo(
    () => (filter.trim() ? rows.filter((r) => pred(`${r.service ?? ""} ${r.replica ?? ""} ${r.body}`)) : rows),
    [rows, pred, filter],
  );
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick) el.scrollTop = el.scrollHeight;
  }, [shown, stick]);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (stickRef.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
    return () => ro.disconnect();
  }, [shown.length === 0]);
  const onScroll = () => {
    const el = ref.current;
    if (el) setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 32);
  };
  return (
    <div className={cx("relative flex min-h-0 flex-col bg-background", className)}>
      <div role="rowgroup" className="relative w-full shrink-0 bg-secondaryBg after:absolute after:inset-x-0 after:bottom-0 after:border-b after:border-b-neutral after:content-['']">
        <div role="row" className="grid w-full select-none text-left text-xs font-medium text-neutral-secondary" style={{ gridTemplateColumns: cols.time ? `${gridCols(cols)}` : gridCols(cols) }}>
          {showTime ? (
            <div role="columnheader" className="relative flex items-center whitespace-nowrap bg-secondaryBg px-2 py-1.5 pl-6">
              {t("날짜")} <span className="ml-1 text-gray-400">({tz})</span>
            </div>
          ) : (
            <div />
          )}
          {showService && (
            <div role="columnheader" className="relative flex items-center whitespace-nowrap bg-secondaryBg px-2 py-1.5">
              {t("서비스")}
            </div>
          )}
          {showReplica && (
            <div role="columnheader" className="relative flex items-center whitespace-nowrap bg-secondaryBg px-2 py-1.5">
              {t("레플리카|one")}
            </div>
          )}
          <div role="columnheader" className="relative flex items-center whitespace-nowrap bg-secondaryBg px-2 py-1.5">
            {t("메시지")}
            {state && (
              <span className="ml-auto flex items-center gap-1.5 pr-3 text-xs font-normal">
                <span className={cx("size-1.5 rounded-full", STATE[state].cls)} />
                <span className="hidden sm:inline">{STATE[state].text}</span>
                {(state === "error" || state === "done") && onReconnect && (
                  <button type="button" onClick={onReconnect} className="ml-1 flex cursor-pointer items-center gap-1 rounded px-1 text-gray-600 hover:text-foreground">
                    <RotateCw className="size-3" /> {t("다시 연결")}
                  </button>
                )}
                <span className="ml-2 tabular-nums text-gray-400">
                  {filter.trim() ? `${shown.length.toLocaleString()} / ` : ""}
                  {t("{count}줄", { count: rows.length.toLocaleString() })}
                </span>
              </span>
            )}
          </div>
        </div>
      </div>
      <div
        ref={ref}
        onScroll={onScroll}
        role="log"
        data-noscroll
        className="min-h-0 flex-1 overflow-y-auto font-mono text-xs [overflow-anchor:none]"
      >
        <div>
          {shown.length === 0 ? (
            <LogEmpty
              title={rows.length && filter.trim() ? t("이 필터에 맞는 로그가 없습니다") : state === "connecting" ? t("로그를 불러오는 중…") : undefined}
              sub={rows.length && filter.trim() ? t("검색어를 줄이거나 지워 보세요.") : state === "connecting" ? t("로그를 가져오는 동안 잠시 기다려 주세요.") : emptyText}
            />
          ) : (
            shown.map((r) => <RowView key={r.n} r={r} cols={cols} />)
          )}
          {footer}
        </div>
      </div>
      <div className="absolute bottom-4 right-4 flex flex-col gap-2">
        <button
          type="button"
          aria-label={t("맨 위로")}
          title={t("맨 위로")}
          onClick={() => {
            setStick(false);
            ref.current?.scrollTo({ top: 0 });
          }}
          className="flex size-9 cursor-pointer items-center justify-center rounded-md border border-gray-200 bg-secondaryBg text-gray-600 hover:border-gray-300 hover:text-foreground"
        >
          <ArrowUp className="size-4" />
        </button>
        <button
          type="button"
          aria-label={t("맨 아래로")}
          title={t("맨 아래로")}
          onClick={() => setStick(true)}
          className={cx(
            "flex size-9 cursor-pointer items-center justify-center rounded-md border bg-secondaryBg hover:text-foreground",
            stick ? "border-gray-200 text-gray-600" : "border-pink-500 text-pink-600",
          )}
        >
          <ArrowDown className="size-4" />
        </button>
      </div>
    </div>
  );
}
/** Split a deployment log where the build ends: the first "==> pre_deploy", "==> deploying" or "==> starting" marker. */
export function splitDeploymentLog(lines: string[]): { build: string[]; deploy: string[] } {
  const i = lines.findIndex((l) => {
    const m = TS_RE.exec(l);
    return /^==> (pre_deploy|pre-deploy|deploying|starting)\b/.test(m ? m[2] : l);
  });
  return i < 0 ? { build: lines, deploy: [] } : { build: lines.slice(0, i), deploy: lines.slice(i) };
}
/** Attach arrival times to streamed lines that carry no timestamp of their own. */
export function useStampedLines(lines: string[], service?: string): LogLine[] {
  const times = useRef<number[]>([]);
  return useMemo(() => {
    if (lines.length < times.current.length) times.current = [];
    const now = Date.now();
    while (times.current.length < lines.length) times.current.push(now);
    return lines.map((text, i) => ({ text, service, at: times.current[i] }));
  }, [lines, service]);
}
/** One histogram bucket: gray volume with the error share stacked in red at the bottom; empty buckets show a faint stub. */
export function HistogramBar({ n, err, max }: { n: number; err: number; max: number }) {
  if (!n) return <div className="h-[2px] flex-1 rounded-[1px] bg-gray-100" />;
  return (
    <div className="flex flex-1 flex-col justify-end overflow-hidden rounded-[1px]" style={{ height: `${Math.max(8, (n / max) * 100)}%` }}>
      {n > err && <div className="min-h-px bg-gray-300" style={{ flexGrow: n - err }} />}
      {err > 0 && <div className="min-h-[2px] bg-danger-button" style={{ flexGrow: err }} />}
    </div>
  );
}

export function LogHistogram({ lines, buckets = 64 }: { lines: LogLine[]; buckets?: number }) {
  const data = useMemo(() => {
    const pts = lines.filter((l) => l.at).map((l) => ({ t: l.at as number, err: l.stream === "stderr" || levelOf(l.text) === "error" }));
    if (!pts.length) return null;
    const t0 = Math.min(...pts.map((p) => p.t));
    const t1 = Math.max(...pts.map((p) => p.t), t0 + 1);
    const span = (t1 - t0) / buckets;
    const bins = Array.from({ length: buckets }, () => ({ n: 0, err: 0 }));
    for (const p of pts) {
      const i = Math.min(buckets - 1, Math.floor((p.t - t0) / span));
      bins[i].n++;
      if (p.err) bins[i].err++;
    }
    return { t0, t1, bins, max: Math.max(...bins.map((b) => b.n), 1) };
  }, [lines, buckets]);
  if (!data) return null;
  const hm = (t: number) => {
    const d = new Date(t);
    return `${p2(d.getHours())}:${p2(d.getMinutes())}`;
  };
  return (
    <div className="shrink-0 bg-secondaryBg px-6 pb-2" aria-hidden="true">
      <div className="flex h-9 items-end gap-px">
        {data.bins.map((b, i) => (
          <HistogramBar key={i} n={b.n} err={b.err} max={data.max} />
        ))}
      </div>
      <div className="mt-0.5 flex justify-between text-xs tabular-nums text-neutral-secondary">
        <span>{hm(data.t0)}</span>
        <span>{hm(data.t1)}</span>
      </div>
    </div>
  );
}

/** Client-side download of the loaded lines as a text file. */
export function downloadLogs(lines: LogLine[], name = "logs.txt") {
  const text = lines.map((l) => `${l.at ? new Date(l.at).toISOString() + " " : ""}${l.service ? `[${l.service}] ` : ""}${l.text}`).join("\n");
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  a.download = name;
  a.click();
  URL.revokeObjectURL(a.href);
}
