import { AlertTriangle, CheckCircle2, CircleSlash, Clock, Loader2, Moon, PauseCircle, XCircle } from "lucide-react";
import type { ReactNode } from "react";
import type { DeploymentStatus, InstanceStatus } from "@/lib/api";
import { cx } from "./ui";
import { t } from "@/lib/i18n";

type S = InstanceStatus | DeploymentStatus;
export type Tone = "success" | "progress" | "warning" | "danger" | "neutral" | "accent" | "sleep";

export const STATUS_LABEL: Record<S, string> = {
  idle: t("대기"),
  deploying: t("배포 중"),
  online: t("온라인"),
  crashed: t("크래시"),
  failed: t("실패"),
  offline: t("오프라인"),
  sleeping: t("슬립"),
  scheduled: t("예약됨"),
  waiting: t("CI 대기"),
  queued: t("대기 중"),
  initializing: t("준비 중"),
  building: t("빌드 중"),
  healthchecking: t("헬스체크 중"),
  pre_deploy: t("사전 배포 명령"),
  live: t("활성|deployment"),
  cancelled: t("취소됨"),
  removed: t("제거됨"),
  skipped: t("건너뜀"),
};

const TONE: Record<S, Tone> = {
  idle: "neutral",
  deploying: "progress",
  online: "success",
  crashed: "danger",
  failed: "danger",
  offline: "neutral",
  sleeping: "sleep",
  scheduled: "accent",
  waiting: "accent",
  queued: "progress",
  initializing: "progress",
  building: "progress",
  healthchecking: "progress",
  pre_deploy: "progress",
  live: "success",
  cancelled: "neutral",
  removed: "neutral",
  skipped: "neutral",
};

export function statusTone(s: S): Tone {
  return TONE[s] ?? "neutral";
}

// -700 text on a -100 (light) / -200 (dark) fill: every pair is at least 5.5:1 (WCAG AA). The light theme used
// -500 text before, which put WARNING/SLEEP at 1.8:1 and SUCCESS at 3.1:1.
const BADGE: Record<Tone, string> = {
  success: "text-green-700 bg-green-100 dark:bg-green-200",
  progress: "text-blue-700 bg-blue-100 dark:bg-blue-200",
  warning: "text-yellow-700 bg-yellow-100 dark:bg-yellow-200",
  danger: "text-red-700 bg-red-100 dark:bg-red-200",
  neutral: "text-gray-700 bg-gray-100 dark:bg-gray-200",
  accent: "text-pink-700 bg-pink-100 dark:bg-pink-200",
  sleep: "text-yellow-700 bg-yellow-100 dark:bg-yellow-200",
};

/**
 * Tone for deployment UI (badge, pill, dot): QUEUED is gray and WAITING (CI / needs approval) is yellow (§1.6),
 * so a deployment shows the same colour in the list, the detail header and the activity feed.
 * Canvas text tones keep using `statusTone`.
 */
export function deploymentTone(status: S): Tone {
  if (status === "queued") return "neutral";
  if (status === "waiting") return "warning";
  return statusTone(status);
}

function badgeCls(status: S): string {
  return BADGE[deploymentTone(status)];
}

/** Tinted badge used in deployment rows. */
export function StatusBadge({ status, className, label }: { status: S; className?: string; label?: string }) {
  return (
    <span
      className={cx(
        "flex shrink-0 items-center justify-center whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium uppercase leading-[18px] sm:px-2 sm:py-1",
        badgeCls(status),
        className,
      )}
    >
      {label ?? STATUS_LABEL[status] ?? status}
    </span>
  );
}

