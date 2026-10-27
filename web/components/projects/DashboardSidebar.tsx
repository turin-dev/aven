"use client";

import Link from "next/link";
import { useState } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { BarChart2, Bell, Check, ChevronDown, Layout, LayoutGrid, LogOut, MoreHorizontal, MoreVertical, Plus, ShieldCheck, SquareTerminal, UserRound, Users } from "lucide-react";
import { api, type Workspace } from "@/lib/api";
import { useMe } from "@/lib/queries";
import { withCtx } from "@/lib/nav";
import { Avatar, AvenMark, Menu, MenuItem, cx } from "@/components/ui";
import { NewWorkspaceDialog } from "@/components/Shell";
import { t, tr } from "@/lib/i18n";
import { writeStorage } from "@/lib/legacyStorage";

const WORKSPACE_KEY = "aven.workspace.v1";
const DIVIDER = "border-t border-black/[0.08] dark:border-white/[0.08]";
const ITEM = "flex items-center gap-3 px-3 py-2 h-9 rounded-lg text-sm overflow-hidden whitespace-nowrap";
const IDLE = "text-neutral-secondary hover:bg-black/[0.02] dark:hover:bg-white/[0.03]";

export function workspaceKind(w: Pick<Workspace, "default" | "personal">) {
  return w.default ? t("기본") : w.personal ? t("개인") : t("팀");
}

/** Stay on the dashboard when switching workspace (the canvas switcher jumps into a project instead). */
export function useSwitchWorkspace() {
  const router = useRouter();
  return (id: string) => {
    writeStorage(WORKSPACE_KEY, id);
    router.push(withCtx("/projects/", { ws: id }));
  };
}

function NavLink({ href, icon, children, active, strokeThin }: { href: string; icon: React.ReactNode; children: React.ReactNode; active?: boolean; strokeThin?: boolean }) {
  return (
    <Link className={cx("block", active && "active")} href={href} aria-current={active ? "page" : undefined}>
      <div className={cx(ITEM, "cursor-pointer", active ? "bg-black/[0.03] font-medium text-foreground dark:bg-white/[0.06]" : IDLE)}>
        <div
          className={cx("icon-container icon-sm shrink-0 text-lg", active ? "text-pink-500 dark:text-pink-600" : "text-neutral-secondary", strokeThin && "[&_svg]:stroke-[1.5]")}
          aria-hidden="true"
        >
          {icon}
        </div>
        <span>{children}</span>
      </div>
    </Link>
  );
}
function SubLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <Link className="block" href={href}>
      <div className={cx("flex h-8 items-center rounded-lg pl-10 pr-3 text-sm whitespace-nowrap", IDLE)}>{children}</div>
    </Link>
  );
}

function CogIcon() {
  return (
    <svg width="1em" height="1em" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        clipRule="evenodd"
        d="M13.906 4.91a6.41 6.41 0 0 0-1.451-.84l-.469-1.873a1.25 1.25 0 0 0-1.212-.947H9.226a1.25 1.25 0 0 0-1.213.947L7.545 4.07a6.41 6.41 0 0 0-1.452.84l-1.857-.532a1.25 1.25 0 0 0-1.427.577l-.774 1.34A1.25 1.25 0 0 0 2.25 7.82l1.388 1.343a6.477 6.477 0 0 0 0 1.676L2.25 12.181a1.25 1.25 0 0 0-.214 1.524l.774 1.34a1.25 1.25 0 0 0 1.427.577l1.857-.531c.442.34.93.623 1.452.84l.468 1.872c.14.557.64.947 1.213.947h1.548a1.25 1.25 0 0 0 1.213-.947l.468-1.873c.522-.216 1.01-.5 1.452-.84l1.857.532a1.25 1.25 0 0 0 1.427-.577l.774-1.34a1.25 1.25 0 0 0-.214-1.524l-1.389-1.343a6.494 6.494 0 0 0 0-1.676l1.389-1.343a1.25 1.25 0 0 0 .213-1.524l-.773-1.34a1.25 1.25 0 0 0-1.427-.577l-1.857.531ZM10 12.332a2.333 2.333 0 1 0 0-4.666 2.333 2.333 0 0 0 0 4.666Z"
        stroke="currentColor"
        strokeWidth="1.5"
      />
    </svg>
  );
}

