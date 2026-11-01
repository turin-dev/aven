/**
 * .env reader/writer with the same rules as the server (internal/dotenv):
 * comments, `export ` prefix, '…' / `…` literal, "…" with \n \r \t \" \\ \$ \' \` escapes,
 * multiline quoted values, inline `#` comments after whitespace. `${{ }}` references are kept as-is.
 */

export class DotenvError extends Error {
  line: number;
  constructor(line: number, msg: string) {
    super(`line ${line}: ${msg}`);
    this.line = line;
  }
}

const KEY_RE = /^[A-Za-z_][A-Za-z0-9_.-]*$/;

/** Parse .env text into [key, value] pairs in file order. Throws DotenvError. */
export function parseEnvPairs(input: string): [string, string][] {
  return scan(input, null);
}

/** Result of {@link parseEnvLoose}: the entries that parsed and the errors of the ones that didn't. */
export type LooseResult = { pairs: [string, string][]; errors: DotenvError[] };

/**
 * Lenient parse for pasted text: an entry that fails is reported in `errors` and skipped (from its first line), and
 * parsing continues on the next line. Same rules as {@link parseEnvPairs} otherwise.
 */
export function parseEnvLoose(input: string): LooseResult {
  const errors: DotenvError[] = [];
  const pairs = scan(input, errors);
  return { pairs, errors };
}

function scan(input: string, errors: DotenvError[] | null): [string, string][] {
  const s = input.replace(/^\uFEFF/, "").replace(/\r\n/g, "\n");
  let pos = 0;
  let line = 1;
  const out: [string, string][] = [];
  const eof = () => pos >= s.length;
  const skipSpaces = () => {
    while (!eof() && (s[pos] === " " || s[pos] === "\t" || s[pos] === "\r")) pos++;
  };
  const restOfLine = () => {
    const start = pos;
    while (!eof() && s[pos] !== "\n") pos++;
    const r = s.slice(start, pos);
    if (!eof()) {
      pos++;
      line++;
    }
    return r;
  };
  const trunc = (x: string) => (x.length > 40 ? `${x.slice(0, 40)}…` : x);
  for (;;) {
    // blank lines and comments
    for (;;) {
      skipSpaces();
      if (eof()) return out;
      if (s[pos] === "\n") {
        pos++;
        line++;
      } else if (s[pos] === "#") restOfLine();
      else break;
    }
    const at = line;
    const start = pos;
    try {
      out.push(entry(at, start));
    } catch (e) {
      if (!errors || !(e instanceof DotenvError)) throw e;
      errors.push(e);
      // Skip the failed entry's first line and carry on.
      pos = start;
      line = at;
      restOfLine();
    }
  }

  /** Reads one KEY=VALUE entry starting at `start`. */
  function entry(at: number, start: number): [string, string] {
    while (!eof() && s[pos] !== "=" && s[pos] !== "\n") pos++;
    let head = s.slice(start, pos).trim();
    if (eof() || s[pos] !== "=") throw new DotenvError(at, `expected KEY=VALUE, got "${trunc(head)}"`);
    pos++;
    const ex = /^export[ \t]+(.+)$/.exec(head);
    if (ex) head = ex[1].trim();
    if (!KEY_RE.test(head)) throw new DotenvError(at, `invalid variable name "${trunc(head)}"`);
    const before = pos;
    skipSpaces();
    if (eof() || s[pos] === "\n" || (pos > before && s[pos] === "#")) {
      restOfLine();
      return [head, ""];
    }
    const q = s[pos];
    if (q === "'" || q === '"' || q === "`") {
      pos++;
      let v = "";
      for (;;) {
        if (eof()) throw new DotenvError(at, `unterminated ${q} quoted value`);
        const c = s[pos++];
        if (c === q) break;
        if (c === "\n") {
          line++;
          v += c;
        } else if (c === "\\" && q === '"') {
          if (eof()) throw new DotenvError(at, `unterminated ${q} quoted value`);
          const e = s[pos++];
          if (e === "n") v += "\n";
          else if (e === "r") v += "\r";
          else if (e === "t") v += "\t";
          else if (e === '"' || e === "\\" || e === "$" || e === "'" || e === "`") v += e;
          else if (e === "\n") {
            line++;
            v += "\\\n";
          } else v += `\\${e}`;
        } else v += c;
      }
      skipSpaces();
      if (!eof() && s[pos] !== "\n" && s[pos] !== "#") throw new DotenvError(line, `unexpected characters after closing quote of ${head}`);
      restOfLine();
      return [head, v];
    }
    let raw = restOfLine();
    for (let i = 1; i < raw.length; i++)
      if (raw[i] === "#" && (raw[i - 1] === " " || raw[i - 1] === "\t")) {
        raw = raw.slice(0, i);
        break;
      }
    return [head, raw.trim()];
  }
}

