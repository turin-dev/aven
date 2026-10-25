"use client";

import { useSearchParams } from "next/navigation";
import { selectWorkspaceID, rememberedSelection } from "./workspaceSelection";
import { useQueries, useQuery } from "@tanstack/react-query";
import { api, isTerminal, type CanvasData, type Deployment, type ID, type MetricRange, type ProjectRole, type TemplateListQuery, type UsageGroupBy } from "./api";

const POLL = 3000;
const IDLE_POLL = 20000;

export const qk = {
  me: ["me"] as const,
  projects: ["projects"] as const,
  workspaces: ["workspaces"] as const,
  workspaceMembers: (wsid: ID) => ["workspace-members", wsid] as const,
  project: (pid: ID) => ["project", pid] as const,
  canvas: (eid: ID) => ["canvas", eid] as const,
  instance: (sid: ID, eid: ID) => ["instance", sid, eid] as const,
  serviceAdmin: (sid: ID, eid: ID) => ["service-admin", sid, eid] as const,
  resolved: (sid: ID, eid: ID) => ["resolved", sid, eid] as const,
  networking: (sid: ID, eid: ID) => ["networking", sid, eid] as const,
  privateDnsAvailable: (sid: ID, eid: ID, name: string) => ["private-dns-available", sid, eid, name] as const,
  deployments: (sid: ID, eid: ID) => ["deployments", sid, eid] as const,
  imageUpdates: (sid: ID, eid: ID) => ["image-updates", sid, eid] as const,
  deployment: (did: ID) => ["deployment", did] as const,
  cronRuns: (did: ID) => ["cron-runs", did] as const,
  metrics: (sid: ID, eid: ID, r: MetricRange) => ["metrics", sid, eid, r] as const,
  envMetrics: (eid: ID, r: MetricRange) => ["env-metrics", eid, r] as const,
  backups: (vid: ID) => ["backups", vid] as const,
  backupSchedule: (vid: ID) => ["backup-schedule", vid] as const,
  envVolumes: (eid: ID) => ["env-volumes", eid] as const,
  shared: (eid: ID) => ["shared", eid] as const,
  templates: ["templates"] as const,
  events: (pid: ID, eid?: ID) => ["events", pid, eid ?? "all"] as const,
  system: ["system"] as const,
  nodes: ["nodes"] as const,
  nodeMetrics: (nid: ID, r: MetricRange) => ["node-metrics", nid, r] as const,
  registries: (pid: ID) => ["registries", pid] as const,
  github: ["github"] as const,
  githubConnection: ["github", "connection"] as const,
  githubRepos: ["github", "repos"] as const,
  users: ["users"] as const,
  invites: ["invites"] as const,
  tokens: ["tokens"] as const,
  sessions: ["sessions"] as const,
  passkeys: ["passkeys"] as const,
  workspaceSecurity: ["workspace-security"] as const,
  resourceLimits: ["instance-resource-limits"] as const,
  instanceOverview: ["instance-overview"] as const,
  instanceNotice: ["instance-notice"] as const,
  ownerOverview: ["owner", "overview"] as const,
  setupStatus: ["owner", "setup"] as const,
  selfService: ["owner", "self-service"] as const,
  serverConfig: ["owner", "server-config"] as const,
  ownerSessions: ["owner", "sessions"] as const,
  ownerTokens: ["owner", "tokens"] as const,
  keelBackups: ["owner", "backups"] as const,
  workspaceResources: (wsid: ID) => ["workspace-resources", wsid] as const,
  members: (pid: ID) => ["members", pid] as const,
  audit: ["audit"] as const,
  accessControls: ["access-controls"] as const,
  accessPolicies: ["access-policies"] as const,
  accessGroups: ["access-groups"] as const,
  workspaceAccessGroups: (wsid: ID) => ["access-groups", wsid] as const,
  workspaceGroupSettings: (wsid: ID) => ["workspace-group-settings", wsid] as const,
  projectAccessGroups: (pid: ID) => ["project-access-groups", pid] as const,
  notifications: ["notifications"] as const,
  notificationPrefs: ["notification-prefs"] as const,
  channels: (pid: ID | null) => ["channels", pid ?? "workspace"] as const,
  deliveries: (pid: ID | null, cid: ID) => ["deliveries", pid ?? "workspace", cid] as const,
  alertRules: (pid: ID) => ["alert-rules", pid] as const,
  usage: (pid: ID, from: string, to: string, group: UsageGroupBy) => ["usage", pid, from, to, group] as const,
  workspaceUsage: (from: string, to: string) => ["usage", "workspace", from, to] as const,
};

