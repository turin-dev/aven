"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
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
import { dateLocale, t } from "@/lib/i18n";
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
  const names = useMemo(() => new Map(services.map((s) => [s.id, s.name])), [services]);
  const list = useMemo(() => events.data ?? [], [events.data]);
  // Re-render twice a minute so "2분 전" labels keep up while the panel stays open.
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => tick((n) => n + 1), 30_000);
    return () => clearInterval(timer);
  }, []);
  const days = useMemo(() => {
    const out: { key: string; label: string; items: AvenEvent[] }[] = [];
    for (const ev of list) {
      const d = new Date(ev.created_at);
      const key = isNaN(d.getTime()) ? "?" : d.toDateString();
      const last = out[out.length - 1];
      if (last?.key === key) last.items.push(ev);
      else out.push({ key, label: dayLabel(d), items: [ev] });
    }
    return out;
  }, [list]);

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
      <div className="-mx-2 mt-2 min-h-0 flex-1 overflow-y-auto px-2 pb-2" aria-live="polite" aria-busy={events.isPending}>
        {events.isPending ? (
          <ul aria-label={t("불러오는 중…")} className="space-y-1 px-2 pt-1">
            {[0, 1, 2, 3].map((i) => (
              <li key={i} className="flex animate-soft-pulse items-start gap-3 py-2.5" aria-hidden>
                <span className="size-7 shrink-0 rounded-md bg-gray-100" />
                <span className="flex-1 space-y-1.5 pt-0.5">
                  <span className="block h-3 w-1/3 rounded bg-gray-100" />
                  <span className="block h-3 w-4/5 rounded bg-gray-100" />
                </span>
              </li>
            ))}
          </ul>
        ) : events.isError ? (
          <p className="px-2 py-6 text-center text-sm text-danger">{t("활동을 불러오지 못했습니다")}</p>
        ) : list.length === 0 ? (
          <div className="flex flex-col items-center px-4 py-10 text-center">
            <span className="flex size-9 items-center justify-center rounded-lg border border-dashed border-gray-300">
              <Activity className="size-4 text-gray-400" aria-hidden />
            </span>
            <p className="mt-3 text-sm font-medium">{t("아직 활동이 없습니다")}</p>
            <p className="mt-1 text-xs leading-relaxed text-gray-500">{t("배포, 설정 변경, 재시작 같은 일이 생기면 여기에 바로 나타납니다.")}</p>
          </div>
        ) : (
          days.map((day) => (
            <section key={day.key} aria-label={day.label}>
              <h3 className="sticky top-0 z-[1] bg-background/95 px-2 pb-1 pt-3 text-xs font-medium text-gray-500 backdrop-blur-sm">{day.label}</h3>
              <ol>
                {day.items.map((ev) => {
                  const name = ev.service_id ? names.get(ev.service_id) : null;
                  const label = name ?? ev.service_name ?? (ev.service_id ? t("삭제된 서비스") : t("프로젝트"));
                  return (
                    <li key={ev.id} className="animate-fade-in">
                      <button
                        type="button"
                        disabled={!name}
                        onClick={() => onOpen(ev)}
                        aria-label={name ? t("{name} 열기: {message}", { name, message: ev.message }) : undefined}
                        className="group flex w-full items-start gap-3 rounded-md px-2 py-2.5 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500 enabled:cursor-pointer enabled:hover:bg-gray-100"
                      >
                        <span className="flex size-7 shrink-0 items-center justify-center rounded-md border border-gray-100 bg-secondaryBg [&_svg]:!size-4">
                          {eventIcon(ev.kind)}
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="flex items-baseline gap-2">
                            <span className={cx("min-w-0 truncate text-sm font-semibold", !name && "text-gray-600")}>{label}</span>
                            <span className="ml-auto shrink-0 text-xs tabular-nums text-gray-500" title={fmtTime(ev.created_at)}>
                              {fmtRelative(ev.created_at)}
                            </span>
                          </span>
                          <span className="block break-words text-sm leading-snug text-foreground/80">{ev.message}</span>
                          {ev.actor && <span className="mt-0.5 block truncate text-xs text-gray-500">{t("{actor}님이 실행", { actor: ev.actor })}</span>}
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ol>
            </section>
          ))
        )}
      </div>
    </section>
  );
}

/** "오늘", "어제" or a short date for an activity day header. */
function dayLabel(d: Date): string {
  if (isNaN(d.getTime())) return "";
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return t("오늘");
  if (d.toDateString() === yesterday.toDateString()) return t("어제");
  return d.toLocaleDateString(dateLocale(), { month: "long", day: "numeric", weekday: "short", ...(d.getFullYear() !== today.getFullYear() ? { year: "numeric" } : {}) });
}
