// Pure helpers for the log viewers (no React, no imports) so they can be unit tested with `node --test`.

/* ── Levels and filters ──────────────────────────────────────────────── */

export type Level = "error" | "warn" | "stage" | "ok" | "info";
export const LEVELS: Level[] = ["error", "warn", "stage", "ok", "info"];

export function levelOf(body: string): Level {
  if (body.startsWith("==>")) return "stage";
  if (/\berror\b|\bfatal\b|panic:/i.test(body)) return "error";
  if (/^warn(ing)?\b|\bWARN\b/.test(body)) return "warn";
  if (/healthcheck OK|배포 완료/.test(body)) return "ok";
  return "info";
}

type Token = { neg: boolean; raw: string };
function tokens(q: string): Token[] {
  return (q.match(/-?"[^"]*"|\S+/g) ?? []).map((tok) => {
    const neg = tok.startsWith("-") && tok.length > 1;
    const raw = (neg ? tok.slice(1) : tok).replace(/^"|"$/g, "").toLowerCase();
    return { neg, raw };
  });
}

/** Tokens split by spaces (quotes keep phrases); "-term" excludes. All tokens must match. */
export function makeFilter(q: string): (s: string) => boolean {
  const toks = tokens(q);
  if (!toks.length) return () => true;
  return (s) => {
    const l = s.toLowerCase();
    return toks.every((tok) => (tok.raw ? l.includes(tok.raw) === !tok.neg : true));
  };
}

/** The positive (non-excluded) terms of a filter query, lower-cased, for highlighting. */
export function highlightTerms(q: string): string[] {
  return [...new Set(tokens(q).filter((tok) => !tok.neg && tok.raw).map((tok) => tok.raw))];
}

/** Case-insensitive match ranges [start, end) of `terms` in `text`, sorted and merged. */
export function matchRanges(text: string, terms: string[]): [number, number][] {
  if (!terms.length || !text) return [];
  const l = text.toLowerCase();
  const out: [number, number][] = [];
  for (const term of terms) {
    for (let i = l.indexOf(term); i >= 0; i = l.indexOf(term, i + term.length)) out.push([i, i + term.length]);
  }
  if (out.length < 2) return out;
  out.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const merged: [number, number][] = [out[0]];
  for (const r of out.slice(1)) {
    const last = merged[merged.length - 1];
    if (r[0] <= last[1]) last[1] = Math.max(last[1], r[1]);
    else merged.push(r);
  }
  return merged;
}

/* ── ANSI ────────────────────────────────────────────────────────────── */

export type AnsiStyle = { fg?: string; bg?: string; bold?: boolean; dim?: boolean; italic?: boolean; underline?: boolean };
export type Segment = { text: string; st?: AnsiStyle; mark?: boolean };

/** Named colours are theme tokens (Tailwind classes resolved by the viewer); 256/RGB colours are CSS colours. */
const NAMED = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"];

