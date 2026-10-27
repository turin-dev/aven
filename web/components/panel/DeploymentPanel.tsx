"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Moon, RefreshCw, X } from "lucide-react";
import {
  isTerminal,
  sse,
  type CanvasService,
  type DeployLogLine,
  type Deployment,
  type DeploymentStatus,
  type Environment,
  type HttpLogLine,
} from "@/lib/api";
import { qk, useCaps, useDeployment } from "@/lib/queries";
import { fmtDuration, fmtRelative, fmtTime, shortSha } from "@/lib/format";
import { useLogStream, useParsedLines } from "@/lib/useLogStream";
import type { DepTab } from "@/lib/nav";
import type { ServiceActions } from "@/lib/useServiceActions";
import { StatusPill, TRIGGER_LABEL } from "../StatusBadge";
import { ServiceIcon } from "../ServiceIcon";
import { LogFilter, LogTable, LogToolbar, splitDeploymentLog, type LogLine } from "../LogTable";
import { HttpLogTable, NetworkLogKinds, type NetworkLogKind } from "../HttpLogTable";
import { NETWORK_FILTER_PLACEHOLDER } from "../NetworkLogTable";
import { NetworkDiagnostics } from "./NetworkDiagnostics";
import { ServiceNetworkLogs } from "./ServiceNetworkLogs";
import { ApiError } from "@/lib/api";
import { Button, CopyButton, ErrorBox, IconButton, Spinner, cx, useConfirm, useEscape } from "../ui";
import { DeploymentMenu, ReplicaChips, Stepper, depTitle, fmtClock, stepsFor, useTicker, viaText } from "./DeploymentsTab";
import { HeaderTabs, escCloses, tabId, tabPanelId } from "./deployments/HeaderTabs";
import { panelCls } from "./ServicePanel";
import { t, tr } from "@/lib/i18n";

const TABS: { id: DepTab; label: string }[] = [
  { id: "details", label: t("상세") },
  { id: "build", label: t("빌드 로그") },
  { id: "deploy", label: t("배포 로그") },
  { id: "http", label: t("네트워크 로그") },
];

function Info({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-sm leading-6 text-gray-500">{label}</dt>
      <dd className="flex min-w-0 items-center gap-1 text-sm leading-6 text-foreground">{children}</dd>
    </>
  );
}

const BUILDER: Record<string, string> = { dockerfile: "Dockerfile", railpack: "Railpack", image: t("이미지") };

