"use client";

import Link from "next/link";
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { Bell, ChevronDown, LayoutGrid, List, Plus, Search, X } from "lucide-react";
import { useRouter } from "next/navigation";
import { api } from "@/lib/api";
import { qk, useCaps, useMe } from "@/lib/queries";
import { useWorkspace } from "@/lib/workspace";
import { withCtx } from "@/lib/nav";
import { Avatar, Button, AvenMark, cx } from "@/components/ui";
import { Toaster } from "@/components/Toaster";
import { NewProjectDialog } from "@/components/Shell";
import { useKeyboardShortcuts } from "@/components/KeyboardShortcuts";
import { ProjectCard, ProjectRow, ProjectSkeleton, LIST_FRAME } from "@/components/projects/ProjectCard";
import { DashboardSidebar, MobileNav } from "@/components/projects/DashboardSidebar";
import { t, tr } from "@/lib/i18n";

const FAV_KEY = "keel.favorites.v1";
const VIEW_KEY = "keel.projects.view.v1";
const SORT_KEY = "keel.projects.sort.v1";

const SORTS = [
  { value: "recent", label: t("정렬: 최근 활동") },
  { value: "created", label: t("정렬: 생성일") },
  { value: "name", label: t("정렬: 이름순") },
] as const;
type Sort = (typeof SORTS)[number]["value"];

function useFavorites() {
  const [fav, setFav] = useState<string[]>([]);
  useEffect(() => {
    try {
      setFav(JSON.parse(window.localStorage.getItem(FAV_KEY) ?? "[]"));
    } catch {
      /* ignore */
    }
  }, []);
  const toggle = (id: string) =>
    setFav((f) => {
      const n = f.includes(id) ? f.filter((x) => x !== id) : [...f, id];
      try {
        window.localStorage.setItem(FAV_KEY, JSON.stringify(n));
      } catch {
        /* ignore */
      }
      return n;
    });
  return { fav, toggle };
}

