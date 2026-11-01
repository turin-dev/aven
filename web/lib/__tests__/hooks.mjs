// Module resolution for the pure-function tests: maps the `@/` alias to web/, adds the `.ts` extension that the app's
// imports leave out, and swaps `@/lib/i18n` for a tiny stub (the real module is a React .tsx file).
const root = new URL("../../", import.meta.url);
const stub = new URL("./i18n-stub.mjs", import.meta.url).href;

export async function resolve(spec, ctx, next) {
  if (spec === "@/lib/i18n") return { url: stub, shortCircuit: true };
  if (spec.startsWith("@/")) spec = new URL(spec.slice(2), root).href;
  if ((spec.startsWith(".") || spec.startsWith("file:")) && !/\.[cm]?[jt]sx?$/.test(spec)) {
    try {
      return await next(`${spec}.ts`, ctx);
    } catch {
      /* fall through */
    }
  }
  return next(spec, ctx);
}

// The app's .ts files are ES modules; say so instead of letting Node guess (and warn).
export async function load(url, ctx, next) {
  if (url.endsWith(".ts")) return next(url, { ...ctx, format: "module-typescript" });
  return next(url, ctx);
}
