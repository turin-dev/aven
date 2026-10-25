/*
 * Minimal QR Code encoder (ISO/IEC 18004) for the 2FA setup screen: byte mode, error correction level M,
 * versions 1–10 (up to 213 bytes), all 8 mask patterns scored with the standard penalty rules.
 * Runs entirely in the browser so the TOTP secret never leaves the page.
 */

import { t } from "@/lib/i18n";

export type QrMatrix = boolean[][]; // [row][col], true = dark

/** Error correction level M: [data codewords per block, number of blocks] groups and EC codewords per block, by version. */
const ECC_M: { ec: number; groups: [number, number][] }[] = [
  { ec: 0, groups: [] }, // index 0 unused
  { ec: 10, groups: [[16, 1]] },
  { ec: 16, groups: [[28, 1]] },
  { ec: 26, groups: [[44, 1]] },
  { ec: 18, groups: [[32, 2]] },
  { ec: 24, groups: [[43, 2]] },
  { ec: 16, groups: [[27, 4]] },
  { ec: 18, groups: [[31, 4]] },
  { ec: 22, groups: [[38, 2], [39, 2]] },
  { ec: 22, groups: [[36, 3], [37, 2]] },
  { ec: 26, groups: [[43, 4], [44, 1]] },
];

/** Alignment pattern centre coordinates by version. */
const ALIGN: number[][] = [[], [], [6, 18], [6, 22], [6, 26], [6, 30], [6, 34], [6, 22, 38], [6, 24, 42], [6, 26, 46], [6, 28, 50]];

export const QR_MAX_VERSION = 10;

const dataCodewords = (v: number) => ECC_M[v].groups.reduce((n, [len, count]) => n + len * count, 0);
const countBits = (v: number) => (v <= 9 ? 8 : 16);

/* ── Reed–Solomon over GF(256), primitive polynomial x^8+x^4+x^3+x^2+1 (0x11D) ── */

function gfMul(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z & 0xff;
}

/** Coefficients (highest power first, leading 1 omitted) of prod_{i<degree} (x - α^i). */
export function rsGenerator(degree: number): number[] {
  const g = new Array<number>(degree).fill(0);
  g[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      g[j] = gfMul(g[j], root);
      if (j + 1 < degree) g[j] ^= g[j + 1];
    }
    root = gfMul(root, 2);
  }
  return g;
}

export function rsRemainder(data: number[], gen: number[]): number[] {
  const r = new Array<number>(gen.length).fill(0);
  for (const b of data) {
    const f = b ^ (r.shift() as number);
    r.push(0);
    for (let i = 0; i < gen.length; i++) r[i] ^= gfMul(gen[i], f);
  }
  return r;
}

/* ── data codewords ── */

function encodeData(bytes: Uint8Array, v: number): number[] {
  const bits: number[] = [];
  const put = (val: number, n: number) => {
    for (let i = n - 1; i >= 0; i--) bits.push((val >>> i) & 1);
  };
  put(0b0100, 4); // byte mode
  put(bytes.length, countBits(v));
  for (const b of bytes) put(b, 8);
  const cap = dataCodewords(v) * 8;
  put(0, Math.min(4, cap - bits.length)); // terminator
  while (bits.length % 8) bits.push(0);
  const out: number[] = [];
  for (let i = 0; i < bits.length; i += 8) out.push(bits.slice(i, i + 8).reduce((a, b) => (a << 1) | b, 0));
  for (let pad = 0xec; out.length < dataCodewords(v); pad ^= 0xec ^ 0x11) out.push(pad);
  return out;
}

/** Splits into blocks, appends EC codewords and interleaves (data columns first, then EC columns). */
function interleave(data: number[], v: number): number[] {
  const { ec, groups } = ECC_M[v];
  const gen = rsGenerator(ec);
  const blocks: { d: number[]; e: number[] }[] = [];
  let k = 0;
  for (const [len, count] of groups)
    for (let i = 0; i < count; i++) {
      const d = data.slice(k, k + len);
      k += len;
      blocks.push({ d, e: rsRemainder(d, gen) });
    }
  const out: number[] = [];
  const maxLen = Math.max(...blocks.map((b) => b.d.length));
  for (let i = 0; i < maxLen; i++) for (const b of blocks) if (i < b.d.length) out.push(b.d[i]);
  for (let i = 0; i < ec; i++) for (const b of blocks) out.push(b.e[i]);
  return out;
}

