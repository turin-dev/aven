"use client";

import { useEffect, useState, type ReactNode } from "react";
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock,
  Globe,
  Hammer,
  History,
  Layers,
  Loader2,
  Lock,
  MapPin,
  Moon,
  MoreVertical,
  PauseCircle,
  Play,
  RefreshCw,
  Rocket,
  RotateCcw,
  RotateCw,
  ScrollText,
  SkipForward,
  Sparkles,
  Square,
  Trash2,
  XCircle,
} from "lucide-react";
import {
  isTerminal,
  type CanvasService,
  type Deployment,
  type DeploymentStatus,
  type Environment,
  type Replica,
  type ServiceInstance,
} from "@/lib/api";
import { useCaps, useCronRuns, useDeployments, useNodes } from "@/lib/queries";
import { lock, NO_PERM } from "../Perm";
import { describeCron, nextRuns } from "@/lib/cron";
import { fmtDuration, fmtRelative, fmtTime, fmtUntil, shortSha } from "@/lib/format";
import { pendingSummary, useStaged } from "@/lib/staged";
import type { PanelTab } from "@/lib/nav";
import type { ServiceActions } from "@/lib/useServiceActions";
import { StatusBadge, TRIGGER_LABEL } from "../StatusBadge";
import { BrandIcon } from "../ServiceIcon";
import { Button, ErrorBox, Menu, MenuItem, Spinner, cx, useConfirm } from "../ui";
import { openAvenAgent } from "../AgentPanel";
import { t, tr } from "@/lib/i18n";
import { readStorage, writeStorage } from "@/lib/legacyStorage";


