import { dateLocale, t } from "./i18n";

/** Milliseconds since the epoch, or NaN for a missing or unparsable value. */
function toMs(d: Date | string | number | null | undefined): number {
  if (d === null || d === undefined || d === "") return NaN;
  if (d instanceof Date) return d.getTime();
  return typeof d === "number" ? d : new Date(d).getTime();
}

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString(dateLocale(), {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function fmtRelative(iso: string | null | undefined): string {
  if (!iso) return "—";
  const ms = toMs(iso);
  if (isNaN(ms)) return iso;
  const s = Math.round((Date.now() - ms) / 1000);
  // A timestamp slightly ahead of the browser clock (server clock skew) still reads as "just now".
  if (s < 5) return s < -60 ? fmtTime(iso) : t("방금 전");
  if (s < 60) return t("{n}초 전", { n: s });
  const m = Math.floor(s / 60);
  if (m < 60) return t("{n}분 전", { n: m });
  const h = Math.floor(m / 60);
  if (h < 24) return t("{n}시간 전", { n: h });
  const d = Math.floor(h / 24);
  if (d < 30) return t("{n}일 전", { n: d });
  return fmtTime(iso);
}

export function fmtDuration(
  start: string | null | undefined,
  end: string | null | undefined,
): string {
  if (!start) return "—";
  const a = new Date(start).getTime();
  const b = end ? new Date(end).getTime() : Date.now();
  if (isNaN(a) || isNaN(b)) return "—";
  const s = Math.max(0, Math.round((b - a) / 1000));
  if (s < 60) return t("{n}초", { n: s });
  const m = Math.floor(s / 60);
  if (m < 60) return t("{m}분 {s}초", { m, s: s % 60 });
  const h = Math.floor(m / 60);
  if (h < 24) return t("{h}시간 {m}분", { h, m: m % 60 });
  return t("{d}일 {h}시간", { d: Math.floor(h / 24), h: h % 24 });
}

export function shortSha(sha: string | null | undefined): string {
  return sha ? sha.slice(0, 7) : "—";
}

/** Parse .env-style text into key/value pairs. */
export function parseDotenv(text: string): [string, string][] {
  const out: [string, string][] = [];
  for (const raw of text.split(/\r?\n/)) {
    let line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    if (line.startsWith("export ")) line = line.slice(7).trim();
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    let val = line.slice(eq + 1).trim();
    const q = val[0];
    // Closing quote, optionally followed by an inline comment: KEY="a b" # note
    const close = q === '"' || q === "'" ? closingQuote(val, q) : -1;
    if (close > 0 && /^\s*(#.*)?$/.test(val.slice(close + 1))) {
      val = val.slice(1, close);
      if (q === '"') val = val.replace(/\\n/g, "\n").replace(/\\"/g, '"');
    } else {
      const hash = val.search(/\s#/);
      if (hash >= 0) val = val.slice(0, hash).trim();
    }
    out.push([key, val]);
  }
  return out;
}

/** Index of the quote that closes val[0], skipping backslash escapes inside double quotes. */
function closingQuote(val: string, q: string): number {
  for (let i = 1; i < val.length; i++) {
    if (q === '"' && val[i] === "\\") i++;
    else if (val[i] === q) return i;
  }
  return -1;
}

/** Decimal (SI) units: 1 KB = 1000 B, matching the server's metrics and usage figures. */
export function fmtBytes(n: number | null | undefined, digits = 1): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "—";
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  let v = Math.abs(n);
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  // Rounding can cross a display boundary (99.96 -> "100.0", 999.6 KB -> "1000 KB"); re-check after rounding.
  let text = i === 0 ? String(Math.round(v)) : v.toFixed(digits);
  if (Number(text) >= 100) text = String(Math.round(v));
  if (Number(text) >= 1000 && i < u.length - 1) {
    i++;
    text = (v / 1000).toFixed(digits);
  }
  return `${n < 0 ? "-" : ""}${text} ${u[i]}`;
}

/** Relative time in the future: "3시간 후". */
export function fmtUntil(d: Date | string | null | undefined): string {
  const ms = toMs(d);
  if (isNaN(ms)) return "—";
  const s = Math.round((ms - Date.now()) / 1000);
  if (s < 60) return t("곧");
  const m = Math.floor(s / 60);
  if (m < 60) return t("{n}분 후", { n: m });
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? t("{h}시간 {m}분 후", { h, m: m % 60 }) : t("{h}시간 후", { h });
  return t("{n}일 후", { n: Math.floor(h / 24) });
}

export function repoLabel(url: string | null | undefined): string {
  if (!url) return "";
  return url
    .replace(/^[a-z+]+:\/\//, "")
    .replace(/^git@/, "")
    .replace(/\.git$/, "")
    .replace(/^github\.com[/:]/, "")
    .replace(/^\/+/, "");
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