/** In-app notifications (v1.5): polled every 30 s for the bell, or only every 5 min while the inbox stream is live. */
export function useNotifications(live = false) {
  return useQuery({ queryKey: qk.notifications, queryFn: () => api.notifications(false, 30), refetchInterval: live ? 300_000 : 30_000, staleTime: 10_000 });
}

export function useChannels(pid: ID | null, enabled = true) {
  return useQuery({ queryKey: qk.channels(pid), queryFn: () => api.channels(pid), enabled });
}

export function useAlertRules(pid: ID | null | undefined) {
  return useQuery({ queryKey: qk.alertRules(pid ?? ""), queryFn: () => api.alertRules(pid!), enabled: !!pid, refetchInterval: 30_000 });
}

export function useProjectUsage(pid: ID | null | undefined, from: string, to: string, group: UsageGroupBy) {
  return useQuery({
    queryKey: qk.usage(pid ?? "", from, to, group),
    queryFn: () => api.projectUsage(pid!, { from, to, group }),
    enabled: !!pid,
    placeholderData: (prev) => prev,
  });
}

export function useWorkspaceUsage(from: string, to: string, enabled: boolean) {
  return useQuery({ queryKey: qk.workspaceUsage(from, to), queryFn: () => api.workspaceUsage({ from, to }), enabled });
}

export function useMe() {
  return useQuery({ queryKey: qk.me, queryFn: api.me, staleTime: 60_000 });
}

/** Capabilities fail closed while identity or membership is loading. */
export function useCaps(pid: ID | null | undefined, workspaceID?: ID | null) {
  const d = useMe().data;
  const projects = useProjects().data;
  const workspaces = useWorkspaces().data ?? d?.workspaces ?? [];
  const params = useSearchParams();
  const wsid = workspaceID ?? selectWorkspaceID(workspaces, projects ?? [], pid ?? params.get("project"), params.get("ws"),
    rememberedSelection("workspace"), rememberedSelection("project"));
  const workspace = workspaces.find((w) => w.id === wsid);
  const role: ProjectRole | null = d && pid ? d.projects?.[pid] ?? null : null;
  const workspaceAdmin = workspace?.role === "owner" || workspace?.role === "admin";
  return {
    role, workspaceId: wsid, workspaceRole: workspace?.role ?? null,
    instanceAdmin: d?.instance_admin === true,
    workspaceAdmin,
    canCreateProjects: !!d && (workspaceAdmin || workspace?.role === "member"),
    canEdit: role === "admin" || role === "member",
    canAdmin: role === "admin",
    readOnly: role === "viewer",
  };
}

const canvasBusy = (c?: CanvasData) =>
  !!c?.services.some(
    (s) => s.instance.status === "deploying" || (s.latest_deployment && !isTerminal(s.latest_deployment.status)),
  );
const depsBusy = (d?: Deployment[]) => !!d?.some((x) => !isTerminal(x.status));

export function useProjects() {
  return useQuery({ queryKey: qk.projects, queryFn: () => api.listProjects(), staleTime: 10_000 });
}

/** v1.6: workspaces the current user can see (default first, then own personal, then others). */
export function useWorkspaces() {
  return useQuery({ queryKey: qk.workspaces, queryFn: () => api.workspaces(), staleTime: 30_000 });
}