/* ── Stepper ─────────────────────────────────────────────────────────── */
type Step = { id: DeploymentStatus; label: string };
export function stepsFor(inst: Pick<ServiceInstance, "pre_deploy_command" | "healthcheck_type">, d: Deployment): Step[] {
  const out: Step[] = [{ id: "initializing", label: t("준비") }];
  if (d.builder_used !== "image") out.push({ id: "building", label: t("빌드") });
  if (inst.pre_deploy_command) out.push({ id: "pre_deploy", label: t("사전 배포") });
  out.push({ id: "deploying", label: t("배포") });
  if (inst.healthcheck_type !== "none") out.push({ id: "healthchecking", label: t("헬스체크") });
  out.push({ id: "live", label: t("활성|deployment") });
  return out;
}
export function Stepper({ steps, status }: { steps: Step[]; status: DeploymentStatus }) {
  const idx = status === "queued" ? -1 : steps.findIndex((s) => s.id === status);
  return (
    <ol className="flex items-center" aria-label={t("배포 단계")}>
      {steps.map((s, i) => {
        const done = i < idx || (s.id === "live" && idx === i);
        const active = i === idx && s.id !== "live";
        return (
          <li key={s.id} className="flex min-w-0 flex-1 items-center last:flex-none" aria-current={active ? "step" : undefined}>
            <span className="flex items-center gap-1.5">
              {done ? (
                <CheckCircle2 className="size-4 shrink-0 text-success" />
              ) : active ? (
                <Loader2 className="size-4 shrink-0 animate-spin text-blue-600" />
              ) : (
                <span className="size-4 shrink-0 rounded-full border border-gray-300" />
              )}
              <span className={cx("whitespace-nowrap text-sm", done ? "text-gray-600" : active ? "font-medium text-blue-600" : "text-gray-400")}>
                {s.label}
              </span>
            </span>
            {i < steps.length - 1 && <span className={cx("mx-2.5 h-px min-w-3 flex-1", i < idx ? "bg-green-300" : "bg-gray-200")} />}
          </li>
        );
      })}
    </ol>
  );
}
/* ── Helpers ─────────────────────────────────────────────────────────── */
export function canRollback(d: Deployment, currentId: string | null) {
  return !!d.image && d.id !== currentId && (d.status === "live" || d.status === "removed");
}
export function useTicker(on: boolean) {
  const [, tick] = useState(0);
  useEffect(() => {
    if (!on) return;
    const timer = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, [on]);
}
export function depTitle(d: Deployment): string {
  if (d.commit_message) return d.commit_message.split("\n")[0];
  if (d.image) return d.image;
  if (d.trigger === "variables") return t("변수 변경 적용");
  return TRIGGER_LABEL[d.trigger] ?? d.trigger;
}

export function fmtClock(start: string | null | undefined, end?: string | null): string {
  if (!start) return "00:00";
  const a = Date.parse(start);
  const b = end ? Date.parse(end) : Date.now();
  if (isNaN(a) || isNaN(b)) return "00:00";
  const s = Math.max(0, Math.floor((b - a) / 1000));
  const p = (n: number) => String(n).padStart(2, "0");
  const h = Math.floor(s / 3600);
  return h ? `${h}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}` : `${p(Math.floor(s / 60))}:${p(s % 60)}`;
}

function viaText(d: Deployment) {
  if (d.trigger === "github") return "GitHub";
  if (d.trigger === "cli") return "CLI";
  if (d.builder_used === "image" && d.trigger === "manual") return "Docker";
  return TRIGGER_LABEL[d.trigger] ?? d.trigger;
}


/* ── Card tones (§1.6: badge + tint, §4.3: View logs outline colour, kebab focus ring) ── */
type CardTone = "green" | "blue" | "yellow" | "red" | "gray";
export function cardTone(s: DeploymentStatus): CardTone {
  switch (s) {
    case "live":
      return "green";
    case "failed":
    case "crashed":
      return "red";
    case "waiting":
      return "yellow";
    case "queued":
    case "removed":
    case "skipped":
    case "cancelled":
      return "gray";
    default:
      return "blue";
  }
}
const TONES: Record<CardTone, { bg: string; border: string; ring: string; within: string; btn: string; text: string }> = {
  green: {
    bg: "bg-green-50",
    border: "border-green-200",
    ring: "focus-visible:ring-green-500",
    within: "[&:has([data-card-link]:focus-visible)]:ring-green-500",
    btn: "border-green-200 text-green-500 dark:text-green-700 hover:bg-green-100 focus-visible:ring-green-600 focus-visible:bg-green-100",
    text: "text-green-500",
  },
  blue: {
    bg: "bg-blue-50",
    border: "border-blue-200",
    ring: "focus-visible:ring-blue-500",
    within: "[&:has([data-card-link]:focus-visible)]:ring-blue-500",
    btn: "border-blue-200 text-blue-500 dark:text-blue-700 hover:bg-blue-100 focus-visible:ring-blue-600 focus-visible:bg-blue-100",
    text: "text-blue-500",
  },
  yellow: {
    bg: "bg-yellow-50",
    border: "border-yellow-200",
    ring: "focus-visible:ring-yellow-500",
    within: "[&:has([data-card-link]:focus-visible)]:ring-yellow-500",
    btn: "border-yellow-200 text-yellow-500 dark:text-yellow-700 hover:bg-yellow-100 focus-visible:ring-yellow-600 focus-visible:bg-yellow-100",
    text: "text-yellow-600",
  },
  red: {
    bg: "bg-red-50",
    border: "border-red-200",
    ring: "focus-visible:ring-red-500",
    within: "[&:has([data-card-link]:focus-visible)]:ring-red-500",
    btn: "border-red-200 text-red-500 dark:text-red-700 hover:bg-red-100 focus-visible:ring-red-600 focus-visible:bg-red-100",
    text: "text-red-500",
  },
  gray: {
    bg: "bg-gray-100",
    border: "border-gray-200",
    ring: "focus-visible:ring-gray-500",
    within: "[&:has([data-card-link]:focus-visible)]:ring-gray-500",
    btn: "border-gray-200 text-foreground hover:bg-gray-200 focus-visible:ring-gray-600 focus-visible:bg-gray-200",
    text: "text-gray-500",
  },
};

const LOGS_BTN =
  "hidden sm:inline-flex shrink-0 items-center justify-center whitespace-nowrap h-[34px] px-3 rounded-md border bg-transparent text-sm leading-5 cursor-pointer transform transition-transform duration-50 active:scale-95 focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50";

/** Content above an `absolute inset-0` card link: only real controls catch the pointer. */
const PASS_THROUGH = "relative pointer-events-none [&_:is(a,button,input,select,textarea,label,summary,[title],[tabindex],[role=button])]:pointer-events-auto";
const GRID = "grid w-full grid-cols-[auto_minmax(0,1fr)_auto] sm:grid-cols-[auto_auto_minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2";

/* ── Avatar ──────────────────────────────────────────────────────────── */
function Avatar({ d }: { d: Deployment }) {
  const who = d.commit_author ?? "keel";
  const src = d.trigger === "cli" ? "cli" : d.builder_used === "image" ? "docker" : "github";
  return (
    <div className="relative h-9 w-9 shrink-0" aria-hidden="true" title={who}>
      <span className="absolute left-0 top-0 inline-block size-6 overflow-hidden rounded-full">
        <span className="flex h-full w-full items-center justify-center rounded-full bg-gradient-to-br from-[#6f31aa] to-[#306ee8] text-xs font-semibold uppercase text-white">
          {who.slice(0, 1)}
        </span>
      </span>
      {/* §4.3: GitHub `border-secondaryBg bg-white dark:bg-black`, Docker `bg-blue-600 text-white p-1`, CLI `bg-gray-600 text-white p-1`. */}
      <span
        className={cx(
          "icon-container icon-lg absolute bottom-0 right-0 flex items-center justify-center rounded-full border border-secondaryBg",
          src === "github" && "bg-white text-black dark:bg-black dark:text-white",
          src === "docker" && "bg-blue-600 p-1 text-white",
          src === "cli" && "bg-gray-600 p-1 text-white",
        )}
      >
        {src === "cli" ? (
          <span className="font-mono text-[10px] font-bold leading-none">&gt;_</span>
        ) : (
          <BrandIcon name={src} mono className={src === "github" ? "size-[18px]" : "size-3.5"} />
        )}
      </span>
    </div>
  );
}
/* ── Deployment actions menu ─────────────────────────────────────────── */
export type DepMenuProps = {
  d: Deployment;
  inst: ServiceInstance;
  actions: ServiceActions;
  confirm: ReturnType<typeof useConfirm>["confirm"];
  onLogs?: () => void;
  /** false(viewer)면 재배포/롤백/재시작/중단/제거를 "권한이 없습니다"로 잠근다. */
  canEdit?: boolean;
  /** Environment name for confirm copy. */
  envName?: string;
};

/**
 * Deployment actions kebab, modeled on Railway's dashboard (§4.3): 로그 보기, 재시작, 재배포, (최신 커밋 강제 재빌드), 롤백, then danger 중단 / 제거.
 * Ghost icon button 34px whose focus ring follows the card status colour.
 */
export function DeploymentMenu({
  d,
  inst,
  actions,
  confirm,
  onLogs,
  canEdit = true,
  envName,
  ring = "focus-visible:ring-gray-500",
}: DepMenuProps & { ring?: string; large?: boolean }) {
  const running = !isTerminal(d.status);
  const current = d.id === inst.current_deployment_id;
  const rollbackable = canRollback(d, inst.current_deployment_id);
  const canForceBuild = current && !!d.commit_sha && !!inst.skip_builds && !running;
  const offline = inst.status === "offline";
  const why = canEdit ? undefined : NO_PERM;
  const ref = shortSha(d.commit_sha) !== "—" ? shortSha(d.commit_sha) : (d.image ?? d.id);
  const failed = d.status === "failed" || d.status === "crashed";
  return (
    <Menu
      label={t("배포 작업")}
      trigger={(p) => (
        <button
          type="button"
          {...p}
          title={t("배포 작업")}
          aria-label={t("배포 작업 더 보기")}
          className={cx(
            "flex shrink-0 cursor-pointer items-center justify-center rounded-md text-foreground hover:text-gray-700 focus:outline-none focus-visible:outline-none focus-visible:ring-2 data-[state=open]:text-gray-700",
            ring,
          )}
        >
          <span className="icon-container icon-md text-2xl text-current">
            <MoreVertical aria-hidden />
          </span>
        </button>
      )}
    >
      {(close) => (
        <>
          {onLogs && (
            <MenuItem
              icon={<ScrollText />}
              onSelect={() => {
                close();
                onLogs();
              }}
            >
              {t("로그 보기")}
            </MenuItem>
          )}
          {current && !inst.self && (
            <MenuItem
              icon={<RotateCw />}
              disabled={!canEdit || offline || running}
              reason={why}
              onSelect={() => {
                close();
                confirm({
                  title: t("재시작"),
                  message: t("현재 배포의 컨테이너 {count}개를 다시 시작합니다. 이미지는 그대로이고, 잠깐 응답이 끊길 수 있습니다.", { count: inst.replicas }),
                  confirmLabel: t("재시작"),
                  run: actions.restart,
                });
              }}
            >
              {t("재시작")}
            </MenuItem>
          )}
          <MenuItem
            icon={<RotateCcw />}
            disabled={!canEdit || !d.image || running}
            reason={why}
            onSelect={() => {
              close();
              confirm({
                title: t("다시 배포"),
                message: tr("{ref}의 이미지와 {vars}로 새 배포를 만듭니다. 빌드는 다시 하지 않습니다.", {
                  ref: <span className="font-mono text-foreground">{ref}</span>,
                  vars: <b className="text-foreground">{t("현재 변수")}</b>,
                }),
                confirmLabel: t("재배포"),
                run: () => actions.redeploy(d),
              });
            }}
          >
            {t("재배포")}
          </MenuItem>
          {current && !canForceBuild && (
            <MenuItem
              icon={<Rocket />}
              disabled={!canEdit || running}
              reason={why}
              onSelect={() => {
                close();
                void actions.deploy();
              }}
            >
              {d.builder_used === "image" ? t("이미지 다시 받아 배포") : t("최신 커밋으로 배포")}
            </MenuItem>
          )}
          {canForceBuild && (
            <MenuItem
              icon={<Hammer />}
              disabled={!canEdit}
              reason={why}
              onSelect={() => {
                close();
                confirm({
                  title: t("최신 커밋 강제 재빌드"),
                  message: t("연결된 브랜치의 최신 커밋으로 새 배포를 시작하고, 보존된 빌드 이미지를 재사용하지 않습니다."),
                  confirmLabel: t("다시 빌드"),
                  run: actions.forceBuild,
                });
              }}
            >
              {t("최신 커밋 강제 재빌드")}
            </MenuItem>
          )}
          {!current && (
            <MenuItem
              icon={<History />}
              disabled={!canEdit || !rollbackable}
              reason={why}
              onSelect={() => {
                close();
                confirm({
                  title: t("이 배포로 롤백"),
                  message: tr("{ref}의 이미지와 {vars}로 되돌립니다.", {
                    ref: <span className="font-mono text-foreground">{ref}</span>,
                    vars: <b className="text-foreground">{t("그때의 변수")}</b>,
                  }),
                  confirmLabel: t("롤백"),
                  run: () => actions.rollback(d),
                });
              }}
            >
              {t("롤백")}
            </MenuItem>
          )}
          {failed && (
            <MenuItem
              icon={<Sparkles />}
              onSelect={() => {
                close();
                openAvenAgent({
                  serviceId: d.service_id,
                  environmentId: d.environment_id,
                  prompt: d.status === "failed"
                    ? t("배포 {id}가 실패했습니다. 빌드·런타임 로그를 읽고 원인과 고칠 방법을 알려 주세요.", { id: d.id })
                    : t("배포 {id}가 비정상 종료했습니다. 빌드·런타임 로그를 읽고 원인과 고칠 방법을 알려 주세요.", { id: d.id }),
                });
              }}
            >
              {t("Aven Agent로 진단")}
            </MenuItem>
          )}
          {(running || (current && !offline)) && <div role="separator" className="my-2 h-px w-full bg-gray-100" />}
          {running && (
            <MenuItem
              danger
              icon={<Square />}
              disabled={!canEdit}
              reason={why}
              onSelect={() => {
                close();
                confirm({
                  title: t("배포 중단"),
                  message: t("진행 중인 배포를 멈춥니다. 현재 활성 배포는 그대로 유지됩니다."),
                  confirmLabel: t("중단"),
                  danger: true,
                  run: () => actions.cancel(d.id),
                });
              }}
            >
              {t("중단")}
            </MenuItem>
          )}
          {current && !offline && !running && !inst.self && (
            <MenuItem
              danger
              icon={<Trash2 />}
              disabled={!canEdit}
              reason={why}
              onSelect={() => {
                close();
                confirm({
                  title: t("배포 제거"),
                  message: envName
                    ? tr("{env} 환경에서 이 배포의 컨테이너를 멈추고 서비스를 오프라인으로 둡니다. 다시 배포하거나 롤백할 때까지 접속할 수 없습니다. 배포 기록과 이미지는 남습니다.", {
                        env: <span className="font-mono text-foreground">{envName}</span>,
                      })
                    : t("이 배포의 컨테이너를 멈추고 서비스를 오프라인으로 둡니다. 다시 배포하거나 롤백할 때까지 접속할 수 없습니다. 배포 기록과 이미지는 남습니다."),
                  confirmLabel: t("제거"),
                  danger: true,
                  run: actions.stop,
                });
              }}
            >
              {t("제거")}
            </MenuItem>
          )}
        </>
      )}
    </Menu>
  );
}


function Meta({ d }: { d: Deployment }) {
  const extra = [d.commit_author, d.commit_sha && shortSha(d.commit_sha), d.started_at && d.finished_at && fmtDuration(d.started_at, d.finished_at)]
    .filter(Boolean)
    .join(" · ");
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-y-1">
      <p className="w-full min-w-0 truncate text-left text-sm text-foreground" title={d.commit_message ?? depTitle(d)}>
        {depTitle(d)}
      </p>
      <p className="min-w-0 truncate text-sm text-gray-500" title={extra || undefined}>
        <time dateTime={d.created_at} title={fmtTime(d.created_at)}>
          {fmtRelative(d.created_at)}
        </time>{" "}
        via <span className="text-gray-500">{viaText(d)}</span>
      </p>
    </div>
  );
}

