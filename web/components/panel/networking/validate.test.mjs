// Run with: node --test web/components/panel/networking/validate.test.mjs
// Node strips the types of validate.ts itself (Node 22.18+ / 24).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  actionProblem,
  checkDomain,
  checkRedirectTarget,
  clauseProblem,
  cleanDiagHost,
  invalidIPs,
  isIPOrCIDR,
  isPort,
  normalizeHostInput,
  parseResolved,
  pathRuleProblems,
  shortDuration,
} from "./validate.ts";

test("normalizeHostInput strips scheme, path, trailing dot and converts IDN", () => {
  const cases = [
    ["  Example.COM ", "example.com"],
    ["https://app.example.com/login?x=1", "app.example.com"],
    ["example.com.", "example.com"],
    ["한국.com", "xn--3e0b707e.com"],
    ["*.Apps.Example.com", "*.apps.example.com"],
    ["example.com:8080", "example.com:8080"],
  ];
  for (const [input, want] of cases) assert.equal(normalizeHostInput(input), want, input);
});

test("checkDomain mirrors the server's domainRe and wildcard rule", () => {
  const cases = [
    ["example.com", null],
    ["a.b.example.co", null],
    ["*.example.com", null],
    ["xn--3e0b707e.com", null],
    ["", "empty"],
    ["localhost", "single_label"],
    ["*.com", "wildcard_short"],
    ["a.*.example.com", "wildcard_position"],
    ["foo..com", "label_empty"],
    [".example.com", "label_empty"],
    ["-a.example.com", "hyphen"],
    ["a-.example.com", "hyphen"],
    ["my_app.example.com", "chars"],
    ["exa mple.com", "chars"],
    ["example.c0m", "tld"],
    ["example.c", "tld"],
    ["1.2.3.4", "ip"],
    ["2001:db8::1", "ip"],
    ["example.com:8080", "port"],
    [`${"a".repeat(64)}.com`, "label_long"],
    [`${"abcdefghi.".repeat(26)}com`, "too_long"],
  ];
  for (const [host, want] of cases) assert.equal(checkDomain(host), want, host);
  assert.equal(checkDomain("api.example.com", ["API.example.com"]), "duplicate");
});

test("isPort", () => {
  for (const ok of ["1", "80", "65535"]) assert.ok(isPort(ok), ok);
  for (const bad of ["", "0", "65536", "8o", "-1", "123456"]) assert.ok(!isPort(bad), bad);
});

test("cleanDiagHost accepts names and addresses only", () => {
  assert.equal(cleanDiagHost(" Postgres "), "postgres");
  assert.equal(cleanDiagHost("postgres.production.keel.internal"), "postgres.production.keel.internal");
  assert.equal(cleanDiagHost("https://api.example.com/health"), "api.example.com");
  assert.equal(cleanDiagHost("[fd00::1]"), "fd00::1");
  assert.equal(cleanDiagHost("10.0.0.5"), "10.0.0.5");
  assert.equal(cleanDiagHost("redis_cache"), "redis_cache");
  for (const bad of ["", "a b", "foo;rm -rf", "$(id)", "foo..bar"]) assert.equal(cleanDiagHost(bad), null, bad);
});

test("pathRuleProblems", () => {
  assert.deepEqual(
    pathRuleProblems([
      { path: "/api/*" },
      { path: "api" },
      { path: "/api/*" },
      { path: "/x", strip_prefix: true },
      { path: "/y", target_port: 70000 },
      { path: "" },
    ]),
    [null, "path", "duplicate", "strip", "port", null],
  );
});

test("checkRedirectTarget", () => {
  assert.equal(checkRedirectTarget("example.com", "www.example.com"), null);
  assert.equal(checkRedirectTarget("https://example.com/base", "www.example.com"), null);
  assert.equal(checkRedirectTarget("https://WWW.example.com/x", "www.example.com"), "loop");
  assert.equal(checkRedirectTarget("javascript:alert(1)", "www.example.com"), "invalid");
  assert.equal(checkRedirectTarget("exa mple.com", "www.example.com"), "invalid");
});