function ScopeMenu({ wsList, cur, wsid }: { wsList: Workspace[]; cur: Workspace | null; wsid: string | null }) {
  const [creating, setCreating] = useState(false);
  const switchWs = useSwitchWorkspace();
  const name = cur?.name ?? t("작업 공간");
  return (
    <>
      <Menu
        align="start"
        label={t("작업 공간")}
        trigger={(trig) => (
          <button
            type="button"
            {...trig}
            data-testid="dashboard-scope"
            aria-label={t("작업 공간: {name}. 작업 공간 바꾸기", { name })}
            className="flex h-[42px] w-full cursor-pointer items-center rounded-md border-none p-0 hover:bg-transparent focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-400"
          >
            <div className="flex min-w-0 items-center gap-1.5 pl-2 pr-1">
              <span className="relative flex shrink-0" title={name}>
                <Avatar name={name} size={24} />
              </span>
              <div className="grid min-w-0 flex-1 justify-items-start gap-y-0.5">
                <span className="max-w-full truncate text-left text-sm font-semibold">{name}</span>
                <div className="flex items-center gap-1">
                  <span className="text-[10px] font-medium uppercase text-blue-700">{cur ? workspaceKind(cur) : "aven"}</span>
                </div>
              </div>
              <div className="icon-container mt-1 shrink-0 self-start text-gray-500 [&_svg]:size-3" aria-hidden="true">
                <ChevronDown />
              </div>
            </div>
          </button>
        )}
      >
        {(close) => (
          <div className="w-[239px]">
            <div className="px-3 py-1 text-xs font-medium text-gray-600">{t("작업 공간")}</div>
            {wsList.map((w) => (
              <MenuItem
                key={w.id}
                icon={w.id === wsid ? <Check className="text-pink-600" /> : <span className="inline-block size-4" />}
                onSelect={() => {
                  close();
                  if (w.id !== wsid) switchWs(w.id);
                }}
              >
                <span className="min-w-0 flex-1 truncate">{w.name}</span>
                <span className="ml-2 shrink-0 text-[10px] font-medium uppercase text-blue-700">{workspaceKind(w)}</span>
              </MenuItem>
            ))}
            <div className="my-2 h-px w-full bg-gray-100" />
            <MenuItem
              icon={<Plus />}
              onSelect={() => {
                close();
                setCreating(true);
              }}
            >
              {t("새 작업 공간")}
            </MenuItem>
          </div>
        )}
      </Menu>
      <NewWorkspaceDialog open={creating} onClose={() => setCreating(false)} onCreated={(id) => switchWs(id)} />
    </>
  );
}

