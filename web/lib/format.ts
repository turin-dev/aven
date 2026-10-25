import { getLocale, t } from "./i18n";

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString(getLocale() === "en" ? "en-US" : "ko-KR", {
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
  const ms = new Date(iso).getTime();
  if (isNaN(ms)) return iso;
  const s = Math.round((Date.now() - ms) / 1000);
  if (s < 5) return t("방금 전");
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
  return t("{h}시간 {m}분", { h: Math.floor(m / 60), m: m % 60 });
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
    if (
      val.length >= 2 &&
      ((val.startsWith('"') && val.endsWith('"')) ||
        (val.startsWith("'") && val.endsWith("'")))
    ) {
      const q = val[0];
      val = val.slice(1, -1);
      if (q === '"') val = val.replace(/\\n/g, "\n").replace(/\\"/g, '"');
    } else {
      const hash = val.search(/\s#/);
      if (hash >= 0) val = val.slice(0, hash).trim();
    }
    out.push([key, val]);
  }
  return out;
}

export function fmtBytes(n: number | null | undefined, digits = 1): string {
  if (n === null || n === undefined || isNaN(n)) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = Math.abs(n);
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  return `${n < 0 ? "-" : ""}${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(digits)} ${u[i]}`;
}

/** Relative time in the future: "3시간 후". */
export function fmtUntil(d: Date | string | null | undefined): string {
  if (!d) return "—";
  const ms = typeof d === "string" ? new Date(d).getTime() : d.getTime();
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