const ESC_RE = /\x1b(?:\[([0-9;:?]*)([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[()#][0-9A-Za-z]|[@-_])/g;

function color256(n: number): string {
  if (n < 8) return NAMED[n];
  if (n < 16) return `bright-${NAMED[n - 8]}`;
  if (n < 232) {
    const i = n - 16;
    const v = (x: number) => (x === 0 ? 0 : 55 + x * 40);
    return `rgb(${v(Math.floor(i / 36))},${v(Math.floor(i / 6) % 6)},${v(i % 6)})`;
  }
  const g = 8 + (n - 232) * 10;
  return `rgb(${g},${g},${g})`;
}

function applySgr(st: AnsiStyle, params: string): AnsiStyle {
  const ps = params === "" ? [0] : params.split(/[;:]/).map((p) => (p === "" ? 0 : parseInt(p, 10)));
  let next: AnsiStyle = { ...st };
  for (let i = 0; i < ps.length; i++) {
    const p = ps[i];
    if (p === 0) next = {};
    else if (p === 1) next.bold = true;
    else if (p === 2) next.dim = true;
    else if (p === 3) next.italic = true;
    else if (p === 4) next.underline = true;
    else if (p === 22) next.bold = next.dim = undefined;
    else if (p === 23) next.italic = undefined;
    else if (p === 24) next.underline = undefined;
    else if (p >= 30 && p <= 37) next.fg = NAMED[p - 30];
    else if (p >= 90 && p <= 97) next.fg = `bright-${NAMED[p - 90]}`;
    else if (p === 39) next.fg = undefined;
    else if (p >= 40 && p <= 47) next.bg = NAMED[p - 40];
    else if (p >= 100 && p <= 107) next.bg = `bright-${NAMED[p - 100]}`;
    else if (p === 49) next.bg = undefined;
    else if (p === 38 || p === 48) {
      let col: string | undefined;
      if (ps[i + 1] === 5 && ps[i + 2] !== undefined) {
        col = color256(ps[i + 2] & 255);
        i += 2;
      } else if (ps[i + 1] === 2 && ps[i + 4] !== undefined) {
        col = `rgb(${ps[i + 2] & 255},${ps[i + 3] & 255},${ps[i + 4] & 255})`;
        i += 4;
      }
      if (p === 38) next.fg = col;
      else next.bg = col;
    }
  }
  return next;
}

const isPlain = (st: AnsiStyle) => !st.fg && !st.bg && !st.bold && !st.dim && !st.italic && !st.underline;

/** Text after the last carriage return: progress bars redraw a line with `\r`. */
function lastFrame(s: string): string {
  const s2 = s.endsWith("\r") ? s.slice(0, -1) : s;
  const i = s2.lastIndexOf("\r");
  return i < 0 ? s2 : s2.slice(i + 1);
}

/** Splits a log line into styled segments (SGR colours kept, other escape sequences dropped). */
export function parseAnsi(input: string): Segment[] {
  const s = input.includes("\r") ? lastFrame(input) : input;
  if (!s.includes("\x1b")) return [{ text: s }];
  const out: Segment[] = [];
  let st: AnsiStyle = {};
  let last = 0;
  ESC_RE.lastIndex = 0;
  for (let m = ESC_RE.exec(s); m; m = ESC_RE.exec(s)) {
    if (m.index > last) out.push(isPlain(st) ? { text: s.slice(last, m.index) } : { text: s.slice(last, m.index), st });
    if (m[2] === "m") st = applySgr(st, m[1] ?? "");
    last = ESC_RE.lastIndex;
  }
  if (last < s.length) out.push(isPlain(st) ? { text: s.slice(last) } : { text: s.slice(last), st });
  return out.length ? out : [{ text: "" }];
}

/** Plain text of a log line (no escape sequences, last `\r` frame). */
export function stripAnsi(s: string): string {
  const f = s.includes("\r") ? lastFrame(s) : s;
  return f.includes("\x1b") ? f.replace(ESC_RE, "") : f;
}

/** Splits segments at the given ranges of their concatenated text, flagging the covered parts with `mark`. */
export function markSegments(segs: Segment[], ranges: [number, number][]): Segment[] {
  if (!ranges.length) return segs;
  const out: Segment[] = [];
  let pos = 0;
  let r = 0;
  for (const seg of segs) {
    const end = pos + seg.text.length;
    let cur = pos;
    while (cur < end) {
      while (r < ranges.length && ranges[r][1] <= cur) r++;
      const rg = ranges[r];
      if (!rg || rg[0] >= end) {
        out.push({ ...seg, text: seg.text.slice(cur - pos) });
        cur = end;
      } else if (rg[0] > cur) {
        out.push({ ...seg, text: seg.text.slice(cur - pos, rg[0] - pos) });
        cur = rg[0];
      } else {
        const stop = Math.min(rg[1], end);
        out.push({ ...seg, text: seg.text.slice(cur - pos, stop - pos), mark: true });
        cur = stop;
      }
    }
    pos = end;
  }
  return out;
}

/* ── Timestamps ──────────────────────────────────────────────────────── */

export const TS_RE = /^(\d{2}:\d{2}:\d{2}) (.*)$/s;

/** Server log prefixes are UTC HH:MM:SS; anchor them to the deployment's UTC date (rolling over midnight). */
export function fromUtcHms(base: Date, hms: string): Date {
  const [h, m, s] = hms.split(":").map(Number);
  const d = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth(), base.getUTCDate(), h, m, s));
  if (d.getTime() < base.getTime() - 3600_000) d.setUTCDate(d.getUTCDate() + 1);
  return d;
}

/* ── Deployment stages ───────────────────────────────────────────────── */

export type StageId = "initializing" | "building" | "pre_deploy" | "deploying" | "healthchecking" | "live";

/** "==> <word>" markers the server (status changes) and builders print, mapped to stepper stages. */
const STAGE_WORDS: Record<string, StageId> = {
  initializing: "initializing",
  cloning: "building",
  pulling: "building",
  building: "building",
  pre_deploy: "pre_deploy",
  "pre-deploy": "pre_deploy",
  deploying: "deploying",
  starting: "deploying",
  healthchecking: "healthchecking",
  healthcheck: "healthchecking",
  live: "live",
};

/**
 * First time each stage appeared in a build log ("HH:MM:SS ==> building" …), as epoch ms.
 * Stages are only recorded in order, so a later "==> healthcheck OK" does not reset an earlier stage.
 */
export function stageTimes(lines: string[], base: Date): Partial<Record<StageId, number>> {
  const out: Partial<Record<StageId, number>> = {};
  for (const line of lines) {
    const m = TS_RE.exec(line);
    if (!m || !m[2].startsWith("==> ")) continue;
    const word = /^==> ([\w-]+)/.exec(m[2])?.[1]?.toLowerCase() ?? "";
    const id = STAGE_WORDS[word] ?? (/배포 완료/.test(m[2]) ? "live" : undefined);
    if (id && out[id] === undefined) out[id] = fromUtcHms(base, m[1]).getTime();
  }
  return out;
}

/** The latest BuildKit step ("#7 [4/6] RUN npm ci") in a build log, for a live progress line. */
export function buildStep(lines: string[]): { n: number; of: number; text: string } | null {
  for (let i = lines.length - 1, seen = 0; i >= 0 && seen < 400; i--, seen++) {
    const m = /#\d+ \[(?:[\w.-]+ )?(\d+)\/(\d+)\] (.+)$/.exec(stripAnsi(lines[i]));
    if (m) return { n: Number(m[1]), of: Number(m[2]), text: m[3].trim() };
  }
  return null;
}

/* ── Streams ─────────────────────────────────────────────────────────── */

/** Exponential reconnect delay with ±50% jitter so many tabs don't reconnect in lock-step. */
export function backoffDelay(attempt: number, rand: () => number = Math.random, base = 1000, cap = 15000): number {
  const d = Math.min(base * 2 ** Math.max(0, attempt - 1), cap);
  return Math.round(d / 2 + rand() * (d / 2));
}

/** Formats elapsed milliseconds as "12s", "3m 04s" or "1h 02m". Locale-neutral: used inside dense steppers. */
export function fmtSpan(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  const p = (n: number) => String(n).padStart(2, "0");
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${p(s % 60)}s`;
  return `${Math.floor(m / 60)}h ${p(m % 60)}m`;
}
