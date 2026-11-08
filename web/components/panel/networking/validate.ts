/* Input checks for Settings > Networking and Edge. Pure functions without imports so `node --test` can run
   validate.test.mjs directly. They mirror the server's rules (internal/api/api.go domainRe, domains_extra.go,
   internal/edge/rules.go and settings.go) so the form tells the user what is wrong before the request fails. */

/** Why a host name is rejected. */
export type HostProblem =
  | "empty"
  | "port"
  | "ip"
  | "chars"
  | "label_empty"
  | "label_long"
  | "hyphen"
  | "tld"
  | "single_label"
  | "wildcard_position"
  | "wildcard_short"
  | "too_long"
  | "duplicate";

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const TLD = /^[a-z]{2,63}$/;
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

/** A DNS label (generated domain prefix, private endpoint name): lower-case letters, digits and inner hyphens. */
export const isLabel = (s: string) => LABEL.test(s);

/** 1..65535 written as plain digits. */
export const isPort = (s: string) => /^\d{1,5}$/.test(s) && Number(s) >= 1 && Number(s) <= 65535;

/** Converts non-ASCII labels (한국.com) to punycode with the URL parser; returns the input when it cannot. */
export function toAsciiHost(h: string) {
  if (/^[\x00-\x7f]*$/.test(h)) return h;
  try {
    const wild = h.startsWith("*.");
    const ascii = new URL(`http://${wild ? h.slice(2) : h}`).hostname;
    return wild ? `*.${ascii}` : ascii;
  } catch {
    return h;
  }
}

/**
 * What the user typed into the custom-domain field, as the host the server will store: trimmed, lower-case, without
 * scheme, path, query or a trailing dot, IDN labels in punycode. The port is kept so `checkDomain` can point it out.
 */
