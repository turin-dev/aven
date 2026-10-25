/*
 * Minimal terminal emulator for the service shell (no dependencies).
 * Handles the subset that shells and common tools emit: printable text, CR/LF/BS/TAB,
 * SGR colors (16/256/truecolor, bold, dim, italic, underline, inverse), cursor movement (CUP/CUU/CUD/CUF/CUB/CHA/VPA),
 * erase in line/display, insert/delete chars, save/restore cursor, alternate screen (as clear) and OSC (ignored).
 */

export type Style = { fg: string | null; bg: string | null; bold: boolean; dim: boolean; italic: boolean; underline: boolean; inverse: boolean };
export type Cell = { ch: string; st: Style };
export type Run = { text: string; st: Style };

const PLAIN: Style = { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, inverse: false };

// xterm default palette (0-15)
const BASE16 = [
  "#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
  "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
];

function color256(n: number): string {
  if (n < 16) return BASE16[n];
  if (n < 232) {
    const i = n - 16;
    const v = (x: number) => (x === 0 ? 0 : 55 + x * 40);
    return `rgb(${v(Math.floor(i / 36))},${v(Math.floor(i / 6) % 6)},${v(i % 6)})`;
  }
  const g = 8 + (n - 232) * 10;
  return `rgb(${g},${g},${g})`;
}

const MAX_LINES = 3000;

export class Terminal {
  cols: number;
  rows: number;
  lines: Cell[][] = [[]];
  /** cursor row (index into lines) and column */
  cy = 0;
  cx = 0;
  private st: Style = { ...PLAIN };
  private saved: [number, number] = [0, 0];
  private pending = "";

  constructor(cols = 80, rows = 24) {
    this.cols = cols;
    this.rows = rows;
  }

  resize(cols: number, rows: number) {
    this.cols = Math.max(1, cols);
    this.rows = Math.max(1, rows);
  }

  /** First line of the visible screen (cursor addressing is relative to it). */
  get top() {
    return Math.max(0, this.lines.length - this.rows);
  }

  reset() {
    this.lines = [[]];
    this.cy = this.cx = 0;
    this.st = { ...PLAIN };
  }

  private line(y: number): Cell[] {
    while (this.lines.length <= y) this.lines.push([]);
    return this.lines[y];
  }

  private trim() {
    const extra = this.lines.length - MAX_LINES;
    if (extra > 0) {
      this.lines.splice(0, extra);
      this.cy = Math.max(0, this.cy - extra);
    }
  }

  private newline() {
    this.cy++;
    this.line(this.cy);
    this.trim();
  }

  private put(ch: string) {
    if (this.cx >= this.cols) {
      this.cx = 0;
      this.newline();
    }
    const l = this.line(this.cy);
    while (l.length < this.cx) l.push({ ch: " ", st: PLAIN });
    l[this.cx] = { ch, st: this.st };
    this.cx++;
  }

  write(data: string) {
    const s = this.pending + data;
    this.pending = "";
    let i = 0;
    while (i < s.length) {
      const c = s[i];
      if (c === "\x1b") {
        const n = this.escape(s, i);
        if (n < 0) {
          this.pending = s.slice(i); // incomplete sequence: wait for more data
          return;
        }
        i = n;
        continue;
      }
      i++;
      switch (c) {
        case "\r":
          this.cx = 0;
          break;
        case "\n":
          this.newline();
          break;
        case "\b":
          this.cx = Math.max(0, this.cx - 1);
          break;
        case "\t":
          this.cx = Math.min(this.cols - 1, (Math.floor(this.cx / 8) + 1) * 8);
          break;
        case "\x07": // bell
          break;
        default:
          if (c >= " " && c !== "\x7f") this.put(c);
      }
    }
  }

