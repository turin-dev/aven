"use client";

import { useEffect, useState, type ReactNode } from "react";
import { Clock, Copy, HardDrive, PencilLine, Server } from "lucide-react";
import { isTerminal, type CanvasService } from "@/lib/api";
import { fmtBytes, fmtRelative, fmtTime, fmtUntil } from "@/lib/format";
import { describeCron, nextRuns } from "@/lib/cron";
import { pendingSummary, useStaged } from "@/lib/staged";
import { useNodes } from "@/lib/queries";
import { dateLocale, t } from "@/lib/i18n";
import { STATUS_LABEL, StatusIcon, TEXT_TONE, statusTone, type Tone } from "./StatusBadge";
import { ServiceIcon } from "./ServiceIcon";
import { HABadge } from "./HABadge";
import { SelfServiceBadge } from "./SelfService";
import { cx } from "./ui";

export const CARD_W = 287;
export const CARD_H = 144;
export const VOL_H = 48;

/** Like TEXT_TONE, but a healthy service reads green as on the reference canvas. */
const STATUS_TEXT: Record<Tone, string> = { ...TEXT_TONE, success: "text-success" };

function attachmentCount(s: CanvasService): number {
  const inst = s.instance;
  return (s.volumes.length > 0 ? 1 : 0) + (Math.max(inst.replicas, inst.current_replicas ?? 0) > 1 ? 1 : 0) + (inst.cron_schedule ? 1 : 0);
}

/** Drawn height of a service node: 144 + 48 per attachment row. */
export function cardHeight(s: CanvasService): number {
  return CARD_H + VOL_H * attachmentCount(s);
}

/** Height reserved for layout/overlap checks (never less than the historical card + volume slot). */
export function cardSpan(s: CanvasService): number {
  return Math.max(CARD_H + VOL_H, cardHeight(s));
}

type NodeState = "edited" | "new" | "removed";
const NODE_STATE: Record<NodeState, { card: string; badge: string; label: string }> = {
  edited: { card: "border-blue-200 bg-blue-50 dark:border-blue-200", badge: "bg-blue-300", label: t("수정됨") },
  new: { card: "border-green-200 bg-green-50 dark:border-green-200", badge: "bg-green-300", label: t("신규") },
  removed: { card: "border-red-200 bg-red-50 opacity-40 dark:border-red-200", badge: "bg-red-300", label: t("삭제됨") },
};

/** Elapsed clock for in-progress deployments ("00:12"), ticking once a second while shown. */
function useClock(since: string | null) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!since) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [since]);
  if (!since) return null;
  const s = Math.max(0, Math.floor((now - new Date(since).getTime()) / 1000));
  const m = Math.floor(s / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return m >= 60 ? `${Math.floor(m / 60)}:${pad(m % 60)}:${pad(s % 60)}` : `${pad(m)}:${pad(s % 60)}`;
}

function statusLine(s: CanvasService) {
  const inst = s.instance;
  const d = s.latest_deployment;
  const inProgress = !!d && !isTerminal(d.status);
  const since = inProgress ? (d.started_at ?? d.created_at) : null;
  const when = d?.finished_at ?? d?.created_at ?? null;
  if (inProgress && inst.status === "online") return { status: inst.status, label: t("온라인"), progress: STATUS_LABEL[d.status], since, when: null as string | null };
  if (inProgress) return { status: d.status, label: STATUS_LABEL[d.status], progress: null, since, when: null };
  const base = { progress: null as string | null, since: null as string | null };
  switch (inst.status) {
    case "online":
      return { ...base, status: inst.status, label: t("온라인"), when: null };
    case "scheduled":
      return { ...base, status: inst.status, label: t("예약됨"), when: null };
    case "failed":
      return { ...base, status: inst.status, label: t("배포 실패"), when };
    case "crashed":
      return { ...base, status: inst.status, label: t("크래시"), when };
    case "sleeping":
      return { ...base, status: inst.status, label: t("슬립 중"), when: null };
    case "offline":
      return { ...base, status: inst.status, label: t("오프라인"), when: null };
    case "deploying":
      return { ...base, status: inst.status, label: t("배포 중"), when: null };
    default:
      // 건너뛴 배포(감시 경로 불일치, 집중형 PR 환경에서 변경 없는 서비스)
      if (d?.status === "skipped") return { ...base, status: d.status, label: t("건너뜀"), when };
      return { ...base, status: inst.status, label: d ? t("대기") : t("아직 배포하지 않음"), when: null };
  }
}
/** "오늘 14:05" style time of the next cron run. */
function fmtNextRun(d: Date): string {
  const time = d.toLocaleTimeString(dateLocale(), { hour: "2-digit", minute: "2-digit", hour12: false });
  const today = new Date();
  if (d.toDateString() === today.toDateString()) return time;
  return `${d.toLocaleDateString(dateLocale(), { month: "numeric", day: "numeric" })} ${time}`;
}

