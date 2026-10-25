/* Database high availability API (see docs/api-v1.md "### Database high availability"). */

import { useQuery } from "@tanstack/react-query";
import { ApiError, type ID } from "./api";
import { t } from "./i18n";

export type HARole = "root" | "replica" | "internal" | "edge";
/** Role reported by the engine itself (data nodes only; "" for proxies). */
export type HADBRole = "primary" | "replica" | "uninitialized" | "recovering" | "unknown" | "";

export type HASelector = {
  role: Exclude<HARole, "root">;
  label: string;
  nodeLabel: string;
  description?: string;
  options: number[];
  default: number;
};

export type HACompanion = {
  key: string;
  name: string;
  description: string;
  selectors: HASelector[];
  supported_versions: number[];
  /** major(.minor) read from the root image tag */
  version?: string;
};

export type HAMember = {
  service_id: ID;
  name: string;
  role: HARole;
  ordinal: number;
  node_id: string;
  instance_status: string;
  db_role: HADBRole;
  /** primary recorded by Aven */
  primary: boolean;
  healthy: boolean;
  streaming: boolean;
  lag_bytes?: number;
  error?: string;
  checked_at?: string;
};

export type HACluster = {
  id: ID;
  kind: "postgres" | "redis" | "mongo";
  environment_id: ID;
  root_service_id: ID;
  primary_service_id: ID;
  status: "provisioning" | "active" | "switching";
  auto_failover: boolean;
  /** true when Aven promotes replicas itself (postgres); otherwise the engine elects a primary (Sentinel / replica set). */
  keel_failover: boolean;
  busy: boolean;
  message: string;
  companion_template: string;
  standalone_template: string;
  counts: Partial<Record<HARole, number>>;
  /** replica counts the companion template allows (scale) */
  replica_options: number[];
  endpoint: { service_id: ID; name: string; private_host: string; url_variable: string } | null;
  members: HAMember[];
  created_at: string;
};

export type HAStatus = {
  cluster: HACluster | null;
  eligible: boolean;
  problems: string[];
  companion?: HACompanion;
};

const enc = encodeURIComponent;
const base = (sid: ID, eid: ID) => `/api/services/${enc(sid)}/instances/${enc(eid)}/ha`;

async function call<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    credentials: "include",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    let data: unknown;
    try {
      const j = (await res.json()) as { error?: string };
      data = j;
      if (j?.error) msg = j.error;
    } catch {
      /* ignore */
    }
    if (res.status === 401 && typeof window !== "undefined" && !window.location.pathname.startsWith("/login")) {
      window.location.href = `/login/?next=${enc(window.location.pathname + window.location.search)}`;
    }
    throw new ApiError(res.status, msg, data);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const haApi = {
  status: (sid: ID, eid: ID) => call<HAStatus>("GET", base(sid, eid)),
  convert: (sid: ID, eid: ID, input: { replicas?: number; internal?: number; edge?: number; auto_failover?: boolean; skip_backup?: boolean }) =>
    call<{ cluster: HACluster; migrated_services: string[] }>("POST", base(sid, eid), input),
  update: (sid: ID, eid: ID, input: { auto_failover?: boolean; replicas?: number }) => call<{ cluster: HACluster }>("PATCH", base(sid, eid), input),
  revert: (sid: ID, eid: ID) => call<void>("DELETE", base(sid, eid)),
  switchover: (sid: ID, eid: ID, to: ID) => call<{ cluster: HACluster }>("POST", `${base(sid, eid)}/switchover`, { to }),
  environment: (eid: ID) => call<HACluster[]>("GET", `/api/environments/${enc(eid)}/ha`),
};

export const haKeys = {
  status: (sid: ID, eid: ID) => ["ha", sid, eid] as const,
  env: (eid: ID) => ["ha-env", eid] as const,
};

/** HA status of one service (eligibility before conversion, cluster afterwards). Polls while a cluster exists. */
export function useHAStatus(sid: ID, eid: ID) {
  return useQuery({
    queryKey: haKeys.status(sid, eid),
    queryFn: () => haApi.status(sid, eid),
    retry: false,
    refetchInterval: (q) => (q.state.data?.cluster ? 5_000 : false),
  });
}

/** Clusters of an environment (canvas badges). */
export function useEnvHA(eid: ID | null | undefined) {
  return useQuery({
    queryKey: haKeys.env(eid ?? ""),
    queryFn: () => haApi.environment(eid!),
    enabled: !!eid,
    retry: false,
    staleTime: 5_000,
    refetchInterval: 10_000,
  });
}

/** Where a service sits in its environment's HA cluster (null when standalone). */
export function haMemberOf(clusters: HACluster[] | undefined, sid: ID): { cluster: HACluster; member: HAMember } | null {
  for (const c of clusters ?? []) {
    const m = c.members.find((x) => x.service_id === sid);
    if (m) return { cluster: c, member: m };
  }
  return null;
}

export const HA_ROLE_LABEL: Record<HARole, string> = { root: t("원본"), replica: "replica", internal: t("조정 노드"), edge: t("프록시") };

export const HA_ENGINE_LABEL: Record<HACluster["kind"], string> = {
  postgres: t("Aven이 주 노드를 감시하고, 30초 동안 응답이 없으면 가장 최신인 replica를 승격합니다."),
  redis: t("모든 노드의 Sentinel이 투표로 주 노드를 뽑습니다. HAProxy가 주 노드로만 연결을 보냅니다."),
  mongo: t("replica set 구성원이 투표로 주 노드를 뽑습니다. 드라이버가 주 노드를 스스로 찾습니다."),
};
