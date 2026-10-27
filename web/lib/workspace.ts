"use client";

import { useCallback, useEffect, useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import type { Environment, ProjectDetail, Workspace as WorkspaceInfo } from "./api";
import { useMe, useProject, useProjects, useWorkspaces } from "./queries";
import { withCtx } from "./nav";
import { selectWorkspaceID, rememberedSelection } from "./workspaceSelection";
import { t } from "./i18n";
import { writeStorage } from "./legacyStorage";

const PROJECT_KEY = "aven.project.v1";
const WORKSPACE_KEY = "aven.workspace.v1";

/**
 * Current project, environment and workspace.
 *
 * The workspace comes from `?ws=` (remembered in localStorage under `aven.workspace.v1`, falling back to `keel.workspace.v1`); if a project is
 * selected via `?project=` that belongs to another workspace, the workspace follows the project. It falls back
 * to the current project's workspace, then the user's personal workspace, then the default, then the first.
 *
 * The project comes from `?project=` (remembered in localStorage), the environment from `?env=<name>`;
 * the project falls back to the first project *of the current workspace* and its default environment.
 */
export function useWorkspace() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname() || "/";
  const allProjects = useProjects();
  const workspaces = useWorkspaces();
  const me = useMe();
  const pidParam = params.get("project");
  const wsParam = params.get("ws");
  const envParam = params.get("env");

  const wsList: WorkspaceInfo[] = workspaces.data ?? me.data?.workspaces ?? [];

  // If a project is explicitly selected, its workspace wins so cross-workspace links switch workspace.
  const selectedProject = useMemo(() => {
    if (!pidParam || !allProjects.data) return null;
    return allProjects.data.find((p) => p.id === pidParam) ?? null;
  }, [allProjects.data, pidParam]);

  const wsid = useMemo(() => selectWorkspaceID(wsList, allProjects.data ?? [], pidParam, wsParam,
    rememberedSelection("workspace"), rememberedSelection("project")), [wsList, allProjects.data, pidParam, wsParam]);

  const curWorkspace = useMemo(() => wsList.find((w) => w.id === wsid) ?? null, [wsList, wsid]);

  // Projects visible in the current workspace (client-side filter by workspace_id).
  const projectsData = useMemo(() => {
    const list = allProjects.data;
    if (!list) return undefined;
    if (!wsid) return [];
    return list.filter((p) => p.workspace_id === wsid);
  }, [allProjects.data, wsid]);

  // Shape-compatible with the previous `projects` (a useQuery result) but with the filtered data.
  const projects = useMemo(
    () => ({ ...allProjects, data: projectsData }),
    [allProjects, projectsData],
  );

  const pid = useMemo(() => {
    const list = projectsData;
    if (!list) return pidParam ?? null;
    if (pidParam) return list.find((p) => p.id === pidParam)?.id ?? null;
    const want = rememberedSelection("project");
    return list.find((p) => p.id === want)?.id ?? list[0]?.id ?? null;
  }, [projectsData, pidParam]);

  useEffect(() => {
    if (pid && pidParam === pid) {
      writeStorage(PROJECT_KEY, pid);
    }
  }, [pid, pidParam]);

  // Remember the resolved workspace so it survives reloads without a `?ws=` param.
  useEffect(() => {
    if (!wsid || !allProjects.data || !wsList.length) return;
    writeStorage(WORKSPACE_KEY, wsid);
  }, [wsid, allProjects.data, wsList.length]);

  const project = useProject(pid);
  const detail: ProjectDetail | undefined = project.data;
  const envs: Environment[] = useMemo(
    () =>
      [...(detail?.environments ?? [])].sort((a, b) =>
        a.id === detail?.default_environment_id ? -1 : b.id === detail?.default_environment_id ? 1 : a.created_at.localeCompare(b.created_at),
      ),
    [detail],
  );
  const env =
    envs.find((e) => e.name === envParam) ?? envs.find((e) => e.id === detail?.default_environment_id) ?? envs[0];
  const isDefaultEnv = !!env && env.id === detail?.default_environment_id;

  /** Switch environment, keeping the current page but dropping open panels. */
  const switchEnv = useCallback(
    (name: string) => {
      router.push(withCtx(pathname, { project: pid, env: name, ws: wsid }));
    },
    [router, pathname, pid, wsid],
  );

  const switchProject = useCallback(
    (id: string) => {
      try {
        window.localStorage.setItem(PROJECT_KEY, id);
      } catch {
        /* ignore */
      }
      router.push(withCtx(pathname === "/settings/" ? pathname : "/", { project: id }));
    },
    [router, pathname],
  );

  /** Switch workspace: jump to its first project (or the empty dashboard, prompting to create one). */
  const switchWorkspace = useCallback(
    (id: string) => {
      try {
        window.localStorage.setItem(WORKSPACE_KEY, id);
      } catch {
        /* ignore */
      }
      const first = (allProjects.data ?? []).find((p) => p.workspace_id === id);
      if (first) {
        try {
          window.localStorage.setItem(PROJECT_KEY, first.id);
        } catch {
          /* ignore */
        }
        router.push(withCtx("/", { project: first.id }, { ws: id }));
      } else {
        router.push(withCtx("/", {}, { ws: id }));
      }
    },
    [router, allProjects.data],
  );

  return {
    projects,
    allProjects,
    workspaces,
    wsList,
    wsid,
    curWorkspace,
    switchWorkspace,
    pid,
    project,
    detail,
    envs,
    env,
    isDefaultEnv,
    switchEnv,
    switchProject,
    isPending: allProjects.isPending || workspaces.isPending || (!!pid && project.isPending),
    error: allProjects.error ?? workspaces.error ?? project.error ?? ((!allProjects.isPending && !workspaces.isPending && (pidParam || wsParam) && !wsid) ? new Error(t("접근할 수 없는 작업 공간 또는 프로젝트입니다.")) : null),
  };
}

export type Workspace = ReturnType<typeof useWorkspace>;

/** Stable accent for an environment name (non-production indicator). */
const ENV_COLORS = ["#e0a43a", "#3fb6c9", "#d55181", "#5fb36a", "#3987e5", "#d95926"];
export function envColor(name: string): string {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  if (name === "staging") return ENV_COLORS[0];
  return ENV_COLORS[h % ENV_COLORS.length];
}
