/* v1.5 API clients for canvas groups, observability dashboards, usage limits and function code.
   See docs/api-v1.md "### Canvas service groups", "### Observability dashboards", "### Hard usage limits", "### Functions". */

import { ApiError, type CanvasGroup, type ID, type MetricRange, type UsageLimit, type UsageSuspension, type UsageTotals } from "./api";
import { t } from "@/lib/i18n";

const enc = encodeURIComponent;

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
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

/* ── canvas groups ─────────────────────────────────────────────────── */

export const GROUP_COLORS = ["gray", "red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"] as const;
export const GROUP_COLOR_LABEL: Record<(typeof GROUP_COLORS)[number], string> = {
  gray: t("회색"),
  red: t("빨강"),
  orange: t("주황"),
  yellow: t("노랑"),
  green: t("초록"),
  teal: t("청록"),
  blue: t("파랑"),
  purple: t("보라"),
  pink: t("분홍"),
};
const GROUP_HEX: Record<string, string> = {
  gray: "#8b8a99",
  red: "#e5484d",
  orange: "#f76b15",
  yellow: "#f5d90a",
  green: "#30a46c",
  teal: "#12a594",
  blue: "#3e63dd",
  purple: "#8e4ec6",
  pink: "#d6409f",
};
/** CSS color of a group color name or #rrggbb. */
export const groupHex = (c: string) => GROUP_HEX[c] ?? (/^#[0-9a-f]{6}$/i.test(c) ? c : GROUP_HEX.gray);

export type GroupInput = { name?: string; color?: string; service_ids?: ID[]; rect?: CanvasGroup["rect"] };

/* ── dashboards ────────────────────────────────────────────────────── */

export type WidgetType = "metric" | "logs" | "http" | "events";
export type WidgetMetric = "cpu_percent" | "memory_bytes" | "net_rx_bytes" | "net_tx_bytes" | "disk_bytes";
export const WIDGET_METRICS: { value: WidgetMetric; label: string }[] = [
  { value: "cpu_percent", label: "CPU" },
  { value: "memory_bytes", label: t("메모리") },
  { value: "net_rx_bytes", label: t("네트워크 수신") },
  { value: "net_tx_bytes", label: t("네트워크 송신") },
  { value: "disk_bytes", label: t("디스크") },
];
export const WIDGET_TYPES: { value: WidgetType; label: string; desc: string }[] = [
  { value: "metric", label: t("지표 차트"), desc: t("서비스별 CPU, 메모리, 네트워크, 디스크") },
  { value: "logs", label: t("로그"), desc: t("환경 또는 서비스 하나의 실시간 로그") },
  { value: "http", label: t("HTTP 로그"), desc: t("서비스 하나의 요청 기록") },
  { value: "events", label: t("이벤트"), desc: t("프로젝트 활동(배포, 설정 변경 등)") },
];

/** Grid cell: x/y = position (sort order: y, then x), w = columns (1..12), h = rows (1..8). */
export type Widget = {
  id: ID;
  type: WidgetType;
  title: string;
  service_id?: ID | null;
  environment_id?: ID | null;
  metric?: WidgetMetric | "";
  range: MetricRange;
  x: number;
  y: number;
  w: number;
  h: number;
};
export type Dashboard = { id: ID; project_id: ID; name: string; widgets: Widget[]; created_at: string; updated_at: string };
export const DASHBOARD_COLUMNS = 12;
export const MAX_WIDGETS = 30;

/* ── usage limits ──────────────────────────────────────────────────── */

export type UsageLimitStatus = {
  project_id: ID;
  /** YYYY-MM (UTC) */
  month: string;
  usage_limit: UsageLimit | null;
  usage_suspension: UsageSuspension | null;
  /** Includes the hour that is not rolled up yet. */
  month_to_date: UsageTotals;
  exceeded: ("cpu_core_hours" | "memory_gb_hours" | "egress_gb")[];
};
export type ResumeResult = { restarted: ID[]; failed: Record<ID, string> };

/* ── functions ─────────────────────────────────────────────────────── */

export type FunctionCode = {
  code: string;
  runtime: "bun";
  sha256: string;
  updated_at: string | null;
  /** Code hash of the current deployment (null before the first deploy). */
  deployed_sha256: string | null;
  max_bytes: number;
};

const inst = (sid: ID, eid: ID) => `/services/${enc(sid)}/instances/${enc(eid)}`;

export const layoutApi = {
  groups: (eid: ID) => call<CanvasGroup[]>("GET", `/environments/${enc(eid)}/groups`),
  createGroup: (eid: ID, input: GroupInput) => call<CanvasGroup>("POST", `/environments/${enc(eid)}/groups`, input),
  updateGroup: (eid: ID, gid: ID, input: GroupInput) => call<CanvasGroup>("PATCH", `/environments/${enc(eid)}/groups/${enc(gid)}`, input),
  deleteGroup: (eid: ID, gid: ID) => call<void>("DELETE", `/environments/${enc(eid)}/groups/${enc(gid)}`),

  dashboards: (pid: ID) => call<Dashboard[]>("GET", `/projects/${enc(pid)}/dashboards`),
  createDashboard: (pid: ID, input: { name: string; widgets?: Partial<Widget>[] }) => call<Dashboard>("POST", `/projects/${enc(pid)}/dashboards`, input),
  updateDashboard: (pid: ID, id: ID, input: { name?: string; widgets?: Partial<Widget>[] }) =>
    call<Dashboard>("PATCH", `/projects/${enc(pid)}/dashboards/${enc(id)}`, input),
  deleteDashboard: (pid: ID, id: ID) => call<void>("DELETE", `/projects/${enc(pid)}/dashboards/${enc(id)}`),

  usageLimit: (pid: ID) => call<UsageLimitStatus>("GET", `/projects/${enc(pid)}/usage/limit`),
  /** Project admin. 409 when not suspended or still over the limit. */
  resumeUsage: (pid: ID) => call<ResumeResult>("POST", `/projects/${enc(pid)}/usage/resume`),

  functionCode: (sid: ID, eid: ID) => call<FunctionCode>("GET", `${inst(sid, eid)}/function`),
  saveFunctionCode: (sid: ID, eid: ID, code: string, deploy = false) =>
    call<FunctionCode>("PUT", `${inst(sid, eid)}/function${deploy ? "?deploy=1" : ""}`, { code }),
};

export const layoutKeys = {
  dashboards: (pid: ID) => ["dashboards", pid] as const,
  usageLimit: (pid: ID) => ["usage-limit", pid] as const,
  functionCode: (sid: ID, eid: ID) => ["function-code", sid, eid] as const,
};