function Attachment({ z, children, title, ...rest }: { z: string; children: ReactNode; title?: string; "data-vol"?: string }) {
  return (
    <div
      {...rest}
      title={title}
      className={cx(
        "node-modifier relative -mt-[24px] flex h-[72px] w-full translate-x-px items-center overflow-hidden rounded-xl border bg-secondaryBg px-0 pb-0 pt-[24px] hover:border-pink-200 hover:shadow-[0px_0px_0px_4px_rgba(166,_103,_228,_0.06)] hover:dark:border-gray-200 hover:dark:bg-gray-100 hover:dark:shadow-none",
        z,
      )}
    >
      {children}
    </div>
  );
}

function AttachmentRow({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <div className="grid h-full w-full cursor-pointer grid-cols-[24px_1fr] items-center gap-3 px-6">
      <span className="icon-container icon-sm mx-auto text-lg text-gray-500">{icon}</span>
      <p className="flex min-w-0 items-center gap-3 overflow-hidden text-ellipsis whitespace-nowrap text-sm text-gray-500">{children}</p>
    </div>
  );
}

export function ServiceCard({ svc, envId, selected }: { svc: CanvasService; envId: string; selected: boolean }) {
  const inst = svc.instance;
  const domain = inst.domains[0]?.host;
  // aven-server 자신(self 서비스)은 도메인 대신 지금 도는 서버 버전을 보여 준다.
  const sub = !domain && inst.self ? t("이 서버 · {version}", { version: inst.self.version }) : null;
  const line = statusLine(svc);
  const clock = useClock(line.since);
  const tone = statusTone(line.status);
  const vol = svc.volumes[0];
  const { staged: local } = useStaged(svc.id, envId);
  const staged = pendingSummary(inst, local).count;
  const replicas = inst.replicas;
  const running = inst.current_replicas ?? replicas;
  const showReplicas = Math.max(replicas, running) > 1;
  const nodes = useNodes();
  const node = (nodes.data?.length ?? 0) > 1 ? nodes.data?.find((n) => n.id === inst.node_id) : undefined;
  const cron = inst.cron_schedule;
  const nextRun = cron ? nextRuns(cron)[0] : null;
  const d = svc.latest_deployment;
  const inProgress = !!d && !isTerminal(d.status);
  // keel deletes services immediately, so "removed" has no source yet; "new" = never deployed.
  const state: NodeState | null = staged > 0 ? "edited" : !inst.current_deployment_id && !d ? "new" : null;
  const failed = !inProgress && (inst.status === "failed" || inst.status === "crashed");

  const volPct = vol && vol.size_bytes !== null && vol.size_limit_mb ? Math.min(100, (vol.size_bytes / (vol.size_limit_mb * 1048576)) * 100) : 0;
  const replicaLabel = inProgress && running !== replicas
    ? t("레플리카 {count}개로 조정 중", { count: replicas })
    : running < replicas
      ? t("{running}/{replicas} 레플리카 실행 중", { running, replicas })
      : t("레플리카 {count}개", { count: Math.max(replicas, running) });

  return (
    <div className="relative w-[287px]" style={{ height: cardHeight(svc) }}>
      <div
        className={cx(
          "node group pointer-events-auto relative z-10 flex h-[144px] w-[287px] translate-x-px translate-y-px cursor-pointer flex-col justify-between rounded-xl border p-6 shadow-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700",
          state ? NODE_STATE[state].card : "bg-white dark:bg-secondaryBg",
          selected
            ? "border-pink-400 dark:border-pink-400"
            : failed
              ? "border-red-400 dark:border-red-400"
              : !state && "border-gray-200 dark:border-gray-200",
          !selected &&
            "hover:shadow-[0px_0px_0px_4px_rgba(166,_103,_228,_0.06)] hover:dark:shadow-[0px_0px_0px_4px_rgba(255,_255,_255,_0.06)]",
          !selected && !failed && !state && "hover:border-pink-200 hover:dark:border-gray-200",
        )}
      >
        {state && (
          <span
            title={state === "edited" ? t("적용하지 않은 변경 사항 {count}개", { count: staged }) : undefined}
            className={cx("absolute right-[10px] top-[10px] z-10 rounded px-1.5 py-0.5 text-[10px] font-semibold text-background", NODE_STATE[state].badge)}
          >
            {NODE_STATE[state].label}
          </span>
        )}
        <div className={cx("flex space-x-3", domain || sub ? "items-start" : "items-center")}>
          <span className={cx("icon-container icon-lg text-2xl text-current", (domain || sub) && "mt-1")}>
            <ServiceIcon service={svc} className="size-[24px]" />
          </span>
          <div className="flex min-w-0 flex-1 flex-col">
            <div className={cx("flex items-center gap-2", state && "pr-14")}>
              <p className="min-w-0 truncate text-left text-base font-semibold">{svc.name}</p>
              <span className="ml-auto flex shrink-0 items-center gap-1">
                {inst.self && <SelfServiceBadge self={inst.self} />}
                <HABadge svcId={svc.id} envId={envId} />
              </span>
            </div>
            {domain && (
              <a
                href={`https://${domain}`}
                target="_blank"
                rel="noreferrer"
                data-nodrag
                onClick={(e) => e.stopPropagation()}
                className="block max-w-full truncate text-sm text-neutral-secondary hover:underline"
              >
                {domain}
              </a>
            )}
            {sub && <span className="block max-w-full truncate text-sm text-neutral-secondary">{sub}</span>}
          </div>
        </div>
        <div className="flex items-end justify-between">
          {staged > 0 && !inProgress ? (
            <div className="grid min-w-0 flex-1 grid-cols-[24px_1fr] items-center gap-3">
              <span className="flex items-center justify-center text-blue-400">
                <PencilLine className="size-4" />
              </span>
              <p className="min-w-0 truncate whitespace-nowrap text-sm text-blue-600">{t("변경 {count}개", { count: staged })}</p>
            </div>
          ) : (
            <div className="grid min-w-0 flex-1 grid-cols-[24px_1fr] items-center gap-3">
              {line.status === "online" ? (
                <svg className="mx-auto" width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                  <circle cx="8" cy="8" r="3" className="fill-green-500" />
                  <circle opacity="0.5" cx="8" cy="8" r="6.25" strokeWidth="1.5" className="stroke-green-200" />
                </svg>
              ) : (
                <span className="flex items-center justify-center">
                  <StatusIcon status={line.status} className="!size-4" />
                </span>
              )}
              <p className={cx("min-w-0 truncate whitespace-nowrap text-sm", line.since && !line.progress ? "text-gray-500" : STATUS_TEXT[tone])}>
                {line.label}
                {line.progress && <span className="text-gray-500"> · {line.progress}</span>}
                {clock && <span className="tabular-nums text-gray-500"> ({clock})</span>}
                {line.when && !node && (
                  <span title={fmtTime(line.when)}> {fmtRelative(line.when)}</span>
                )}
              </p>
            </div>
          )}
          {node && (
            <span
              title={t("{name} 노드에서 실행", { name: node.name })}
              className={cx(
                "flex h-5 shrink-0 items-center gap-1 rounded border px-1.5 text-xs",
                node.status === "online" ? "border-gray-200 text-gray-500" : "border-red-200 text-red-600",
              )}
            >
              <Server className="size-3" />
              {node.name}
            </span>
          )}
        </div>
      </div>
      {vol && (
        <Attachment
          data-vol={vol.id}
          z="z-[3]"
          title={vol.size_bytes !== null ? `${vol.name} · ${fmtBytes(vol.size_bytes)}${vol.size_limit_mb != null ? ` / ${fmtBytes(vol.size_limit_mb * 1048576)}` : ""}` : vol.name}
        >
          <div className="absolute bottom-0 left-0 top-0 -z-1 flex h-full bg-gray-200 opacity-30" style={{ width: `${volPct || 6}%` }} />
          <AttachmentRow icon={<HardDrive />}>
            <span className="min-w-0 truncate">{vol.name}</span>
            {vol.size_bytes !== null && vol.size_limit_mb != null && vol.size_bytes >= vol.size_limit_mb * 1048576 * 0.9 && (
              <span
                className={cx(
                  "ml-auto shrink-0 tabular-nums",
                  vol.size_bytes >= vol.size_limit_mb * 1048576 ? "text-red-600" : "text-yellow-600",
                )}
              >
                {fmtBytes(vol.size_bytes)} / {fmtBytes(vol.size_limit_mb * 1048576)}
              </span>
            )}
          </AttachmentRow>
        </Attachment>
      )}
      {showReplicas && (
        <Attachment z="z-[2]">
          <AttachmentRow icon={<Copy className="-rotate-90" />}>
            <span className="min-w-0 truncate tabular-nums">{replicaLabel}</span>
          </AttachmentRow>
        </Attachment>
      )}
      {cron && (
        <Attachment z="z-[1]" title={`${describeCron(cron)} (${cron})`}>
          <AttachmentRow icon={<Clock />}>
            <span className="min-w-0 truncate tabular-nums">
              {nextRun ? (
                <>
                  {t("다음 실행 {time}", { time: fmtNextRun(nextRun) })}
                  <span className="text-gray-400"> · {fmtUntil(nextRun)}</span>
                </>
              ) : (
                describeCron(cron)
              )}
            </span>
          </AttachmentRow>
        </Attachment>
      )}
    </div>
  );
}