/* ── matrix ── */

class Grid {
  readonly version: number;
  readonly size: number;
  readonly m: boolean[][];
  readonly fn: boolean[][];
  constructor(version: number) {
    this.version = version;
    this.size = version * 4 + 17;
    this.m = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false));
    this.fn = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false));
  }
  /** x = column, y = row. */
  setFn(x: number, y: number, dark: boolean) {
    this.m[y][x] = dark;
    this.fn[y][x] = true;
  }
}

function drawFunctionPatterns(g: Grid) {
  const n = g.size;
  for (let i = 0; i < n; i++) {
    g.setFn(6, i, i % 2 === 0);
    g.setFn(i, 6, i % 2 === 0);
  }
  for (const [cx, cy] of [
    [3, 3],
    [n - 4, 3],
    [3, n - 4],
  ])
    for (let dy = -4; dy <= 4; dy++)
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx;
        const y = cy + dy;
        if (x < 0 || y < 0 || x >= n || y >= n) continue;
        const d = Math.max(Math.abs(dx), Math.abs(dy));
        g.setFn(x, y, d !== 2 && d !== 4);
      }
  const pos = ALIGN[g.version];
  const last = pos.length - 1;
  for (let i = 0; i <= last; i++)
    for (let j = 0; j <= last; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === last) || (i === last && j === 0)) continue;
      for (let dy = -2; dy <= 2; dy++)
        for (let dx = -2; dx <= 2; dx++) g.setFn(pos[i] + dx, pos[j] + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
    }
  drawFormat(g, 0); // reserve; overwritten for the chosen mask
  drawVersion(g);
}

/** 15-bit format information for level M (indicator 00) and the mask, BCH(15,5) masked with 0x5412. */
export function formatBits(mask: number): number {
  const data = (0b00 << 3) | mask;
  let rem = data;
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  return ((data << 10) | rem) ^ 0x5412;
}

function drawFormat(g: Grid, mask: number) {
  const bits = formatBits(mask);
  const bit = (i: number) => ((bits >>> i) & 1) === 1;
  const n = g.size;
  for (let i = 0; i <= 5; i++) g.setFn(8, i, bit(i));
  g.setFn(8, 7, bit(6));
  g.setFn(8, 8, bit(7));
  g.setFn(7, 8, bit(8));
  for (let i = 9; i < 15; i++) g.setFn(14 - i, 8, bit(i));
  for (let i = 0; i < 8; i++) g.setFn(n - 1 - i, 8, bit(i));
  for (let i = 8; i < 15; i++) g.setFn(8, n - 15 + i, bit(i));
  g.setFn(8, n - 8, true); // dark module
}

/** 18-bit version information (versions 7+), BCH(18,6). */
export function versionBits(v: number): number {
  let rem = v;
  for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
  return (v << 12) | rem;
}

function drawVersion(g: Grid) {
  if (g.version < 7) return;
  const bits = versionBits(g.version);
  for (let i = 0; i < 18; i++) {
    const dark = ((bits >>> i) & 1) === 1;
    const a = g.size - 11 + (i % 3);
    const b = Math.floor(i / 3);
    g.setFn(a, b, dark);
    g.setFn(b, a, dark);
  }
}

function drawCodewords(g: Grid, cw: number[]) {
  const n = g.size;
  let i = 0;
  for (let right = n - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5; // skip the vertical timing pattern
    for (let vert = 0; vert < n; vert++)
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? n - 1 - vert : vert;
        if (!g.fn[y][x] && i < cw.length * 8) {
          g.m[y][x] = ((cw[i >>> 3] >>> (7 - (i & 7))) & 1) === 1;
          i++;
        }
        // remainder bits stay light
      }
  }
}

/** Mask condition (x = column, y = row): true flips the module. */
export function maskBit(mask: number, x: number, y: number): boolean {
  switch (mask) {
    case 0:
      return (x + y) % 2 === 0;
    case 1:
      return y % 2 === 0;
    case 2:
      return x % 3 === 0;
    case 3:
      return (x + y) % 3 === 0;
    case 4:
      return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0;
    case 5:
      return ((x * y) % 2) + ((x * y) % 3) === 0;
    case 6:
      return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0;
    default:
      return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0;
  }
}

