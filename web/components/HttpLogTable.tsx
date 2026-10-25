"use client";

import { memo, useLayoutEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { ArrowDown, ArrowUp, Waypoints } from "lucide-react";
import type { HttpLogLine } from "@/lib/api";
import type { StreamState } from "@/lib/useLogStream";
import { LogEmpty, makeFilter } from "./LogTable";
import { fmtBytes } from "@/lib/format";
import { useCtxHref } from "@/lib/nav";
import { cx } from "./ui";
import { t } from "@/lib/i18n";

const p2 = (n: number) => String(n).padStart(2, "0");
function fmt(time: string) {
  const d = new Date(time);
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}

export function statusClass(s: number) {
  if (s >= 500) return "bg-red-100 text-red-500";
  if (s >= 400) return "bg-yellow-100 text-yellow-600";
  if (s >= 300) return "bg-blue-100 text-blue-600";
  return "bg-green-100 text-green-600";
}


function fmtMs(ms: number) {
  if (ms >= 1000) return `${(ms / 1000).toFixed(ms >= 10000 ? 0 : 1)}s`;
  return ms >= 10 ? `${Math.round(ms)}ms` : `${ms.toFixed(1)}ms`;
}

const Row = memo(function Row({ l }: { l: HttpLogLine }) {
  const href = useCtxHref();
  return (
    <div
      className="flex min-h-[26px] items-center gap-3 px-6 font-mono text-xs hover:bg-gray-50"
      title={`${l.client_ip} · ${l.user_agent}`}
    >
      <span className="w-[140px] shrink-0 tabular-nums text-neutral-secondary">{fmt(l.t)}</span>
      <span className="w-[52px] shrink-0 font-semibold uppercase text-foreground/90">{l.method}</span>
      <span className={cx("inline-flex h-[18px] w-[38px] shrink-0 items-center justify-center rounded text-xs font-medium tabular-nums", statusClass(l.status))}>
        {l.status}
      </span>
      <span className="min-w-0 flex-1 truncate text-foreground">
        <span className="hidden text-gray-400 lg:inline">{l.host}</span>
        {l.path}
      </span>
      <span className={cx("w-[64px] shrink-0 text-right tabular-nums", l.duration_ms >= 500 ? "text-yellow-600" : "text-gray-600")}>
        {fmtMs(l.duration_ms)}
      </span>
      <span className="hidden w-[68px] shrink-0 text-right tabular-nums text-gray-400 sm:block">{fmtBytes(l.bytes)}</span>
      <span className="hidden w-[150px] shrink-0 truncate text-gray-400 xl:block">{l.client_ip}</span>
      <span className="flex w-[20px] shrink-0 justify-end">
        {l.trace_id && (
          <Link href={href("/traces/", { trace: l.trace_id })} aria-label={t("trace 보기")} title={t("trace 보기")} className="text-gray-400 hover:text-pink-500">
            <Waypoints className="size-3.5" />
          </Link>
        )}
      </span>
    </div>
  );
});

export type NetworkLogKind = "http" | "dns" | "network";

export function NetworkLogKinds({ value, onChange }: { value: NetworkLogKind; onChange: (v: NetworkLogKind) => void }) {
  const kinds: [NetworkLogKind, string][] = [
    ["http", "HTTP"],
    ["dns", "DNS"],
    ["network", "Network Flow"],
  ];
  return (
    <div role="radiogroup" aria-label={t("네트워크 로그 종류")} dir="ltr" className="flex max-w-max rounded-md text-gray-600">
      {kinds.map(([id, label], i) => (
        <button
          key={id}
          type="button"
          role="radio"
          aria-checked={value === id}
          data-state={value === id ? "on" : "off"}
          onClick={() => onChange(id)}
          className={cx(
            "relative flex h-[34px] shrink-0 cursor-pointer items-center justify-center whitespace-nowrap border border-gray-300 bg-secondaryBg px-3 py-2 text-sm focus:outline-none focus-visible:z-[100] focus-visible:ring-2 focus-visible:ring-pink-500",
            i === 0 ? "rounded-l-md" : "-ml-px",
            i === kinds.length - 1 && "rounded-r-md",
            value === id ? "z-50 text-pink-500" : "text-gray-500 hover:text-foreground",
          )}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

export function HttpLogTable({
  lines,
  state,
  filter,
  emptyText,
  className,
}: {
  lines: HttpLogLine[];
  state?: StreamState;
  filter: string;
  emptyText: string;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);
  const pred = useMemo(() => makeFilter(filter), [filter]);
  const shown = useMemo(
    () => (filter.trim() ? lines.filter((l) => pred(`${l.method} ${l.status} ${l.host}${l.path} ${l.client_ip} ${l.user_agent}`)) : lines),
    [lines, pred, filter],
  );
  const errors = useMemo(() => lines.filter((l) => l.status >= 500).length, [lines]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick) el.scrollTop = el.scrollHeight;
  }, [shown, stick]);

  return (
    <div className={cx("relative flex min-h-0 flex-col bg-background", className)}>
      <div role="rowgroup" className="relative flex shrink-0 items-center gap-3 bg-secondaryBg px-6 py-1.5 text-xs font-medium text-neutral-secondary after:absolute after:inset-x-0 after:bottom-0 after:border-b after:border-b-neutral after:content-['']">
        <span className="w-[140px] shrink-0">{t("시간")}</span>
        <span className="w-[52px] shrink-0">{t("메서드")}</span>
        <span className="w-[38px] shrink-0">{t("상태")}</span>
        <span className="flex-1">{t("경로")}</span>
        <span className="w-[64px] shrink-0 text-right">{t("소요")}</span>
        <span className="hidden w-[68px] shrink-0 text-right sm:block">{t("크기")}</span>
        <span className="hidden w-[150px] shrink-0 xl:block">{t("클라이언트")}</span>
        <span className="w-[20px] shrink-0" aria-hidden="true" />
        {state && (
          <span className="ml-2 flex shrink-0 items-center gap-1.5 text-xs">
            <span className={cx("size-1.5 rounded-full", state === "open" ? "animate-soft-pulse bg-green-600" : state === "error" ? "bg-red-600" : "bg-gray-400")} />
            <span className="tabular-nums text-gray-400">
              {t("{count}건", { count: lines.length })}{errors > 0 && <span className="text-danger"> · 5xx {errors}</span>}
            </span>
          </span>
        )}
      </div>
      <div
        ref={ref}
        role="log"
        data-noscroll
        onScroll={() => {
          const el = ref.current;
          if (el) setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 32);
        }}
        className="min-h-0 flex-1 overflow-y-auto"
      >
        {shown.length === 0 ? (
          <LogEmpty
            title={lines.length && filter.trim() ? t("이 필터에 맞는 요청이 없습니다") : state === "connecting" ? t("로그를 불러오는 중…") : undefined}
            sub={lines.length && filter.trim() ? undefined : state === "connecting" ? t("로그를 가져오는 동안 잠시 기다려 주세요.") : emptyText}
          />
        ) : (
          shown.map((l, i) => <Row key={i} l={l} />)
        )}
      </div>
      <div className="absolute bottom-4 right-4 flex flex-col gap-2">
        <button
          type="button"
          aria-label={t("맨 위로")}
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
