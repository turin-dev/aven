// Run from web/: node --no-warnings --test "lib/__tests__/*.test.mjs"
import { test } from "node:test";
import assert from "node:assert/strict";
import { withoutSent, pendingSummary } from "../staged.ts";

test("withoutSent keeps edits made while the save was in flight", () => {
  const sent = { vars: { A: "1", B: null }, settings: { replicas: 2 } };
  const current = { vars: { A: "2", B: null, C: "new" }, settings: { replicas: 2, port: 8080 } };
  assert.deepEqual(withoutSent(current, sent), { vars: { A: "2", C: "new" }, settings: { port: 8080 } });
  assert.deepEqual(withoutSent(sent, sent), { vars: {}, settings: {} });
});

test("pendingSummary merges local and server changes", () => {
  const inst = { pending_changes: ["variables.A", "port"] };
  const s = pendingSummary(inst, { vars: { A: "x", B: "y" }, settings: {} });
  assert.deepEqual(s.all.sort(), ["port", "variables.A", "variables.B"]);
  assert.equal(s.count, 3);
});
