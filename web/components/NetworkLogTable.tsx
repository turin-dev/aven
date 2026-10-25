"use client";

import { memo, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, Info } from "lucide-react";
import type { DNSLogLine, NetworkCaptureStatus, NetworkFlowLogLine } from "@/lib/api";
import type { StreamState } from "@/lib/useLogStream";
import { fmtBytes } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { LogEmpty, type LogLine } from "./LogTable";
import { cx } from "./ui";

export type NetworkLogRecord = DNSLogLine | NetworkFlowLogLine;

const p2 = (n: number) => String(n).padStart(2, "0");
function fmtTime(t: string) {
  const d = new Date(t);
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}

function fmtMs(ms: number) {
  if (ms >= 1000) return `${(ms / 1000).toFixed(ms >= 10000 ? 0 : 1)}s`;
  return ms >= 10 ? `${Math.round(ms)}ms` : `${ms.toFixed(1)}ms`;
}

const hostPort = (a: string, p: number) => (a.includes(":") ? `[${a}]:${p}` : `${a}:${p}`);

export const PEER_KIND_LABEL: Record<string, string> = {
  service: t("서비스"),
  internet: t("인터넷"),
  edge_proxy: t("엣지 프록시"),
  host: t("호스트"),
  private: t("사설망"),
  unknown: t("알 수 없음"),
};

const FLOW_STATUS_LABEL: Record<string, string> = { ok: t("정상"), refused: t("거부됨"), timeout: t("시간 초과"), reset: t("끊김") };

function failed(r: NetworkLogRecord) {
  return r.kind === "dns" ? r.status === "failed" : r.status === "refused" || r.status === "timeout";
}

/** Plain-text form of a record for the histogram and log download. */
export function networkLogLine(r: NetworkLogRecord, serviceName?: string): LogLine {
  const service = serviceName ?? r.service;
  const stream = failed(r) ? "stderr" : "stdout";
  if (r.kind === "dns") {
    const answers = r.answers.length ? r.answers.join(", ") : "-";
    return { text: `${r.zone} ${r.qtype} ${r.rcode} ${r.qname} ${answers}`, at: Date.parse(r.t), service, replica: r.replica, stream };
  }
  return {
    text: `${r.direction} ${r.protocol} ${hostPort(r.src_addr, r.src_port)} -> ${hostPort(r.dst_addr, r.dst_port)} peer=${r.peer} sent=${r.bytes_sent} received=${r.bytes_received}${r.latency_ms ? ` latency=${r.latency_ms}ms` : ""} ${r.status}${r.flow_state === "active" ? " (active)" : ""}`,
    at: Date.parse(r.t),
    service,
    replica: r.replica,
    stream,
  };
}

function Pill({ bad, children }: { bad: boolean; children: ReactNode }) {
  return (
    <span className={cx("inline-flex h-[18px] shrink-0 items-center justify-center rounded px-1.5 text-xs font-medium", bad ? "bg-red-100 text-red-500" : "bg-green-100 text-green-600")}>
      {children}
    </span>
  );
}
const DNSRow = memo(function DNSRow({ r, service, showService }: { r: DNSLogLine; service: string; showService: boolean }) {
  return (
    <div role="row" className="flex min-h-[26px] items-center gap-3 px-6 font-mono text-xs hover:bg-gray-50" title={`${t("리졸버 {resolver}", { resolver: r.resolver })} · ${fmtMs(r.duration_ms)}${r.replica ? ` · ${r.replica}` : ""}`}>
      <span className="w-[140px] shrink-0 tabular-nums text-neutral-secondary">{fmtTime(r.t)}</span>
      {showService && <span className="hidden w-[110px] shrink-0 truncate text-gray-600 md:block">{service}</span>}
      <span className={cx("w-[60px] shrink-0", r.zone === "internal" ? "text-blue-600" : "text-gray-500")}>{r.zone === "internal" ? t("내부") : t("외부")}</span>
      <span className="w-[46px] shrink-0 font-semibold text-foreground/90">{r.qtype}</span>
      <span className="w-[86px] shrink-0">
        <Pill bad={r.status === "failed"}>{r.rcode}</Pill>
      </span>
      <span className="min-w-0 flex-1 truncate text-foreground">{r.qname}</span>
      <span className="hidden w-[260px] shrink-0 truncate text-gray-500 lg:block">
        {r.cname_chain?.length ? <span className="text-gray-400">{r.cname_chain.join(" → ")} → </span> : null}
        {r.answers.length ? r.answers.join(", ") : "—"}
      </span>
    </div>
  );
});
const FlowRow = memo(function FlowRow({ r, service, showService }: { r: NetworkFlowLogLine; service: string; showService: boolean }) {
  const bad = r.status !== "ok";
  return (
    <div
      role="row"
      className="flex min-h-[26px] items-center gap-3 px-6 font-mono text-xs hover:bg-gray-50"
      title={`${PEER_KIND_LABEL[r.peer_kind] ?? r.peer_kind} · ${t("패킷 {count}", { count: r.packets })} · ${t("지속 {duration}", { duration: fmtMs(r.duration_ms) })}${r.flow_state === "active" ? t(" · 아직 열린 연결") : ""}${r.replica ? ` · ${r.replica}` : ""}`}
    >
      <span className="w-[140px] shrink-0 tabular-nums text-neutral-secondary">{fmtTime(r.t)}</span>
      {showService && <span className="hidden w-[110px] shrink-0 truncate text-gray-600 md:block">{service}</span>}
      <span className={cx("w-[52px] shrink-0", r.direction === "egress" ? "text-blue-600" : "text-yellow-600")}>{r.direction === "egress" ? t("송신") : t("수신")}</span>
      <span className="w-[32px] shrink-0 uppercase text-gray-500">{r.protocol}</span>
      <span className="hidden w-[170px] shrink-0 truncate text-gray-500 xl:block">{hostPort(r.src_addr, r.src_port)}</span>
      <span className="hidden w-[170px] shrink-0 truncate text-gray-500 lg:block">{hostPort(r.dst_addr, r.dst_port)}</span>
      <span className="min-w-0 flex-1 truncate text-foreground">
        {r.peer_kind === "service" ? r.peer : (PEER_KIND_LABEL[r.peer_kind] ?? r.peer)}
        <span className="text-gray-400">:{r.direction === "egress" ? r.dst_port : r.src_port}</span>
      </span>
      <span className="w-[120px] shrink-0 text-right tabular-nums text-gray-600">
        ↑{fmtBytes(r.bytes_sent)} ↓{fmtBytes(r.bytes_received)}
      </span>
      <span className="hidden w-[56px] shrink-0 text-right tabular-nums text-gray-500 sm:block">{r.latency_ms ? fmtMs(r.latency_ms) : "—"}</span>
      <span className="w-[72px] shrink-0 text-right">
        <Pill bad={bad}>{FLOW_STATUS_LABEL[r.status] ?? r.status}</Pill>
      </span>
    </div>
  );
});

