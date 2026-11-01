// Mirrors internal/dotenv/dotenv_test.go so the dashboard and the server read .env text the same way.
import test from "node:test";
import assert from "node:assert/strict";
import { DotenvError, marshalEnv, parseEnv, parseEnvLoose, parseRaw, quoteEnv, renderRaw, diffVars } from "@/lib/dotenv";

test("parse: same rules as the server", () => {
  const src =
    "\uFEFF# header comment\r\n" +
    "\n" +
    "PLAIN=value\n" +
    "  SPACED  =   padded value   \n" +
    "export EXPORTED=yes\n" +
    "export\tTABBED=tab\n" +
    "export=notprefix\n" +
    "EMPTY=\n" +
    "EMPTY2=   # only a comment\n" +
    "INLINE=abc # trailing comment\n" +
    "HASH=abc#def\n" +
    "LEADHASH=#notcomment\n" +
    "SINGLE='it is ${{ literal }} \\n'\n" +
    'DOUBLE="tab\\tnl\\nquote\\" bs\\\\ dollar\\$ unknown\\q"  # c\n' +
    'MULTI="line1\nline2\n"\n' +
    "MULTISINGLE='a\nb'\n" +
    "BACK=`x 'y' \"z\"`\n" +
    "REF=${{ postgres.DATABASE_URL }}\n" +
    "dotted.key-1=ok\n" +
    "DUP=1\n" +
    "DUP=2\n" +
    "EQ=a=b=c\n";
  assert.deepEqual(parseEnv(src), {
    PLAIN: "value",
    SPACED: "padded value",
    EXPORTED: "yes",
    TABBED: "tab",
    export: "notprefix",
    EMPTY: "",
    EMPTY2: "",
    INLINE: "abc",
    HASH: "abc#def",
    LEADHASH: "#notcomment",
    SINGLE: "it is ${{ literal }} \\n",
    DOUBLE: 'tab\tnl\nquote" bs\\ dollar$ unknown\\q',
    MULTI: "line1\nline2\n",
    MULTISINGLE: "a\nb",
    BACK: `x 'y' "z"`,
    REF: "${{ postgres.DATABASE_URL }}",
    "dotted.key-1": "ok",
    DUP: "2",
    EQ: "a=b=c",
  });
});

test("parse errors carry the line of the entry", () => {
  for (const [src, line] of [
    ["A=1\nNOEQUALS\n", 2],
    ["1BAD=x", 1],
    ['A="open\nstill', 1],
    ["A='x' trailing", 1],
    ['\n\nB="x"y', 3],
    ["BAD KEY=1", 1],
    ["=value", 1],
  ]) {
    assert.throws(
      () => parseEnv(src),
      (e) => e instanceof DotenvError && e.line === line,
      JSON.stringify(src),
    );
  }
  assert.throws(
    () => parseEnv('A="x\ny\nz"\nB=ok\nC'),
    (e) => e instanceof DotenvError && e.line === 5,
  );
});

test("loose parse skips broken entries and keeps going", () => {
  const { pairs, errors } = parseEnvLoose(
    [
      "# pasted from a README",
      "Run these:",
      "export API_KEY='abc def' # comment",
      "BAD KEY=1",
      'PEM="-----BEGIN-----',
      "MIIB",
      '-----END-----"',
      "PORT=3000",
      "URL=postgres://u:p@h/db?sslmode=disable",
      "",
    ].join("\n"),
  );
  assert.deepEqual(pairs, [
    ["API_KEY", "abc def"],
    ["PEM", "-----BEGIN-----\nMIIB\n-----END-----"],
    ["PORT", "3000"],
    ["URL", "postgres://u:p@h/db?sslmode=disable"],
  ]);
  assert.deepEqual(
    errors.map((e) => e.line),
    [2, 4],
  );
  // A URL pasted alone is not a variable.
  assert.deepEqual(parseEnvLoose("postgres://u:p@h/db?sslmode=disable").pairs, []);
});

test("loose parse recovers after an unterminated quote", () => {
  const { pairs, errors } = parseEnvLoose('A="never closed\nB=2\n');
  assert.equal(errors.length, 1);
  assert.equal(errors[0].line, 1);
  assert.deepEqual(pairs, [["B", "2"]]);
});

test("marshal → parse round trip", () => {
  const values = [
    "", "plain", "with space", " leading", "trailing ", "a#b", " #x", "#start",
    "quote'single", 'quote"double', "both'and\"", "back`tick", "back\\slash", "\\n literal",
    "line1\nline2", "crlf\r\nend", "\n", "tab\there", "${{ postgres.DATABASE_URL }}", "$HOME ${VAR}",
    "유니코드 값", "=", "a=b", "'", '"', "\\", "trailing\\", "mixed ' \" \\ $ ` \n end",
    "postgres://u:p@h:5432/db?sslmode=disable", "-----BEGIN KEY-----\nabc\n-----END KEY-----\n",
  ];
  const m = Object.fromEntries(values.map((v, i) => [`K${i}`, v]));
  const text = marshalEnv(m);
  assert.deepEqual(parseEnv(text), m);
  assert.equal(marshalEnv(parseEnv(text)), text);
});

test("quote and marshal format", () => {
  assert.equal(marshalEnv({ B: "2", A: "1 2", C: "" }), "A='1 2'\nB=2\nC=\n");
  assert.equal(quoteEnv("it's"), `"it's"`);
  assert.equal(quoteEnv("a\nb"), '"a\\nb"');
});

test("raw editor: sealed variables become comments and stay out of the parse", () => {
  const text = renderRaw("env", { A: "1", S: "<sealed>" }, ["S"], "note line 1\nnote line 2");
  assert.equal(text, "# note line 1\n# note line 2\n# S=<sealed>\n\nA=1\n");
  assert.deepEqual(parseRaw("env", text), { A: "1" });
  assert.deepEqual(parseRaw("json", renderRaw("json", { B: "x", A: "y" }, [])), { A: "y", B: "x" });
  assert.throws(() => parseRaw("json", "[1]"));
  assert.throws(() => parseRaw("json", '{"A": 1}'));
  assert.deepEqual(parseRaw("json", "  "), {});
});

test("diffVars lists added, changed and removed keys; kept keys are not removed", () => {
  const d = diffVars({ A: "1", B: "2", S: "<sealed>", R: "x" }, { A: "1", B: "3", C: "4" }, new Set(["S"]));
  assert.deepEqual(d, { added: ["C"], changed: ["B"], removed: ["R"] });
  // Writing the sealed marker back is not a change.
  assert.deepEqual(diffVars({ S: "<sealed>" }, { S: "<sealed>" }, new Set(["S"])), { added: [], changed: [], removed: [] });
});
