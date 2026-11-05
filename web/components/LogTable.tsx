"use client";

import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { ArrowDown, ArrowUp, Check, Clock, Copy, Download, RotateCw, Search, WrapText } from "lucide-react";
import type { StreamState } from "@/lib/useLogStream";
import {
  TS_RE,
  fromUtcHms,
  highlightTerms,
  levelOf,
  makeFilter,
  markSegments,
  matchRanges,
  parseAnsi,
  stripAnsi,
  type AnsiStyle,
  type Level,
} from "@/lib/logs";
import { cx } from "./ui";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { t } from "@/lib/i18n";

export { levelOf, makeFilter } from "@/lib/logs";

export type LogLine = { text: string; service?: string; at?: number; replica?: string; stream?: "stdout" | "stderr" };

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

type Row = {
  n: number;
  /** Epoch ms of the line (or of the last stamped line above it when `carried`). */
  ts: number | null;
  carried: boolean;
  /** Raw message (may hold ANSI escapes). */
  body: string;
  /** Message without escapes: filtering, highlighting, copying. */
  plain: string;
  level: Level;
  service?: string;
  replica?: string;
  stream?: string;
};
const p2 = (n: number) => String(n).padStart(2, "0");
/** Local "YYYY-MM-DD HH:MM:SS". */
function fmtLocal(d: Date) {
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
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

/* ANSI colours map to theme tokens so they stay readable in light and dark mode. */
const FG: Record<string, string> = {
  black: "text-gray-500",
  red: "text-red-600",
  green: "text-green-600",
  yellow: "text-yellow-600",
  blue: "text-blue-600",
  magenta: "text-pink-600",
  cyan: "text-cyan-600",
  white: "text-gray-600",
  "bright-black": "text-gray-400",
  "bright-red": "text-red-500",
  "bright-green": "text-green-500",
  "bright-yellow": "text-yellow-500",
  "bright-blue": "text-blue-500",
  "bright-magenta": "text-pink-500",
  "bright-cyan": "text-cyan-500",
  "bright-white": "text-foreground",
};
const BG: Record<string, string> = {
  black: "bg-gray-200",
  red: "bg-red-100",
  green: "bg-green-100",
  yellow: "bg-yellow-100",
  blue: "bg-blue-100",
  magenta: "bg-pink-100",
  cyan: "bg-cyan-100",
  white: "bg-gray-100",
  "bright-black": "bg-gray-200",
  "bright-red": "bg-red-200",
  "bright-green": "bg-green-200",
  "bright-yellow": "bg-yellow-200",
  "bright-blue": "bg-blue-200",
  "bright-magenta": "bg-pink-200",
  "bright-cyan": "bg-cyan-200",
  "bright-white": "bg-gray-50",
};
function segClass(st?: AnsiStyle): string | undefined {
  if (!st) return undefined;
  return (
    cx(
      st.fg && FG[st.fg],
      st.bg && BG[st.bg],
      st.bold && "font-bold",
      st.dim && "opacity-70",
      st.italic && "italic",
      st.underline && "underline",
    ) || undefined
  );
}
function segStyle(st?: AnsiStyle): CSSProperties | undefined {
  if (!st) return undefined;
  const color = st.fg && !FG[st.fg] ? st.fg : undefined;
  const backgroundColor = st.bg && !BG[st.bg] ? st.bg : undefined;
  return color || backgroundColor ? { color, backgroundColor } : undefined;
}

function Body({ body, plain, terms }: { body: string; plain: string; terms: string[] }) {
  const segs = markSegments(parseAnsi(body), matchRanges(plain, terms));
  if (segs.length === 1 && !segs[0].st && !segs[0].mark) return <>{segs[0].text || " "}</>;
  return (
    <>
      {segs.map((s, i) =>
        s.mark ? (
          <mark key={i} className={cx("rounded-[2px] bg-yellow-200 text-inherit", segClass(s.st))} style={segStyle(s.st)}>
            {s.text}
          </mark>
        ) : (
          <span key={i} className={segClass(s.st)} style={segStyle(s.st)}>
            {s.text}
          </span>
        ),
      )}
    </>
  );
}

const RowView = memo(function RowView({ r, i, cols, wrap, terms }: { r: Row; i: number; cols: Cols; wrap: boolean; terms: string[] }) {
  const date = r.ts !== null ? fmtLocal(new Date(r.ts)) : "";
  return (
    <div
      role="row"
      data-i={i}
      className="font-mono text-xs hover:bg-gray-50"
      style={{ display: "grid", gridTemplateColumns: gridCols(cols, wrap), ...(wrap ? null : { width: "max-content", minWidth: "100%" }) }}
    >
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
          {cols.time && <span className="whitespace-nowrap pl-2.5 tabular-nums">{date}</span>}
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
      <div role="cell" className={cx("min-w-0 py-1 pl-2 pr-2 tabular-nums", ROW_BG[r.level])}>
        <div className={cx("select-text px-1 text-left", wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre", TEXT[r.level])}>
          <Body body={r.body} plain={r.plain} terms={terms} />
        </div>
      </div>
    </div>
  );
});
const TIME_W = 196;
const BAR_W = 24;
function gridCols(c: Cols, wrap = true) {
  return [c.time ? `${TIME_W}px` : `${BAR_W}px`, c.service ? "140px" : null, c.replica ? "110px" : null, wrap ? "minmax(0,1fr)" : "auto"].filter(Boolean).join(" ");
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
          className="relative m-0 block h-5 w-full resize-none overflow-x-hidden whitespace-pre-wrap bg-transparent pr-8 font-mono text-sm leading-5 text-foreground caret-foreground focus:outline-none"
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

/** A boolean view preference kept in localStorage (read after mount so SSR markup matches). */
function usePref(key: string, initial: boolean) {
  const [v, setV] = useState(initial);
  useEffect(() => {
    try {
      const s = window.localStorage.getItem(key);
      if (s !== null) setV(s === "1");
    } catch {}
  }, [key]);
  const set = (next: boolean) => {
    setV(next);
    try {
      window.localStorage.setItem(key, next ? "1" : "0");
    } catch {}
  };
  return [v, set] as const;
}

type LevelFilter = "all" | "warn" | "error";
const levelOk = (lv: Level, f: LevelFilter) => f === "all" || lv === "error" || (f === "warn" && lv === "warn");

/** Largest i in [0, n) with offsets[i] <= y. */
function rowAt(offsets: Float64Array, n: number, y: number) {
  let lo = 0;
  let hi = n - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (offsets[mid] <= y) lo = mid;
    else hi = mid - 1;
  }
  return Math.max(0, lo);
}

const LINE_H = 16;
const ROW_PAD = 8;
const CHAR_W = 7.2;
const OVERSCAN = 800;
const heightKey = (r: Row) => `${r.level}\u0000${r.body}`;

const TOOL_BTN =
  "inline-flex h-7 cursor-pointer items-center gap-1.5 rounded-md px-2 text-xs text-gray-600 hover:bg-gray-100 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500 aria-pressed:bg-gray-100 aria-pressed:text-foreground [&_svg]:size-3.5";

/**
 * Log viewer: virtualized rows (variable heights, measured and cached per line text), ANSI colours,
 * filter highlighting, follow-tail that pauses when you scroll up. `tools` adds level filter, wrap,
 * timestamp, copy and download controls.
 */
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
  tools = false,
  downloadName = "logs.txt",
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
  /** Show the level filter, wrap / timestamp toggles and copy / download buttons. */
  tools?: boolean;
  downloadName?: string;
}) {
  const [wrapPref, setWrap] = usePref("aven.logs.wrap", true);
  const [timePref, setTimePref] = usePref("aven.logs.time", true);
  const [level, setLevel] = useState<LevelFilter>("all");
  const [copied, setCopied] = useState(false);
  const wrap = !tools || wrapPref;
  const time = showTime && (!tools || timePref);
  const cols = useMemo(() => ({ time, service: !!showService, replica: !!showReplica }), [time, showService, showReplica]);
  const scroller = useRef<HTMLDivElement>(null);
  const rowsBox = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);
  const [tz, setTz] = useState(t("현지"));
  useEffect(() => setTz(tzLabel() || t("현지")), []);

  const rows = useMemo(() => {
    const base = baseDate ? new Date(baseDate) : new Date();
    const out: Row[] = new Array(lines.length);
    let last: number | null = null;
    for (let i = 0; i < lines.length; i++) {
      const l = lines[i];
      const m = TS_RE.exec(l.text);
      let ts: number | null;
      let carried = false;
      if (m) ts = last = fromUtcHms(base, m[1]).getTime();
      else if (l.at) ts = last = l.at;
      else {
        ts = last;
        carried = true;
      }
      const body = m ? m[2] : l.text;
      const plain = stripAnsi(body);
      const lv = levelOf(plain);
      out[i] = {
        n: i + 1,
        ts,
        carried,
        body,
        plain,
        level: lv === "info" && l.stream === "stderr" && /\bwarn/i.test(plain) ? "warn" : lv,
        service: l.service,
        replica: l.replica,
        stream: l.stream,
      };
    }
    return out;
  }, [lines, baseDate]);
  const counts = useMemo(() => {
    if (!tools) return { error: 0, warn: 0 };
    let error = 0;
    let warn = 0;
    for (const r of rows) {
      if (r.level === "error") error++;
      else if (r.level === "warn") warn++;
    }
    return { error, warn };
  }, [rows, tools]);
  const q = filter.trim();
  const pred = useMemo(() => makeFilter(filter), [filter]);
  const terms = useMemo(() => highlightTerms(filter), [filter]);
  const shown = useMemo(
    () =>
      q || level !== "all"
        ? rows.filter((r) => levelOk(r.level, level) && (!q || pred(`${r.service ?? ""} ${r.replica ?? ""} ${r.plain}`)))
        : rows,
    [rows, pred, q, level],
  );
  const filtered = !!q || level !== "all";

  /* ── Virtual window ── */
  const heights = useRef(new Map<string, number>());
  const [hv, setHv] = useState(0);
  const [vp, setVp] = useState({ top: 0, h: 600, w: 800 });
  const fixedW = (time ? TIME_W : BAR_W) + (showService ? 140 : 0) + (showReplica ? 110 : 0) + 24;
  const offsets = useMemo(() => {
    const per = Math.max(10, Math.floor((vp.w - fixedW) / CHAR_W));
    const est = (r: Row) => {
      if (!wrap) return LINE_H + ROW_PAD;
      let n = 0;
      for (const part of r.plain.split("\n")) n += Math.max(1, Math.ceil(part.length / per));
      return n * LINE_H + ROW_PAD;
    };
    const o = new Float64Array(shown.length + 1);
    const hs = heights.current;
    for (let i = 0; i < shown.length; i++) o[i + 1] = o[i] + (hs.get(heightKey(shown[i])) ?? est(shown[i]));
    return o;
    // hv re-runs this after new measurements land in the cache.
  }, [shown, hv, wrap, vp.w, fixedW]);
  const total = offsets[shown.length];
  const viewTop = stick ? Math.max(0, total - vp.h) : vp.top;
  const first = shown.length ? rowAt(offsets, shown.length, viewTop - OVERSCAN) : 0;
  const last = shown.length ? Math.min(shown.length, rowAt(offsets, shown.length, viewTop + vp.h + OVERSCAN) + 1) : 0;

  // Measure rendered rows, cache their heights and keep the view steady (or pinned to the bottom).
  useLayoutEffect(() => {
    const box = rowsBox.current;
    const el = scroller.current;
    if (!el) return;
    let changed = false;
    let delta = 0;
    if (box) {
      const anchor = el.scrollTop;
      for (const child of Array.from(box.children) as HTMLElement[]) {
        const i = Number(child.dataset.i);
        const r = shown[i];
        if (!r) continue;
        const h = child.offsetHeight;
        const before = offsets[i + 1] - offsets[i];
        if (h && Math.abs(h - before) > 0.5) {
          heights.current.set(heightKey(r), h);
          changed = true;
          if (offsets[i + 1] <= anchor) delta += h - before;
        }
      }
    }
    if (stick) el.scrollTop = el.scrollHeight;
    else if (delta) el.scrollTop += delta;
    if (changed) setHv((v) => v + 1);
  });

  useEffect(() => {
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      setVp((v) => {
        if (v.h === el.clientHeight && v.w === el.clientWidth) return v;
        if (v.w !== el.clientWidth) heights.current.clear();
        return { top: el.scrollTop, h: el.clientHeight, w: el.clientWidth };
      });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  useEffect(() => {
    heights.current.clear();
    setHv((v) => v + 1);
  }, [wrap, cols]);

  const lastScroll = useRef({ top: 0, height: 0 });
  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
    const prev = lastScroll.current;
    const up = el.scrollTop < prev.top - 2 && el.scrollHeight >= prev.height;
    lastScroll.current = { top: el.scrollTop, height: el.scrollHeight };
    if (dist < 32) setStick(true);
    else if (up) setStick(false);
    setVp((v) => (v.top === el.scrollTop ? v : { ...v, top: el.scrollTop }));
  };

  // Lines that arrived while you were reading further up.
  const pausedAt = useRef<number | null>(null);
  if (stick) pausedAt.current = null;
  else if (pausedAt.current === null) pausedAt.current = shown.length;
  const unseen = pausedAt.current === null ? 0 : Math.max(0, shown.length - pausedAt.current);

  const asText = () =>
    shown
      .map((r) => `${time && r.ts !== null ? fmtLocal(new Date(r.ts)) + " " : ""}${r.service ? `[${r.service}] ` : ""}${r.replica ? `${r.replica} ` : ""}${r.plain}`)
      .join("\n");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(asText());
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    } catch {
      /* clipboard denied */
    }
  };
  const download = () => {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([asText()], { type: "text/plain" }));
    a.download = downloadName;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 0);
  };

  const levelOpts: [LevelFilter, string, number | null][] = [
    ["all", t("전체"), null],
    ["warn", t("경고 이상"), counts.warn + counts.error],
    ["error", t("오류만"), counts.error],
  ];

  return (
    <div className={cx("relative flex min-h-0 flex-col bg-background", className)}>
      {tools && (
        <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-b-neutral bg-secondaryBg px-6 py-1.5 font-sans">
          <div role="radiogroup" aria-label={t("로그 레벨")} className="flex items-center gap-0.5">
            {levelOpts.map(([v, label, n]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={level === v}
                onClick={() => setLevel(v)}
                className={cx(TOOL_BTN, level === v && "bg-gray-100 text-foreground")}
              >
                {label}
                {n !== null && n > 0 && (
                  <span className={cx("rounded-full px-1.5 tabular-nums", v === "error" ? "bg-red-100 text-red-600" : "bg-yellow-100 text-yellow-600")}>
                    {n.toLocaleString()}
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className="ml-auto flex items-center gap-0.5">
            <button type="button" aria-pressed={wrapPref} onClick={() => setWrap(!wrapPref)} className={TOOL_BTN} title={t("긴 줄 바꿈")}>
              <WrapText aria-hidden /> <span className="hidden md:inline">{t("줄 바꿈")}</span>
            </button>
            {showTime && (
              <button type="button" aria-pressed={timePref} onClick={() => setTimePref(!timePref)} className={TOOL_BTN} title={t("시간 표시")}>
                <Clock aria-hidden /> <span className="hidden md:inline">{t("시간")}</span>
              </button>
            )}
            <button
              type="button"
              onClick={copy}
              disabled={!shown.length}
              className={cx(TOOL_BTN, "disabled:cursor-not-allowed disabled:opacity-50")}
              aria-label={filtered ? t("필터된 로그 복사") : t("로그 복사")}
              title={copied ? t("복사됨") : filtered ? t("필터된 로그 복사") : t("로그 복사")}
            >
              {copied ? <Check aria-hidden className="text-success" /> : <Copy aria-hidden />}
              <span className="hidden md:inline" aria-live="polite">
                {copied ? t("복사됨") : t("복사")}
              </span>
            </button>
            <button
              type="button"
              onClick={download}
              disabled={!shown.length}
              className={cx(TOOL_BTN, "disabled:cursor-not-allowed disabled:opacity-50")}
              aria-label={t("로그 내려받기")}
              title={t("로그 내려받기")}
            >
              <Download aria-hidden />
            </button>
          </div>
        </div>
      )}
      {/* role="table" groups the header row and the log rows, so the row/cell roles have the parent ARIA requires. */}
      <div role="table" aria-label={t("로그")} className="flex min-h-0 flex-1 flex-col">
      <div role="rowgroup" className="relative w-full shrink-0 bg-secondaryBg after:absolute after:inset-x-0 after:bottom-0 after:border-b after:border-b-neutral after:content-['']">
        <div role="row" className="grid w-full select-none text-left text-xs font-medium text-neutral-secondary" style={{ gridTemplateColumns: gridCols(cols) }}>
          {time ? (
            <div role="columnheader" className="relative flex items-center whitespace-nowrap bg-secondaryBg px-2 py-1.5 pl-6">
              {t("날짜")} <span className="ml-1 text-gray-500">({tz})</span>
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
                <span className={cx("size-1.5 rounded-full", STATE[state].cls)} aria-hidden />
                <span className="hidden sm:inline" role="status">
                  {STATE[state].text}
                </span>
                {(state === "error" || state === "done") && onReconnect && (
                  <button type="button" onClick={onReconnect} className="ml-1 flex cursor-pointer items-center gap-1 rounded px-1 text-gray-600 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-pink-500">
                    <RotateCw className="size-3" aria-hidden /> {t("다시 연결")}
                  </button>
                )}
                <span className="ml-2 tabular-nums text-gray-500">
                  {filtered ? `${shown.length.toLocaleString()} / ` : ""}
                  {t("{count}줄", { count: rows.length.toLocaleString() })}
                </span>
              </span>
            )}
          </div>
        </div>
      </div>
      <div
        ref={scroller}
        onScroll={onScroll}
        role="rowgroup"
        aria-label={t("로그")}
        tabIndex={0}
        data-noscroll
        className={cx(
          "min-h-0 flex-1 overflow-y-auto font-mono text-xs [overflow-anchor:none] focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-pink-500/40",
          wrap ? "overflow-x-hidden" : "overflow-x-auto",
        )}
      >
        {shown.length === 0 ? (
          <LogEmpty
            title={rows.length && filtered ? t("이 필터에 맞는 로그가 없습니다") : state === "connecting" ? t("로그를 불러오는 중…") : undefined}
            sub={rows.length && filtered ? t("검색어를 줄이거나 지워 보세요.") : state === "connecting" ? t("로그를 가져오는 동안 잠시 기다려 주세요.") : emptyText}
          />
        ) : (
          <div style={{ paddingTop: offsets[first], paddingBottom: total - offsets[last] }}>
            <div ref={rowsBox}>
              {shown.slice(first, last).map((r, k) => (
                <RowView key={r.n} r={r} i={first + k} cols={cols} wrap={wrap} terms={terms} />
              ))}
            </div>
          </div>
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
            scroller.current?.scrollTo({ top: 0 });
          }}
          className="flex size-9 cursor-pointer items-center justify-center rounded-md border border-gray-200 bg-secondaryBg text-gray-600 hover:border-gray-300 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-pink-500"
        >
          <ArrowUp className="size-4" aria-hidden />
        </button>
        <button
          type="button"
          aria-label={unseen ? t("맨 아래로 (새 로그 {count}줄)", { count: unseen }) : t("맨 아래로")}
          title={stick ? t("최신 로그를 따라가는 중") : t("맨 아래로 이동해 최신 로그 따라가기")}
          aria-pressed={stick}
          onClick={() => setStick(true)}
          className={cx(
            "relative flex size-9 cursor-pointer items-center justify-center rounded-md border bg-secondaryBg hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-pink-500",
            stick ? "border-gray-200 text-gray-600" : "border-pink-500 text-pink-600",
          )}
        >
          <ArrowDown className="size-4" aria-hidden />
          {unseen > 0 && (
            <span className="absolute -left-2 -top-2 min-w-5 rounded-full bg-pink-500 px-1 text-center font-sans text-[10px] font-semibold leading-5 text-white tabular-nums">
              {unseen > 999 ? "999+" : unseen}
            </span>
          )}
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
