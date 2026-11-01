// Run from web/: node --no-warnings --test "lib/__tests__/*.test.mjs"
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  backoffDelay,
  buildStep,
  highlightTerms,
  levelOf,
  makeFilter,
  markSegments,
  matchRanges,
  parseAnsi,
  stageTimes,
  stripAnsi,
  fmtSpan,
} from "../logs.ts";

test("levelOf", () => {
  const cases = [
    ["==> building", "stage"],
    ["Error: boom", "error"],
    ["panic: nil map", "error"],
    ["warning: deprecated", "warn"],
    ["level=WARN slow", "warn"],
    ["==> healthcheck OK", "stage"],
    ["healthcheck OK", "ok"],
    ["GET / 200", "info"],
  ];
  for (const [line, want] of cases) assert.equal(levelOf(line), want, line);
});

test("makeFilter: tokens, phrases and exclusions", () => {
  const f = makeFilter('get -healthz "api/me"');
  assert.equal(f("GET /api/me 200"), true);
  assert.equal(f("GET /healthz /api/me"), false);
  assert.equal(f("GET /api/posts"), false);
  assert.equal(makeFilter("   ")("anything"), true);
});

test("highlightTerms skips exclusions and duplicates", () => {
  assert.deepEqual(highlightTerms('Foo -bar "a b" foo'), ["foo", "a b"]);
});

test("matchRanges merges overlaps, case-insensitive", () => {
  assert.deepEqual(matchRanges("abcABC", ["abc"]), [[0, 6]]);
  assert.deepEqual(matchRanges("abc-ABC", ["abc"]), [[0, 3], [4, 7]]);
  assert.deepEqual(matchRanges("hello world", ["lo w", "hell"]), [[0, 7]]);
  assert.deepEqual(matchRanges("xyz", []), []);
  assert.deepEqual(matchRanges("aaaa", ["aa"]), [[0, 4]]);
});

test("parseAnsi: SGR colours and resets", () => {
  assert.deepEqual(parseAnsi("plain"), [{ text: "plain" }]);
  const segs = parseAnsi("\x1b[1;31mERR\x1b[0m ok \x1b[38;5;196mx\x1b[39m");
  assert.deepEqual(segs, [
    { text: "ERR", st: { bold: true, fg: "red" } },
    { text: " ok " },
    { text: "x", st: { fg: "rgb(255,0,0)" } },
  ]);
  assert.deepEqual(parseAnsi("\x1b[38;2;1;2;3mrgb"), [{ text: "rgb", st: { fg: "rgb(1,2,3)" } }]);
  assert.deepEqual(parseAnsi("\x1b[92mhi"), [{ text: "hi", st: { fg: "bright-green" } }]);
});

test("parseAnsi drops cursor and OSC sequences and keeps the last \\r frame", () => {
  assert.deepEqual(parseAnsi("\x1b[2K\x1b[1Gdone"), [{ text: "done" }]);
  assert.deepEqual(parseAnsi("\x1b]0;title\x07text"), [{ text: "text" }]);
  assert.equal(stripAnsi(" 10%\r 50%\r100%"), "100%");
  assert.equal(stripAnsi("progress 100%\r"), "progress 100%");
  assert.equal(stripAnsi("\x1b[31mred\x1b[0m"), "red");
});

test("markSegments splits across segment boundaries", () => {
  const segs = [{ text: "abc", st: { fg: "red" } }, { text: "def" }];
  assert.deepEqual(markSegments(segs, [[2, 4]]), [
    { text: "ab", st: { fg: "red" } },
    { text: "c", st: { fg: "red" }, mark: true },
    { text: "d", mark: true },
    { text: "ef" },
  ]);
  assert.deepEqual(markSegments(segs, []), segs);
  assert.deepEqual(markSegments([{ text: "aXbX" }], [[1, 2], [3, 4]]), [
    { text: "a" },
    { text: "X", mark: true },
    { text: "b" },
    { text: "X", mark: true },
  ]);
});

test("stageTimes records the first time of each stage", () => {
  const base = new Date("2026-03-01T10:00:00Z");
  const t = stageTimes(
    [
      "10:00:01 ==> initializing",
      "10:00:02 ==> building",
      "10:00:05 #1 [1/2] FROM node",
      "10:00:40 ==> deploying",
      "10:00:41 ==> healthcheck /healthz",
      "10:00:50 ==> healthcheck OK",
      "10:00:51 ==> live",
    ],
    base,
  );
  assert.equal(t.initializing, Date.parse("2026-03-01T10:00:01Z"));
  assert.equal(t.building, Date.parse("2026-03-01T10:00:02Z"));
  assert.equal(t.deploying, Date.parse("2026-03-01T10:00:40Z"));
  assert.equal(t.healthchecking, Date.parse("2026-03-01T10:00:41Z"));
  assert.equal(t.live, Date.parse("2026-03-01T10:00:51Z"));
  assert.equal(t.pre_deploy, undefined);
});

test("stageTimes rolls over midnight", () => {
  const t = stageTimes(["23:59:59 ==> building", "00:00:10 ==> deploying"], new Date("2026-03-01T23:59:50Z"));
  assert.equal(t.deploying, Date.parse("2026-03-02T00:00:10Z"));
});

test("buildStep finds the latest BuildKit step", () => {
  assert.deepEqual(buildStep(["#4 [1/6] FROM x", "#7 [4/6] RUN npm ci", "#7 12.4 added 214 packages"]), { n: 4, of: 6, text: "RUN npm ci" });
  assert.deepEqual(buildStep(["#3 [builder 2/5] COPY . ."]), { n: 2, of: 5, text: "COPY . ." });
  assert.equal(buildStep(["no steps here"]), null);
});

test("backoffDelay grows, caps and jitters", () => {
  assert.equal(backoffDelay(1, () => 1), 1000);
  assert.equal(backoffDelay(1, () => 0), 500);
  assert.equal(backoffDelay(3, () => 1), 4000);
  assert.equal(backoffDelay(20, () => 1), 15000);
  assert.equal(backoffDelay(0, () => 1), 1000);
});

test("fmtSpan", () => {
  assert.equal(fmtSpan(4_400), "4s");
  assert.equal(fmtSpan(64_000), "1m 04s");
  assert.equal(fmtSpan(3_720_000), "1h 02m");
  assert.equal(fmtSpan(-5), "0s");
});