  /** Parses an escape sequence at s[i] (ESC). Returns the next index, or -1 if incomplete. */
  private escape(s: string, i: number): number {
    if (i + 1 >= s.length) return -1;
    const k = s[i + 1];
    if (k === "[") {
      let j = i + 2;
      while (j < s.length && (s.charCodeAt(j) < 0x40 || s.charCodeAt(j) > 0x7e)) j++;
      if (j >= s.length) return s.length - i > 64 ? i + 2 : -1;
      this.csi(s.slice(i + 2, j), s[j]);
      return j + 1;
    }
    if (k === "]" || k === "P" || k === "_" || k === "^") {
      // OSC/DCS…: until BEL or ST (ESC \)
      for (let j = i + 2; j < s.length; j++) {
        if (s[j] === "\x07") return j + 1;
        if (s[j] === "\x1b" && s[j + 1] === "\\") return j + 2;
      }
      return s.length - i > 4096 ? s.length : -1;
    }
    if (k === "(" || k === ")" || k === "#") return i + 3 <= s.length ? i + 3 : -1; // charset selection
    if (k === "7") this.saved = [this.cy - this.top, this.cx];
    if (k === "8") [this.cy, this.cx] = [this.top + this.saved[0], this.saved[1]];
    if (k === "c") this.reset();
    if (k === "M") this.cy = Math.max(this.top, this.cy - 1); // reverse index
    return i + 2;
  }

  private csi(params: string, final: string) {
    const priv = params.startsWith("?");
    const ps = (priv ? params.slice(1) : params).split(";").map((p) => (p === "" ? NaN : parseInt(p, 10)));
    const n = (d = 1) => (Number.isNaN(ps[0]) || ps[0] === undefined ? d : ps[0]);
    switch (final) {
      case "m":
        this.sgr(ps);
        break;
      case "A":
        this.cy = Math.max(this.top, this.cy - n());
        break;
      case "B":
        this.cy = this.cy + n();
        this.line(this.cy);
        break;
      case "C":
        this.cx = Math.min(this.cols - 1, this.cx + n());
        break;
      case "D":
        this.cx = Math.max(0, this.cx - n());
        break;
      case "G":
        this.cx = Math.min(this.cols - 1, n() - 1);
        break;
      case "d":
        this.cy = this.top + Math.min(this.rows - 1, n() - 1);
        this.line(this.cy);
        break;
      case "H":
      case "f": {
        const r = Number.isNaN(ps[0]) || ps[0] === undefined ? 1 : ps[0];
        const c = Number.isNaN(ps[1]) || ps[1] === undefined ? 1 : ps[1];
        this.cy = this.top + Math.min(this.rows - 1, Math.max(0, r - 1));
        this.cx = Math.min(this.cols - 1, Math.max(0, c - 1));
        this.line(this.cy);
        break;
      }
      case "K": {
        const l = this.line(this.cy);
        const mode = n(0);
        if (mode === 0) l.length = Math.min(l.length, this.cx);
        else if (mode === 1) for (let x = 0; x <= this.cx && x < l.length; x++) l[x] = { ch: " ", st: PLAIN };
        else l.length = 0;
        break;
      }
      case "J": {
        const mode = n(0);
        if (mode === 2 || mode === 3) {
          // clear screen: push the old screen into scrollback like xterm does
          const top = this.top;
          for (let y = top; y < this.lines.length; y++) this.lines[y] = [];
          if (mode === 3) this.lines = this.lines.slice(top);
          this.cy = Math.max(0, mode === 3 ? 0 : this.cy);
        } else if (mode === 0) {
          this.line(this.cy).length = Math.min(this.line(this.cy).length, this.cx);
          this.lines.length = this.cy + 1;
        } else if (mode === 1) {
          for (let y = this.top; y < this.cy; y++) this.lines[y] = [];
        }
        break;
      }
      case "P": {
        this.line(this.cy).splice(this.cx, n());
        break;
      }
      case "@": {
        const l = this.line(this.cy);
        if (this.cx <= l.length) l.splice(this.cx, 0, ...Array.from({ length: n() }, () => ({ ch: " ", st: PLAIN })));
        break;
      }
      case "X": {
        const l = this.line(this.cy);
        for (let x = this.cx; x < this.cx + n() && x < l.length; x++) l[x] = { ch: " ", st: PLAIN };
        break;
      }
      case "s":
        this.saved = [this.cy - this.top, this.cx];
        break;
      case "u":
        this.cy = this.top + this.saved[0];
        this.cx = this.saved[1];
        break;
      case "h":
      case "l":
        // alternate screen (1049/47/1047): treat as clear
        if (priv && ps.some((p) => p === 1049 || p === 47 || p === 1047)) {
          for (let y = this.top; y < this.lines.length; y++) this.lines[y] = [];
          this.cy = this.top;
          this.cx = 0;
        }
        break;
      default:
      // unsupported: ignore
    }
  }

