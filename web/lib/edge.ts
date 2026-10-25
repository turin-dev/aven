/* Edge settings API (CDN caching, WAF / Under Attack Mode, edge rules). See docs/api-v1.md "### Edge: CDN, WAF and edge rules". */

import { ApiError, type ID } from "./api";
import { t } from "@/lib/i18n";

export type HTMLCaching = "auto" | "force" | "never";
export type PurgeOnDeploy = "off" | "html" | "all";

export type EdgeCDN = {
  enabled: boolean;
  html_caching: HTMLCaching;
  /** 1800, 3600, 7200, 14400, 43200 or 86400 */
  default_ttl_seconds: number;
  honor_swr: boolean;
  purge_on_deploy: PurgeOnDeploy;
  last_purged_html: string | null;
  last_purged_all: string | null;
};

export type EdgeRateLimit = { requests: number; window_seconds: number };

export type EdgeWAF = {
  allow_ips: string[];
  deny_ips: string[];
  rate_limit: EdgeRateLimit | null;
  managed_rules: boolean;
  /** null = off; until null = until disabled. */
  under_attack: { since: string; until: string | null } | null;
};

export type EdgeAttr = "ipv4.src" | "ip.src" | "http.host" | "http.path" | "http.header" | "http.method";
export type EdgeOp = "eq" | "neq" | "in" | "not_in" | "contains" | "matches";

export type EdgeClause = { attr: EdgeAttr; op: EdgeOp; key?: string; value: string | string[] };
export type EdgeExpr = EdgeClause | { and: EdgeExpr[] } | { or: EdgeExpr[] } | { not: EdgeExpr };

export type EdgeActionName = "block" | "allow" | "challenge" | "redirect" | "cache_override" | "rewrite" | "set_header";

export type EdgeActionParams = {
  status?: number;
  body?: string;
  host?: string;
  location?: string;
  preserve_path?: boolean;
  preserve_query?: boolean;
  ttl_seconds?: number;
  bypass?: boolean;
  path?: string;
  name?: string;
  value?: string;
  target?: "response" | "request";
};

export type EdgeRule = {
  id?: string;
  description?: string;
  priority: number;
  enabled?: boolean;
  if: EdgeExpr;
  then: { action: EdgeActionName; params?: EdgeActionParams };
};

export type EdgeRuleset = { version: 1; rules: EdgeRule[] };

export type EdgeSettings = {
  cdn: EdgeCDN;
  waf: EdgeWAF;
  rules: EdgeRuleset;
  /** The service has a public (non-redirect) domain. */
  available: boolean;
  domains: string[];
  under_attack_active: boolean;
};

export type EdgePatch = { cdn?: Partial<Omit<EdgeCDN, "last_purged_html" | "last_purged_all">>; waf?: Partial<Omit<EdgeWAF, "under_attack">> };

const enc = encodeURIComponent;
const base = (sid: ID, eid: ID) => `/api/services/${enc(sid)}/instances/${enc(eid)}/edge`;

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "include",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (j?.error) msg = j.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg);
  }
  return (await res.json()) as T;
}

export const edgeApi = {
  get: (sid: ID, eid: ID) => call<EdgeSettings>("GET", base(sid, eid)),
  patch: (sid: ID, eid: ID, patch: EdgePatch) => call<EdgeSettings>("PATCH", base(sid, eid), patch),
  putRules: (sid: ID, eid: ID, rules: EdgeRuleset) => call<EdgeSettings>("PUT", `${base(sid, eid)}/rules`, rules),
  purge: (sid: ID, eid: ID, scope: "html" | "all") => call<EdgeSettings>("POST", `${base(sid, eid)}/cdn/purge`, { scope }),
  underAttack: (sid: ID, eid: ID, duration: "" | "1h" | "3h" | "12h" | "24h") =>
    call<EdgeSettings>("POST", `${base(sid, eid)}/waf/under-attack`, { duration }),
  stopUnderAttack: (sid: ID, eid: ID) => call<EdgeSettings>("DELETE", `${base(sid, eid)}/waf/under-attack`),
};

export const edgeKey = (sid: ID, eid: ID) => ["edge", sid, eid] as const;

export const TTL_OPTIONS = [
  { value: 1800, label: t("30분") },
  { value: 3600, label: t("1시간") },
  { value: 7200, label: t("2시간") },
  { value: 14400, label: t("4시간") },
  { value: 43200, label: t("12시간") },
  { value: 86400, label: t("1일") },
];

export const ATTR_OPS: Record<EdgeAttr, EdgeOp[]> = {
  "ipv4.src": ["eq", "neq", "in", "not_in"],
  "ip.src": ["eq", "neq", "in", "not_in"],
  "http.host": ["eq", "neq", "in", "matches"],
  "http.path": ["eq", "neq", "contains", "matches"],
  "http.header": ["eq", "neq", "contains", "matches"],
  "http.method": ["eq", "neq", "in", "not_in"],
};

export const isClause = (e: EdgeExpr): e is EdgeClause => "attr" in e;

/** The builder edits a flat list of clauses joined by and/or; anything nested is edited as JSON. */
export function flatConditions(e: EdgeExpr): { join: "and" | "or"; clauses: EdgeClause[] } | null {
  if (isClause(e)) return { join: "and", clauses: [e] };
  if ("and" in e && e.and.every(isClause)) return { join: "and", clauses: e.and as EdgeClause[] };
  if ("or" in e && e.or.every(isClause)) return { join: "or", clauses: e.or as EdgeClause[] };
  return null;
}

export function joinConditions(join: "and" | "or", clauses: EdgeClause[]): EdgeExpr {
  if (clauses.length === 1) return clauses[0];
  return join === "and" ? { and: clauses } : { or: clauses };
}