const REPLICA_TONE: Record<Replica["status"], string> = {
  running: "bg-green-600",
  starting: "bg-yellow-600 animate-soft-pulse",
  restarting: "bg-yellow-600 animate-soft-pulse",
  exited: "bg-red-600",
};
const REPLICA_LABEL: Record<Replica["status"], string> = { running: t("실행 중"), starting: t("시작 중"), restarting: t("재시작 중"), exited: t("종료됨") };

export function ReplicaChips({ replicas }: { replicas: Replica[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {replicas.map((r) => (
        <span
          key={r.name}
          className="inline-flex h-6 items-center gap-1.5 rounded-full border border-gray-200 bg-background px-2.5 text-xs text-gray-600"
          title={r.restarts ? t("{name}: {status}, 재시작 {count}회", { name: r.name, status: REPLICA_LABEL[r.status], count: r.restarts }) : t("{name}: {status}", { name: r.name, status: REPLICA_LABEL[r.status] })}
        >
          <span className={cx("size-1.5 rounded-full", REPLICA_TONE[r.status])} />
          <span className="font-mono text-xs text-foreground">{r.name}</span>
          <span>{REPLICA_LABEL[r.status]}</span>
          {r.restarts > 0 && <span className="tabular-nums text-yellow-600">↻{r.restarts}</span>}
        </span>
      ))}
    </div>
  );
}

type RowProps = DepMenuProps & { onLogs: () => void };

/**
 * Deployment card, modeled on Railway's dashboard (§4.3): `flex items-center justify-between rounded-md px-3 py-4 bg-gray-100 select-none` + status tint.
 * Grid: badge (sm:min-w-20) · 36px avatar · body · actions (row-reverse, gap-x-3, pl-2). `note` is an optional line under the body.
 */
function CardRow({
  badge,
  d,
  actions,
  note,
  className,
}: {
  badge: ReactNode;
  d: Deployment;
  actions: ReactNode;
  note?: ReactNode;
  className: string;
}) {
  return (
    <div className={cx("flex h-full w-full select-none items-center justify-between rounded-md px-3 py-4", className)}>
      <div className={GRID}>
        <div className="flex sm:min-w-20">{badge}</div>
        <div className="row-start-2 sm:row-start-auto">
          <Avatar d={d} />
        </div>
        <div className="col-span-2 row-start-2 min-w-0 sm:col-span-1 sm:row-start-auto">
          <Meta d={d} />
        </div>
        <div className="col-start-3 row-start-1 flex shrink-0 flex-row-reverse items-center gap-x-3 pl-2 sm:col-start-auto sm:row-start-auto">{actions}</div>
        {note && <div className="col-span-2 col-start-2 min-w-0 sm:col-span-1 sm:col-start-3">{note}</div>}
      </div>
    </div>
  );
}

function CardLink({ d, onLogs, className }: { d: Deployment; onLogs: () => void; className?: string }) {
  return (
    <button
      type="button"
      data-card-link
      data-dep-link={d.id}
      aria-label={t("배포 보기: {title}", { title: depTitle(d) })}
      onClick={onLogs}
      className={cx("absolute inset-0 cursor-pointer rounded-[inherit] focus-visible:outline-none focus-visible:ring-0", className)}
    />
  );
}

function LogsButton({ tone, onClick }: { tone: CardTone; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={cx(LOGS_BTN, TONES[tone].btn)}>
      {t("로그 보기")}
    </button>
  );
}

function ActiveCard(p: RowProps) {
  const { d, inst, onLogs, actions, confirm, canEdit = true } = p;
  const [open, setOpen] = useState(false);
  const stopped = inst.status === "offline";
  const sleeping = inst.status === "sleeping";
  const crashed = inst.status === "crashed" || d.status === "crashed";
  const tone: CardTone = crashed ? "red" : stopped ? "gray" : sleeping ? "yellow" : "green";
  const T = TONES[tone];
  const exited = d.replicas.filter((r) => r.status === "exited").length;
  const regionId = `dep-active-${d.id}`;
  return (
    <div className={cx("relative flex select-none flex-col rounded-lg border [&:has([data-card-link]:focus-visible)]:ring-2", T.border, T.within)}>
      <div className="relative">
        <CardLink d={d} onLogs={onLogs} />
        <div className={cx(PASS_THROUGH, "m-1")}>
          <CardRow
            d={d}
            className={T.bg}
            badge={
              <>
                {crashed ? (
                  <StatusBadge status="crashed" />
                ) : stopped ? (
                  <StatusBadge status="removed" label={t("중지됨")} />
                ) : sleeping ? (
                  <StatusBadge status="sleeping" />
                ) : (
                  <StatusBadge status="live" />
                )}
                {exited > 0 && (
                  <span
                    className="ml-1 rounded-full bg-red-50 px-1 py-px text-xs font-medium tabular-nums text-red-600"
                    title={t("레플리카 {total}개 중 {exited}개가 종료됨", { total: d.replicas.length, exited })}
                  >
                    {exited}/{d.replicas.length}
                  </span>
                )}
              </>
            }
            actions={
              <>
                <DeploymentMenu {...p} ring={T.ring} />
                <LogsButton tone={tone} onClick={onLogs} />
                {crashed && (
                  <button
                    type="button"
                    {...lock(canEdit)}
                    onClick={() =>
                      confirm({
                        title: t("재시작"),
                        message: t("현재 배포의 컨테이너 {count}개를 다시 시작합니다.", { count: inst.replicas }),
                        confirmLabel: t("재시작"),
                        run: actions.restart,
                      })
                    }
                    className={cx(LOGS_BTN, T.btn)}
                  >
                    {t("재시작")}
                  </button>
                )}
              </>
            }
          />
        </div>
      </div>
      <div className="mx-1 mb-1 rounded-md">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          aria-controls={regionId}
          className={cx(
            "group flex w-full cursor-pointer items-center justify-between gap-x-4 rounded-md py-2 pl-4 pr-3 focus-visible:outline-none focus-visible:ring-2 sm:pl-24",
            T.bg,
            T.ring,
          )}
        >
          <div className={cx("flex min-w-0 items-center gap-x-6 text-sm", T.text)}>
            <span className="icon-container icon-sm shrink-0 text-lg text-current">
              {crashed ? <XCircle /> : stopped ? <PauseCircle /> : sleeping ? <Moon /> : <CheckCircle2 />}
            </span>
            <p className="min-w-0 truncate text-left">
              {crashed
                ? t("컨테이너가 계속 종료됩니다")
                : stopped
                  ? t("서비스가 중지되었습니다")
                  : sleeping
                    ? t("슬립 중 · 요청이 들어오면 깨어납니다 ({minutes}분 동안 요청 없음)", { minutes: Math.round((inst.sleep_after_s ?? 600) / 60) })
                    : t("배포 성공")}
            </p>
          </div>
          <span className={cx("icon-container icon-sm shrink-0 text-lg text-foreground transition-transform", open ? "rotate-180" : "rotate-0")}>
            <ChevronDown aria-hidden />
          </span>
        </button>
        {open && (
          <div id={regionId} role="region" aria-label={t("활성 배포 정보")} className="mt-1 animate-pop-in space-y-3 overflow-hidden rounded-md bg-background px-4 py-3 text-sm dark:bg-secondaryBg">
            {d.replicas.length > 0 && <ReplicaChips replicas={d.replicas} />}
            {d.error && <div className="whitespace-pre-wrap break-words font-mono text-xs text-red-600">{d.error}</div>}
            <dl className="grid grid-cols-[96px_minmax(0,1fr)] gap-x-4 gap-y-1.5 sm:grid-cols-[110px_minmax(0,1fr)]">
              <dt className="text-gray-500">{t("배포 ID")}</dt>
              <dd className="truncate font-mono text-xs leading-5 text-gray-600">{d.id}</dd>
              <dt className="text-gray-500">{t("이미지")}</dt>
              <dd className="truncate font-mono text-xs leading-5 text-gray-600" title={d.image ?? undefined}>
                {d.image ?? "—"}
              </dd>
              {d.branch && (
                <>
                  <dt className="text-gray-500">{t("브랜치")}</dt>
                  <dd className="truncate font-mono text-xs leading-5 text-gray-600">{d.branch}</dd>
                </>
              )}
              <dt className="text-gray-500">{t("완료")}</dt>
              <dd className="tabular-nums text-gray-600">{fmtTime(d.finished_at)}</dd>
            </dl>
          </div>
        )}
      </div>
    </div>
  );
}

function ProgressCard(p: RowProps) {
  const { d, inst, onLogs } = p;
  useTicker(true);
  const tone = cardTone(d.status);
  const T = TONES[tone];
  return (
    <div className={cx("relative flex select-none flex-col rounded-lg border [&:has([data-card-link]:focus-visible)]:ring-2", T.border, T.within)}>
      <div className="relative">
        <CardLink d={d} onLogs={onLogs} />
        <div className={cx(PASS_THROUGH, "m-1")}>
          <CardRow
            d={d}
            className={T.bg}
            badge={<StatusBadge status={d.status} />}
            actions={
              <>
                <DeploymentMenu {...p} ring={T.ring} />
                <LogsButton tone={tone} onClick={onLogs} />
                {d.started_at && (
                  <span className="shrink-0 text-sm tabular-nums text-gray-500" title={t("경과 시간")} aria-label={t("경과 {duration}", { duration: fmtDuration(d.started_at, null) })}>
                    {fmtClock(d.started_at)}
                  </span>
                )}
              </>
            }
          />
        </div>
      </div>
      {d.status !== "waiting" && d.status !== "queued" ? (
        <div className={cx("mx-1 mb-1 overflow-x-auto rounded-md py-3 pl-4 pr-5 sm:pl-24", T.bg)}>
          <Stepper steps={stepsFor(inst, d)} status={d.status} />
        </div>
      ) : (
        <div className={cx("mx-1 mb-1 rounded-md py-2 pl-4 pr-5 text-sm sm:pl-24", T.bg, T.text)}>
          {d.status === "waiting" ? t("커밋의 CI가 끝나면 배포를 시작합니다") : t("앞선 배포가 끝나면 시작합니다")}
        </div>
      )}
    </div>
  );
}

function HistoryRow(p: RowProps) {
  const { d, onLogs } = p;
  const skipped = d.status === "skipped";
  const tone = cardTone(d.status);
  const T = TONES[tone];
  return (
    <div className={cx("group relative select-none rounded-md border", tone === "gray" ? "border-gray-200" : T.border)}>
      <CardLink d={d} onLogs={onLogs} className={cx("rounded-md focus-visible:ring-2", T.ring)} />
      <div className={cx(PASS_THROUGH, "m-1")}>
        <CardRow
          d={d}
          className={T.bg}
          badge={<StatusBadge status={d.status} />}
          note={
            skipped ? (
              <p className="flex items-center gap-1.5 text-xs text-gray-500">
                <SkipForward className="size-3.5 shrink-0" aria-hidden /> {t("감시 경로에 해당하는 변경이 없어 배포하지 않았습니다")}
              </p>
            ) : d.error ? (
              <p className="truncate font-mono text-xs text-red-600" title={d.error}>
                {d.error}
              </p>
            ) : null
          }
          actions={
            <>
              <DeploymentMenu {...p} ring={T.ring} />
              <div className="flex flex-row-reverse gap-x-3 opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100">
                <LogsButton tone={tone} onClick={onLogs} />
              </div>
            </>
          }
        />
      </div>
    </div>
  );
}

/** Loading placeholder shaped like the real list (active card + history rows). */
function DeploymentsSkeleton() {
  const row = (k: number, op: string) => (
    <div key={k} className={cx("flex items-center gap-4 rounded-md bg-gray-100 px-3 py-4", op)}>
      <div className="h-6 w-14 animate-pulse rounded-md bg-gray-200" />
      <div className="size-9 animate-pulse rounded-full bg-gray-200" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="h-4 w-1/2 animate-pulse rounded bg-gray-200" />
        <div className="h-3.5 w-1/3 animate-pulse rounded bg-gray-200" />
      </div>
    </div>
  );
  return (
    <div role="status" aria-label={t("배포 기록 불러오는 중")} className="flex flex-col gap-y-2">
      <div className="rounded-lg border border-gray-200 p-1">{row(0, "")}</div>
      <div className="mt-4 h-4 w-16 animate-pulse rounded bg-gray-100" />
      <div className="mt-2 flex flex-col gap-y-2">{[1, 2, 3].map((k) => row(k, k === 3 ? "opacity-50" : k === 2 ? "opacity-75" : ""))}</div>
    </div>
  );
}

/* ── Cron ────────────────────────────────────────────────────────────── */
function CronSection({ inst, actions, canEdit }: { inst: ServiceInstance; actions: ServiceActions; canEdit: boolean }) {
  const runs = useCronRuns(inst.current_deployment_id);
  const [busy, setBusy] = useState(false);
  const next = inst.cron_schedule ? nextRuns(inst.cron_schedule, 1)[0] : null;
  useTicker(!!runs.data?.some((r) => r.status === "running"));
  return (
    <div className="rounded-lg border border-gray-200">
      <div className="flex flex-wrap items-center gap-3 px-4 py-3.5">
        <span className="flex size-8 items-center justify-center rounded-full bg-pink-100 text-pink-600">
          <Clock className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="text-base font-medium">{describeCron(inst.cron_schedule ?? "")}</div>
          <div className="text-sm text-gray-500">
            <span className="font-mono text-xs">{inst.cron_schedule}</span>
            {next && (
              <>
                {" "}
                {tr("· 다음 실행 {until}", { until: <span className="tabular-nums" title={fmtTime(next.toISOString())}>{fmtUntil(next)}</span> })}
              </>
            )}
          </div>
        </div>
        <Button
          size="sm"
          disabled={!inst.current_deployment_id || busy}
          loading={busy}
          {...lock(canEdit)}
          onClick={async () => {
            setBusy(true);
            await actions.runCron();
            await runs.refetch();
            setBusy(false);
          }}
        >
          <Play /> {t("지금 실행")}
        </Button>
      </div>
      <div className="border-t border-gray-200">
        <div className="px-4 pb-1 pt-3 text-xs font-medium uppercase text-gray-500">{t("크론 실행 기록")}</div>
        {runs.isPending && inst.current_deployment_id ? (
          <div className="flex justify-center py-4">
            <Spinner />
          </div>
        ) : !runs.data?.length ? (
          <div className="px-4 pb-4 pt-1 text-sm text-gray-500">{t("아직 실행 기록이 없습니다.")}</div>
        ) : (
          <ul className="pb-2">
            {runs.data.slice(0, 10).map((r) => (
              <li key={r.id} className="flex items-center gap-3 px-4 py-2 text-sm">
                {r.status === "running" ? (
                  <Loader2 className="size-4 shrink-0 animate-spin text-blue-600" />
                ) : r.status === "succeeded" ? (
                  <CheckCircle2 className="size-4 shrink-0 text-success" />
                ) : (
                  <XCircle className="size-4 shrink-0 text-danger" />
                )}
                <span className="w-[150px] shrink-0 tabular-nums text-foreground/90" title={fmtTime(r.started_at)}>
                  {fmtRelative(r.started_at)}
                </span>
                <span className="text-gray-500">{r.status === "running" ? t("실행 중") : r.status === "succeeded" ? t("성공") : t("실패")}</span>
                <span className="ml-auto tabular-nums text-gray-500">{fmtDuration(r.started_at, r.finished_at)}</span>
                <span className={cx("w-[64px] text-right font-mono text-xs tabular-nums", r.exit_code ? "text-danger" : "text-gray-400")}>
                  {r.exit_code === null ? "" : `exit ${r.exit_code}`}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
/* ── Tab ─────────────────────────────────────────────────────────────── */

const INFO_LINK =
  "cursor-pointer text-nowrap text-sm hover:underline focus-visible:rounded-sm focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-pink-500 focus-visible:ring-0";

const HIDE_SKIPPED_KEY = "aven.deployments.hideSkipped";
const LEGACY_HIDE_SKIPPED_KEY = "keel.deployments.hideSkipped";
const HISTORY_PAGE = 20;

function useHideSkipped() {
  const [hide, setHide] = useState(false);
  useEffect(() => {
    try {
      setHide(readStorage(HIDE_SKIPPED_KEY, LEGACY_HIDE_SKIPPED_KEY) === "1");
    } catch {}
  }, []);
  const toggle = () =>
    setHide((v) => {
      try {
        writeStorage(HIDE_SKIPPED_KEY, v ? "0" : "1");
      } catch {}
      return !v;
    });
  return [hide, toggle] as const;
}

export function DeploymentsTab({
  svc,
  env,
  actions,
  onOpenDep,
  onTab,
}: {
  svc: CanvasService;
  env: Environment;
  actions: ServiceActions;
  onOpenDep: (id: string) => void;
  onTab: (tab: PanelTab) => void;
}) {
  const inst = svc.instance;
  const deps = useDeployments(svc.id, env.id);
  const { staged: local } = useStaged(svc.id, env.id);
  const pend = pendingSummary(inst, local);
  const staged = pend.count;
  const { confirm, node } = useConfirm();
  const [historyOpen, setHistoryOpen] = useState(true);
  const [hideSkipped, toggleSkipped] = useHideSkipped();
  const [shown, setShown] = useState(HISTORY_PAGE);
  const [deploying, setDeploying] = useState(false);

  const list = deps.data ?? [];
  const active = list.find((d) => d.id === inst.current_deployment_id);
  const progress = list.filter((d) => !isTerminal(d.status));
  const past = list.filter((d) => d !== active && !progress.includes(d));
  const skippedCount = past.filter((d) => d.status === "skipped").length;
  const history = hideSkipped ? past.filter((d) => d.status !== "skipped") : past;
  const canEdit = useCaps(svc.project_id).canEdit;
  const props = (d: Deployment): RowProps => ({ d, inst, actions, confirm, onLogs: () => onOpenDep(d.id), canEdit, envName: env.name });
  const domain = inst.domains[0]?.host;
  const nodes = useNodes();
  const nodeName = nodes.data?.find((n) => n.id === inst.node_id)?.name;
  const noSource = svc.source_type === "image" && !inst.image;

  const deploy = async () => {
    setDeploying(true);
    try {
      await actions.deploy();
    } finally {
      setDeploying(false);
    }
  };
  const deployAction = noSource ? (
    <Button variant="outline" onClick={() => onTab("settings")}>
      {t("설정에서 소스 연결")}
    </Button>
  ) : (
    <Button onClick={deploy} loading={deploying} {...lock(canEdit)}>
      {!deploying && <Rocket />} {list.length ? t("새로 배포") : t("첫 배포 시작")}
    </Button>
  );

  return (
    <div className="flex flex-col gap-y-4 pb-8">
      {/* aven-server 자신(self 서비스)은 도메인·노드·레플리카가 Aven 밖의 서버 설정이라 이 줄을 숨긴다. */}
      {!inst.self && (
      <div className="flex flex-col gap-y-4 sm:flex-row sm:items-center sm:justify-between sm:gap-0">
        <div className="flex min-w-0 items-center gap-x-2 text-sm">
          {domain ? (
            <>
              <span className="icon-container icon-sm shrink-0 text-lg text-green-300">
                <Globe aria-hidden />
              </span>
              <a
                href={`https://${domain}`}
                target="_blank"
                rel="noopener noreferrer"
                title={inst.domains.length > 1 ? t("도메인 {count}개 · 설정에서 모두 보기", { count: inst.domains.length }) : undefined}
                className="max-w-80 truncate text-foreground hover:underline focus-visible:rounded-sm focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-pink-500 focus-visible:ring-0 lg:max-w-none"
              >
                {domain}
              </a>
            </>
          ) : (
            <>
              <span className="icon-container icon-sm shrink-0 text-lg text-gray-300">
                <Lock aria-hidden />
              </span>
              <span className="truncate text-gray-500" title={t("비공개 도메인")}>
                {inst.private_domain}
              </span>
            </>
          )}
        </div>
        <div className="flex items-center gap-4">
          {nodeName && (
            <div className="flex min-w-0 items-center gap-x-2 text-gray-300">
              <span className="icon-container icon-sm shrink-0 text-lg text-current">
                <MapPin aria-hidden />
              </span>
              <button type="button" title={nodeName} onClick={() => onTab("settings")} className={INFO_LINK}>
                {nodeName}
              </button>
            </div>
          )}
          <div className="flex items-center gap-x-2 text-gray-300">
            <span className="icon-container icon-sm -rotate-90 text-lg text-current">
              <Layers aria-hidden />
            </span>
            <button type="button" onClick={() => onTab("settings")} className={INFO_LINK}>
              {t("레플리카 {count}개", { count: inst.current_replicas ?? inst.replicas })}
            </button>
          </div>
        </div>
      </div>
      )}

      {staged > 0 && (
        <div className="flex items-center gap-3 rounded-lg border border-pink-200 bg-pink-50 px-4 py-3 text-sm">
          <span className="size-2 shrink-0 rounded-full bg-pink-600" aria-hidden />
          <span className="flex-1">
            {pend.local.length
              ? tr("적용하지 않은 변경 사항 {count}개가 있습니다. 캔버스 위쪽의 변경 사항 배너에서 배포하면 반영됩니다.", { count: <b className="tabular-nums text-pink-600">{t("{count}개", { count: staged })}</b> })
              : tr("배포하지 않은 변경 사항 {count}개가 있습니다. 캔버스 위쪽의 변경 사항 배너에서 배포하면 반영됩니다.", { count: <b className="tabular-nums text-pink-600">{t("{count}개", { count: staged })}</b> })}
          </span>
        </div>
      )}

      {actions.error ? <ErrorBox error={actions.error} /> : null}

      {inst.cron_schedule && <CronSection inst={inst} actions={actions} canEdit={canEdit} />}

      {deps.error && !deps.data ? (
        <div role="alert" className="flex flex-col items-center gap-3 rounded-lg border border-red-200 bg-red-50 px-6 py-8 text-center">
          <XCircle className="size-5 text-red-500" aria-hidden />
          <div className="text-sm font-semibold text-foreground">{t("배포 기록을 불러오지 못했습니다")}</div>
          <ErrorBox error={deps.error} className="max-w-[460px] text-left" />
          <Button variant="outline" onClick={() => deps.refetch()} loading={deps.isFetching}>
            {!deps.isFetching && <RefreshCw />} {t("다시 시도")}
          </Button>
        </div>
      ) : deps.isPending ? (
        <DeploymentsSkeleton />
      ) : list.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-gray-200 px-6 py-12 text-center">
          <span className="mb-1 flex size-10 items-center justify-center rounded-full bg-gray-100 text-gray-500">
            <Rocket className="size-5" aria-hidden />
          </span>
          <div className="text-sm font-semibold text-foreground">{t("아직 배포가 없습니다")}</div>
          <p className="max-w-[420px] text-sm text-gray-500">
            {noSource
              ? t("배포할 이미지나 저장소가 아직 연결되지 않았습니다. 설정에서 소스를 연결하면 배포할 수 있습니다.")
              : svc.source_type === "repo"
                ? t("{branch} 브랜치에 푸시하거나 지금 배포를 시작하면 여기에 기록이 쌓입니다.", { branch: String(inst.branch) })
                : t("지금 배포를 시작하면 여기에 기록이 쌓입니다.")}
          </p>
          <div className="mt-3">{deployAction}</div>
        </div>
      ) : (
        <>
          <div className="flex flex-col gap-y-2">
            {progress.map((d) => (
              <ProgressCard key={d.id} {...props(d)} />
            ))}
            {active ? (
              <ActiveCard {...props(active)} />
            ) : (
              progress.length === 0 && (
                <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-gray-200 px-6 py-8 text-center sm:flex-row sm:justify-between sm:text-left">
                  <div className="min-w-0">
                    <div className="text-sm font-semibold text-foreground">{t("활성 배포가 없습니다")}</div>
                    <p className="text-sm text-gray-500">
                      {inst.status === "offline" ? t("서비스가 중지되어 있습니다. 다시 배포하거나 기록에서 롤백하세요.") : t("아래 기록에서 롤백하거나 새로 배포하세요.")}
                    </p>
                  </div>
                  <div className="mt-2 shrink-0 sm:mt-0">{deployAction}</div>
                </div>
              )
            )}
          </div>

          {past.length > 0 && (
            <section aria-labelledby="dep-history" className="mb-8">
              <div className="flex items-center justify-between">
                <button
                  type="button"
                  onClick={() => setHistoryOpen((o) => !o)}
                  aria-expanded={historyOpen}
                  aria-controls="dep-history-list"
                  className="group flex cursor-pointer items-center gap-x-2 rounded text-foreground focus-visible:rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-pink-500 focus-visible:ring-0"
                >
                  <span className={cx("icon-container icon-md text-2xl text-current transition-transform", historyOpen ? "rotate-90" : "rotate-0")}>
                    <ChevronRight aria-hidden />
                  </span>
                  <h2 id="dep-history" className="text-xs font-medium uppercase">
                    {t("기록")}
                  </h2>
                </button>
                <button
                  type="button"
                  onClick={toggleSkipped}
                  aria-pressed={hideSkipped}
                  title={skippedCount ? t("건너뛴 배포 {count}개", { count: skippedCount }) : undefined}
                  className="cursor-pointer text-xs text-gray-500 transition-colors hover:text-foreground focus-visible:rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-pink-500"
                >
                  {hideSkipped ? t("건너뜀 표시") : t("건너뜀 숨기기")}
                </button>
              </div>
              {historyOpen && (
                <div id="dep-history-list" className="flex flex-col gap-y-2 pt-4">
                  {history.slice(0, shown).map((d) => (
                    <HistoryRow key={d.id} {...props(d)} />
                  ))}
                  {history.length === 0 && (
                    <p className="rounded-md border border-dashed border-gray-200 px-4 py-4 text-center text-sm text-gray-500">
                      {t("표시할 기록이 없습니다. 건너뛴 배포 {count}개를 숨겼습니다.", { count: skippedCount })}
                    </p>
                  )}
                  {history.length > shown && (
                    <Button variant="outline" className="mx-auto mt-2" onClick={() => setShown((n) => n + HISTORY_PAGE)}>
                      {t("더 보기")} <span className="tabular-nums text-gray-500">({history.length - shown})</span>
                    </Button>
                  )}
                </div>
              )}
            </section>
          )}
        </>
      )}
      {node}
    </div>
  );
}
