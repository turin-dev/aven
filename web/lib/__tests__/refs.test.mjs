import test from "node:test";
import assert from "node:assert/strict";
import { refProblems, refsIn, resolvePreview, referencedServices, tokenize } from "@/lib/refs";

const scope = {
  self: "web",
  scopes: {
    shared: { DOMAIN: "example.com", HOST: "app.${{ DOMAIN }}" },
    web: { PORT: undefined, NAME: "web", A: "${{ B }}", B: "${{ A }}" },
    postgres: { PGHOST: "postgres.internal", PGPASSWORD: undefined, DATABASE_URL: "postgresql://u:${{PGPASSWORD}}@${{PGHOST}}/db" },
  },
};

test("refsIn / tokenize / referencedServices", () => {
  assert.deepEqual(refsIn("a ${{ postgres.X }} ${{Y}}"), [
    { ns: "postgres", key: "X", raw: "${{ postgres.X }}" },
    { ns: null, key: "Y", raw: "${{Y}}" },
  ]);
  assert.deepEqual(
    tokenize("x${{a.b}}y").map((s) => s.text),
    ["x", "${{a.b}}", "y"],
  );
  assert.deepEqual([...referencedServices({ A: "${{ shared.X }} ${{ redis.URL }}", B: "${{ Z }}" })], ["redis"]);
});

test("refProblems", () => {
  assert.deepEqual(refProblems("${{ postgres.PGHOST }}:${{ PORT }} ${{ shared.DOMAIN }}", scope), []);
  assert.deepEqual(
    refProblems("${{ pg.HOST }} ${{ postgres.NOPE }} ${{ NOPE }} ${{ shared.NOPE }}", scope).map((p) => p.kind),
    ["service", "variable", "variable", "variable"],
  );
  assert.deepEqual(refProblems("${{ postgres.PGHOST", scope), [{ kind: "malformed", raw: "${{ postgres.PGHOST" }]);
  assert.deepEqual(refProblems("${{ bad name }}", scope), [{ kind: "malformed", raw: "${{ bad name }}" }]);
  assert.deepEqual(refProblems("http://${HOST}/", scope), [{ kind: "shell", raw: "${HOST}", key: "HOST" }]);
  // the same problem twice is reported once
  assert.equal(refProblems("${{ X }}${{ X }}", scope).length, 1);
});

test("resolvePreview follows the server's scoping", () => {
  const p = resolvePreview("${{ postgres.DATABASE_URL }}", scope, "DB");
  assert.equal(p.value, "postgresql://u:${{PGPASSWORD}}@postgres.internal/db");
  assert.deepEqual(p.unknown, ["postgres.PGPASSWORD"]);
  assert.deepEqual(resolvePreview("https://${{ shared.HOST }}", scope).value, "https://app.example.com");
  const m = resolvePreview("x${{ NOPE }}y", scope);
  assert.equal(m.value, "xy");
  assert.deepEqual(m.missing, ["web.NOPE"]);
});

test("resolvePreview detects cycles, including through the edited variable", () => {
  assert.deepEqual(resolvePreview("${{ A }}", scope, "C").cycle, ["web.A", "web.B", "web.A"]);
  assert.deepEqual(resolvePreview("${{ B }}", { ...scope, scopes: { ...scope.scopes, web: { ...scope.scopes.web, B: "${{ C }}" } } }, "C").cycle, ["web.C", "web.B", "web.C"]);
  assert.equal(resolvePreview("${{ NAME }}", scope, "C").cycle, null);
});