function UserMenu({ user }: { user: string }) {
  const qc = useQueryClient();
  const router = useRouter();
  const me = useMe().data;
  const logout = async () => {
    try {
      await api.logout();
    } finally {
      qc.clear();
      window.location.href = "/login/";
    }
  };
  return (
    <Menu
      label={t("계정")}
      side="right"
      trigger={(trig) => (
        <button type="button" {...trig} aria-label={t("계정 메뉴")} className="group flex h-[42px] w-full cursor-pointer items-center border-none bg-transparent p-0 focus:outline-none">
          <div className="flex w-full cursor-pointer items-center gap-2 overflow-hidden whitespace-nowrap rounded-lg px-2 py-2 transition-colors hover:bg-black/[0.02] group-data-[state=open]:bg-black/[0.03] dark:hover:bg-white/[0.03] dark:group-data-[state=open]:bg-white/[0.06]">
            <span className="flex shrink-0 overflow-hidden rounded-full" title={user}>
              <Avatar name={user || "?"} size={24} />
            </span>
            <p className="min-w-0 flex-1 truncate text-left text-sm font-medium leading-tight">{user}</p>
            <div className="icon-container icon-sm shrink-0 text-lg text-neutral-secondary" aria-hidden="true">
              <MoreVertical />
            </div>
          </div>
        </button>
      )}
    >
      {(close) => (
        <>
          <div className="px-3 py-1 text-xs text-gray-600">
            {tr("{user} 계정으로 로그인됨", { user: <span className="font-medium text-foreground">{user}</span> })}
            {me?.email && <div className="mt-0.5 truncate">{me.email}</div>}
          </div>
          <div className="my-2 h-px w-full bg-gray-100" />
          <MenuItem icon={<UserRound />} onSelect={() => { close(); router.push("/account/"); }}>
            {t("계정 설정")}
          </MenuItem>
          <MenuItem icon={<Bell />} onSelect={() => { close(); router.push("/account/?section=notifications"); }}>
            {t("알림")}
          </MenuItem>
          {me?.instance_admin && (
            <MenuItem icon={<ShieldCheck />} onSelect={() => { close(); router.push("/admin/"); }}>
              {t("어드민")}
            </MenuItem>
          )}
          <div className="my-2 h-px w-full bg-gray-100" />
          <MenuItem icon={<LogOut />} onSelect={logout}>
            {t("로그아웃")}
          </MenuItem>
        </>
      )}
    </Menu>
  );
}

export function DashboardSidebar({
  wsList,
  cur,
  wsid,
  workspaceAdmin,
}: {
  wsList: Workspace[];
  cur: Workspace | null;
  wsid: string | null;
  workspaceAdmin: boolean;
}) {
  const me = useMe().data;
  const [settingsOpen, setSettingsOpen] = useState(false);
  const user = me?.user ?? "";
  const ws = (path: string, extra: Record<string, string> = {}) => withCtx(path, { ws: wsid }, extra);
  return (
    <aside className="group/sidebar relative hidden h-full flex-col bg-background sm:flex" style={{ width: 219, minWidth: 219 }}>
      <div className="flex flex-1 flex-col overflow-y-auto overflow-x-hidden pb-3 pt-4">
        <div className="px-2">
          <Link className="active flex items-center px-2" href="/projects/" aria-current="page" style={{ marginBottom: 32 }} aria-label="aven">
            <div className="relative">
              <AvenMark className="logo rounded-full" />
            </div>
          </Link>
          <div className="mb-2">
            <ScopeMenu wsList={wsList} cur={cur} wsid={wsid} />
          </div>
        </div>
        <div className={cx("my-3", DIVIDER)} />
        <div className="px-2">
          <nav className="flex flex-col gap-1">
            <NavLink href={ws("/projects/")} icon={<LayoutGrid />} active>
              {t("프로젝트|plural")}
            </NavLink>
            <NavLink href="/templates/" icon={<Layout />} strokeThin>
              {t("템플릿")}
            </NavLink>
            <div className={cx("-mx-2 my-2", DIVIDER)} />
            <NavLink href={ws("/usage/")} icon={<BarChart2 />}>
              {t("사용량")}
            </NavLink>
            <NavLink href={ws("/settings/", { section: "workspace-members" })} icon={<Users />}>
              {t("멤버")}
            </NavLink>
            <button type="button" aria-expanded={settingsOpen} onClick={() => setSettingsOpen((o) => !o)} className={cx(ITEM, IDLE, "w-full cursor-pointer text-left")}>
              <div className="icon-container icon-sm shrink-0 text-lg text-neutral-secondary" aria-hidden="true">
                <CogIcon />
              </div>
              <span className="flex items-center gap-2">
                {t("설정")}
                <div className={cx("icon-container icon-sm text-lg text-neutral-secondary transition-transform duration-200", settingsOpen && "rotate-180")} aria-hidden="true">
                  <ChevronDown />
                </div>
              </span>
            </button>
            {settingsOpen && (
              <div className="flex flex-col gap-1">
                <SubLink href={ws("/settings/")}>{t("일반")}</SubLink>
                {workspaceAdmin && <SubLink href={ws("/settings/", { section: "access-groups" })}>{t("접근 그룹")}</SubLink>}
                <SubLink href={ws("/settings/", { section: "integrations" })}>{t("통합")}</SubLink>
                <SubLink href={ws("/settings/", { section: "notifications" })}>{t("알림 채널")}</SubLink>
              </div>
            )}
          </nav>
        </div>
        <div className={cx("my-3", DIVIDER)} />
        <div className="px-2">
          <div className="flex flex-col gap-1">
            {me?.instance_admin && (
              <SecondaryLink href="/admin/" icon={<ShieldCheck />}>
                {t("어드민")}
              </SecondaryLink>
            )}
            <SecondaryLink href="/cli/" icon={<SquareTerminal />}>
              CLI
            </SecondaryLink>
          </div>
        </div>
        <div className="flex-1" />
      </div>
      <div className={cx("px-2 py-2", DIVIDER)}>
        <UserMenu user={user} />
      </div>
    </aside>
  );
}

