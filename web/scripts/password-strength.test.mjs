// Run: node --test scripts/password-strength.test.mjs (node 22.18+ strips the TypeScript types).
import { test } from "node:test";
import assert from "node:assert/strict";
import { passwordStrength } from "../lib/passwordStrength.ts";

test("weak passwords score low", () => {
  for (const pw of ["", "a", "password", "Password123!", "12345678", "qwertyuiop", "aaaaaaaaaaaa", "abcdefgh"]) {
    assert.ok(passwordStrength(pw).score <= 1, `${pw}: ${passwordStrength(pw).score}`);
  }
  assert.equal(passwordStrength("password").score, 0);
  assert.ok(passwordStrength("password").hints.includes("common"));
  assert.ok(passwordStrength("short").hints.includes("short"));
});

test("long, varied passwords score high", () => {
  for (const pw of ["correct horse battery staple", "vR7#kq2!Lm9@xZ", "그린티라떼와 고양이 세 마리"]) {
    assert.ok(passwordStrength(pw).score >= 3, `${pw}: ${passwordStrength(pw).score}`);
  }
});

test("passwords containing the user's name or email are capped", () => {
  const s = passwordStrength("turin-2026-super-long", ["Turin", "turin@example.com"]);
  assert.ok(s.score <= 1);
  assert.ok(s.hints.includes("personal"));
  assert.ok(!passwordStrength("unrelated-long-phrase-77", ["Turin", "turin@example.com"]).hints.includes("personal"));
});

test("score is monotonic enough for a meter", () => {
  assert.ok(passwordStrength("tablelamp").score < passwordStrength("tablelamp-Orbit-93").score);
});
