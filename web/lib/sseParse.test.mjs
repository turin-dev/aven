// Run: node --test --experimental-strip-types lib/sseParse.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { createSSEParser } from "./sseParse.ts";

function parse(chunks) {
  const out = [];
  const p = createSSEParser((m) => out.push(m));
  chunks.forEach((c) => p.push(c));
  p.end();
  return out;
}

const cases = [
  { name: "basic", chunks: ['event: token\ndata: {"text":"hi"}\n\n'], want: [{ event: "token", data: '{"text":"hi"}' }] },
  { name: "no space after colon", chunks: ["event:token\ndata:x\n\n"], want: [{ event: "token", data: "x" }] },
  { name: "CRLF", chunks: ["event: a\r\ndata: 1\r\n\r\n"], want: [{ event: "a", data: "1" }] },
  { name: "CRLF split across chunks", chunks: ["event: a\r", "\ndata: 1\r", "\n\r", "\n"], want: [{ event: "a", data: "1" }] },
  { name: "bare CR", chunks: ["data: 1\r\r"], want: [{ event: "message", data: "1" }] },
  { name: "comments are skipped", chunks: [": keep-alive\n\nevent: a\ndata: 1\n\n"], want: [{ event: "a", data: "1" }] },
  { name: "multi-line data", chunks: ["data: a\ndata: b\n\n"], want: [{ event: "message", data: "a\nb" }] },
  { name: "split mid-line", chunks: ["ev", "ent: tok", "en\nda", "ta: x\n", "\n"], want: [{ event: "token", data: "x" }] },
  { name: "unterminated last event", chunks: ["event: done\ndata: z"], want: [{ event: "done", data: "z" }] },
  { name: "event name resets", chunks: ["event: a\ndata: 1\n\ndata: 2\n\n"], want: [{ event: "a", data: "1" }, { event: "message", data: "2" }] },
  { name: "empty event without data is dropped", chunks: ["event: a\n\n"], want: [] },
  { name: "only one leading space stripped", chunks: ["data:  two\n\n"], want: [{ event: "message", data: " two" }] },
];

for (const c of cases)
  test(c.name, () => {
    assert.deepEqual(
      parse(c.chunks).map(({ event, data }) => ({ event, data })),
      c.want,
    );
  });
