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
