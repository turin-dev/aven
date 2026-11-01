/* Pure canvas geometry: view math, snapping/alignment, edge routing and fuzzy matching.
   No React and no path aliases, so `node --test components/canvas/geometry.test.mts` can import it directly. */

export type Pos = { x: number; y: number };
export type Rect = { x: number; y: number; w: number; h: number };
/** Screen transform of the canvas: screen = canvas * z + (x, y). */
export type View = { x: number; y: number; z: number };

export const MIN_Z = 0.2;
export const MAX_Z = 2;

export const clampZ = (z: number, min = MIN_Z, max = MAX_Z) => Math.min(max, Math.max(min, z));

/** Zooms by `factor` keeping the screen point (px, py) fixed. */
export function zoomAround(v: View, factor: number, px: number, py: number): View {
  const z = clampZ(v.z * factor);
  const k = z / v.z;
  return { z, x: px - (px - v.x) * k, y: py - (py - v.y) * k };
}

/** Converts a screen point (relative to the viewport) to canvas coordinates. */
export const toCanvas = (v: View, sx: number, sy: number): Pos => ({ x: (sx - v.x) / v.z, y: (sy - v.y) / v.z });

/** Bounding box of rects, or null for none. */
export function bounds(rects: Rect[]): Rect | null {
  if (!rects.length) return null;
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const r of rects) {
    minX = Math.min(minX, r.x);
    minY = Math.min(minY, r.y);
    maxX = Math.max(maxX, r.x + r.w);
    maxY = Math.max(maxY, r.y + r.h);
  }
  return { x: minX, y: minY, w: maxX - minX, h: maxY - minY };
}

/**
 * View that frames `rects` inside a w×h viewport with `pad` around them.
 * Never zooms in past 1 (cards stay at their natural size) and never below `floor`; if the floor crops the content
 * vertically the view starts at the top instead of the middle.
 */
export function fitView(rects: Rect[], w: number, h: number, opts: { pad?: number; floor?: number; top?: number } = {}): View | null {
  const b = bounds(rects);
  if (!b || w <= 0 || h <= 0) return null;
  const pad = opts.pad ?? 72;
  const top = opts.top ?? 48;
  const fitZ = Math.min(1, (w - pad * 2) / Math.max(1, b.w), (h - pad * 2) / Math.max(1, b.h));
  const z = clampZ(Math.max(opts.floor ?? 0, fitZ));
  const ch = b.h * z;
  return {
    z,
    x: (w - b.w * z) / 2 - b.x * z,
    y: ch > h - top ? top - b.y * z : (h - ch) / 2 - b.y * z,
  };
}

/** Linear interpolation between two views (for animated zoom/fit). */
export function lerpView(a: View, b: View, t: number): View {
  // Interpolating the zoom geometrically keeps the motion even across zoom levels.
  const z = a.z * Math.pow(b.z / a.z, t);
  return { z, x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
}

export const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3);

export const snapTo = (n: number, grid: number) => Math.round(n / grid) * grid;

/** An alignment guide in canvas coordinates: a vertical line at x (axis "x") or a horizontal line at y (axis "y"). */
export type Guide = { axis: "x" | "y"; at: number; from: number; to: number };

/**
 * Snaps a moving rect to the edges and centres of other rects when one is within `threshold` (canvas units).
 * Returns the correction to add to the rect position (0 on an axis without a match) and the guides to draw.
 */
