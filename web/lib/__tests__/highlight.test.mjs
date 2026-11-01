import test from "node:test";
import assert from "node:assert/strict";
import { highlightEnv, highlightJson, TOK } from "@/components/panel/variables/highlight";

const join = (lines) => lines.map((l) => l.map((t) => t.text).join("")).join("\n");
const classOf = (line, text) => line.find((t) => t.text === text)?.cls;

test("highlighting never changes the text", () => {
  const env = '# c\nexport A=1 # x\nB="multi\nline ${{ X }}\nend" # tail\nC=\'s\'\n\nD=${{ pg.URL }}\nnot a var\n';
  assert.equal(join(highlightEnv(env)), env);
  const json = '{\n  "A": "x ${{ y }}",\n  "B\\"q": "z"\n}\n';
  assert.equal(join(highlightJson(json)), json);
});

test("env tokens", () => {
  const l = highlightEnv('A="x\ny" # c\nB=${{ pg.URL }} # note\n# only');
  assert.equal(classOf(l[0], "A"), TOK.key);
  assert.equal(classOf(l[1], 'y"'), TOK.string); // continuation of the quoted value, not a key
  assert.equal(classOf(l[1], "# c"), TOK.comment);
  assert.equal(classOf(l[2], "${{ pg.URL }}"), TOK.ref);
  assert.equal(classOf(l[2], " # note"), TOK.comment);
  assert.equal(classOf(l[3], "# only"), TOK.comment);
});
