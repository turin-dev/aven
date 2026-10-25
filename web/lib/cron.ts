// Minimal 5-field cron parser (UTC), next-run calculation and Korean descriptions.
import { dateLocale, t } from "@/lib/i18n";

type Field = { any: boolean; values: Set<number> };
export type Cron = { min: Field; hour: Field; dom: Field; mon: Field; dow: Field };

const RANGES: [number, number][] = [
  [0, 59],
  [0, 23],
  [1, 31],
  [1, 12],
  [0, 7],
];

// Short weekday names from Intl (Sun..Sat for 2024-01-07..13 in UTC): "일".."토" in Korean, "Sun".."Sat" in English.
const DOW_NAMES = Array.from({ length: 7 }, (_, d) =>
  new Intl.DateTimeFormat(dateLocale(), { weekday: "short", timeZone: "UTC" }).format(new Date(Date.UTC(2024, 0, 7 + d))),
);
const MON_NAMES = ["", t("1월"), t("2월"), t("3월"), t("4월"), t("5월"), t("6월"), t("7월"), t("8월"), t("9월"), t("10월"), t("11월"), t("12월")];
const MON_ALIAS: Record<string, number> = { jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12 };
const DOW_ALIAS: Record<string, number> = { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 };

const MACROS: Record<string, string> = {
  "@yearly": "0 0 1 1 *",
  "@annually": "0 0 1 1 *",
  "@monthly": "0 0 1 * *",
  "@weekly": "0 0 * * 0",
  "@daily": "0 0 * * *",
  "@midnight": "0 0 * * *",
  "@hourly": "0 * * * *",
};

function parseField(src: string, i: number): Field {
  const [lo, hi] = RANGES[i];
  const alias = i === 3 ? MON_ALIAS : i === 4 ? DOW_ALIAS : null;
  const num = (s: string) => {
    const a = alias?.[s.toLowerCase()];
    const n = a ?? Number(s);
    if (!Number.isInteger(n) || n < lo || n > hi) throw new Error(t("{s}: {lo}–{hi} 범위를 벗어났습니다", { s, lo, hi }));
    return n;
  };
  const values = new Set<number>();
  let any = false;
  for (const part of src.split(",")) {
    const [rng, stepS] = part.split("/");
    const step = stepS === undefined ? 1 : Number(stepS);
    if (!Number.isInteger(step) || step < 1) throw new Error(t("{part}: 간격이 올바르지 않습니다", { part }));
    let a: number, b: number;
    if (rng === "*") {
      a = lo;
      b = hi;
      if (stepS === undefined) any = true;
    } else if (rng.includes("-")) {
      const [x, y] = rng.split("-");
      a = num(x);
      b = num(y);
      if (a > b) throw new Error(t("{rng}: 범위가 거꾸로입니다", { rng }));
    } else {
      a = num(rng);
      b = stepS === undefined ? a : hi;
    }
    for (let v = a; v <= b; v += step) values.add(i === 4 && v === 7 ? 0 : v);
  }
  return { any, values };
}

export function parseCron(expr: string): Cron {
  const e = MACROS[expr.trim().toLowerCase()] ?? expr.trim();
  const parts = e.split(/\s+/);
  if (parts.length !== 5) throw new Error(t("필드 5개(분 시 일 월 요일)가 필요합니다"));
  const [min, hour, dom, mon, dow] = parts.map(parseField);
  return { min, hour, dom, mon, dow };
}

export function cronError(expr: string): string | null {
  try {
    parseCron(expr);
    return null;
  } catch (e) {
    return (e as Error).message;
  }
}

function dayMatches(c: Cron, d: Date) {
  const dom = c.dom.values.has(d.getUTCDate());
  const dow = c.dow.values.has(d.getUTCDay());
  if (c.dom.any && c.dow.any) return true;
  if (c.dom.any) return dow;
  if (c.dow.any) return dom;
  return dom || dow;
}

