"use client";

import Link from "next/link";
import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { Bell, BellOff, CheckCheck, SlidersHorizontal } from "lucide-react";
import { api, type UserNotification } from "@/lib/api";
import { qk, useNotifications, useProjects } from "@/lib/queries";
import { canvasHref } from "@/lib/nav";
import { useInboxStream } from "@/lib/useInboxStream";
import { dateLocale, t } from "@/lib/i18n";
import { Spinner, cx, useEscape } from "./ui";

const ATTENTION = /(\.failed|_failed|\.crashed|alert\.firing|limit_exceeded|threshold_reached)$/;

export function relTime(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return t("방금");
  if (s < 3600) return t("{n}분 전", { n: Math.floor(s / 60) });
  if (s < 86400) return t("{n}시간 전", { n: Math.floor(s / 3600) });
  return t("{n}일 전", { n: Math.floor(s / 86400) });
}

/** Canvas link of a notification: its project, environment (by id, see useWorkspace) and service. */
export function notificationHref(n: Pick<UserNotification, "project_id" | "environment_id" | "service_id">): string {
  return canvasHref({ svc: n.service_id }, { project: n.project_id, env: n.environment_id });
}

/**
 * v1.5: per-user inbox (GET /notifications) behind the bell. Live via the shared inbox SSE stream; polled
 * while the stream is down. The panel is portaled and pinned to the viewport's right edge so it never
 * runs off a narrow screen, whatever sits to the right of the bell.
 */
