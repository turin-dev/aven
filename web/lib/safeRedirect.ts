// Post-login redirect targets come from the URL (?next=…), so anyone can craft them.
// Only same-origin paths are allowed; everything else falls back to the home page.
// Kept free of imports so scripts/safe-redirect.test.mjs can run it with plain node.

/** Paths that would bounce straight back to the sign-in flow. */
const LOOP = /^\/(?:login|invite)(?:\/|$)/;

/**
 * Returns a same-origin path (path, query and fragment) for `raw`, or `fallback` when `raw`
 * could leave the origin or loop back to the login page.
 *
 * Browsers strip ASCII tab and newline anywhere in a URL and treat `\` like `/`, so a string
 * such as "/\t/evil.example" or "/\\evil.example" turns into the protocol-relative
 * "//evil.example". Those characters are rejected before parsing, and the parsed path is
 * checked again because dot segments ("/.//evil.example") can also normalise to "//".
 */
export function safeNextPath(raw: string | null | undefined, origin: string, fallback = "/"): string {
  if (!raw || raw.length > 2048) return fallback;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0020\u007f\\]/.test(raw)) return fallback;
  if (!raw.startsWith("/") || raw.startsWith("//")) return fallback;
  let base: URL;
  let url: URL;
  try {
    base = new URL(origin);
    url = new URL(raw, base);
  } catch {
    return fallback;
  }
  if (url.origin !== base.origin || url.username || url.password) return fallback;
  if (url.pathname.startsWith("//") || LOOP.test(url.pathname)) return fallback;
  return url.pathname + url.search + url.hash;
}
