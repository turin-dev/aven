// Variable reference helpers: ${{ VAR }}, ${{ shared.VAR }}, ${{ service.VAR }}.

export const REF_RE = /\$\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}/g;

export type Ref = { ns: string | null; key: string; raw: string };

export function refsIn(value: string): Ref[] {
  const out: Ref[] = [];
  for (const m of value.matchAll(REF_RE)) {
    const ref = m[1];
    const dot = ref.indexOf(".");
    out.push(dot < 0 ? { ns: null, key: ref, raw: m[0] } : { ns: ref.slice(0, dot), key: ref.slice(dot + 1), raw: m[0] });
  }
  return out;
}

export function hasRef(value: string): boolean {
  REF_RE.lastIndex = 0;
  return /\$\{\{\s*[A-Za-z0-9_.-]+\s*\}\}/.test(value);
}

/** Service names referenced by a set of variables (excluding `shared` and same-instance refs). */
export function referencedServices(vars: Record<string, string>): Set<string> {
  const out = new Set<string>();
  for (const v of Object.values(vars)) for (const r of refsIn(v)) if (r.ns && r.ns !== "shared") out.add(r.ns);
  return out;
}

/** Split a value into literal text and reference tokens for highlighting. */
export function tokenize(value: string): { text: string; ref?: Ref }[] {
  const out: { text: string; ref?: Ref }[] = [];
  let last = 0;
  for (const m of value.matchAll(REF_RE)) {
    if (m.index! > last) out.push({ text: value.slice(last, m.index) });
    const ref = m[1];
    const dot = ref.indexOf(".");
    out.push({ text: m[0], ref: dot < 0 ? { ns: null, key: ref, raw: m[0] } : { ns: ref.slice(0, dot), key: ref.slice(dot + 1), raw: m[0] } });
    last = m.index! + m[0].length;
  }
  if (last < value.length) out.push({ text: value.slice(last) });
  return out;
}

/* ── validation and local preview (same rules as internal/vars) ───────── */

/**
 * Everything a reference can point at in one environment. Each scope maps variable names to their raw value, or to
 * `undefined` when the name exists but the value isn't known here (sealed, or injected at deploy time).
 * Scope names: `shared` and service names. `self` is the service being edited; `${{ VAR }}` points into it.
 */
export type RefScope = { self: string; scopes: Record<string, Record<string, string | undefined>> };

export type RefProblem =
  /** `${{ svc.VAR }}` where no service is called `svc`. */
  | { kind: "service"; raw: string; ns: string; key: string }
  /** The scope exists but has no such variable (the server substitutes an empty string). */
  | { kind: "variable"; raw: string; ns: string | null; key: string }
  /** `${{` that never becomes a valid reference: unclosed, or a name with characters references can't use. */
  | { kind: "malformed"; raw: string }
  /** Shell-style `${VAR}`: aven doesn't expand it, the text is kept as-is. */
  | { kind: "shell"; raw: string; key: string };

/** Problems with the references in `value`, in order of appearance (each distinct text once). */
export function refProblems(value: string, scope: RefScope): RefProblem[] {
  const out: RefProblem[] = [];
  const seen = new Set<string>();
  const add = (p: RefProblem) => {
    if (seen.has(p.raw)) return;
    seen.add(p.raw);
    out.push(p);
  };
  const valid: [number, number][] = [];
  for (const m of value.matchAll(REF_RE)) {
    valid.push([m.index!, m.index! + m[0].length]);
    const ref = m[1];
    const dot = ref.indexOf(".");
    const ns = dot < 0 ? null : ref.slice(0, dot);
    const key = dot < 0 ? ref : ref.slice(dot + 1);
    const target = scope.scopes[ns ?? scope.self];
    if (ns !== null && ns !== "shared" && !target) add({ kind: "service", raw: m[0], ns, key });
    else if (!target || !(key in target)) add({ kind: "variable", raw: m[0], ns, key });
  }
  const inValid = (i: number) => valid.some(([a, b]) => i >= a && i < b);
  for (let i = value.indexOf("${{"); i >= 0; i = value.indexOf("${{", i + 3)) {
    if (inValid(i)) continue;
    const close = value.indexOf("}}", i + 3);
    add({ kind: "malformed", raw: close < 0 ? value.slice(i, i + 40) : value.slice(i, close + 2) });
  }
  for (const m of value.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g)) if (!inValid(m.index!)) add({ kind: "shell", raw: m[0], key: m[1] });
  return out;
}

export type Preview = {
  /** The value with every reference substituted. Unknown values (sealed, injected at deploy) keep their `${{ }}` text. */
  value: string;
  /** References whose value isn't known here; `value` is then only partly resolved. */
  unknown: string[];
  /** Names that don't exist (they resolve to an empty string on deploy). */
  missing: string[];
  /** A reference cycle such as ["web.A", "web.B", "web.A"]: the deploy would fail. */
  cycle: string[] | null;
};

/**
 * Resolves `value` as variable `name` of `scope.self` would be resolved at deploy (internal/vars.Resolve): references
 * inside a shared variable's value stay in the shared scope, inside another service's value in that service.
 */
export function resolvePreview(value: string, scope: RefScope, name?: string): Preview {
  const unknown = new Set<string>();
  const missing = new Set<string>();
  const done = new Map<string, string>();
  let cycle: string[] | null = null;
  const stack: string[] = name ? [`${scope.self}.${name}`] : [];
  const expand = (raw: string, home: string): string =>
    raw.replace(REF_RE, (whole, ref: string) => {
      if (cycle) return "";
      const dot = ref.indexOf(".");
      const ns = dot < 0 ? home : ref.slice(0, dot);
      const key = dot < 0 ? ref : ref.slice(dot + 1);
      const id = `${ns}.${key}`;
      const hit = done.get(id);
      if (hit !== undefined) return hit;
      const at = stack.indexOf(id);
      if (at >= 0) {
        cycle = [...stack.slice(at), id];
        return "";
      }
      const target = scope.scopes[ns];
      if (!target || !(key in target)) {
        missing.add(id);
        return "";
      }
      const v = target[key];
      if (v === undefined) {
        unknown.add(id);
        return whole;
      }
      stack.push(id);
      const out = expand(v, ns);
      stack.pop();
      done.set(id, out);
      return out;
    });
  const out = expand(value, scope.self);
  return { value: cycle ? "" : out, unknown: [...unknown], missing: [...missing], cycle };
}
