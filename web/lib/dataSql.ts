// Pure helpers for the database data browser: identifier/literal quoting, generated SELECTs,
// page-local sort and filter, secret masking and dangerous-statement detection.
// No imports so that `node --test lib/dataSql.test.mjs` can load this file directly.

export type SqlKind = "postgres" | "mysql";
export type Cell = string | number | boolean | null | undefined | Record<string, unknown> | unknown[];

/** Quotes an identifier: "x" for PostgreSQL, `x` for MySQL (the quote character is doubled). */
export function quoteIdent(kind: SqlKind, name: string): string {
  if (kind === "mysql") return "`" + name.replace(/`/g, "``") + "`";
  return '"' + name.replace(/"/g, '""') + '"';
}

/**
 * Quotes a string literal. PostgreSQL (standard_conforming_strings, the default since 9.1) only needs ' doubled.
 * MySQL treats backslash as an escape character by default, so it is doubled as well.
 */
export function quoteLiteral(kind: SqlKind, value: string): string {
  let v = value.replace(/'/g, "''");
  if (kind === "mysql") v = v.replace(/\\/g, "\\\\");
  return "'" + v + "'";
}

/** schema.table reference; PostgreSQL's "public" schema and an empty schema are left out. */
export function tableRef(kind: SqlKind, schema: string, name: string): string {
  const t = quoteIdent(kind, name);
  if (!schema || (kind === "postgres" && schema === "public")) return t;
  return quoteIdent(kind, schema) + "." + t;
}

export type SelectSpec = {
  schema: string;
  name: string;
  sort?: { column: string; desc: boolean } | null;
  /** Case-insensitive substring match on any of the columns, compared as text. */
  filter?: { columns: string[]; value: string } | null;
  limit: number;
  offset?: number;
};

/** SELECT for the whole table with ORDER BY / WHERE, used to move a page-local sort or filter to the query console. */
export function buildSelect(kind: SqlKind, s: SelectSpec): string {
  let q = `SELECT * FROM ${tableRef(kind, s.schema, s.name)}`;
  if (s.filter && s.filter.value.trim() !== "" && s.filter.columns.length) {
    // LIKE wildcards in the search text match literally.
    const esc = s.filter.value.trim().replace(/[\\%_]/g, (c) => "\\" + c);
    const lit = quoteLiteral(kind, `%${esc}%`);
    const conds = s.filter.columns.map((c) =>
      kind === "mysql" ? `CAST(${quoteIdent(kind, c)} AS CHAR) LIKE ${lit}` : `${quoteIdent(kind, c)}::text ILIKE ${lit}`,
    );
    q += conds.length === 1 ? ` WHERE ${conds[0]}` : ` WHERE (${conds.join(" OR ")})`;
  }
  if (s.sort) q += ` ORDER BY ${quoteIdent(kind, s.sort.column)} ${s.sort.desc ? "DESC" : "ASC"}`;
  q += ` LIMIT ${Math.max(1, Math.floor(s.limit))}`;
  if (s.offset) q += ` OFFSET ${Math.max(0, Math.floor(s.offset))}`;
  return q + ";";
}

/** Display text of a cell ("" for NULL). Objects (mongo documents, JSON) are serialised. */
export function cellText(v: Cell): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "string") return v;
  return JSON.stringify(v);
}

const NUM = /^-?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i;

/** Sort order for page-local sorting: NULLs last, numbers numerically, everything else with a natural string compare. */
export function compareCells(a: Cell, b: Cell): number {
  const an = a === null || a === undefined;
  const bn = b === null || b === undefined;
  if (an || bn) return an === bn ? 0 : an ? 1 : -1;
  const as = cellText(a);
  const bs = cellText(b);
  if (NUM.test(as) && NUM.test(bs)) {
    const d = Number(as) - Number(bs);
    if (d !== 0 && !Number.isNaN(d)) return d < 0 ? -1 : 1;
  }
  return as.localeCompare(bs, undefined, { numeric: true, sensitivity: "base" });
}

/** Indexes of rows sorted by one column (stable). desc keeps NULLs last. */
export function sortRows(rows: Cell[][], col: number, desc: boolean): number[] {
  const idx = rows.map((_, i) => i);
  idx.sort((x, y) => {
    const a = rows[x][col];
    const b = rows[y][col];
    const an = a === null || a === undefined;
    const bn = b === null || b === undefined;
    if (an || bn) return an === bn ? x - y : an ? 1 : -1;
    const c = compareCells(a, b);
    return (desc ? -c : c) || x - y;
  });
  return idx;
}

/** Case-insensitive substring match against any cell (col < 0) or one column. "NULL" matches SQL NULL. */
export function rowMatches(row: Cell[], query: string, col = -1): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const test = (v: Cell) => (v === null || v === undefined ? q === "null" : cellText(v).toLowerCase().includes(q));
  return col >= 0 ? test(row[col]) : row.some(test);
}