export function ToneBadge({ tone, children, className }: { tone: Tone; children: ReactNode; className?: string }) {
  return (
    <span
      className={cx(
        "flex shrink-0 items-center justify-center whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium uppercase leading-[18px] sm:px-2 sm:py-1",
        BADGE[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

const PILL: Record<Tone, string> = {
  success: "border-green-300 bg-green-50 text-success",
  progress: "border-blue-200 bg-blue-50 text-blue-600",
  warning: "border-yellow-300 bg-yellow-50 text-yellow-600",
  danger: "border-red-200 bg-red-50 text-danger",
  neutral: "border-gray-200 bg-gray-50 text-gray-600",
  accent: "border-pink-300 bg-pink-50 text-pink-600",
  sleep: "border-yellow-300 bg-yellow-50 text-yellow-600",
};

/** Outlined round pill used in the deployment detail header. */
export function StatusPill({ status, className }: { status: S; className?: string }) {
  const tone = deploymentTone(status);
  return (
    <span
      className={cx(
        "inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full border px-3 text-sm font-medium",
        PILL[tone],
        className,
      )}
    >
      {(tone === "progress" || status === "queued") && <span aria-hidden="true" className="size-1.5 animate-soft-pulse rounded-full bg-current" />}
      {STATUS_LABEL[status] ?? status}
    </span>
  );
}

/** Status icon used on canvas cards and the activity feed. */
export function StatusIcon({ status, className }: { status: S; className?: string }) {
  const cls = cx("size-[18px] shrink-0", className);
  switch (status) {
    case "online":
      return (
        <span aria-hidden="true" className={cx("flex size-[14px] shrink-0 items-center justify-center rounded-full bg-green-600/15", className)}>
          <span className="size-1.5 rounded-full bg-green-600" />
        </span>
      );
    case "live":
      return <CheckCircle2 aria-hidden="true" className={cx(cls, "text-success")} />;
    case "crashed":
      return <AlertTriangle aria-hidden="true" className={cx(cls, "text-danger")} />;
    case "failed":
      return <XCircle aria-hidden="true" className={cx(cls, "text-danger")} />;
    case "sleeping":
      return <Moon aria-hidden="true" className={cx(cls, "text-yellow-600")} />;
    case "scheduled":
      return <Clock aria-hidden="true" className={cx(cls, "text-pink-600")} />;
    case "waiting":
      return <Clock aria-hidden="true" className={cx(cls, "text-yellow-600")} />;
    case "queued":
      return <Clock aria-hidden="true" className={cx(cls, "text-gray-500")} />;
    case "offline":
      return <PauseCircle aria-hidden="true" className={cx(cls, "text-gray-400")} />;
  }
  if (statusTone(status) === "progress") return <Loader2 aria-hidden="true" className={cx(cls, "animate-spin text-neutral-secondary")} />;
  return <CircleSlash aria-hidden="true" className={cx(cls, "text-gray-400")} />;
}

export function StatusDot({ status, className }: { status: S; className?: string }) {
  const tone = deploymentTone(status);
  return (
    <span
      aria-hidden="true"
      className={cx(
        "inline-block size-2 shrink-0 rounded-full",
        tone === "success" && "bg-green-600",
        (tone === "warning" || tone === "sleep") && "bg-yellow-600",
        tone === "progress" && "animate-pulse bg-blue-500",
        tone === "danger" && "bg-red-600",
        tone === "accent" && "bg-pink-600",
        tone === "neutral" && "bg-gray-400",
        className,
      )}
    />
  );
}

export const TRIGGER_LABEL: Record<string, string> = {
  manual: t("수동 배포"),
  redeploy: t("재배포"),
  rollback: t("롤백"),
  github: "GitHub",
  variables: t("변수 변경|trigger"),
  cli: "CLI",
  template: t("템플릿|item"),
  image_update: t("이미지 업데이트"),
};

export const TEXT_TONE: Record<Tone, string> = {
  success: "text-gray-600",
  progress: "text-gray-500",
  warning: "text-yellow-600",
  danger: "text-danger",
  neutral: "text-gray-500",
  accent: "text-pink-600",
  sleep: "text-yellow-600",
};