/** Explicit members of the selected default, personal or team workspace. */
export function useWorkspaceMembers(wsid: ID | null | undefined, enabled = true) {
  return useQuery({
    queryKey: qk.workspaceMembers(wsid ?? ""),
    queryFn: () => api.workspaceMembers(wsid!),
    enabled: !!wsid && enabled,
  });
}

export function useProject(pid: ID | null | undefined) {
  return useQuery({
    queryKey: qk.project(pid ?? ""),
    queryFn: () => api.getProject(pid!),
    enabled: !!pid,
    staleTime: 5_000,
  });
}

export function useCanvas(eid: ID | null | undefined, enabled = true) {
  return useQuery({
    queryKey: qk.canvas(eid ?? ""),
    queryFn: () => api.canvas(eid!),
    enabled: !!eid && enabled,
    refetchInterval: (q) => (canvasBusy(q.state.data) ? POLL : IDLE_POLL),
  });
}

export function useResolvedVariables(sid: ID, eid: ID, enabled: boolean) {
  return useQuery({
    queryKey: qk.resolved(sid, eid),
    queryFn: () => api.resolvedVariables(sid, eid),
    enabled: enabled && !!sid && !!eid,
    staleTime: 5_000,
  });
}

export function useDeployments(sid: ID, eid: ID) {
  return useQuery({
    queryKey: qk.deployments(sid, eid),
    queryFn: () => api.listDeployments(sid, eid),
    enabled: !!sid && !!eid,
    refetchInterval: (q) => (depsBusy(q.state.data) ? POLL : IDLE_POLL),
  });
}

export function useImageUpdates(sid: ID, eid: ID) {
  return useQuery({
    queryKey: qk.imageUpdates(sid, eid),
    queryFn: () => api.imageUpdates(sid, eid),
    enabled: !!sid && !!eid,
    refetchInterval: 60_000,
  });
}

export function useDeployment(did: ID) {
  return useQuery({
    queryKey: qk.deployment(did),
    queryFn: () => api.getDeployment(did),
    enabled: !!did,
    refetchInterval: (q) => (q.state.data && isTerminal(q.state.data.status) ? false : POLL),
  });
}

export function useCronRuns(did: ID | null | undefined) {
  return useQuery({
    queryKey: qk.cronRuns(did ?? ""),
    queryFn: () => api.cronRuns(did!),
    enabled: !!did,
    refetchInterval: (q) => (q.state.data?.some((r) => r.status === "running") ? POLL : IDLE_POLL),
  });
}

export function useMetrics(sid: ID, eid: ID, range: MetricRange) {
  return useQuery({
    queryKey: qk.metrics(sid, eid, range),
    queryFn: () => api.metrics(sid, eid, range),
    enabled: !!sid && !!eid,
    refetchInterval: 15_000,
    placeholderData: (prev) => prev,
  });
}

export function useEnvMetrics(eid: ID | null | undefined, range: MetricRange) {
  return useQuery({
    queryKey: qk.envMetrics(eid ?? "", range),
    queryFn: () => api.envMetrics(eid!, range),
    enabled: !!eid,
    refetchInterval: 15_000,
    placeholderData: (prev) => prev,
  });
}

export function useBackups(vid: ID) {
  return useQuery({
    queryKey: qk.backups(vid),
    queryFn: () => api.listBackups(vid),
    enabled: !!vid,
    refetchInterval: (q) => (q.state.data?.some((b) => b.status === "running") ? POLL : false),
  });
}

export function useBackupSchedule(vid: ID) {
  return useQuery({ queryKey: qk.backupSchedule(vid), queryFn: () => api.backupSchedule(vid), enabled: !!vid });
}

export function useEnvVolumes(eid: ID | null | undefined) {
  return useQuery({ queryKey: qk.envVolumes(eid ?? ""), queryFn: () => api.envVolumes(eid!), enabled: !!eid });
}

