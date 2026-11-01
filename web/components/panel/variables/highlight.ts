// Syntax colouring for the RAW editor. Pure: text in, coloured segments per line out.
import { REF_RE } from "@/lib/refs";

export type Tok = { text: string; cls?: string };

export const TOK = {
  comment: "text-gray-400",
  key: "text-pink-600",
  punct: "text-gray-400",
  value: "text-foreground",
  string: "text-green-600",
  ref: "rounded-[3px] bg-pink-100/60 text-pink-600",
} as const;

/** Splits value text so `${{ refs }}` get their own colour. */
function withRefs(text: string, cls: string): Tok[] {
  const out: Tok[] = [];
  let last = 0;
  for (const m of text.matchAll(REF_RE)) {
    if (m.index! > last) out.push({ text: text.slice(last, m.index), cls });
    out.push({ text: m[0], cls: TOK.ref });
    last = m.index! + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last), cls });
  return out;
}

/** Index just past the closing quote `q` in `s` from `from` (backslash escapes count only in "…"), or -1. */
function closeAt(s: string, from: number, q: string): number {
  for (let i = from; i < s.length; i++) {
    if (q === '"' && s[i] === "\\") {
      i++;
      continue;
    }
    if (s[i] === q) return i + 1;
  }
  return -1;
}

/** After a closing quote: spaces and an optional comment. */
function tail(s: string): Tok[] {
  if (!s) return [];
  const m = /^(\s*)(#.*)?(.*)$/.exec(s)!;
  return [{ text: m[1] }, ...(m[2] ? [{ text: m[2], cls: TOK.comment }] : []), ...(m[3] ? [{ text: m[3], cls: TOK.value }] : [])];
}

/** .env text → coloured lines, following multi-line quoted values across lines. */
export function highlightEnv(text: string): Tok[][] {
  const lines: Tok[][] = [];
  let open: string | null = null; // quote char of a value that continues on the next line
  for (const line of text.split("\n")) {
    if (open) {
      const end = closeAt(line, 0, open);
      if (end < 0) {
        lines.push(withRefs(line, TOK.string));
        continue;
      }
      lines.push([...withRefs(line.slice(0, end), TOK.string), ...tail(line.slice(end))]);
      open = null;
      continue;
    }
    if (/^\s*(#|$)/.test(line)) {
      lines.push([{ text: line, cls: TOK.comment }]);
      continue;
    }
    const m = /^(\s*)(export[ \t]+)?([^=]*?)(\s*=\s*)(.*)$/.exec(line);
    if (!m) {
      lines.push([{ text: line, cls: TOK.value }]);
      continue;
    }
    const [, lead, exp, key, eq, rest] = m;
    const head: Tok[] = [{ text: lead }, ...(exp ? [{ text: exp, cls: TOK.punct }] : []), { text: key, cls: TOK.key }, { text: eq, cls: TOK.punct }];
    const q = rest[0];
    if (q === '"' || q === "'" || q === "`") {
      const end = closeAt(rest, 1, q);
      if (end < 0) {
        open = q;
        lines.push([...head, ...withRefs(rest, TOK.string)]);
      } else lines.push([...head, ...withRefs(rest.slice(0, end), TOK.string), ...tail(rest.slice(end))]);
      continue;
    }
    const hash = rest.search(/[ \t]#/);
    const val = hash < 0 ? rest : rest.slice(0, hash);
    lines.push([...head, ...withRefs(val, TOK.value), ...(hash < 0 ? [] : [{ text: rest.slice(hash), cls: TOK.comment }])]);
  }
  return lines;
}

/** JSON text → lines with keys, strings and references coloured. */
export function highlightJson(text: string): Tok[][] {
  return text.split("\n").map((line) => {
    const out: Tok[] = [];
    let last = 0;
    for (const m of line.matchAll(/"(?:[^"\\]|\\.)*"(\s*:)?/g)) {
      if (m.index! > last) out.push({ text: line.slice(last, m.index), cls: TOK.punct });
      if (m[1]) {
        const k = m[0].slice(0, m[0].length - m[1].length);
        out.push({ text: k, cls: TOK.key }, { text: m[1], cls: TOK.punct });
      } else out.push(...withRefs(m[0], TOK.string));
      last = m.index! + m[0].length;
    }
    if (last < line.length) out.push({ text: line.slice(last), cls: TOK.punct });
    return out;
  });
}
