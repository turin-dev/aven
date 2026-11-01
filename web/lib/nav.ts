"use client";

import { useCallback } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Box, ChartNoAxesColumn, FileText, Gauge, LayoutTemplate, Network, Server, Settings, ShieldCheck, SquareTerminal, Waypoints, type LucideIcon } from "lucide-react";
import { t } from "./i18n";

export const PANEL_TABS = ["deployments", "backups", "code", "data", "bucket", "variables", "metrics", "shell", "settings", "admin"] as const;
export type PanelTab = (typeof PANEL_TABS)[number];

export const DEP_TABS = ["details", "build", "deploy", "http"] as const;
export type DepTab = (typeof DEP_TABS)[number];

/** Palette sub-views of the "+ 만들기" menu. */
export const NEW_VIEWS = ["1", "repo", "database", "template", "image", "compose", "volume"] as const;
export type NewView = (typeof NEW_VIEWS)[number];

export type CanvasState = {
  svc: string | null;
  tab: PanelTab;
  dep: string | null;
  dtab: DepTab;
  isNew: NewView | null;
};

export function parseTab(t: string | null | undefined): PanelTab {
  return (PANEL_TABS as readonly string[]).includes(t ?? "") ? (t as PanelTab) : "deployments";
}

export function parseDepTab(t: string | null | undefined): DepTab {
  return (DEP_TABS as readonly string[]).includes(t ?? "") ? (t as DepTab) : "build";
}

function parseNew(v: string | null): NewView | null {
  return v && (NEW_VIEWS as readonly string[]).includes(v) ? (v as NewView) : null;
}

/** Project/environment context carried on every link. */
export type Ctx = { project?: string | null; env?: string | null; ws?: string | null };

export function ctxFrom(p: URLSearchParams): Ctx {
  return { project: p.get("project"), env: p.get("env"), ws: p.get("ws") };
}

function ctxParams(ctx: Ctx): URLSearchParams {
  const q = new URLSearchParams();
  if (ctx.project) q.set("project", ctx.project);
  if (ctx.env) q.set("env", ctx.env);
  if (ctx.ws) q.set("ws", ctx.ws);
  return q;
}

/** `/path/?project=..&env=..&extra..` */
export function withCtx(path: string, ctx: Ctx, extra: Record<string, string | null | undefined> = {}): string {
  const q = ctxParams(ctx);
  for (const [k, v] of Object.entries(extra)) if (v) q.set(k, v);
  const qs = q.toString();
  return qs ? `${path}?${qs}` : path;
}

export function canvasHref(s: Partial<CanvasState>, ctx: Ctx = {}): string {
  const q = ctxParams(ctx);
  if (s.svc) {
    q.set("svc", s.svc);
    if (s.tab && s.tab !== "deployments") q.set("tab", s.tab);
    if (s.dep) {
      q.delete("tab");
      q.set("dep", s.dep);
      if (s.dtab && s.dtab !== "build") q.set("dtab", s.dtab);
    }
  }
  if (s.isNew) q.set("new", s.isNew);
  const qs = q.toString();
  return qs ? `/?${qs}` : "/";
}

function read(p: URLSearchParams): CanvasState {
  return {
    svc: p.get("svc"),
    tab: parseTab(p.get("tab")),
    dep: p.get("dep"),
    dtab: parseDepTab(p.get("dtab")),
    isNew: parseNew(p.get("new")),
  };
}

/**
 * Canvas UI state lives in the URL:
 * /?project=<id>&env=<name>&svc=<serviceId>&tab=<tab>&dep=<depId>&dtab=<details|build|deploy|http>&new=<view>
 */
export function useCanvasState() {
  const params = useSearchParams();
  const router = useRouter();
  const state = read(params);
  const key = params.toString();

  const go = useCallback(
    (patch: Partial<CanvasState>, mode: "push" | "replace" = "push") => {
      const cur = new URLSearchParams(key);
      const next: CanvasState = { ...read(cur), ...patch };
      const href = canvasHref(next, ctxFrom(cur));
      if (mode === "push") router.push(href, { scroll: false });
      else router.replace(href, { scroll: false });
    },
    [key, router],
  );

  return { ...state, go };
}

/** Builds links that keep the current project and environment. */
export function useCtxHref() {
  const params = useSearchParams();
  const ctx = ctxFrom(params);
  return useCallback(
    (path: string, extra?: Record<string, string | null | undefined>) => withCtx(path, ctx, extra),
    // ctx.ws matters too: without it, links built on /?ws=A kept pointing at A after switching to ?ws=B,
    // and following one silently switched the workspace back.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ctx.project, ctx.env, ctx.ws],
  );
}

/** Pages of the left rail. Shared by the rail, the command palette and the top bar breadcrumb. */
export type ShellNavItem = { href: string; label: string; icon: LucideIcon; match: (path: string) => boolean; bottom?: boolean };
export const SHELL_NAV: ShellNavItem[] = [
  { href: "/", label: t("아키텍처"), icon: Network, match: (p) => p === "/" },
  { href: "/observability/", label: t("관측"), icon: ChartNoAxesColumn, match: (p) => p.startsWith("/observability") },
  { href: "/logs/", label: t("로그"), icon: FileText, match: (p) => p.startsWith("/logs") },
  { href: "/traces/", label: t("트레이스"), icon: Waypoints, match: (p) => p.startsWith("/traces") },
  { href: "/nodes/", label: t("노드"), icon: Server, match: (p) => p.startsWith("/nodes") },
  { href: "/sandboxes/", label: t("샌드박스"), icon: Box, match: (p) => p.startsWith("/sandboxes") },
  { href: "/usage/", label: t("사용량"), icon: Gauge, match: (p) => p.startsWith("/usage") },
  { href: "/templates/", label: t("템플릿"), icon: LayoutTemplate, match: (p) => p.startsWith("/templates") },
  { href: "/cli/", label: "CLI", icon: SquareTerminal, match: (p) => p.startsWith("/cli") },
  { href: "/settings/", label: t("설정"), icon: Settings, match: (p) => p.startsWith("/settings"), bottom: true },
  { href: "/admin/", label: t("어드민"), icon: ShieldCheck, match: (p) => p.startsWith("/admin"), bottom: true },
];