/** Parse .env text into a map (last duplicate wins). Throws DotenvError. */
export function parseEnv(text: string): Record<string, string> {
  return Object.fromEntries(parseEnvPairs(text));
}

/** Quote a value so parseEnv returns it unchanged. */
export function quoteEnv(v: string): string {
  if (v === "") return "";
  if (/^[A-Za-z0-9_./:@%+,=-]+$/.test(v)) return v;
  if (!/['\n\r]/.test(v)) return `'${v}'`;
  return `"${v.replace(/[\\"\n\r$`]/g, (c) => ({ "\\": "\\\\", '"': '\\"', "\n": "\\n", "\r": "\\r", $: "\\$", "`": "\\`" })[c]!)}"`;
}

/** Map → .env text, keys sorted, one line per key. */
export function marshalEnv(m: Record<string, string>): string {
  return Object.keys(m)
    .sort()
    .map((k) => `${k}=${quoteEnv(m[k])}\n`)
    .join("");
}

/**
 * Raw-editor text (env or json) with sealed variables left out of the values. In env format they are listed as
 * `# KEY=<sealed>` comments under `note` (one comment line per line of the note), so the user still sees them.
 */
export function renderRaw(format: "env" | "json", vars: Record<string, string>, sealed: string[], note = ""): string {
  const hidden = new Set(sealed);
  const visible = Object.fromEntries(Object.entries(vars).filter(([k]) => !hidden.has(k)));
  if (format === "json") {
    const sorted = Object.fromEntries(Object.keys(visible).sort().map((k) => [k, visible[k]]));
    return `${JSON.stringify(sorted, null, 2)}\n`;
  }
  const keys = [...hidden].filter((k) => k in vars).sort();
  let head = "";
  if (keys.length) {
    if (note) head = note.split("\n").map((l) => `# ${l}\n`).join("");
    head += keys.map((k) => `# ${k}=<sealed>\n`).join("");
    if (Object.keys(visible).length) head += "\n";
  }
  return head + marshalEnv(visible);
}

/** Parse raw-editor text. Throws Error with a readable message. */
export function parseRaw(format: "env" | "json", text: string): Record<string, string> {
  if (format === "env") return parseEnv(text);
  if (!text.trim()) return {};
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    throw new Error("content must be a JSON object of strings");
  }
  if (!v || typeof v !== "object" || Array.isArray(v) || Object.values(v).some((x) => typeof x !== "string"))
    throw new Error("content must be a JSON object of strings");
  return v as Record<string, string>;
}

export type VarDiff = { added: string[]; changed: string[]; removed: string[] };

/**
 * Key-level difference between the current variables and an edited set, keys sorted. Keys in `keep` (sealed variables
 * the editor doesn't show) are never counted as removed, and writing `<sealed>` back to them is not a change.
 */
export function diffVars(prev: Record<string, string>, next: Record<string, string>, keep: Set<string> = new Set()): VarDiff {
  const added: string[] = [];
  const changed: string[] = [];
  const removed: string[] = [];
  for (const [k, v] of Object.entries(next)) {
    if (!(k in prev)) added.push(k);
    else if (prev[k] !== v && !(keep.has(k) && v === "<sealed>")) changed.push(k);
  }
  for (const k of Object.keys(prev)) if (!(k in next) && !keep.has(k)) removed.push(k);
  return { added: added.sort(), changed: changed.sort(), removed: removed.sort() };
}