export function InboxMenu({ className }: { className?: string }) {
  const router = useRouter();
  const qc = useQueryClient();
  const inbox = useNotifications(useInboxStream());
  const projects = useProjects();
  const [open, setOpen] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const id = useId();
  const list = inbox.data?.notifications ?? [];
  const unread = inbox.data?.unread ?? 0;
  const projectName = (pid: string) => projects.data?.find((p) => p.id === pid)?.name ?? t("프로젝트");

  const close = (refocus = false) => {
    setOpen(false);
    if (refocus) button.current?.focus();
  };
  useEscape(() => close(true), open);
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => {
      const target = e.target as Node;
      if (!panel.current?.contains(target) && !button.current?.contains(target)) setOpen(false);
    };
    document.addEventListener("mousedown", h);
    panel.current?.querySelector<HTMLElement>("[data-inbox-item], a, button")?.focus();
    return () => document.removeEventListener("mousedown", h);
  }, [open]);

  const markRead = async (input: { ids: string[] } | { all: true }) => {
    // Optimistic: the dot and the badge go away at once; the refetch below settles the real state.
    qc.setQueryData<{ notifications: UserNotification[]; unread: number }>(qk.notifications, (d) => {
      if (!d) return d;
      const now = new Date().toISOString();
      const hit = (n: UserNotification) => !n.read_at && ("all" in input || input.ids.includes(n.id));
      const changed = d.notifications.filter(hit).length;
      return { notifications: d.notifications.map((n) => (hit(n) ? { ...n, read_at: now } : n)), unread: "all" in input ? 0 : Math.max(0, d.unread - changed) };
    });
    try {
      await api.readNotifications(input);
    } finally {
      qc.invalidateQueries({ queryKey: qk.notifications });
    }
  };

  // Arrow keys move between items, like a menu.
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const items = [...(panel.current?.querySelectorAll<HTMLElement>("[data-inbox-item]") ?? [])];
    if (!items.length) return;
    e.preventDefault();
    const i = items.indexOf(document.activeElement as HTMLElement);
    items[(i + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length].focus();
  };

  return (
    <>
      <button
        ref={button}
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-controls={open ? id : undefined}
        aria-label={unread ? t("알림 (읽지 않음 {count}개)", { count: unread }) : t("알림")}
        title={t("알림")}
        className={cx(
          "relative flex min-h-[28px] cursor-pointer items-center gap-1 rounded-md border border-transparent p-1.5 text-neutral-secondary hover:bg-gray-100 hover:text-gray-900 focus-visible:ring-2 focus-visible:ring-pink-500",
          open && "bg-gray-100",
          className,
        )}
      >
        <span className="relative">
          <span className="icon-container icon-sm text-lg text-current">
            <Bell />
          </span>
          {unread > 0 && (
            <span
              aria-hidden="true"
              className="absolute -right-[7px] -top-[6px] flex h-[14px] min-w-[14px] items-center justify-center rounded-full bg-red-600 px-[3px] text-[10px] font-semibold leading-none text-white"
            >
              {unread > 99 ? "99+" : unread}
            </span>
          )}
        </span>
      </button>
      {open &&
        createPortal(
          <div
            ref={panel}
            id={id}
            role="dialog"
            aria-label={t("알림")}
            onKeyDown={onKeyDown}
            className="fixed right-2 top-[52px] z-[60] w-[min(379px,calc(100vw-16px))] animate-pop-in overflow-hidden rounded-md border border-black/10 bg-background shadow motion-reduce:animate-none dark:border-white/10"
          >
            <div className="flex items-center justify-between px-3 pb-2 pt-3">
              <span className="text-sm font-medium text-foreground">
                {t("알림")}
                {unread > 0 && <span className="ml-1.5 text-xs font-normal text-gray-500">{t("읽지 않음 {count}개", { count: unread })}</span>}
              </span>
              {unread > 0 && (
                <button type="button" className="flex cursor-pointer items-center gap-1 text-xs text-pink-500 hover:underline" onClick={() => markRead({ all: true })}>
                  <CheckCheck className="size-3.5" aria-hidden /> {t("모두 읽음")}
                </button>
              )}
            </div>
            {inbox.isPending ? (
              <div className="flex justify-center py-4">
                <Spinner />
              </div>
            ) : inbox.isError && !list.length ? (
              <div className="mx-3 mb-3 px-2 py-6 text-center text-sm text-gray-500">{t("알림을 불러오지 못했습니다.")}</div>
            ) : list.length === 0 ? (
              <div className="mx-3 mb-3 flex flex-col items-center gap-2 rounded border border-dashed border-gray-200 px-[10px] py-8 text-center text-sm text-gray-500">
                <BellOff className="size-5 text-gray-400" aria-hidden />
                {t("알림이 없습니다")}
              </div>
            ) : (
              <ul className="max-h-[min(420px,60dvh)] overflow-y-auto px-1 pb-1">
                {list.map((n) => (
                  <li key={n.id}>
                    <button
                      type="button"
                      data-inbox-item
                      onClick={() => {
                        close();
                        if (!n.read_at) void markRead({ ids: [n.id] });
                        router.push(notificationHref(n));
                      }}
                      className="flex w-full cursor-pointer items-start gap-2 rounded px-2 py-2 text-left hover:bg-neutral-surface-hover focus:bg-neutral-surface-hover focus:outline-none"
                    >
                      <span
                        aria-hidden
                        className={cx("mt-[5px] size-[6px] shrink-0 rounded-full", n.read_at ? "bg-transparent" : ATTENTION.test(n.kind) ? "bg-red-600" : "bg-pink-500")}
                      />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center justify-between gap-2">
                          <span className={cx("truncate text-xs", n.read_at ? "text-gray-500" : "font-medium text-foreground")}>
                            {projectName(n.project_id)} · <span className="font-mono">{n.kind}</span>
                          </span>
                          <time dateTime={n.created_at} title={new Date(n.created_at).toLocaleString(dateLocale())} className="shrink-0 text-xs text-gray-500">
                            {relTime(n.created_at)}
                          </time>
                        </span>
                        <span className={cx("line-clamp-2 text-xs", n.read_at ? "text-gray-400" : "text-gray-500")}>{n.message}</span>
                        {!n.read_at && <span className="sr-only">{t("읽지 않음")}</span>}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <Link
              href="/account/?section=notifications"
              onClick={() => close()}
              className="flex items-center gap-2 border-t border-gray-200 px-3 py-3 text-xs font-medium text-foreground hover:text-pink-600"
            >
              <SlidersHorizontal className="size-3.5" aria-hidden /> {t("환경설정 편집")}
            </Link>
          </div>,
          document.body,
        )}
    </>
  );
}
