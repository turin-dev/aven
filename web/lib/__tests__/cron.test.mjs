// Mirrors internal/cronx: the dashboard's validation and next-run preview must agree with the server's scheduler.
import test from "node:test";
import assert from "node:assert/strict";
import { cronError, describeCron, nextRuns, parseCron } from "@/lib/cron";

const at = (s) => new Date(`${s.replace(" ", "T")}:00Z`);
const iso = (d) => d.toISOString().slice(0, 16).replace("T", " ");
/** True when `expr` fires at minute `s` (UTC): the first run after the minute before is that minute. */
const fires = (expr, s) => {
  const [d] = nextRuns(expr, 1, new Date(at(s).getTime() - 60_000));
  return !!d && iso(d) === s;
};

test("matches like the server", () => {
  for (const [expr, when, want] of [
    ["* * * * *", "2026-10-16 12:34", true],
    ["*/5 * * * *", "2026-10-16 12:35", true],
    ["*/5 * * * *", "2026-10-16 12:36", false],
    ["0 3 * * *", "2026-10-16 03:00", true],
    ["0 3 * * *", "2026-10-16 04:00", false],
    ["15,45 9-17 * * MON-FRI", "2026-10-16 09:45", true],
    ["15,45 9-17 * * MON-FRI", "2026-10-17 09:45", false],
    ["0 0 * * 7", "2026-10-18 00:00", true],
    ["0 0 1 jan *", "2027-01-01 00:00", true],
    ["0 0 13 * 5", "2026-10-16 00:00", true], // both day fields restricted: OR
    ["10-20/5 * * * *", "2026-10-16 00:15", true],
    ["10-20/5 * * * *", "2026-10-16 00:25", false],
    ["@hourly", "2026-10-16 07:00", true],
    ["@daily", "2026-10-16 07:00", false],
    // dom "*/2" counts as a star on the server: AND with the weekday, not OR.
    ["0 0 */2 * 1", "2026-10-19 00:00", true], // Monday the 19th (odd day)
    ["0 0 */2 * 1", "2026-10-21 00:00", false], // Wednesday the 21st: odd day but not Monday
    ["0 0 */2 * 1", "2026-10-26 00:00", false], // Monday the 26th: even day
  ]) {
    assert.equal(fires(expr, when), want, `${expr} @ ${when}`);
  }
});

test("rejects what the server rejects", () => {
  for (const e of ["", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "a b c d e", "5-1 * * * *", "* * * * * *",
    "1,,2 * * * *", "5, * * * *", "1.5 * * * *", "0x5 * * * *", "1e1 * * * *", "1-2-3 * * * *", "*/ * * * *", "- * * * *"]) {
    assert.notEqual(cronError(e), null, JSON.stringify(e));
  }
  for (const e of ["0 3 * * *", " 0  3 * * * ", "@weekly", "0 0 * * sun", "0 0 1 JAN-MAR *", "*/15 9-17 * * 1-5"]) {
    assert.equal(cronError(e), null, e);
  }
});

test("next runs", () => {
  const from = at("2026-10-16 12:34");
  assert.deepEqual(nextRuns("0 3 * * *", 2, from).map(iso), ["2026-10-17 03:00", "2026-10-18 03:00"]);
  assert.deepEqual(nextRuns("*/20 * * * *", 3, from).map(iso), ["2026-10-16 12:40", "2026-10-16 13:00", "2026-10-16 13:20"]);
  // Leap day: more than two years away from March 2028.
  assert.deepEqual(nextRuns("0 0 29 2 *", 1, at("2028-03-01 00:00")).map(iso), ["2032-02-29 00:00"]);
  assert.deepEqual(nextRuns("0 0 31 2 *", 1, from), []);
  assert.deepEqual(nextRuns("not cron", 1, from), []);
});

test("descriptions", () => {
  assert.equal(describeCron("0 3 * * *"), "매일 03:00 (UTC)");
  assert.equal(describeCron("*/15 * * * *"), "15분마다 (UTC)");
  assert.equal(describeCron("0 * * * *"), "매시 정각 (UTC)");
  assert.equal(describeCron("0 9-17 * * 1-5"), "평일 9시–17시 정각 (UTC)");
  assert.equal(describeCron("*/15 9-17 * * 1-5"), "평일 9시–17시 15분마다 (UTC)");
  assert.equal(describeCron("0 0 13 * 5"), "매월 13일 또는 금요일 00:00 (UTC)");
  assert.equal(describeCron("0 0 */10 * 1"), "매월 1일, 11일, 21일, 31일 중 월요일 00:00 (UTC)");
  assert.equal(describeCron("bad"), "");
  assert.ok(parseCron("@daily"));
});
