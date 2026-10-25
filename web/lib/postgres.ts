// PostgreSQL 전용 API: PgBouncer 연결 풀링과 메이저 버전 업그레이드 (docs/api-v1.md "PostgreSQL").
import { request, type ID } from "./api";

export type PoolMode = "transaction" | "session" | "statement";

export type PostgresPooling = {
  status: "off" | "deploying" | "updating" | "enabled" | "failed";
  enabled: boolean;
  pool_mode: PoolMode;
  default_pool_size: number;
  max_client_conn: number;
  /** null: PgBouncer 기본값(200) */
  max_prepared_statements: number | null;
  pooler_service_id?: ID;
  pooler_service_name?: string;
  deployment_id?: ID;
  error?: string;
  variables: string[];
};

export type PostgresPoolingInput = {
  enabled?: boolean;
  pool_mode?: PoolMode;
  default_pool_size?: number;
  max_client_conn?: number;
  max_prepared_statements?: number;
  reset_max_prepared_statements?: boolean;
};

export type PostgresCheck = {
  key: string;
  label: string;
  severity: "blocker" | "warning" | "info";
  passed: boolean;
  message: string;
};

export type PostgresPreflight = {
  ready: boolean;
  current_major: number;
  target_major: number;
  available_targets: number[];
  data_size_bytes: number | null;
  checks: PostgresCheck[];
  checked_at: string;
};

export type PostgresUpgradeStatus = "none" | "preparing" | "upgrading" | "succeeded" | "failed" | "reverting" | "reverted" | "revert_failed";

export type PostgresUpgrade = {
  status: PostgresUpgradeStatus;
  current_major?: number;
  from_major?: number;
  target_major?: number;
  image?: string;
  from_image?: string;
  backup_id?: ID;
  post_backup_id?: ID;
  deployment_id?: ID;
  error?: string;
  can_revert: boolean;
  revert_deadline?: string;
  started_at?: string;
  finished_at?: string;
  in_progress: boolean;
  available_targets: number[];
};

const base = (sid: ID, eid: ID) => `/services/${encodeURIComponent(sid)}/instances/${encodeURIComponent(eid)}/postgres`;

export const postgresApi = {
  pooling: (sid: ID, eid: ID) => request<PostgresPooling>("GET", `${base(sid, eid)}/pooling`),
  setPooling: (sid: ID, eid: ID, input: PostgresPoolingInput) => request<PostgresPooling>("PUT", `${base(sid, eid)}/pooling`, input),
  removePooling: (sid: ID, eid: ID) => request<PostgresPooling>("DELETE", `${base(sid, eid)}/pooling`),
  upgrade: (sid: ID, eid: ID) => request<PostgresUpgrade>("GET", `${base(sid, eid)}/upgrade`),
  preflight: (sid: ID, eid: ID, to?: number) =>
    request<PostgresPreflight>("GET", `${base(sid, eid)}/upgrade/preflight${to ? `?to=${to}` : ""}`),
  startUpgrade: (sid: ID, eid: ID, to_major: number) => request<PostgresUpgrade>("POST", `${base(sid, eid)}/upgrade`, { to_major }),
  revertUpgrade: (sid: ID, eid: ID) => request<PostgresUpgrade>("POST", `${base(sid, eid)}/upgrade/revert`),
};

export const pgKeys = {
  pooling: (sid: ID, eid: ID) => ["pg-pooling", sid, eid] as const,
  upgrade: (sid: ID, eid: ID) => ["pg-upgrade", sid, eid] as const,
  preflight: (sid: ID, eid: ID, to: number) => ["pg-preflight", sid, eid, to] as const,
};
