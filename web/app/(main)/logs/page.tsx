"use client";

import { Suspense, useEffect, useMemo, useState, type ReactNode } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Check, ChevronDown, Columns3, Download, Layers } from "lucide-react";
import { sse, type EnvLogLine, type NetworkCaptureStatus } from "@/lib/api";
import { useCanvas } from "@/lib/queries";
import { useCtxHref } from "@/lib/nav";
import { useWorkspace } from "@/lib/workspace";
import { useLogStream, useParsedLines } from "@/lib/useLogStream";
import { LogFilter, LogHistogram, LogTable, LogToolbar, downloadLogs, type LogLine } from "@/components/LogTable";
import { NETWORK_FILTER_PLACEHOLDER, NetworkLogTable, networkLogLine, type NetworkLogRecord } from "@/components/NetworkLogTable";
import { BrandIcon, iconKey } from "@/components/ServiceIcon";
import { t } from "@/lib/i18n";
import { Button, ErrorBox, Menu, Segmented, Spinner, cx } from "@/components/ui";

type Cols = { time: boolean; service: boolean; replica: boolean };
type Kind = "deploy" | "dns" | "network";
const KINDS: [Kind, string][] = [
  ["deploy", t("배포")],
  ["dns", "DNS"],
  ["network", "Network Flow"],
];

/** Square outline icon button of the log toolbar. */
const SQUARE = "w-[34px] px-0 text-gray-500";

/** Checkbox row inside a toolbar menu (service picker, column picker). */
function CheckItem({ checked, onToggle, children }: { checked: boolean; onToggle: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      role="menuitemcheckbox"
      aria-checked={checked}
      onClick={onToggle}
      className="mb-0.5 flex h-9 w-full cursor-pointer items-center gap-2.5 rounded px-3 text-left text-sm text-neutral-secondary last:mb-0 hover:bg-neutral-surface-hover hover:text-foreground focus:bg-neutral-surface-hover focus:outline-none"
    >
      <span className={cx("flex size-4 shrink-0 items-center justify-center rounded border", checked ? "border-pink-500 bg-pink-500" : "border-gray-300")}>
        {checked && <Check className="size-3 text-white" />}
      </span>
      {children}
    </button>
  );
}