export function normalizeHostInput(raw: string) {
  let h = raw.trim().toLowerCase();
  h = h.replace(/^[a-z][a-z0-9+.-]*:\/\//, "");
  h = h.replace(/[/?#].*$/, "");
  h = h.replace(/\.$/, "");
  return toAsciiHost(h);
}

export const isIPv4 = (s: string) => IPV4.test(s);

/** IPv6 literal (with or without brackets), validated by the URL parser. */
export function isIPv6(s: string) {
  const v = s.replace(/^\[(.*)\]$/, "$1");
  if (!v.includes(":") || !/^[0-9a-fA-F:.]+$/.test(v)) return false;
  try {
    new URL(`http://[${v}]/`);
    return true;
  } catch {
    return false;
  }
}

/** Checks a custom domain the way the server does (`domainRe` + the wildcard rule). `existing` = hosts already on the service. */
export function checkDomain(host: string, existing: string[] = []): HostProblem | null {
  if (!host) return "empty";
  if (isIPv4(host) || isIPv6(host)) return "ip";
  if (/:\d*$/.test(host)) return "port";
  if (host.length > 253) return "too_long";
  const wild = host.startsWith("*.");
  const rest = wild ? host.slice(2) : host;
  if (rest.includes("*")) return "wildcard_position";
  if (/[^a-z0-9.-]/.test(rest)) return "chars";
  const labels = rest.split(".");
  if (labels.some((l) => l === "")) return "label_empty";
  if (labels.length < 2) return wild ? "wildcard_short" : "single_label";
  for (const l of labels) {
    if (l.length > 63) return "label_long";
    if (!LABEL.test(l)) return "hyphen";
  }
  if (!TLD.test(labels[labels.length - 1])) return "tld";
  if (existing.some((e) => e.toLowerCase() === host)) return "duplicate";
  return null;
}

/**
 * Host for the network diagnostics: a service name, a private or public DNS name, or an IP address.
 * Returns the cleaned value or null when it cannot be a host.
 */
export function cleanDiagHost(raw: string): string | null {
  const h = raw.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, "").replace(/[/?#].*$/, "").replace(/\.$/, "");
  if (!h || h.length > 253) return null;
  if (isIPv4(h) || isIPv6(h)) return h.replace(/^\[(.*)\]$/, "$1");
  const labels = h.toLowerCase().split(".");
  return labels.every((l) => /^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$/.test(l)) ? h.toLowerCase() : null;
}

/* ── domain extras (internal/api/domains_extra.go) ───────────────────── */

const PATH_RULE = /^\/[A-Za-z0-9/._~*%:@!$&'()+,;=-]{0,255}$/;
const REDIRECT_TO = /^(https?:\/\/)?[A-Za-z0-9.-]+(:[0-9]{1,5})?(\/[A-Za-z0-9/._~%:@!$&'()+,;=?#-]*)?$/;
const AUTH_USER = /^[A-Za-z0-9._@-]{1,64}$/;

export type PathRuleInput = { path: string; target_port?: number | null; strip_prefix?: boolean };
export type PathProblem = "path" | "duplicate" | "strip" | "port";

/** Problem of each path rule (same index), ignoring rows whose path is still empty. */
export function pathRuleProblems(rules: PathRuleInput[]): (PathProblem | null)[] {
  const seen = new Set<string>();
  return rules.map((r) => {
    const p = r.path.trim();
    if (!p) return null;
    if (!PATH_RULE.test(p)) return "path";
    if (seen.has(p)) return "duplicate";
    seen.add(p);
    if (r.strip_prefix && !p.endsWith("/*")) return "strip";
    if (r.target_port != null && (r.target_port < 1 || r.target_port > 65535 || !Number.isInteger(r.target_port))) return "port";
    return null;
  });
}

export type RedirectProblem = "invalid" | "loop";

export function checkRedirectTarget(to: string, host: string): RedirectProblem | null {
  const v = to.trim();
  if (!REDIRECT_TO.test(v) || v.length > 300) return "invalid";
  const target = v.toLowerCase().replace(/^https?:\/\//, "").split("/")[0].replace(/:\d+$/, "");
  return target === host.toLowerCase() ? "loop" : null;
}

export const isAuthUser = (s: string) => AUTH_USER.test(s);

/* ── edge (internal/edge) ────────────────────────────────────────────── */

/** IP address or CIDR (v4 or v6). */
export function isIPOrCIDR(raw: string) {
  const [ip, bits, extra] = raw.split("/");
  if (extra !== undefined) return false;
  if (isIPv4(ip)) return bits === undefined || (/^\d{1,2}$/.test(bits) && Number(bits) <= 32);
  if (isIPv6(ip) && !ip.startsWith("[")) return bits === undefined || (/^\d{1,3}$/.test(bits) && Number(bits) <= 128);
  return false;
}

export const isIPv4OrCIDR = (raw: string) => isIPOrCIDR(raw) && isIPv4(raw.split("/")[0]);

/** Most ranges a TCP proxy allowlist may hold (internal/node MaxTCPAllowCIDRs). */
export const TCP_ALLOW_MAX = 32;

/**
 * A TCP proxy allowlist typed as text: entries split on commas, spaces and new lines. `bad` lists entries the server
 * would refuse (the forwarder only sees IPv4 client addresses, so IPv6 is refused too).
 */
export function parseTcpAllow(text: string): { list: string[]; bad: string[] } {
  const list = [...new Set(text.split(/[\s,]+/).filter(Boolean))];
  return { list, bad: list.filter((x) => !isIPv4OrCIDR(x)) };
}

/** Entries of an allow/deny list that the server would reject. */
export const invalidIPs = (list: string[]) => list.filter((x) => !isIPOrCIDR(x));

/** RFC 7230 token: header names and HTTP methods. */
export const isToken = (s: string) => s.length > 0 && s.length <= 128 && /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/.test(s);

const RESERVED_HEADERS = new Set(["host", "content-length", "transfer-encoding", "connection"]);
export const isReservedHeader = (name: string) => RESERVED_HEADERS.has(name.toLowerCase()) || name.toLowerCase().startsWith("x-keel-");

export type ClauseInput = { attr: string; op: string; key?: string; value: string | string[] };
export type ClauseProblem = "empty" | "too_many" | "too_long" | "key" | "ip" | "ipv4" | "method" | "wildcards";

/** First problem of one condition clause, mirroring compileClause. */
export function clauseProblem(c: ClauseInput): ClauseProblem | null {
  const list = c.op === "in" || c.op === "not_in";
  const vals = (Array.isArray(c.value) ? c.value : [c.value]).map((v) => v.trim());
  if (vals.length === 0 || vals.some((v) => v === "") || (!list && vals.length !== 1)) return "empty";
  if (list && vals.length > 64) return "too_many";
  if (vals.some((v) => v.length > 256)) return "too_long";
  if (c.op === "matches" && vals.some((v) => (v.match(/\*/g)?.length ?? 0) > 8)) return "wildcards";
  if (c.attr === "http.header" && !isToken(c.key ?? "")) return "key";
  if (c.attr === "ip.src" && !vals.every(isIPOrCIDR)) return "ip";
  if (c.attr === "ipv4.src" && !vals.every(isIPv4OrCIDR)) return "ipv4";
  if (c.attr === "http.method" && !vals.every(isToken)) return "method";
  return null;
}

export type ActionInput = {
  action: string;
  params?: {
    status?: number;
    body?: string;
    host?: string;
    location?: string;
    ttl_seconds?: number;
    bypass?: boolean;
    path?: string;
    name?: string;
    value?: string;
  };
};
export type ActionProblem =
  | "block_status"
  | "body_long"
  | "redirect_target"
  | "redirect_location"
  | "redirect_host"
  | "redirect_status"
  | "ttl"
  | "rewrite_path"
  | "header_name"
  | "header_reserved"
  | "header_value";

/** First problem of a rule action, mirroring validateAction. */
export function actionProblem(a: ActionInput): ActionProblem | null {
  const p = a.params ?? {};
  switch (a.action) {
    case "block":
      if (p.status !== undefined && !(Number.isInteger(p.status) && p.status >= 400 && p.status <= 499)) return "block_status";
      if ((p.body ?? "").length > 4096) return "body_long";
      return null;
    case "redirect": {
      if (!p.host === !p.location) return "redirect_target";
      if (p.location) {
        try {
          const u = new URL(p.location);
          if ((u.protocol !== "http:" && u.protocol !== "https:") || !u.host) return "redirect_location";
        } catch {
          return "redirect_location";
        }
      }
      if (p.host && (p.host.length > 253 || /[^a-z0-9.:-]/i.test(p.host))) return "redirect_host";
      if (p.status !== undefined && ![301, 302, 307, 308].includes(p.status)) return "redirect_status";
      return null;
    }
    case "cache_override":
      if (p.bypass) return null;
      return p.ttl_seconds !== undefined && Number.isInteger(p.ttl_seconds) && p.ttl_seconds >= 1 && p.ttl_seconds <= 2592000 ? null : "ttl";
    case "rewrite": {
      const path = p.path ?? "";
      return !path.startsWith("/") || path.startsWith("//") || path.length > 2048 || /[ \r\n#]/.test(path) ? "rewrite_path" : null;
    }
    case "set_header":
      if (!isToken(p.name ?? "")) return "header_name";
      if (isReservedHeader(p.name ?? "")) return "header_reserved";
      if (/[\r\n]/.test(p.value ?? "") || (p.value ?? "").length > 2048) return "header_value";
      return null;
    default:
      return null;
  }
}

/** Seconds as the shortest readable unit (90 → "90s", 3600 → "1h", 86400 → "1d"). */
export function shortDuration(sec: number) {
  if (sec % 86400 === 0) return `${sec / 86400}d`;
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  if (sec % 60 === 0) return `${sec / 60}m`;
  return `${sec}s`;
}

/**
 * Addresses a name resolved to, read from `getent ahosts` or `nslookup` output (the diagnostics' DNS step). The resolver's
 * own address in nslookup output (`Address: 127.0.0.11#53`) is skipped.
 */
export function parseResolved(output: string): string[] {
  const out: string[] = [];
  for (const line of output.split("\n")) {
    if (/^\s*server:/i.test(line)) continue;
    for (const tok of line.split(/[\s,]+/)) {
      if ((isIPv4(tok) || isIPv6(tok)) && !out.includes(tok)) out.push(tok);
    }
  }
  return out;
}
