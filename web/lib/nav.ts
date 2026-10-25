"use client";

import { useCallback } from "react";
import { useRouter, useSearchParams } from "next/navigation";

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ctx.project, ctx.env],
  );
}
