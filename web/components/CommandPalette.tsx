"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { Building2, CornerDownLeft, FolderKanban, Languages, LayoutGrid, LogOut, Monitor, Moon, Plus, Search, Sparkles, Sun, UserRound } from "lucide-react";
import { api } from "@/lib/api";
import { useCanvas, useCaps, useMe } from "@/lib/queries";
import { SHELL_NAV, canvasHref, withCtx } from "@/lib/nav";
import { useTheme } from "@/lib/theme";
import type { Workspace } from "@/lib/workspace";
import { LOCALES, getLocale, setLocale, t } from "@/lib/i18n";
import { useKeyboardShortcuts } from "./KeyboardShortcuts";
import { ServiceIcon } from "./ServiceIcon";
import { openAvenAgent } from "./AgentPanel";
import { cx, useEscape } from "./ui";

const OPEN_EVENT = "aven:command-palette";
/** Opens the command palette from anywhere (top bar search button, other components). */
export function openCommandPalette() {
  window.dispatchEvent(new Event(OPEN_EVENT));
}

type Command = {
  id: string;
  label: string;
  group: string;
  icon: ReactNode;
  /** Extra words matched by the search (ids, kinds, English names). */
  keywords?: string;
  hint?: string;
  run: () => void;
};

/** Every query word must appear in label/keywords/group; earlier and prefix matches rank higher. */
export function scoreCommand(query: string, c: Pick<Command, "label" | "keywords" | "group">): number {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return 1;
  const label = c.label.toLowerCase();
  const hay = `${label} ${(c.keywords ?? "").toLowerCase()} ${c.group.toLowerCase()}`;
  let score = 0;
  for (const w of words) {
    const i = hay.indexOf(w);
    if (i < 0) return 0;
    score += label.startsWith(w) ? 4 : label.includes(w) ? 2 : 1;
  }
  return score;
}

/**
 * Cmd/Ctrl+K command palette: jump to a page, service, project or workspace and run common actions.
 * Mounted once by the shell and once by the projects dashboard. On the canvas, "Add service" opens the create palette.
 */
export function CommandPalette({ ws }: { ws: Workspace }) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const h = () => setOpen(true);
    window.addEventListener(OPEN_EVENT, h);
    return () => window.removeEventListener(OPEN_EVENT, h);
  }, []);
  useKeyboardShortcuts([{ keys: "mod+k", label: t("명령 팔레트 열기"), group: t("전역"), onTrigger: () => {
    // Another modal (create palette, confirm dialog) owns the keyboard; do not stack on top of it.
    if (!open && document.querySelector('[aria-modal="true"]')) return;
    setOpen((o) => !o);
  } }]);
  if (!open) return null;
  return <PaletteDialog ws={ws} onClose={() => setOpen(false)} />;
}

