// Run: node --test sdk/index.test.ts (Node >= 22.18 strips TypeScript types natively).
import assert from "node:assert/strict";
import { test } from "node:test";
import { createFlagClient, evaluateFlag, stableBucket, type FlagDefinition, type FlagSnapshot } from "./index.ts";

const flag = (over: Partial<FlagDefinition>): FlagDefinition => ({
  id: "flag_1",
  project_id: "prj_1",
  name: "checkout-v2",
  type: "bool",
  environment_id: "env_1",
  default_value: false,
  rules: [],
  configured: true,
  ...over,
});

test("stable bucket matches the Go server vectors", () => {
  // Same vectors as internal/featureflags TestStableBucketVectors.
  assert.equal(stableBucket("checkout-v2", "user-42"), 0.84299246949562123);
  assert.equal(stableBucket("checkout-v2", "user_8f3a"), 0.92247836883631251);
  assert.equal(stableBucket("checkout-v2", "한글"), 0.88976060845657412);
});

test("targeting expressions", () => {
  const ctx = { key: "user-42", plan: "enterprise", region: "eu-west", age: 24, tags: ["beta"] };
  const cases: [string, boolean][] = [
    ['plan == "enterprise"', true],
    ['plan == "enterprise" && (region == "eu-west" || age < 18)', true],
    ['!(plan == "free")', true],
    ['plan in ["free", "enterprise"]', true],
    ['region contains "west"', true],
    ['tags contains "beta"', true],
    ['region matches "^eu-.*"', true],
    ['missing == "yes"', false],
    ['!(missing == "yes")', false],
    ["bucket(key) < 0.9", true], // 0.843…
    ["bucket(key) < 0.8", false],
    ["bucket(accountId) < 1", false], // missing bucket attribute never rolls out
    ['plan = "x"', false], // malformed rules fail closed
  ];
  for (const [when, want] of cases) {
    const result = evaluateFlag(flag({ rules: [{ id: "r", when, value: true }] }), ctx);
    assert.equal(result.value, want, when);
  }
});

test("matching rules must agree, otherwise the default wins", () => {
  const f = flag({
    rules: [
      { id: "ent", when: 'plan == "enterprise"', value: true },
      { id: "all", when: "bucket(key) < 1", value: false },
    ],
    default_value: false,
  });
  assert.equal(evaluateFlag(f, { key: "u", plan: "enterprise" }).reason, "CONFLICT");
  assert.deepEqual(evaluateFlag(f, { key: "u", plan: "free" }), {
    value: false,
    reason: "SPLIT",
    trace: [
      { rule_id: "ent", when: 'plan == "enterprise"', matched: false },
      { rule_id: "all", when: "bucket(key) < 1", matched: true },
    ],
  });
  assert.equal(evaluateFlag(f, { plan: "free" }).reason, "NO_MATCH");
});

test("client caches the snapshot, polls with ETag, and types reads", async () => {
  const snapshot: FlagSnapshot = {
    project_id: "prj_1",
    environment_id: "env_1",
    flags: [
      flag({ rules: [{ id: "ent", when: 'plan == "enterprise"', value: true }] }),
      flag({ id: "flag_2", name: "theme", type: "string", default_value: "classic" }),
      flag({ id: "flag_3", name: "model", type: "json", default_value: { name: "small" } }),
    ],
  };
  const calls: { url: string; headers: Record<string, string> }[] = [];
  const fakeFetch = (async (url: string, init?: { headers?: Record<string, string> }) => {
    const headers = init?.headers ?? {};
    calls.push({ url, headers });
    if (headers["If-None-Match"] === '"v1"') return new Response(null, { status: 304 });
    return new Response(JSON.stringify(snapshot), { status: 200, headers: { ETag: '"v1"', "Content-Type": "application/json" } });
  }) as unknown as typeof fetch;

  const client = createFlagClient();
  assert.equal(client.getBoolean("checkout-v2", {}, true), true); // not synced yet → fallback
  await client.init({ baseUrl: "https://aven.example.com/", token: "keel_x", scope: { environmentId: "env_1" }, pollIntervalMs: 0, fetch: fakeFetch });
  assert.equal(calls[0].url, "https://aven.example.com/api/feature-flags?environment_id=env_1");
  assert.equal(calls[0].headers.Authorization, "Bearer keel_x");
  assert.equal(client.projectId, "prj_1");
  assert.equal(client.getBoolean("checkout-v2", { key: "u", plan: "enterprise" }), true);
  assert.equal(client.getBoolean("checkout-v2", { key: "u", plan: "free" }, true), false); // configured default, not fallback
  assert.equal(client.getString("theme", {}, "x"), "classic");
  assert.deepEqual(client.getJson("model"), { name: "small" });
  assert.equal(client.evaluateNumber("theme", {}, 7).reason, "TYPE_MISMATCH");
  assert.equal(client.evaluateBoolean("nope").reason, "NOT_FOUND");

  await client.refresh();
  assert.equal(calls[1].headers["If-None-Match"], '"v1"');
  assert.equal(client.getString("theme"), "classic"); // 304 keeps the cache
  client.close();

  const account = createFlagClient({ baseUrl: "http://k", token: "keel_acct", scope: { projectId: "prj_1" }, pollIntervalMs: 0, fetch: fakeFetch });
  await account.init();
  assert.equal(calls[2].url, "http://k/api/projects/prj_1/flags");
});
