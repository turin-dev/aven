// Evaluates a Aven IaC module and writes its definition as JSON (used by `aven config`).
// usage: <bun|node> runner.mjs <aven.ts> <out.json>   (context in KEEL_IAC_CONTEXT)
import { writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const [file, out] = process.argv.slice(2);
const data = JSON.parse(process.env.AVEN_IAC_CONTEXT || process.env.KEEL_IAC_CONTEXT || "{}");
const ctx = {
  ...data,
  environmentName: data.environment,
  isEnvironment: (name) => name === data.environment,
  shared: new Proxy({}, { get: (_, key) => (typeof key === "string" ? `\${{shared.${key}}}` : undefined) }),
};
const mod = await import(pathToFileURL(file).href);
let def = mod.default;
if (typeof def === "function") def = await def(ctx);
if (!def || typeof def !== "object" || !Array.isArray(def.services)) {
  throw new Error(`${file} must \`export default defineAven((ctx) => project(...))\` (defineKeel is still accepted)`);
}
const result = { ...def };
if (typeof mod.partial === "string" && mod.partial !== "") result.partial = mod.partial;
writeFileSync(out, JSON.stringify(result));
