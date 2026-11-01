// Rough password strength estimate for the strength meter (advisory only: the server accepts
// any non-empty password up to 256 bytes). No dictionary download, no dependencies; it catches
// the usual weak patterns (short, common, repeated, sequential, contains the user's name/email).
// Kept free of imports so scripts/password-strength.test.mjs can run it with plain node.

export type StrengthHint = "short" | "common" | "repeat" | "sequence" | "personal" | "variety";
export type Strength = { score: 0 | 1 | 2 | 3 | 4; hints: StrengthHint[] };

const COMMON = new Set([
  "password", "passw0rd", "qwerty", "qwertyuiop", "asdf", "asdfgh", "asdfghjkl", "zxcvbnm", "letmein", "welcome", "admin",
  "administrator", "root", "iloveyou", "monkey", "dragon", "master", "login", "secret", "changeme", "default", "test",
  "abc", "abcd", "abcdef", "football", "baseball", "shadow", "sunshine", "princess", "superman", "trustno", "aven", "keel",
  "railway", "docker", "server",
]);
const KEYBOARD_ROWS = ["qwertyuiop", "asdfghjkl", "zxcvbnm", "1234567890"];

/** Number of characters that continue a run of the same character or a +1/-1 sequence (also keyboard rows). */
function patternChars(s: string): { repeat: number; sequence: number } {
  let repeat = 0;
  let sequence = 0;
  const lower = s.toLowerCase();
  for (let i = 2; i < lower.length; i++) {
    const a = lower.charCodeAt(i - 2), b = lower.charCodeAt(i - 1), c = lower.charCodeAt(i);
    if (a === b && b === c) repeat++;
    else if ((b - a === 1 && c - b === 1) || (b - a === -1 && c - b === -1)) sequence++;
    else if (KEYBOARD_ROWS.some((row) => row.includes(lower.slice(i - 2, i + 1)))) sequence++;
  }
  return { repeat, sequence };
}

function poolSize(s: string): number {
  let pool = 0;
  if (/[a-z]/.test(s)) pool += 26;
  if (/[A-Z]/.test(s)) pool += 26;
  if (/[0-9]/.test(s)) pool += 10;
  if (/[^a-zA-Z0-9\u0080-\uffff]/.test(s)) pool += 33;
  if (/[\u0080-\uffff]/.test(s)) pool += 100; // Hangul and other non-ASCII letters
  return Math.max(pool, 1);
}

/** `personal` holds the user's name and email; parts of 3+ characters found in the password weaken it. */
export function passwordStrength(password: string, personal: string[] = []): Strength {
  const hints: StrengthHint[] = [];
  const length = [...password].length;
  if (!length) return { score: 0, hints };
  const lower = password.toLowerCase();
  const core = lower.replace(/[^a-z]/g, "");
  // "Password123!" is still "password" with decorations.
  const common = COMMON.has(lower) || (core.length >= 3 && core.length >= length / 2 && COMMON.has(core));
  const tokens = personal
    .flatMap((p) => p.toLowerCase().split(/[^\p{L}\p{N}]+/u))
    .filter((p) => p.length >= 3);
  const personalHit = tokens.some((p) => lower.includes(p));
  const { repeat, sequence } = patternChars(password);
  const effective = Math.max(0, length - repeat - sequence * 0.75);
  const bits = effective * Math.log2(poolSize(password));

  let score: Strength["score"] = bits < 28 ? 0 : bits < 40 ? 1 : bits < 60 ? 2 : bits < 80 ? 3 : 4;
  const cap = (max: Strength["score"]) => {
    if (score > max) score = max;
  };
  if (length < 8) (hints.push("short"), cap(1));
  if (common) (hints.push("common"), cap(0));
  if (personalHit) (hints.push("personal"), cap(1));
  if (repeat >= 2) hints.push("repeat");
  if (sequence >= 2) hints.push("sequence");
  if (score < 3 && length >= 8 && poolSize(password) <= 26) hints.push("variety");
  return { score, hints };
}