function Details({ d, svc, env }: { d: Deployment; svc: CanvasService; env: Environment }) {
  const running = !isTerminal(d.status);
  return (
    <div className="h-full overflow-y-auto px-5 pb-12 pt-8 md:px-12" data-noscroll>
      {running && (
        <div className="mb-6 overflow-x-auto rounded-lg border border-gray-200 bg-gray-50 px-4 py-3.5">
          <Stepper steps={stepsFor(svc.instance, d)} status={d.status} />
        </div>
      )}
      {d.error && (
        <div className="mb-6 rounded-lg border border-red-200 bg-red-100 px-4 py-3">
          <div className="text-sm font-semibold text-danger">{d.status === "crashed" ? t("크래시 원인") : t("실패 원인")}</div>
          <div className="mt-1 whitespace-pre-wrap break-words font-mono text-sm text-red-700">{d.error}</div>
        </div>
      )}
      {d.status === "skipped" && (
        <div className="mb-6 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-sm text-gray-600">
          {env.kind === "preview"
            ? t("PR 변경 파일이 서비스 경로와 일치하지 않아 배포를 건너뛰고 base 환경 값을 사용합니다.")
            : t("변경 파일이 감시 경로({paths})와 일치하지 않아 배포를 건너뛰었습니다.", { paths: svc.instance.watch_paths.join(", ") || t("없음") })}
        </div>
      )}
      {d.build_reused_from && (
        <div className="mb-6 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-sm text-gray-600">
          {tr("빌드 건너뜀 — 배포 {deployment}의 이미지를 재사용했습니다. 새로 빌드하려면 배포 메뉴의 “최신 커밋 강제 재빌드”를 쓰세요.", {
            deployment: <span className="font-mono">{d.build_reused_from}</span>,
          })}
        </div>
      )}
      {d.replicas.length > 0 && (
        <div className="mb-6">
          <div className="mb-2 text-sm font-semibold text-gray-600">{t("레플리카")}</div>
          <ReplicaChips replicas={d.replicas} />
        </div>
      )}
      <dl className="grid grid-cols-[96px_minmax(0,1fr)] gap-x-4 gap-y-2.5 sm:grid-cols-[120px_minmax(0,1fr)] sm:gap-x-6">
        {d.commit_message && (
          <Info label={t("커밋 메시지")}>
            <span className="min-w-0 whitespace-pre-wrap break-words">{d.commit_message}</span>
          </Info>
        )}
        <Info label={t("커밋")}>
          <span className="min-w-0 truncate font-mono text-sm" title={d.commit_sha ?? undefined}>
            {d.commit_sha ?? "—"}
          </span>
          {d.commit_sha && <CopyButton value={d.commit_sha} label={t("커밋 SHA 복사")} className="size-6" />}
        </Info>
        {d.commit_author && <Info label={t("작성자")}>{d.commit_author}</Info>}
        {d.branch && (
          <Info label={t("브랜치")}>
            <span className="font-mono text-sm">{d.branch}</span>
          </Info>
        )}
        <Info label={t("트리거")}>{TRIGGER_LABEL[d.trigger] ?? d.trigger}</Info>
        <Info label={t("빌더")}>{d.builder_used ? BUILDER[d.builder_used] : "—"}</Info>
        <Info label={t("이미지")}>
          <span className="min-w-0 truncate font-mono text-sm" title={d.image ?? undefined}>
            {d.image ?? "—"}
          </span>
          {d.image && <CopyButton value={d.image} label={t("이미지 복사")} className="size-6" />}
        </Info>
        <Info label={t("배포 ID")}>
          <span className="min-w-0 truncate font-mono text-sm">{d.id}</span>
          <CopyButton value={d.id} label={t("배포 ID 복사")} className="size-6" />
        </Info>
        <Info label={t("생성")}>
          <span className="tabular-nums">{fmtTime(d.created_at)}</span>
        </Info>
        <Info label={t("시작")}>
          <span className="tabular-nums">{fmtTime(d.started_at)}</span>
        </Info>
        <Info label={t("종료")}>
          <span className="tabular-nums">{fmtTime(d.finished_at)}</span>
        </Info>
        <Info label={d.finished_at ? t("소요 시간") : t("경과 시간")}>
          <span className="tabular-nums">{d.started_at ? (d.finished_at ? fmtDuration(d.started_at, d.finished_at) : fmtClock(d.started_at)) : "—"}</span>
        </Info>
      </dl>
    </div>
  );
}

function DeployLogs({ d, svc, phase, filter }: { d: Deployment; svc: CanvasService; phase: string[]; filter: string }) {
  const current = d.id === svc.instance.current_deployment_id;
  const stream = useLogStream(d.status === "skipped" ? null : sse.deployLogs(d.id), { replay: true });
  const parsed = useParsedLines<DeployLogLine>(stream.lines);
  const lines: LogLine[] = useMemo(
    () => [
      ...phase.map((text) => ({ text })),
      ...parsed.map((l) => ({ text: l.msg, at: Date.parse(l.t), replica: l.replica, stream: l.stream })),
    ],
    [phase, parsed],
  );
  const ended = stream.state === "done" && !current;
  return (
    <LogTable
      className="h-full"
      lines={lines}
      state={d.status === "skipped" ? undefined : stream.state}
      onReconnect={stream.reconnect}
      filter={filter}
      baseDate={d.started_at ?? d.created_at}
      showReplica
      emptyText={d.status === "skipped" ? t("건너뛴 배포에는 로그가 없습니다.") : t("아직 출력된 로그가 없습니다.")}
      footer={
        ended ? (
          <div className="mx-4 mt-3 flex items-center gap-2 rounded-md border border-gray-200 bg-secondaryBg px-3 py-2.5 font-sans text-sm text-gray-600">
            <Moon className="size-4 text-gray-400" />{t("이 배포는 더 이상 실행 중이 아닙니다. 마지막 로그를 보여 줍니다.")}
          </div>
        ) : null
      }
    />
  );
}

function HttpLogs({ svc, env, filter }: { svc: CanvasService; env: Environment; filter: string }) {
  const hasDomain = svc.instance.domains.length > 0;
  const stream = useLogStream(hasDomain ? sse.httpLogs(svc.id, env.id) : null, { replay: true, maxLines: 5000 });
  const lines = useParsedLines<HttpLogLine>(stream.lines);
  return (
    <HttpLogTable
      className="h-full"
      lines={lines}
      state={hasDomain ? stream.state : undefined}
      filter={filter}
      emptyText={hasDomain ? t("아직 들어온 요청이 없습니다.") : t("공개 도메인이 없어서 HTTP 로그가 없습니다. 설정 › 네트워킹에서 도메인을 추가하세요.")}
    />
  );
}

