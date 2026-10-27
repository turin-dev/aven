"use client";

import type { ReactNode } from "react";
import {
  Activity,
  AlertTriangle,
  Braces,
  CheckCircle2,
  Clock,
  Moon,
  Server,
  Sunrise,
  Globe,
  History,
  Network,
  Pencil,
  HardDrive,
  Layers,
  PauseCircle,
  Plus,
  Rocket,
  RotateCw,
  SkipForward,
  Trash2,
  X,
  XCircle,
} from "lucide-react";
import type { AvenEvent, Service } from "@/lib/api";
import { useEvents } from "@/lib/queries";
import { fmtRelative, fmtTime } from "@/lib/format";
import { t } from "@/lib/i18n";
import { IconButton, cx } from "../ui";

export function eventIcon(kind: string): ReactNode {
  const c = "size-[18px] shrink-0";
  if (kind === "deploy.live" || kind === "backup.done" || kind === "backup.restored" || kind === "cron.succeeded" || kind === "template.deployed")
    return <CheckCircle2 className={cx(c, "text-success")} />;
  if (kind === "service.slept") return <Moon className={cx(c, "text-yellow-600")} />;
  if (kind === "service.woke") return <Sunrise className={cx(c, "text-pink-600")} />;
  if (kind.startsWith("node.")) return <Server className={cx(c, "text-gray-600")} />;
  if (kind === "deploy.rollback") return <History className={cx(c, "text-pink-600")} />;
  if (kind === "domain.added") return <Globe className={cx(c, "text-gray-600")} />;
  if (kind === "tcp_proxy.enabled") return <Network className={cx(c, "text-gray-600")} />;
  if (kind === "cron.started") return <Clock className={cx(c, "text-pink-600")} />;
  if (kind === "service.restarted") return <RotateCw className={cx(c, "text-yellow-600")} />;
  if (kind === "service.updated" || kind === "project.created") return <Pencil className={cx(c, "text-gray-600")} />;
  if (kind === "deploy.failed" || kind.endsWith(".failed") || kind.endsWith("_failed")) return <XCircle className={cx(c, "text-danger")} />;
  if (kind === "deploy.crashed") return <AlertTriangle className={cx(c, "text-danger")} />;
  if (kind === "deploy.started") return <Rocket className={cx(c, "text-pink-600")} />;
  if (kind === "deploy.skipped") return <SkipForward className={cx(c, "text-gray-400")} />;
  if (kind === "deploy.restarted") return <RotateCw className={cx(c, "text-pink-600")} />;
  if (kind === "variables.updated") return <Braces className={cx(c, "text-pink-600")} />;
  if (kind.startsWith("volume.") || kind.startsWith("backup.")) return <HardDrive className={cx(c, "text-gray-600")} />;
  if (kind.startsWith("environment.")) return <Layers className={cx(c, "text-gray-600")} />;
  if (kind === "service.created") return <Plus className={cx(c, "text-pink-600")} />;
  if (kind === "service.deleted") return <Trash2 className={cx(c, "text-gray-400")} />;
  if (kind === "service.stopped") return <PauseCircle className={cx(c, "text-gray-400")} />;
  return <Activity className={cx(c, "text-gray-600")} />;
}

/**
 * Activity feed body. The shell hosts it in a floating side panel modeled on Railway's dashboard (§4.1 B, `w-activity` 320px);
 * this renders the inside: `h-full px-2 pt-3`.
 */
export function ActivityPanel({
  pid,
  envId,
  services,
  onOpen,
  onClose,
}: {
  pid: string;
  envId: string;
  services: Pick<Service, "id" | "name">[];
  onOpen: (ev: AvenEvent) => void;
  onClose?: () => void;
}) {
  const events = useEvents(pid, envId, 30);
  const names = new Map(services.map((s) => [s.id, s.name]));
  const list = events.data ?? [];

  return (
    <section aria-label={t("활동")} className="flex h-full min-h-0 flex-col px-2 pt-3">
      <header className="flex h-[34px] shrink-0 items-center gap-2.5 pl-2">
        <Activity className="size-4 text-pink-500" />
        <h2 className="flex-1 text-base font-medium">{t("활동")}</h2>
        {onClose && (
          <IconButton label={t("활동 닫기")} onClick={onClose}>
            <X />
          </IconButton>
        )}
      </header>
      <ol className="-mx-2 mt-2 min-h-0 flex-1 overflow-y-auto px-2 pb-2" aria-live="polite">
        {events.isPending ? (
          <li className="px-2 py-6 text-center text-sm text-gray-500">{t("불러오는 중…")}</li>
        ) : list.length === 0 ? (
          <li className="px-2 py-6 text-center text-sm text-gray-500">{t("아직 활동이 없습니다")}</li>
        ) : (
          list.map((ev) => {
            const name = ev.service_id ? names.get(ev.service_id) : null;
            const label = name ?? ev.service_name ?? (ev.service_id ? t("삭제된 서비스") : t("프로젝트"));
            return (
              <li key={ev.id} className="animate-fade-in">
                <button
                  type="button"
                  disabled={!name}
                  onClick={() => onOpen(ev)}
                  className="flex w-full items-start gap-3 rounded-md px-2 py-2.5 text-left enabled:cursor-pointer enabled:hover:bg-gray-100"
                >
                  <span className="mt-0.5">{eventIcon(ev.kind)}</span>
                  <span className="min-w-0 flex-1">
                    <span className={cx("block truncate text-sm font-semibold", !name && "text-gray-600")}>{label}</span>
                    <span className="block text-sm text-foreground/90">{ev.message}</span>
                    <span className="mt-0.5 block text-xs tabular-nums text-gray-500" title={fmtTime(ev.created_at)}>
                      {fmtRelative(ev.created_at)}
                    </span>
                  </span>
                </button>
              </li>
            );
          })
        )}
      </ol>
    </section>
  );
}