export function useSharedVariables(eid: ID | null | undefined) {
  return useQuery({
    queryKey: qk.shared(eid ?? ""),
    queryFn: () => api.sharedVariables(eid!),
    enabled: !!eid,
  });
}

export function useTemplates(enabled = true) {
  return useQuery({ queryKey: qk.templates, queryFn: () => api.templates(), staleTime: 5 * 60_000, enabled });
}

/** Gallery search (server-side q/category/tag/sort). Shares the qk.templates prefix so invalidation covers it. */
export function useTemplateSearch(q: TemplateListQuery) {
  return useQuery({
    queryKey: [...qk.templates, q.q ?? "", q.category ?? "", q.tag ?? "", q.sort ?? ""] as const,
    queryFn: () => api.templates(q),
    staleTime: 60_000,
    placeholderData: (prev) => prev,
  });
}

export function useTemplateDetail(key: string | null | undefined) {
  return useQuery({ queryKey: [...qk.templates, "detail", key ?? ""] as const, queryFn: () => api.template(key!), enabled: !!key });
}

export function useEvents(pid: ID | null | undefined, eid?: ID | null, limit = 50) {
  return useQuery({
    queryKey: qk.events(pid ?? "", eid ?? undefined),
    queryFn: () => api.events(pid!, eid ?? undefined, limit),
    enabled: !!pid,
    refetchInterval: 60_000,
  });
}

/**
 * Settings > Networking view (P4). Polls faster while a custom domain waits for DNS so the status flips without a reload.
 * `retry: false`: an older server answers 404 and the UI falls back to the instance fields.
 */
export function useNetworking(sid: ID, eid: ID, enabled = true) {
  return useQuery({
    queryKey: qk.networking(sid, eid),
    queryFn: () => api.networking(sid, eid),
    enabled: enabled && !!sid && !!eid,
    retry: false,
    refetchInterval: (q) => (q.state.data?.domains.some((d) => d.status === "pending_dns") ? 10_000 : 30_000),
  });
}

/** Private DNS name availability (P5). Callers debounce `name`; an empty name or the current one is not checked. */
export function usePrivateDnsAvailable(sid: ID, eid: ID, name: string, enabled: boolean) {
  return useQuery({
    queryKey: qk.privateDnsAvailable(sid, eid, name),
    queryFn: () => api.privateDnsAvailable(sid, eid, name),
    enabled: enabled && !!name,
    retry: false,
    staleTime: 5_000,
  });
}

export function useNodes(fast = false) {
  return useQuery({ queryKey: qk.nodes, queryFn: api.nodes, refetchInterval: fast ? 2000 : 10_000, staleTime: 2000 });
}

export function useNodeMetrics(nid: ID, range: MetricRange = "1h", enabled = true) {
  return useQuery({ queryKey: qk.nodeMetrics(nid, range), queryFn: () => api.nodeMetrics(nid, range), refetchInterval: 30_000, enabled: !!nid && enabled });
}

export function useRegistries(pid: ID | null | undefined) {
  return useQuery({ queryKey: qk.registries(pid ?? ""), queryFn: () => api.registries(pid!), enabled: !!pid });
}

export function useSystem() {
  return useQuery({ queryKey: qk.system, queryFn: api.system, refetchInterval: 30_000 });
}

export function useGithub() {
  return useQuery({ queryKey: qk.github, queryFn: api.github, staleTime: 60_000 });
}

/** Instances per node across the given environments (node list "서비스 n개", force-remove warning). */
export function usePlacedCount(envIds: ID[]): Record<ID, number> {
  const qs = useQueries({ queries: envIds.map((eid) => ({ queryKey: qk.canvas(eid), queryFn: () => api.canvas(eid), staleTime: 10_000 })) });
  const out: Record<ID, number> = {};
  for (const q of qs) for (const s of q.data?.services ?? []) if (s.instance.node_id) out[s.instance.node_id] = (out[s.instance.node_id] ?? 0) + 1;
  return out;
}
