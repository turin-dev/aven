import type { Project, Workspace } from "./api";

/** Resolve explicit links before remembered choices. Invalid explicit IDs never
 * silently open a different workspace or project. Shared by navigation and RBAC. */
export function selectWorkspaceID(
  workspaces: Workspace[], projects: Pick<Project, "id" | "workspace_id">[],
  projectID: string | null, workspaceID: string | null,
  rememberedWorkspace: string | null, rememberedProject: string | null,
): string | null {
  const known = (id: string | null | undefined) => id && workspaces.some((w) => w.id === id) ? id : null;
  if (projectID) return known(projects.find((p) => p.id === projectID)?.workspace_id);
  if (workspaceID) return known(workspaceID);
  const remembered = known(rememberedWorkspace) ?? known(projects.find((p) => p.id === rememberedProject)?.workspace_id);
  if (remembered) return remembered;
  const ordered = [...workspaces.filter((w) => w.personal), ...workspaces.filter((w) => w.default), ...workspaces.filter((w) => !w.personal && !w.default)];
  return ordered.find((w) => projects.some((p) => p.workspace_id === w.id))?.id ?? ordered[0]?.id ?? null;
}

export function rememberedSelection(key: "workspace" | "project"): string | null {
  if (typeof window === "undefined") return null;
  try { return window.localStorage.getItem(`keel.${key}.v1`); } catch { return null; }
}
