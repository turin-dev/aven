// Run from web/: node --no-warnings --test "lib/__tests__/*.test.mjs"
import { test } from "node:test";
import assert from "node:assert/strict";
import { Terminal, isWide, keyToBytes } from "../terminal.ts";

const key = (k, mods = {}) => ({ key: k, ctrlKey: false, altKey: false, metaKey: false, shiftKey: false, ...mods });

test("keyToBytes: control keys, specials and browser shortcuts", () => {
  assert.equal(keyToBytes(key("c", { ctrlKey: true })), "\x03");
  assert.equal(keyToBytes(key("Enter")), "\r");
  assert.equal(keyToBytes(key("ArrowUp", { altKey: true })), "\x1b\x1b[A");
  assert.equal(keyToBytes(key("v", { ctrlKey: true, shiftKey: true })), null);
  assert.equal(keyToBytes(key("c", { metaKey: true })), null);
  assert.equal(keyToBytes(key("Tab", { shiftKey: true })), null);
  assert.equal(keyToBytes(key("x", { altKey: true })), "\x1bx");
  assert.equal(keyToBytes(key("한")), "한");
});

test("keyToBytes: AltGr (Ctrl+Alt) printable characters are typed, not sent as control codes", () => {
  // German layout: AltGr+Q = "@", AltGr+ß = "\\", AltGr+8 = "["
  assert.equal(keyToBytes(key("@", { ctrlKey: true, altKey: true })), "@");
  assert.equal(keyToBytes(key("\\", { ctrlKey: true, altKey: true })), "\\");
  assert.equal(keyToBytes(key("[", { ctrlKey: true, altKey: true })), "[");
  assert.equal(keyToBytes(key("€", { ctrlKey: true, altKey: true })), "€");
  // Ctrl+Alt+letter stays a meta-prefixed control code.
  assert.equal(keyToBytes(key("a", { ctrlKey: true, altKey: true })), "\x1b\x01");
  // Plain Ctrl+[ is still Escape.
  assert.equal(keyToBytes(key("[", { ctrlKey: true })), "\x1b");
});

test("Terminal: wide characters take two columns", () => {
  assert.equal(isWide("한".codePointAt(0)), true);
  assert.equal(isWide("a".codePointAt(0)), false);
  const t = new Terminal(10, 5);
  t.write("한글ab");
  assert.equal(t.cx, 6);
  assert.equal(t.text(), "한글ab");
  // Cursor addressing after wide text lands on the right cell.
  t.write("\x1b[1;5HX");
  assert.equal(t.text(), "한글Xb");
});

test("Terminal: wide character at the last column wraps", () => {
  const t = new Terminal(5, 5);
  t.write("abcd한");
  assert.equal(t.text(), "abcd\n한");
});

test("Terminal: surrogate pairs stay together, even split across writes", () => {
  const t = new Terminal(20, 5);
  const smile = "😀";
  t.write("a" + smile[0]);
  t.write(smile[1] + "b");
  assert.equal(t.text(), "a😀b");
  assert.equal(t.cx, 4);
});

test("Terminal: cursor down is clamped to the screen", () => {
  const t = new Terminal(10, 3);
  t.write("\x1b[50B");
  assert.equal(t.lines.length, 3);
  assert.equal(t.cy, 2);
});

test("Terminal: runs keep one cell per column", () => {
  const t = new Terminal(10, 3);
  t.write("\x1b[31m한a\x1b[0mb");
  const [line] = t.runs();
  assert.deepEqual(line.map((r) => r.cells), [["한", "", "a"], ["b"]]);
  assert.equal(line[0].st.fg, "#cd3131");
});