function LogsInner() {
  const ws = useWorkspace();
  const canvas = useCanvas(ws.env?.id);
  const params = useSearchParams();
  const router = useRouter();
  const href = useCtxHref();
  const list = useMemo(() => [...(canvas.data?.services ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [canvas.data]);
  const selected = useMemo(() => (params.get("services") ?? "").split(",").filter(Boolean), [params]);
  const typeParam = params.get("type");
  const kind: Kind = typeParam === "dns" || typeParam === "network" ? typeParam : "deploy";
  const [q, setQ] = useState(params.get("q") ?? "");
  const [query, setQuery] = useState(q);
  const [cols, setCols] = useState<Cols>({ time: true, service: true, replica: true });

  // The filter runs on the server; debounce reconnects while typing.
  useEffect(() => {
    const t = setTimeout(() => setQuery(q), 400);
    return () => clearTimeout(t);
  }, [q]);

  useEffect(() => {
    const cur = params.get("q") ?? "";
    if (cur === query) return;
    router.replace(href("/logs/", { services: selected.join(",") || null, q: query || null, type: kind === "deploy" ? null : kind }), { scroll: false });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

  const setServices = (ids: string[]) =>
    router.replace(href("/logs/", { services: ids.join(",") || null, q: query || null, type: kind === "deploy" ? null : kind }), { scroll: false });
  const setKind = (next: Kind) =>
    router.replace(href("/logs/", { services: selected.join(",") || null, q: query || null, type: next === "deploy" ? null : next }), { scroll: false });

  const [capture, setCapture] = useState<NetworkCaptureStatus | null>(null);
  const on = useMemo(
    () => ({
      capture: (data: string) => {
        try {
          setCapture(JSON.parse(data) as NetworkCaptureStatus);
        } catch {
          setCapture(null);
        }
      },
    }),
    [],
  );
  const url = ws.env ? (kind === "deploy" ? sse.envLogs(ws.env.id, selected, query) : sse.envNetworkLogs(ws.env.id, selected, kind, query)) : null;
  const stream = useLogStream(url, { replay: true, on });
  const parsed = useParsedLines<EnvLogLine & Partial<NetworkLogRecord>>(stream.lines);
  const names = useMemo(() => new Map(list.map((s) => [s.id, s.name])), [list]);
  const records = useMemo(() => (kind === "deploy" ? [] : (parsed as unknown as NetworkLogRecord[]).filter((r) => r.kind === kind)), [parsed, kind]);
  const lines: LogLine[] = useMemo(
    () =>
      kind === "deploy"
        ? parsed.map((l) => ({ text: l.msg, at: Date.parse(l.t), service: names.get(l.service_id) ?? l.service_id, replica: l.replica, stream: l.stream }))
        : records.map((r) => networkLogLine(r, names.get(r.service_id))),
    [kind, parsed, records, names],
  );

  const label = selected.length === 0 ? t("모든 서비스") : selected.length === 1 ? (names.get(selected[0]) ?? t("서비스 1개")) : t("서비스 {count}개", { count: selected.length });

  return (
    <div className="flex h-full flex-col bg-background">
      <LogToolbar>
        <div className="shrink-0">
          <Segmented label={t("로그 종류")} value={kind} onChange={setKind} options={KINDS.map(([value, label]) => ({ value, label }))} />
        </div>
        <Menu
          align="start"
          label={t("서비스 선택")}
          trigger={(p) => (
            <Button variant="outline" {...p} className="pr-2.5 [&_svg]:text-gray-600">
              <Layers />
              {label}
              <ChevronDown />
            </Button>
          )}
        >
          {() => (
            <div className="w-[260px]">
              <CheckItem checked={selected.length === 0} onToggle={() => setServices([])}>
                {t("모든 서비스")}
              </CheckItem>
              <div className="my-1 h-px w-full bg-gray-100" />
              {list.map((s) => {
                const on = selected.includes(s.id);
                return (
                  <CheckItem key={s.id} checked={on} onToggle={() => setServices(on ? selected.filter((x) => x !== s.id) : [...selected, s.id])}>
                    <BrandIcon name={iconKey(s)} className="size-3.5" />
                    <span className="min-w-0 flex-1 truncate">{s.name}</span>
                  </CheckItem>
                );
              })}
            </div>
          )}
        </Menu>
        <LogFilter value={q} onChange={setQ} onApply={() => setQuery(q)} className="flex-1" placeholder={kind === "deploy" ? undefined : NETWORK_FILTER_PLACEHOLDER[kind]} />
        <Button
          variant="outline"
          aria-label={t("로그 다운로드")}
          title={t("로그 다운로드")}
          onClick={() => downloadLogs(lines, kind === "deploy" ? "aven-logs.txt" : `aven-${kind}-logs.txt`)}
          className={SQUARE}
        >
          <Download />
        </Button>
        <Menu
          label={t("열 표시")}
          trigger={(p) => (
            <Button variant="outline" {...p} aria-label={t("열 표시")} title={t("열 표시")} className={SQUARE}>
              <Columns3 />
            </Button>
          )}
        >
          {() => (
            <div className="w-[180px]">
              {(
                [
                  ["time", t("시간")],
                  ["service", t("서비스")],
                  ["replica", t("레플리카|one")],
                ] as const
              ).map(([k, l]) => (
                <CheckItem key={k} checked={cols[k]} onToggle={() => setCols((c) => ({ ...c, [k]: !c[k] }))}>
                  {l}
                </CheckItem>
              ))}
            </div>
          )}
        </Menu>
      </LogToolbar>
      <LogHistogram lines={lines} />
      {ws.error || canvas.error ? (
        <div className="px-6">
          <ErrorBox error={ws.error ?? canvas.error} />
        </div>
      ) : ws.isPending || canvas.isPending ? (
        <div className="flex flex-1 items-center justify-center">
          <Spinner />
        </div>
      ) : kind !== "deploy" ? (
        <NetworkLogTable
          className="min-h-0 flex-1"
          kind={kind}
          records={records}
          serviceNames={names}
          state={stream.state}
          capture={capture}
          showService={cols.service}
          emptyText={
            query
              ? t("필터와 일치하는 기록이 없습니다. 검색어를 줄이거나 지워 보세요.")
              : kind === "dns"
                ? t("서비스 컨테이너의 DNS 조회가 여기에 모입니다. 같은 환경의 서비스 이름은 컨테이너 안에서 바로 풀려 기록되지 않고, 외부 이름과 찾지 못한 이름이 기록됩니다.")
                : t("서비스가 주고받는 TCP/UDP 연결(다른 서비스, 인터넷, 엣지 프록시)이 여기에 모입니다.")
          }
        />
      ) : (
        <LogTable
          className="min-h-0 flex-1"
          lines={lines}
          state={stream.state}
          onReconnect={stream.reconnect}
          showTime={cols.time}
          showService={cols.service}
          showReplica={cols.replica}
          emptyText={
            query
              ? t("필터와 일치하는 로그가 없습니다. 검색어를 줄이거나 지워 보세요.")
              : list.length
                ? t("실행 중인 서비스의 로그가 여기에 모입니다. 아직 출력된 로그가 없습니다.")
                : t("서비스가 없습니다. 아키텍처 화면에서 서비스를 만드세요.")
          }
        />
      )}
    </div>
  );
}

export default function LogsPage() {
  return (
    <Suspense fallback={<div className="h-full bg-background" />}>
      <LogsInner />
    </Suspense>
  );
}