export function NetworkLogTable({
  kind,
  records,
  serviceNames,
  state,
  capture,
  emptyText,
  showService = true,
  className,
}: {
  kind: "dns" | "network";
  records: NetworkLogRecord[];
  serviceNames?: Map<string, string>;
  state?: StreamState;
  capture?: NetworkCaptureStatus | null;
  emptyText: string;
  showService?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);
  const shown = records.filter((r) => r.kind === kind);
  const failures = shown.filter(failed).length;

  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick) el.scrollTop = el.scrollHeight;
  }, [shown.length, stick]);

  const head = "relative flex shrink-0 items-center gap-3 bg-secondaryBg px-6 py-1.5 text-xs font-medium text-neutral-secondary after:absolute after:inset-x-0 after:bottom-0 after:border-b after:border-b-neutral after:content-['']";
  const offline = capture && capture.inactive_nodes > 0;

  return (
    <div className={cx("relative flex min-h-0 flex-col bg-background", className)}>
      {offline ? (
        <div role="status" className="mx-6 mb-2 flex items-start gap-2 rounded-md border border-yellow-200 bg-yellow-50 px-3 py-2 text-xs text-yellow-600">
          <Info className="mt-px size-3.5 shrink-0" />
          <span>
            {tr("노드 {count}곳에서 네트워크 로그를 수집하지 못하고 있습니다{reason}. aven-server와 aven-agent는 패킷 캡처를 위해 root 또는 CAP_NET_RAW 권한이 필요합니다.", {
              count: capture.inactive_nodes,
              reason: capture.reason ? <span className="font-mono"> ({capture.reason})</span> : null,
            })}
          </span>
        </div>
      ) : null}
      <div role="row" className={head}>
        <span className="w-[140px] shrink-0">{t("시간")}</span>
        {showService && <span className="hidden w-[110px] shrink-0 md:block">{t("서비스")}</span>}
        {kind === "dns" ? (
          <>
            <span className="w-[60px] shrink-0">{t("영역")}</span>
            <span className="w-[46px] shrink-0">{t("타입")}</span>
            <span className="w-[86px] shrink-0">{t("응답 코드")}</span>
            <span className="flex-1">{t("이름")}</span>
            <span className="hidden w-[260px] shrink-0 lg:block">{t("응답")}</span>
          </>
        ) : (
          <>
            <span className="w-[52px] shrink-0">{t("방향")}</span>
            <span className="w-[32px] shrink-0">L4</span>
            <span className="hidden w-[170px] shrink-0 xl:block">{t("출발")}</span>
            <span className="hidden w-[170px] shrink-0 lg:block">{t("도착")}</span>
            <span className="flex-1">{t("상대")}</span>
            <span className="w-[120px] shrink-0 text-right">{t("트래픽")}</span>
            <span className="hidden w-[56px] shrink-0 text-right sm:block">{t("지연")}</span>
            <span className="w-[72px] shrink-0 text-right">{t("상태")}</span>
          </>
        )}
        {state && (
          <span className="ml-2 flex shrink-0 items-center gap-1.5 text-xs">
            <span className={cx("size-1.5 rounded-full", state === "open" ? "animate-soft-pulse bg-green-600" : state === "error" ? "bg-red-600" : "bg-gray-400")} />
            <span className="tabular-nums text-gray-400">
              {t("{count}건", { count: shown.length })}{failures > 0 && <span className="text-danger"> · {t("실패 {count}", { count: failures })}</span>}
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
            title={state === "connecting" ? t("로그를 불러오는 중…") : undefined}
            sub={state === "connecting" ? t("로그를 가져오는 동안 잠시 기다려 주세요.") : emptyText}
          />
        ) : (
          shown.map((r) => {
            const service = serviceNames?.get(r.service_id) ?? r.service ?? r.service_id;
            return r.kind === "dns" ? (
              <DNSRow key={r.id} r={r} service={service} showService={showService} />
            ) : (
              <FlowRow key={r.id} r={r} service={service} showService={showService} />
            );
          })
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

/** Filter syntax hint shown under the search box for DNS / network logs. */
export const NETWORK_FILTER_PLACEHOLDER: Record<"dns" | "network", string> = {
  dns: t("DNS 검색 예: @rcode:NXDOMAIN @zone:internal @domain:example.com"),
  network: t("Flow 검색 예: @peer:postgres @port:5432 @direction:egress @status:refused"),
};
