# aven-flags

TypeScript SDK for Aven feature flags. It keeps one environment's flags in memory, refreshes them in the background (`If-None-Match`, 30 s by default) and evaluates targeting rules locally, so reads are synchronous. Works in Node ≥ 18, Bun and Deno (server-side: the token must not be shipped to browsers).

```ts
import { flags } from "aven-flags";

await flags.init(); // AVEN_URL + AVEN_TOKEN (project or environment API token; KEEL_URL and KEEL_TOKEN still work)

const on = flags.getBoolean("checkout-v2", { key: userId, plan: user.plan }, false);
const theme = flags.getString("ui-theme", { key: userId }, "classic");
const limit = flags.getNumber("api-rate-limit", { key: userId }, 100);
const model = flags.getJson<{ name: string }>("model-config", { key: userId }, { name: "small" });

const { value, reason, trace } = flags.evaluateBoolean("checkout-v2", { key: userId }, false);
// reason: TARGETING_MATCH | SPLIT | NO_MATCH | CONFLICT | NOT_FOUND | TYPE_MISMATCH
```

The fallback is returned only when the flag can't be read (not loaded yet, missing, other type). If the flag exists and no rule matches, you get its configured default.

## Options

```ts
await flags.init({
  baseUrl: "https://aven.example.com", // default AVEN_URL (KEEL_URL still works)
  token: "aven_…",                      // default AVEN_TOKEN, then AVEN_API_TOKEN (KEEL_* still works)
  authType: "project-token",            // or "account-token" (needs scope.projectId / AVEN_PROJECT_ID)
  scope: { projectId: "prj_…", environmentId: "env_…" }, // environment default: AVEN_ENVIRONMENT_ID
  pollIntervalMs: 30_000,               // 0 disables polling
  onError: (err) => console.warn(err),  // failed background refresh; the last snapshot keeps serving
});
flags.close(); // stop polling
```

Use `createFlagClient(options)` for more than one client. Expression syntax and evaluation rules: `docs/api-v1.md`, "Feature flags".

## Development

```sh
node --test          # Node ≥ 22.18 runs the .ts tests directly
npx tsc -p .         # build dist/
```
