// Run: node --test scripts/safe-redirect.test.mjs (node 22.18+ strips the TypeScript types).
import { test } from "node:test";
import assert from "node:assert/strict";
import { safeNextPath } from "../lib/safeRedirect.ts";

const origin = "https://aven.example.com";

test("keeps same-origin paths with query and fragment", () => {
  for (const [input, want] of [
    ["/", "/"],
    ["/project/abc/?env=prod", "/project/abc/?env=prod"],
    ["/oauth/auth?client_id=x&redirect_uri=https%3A%2F%2Fapp.example&state=s", "/oauth/auth?client_id=x&redirect_uri=https%3A%2F%2Fapp.example&state=s"],
    ["/account/?section=security#passkeys", "/account/?section=security#passkeys"],
    ["/a/../b", "/b"],
  ]) {
    assert.equal(safeNextPath(input, origin), want, input);
  }
});

test("rejects anything that can leave the origin", () => {
  for (const input of [
    null,
    undefined,
    "",
    "https://evil.example/",
    "//evil.example",
    "///evil.example",
    "/\\evil.example",
    "\\\\evil.example",
    "/\t/evil.example",
    "/\n/evil.example",
    "/\r/evil.example",
    "/ /evil.example",
    "/.//evil.example",
    "/x/..//evil.example",
    "javascript:alert(1)",
    "evil.example",
    "/" + "a".repeat(3000),
  ]) {
    assert.equal(safeNextPath(input, origin), "/", JSON.stringify(input));
  }
});

test("rejects paths that loop back into sign-in", () => {
  for (const input of ["/login", "/login/", "/login/?next=/x", "/invite/"]) {
    assert.equal(safeNextPath(input, origin), "/", input);
  }
  assert.equal(safeNextPath("/loginx", origin), "/loginx");
});

test("uses the given fallback", () => {
  assert.equal(safeNextPath("//evil.example", origin, "/home"), "/home");
});
