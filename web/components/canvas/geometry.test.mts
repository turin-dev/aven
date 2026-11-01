// Run: node --test components/canvas/geometry.test.mts (from web/; Node >= 22.18 strips TypeScript types natively).
import assert from "node:assert/strict";
import { test } from "node:test";
import { alignSnap, bounds, clampZ, edgeRoute, fitView, fuzzyBest, fuzzyMatch, lerpView, overlaps, roundedPath, toCanvas, zoomAround } from "./geometry.ts";

test("zoomAround keeps the anchor point fixed and clamps the zoom", () => {
  const v = { x: 100, y: 50, z: 1 };
  const before = toCanvas(v, 400, 300);
  const z = zoomAround(v, 1.5, 400, 300);
  assert.equal(z.z, 1.5);
  const after = toCanvas(z, 400, 300);
  assert.ok(Math.abs(before.x - after.x) < 1e-9 && Math.abs(before.y - after.y) < 1e-9);
  assert.equal(zoomAround(v, 100, 0, 0).z, 2);
  assert.equal(zoomAround(v, 0.001, 0, 0).z, 0.2);
  assert.equal(clampZ(5), 2);
});

test("fitView centres content and never zooms past 1", () => {
  const v = fitView([{ x: 0, y: 0, w: 100, h: 100 }], 1000, 800)!;
  assert.equal(v.z, 1);
  assert.equal(v.x, 450);
  assert.equal(v.y, 350);
  const wide = fitView([{ x: 0, y: 0, w: 4000, h: 100 }], 1000, 800)!;
  assert.ok(wide.z < 1 && wide.z >= 0.2);
  // The floor crops vertically: start at the top.
  const tall = fitView([{ x: 0, y: 0, w: 100, h: 5000 }], 1000, 800, { floor: 0.65 })!;
  assert.equal(tall.z, 0.65);
  assert.equal(tall.y, 48);
  assert.equal(fitView([], 1000, 800), null);
  assert.equal(fitView([{ x: 0, y: 0, w: 10, h: 10 }], 0, 800), null);
});

test("bounds and overlaps", () => {
  assert.deepEqual(bounds([{ x: 0, y: 0, w: 10, h: 10 }, { x: 20, y: -5, w: 5, h: 5 }]), { x: 0, y: -5, w: 25, h: 15 });
  assert.equal(bounds([]), null);
  assert.ok(overlaps({ x: 0, y: 0, w: 10, h: 10 }, { x: 15, y: 0, w: 10, h: 10 }, 6));
  assert.ok(!overlaps({ x: 0, y: 0, w: 10, h: 10 }, { x: 15, y: 0, w: 10, h: 10 }, 4));
});

test("lerpView hits both ends", () => {
  const a = { x: 0, y: 0, z: 0.5 };
  const b = { x: 100, y: 200, z: 2 };
  assert.deepEqual(lerpView(a, b, 0), a);
  const end = lerpView(a, b, 1);
  assert.ok(Math.abs(end.z - 2) < 1e-9 && end.x === 100 && end.y === 200);
});

test("alignSnap snaps to the nearest edge or centre within the threshold", () => {
  const other = { x: 0, y: 0, w: 100, h: 50 };
  // Left edges 4px apart: snaps x, y too far.
  const r = alignSnap({ x: 4, y: 300, w: 100, h: 50 }, [other], 6);
  assert.equal(r.dx, -4);
  assert.equal(r.dy, 0);
  assert.equal(r.guides.length, 1);
  assert.equal(r.guides[0].axis, "x");
  assert.equal(r.guides[0].at, 0);
  assert.equal(r.guides[0].from, 0);
  assert.equal(r.guides[0].to, 350);
  // Vertical centres: other centre y = 25, moving centre y = 28.
  const c = alignSnap({ x: 400, y: 3, w: 100, h: 50 }, [other], 6);
  assert.equal(c.dy, -3);
  assert.equal(c.dx, 0);
  // Nothing close: no change.
  const n = alignSnap({ x: 400, y: 400, w: 100, h: 50 }, [other], 6);
  assert.deepEqual(n, { dx: 0, dy: 0, guides: [] });
  // The closest candidate wins across several rects.
  const m = alignSnap({ x: 205, y: 500, w: 100, h: 50 }, [other, { x: 207, y: 0, w: 10, h: 10 }, { x: 197, y: 0, w: 4, h: 10 }], 6);
  assert.equal(m.dx, 2);
});

test("roundedPath rounds corners and drops degenerate points", () => {
  assert.equal(roundedPath([{ x: 0, y: 0 }], 10), "");
  assert.equal(roundedPath([{ x: 0, y: 0 }, { x: 50, y: 0 }, { x: 100, y: 0 }], 10), "M0 0 L100 0");
  const d = roundedPath([{ x: 0, y: 0 }, { x: 50, y: 0 }, { x: 50, y: 100 }], 10);
  assert.equal(d, "M0 0 L40 0 Q50 0 50 10 L50 100");
  // Radius clamps to half the shorter segment.
  assert.equal(roundedPath([{ x: 0, y: 0 }, { x: 4, y: 0 }, { x: 4, y: 100 }], 10), "M0 0 L2 0 Q4 0 4 2 L4 100");
});

test("edgeRoute goes side to side for cards apart horizontally, else top to bottom", () => {
  const a = { x: 0, y: 0, w: 100, h: 50 };
  const side = edgeRoute(a, { x: 300, y: 200, w: 100, h: 50 }, { radius: 0 });
  assert.equal(side.d, "M100 25 L199 25 L199 225 L298 225");
  assert.equal(side.mid.x, 199);
  const below = edgeRoute(a, { x: 20, y: 300, w: 100, h: 50 }, { radius: 0 });
  assert.equal(below.d, "M50 50 L50 174 L70 174 L70 298");
  // Straight line when aligned.
  assert.equal(edgeRoute(a, { x: 0, y: 300, w: 100, h: 50 }).d, "M50 50 L50 298");
});

test("fuzzyMatch prefers prefixes and substrings and rejects missing characters", () => {
  assert.equal(fuzzyMatch("xyz", "postgres"), null);
  assert.deepEqual(fuzzyMatch("", "anything"), { score: 0, hits: [] });
  const prefix = fuzzyMatch("post", "PostgreSQL")!;
  const inner = fuzzyMatch("gres", "PostgreSQL")!;
  const scattered = fuzzyMatch("pgs", "PostgreSQL")!;
  assert.deepEqual(prefix.hits, [0, 1, 2, 3]);
  assert.ok(prefix.score > inner.score && inner.score > scattered.score);
  assert.deepEqual(scattered.hits, [0, 4, 7]);
  assert.ok(fuzzyMatch("gh repo", "GitHub repository"));
  assert.ok(fuzzyMatch("dimg", "Docker image"));
  // Korean works the same way.
  assert.ok(fuzzyMatch("데베", "데이터베이스"));
});

test("fuzzyBest weighs the first field more", () => {
  assert.equal(fuzzyBest("zzz", "a", "b"), null);
  assert.ok(fuzzyBest("redis", "Redis", "cache")! > fuzzyBest("redis", "Cache", "redis")!);
});
