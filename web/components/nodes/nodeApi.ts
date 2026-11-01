"use client";

import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, request, type ID, type InstanceStatus, type AvenNode } from "@/lib/api";
import { qk } from "@/lib/queries";

// Node operations that lib/api.ts does not wrap yet. Shapes follow internal/api/nodeops.go.

export type DrainMove = { instance_id: ID; service_id: ID; to: ID; deployment_id?: ID };
export type DrainSkip = { instance_id: ID; service_id: ID; reason: string };
export type DrainResult = { node: AvenNode; moved: DrainMove[]; skipped: DrainSkip[] };

/** POST /api/nodes/{nid}/drain: stop scheduling on the node and redeploy its instances elsewhere. */
export const drainNode = (nid: ID, targetNodeID?: string) =>
  request<DrainResult>("POST", `/nodes/${encodeURIComponent(nid)}/drain`, targetNodeID ? { target_node_id: targetNodeID } : {});

/** One service instance placed on a node, with enough context to link to it. */
export type Placement = {
  nodeId: ID;
  serviceId: ID;
  serviceName: string;
  projectId: ID;
  projectName: string;
  envId: ID;
  envName: string;
  workspaceId: ID;
  status: InstanceStatus;
};

const placementsKey = ["ui-admin", "node-placements"] as const;

async function scanPlacements(qc: QueryClient): Promise<Record<ID, Placement[]>> {
  const projects = await qc.fetchQuery({ queryKey: qk.projects, queryFn: () => api.listProjects(), staleTime: 10_000 });
  const details = await Promise.all(projects.map((p) => api.getProject(p.id).catch(() => null)));
  const envs = details.flatMap((d) => (d ? d.environments.map((e) => ({ project: d, env: e })) : []));
  const out: Record<ID, Placement[]> = {};
  // Canvases share the cache with the project canvas (qk.canvas), so opening a project afterwards is instant.
  await Promise.all(
    envs.map(async ({ project, env }) => {
      const c = await qc.fetchQuery({ queryKey: qk.canvas(env.id), queryFn: () => api.canvas(env.id), staleTime: 10_000 }).catch(() => null);
      for (const s of c?.services ?? []) {
        const nid = s.instance.node_id;
        if (!nid) continue;
        (out[nid] ??= []).push({
          nodeId: nid,
          serviceId: s.id,
          serviceName: s.name,
          projectId: project.id,
          projectName: project.name,
          envId: env.id,
          envName: env.name,
          workspaceId: project.workspace_id,
          status: s.instance.status,
        });
      }
    }),
  );
  for (const list of Object.values(out)) list.sort((a, b) => a.projectName.localeCompare(b.projectName) || a.serviceName.localeCompare(b.serviceName));
  return out;
}

/**
 * Services per node across every project the caller can open (server owners see all of them).
 * It walks projects → environments → canvases, so it only runs while `enabled`.
 */
export function useNodePlacements(enabled: boolean) {
  const qc = useQueryClient();
  return useQuery({ queryKey: placementsKey, queryFn: () => scanPlacements(qc), enabled, staleTime: 30_000 });
}

export function invalidatePlacements(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: placementsKey });
}