/** True when the text looks like a JSON object or array (worth pretty-printing in the inspector). */
export function asJson(text: string): unknown | undefined {
  const s = text.trim();
  if (!(s.startsWith("{") && s.endsWith("}")) && !(s.startsWith("[") && s.endsWith("]"))) return undefined;
  try {
    return JSON.parse(s);
  } catch {
    return undefined;
  }
}

export const MASK = "••••••••";

/**
 * Hides secrets in a connection string or command: every occurrence of the known secrets and the password part
 * of any scheme://user:password@host URL. Over-masking is preferred to leaking.
 */
export function maskSecrets(text: string, secrets: (string | undefined | null)[] = []): string {
  let out = text;
  for (const s of secrets) if (s && s.length >= 3) out = out.split(s).join(MASK);
  return out.replace(/([a-z][a-z0-9+.-]*:\/\/[^\s:/@"']*:)([^\s"']*)@/gi, (_m, head: string, pw: string) => (pw === MASK ? `${head}${pw}@` : `${head}${MASK}@`));
}

export type Danger = { level: "warn" | "severe"; reason: string };

/** Strips -- line comments, /* block comments *\/ and string literals so keyword checks do not trip on data. */
function stripSql(q: string): string {
  return q
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .replace(/--[^\n]*/g, " ")
    .replace(/'(?:[^']|'')*'/g, "''")
    .replace(/"(?:[^"]|"")*"/g, '""')
    .replace(/`(?:[^`]|``)*`/g, "``");
}

/**
 * Classifies a console query that changes data or schema. Returns null for reads.
 * reason is a stable code ("drop", "truncate", "delete-all", "update-all", "delete", "update", "alter", "flush", "mongo-drop", "mongo-delete").
 */
export function dangerOf(kind: string, query: string): Danger | null {
  if (kind === "redis") {
    for (const line of query.split("\n")) {
      const cmd = line.trim().split(/\s+/)[0]?.toUpperCase();
      if (cmd === "FLUSHALL" || cmd === "FLUSHDB") return { level: "severe", reason: "flush" };
    }
    for (const line of query.split("\n")) {
      const cmd = line.trim().split(/\s+/)[0]?.toUpperCase();
      if (cmd === "DEL" || cmd === "UNLINK") return { level: "warn", reason: "delete" };
    }
    return null;
  }
  if (kind === "mongo") {
    if (/\.\s*(drop|dropDatabase)\s*\(/.test(query)) return { level: "severe", reason: "mongo-drop" };
    if (/\.\s*(deleteMany|deleteOne|remove|findOneAndDelete)\s*\(/.test(query)) return { level: "warn", reason: "mongo-delete" };
    return null;
  }
  const statements = stripSql(query)
    .split(";")
    .map((s) => s.trim().replace(/\s+/g, " ").toLowerCase())
    .filter(Boolean);
  let worst: Danger | null = null;
  const bump = (d: Danger) => {
    if (!worst || (worst.level === "warn" && d.level === "severe")) worst = d;
  };
  for (const s of statements) {
    if (/^drop\b/.test(s)) bump({ level: "severe", reason: "drop" });
    else if (/^truncate\b/.test(s)) bump({ level: "severe", reason: "truncate" });
    else if (/^(with\b.*\)\s*)?delete\b/.test(s)) bump(/\bwhere\b/.test(s) ? { level: "warn", reason: "delete" } : { level: "severe", reason: "delete-all" });
    else if (/^(with\b.*\)\s*)?update\b/.test(s)) bump(/\bwhere\b/.test(s) ? { level: "warn", reason: "update" } : { level: "severe", reason: "update-all" });
    else if (/^alter\b/.test(s)) bump({ level: "warn", reason: "alter" });
  }
  return worst;
}

/** RFC 4180 CSV of a result (NULL becomes an empty field). */
export function toCsv(columns: string[], rows: Cell[][]): string {
  const f = (s: string) => (/[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s);
  return [columns.map(f).join(","), ...rows.map((r) => r.map((v) => f(cellText(v))).join(","))].join("\r\n") + "\r\n";
}

/** Bounded, de-duplicated most-recent-first history list. */
export function pushHistory(list: string[], q: string, max = 30): string[] {
  const s = q.trim();
  if (!s) return list;
  return [s, ...list.filter((x) => x !== s)].slice(0, max);
}