  private sgr(ps: number[]) {
    if (ps.length === 0 || (ps.length === 1 && Number.isNaN(ps[0]))) ps = [0];
    const st = { ...this.st };
    for (let i = 0; i < ps.length; i++) {
      const p = Number.isNaN(ps[i]) ? 0 : ps[i];
      if (p === 0) Object.assign(st, PLAIN);
      else if (p === 1) st.bold = true;
      else if (p === 2) st.dim = true;
      else if (p === 3) st.italic = true;
      else if (p === 4) st.underline = true;
      else if (p === 7) st.inverse = true;
      else if (p === 22) st.bold = st.dim = false;
      else if (p === 23) st.italic = false;
      else if (p === 24) st.underline = false;
      else if (p === 27) st.inverse = false;
      else if (p >= 30 && p <= 37) st.fg = BASE16[p - 30];
      else if (p >= 90 && p <= 97) st.fg = BASE16[p - 90 + 8];
      else if (p === 39) st.fg = null;
      else if (p >= 40 && p <= 47) st.bg = BASE16[p - 40];
      else if (p >= 100 && p <= 107) st.bg = BASE16[p - 100 + 8];
      else if (p === 49) st.bg = null;
      else if (p === 38 || p === 48) {
        let col: string | null = null;
        if (ps[i + 1] === 5 && ps[i + 2] !== undefined) {
          col = color256(ps[i + 2] & 255);
          i += 2;
        } else if (ps[i + 1] === 2 && ps[i + 4] !== undefined) {
          col = `rgb(${ps[i + 2] & 255},${ps[i + 3] & 255},${ps[i + 4] & 255})`;
          i += 4;
        }
        if (p === 38) st.fg = col;
        else st.bg = col;
      }
    }
    this.st = st;
  }

  /** Lines as style runs, for rendering. */
  runs(): Run[][] {
    return this.lines.map((l) => {
      const out: Run[] = [];
      for (const c of l) {
        const last = out[out.length - 1];
        if (last && last.st === c.st) last.text += c.ch;
        else out.push({ text: c.ch, st: c.st });
      }
      return out;
    });
  }

  /** Plain text (for copy and tests). */
  text(): string {
    return this.lines.map((l) => l.map((c) => c.ch).join("").trimEnd()).join("\n").trimEnd();
  }
}

/** Maps a keyboard event to the bytes a terminal would send, or null to let the browser handle it. */
export function keyToBytes(e: { key: string; ctrlKey: boolean; altKey: boolean; metaKey: boolean; shiftKey: boolean }): string | null {
  if (e.metaKey) return null; // Cmd+C / Cmd+V stay with the browser
  if (e.ctrlKey && e.shiftKey) return null; // Ctrl+Shift+C/V: copy/paste
  const special: Record<string, string> = {
    Enter: "\r",
    Backspace: "\x7f",
    Tab: "\t",
    Escape: "\x1b",
    ArrowUp: "\x1b[A",
    ArrowDown: "\x1b[B",
    ArrowRight: "\x1b[C",
    ArrowLeft: "\x1b[D",
    Home: "\x1b[H",
    End: "\x1b[F",
    Delete: "\x1b[3~",
    Insert: "\x1b[2~",
    PageUp: "\x1b[5~",
    PageDown: "\x1b[6~",
  };
  if (e.key === "Tab" && e.shiftKey) return null; // Shift+Tab leaves the terminal (keyboard users)
  if (special[e.key]) return (e.altKey ? "\x1b" : "") + special[e.key];
  if (e.ctrlKey && e.key.length === 1) {
    const k = e.key.toLowerCase();
    if (k >= "a" && k <= "z") return String.fromCharCode(k.charCodeAt(0) - 96);
    const map: Record<string, string> = { "[": "\x1b", "\\": "\x1c", "]": "\x1d", "^": "\x1e", _: "\x1f", " ": "\x00", "@": "\x00" };
    return map[k] ?? null;
  }
  if (e.key.length === 1) return (e.altKey ? "\x1b" : "") + e.key;
  return null;
}