function SecondaryLink({ href, icon, children }: { href: string; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <Link href={href} className="block focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700">
      <div className="flex h-9 items-center gap-3 overflow-hidden whitespace-nowrap rounded-lg px-3 py-2 text-sm text-gray-400 transition-colors hover:bg-black/[0.02] hover:text-neutral-secondary dark:hover:bg-white/[0.03]">
        <span className="relative shrink-0">
          <div aria-hidden="true" className="icon-container icon-sm text-current text-lg">
            {icon}
          </div>
        </span>
        <span>{children}</span>
      </div>
    </Link>
  );
}

export function MobileNav({ wsid }: { wsid: string | null }) {
  const ws = (path: string, extra: Record<string, string> = {}) => withCtx(path, { ws: wsid }, extra);
  const items = [
    { href: ws("/projects/"), label: t("프로젝트|plural"), icon: <LayoutGrid />, active: true },
    { href: "/templates/", label: t("템플릿"), icon: <Layout /> },
    { href: ws("/usage/"), label: t("사용량"), icon: <BarChart2 /> },
    { href: ws("/settings/", { section: "workspace-members" }), label: t("멤버"), icon: <Users /> },
    { href: ws("/settings/"), label: t("설정"), icon: <CogIcon /> },
  ];
  return (
    <div
      className="pointer-events-none fixed bottom-0 left-0 right-0 z-30 flex flex-col items-center pb-4 sm:hidden"
      style={{ background: "linear-gradient(to bottom, transparent 0%, var(--background) 50%)", height: 160 }}
    >
      <nav
        className="pointer-events-auto mt-auto flex max-w-[calc(100%-40px)] items-center rounded-2xl border border-transparent bg-background px-2 dark:border-white/[0.08] dark:bg-secondaryBg"
        style={{ height: 56, boxShadow: "rgba(0, 0, 0, 0.08) 0px 2px 16px, rgba(0, 0, 0, 0.06) 0px 0px 0px 1px" }}
      >
        {items.map((it) => (
          <Link
            key={it.label}
            href={it.href}
            className={cx("flex h-full flex-1 flex-col items-center justify-center gap-1", it.active ? "active text-foreground" : "text-neutral-secondary")}
            style={{ minWidth: 56, maxWidth: 72 }}
          >
            <div className={cx("icon-container icon-sm text-lg", it.active ? "text-pink-500 dark:text-pink-600" : "text-current")} aria-hidden="true">
              {it.icon}
            </div>
            <span className={cx("text-[10px] leading-tight", it.active && "font-medium")}>{it.label}</span>
          </Link>
        ))}
        <div className="mx-1 h-5 w-0.5 rounded-full bg-black/[0.05] dark:bg-white/10" />
        <Link href="/account/" aria-label={t("계정")}className="rounded-lg px-4 py-3 text-neutral-secondary">
          <div aria-hidden="true" className="icon-container icon-sm text-current text-lg">
            <MoreHorizontal />
          </div>
        </Link>
      </nav>
    </div>
  );
}