export function DeploymentPanel({
  id,
  svc,
  env,
  actions,
  tab: tabProp,
  onTab,
  onClose,
}: {
  id: string;
  svc: CanvasService;
  env: Environment;
  actions: ServiceActions;
  tab: DepTab;
  onTab: (t: DepTab) => void;
  onClose: () => void;
}) {
  // aven-server 자신(self 서비스)의 배포에는 Aven이 띄운 컨테이너가 없어 배포 로그·네트워크 로그가 없다.
  const self = !!svc.instance.self;
  const tabs = self ? TABS.filter((x) => x.id === "details" || x.id === "build") : TABS;
  const tab: DepTab = self && (tabProp === "deploy" || tabProp === "http") ? "build" : tabProp;
  const qc = useQueryClient();
  const dep = useDeployment(id);
  const canEdit = useCaps(svc.project_id).canEdit;
  const { confirm, node } = useConfirm();
  const [filter, setFilter] = useState("");
  const [netKind, setNetKind] = useState<NetworkLogKind>("http");
  const root = useRef<HTMLElement>(null);
  // First Esc leaves the log filter; the next closes the pane (Menu/Dialog Esc entries sit above this one).
  useEscape(() => escCloses(root.current) && onClose());
  // Move focus into the front pane so keyboard users land on it (focus returns to the card on close).
  useEffect(() => {
    root.current?.focus({ preventScroll: true });
  }, []);

  const patchStatus = (status: string) =>
    qc.setQueryData<Deployment>(qk.deployment(id), (old) => (old ? { ...old, status: status as DeploymentStatus } : old));

  const skipped = dep.data?.status === "skipped";
  const stream = useLogStream(skipped ? null : sse.buildLogs(id), {
    replay: true,
    onStatus: (s) => {
      patchStatus(s);
      qc.invalidateQueries({ queryKey: qk.deployments(svc.id, env.id) });
    },
    onDone: (s) => {
      patchStatus(s);
      qc.invalidateQueries({ queryKey: qk.deployment(id) });
      qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      qc.invalidateQueries({ queryKey: qk.deployments(svc.id, env.id) });
    },
  });
  const { build, deploy } = useMemo(() => splitDeploymentLog(stream.lines), [stream.lines]);

  const d = dep.data;
  const running = !!d && !isTerminal(d.status);
  useTicker(running);

  const notFound = dep.error instanceof ApiError && dep.error.status === 404;
  const ref = d ? (d.commit_sha ? shortSha(d.commit_sha) : d.id) : null;

  return (
    <section ref={root} tabIndex={-1} aria-labelledby="dep-title" className={cx(panelCls, "z-[31]")}>
      <header className="flex shrink-0 flex-col gap-1.5 px-5 pt-8 md:px-12 md:pt-10">
        <div className="flex w-full min-w-0 items-center gap-x-3">
          <ServiceIcon service={svc} className="size-6 shrink-0" />
          <h2 id="dep-title" className="flex min-w-0 flex-1 items-center gap-x-3">
            <span className="min-w-0 truncate text-lg font-semibold leading-7 text-foreground">{svc.name}</span>
            <span aria-hidden className="hidden shrink-0 text-lg font-bold text-gray-200 sm:block">
              /
            </span>
            {ref ? (
              <span className="hidden shrink-0 font-mono text-lg leading-7 text-foreground sm:block" title={d?.commit_sha ?? d?.id}>
                {ref}
              </span>
            ) : !dep.error ? (
              <span className="hidden h-5 w-20 shrink-0 animate-pulse rounded bg-gray-100 sm:block" aria-hidden />
            ) : null}
            {d && <StatusPill status={d.status} className="!h-auto !gap-1 !px-2 !py-0.5 !text-xs" />}
          </h2>
          <div className="flex shrink-0 items-center gap-x-2 sm:gap-x-3">
            {d && (
              <time dateTime={d.created_at} className="hidden text-right text-xs tabular-nums text-gray-500 lg:block">
                {fmtTime(d.created_at)}
              </time>
            )}
            {d && <DeploymentMenu d={d} inst={svc.instance} actions={actions} confirm={confirm} canEdit={canEdit} envName={env.name} />}
            <IconButton label={t("배포 상세 닫기")} onClick={onClose} className="text-foreground">
              <X />
            </IconButton>
          </div>
        </div>
        {d ? (
          <div className="flex min-w-0 flex-col gap-0.5 sm:pl-9">
            <p className="min-w-0 truncate text-sm text-foreground" title={d.commit_message ?? undefined}>
              {depTitle(d)}
            </p>
            <p className="min-w-0 truncate text-sm text-gray-500">
              {tr("{time} · {trigger}", {
                time: (
                  <time className="tabular-nums" dateTime={d.created_at} title={fmtTime(d.created_at)}>
                    {fmtRelative(d.created_at)}
                  </time>
                ),
                trigger: viaText(d),
              })}
              {d.started_at && (
                <span className="tabular-nums">
                  {" · "}
                  {running ? fmtClock(d.started_at) : fmtDuration(d.started_at, d.finished_at)}
                </span>
              )}
            </p>
          </div>
        ) : !dep.error ? (
          <div className="flex flex-col gap-2 sm:pl-9" aria-hidden>
            <div className="h-4 w-2/5 animate-pulse rounded bg-gray-100" />
            <div className="h-4 w-1/4 animate-pulse rounded bg-gray-100" />
          </div>
        ) : null}
      </header>
      <div className="mt-4 shrink-0 border-b border-gray-200 px-5 md:px-12">
        <HeaderTabs
          tabs={tabs}
          value={tab}
          onChange={onTab}
          label={t("배포 상세 탭")}
          idPrefix="dep"
          className="mb-[-1px] pb-3 md:pb-0"
        />
      </div>
      {tab !== "details" && d && (
        <LogToolbar>
          {tab === "http" && <NetworkLogKinds value={netKind} onChange={setNetKind} />}
          <LogFilter
            value={filter}
            onChange={setFilter}
            placeholder={
              tab === "http"
                ? netKind === "http"
                  ? t("HTTP 로그 검색 예: 경로, 상태 코드")
                  : NETWORK_FILTER_PLACEHOLDER[netKind]
                : tab === "build"
                  ? t("빌드 로그 검색")
                  : undefined
            }
          />
          {tab === "http" && netKind !== "http" && canEdit && <NetworkDiagnostics sid={svc.id} eid={env.id} />}
        </LogToolbar>
      )}

      {actions.error ? <ErrorBox className="mx-5 mt-3 md:mx-12" error={actions.error} /> : null}

      <div role="tabpanel" id={tabPanelId("dep")} aria-labelledby={tabId("dep", tab)} className="min-h-0 flex-1">
        {dep.error && !d ? (
          <div className="px-5 pt-8 md:px-12">
            <div role="alert" className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-gray-200 px-6 py-10 text-center">
              <div className="text-sm font-semibold text-foreground">{notFound ? t("배포를 찾을 수 없습니다") : t("배포 정보를 불러오지 못했습니다")}</div>
              {notFound ? (
                <p className="text-sm text-gray-500">{t("삭제되었거나 다른 서비스의 배포입니다.")}</p>
              ) : (
                <ErrorBox error={dep.error} className="max-w-[460px] text-left" />
              )}
              <div className="mt-1 flex gap-3">
                {!notFound && (
                  <Button variant="outline" onClick={() => dep.refetch()} loading={dep.isFetching}>
                    {!dep.isFetching && <RefreshCw />} {t("다시 시도")}
                  </Button>
                )}
                <Button variant="outline" onClick={onClose}>
                  {t("배포 목록으로")}
                </Button>
              </div>
            </div>
          </div>
        ) : !d ? (
          <div role="status" className="flex flex-col items-center gap-2 py-16 text-center">
            <Spinner />
            <span className="text-xs text-gray-500">{t("배포 정보를 불러오는 중…")}</span>
          </div>
        ) : tab === "details" ? (
          <Details d={d} svc={svc} env={env} />
        ) : tab === "build" ? (
          <LogTable
            className="h-full"
            lines={build.map((text) => ({ text }))}
            state={skipped ? undefined : stream.state}
            onReconnect={stream.reconnect}
            filter={filter}
            baseDate={d.started_at ?? d.created_at}
            emptyText={skipped ? t("건너뛴 배포는 빌드하지 않았습니다.") : running ? t("빌드 로그를 기다리는 중…") : t("이 배포에는 빌드 로그가 없습니다.")}
          />
        ) : tab === "deploy" ? (
          <DeployLogs d={d} svc={svc} phase={deploy} filter={filter} />
        ) : netKind === "http" ? (
          <HttpLogs svc={svc} env={env} filter={filter} />
        ) : (
          <ServiceNetworkLogs sid={svc.id} eid={env.id} kind={netKind} filter={filter} />
        )}
      </div>
      {node}
    </section>
  );
}