export function alignSnap(moving: Rect, others: Rect[], threshold: number): { dx: number; dy: number; guides: Guide[] } {
  const xs = (r: Rect) => [r.x, r.x + r.w / 2, r.x + r.w];
  const ys = (r: Rect) => [r.y, r.y + r.h / 2, r.y + r.h];
  let bestX: { d: number; at: number } | null = null;
  let bestY: { d: number; at: number } | null = null;
  const mx = xs(moving);
  const my = ys(moving);
  for (const o of others) {
    for (const a of xs(o))
      for (const m of mx) {
        const d = a - m;
        if (Math.abs(d) <= threshold && (!bestX || Math.abs(d) < Math.abs(bestX.d))) bestX = { d, at: a };
      }
    for (const a of ys(o))
      for (const m of my) {
        const d = a - m;
        if (Math.abs(d) <= threshold && (!bestY || Math.abs(d) < Math.abs(bestY.d))) bestY = { d, at: a };
      }
  }
  const dx = bestX?.d ?? 0;
  const dy = bestY?.d ?? 0;
  const snapped = { ...moving, x: moving.x + dx, y: moving.y + dy };
  const guides: Guide[] = [];
  const near = (a: number, b: number) => Math.abs(a - b) < 0.5;
  if (bestX) {
    const at = bestX.at;
    const hit = others.filter((o) => xs(o).some((v) => near(v, at)));
    const ys0 = [snapped.y, snapped.y + snapped.h, ...hit.flatMap((o) => [o.y, o.y + o.h])];
    guides.push({ axis: "x", at, from: Math.min(...ys0), to: Math.max(...ys0) });
  }
  if (bestY) {
    const at = bestY.at;
    const hit = others.filter((o) => ys(o).some((v) => near(v, at)));
    const xs0 = [snapped.x, snapped.x + snapped.w, ...hit.flatMap((o) => [o.x, o.x + o.w])];
    guides.push({ axis: "y", at, from: Math.min(...xs0), to: Math.max(...xs0) });
  }
  return { dx, dy, guides };
}

/** True when two rects overlap once each is grown by `gap` on every side. */
export const overlaps = (a: Rect, b: Rect, gap = 0) =>
  a.x < b.x + b.w + gap && b.x < a.x + a.w + gap && a.y < b.y + b.h + gap && b.y < a.y + a.h + gap;

/** SVG path through orthogonal points with rounded corners of radius r (clamped to half of the shorter segment). */
export function roundedPath(pts: Pos[], r: number): string {
  if (pts.length < 2) return "";
  // Drop zero-length segments and collinear middle points so corners are real corners.
  const p: Pos[] = [];
  for (const q of pts) {
    const last = p[p.length - 1];
    if (last && Math.abs(last.x - q.x) < 0.01 && Math.abs(last.y - q.y) < 0.01) continue;
    if (p.length >= 2) {
      const a = p[p.length - 2];
      const b = p[p.length - 1];
      if ((Math.abs(a.x - b.x) < 0.01 && Math.abs(b.x - q.x) < 0.01) || (Math.abs(a.y - b.y) < 0.01 && Math.abs(b.y - q.y) < 0.01)) p.pop();
    }
    p.push(q);
  }
  const f = (n: number) => Math.round(n * 10) / 10;
  let d = `M${f(p[0].x)} ${f(p[0].y)}`;
  for (let i = 1; i < p.length - 1; i++) {
    const a = p[i - 1];
    const b = p[i];
    const c = p[i + 1];
    const l1 = Math.hypot(b.x - a.x, b.y - a.y);
    const l2 = Math.hypot(c.x - b.x, c.y - b.y);
    const rr = Math.max(0, Math.min(r, l1 / 2, l2 / 2));
    if (rr < 0.5) {
      d += ` L${f(b.x)} ${f(b.y)}`;
      continue;
    }
    const s = { x: b.x - ((b.x - a.x) / l1) * rr, y: b.y - ((b.y - a.y) / l1) * rr };
    const e = { x: b.x + ((c.x - b.x) / l2) * rr, y: b.y + ((c.y - b.y) / l2) * rr };
    d += ` L${f(s.x)} ${f(s.y)} Q${f(b.x)} ${f(b.y)} ${f(e.x)} ${f(e.y)}`;
  }
  const last = p[p.length - 1];
  return `${d} L${f(last.x)} ${f(last.y)}`;
}