function applyMask(g: Grid, mask: number) {
  for (let y = 0; y < g.size; y++) for (let x = 0; x < g.size; x++) if (!g.fn[y][x] && maskBit(mask, x, y)) g.m[y][x] = !g.m[y][x];
}

/* ── penalty (N1=3, N2=3, N3=40, N4=10; finder-like runs count the light quiet zone outside the symbol) ── */

export function penalty(m: QrMatrix): number {
  const n = m.length;
  let score = 0;
  const addHistory = (len: number, h: number[]) => {
    if (h[0] === 0) len += n; // first run touches the quiet zone
    h.pop();
    h.unshift(len);
  };
  const countFinder = (h: number[]) => {
    const u = h[1];
    const core = u > 0 && h[2] === u && h[3] === u * 3 && h[4] === u && h[5] === u;
    return (core && h[0] >= u * 4 && h[6] >= u ? 1 : 0) + (core && h[6] >= u * 4 && h[0] >= u ? 1 : 0);
  };
  const line = (get: (i: number) => boolean) => {
    let color = false;
    let run = 0;
    const h = [0, 0, 0, 0, 0, 0, 0];
    for (let i = 0; i < n; i++) {
      if (get(i) === color) {
        run++;
        if (run === 5) score += 3;
        else if (run > 5) score++;
      } else {
        addHistory(run, h);
        if (!color) score += countFinder(h) * 40;
        color = get(i);
        run = 1;
      }
    }
    if (color) {
      addHistory(run, h);
      run = 0;
    }
    addHistory(run + n, h);
    score += countFinder(h) * 40;
  };
  for (let y = 0; y < n; y++) line((x) => m[y][x]);
  for (let x = 0; x < n; x++) line((y) => m[y][x]);
  for (let y = 0; y < n - 1; y++)
    for (let x = 0; x < n - 1; x++) {
      const c = m[y][x];
      if (c === m[y][x + 1] && c === m[y + 1][x] && c === m[y + 1][x + 1]) score += 3;
    }
  let dark = 0;
  for (const row of m) for (const c of row) if (c) dark++;
  const total = n * n;
  const k = Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1;
  return score + k * 10;
}

export type QrOptions = { /** Force a mask 0–7 (tests); otherwise the lowest-penalty mask is used. */ mask?: number; /** Minimum version. */ minVersion?: number };

export type QrCode = { version: number; mask: number; size: number; modules: QrMatrix };

/** Encodes text (UTF-8, byte mode, level M). Throws when it does not fit in version 10. */
export function encodeQr(text: string, opts: QrOptions = {}): QrCode {
  const bytes = new TextEncoder().encode(text);
  let v = Math.max(1, opts.minVersion ?? 1);
  while (v <= QR_MAX_VERSION && 4 + countBits(v) + bytes.length * 8 > dataCodewords(v) * 8) v++;
  if (v > QR_MAX_VERSION) throw new Error(t("QR 코드에 넣기에는 너무 깁니다 ({size}바이트, 최대 {max}바이트)", { size: bytes.length, max: dataCodewords(QR_MAX_VERSION) - 3 }));
  const cw = interleave(encodeData(bytes, v), v);
  const base = new Grid(v);
  drawFunctionPatterns(base);
  drawCodewords(base, cw);
  const build = (mask: number) => {
    const g = new Grid(v);
    for (let y = 0; y < g.size; y++)
      for (let x = 0; x < g.size; x++) {
        g.m[y][x] = base.m[y][x];
        g.fn[y][x] = base.fn[y][x];
      }
    applyMask(g, mask);
    drawFormat(g, mask);
    return g;
  };
  let mask = opts.mask ?? -1;
  if (mask < 0 || mask > 7) {
    let best = Infinity;
    for (let k = 0; k < 8; k++) {
      const p = penalty(build(k).m);
      if (p < best) {
        best = p;
        mask = k;
      }
    }
  }
  const g = build(mask);
  return { version: v, mask, size: g.size, modules: g.m };
}

/** SVG path (one unit per module, offset by `quiet` modules) covering the dark modules, merged per row run. */
export function qrPath(modules: QrMatrix, quiet = 4): string {
  let d = "";
  modules.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) {
      if (!row[x]) continue;
      let w = 1;
      while (x + w < row.length && row[x + w]) w++;
      d += `M${x + quiet} ${y + quiet}h${w}v1h-${w}z`;
      x += w - 1;
    }
  });
  return d;
}