function PaletteDialog({ ws, onClose }: { ws: Workspace; onClose: () => void }) {
  const router = useRouter();
  const pathname = usePathname() || "/";
  const qc = useQueryClient();
  const me = useMe().data;
  const caps = useCaps(ws.pid, ws.wsid);
  const { setTheme } = useTheme();
  // Read the canvas cache only; the palette never triggers its own fetch.
  const canvas = useCanvas(ws.env?.id, false);
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const restore = useRef<Element | null>(null);
  useEscape(onClose);
  useEffect(() => {
    restore.current = document.activeElement;
    input.current?.focus();
    return () => (restore.current as HTMLElement | null)?.focus?.();
  }, []);

  const commands = useMemo(() => {
    const out: Command[] = [];
    const ctx = { project: ws.pid, env: ws.env && !ws.isDefaultEnv ? ws.env.name : null };
    const go = (href: string) => () => router.push(href);
    const G_ACTIONS = t("작업");
    // Same rule as the canvas page: restricted environments are editable by project admins only.
    const canAddService = pathname === "/" && !!ws.pid && !!ws.env && caps.canEdit && !(ws.env.restricted && !caps.canAdmin);
    if (canAddService)
      out.push({ id: "add-service", label: t("서비스 추가"), group: G_ACTIONS, icon: <Plus />, keywords: "new service create add database template", run: go(canvasHref({ isNew: "1" }, ctx)) });
    if (caps.canCreateProjects) out.push({ id: "new-project", label: t("새 프로젝트"), group: G_ACTIONS, icon: <Plus />, keywords: "new project create", run: go(withCtx("/projects/", { ws: ws.wsid }, { new: "project" })) });
    // The projects dashboard has no agent panel and no current environment.
    const inShell = !pathname.startsWith("/projects");
    if (ws.pid && inShell) out.push({ id: "agent", label: t("Aven 에이전트에게 묻기"), group: G_ACTIONS, icon: <Sparkles />, keywords: "ai agent assistant chat", run: () => openAvenAgent() });

    if (inShell)
      for (const s of canvas.data?.services ?? [])
      out.push({
        id: `svc-${s.id}`,
        label: s.name,
        group: ws.env ? t("서비스 · {env}", { env: ws.env.name }) : t("서비스"),
        icon: <ServiceIcon service={s} className="size-4" />,
        keywords: `service ${s.source_type ?? ""} ${s.database_kind ?? ""}`,
        run: go(canvasHref({ svc: s.id }, ctx)),
      });

    const G_PAGES = t("페이지");
    out.push({ id: "page-projects", label: t("모든 프로젝트"), group: G_PAGES, icon: <LayoutGrid />, keywords: "projects dashboard home", run: go(withCtx("/projects/", { ws: ws.wsid })) });
    for (const n of SHELL_NAV) {
      if (n.href === "/admin/" && !me?.instance_admin) continue;
      const Icon = n.icon;
      out.push({ id: `page-${n.href}`, label: n.label, group: G_PAGES, icon: <Icon />, keywords: n.href.replace(/\//g, " "), run: go(n.href === "/admin/" ? n.href : withCtx(n.href, ctx)) });
    }
    out.push({ id: "page-account", label: t("계정 설정"), group: G_PAGES, icon: <UserRound />, keywords: "account profile", run: go("/account/") });

    for (const p of ws.projects.data ?? [])
      out.push({
        id: `project-${p.id}`,
        label: p.name,
        group: t("프로젝트|plural"),
        icon: <FolderKanban />,
        keywords: `project ${p.description ?? ""}`,
        hint: p.id === ws.pid ? t("현재") : undefined,
        run: () => (p.id === ws.pid && pathname === "/" ? undefined : ws.switchProject(p.id)),
      });
    if (ws.wsList.length > 1)
      for (const w of ws.wsList)
        out.push({
          id: `ws-${w.id}`,
          label: w.name,
          group: t("작업 공간"),
          icon: <Building2 />,
          keywords: "workspace team",
          hint: w.id === ws.wsid ? t("현재") : undefined,
          run: () => w.id !== ws.wsid && (pathname.startsWith("/projects") ? router.push(withCtx("/projects/", { ws: w.id })) : ws.switchWorkspace(w.id)),
        });

    const G_PREFS = t("환경설정");
    out.push({ id: "theme-dark", label: t("다크 테마로 바꾸기"), group: G_PREFS, icon: <Moon />, keywords: "theme dark", run: () => setTheme("dark") });
    out.push({ id: "theme-light", label: t("라이트 테마로 바꾸기"), group: G_PREFS, icon: <Sun />, keywords: "theme light", run: () => setTheme("light") });
    out.push({ id: "theme-system", label: t("시스템 테마 따르기"), group: G_PREFS, icon: <Monitor />, keywords: "theme system", run: () => setTheme("system") });
    out.push({
      id: "locale",
      // Language names stay in their own language, as in LanguageSelect.
      label: LOCALES.find((l) => l.value !== getLocale())?.label ?? "English",
      group: G_PREFS,
      icon: <Languages />,
      keywords: `language locale english korean ${t("언어")}`,
      run: () => setLocale(getLocale() === "ko" ? "en" : "ko"),
    });
    out.push({
      id: "logout",
      label: t("로그아웃"),
      group: G_PREFS,
      icon: <LogOut />,
      keywords: "logout sign out",
      run: async () => {
        try {
          await api.logout();
        } finally {
          qc.clear();
          window.location.href = "/login/";
        }
      },
    });
    return out;
  }, [ws, caps.canEdit, caps.canAdmin, caps.canCreateProjects, canvas.data, me?.instance_admin, router, pathname, setTheme, qc]);

  const shown = useMemo(() => {
    if (!q.trim()) return commands;
    return commands
      .map((c, i) => ({ c, i, s: scoreCommand(q, c) }))
      .filter((x) => x.s > 0)
      .sort((a, b) => b.s - a.s || a.i - b.i)
      .map((x) => x.c);
  }, [commands, q]);
  const idx = Math.min(active, Math.max(shown.length - 1, 0));

  useEffect(() => {
    list.current?.querySelector<HTMLElement>(`[data-idx="${idx}"]`)?.scrollIntoView({ block: "nearest" });
  }, [idx]);

  const choose = (c: Command | undefined) => {
    if (!c) return;
    // Close first so focus restore does not fight with the destination page.
    restore.current = null;
    onClose();
    c.run();
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) {
      e.preventDefault();
      setActive((idx + 1) % Math.max(shown.length, 1));
    } else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) {
      e.preventDefault();
      setActive((idx - 1 + shown.length) % Math.max(shown.length, 1));
    } else if (e.key === "Home") {
      setActive(0);
    } else if (e.key === "End") {
      setActive(shown.length - 1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      choose(shown[idx]);
    } else if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      onClose();
    }
  };

  // Group headers follow the (possibly re-ranked) order; a group shows once, at its first entry.
  const seen = new Set<string>();
  return (
    <div
      className="fixed inset-0 z-[65] flex animate-fade-in items-start justify-center bg-black/[.45] px-3 pt-[12vh] motion-reduce:animate-none sm:px-4 sm:pt-[18vh] dark:bg-black/80"
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("명령 팔레트")}
        className="w-full max-w-[559px] animate-pop-in overflow-hidden rounded-lg border border-gray-200 bg-white text-sm shadow-[0px_13px_29px_0px_rgba(20,17,29,0.15),0px_53px_53px_0px_rgba(20,17,29,0.13)] motion-reduce:animate-none dark:border-white/5 dark:bg-secondaryBg"
      >
        <div className="relative p-1.5 pb-1">
          <Search className="pointer-events-none absolute left-4 top-1/2 size-4 -translate-y-1/2 text-gray-400" aria-hidden />
          <input
            ref={input}
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setActive(0);
            }}
            onKeyDown={onKey}
            placeholder={t("페이지, 서비스, 프로젝트 검색 또는 명령 실행…")}
            aria-label={t("명령 검색")}
            role="combobox"
            aria-expanded="true"
            aria-controls="command-list"
            aria-autocomplete="list"
            aria-activedescendant={shown[idx] ? `cmd-${shown[idx].id}` : undefined}
            spellCheck={false}
            autoComplete="off"
            className="w-full rounded-md border border-gray-200 bg-secondaryBg p-3 pl-9 text-sm text-foreground outline-none placeholder:text-gray-400 focus:border-pink-400 dark:border-white/20"
          />
        </div>
        <div ref={list} id="command-list" role="listbox" aria-label={t("명령")} className="max-h-[min(440px,60dvh)] overflow-y-auto overflow-x-hidden py-1.5">
          {shown.length === 0 ? (
            <div className="px-3 py-8 text-center text-sm text-gray-500">{t("‘{query}’와 일치하는 항목이 없습니다.", { query: q })}</div>
          ) : (
            shown.map((c, i) => {
              const header = !seen.has(c.group) ? c.group : null;
              seen.add(c.group);
              return (
                <div key={c.id}>
                  {header && <div className="px-5 pb-1 pt-2.5 text-xs font-medium text-gray-500">{header}</div>}
                  <button
                    type="button"
                    id={`cmd-${c.id}`}
                    role="option"
                    tabIndex={-1}
                    aria-selected={i === idx}
                    data-idx={i}
                    onMouseMove={() => i !== idx && setActive(i)}
                    onClick={() => choose(c)}
                    className={cx(
                      "mx-2 grid w-[calc(100%-1rem)] cursor-pointer grid-cols-[auto_1fr_auto] items-center gap-x-3 rounded-lg px-3 py-2.5 text-left",
                      i === idx ? "bg-gray-100 text-foreground" : "text-gray-600",
                    )}
                  >
                    <span className="flex size-4 shrink-0 items-center justify-center [&_svg]:size-4" aria-hidden>
                      {c.icon}
                    </span>
                    <span className="min-w-0 truncate">{c.label}</span>
                    <span className="flex items-center gap-2 text-xs text-gray-400">
                      {c.hint && <span>{c.hint}</span>}
                      {i === idx && <CornerDownLeft className="size-3.5" aria-hidden />}
                    </span>
                  </button>
                </div>
              );
            })
          )}
        </div>
        <div className="hidden items-center gap-4 border-t border-gray-200 px-4 py-2 text-xs text-gray-500 sm:flex dark:border-white/5">
          <span>
            <kbd className="font-sans">↑↓</kbd> {t("이동")}
          </span>
          <span>
            <kbd className="font-sans">Enter</kbd> {t("실행")}
          </span>
          <span>
            <kbd className="font-sans">Esc</kbd> {t("닫기")}
          </span>
        </div>
      </div>
    </div>
  );
}