/**
 * Orthogonal route from card a to card b: side to side when they sit apart horizontally, else top to bottom.
 * `lane` (0, ±1, …) offsets the middle segment so parallel edges between the same columns don't overlap.
 * Returns the path and the midpoint of its middle segment.
 */
export function edgeRoute(a: Rect, b: Rect, opts: { radius?: number; lane?: number; gap?: number } = {}): { d: string; mid: Pos } {
  const radius = opts.radius ?? 10;
  const lane = (opts.lane ?? 0) * 8;
  const gap = opts.gap ?? 2;
  const acx = a.x + a.w / 2;
  const bcx = b.x + b.w / 2;
  const acy = a.y + a.h / 2;
  const bcy = b.y + b.h / 2;
  if (b.x >= a.x + a.w + 24 || a.x >= b.x + b.w + 24) {
    const right = bcx > acx;
    const x1 = right ? a.x + a.w : a.x;
    const x2 = right ? b.x - gap : b.x + b.w + gap;
    const xm = Math.round((x1 + x2) / 2) + lane;
    const pts = [
      { x: x1, y: acy },
      { x: xm, y: acy },
      { x: xm, y: bcy },
      { x: x2, y: bcy },
    ];
    return { d: roundedPath(pts, radius), mid: { x: xm, y: (acy + bcy) / 2 } };
  }
  const down = bcy > acy;
  const y1 = down ? a.y + a.h : a.y;
  const y2 = down ? b.y - gap : b.y + b.h + gap;
  const ym = Math.round((y1 + y2) / 2) + lane;
  const pts = [
    { x: acx, y: y1 },
    { x: acx, y: ym },
    { x: bcx, y: ym },
    { x: bcx, y: y2 },
  ];
  return { d: roundedPath(pts, radius), mid: { x: (acx + bcx) / 2, y: ym } };
}

/**
 * Fuzzy match of `query` against `text` (case-insensitive subsequence). Returns null when not every query character
 * appears in order, else a score (higher is better) and the matched character indices for highlighting.
 * Contiguous runs, word starts and an exact prefix score higher; a plain substring always beats a scattered match.
 */
export function fuzzyMatch(query: string, text: string): { score: number; hits: number[] } | null {
  const q = query.trim().toLowerCase();
  if (!q) return { score: 0, hits: [] };
  const s = text.toLowerCase();
  const sub = s.indexOf(q);
  if (sub >= 0) {
    const hits = Array.from({ length: q.length }, (_, i) => sub + i);
    const wordStart = sub === 0 || /[\s\-_/.:@]/.test(s[sub - 1]);
    return { score: 1000 - sub + (sub === 0 ? 500 : 0) + (wordStart ? 200 : 0) + (q.length === s.length ? 300 : 0), hits };
  }
  const hits: number[] = [];
  let score = 0;
  let qi = 0;
  let prev = -2;
  for (let i = 0; i < s.length && qi < q.length; i++) {
    if (q[qi] === " ") {
      qi++;
      i--;
      continue;
    }
    if (s[i] !== q[qi]) continue;
    const wordStart = i === 0 || /[\s\-_/.:@]/.test(s[i - 1]);
    score += 10 + (i === prev + 1 ? 15 : 0) + (wordStart ? 20 : 0);
    hits.push(i);
    prev = i;
    qi++;
  }
  while (qi < q.length && q[qi] === " ") qi++;
  if (qi < q.length) return null;
  // Penalise spread-out matches.
  return { score: score - (hits[hits.length - 1] - hits[0]), hits };
}

/** Best fuzzy score of `query` over several fields (the first field, usually the label, weighs more). */
export function fuzzyBest(query: string, ...fields: (string | undefined | null)[]): number | null {
  let best: number | null = null;
  fields.forEach((f, i) => {
    if (!f) return;
    const m = fuzzyMatch(query, f);
    if (!m) return;
    const sc = m.score * (i === 0 ? 1 : 0.6);
    if (best === null || sc > best) best = sc;
  });
  return best;
}