/** A string preference persisted in localStorage (read after mount so SSR markup matches). */
function useStoredPref<T extends string>(key: string, allowed: readonly T[], fallback: T) {
  const [value, setValue] = useState<T>(fallback);
  useEffect(() => {
    try {
      const v = window.localStorage.getItem(key);
      if (v && (allowed as readonly string[]).includes(v)) setValue(v as T);
    } catch {
      /* ignore */
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const set = (v: T) => {
    setValue(v);
    try {
      window.localStorage.setItem(key, v);
    } catch {
      /* ignore */
    }
  };
  return [value, set] as const;
}

function ViewToggle({ view, onChange }: { view: "grid" | "list"; onChange: (v: "grid" | "list") => void }) {
  const items = [
    { value: "grid" as const, label: t("카드로 보기"), icon: <LayoutGrid /> },
    { value: "list" as const, label: t("목록으로 보기"), icon: <List /> },
  ];
  return (
    <div role="group" aria-label={t("보기 방식")} className="flex rounded-lg border border-black/5 bg-gray-50 dark:border-white/5">
      {items.map((it) => {
        const on = view === it.value;
        return (
          <button
            key={it.value}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={it.label}
            title={it.label}
            data-state={on ? "on" : "off"}
            onClick={() => onChange(it.value)}
            className="relative flex h-[32px] w-[32px] cursor-pointer items-center justify-center border-transparent text-sm focus:outline-none focus-visible:rounded-lg focus-visible:outline-offset-0 focus-visible:outline-pink-200 data-[state=off]:text-gray-600 data-[state=on]:text-foreground data-[state=off]:hover:text-gray-800"
          >
            {on && <div className="absolute inset-0 rounded-lg border-[0.5px] border-black/10 bg-white dark:border-black/60 dark:bg-secondaryBg" />}
            <div className="z-10 flex items-center justify-center">
              <div aria-hidden="true" className="icon-container icon-sm text-current text-lg">
                {it.icon}
              </div>
            </div>
          </button>
        );
      })}
    </div>
  );
}

function ProjectsInner() {
  const ws = useWorkspace();
  const me = useMe().data;
  const router = useRouter();
  const caps = useCaps(null, ws.wsid);
  const notifications = useQuery({ queryKey: qk.notifications, queryFn: () => api.notifications(), staleTime: 30_000 });
  const unread = notifications.data?.unread ?? 0;
  const [creating, setCreating] = useState(false);
  const [q, setQ] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);
  const [mac, setMac] = useState(false);
  useEffect(() => setMac(/Mac|iPhone|iPad/.test(navigator.platform)), []);
  useKeyboardShortcuts([
    { keys: "mod+k", label: t("프로젝트 검색에 포커스"), group: t("대시보드"), onTrigger: () => searchRef.current?.focus() },
    { keys: "mod+/", label: t("새 프로젝트 만들기"), group: t("대시보드"), enabled: caps.canCreateProjects, onTrigger: () => setCreating(true) },
  ]);
  const [sort, setSort] = useStoredPref<Sort>(SORT_KEY, ["recent", "created", "name"], "recent");
  const [view, setView] = useStoredPref(VIEW_KEY, ["grid", "list"] as const, "grid");
  const { fav, toggle } = useFavorites();
  const projects = ws.projects;
  const all = useMemo(() => projects.data ?? [], [projects.data]);

  // "Recent activity" = newest deployment in the default environment (same canvas queries the cards use), else creation time.
  const canvases = useQueries({
    queries: all.map((p) => ({ queryKey: qk.canvas(p.default_environment_id), queryFn: () => api.canvas(p.default_environment_id), enabled: !!p.default_environment_id })),
  });
  const activity = useMemo(() => {
    const m: Record<string, string> = {};
    all.forEach((p, i) => {
      let t = p.created_at;
      for (const s of canvases[i]?.data?.services ?? []) if (s.latest_deployment && s.latest_deployment.created_at > t) t = s.latest_deployment.created_at;
      m[p.id] = t;
    });
    return m;
  }, [all, canvases]);

  const list = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const base = all.filter((p) => !needle || `${p.name} ${p.description}`.toLowerCase().includes(needle));
    const sorted = [...base].sort((a, b) =>
      sort === "name" ? a.name.localeCompare(b.name) : sort === "created" ? b.created_at.localeCompare(a.created_at) : (activity[b.id] ?? "").localeCompare(activity[a.id] ?? ""),
    );
    return [...sorted.filter((p) => fav.includes(p.id)), ...sorted.filter((p) => !fav.includes(p.id))];
  }, [all, q, sort, fav, activity]);

  const curName = ws.curWorkspace?.name ?? t("작업 공간");
  const workspaceAdmin = ws.curWorkspace?.role === "owner" || ws.curWorkspace?.role === "admin";

  return (
    <div className="relative min-h-dvh bg-background sm:h-dvh">
      <Toaster />
      <NewProjectDialog
        open={creating}
        onClose={() => setCreating(false)}
        workspaceId={ws.wsid}
        workspaceName={ws.curWorkspace?.name}
        onCreated={(id) => router.push(withCtx("/", { project: id }))}
      />
      <div className="flex flex-col sm:absolute sm:inset-0">
        <div className="flex min-h-dvh sm:min-h-0 sm:flex-1 sm:overflow-hidden">
          <DashboardSidebar wsList={ws.wsList} cur={ws.curWorkspace} wsid={ws.wsid} workspaceAdmin={workspaceAdmin} />
          <div className="flex min-w-0 flex-1 flex-col px-2 pb-2 sm:pl-0">
            <header className="flex h-[56px] shrink-0 items-center pl-2 pr-2 sm:pl-4">
              <div className="flex min-w-0 flex-1 items-center gap-2">
                <div className="flex min-w-0 sm:hidden">
                  <div className="flex min-w-0 items-center gap-2">
                    <div className="relative shrink-0">
                      <AvenMark className="logo rounded-full" />
                    </div>
                    <span className="shrink-0 text-gray-300">/</span>
                    <Avatar name={curName} size={24} />
                    <span className="truncate text-sm font-semibold">{curName}</span>
                  </div>
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Link href="/account/?section=notifications" aria-label={t("알림")} className="group grid justify-center focus:outline-none focus-visible:outline-none">
                  <div className="flex min-h-[28px] items-center rounded-md border border-transparent p-1.5 text-neutral-secondary hover:bg-gray-100 group-focus-visible:ring-2 group-focus-visible:ring-pink-500">
                    <div className="relative">
                      <div title={t("알림")} aria-hidden="true" className="icon-container icon-sm text-current text-lg">
                        <Bell />
                      </div>
                      {unread > 0 && <span className="absolute -right-1 -top-1 size-2 rounded-full bg-red-600" aria-hidden="true" />}
                    </div>
                  </div>
                </Link>
              </div>
            </header>
            <div className="flex flex-1 gap-2 sm:min-h-0">
              <main className="min-w-0 flex-1 rounded-lg border border-black/[0.12] bg-background pb-20 sm:overflow-y-auto sm:pb-0 sm:[scrollbar-gutter:stable] dark:border-white/[0.12]">
                <div className="w-full px-5 pt-12 sm:px-12 md:px-16">
                  <div className="mx-auto w-full max-w-[1120px] space-y-8 pb-24">
                    <div className="flex flex-wrap items-center gap-3">
                      <h1 className="text-h2 flex-1 font-normal">{t("프로젝트|plural")}</h1>
                      <label className="group order-last flex w-full cursor-text items-center gap-2 rounded-md border border-gray-200 bg-transparent px-2.5 py-1.5 text-sm text-neutral-secondary hover:border-gray-400 focus-within:border-pink-400 sm:order-1 sm:w-56">
                        <div className="icon-container icon-sm shrink-0 text-lg text-gray-500" aria-hidden="true">
                          <Search />
                        </div>
                        <input
                          ref={searchRef}
                          value={q}
                          onChange={(e) => setQ(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Escape") {
                              e.stopPropagation();
                              setQ("");
                              e.currentTarget.blur();
                            }
                          }}
                          placeholder={t("프로젝트 검색...")}
                          aria-label={t("프로젝트 검색")}
                          className="h-5 min-w-0 flex-1 bg-transparent text-left text-sm text-foreground placeholder:text-neutral-secondary focus:outline-none"
                        />
                        {q ? (
                          <button type="button" aria-label={t("검색 지우기")} onClick={() => setQ("")} className="shrink-0 cursor-pointer text-gray-500 hover:text-foreground">
                            <X className="size-3.5" />
                          </button>
                        ) : (
                          <span className="hidden shrink-0 items-center gap-0.5 sm:flex">
                            <kbd className="rounded border border-gray-200 bg-gray-100 px-1 font-sans text-xs text-gray-500">{mac ? "⌘" : "Ctrl"}</kbd>
                            <kbd className="rounded border border-gray-200 bg-gray-100 px-1 font-sans text-xs text-gray-500">K</kbd>
                          </span>
                        )}
                      </label>
                      {caps.canCreateProjects && (
                        <div className="sm:order-2">
                          <Button variant="primary" onClick={() => setCreating(true)} title={t("새 프로젝트")}>
                            <div className="icon-container icon-md !h-4 !w-4 text-2xl text-white" aria-hidden="true">
                              <Plus />
                            </div>
                            <span className="inline-block min-w-0">{t("새로 만들기")}</span>
                          </Button>
                        </div>
                      )}
                    </div>
                    <div className="grid gap-8">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-3">
                          <div className="hidden items-center gap-2 sm:flex">
                            <button type="button" onClick={() => setQ("")} className="flex cursor-pointer items-center gap-1.5 text-sm text-foreground">
                              <div className="icon-container icon-sm shrink-0 text-lg text-gray-400" aria-hidden="true">
                                <LayoutGrid />
                              </div>
                              {t("프로젝트 {count}개", { count: projects.data ? all.length : "…" })}
                            </button>
                          </div>
                          <div className="hidden h-5 w-0.5 shrink-0 rounded-full bg-gray-200 sm:block" />
                          <div className="group flex w-auto flex-col">
                            <label className="sr-only" htmlFor="project-sort">
                              {t("프로젝트 정렬")}
                            </label>
                            <div className="relative flex items-center">
                              <select
                                id="project-sort"
                                value={sort}
                                onChange={(e) => setSort(e.target.value as Sort)}
                                className="select h-[34px] w-full min-w-0 cursor-pointer appearance-none rounded-none border-none bg-transparent py-1 pl-0 pr-6 text-sm text-neutral-secondary hover:bg-transparent hover:text-gray-700 focus:outline-none focus-visible:underline"
                              >
                                {SORTS.map((s) => (
                                  <option key={s.value} value={s.value} className="bg-background text-foreground">
                                    {s.label}
                                  </option>
                                ))}
                              </select>
                              <div className="pointer-events-none absolute right-1.5 top-px flex min-h-full items-center">
                                <div className="icon-container icon-sm w-1 text-lg text-gray-400" aria-hidden="true">
                                  <ChevronDown />
                                </div>
                              </div>
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center">
                          <ViewToggle view={view} onChange={setView} />
                        </div>
                      </div>
                      {projects.isPending ? (
                        <ProjectSkeleton view={view} />
                      ) : projects.isError ? (
                        <div className="rounded-lg border border-gray-200 p-4 text-sm">
                          {t("프로젝트를 불러오지 못했습니다.")}{" "}
                          <button type="button" onClick={() => projects.refetch()} className="cursor-pointer underline hover:text-pink-600">
                            {t("다시 시도")}
                          </button>
                        </div>
                      ) : list.length === 0 ? (
                        <div className="flex flex-col items-center gap-1 rounded-lg border border-dashed border-gray-200 px-6 py-14 text-center">
                          <p className="text-sm font-medium text-foreground">
                            {q.trim() ? t("“{query}”와 일치하는 프로젝트가 없습니다", { query: q.trim() }) : t("프로젝트가 아직 없습니다")}
                          </p>
                          {!q.trim() && caps.canCreateProjects && (
                            <p className="text-sm text-gray-500">
                              {tr("{link}를 만들어 시작하세요.", {
                                link: (
                                  <button type="button" onClick={() => setCreating(true)} className="cursor-pointer text-pink-600 hover:underline">
                                    {t("새 프로젝트")}
                                  </button>
                                ),
                              })}
                            </p>
                          )}
                        </div>
                      ) : view === "grid" ? (
                        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                          {list.map((p) => (
                            <ProjectCard key={p.id} p={p} favorite={fav.includes(p.id)} onFavorite={() => toggle(p.id)} />
                          ))}
                        </div>
                      ) : (
                        <div className={LIST_FRAME}>
                          {list.map((p) => (
                            <ProjectRow key={p.id} p={p} favorite={fav.includes(p.id)} onFavorite={() => toggle(p.id)} />
                          ))}
                        </div>
                      )}
                    </div>
                  </div>
                </div>
              </main>
            </div>
          </div>
        </div>
        <MobileNav wsid={ws.wsid} />
      </div>
    </div>
  );
}

export default function ProjectsPage() {
  return (
    <Suspense fallback={null}>
      <ProjectsInner />
    </Suspense>
  );
}