test("isIPOrCIDR / invalidIPs", () => {
  for (const ok of ["203.0.113.7", "198.51.100.0/24", "2001:db8::/32", "::1", "0.0.0.0/0"]) assert.ok(isIPOrCIDR(ok), ok);
  for (const bad of ["300.1.1.1", "10.0.0.0/33", "2001:db8::/129", "example.com", "1.2.3.4/24/1", "[::1]"]) assert.ok(!isIPOrCIDR(bad), bad);
  assert.deepEqual(invalidIPs(["1.2.3.4", "nope", "10.0.0.0/8"]), ["nope"]);
});

test("clauseProblem", () => {
  assert.equal(clauseProblem({ attr: "http.path", op: "matches", value: "/admin*" }), null);
  assert.equal(clauseProblem({ attr: "http.path", op: "eq", value: " " }), "empty");
  assert.equal(clauseProblem({ attr: "http.header", op: "eq", key: "", value: "x" }), "key");
  assert.equal(clauseProblem({ attr: "ip.src", op: "in", value: ["10.0.0.0/8", "bogus"] }), "ip");
  assert.equal(clauseProblem({ attr: "ipv4.src", op: "eq", value: "2001:db8::1" }), "ipv4");
  assert.equal(clauseProblem({ attr: "http.method", op: "in", value: ["GET", "PO ST"] }), "method");
  assert.equal(clauseProblem({ attr: "http.method", op: "in", value: [] }), "empty");
  assert.equal(clauseProblem({ attr: "http.path", op: "matches", value: "*".repeat(9) }), "wildcards");
});

test("actionProblem", () => {
  assert.equal(actionProblem({ action: "block" }), null);
  assert.equal(actionProblem({ action: "block", params: { status: 500 } }), "block_status");
  assert.equal(actionProblem({ action: "redirect", params: {} }), "redirect_target");
  assert.equal(actionProblem({ action: "redirect", params: { location: "/relative" } }), "redirect_location");
  assert.equal(actionProblem({ action: "redirect", params: { location: "https://example.com/new", status: 303 } }), "redirect_status");
  assert.equal(actionProblem({ action: "redirect", params: { host: "www.example.com" } }), null);
  assert.equal(actionProblem({ action: "cache_override", params: { ttl_seconds: 0 } }), "ttl");
  assert.equal(actionProblem({ action: "cache_override", params: { bypass: true } }), null);
  assert.equal(actionProblem({ action: "rewrite", params: { path: "//evil" } }), "rewrite_path");
  assert.equal(actionProblem({ action: "set_header", params: { name: "X-Keel-Foo", value: "1" } }), "header_reserved");
  assert.equal(actionProblem({ action: "set_header", params: { name: "X Frame", value: "1" } }), "header_name");
  assert.equal(actionProblem({ action: "set_header", params: { name: "X-Frame-Options", value: "DENY" } }), null);
});

test("shortDuration", () => {
  assert.equal(shortDuration(90), "90s");
  assert.equal(shortDuration(600), "10m");
  assert.equal(shortDuration(7200), "2h");
  assert.equal(shortDuration(86400), "1d");
});

test("parseResolved reads getent and nslookup output", () => {
  const getent = "10.0.1.5      STREAM postgres\n10.0.1.5      DGRAM\nfd00:1::5     STREAM\n";
  assert.deepEqual(parseResolved(getent), ["10.0.1.5", "fd00:1::5"]);
  const ns = "Server:\t\t127.0.0.11\nAddress:\t127.0.0.11#53\n\nNon-authoritative answer:\nName:\texample.com\nAddress: 93.184.216.34\nName:\texample.com\nAddress: 2606:2800:220:1:248:1893:25c8:1946\n";
  assert.deepEqual(parseResolved(ns), ["93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"]);
  assert.deepEqual(parseResolved("DNS check unavailable"), []);
});