/** Next `count` run times after `from` (UTC semantics). */
export function nextRuns(expr: string, count = 1, from = new Date()): Date[] {
  let c: Cron;
  try {
    c = parseCron(expr);
  } catch {
    return [];
  }
  const out: Date[] = [];
  const d = new Date(from.getTime());
  d.setUTCSeconds(0, 0);
  d.setUTCMinutes(d.getUTCMinutes() + 1);
  const limit = from.getTime() + 366 * 24 * 3600_000 * 2;
  while (out.length < count && d.getTime() < limit) {
    if (!c.mon.values.has(d.getUTCMonth() + 1)) {
      d.setUTCMonth(d.getUTCMonth() + 1, 1);
      d.setUTCHours(0, 0, 0, 0);
      continue;
    }
    if (!dayMatches(c, d)) {
      d.setUTCDate(d.getUTCDate() + 1);
      d.setUTCHours(0, 0, 0, 0);
      continue;
    }
    if (!c.hour.values.has(d.getUTCHours())) {
      d.setUTCHours(d.getUTCHours() + 1, 0, 0, 0);
      continue;
    }
    if (!c.min.values.has(d.getUTCMinutes())) {
      d.setUTCMinutes(d.getUTCMinutes() + 1, 0, 0);
      continue;
    }
    out.push(new Date(d.getTime()));
    d.setUTCMinutes(d.getUTCMinutes() + 1, 0, 0);
  }
  return out;
}

const p2 = (n: number) => String(n).padStart(2, "0");

function single(f: Field): number | null {
  return !f.any && f.values.size === 1 ? [...f.values][0] : null;
}
function stepOf(src: string): number | null {
  const m = /^\*\/(\d+)$/.exec(src);
  return m ? Number(m[1]) : null;
}
function list(f: Field, fmt: (n: number) => string = String) {
  return [...f.values].sort((a, b) => a - b).map(fmt).join(", ");
}

/** Korean description such as "매일 18:00 (UTC)". */
export function describeCron(expr: string): string {
  let c: Cron;
  const e = (MACROS[expr.trim().toLowerCase()] ?? expr.trim()).split(/\s+/);
  try {
    c = parseCron(expr);
  } catch {
    return "";
  }
  const [mS, hS] = e;
  const m = single(c.min);
  const h = single(c.hour);
  let time: string;
  const ms = stepOf(mS);
  const hs = stepOf(hS);
  if (c.min.any && c.hour.any) time = t("매분");
  else if (ms && c.hour.any) time = t("{ms}분마다", { ms });
  else if (m !== null && c.hour.any) time = m === 0 ? t("매시 정각") : t("매시 {min}분", { min: m });
  else if (m !== null && hs) time = t("{hs}시간마다 {min}분", { hs, min: m });
  else if (m !== null && h !== null) time = `${p2(h)}:${p2(m)}`;
  else if (m !== null) time = t("{hours} {min}분", { hours: list(c.hour, (x) => t("{x}시", { x })), min: m });
  else time = t("{hours} {mins}", { hours: list(c.hour, (x) => t("{x}시", { x })), mins: list(c.min, (x) => t("{x}분", { x })) });

  let day: string;
  if (c.dom.any && c.dow.any) day = c.mon.any ? (h !== null || (!c.hour.any && !hs) ? t("매일") : "") : "";
  else if (!c.dow.any && c.dom.any) {
    const ds = [...c.dow.values].sort();
    day = ds.join() === "1,2,3,4,5" ? t("평일") : ds.join() === "0,6" ? t("주말") : t("매주 {days}요일", { days: ds.map((d) => DOW_NAMES[d]).join("·") });
  } else if (!c.dom.any && c.dow.any) day = t("매월 {days}", { days: list(c.dom, (x) => t("{x}일", { x })) });
  else day = t("매월 {dom} 또는 {dow}요일", { dom: list(c.dom, (x) => t("{x}일", { x })), dow: [...c.dow.values].map((d) => DOW_NAMES[d]).join("·") });
  const mon = c.mon.any ? "" : t("{months} ", { months: list(c.mon, (x) => MON_NAMES[x]) });
  return `${mon}${day ? day + " " : ""}${time}`.trim() + " (UTC)";
}
