// i18n 검사: node scripts/i18n-check.mjs [--strict] [파일...]
// 1) t()/tr() 밖에 남은 한국어 문자열(문자열·템플릿 리터럴, JSX 텍스트)
// 2) 영어 사전(lib/i18n/en/*.ts)에 없는 t()/tr() 키
// 3) 원문과 번역의 {변수} 불일치, 같은 원문에 다른 번역
// 2)·3)이 있으면 실패한다. 1)은 검색 키워드처럼 일부러 남긴 것이 있어 보고만 하고, --strict일 때만 실패한다.
// 파일을 주면 그 파일만, 없으면 app/components/lib 전체(lib/mock, lib/i18n 제외)를 본다. 파서는 next에 들어 있는 Babel을 쓴다.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..");
const { parse } = createRequire(path.join(root, "package.json"))("next/dist/compiled/babel/parser");
const HANGUL = /[가-힣]/;
const parseFile = (file, text) => parse(text, { sourceType: "module", plugins: ["jsx", "typescript"], errorRecovery: true, sourceFilename: file });

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (/\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

// node를 돌며 cb(node, parent)를 부른다. cb가 false를 돌려주면 그 아래로 내려가지 않는다.
function traverse(node, cb, parent = null) {
  if (!node || typeof node.type !== "string") return;
  if (cb(node, parent) === false) return;
  for (const k of Object.keys(node)) {
    if (k === "loc" || k === "leadingComments" || k === "trailingComments" || k === "innerComments" || k === "extra") continue;
    const v = node[k];
    if (Array.isArray(v)) v.forEach((c) => traverse(c, cb, node));
    else if (v && typeof v.type === "string") traverse(v, cb, node);
  }
}

const keyName = (k) => (k.type === "StringLiteral" ? k.value : k.type === "Identifier" ? k.name : null);
const dictDir = path.join(root, "lib/i18n/en");
const dict = new Map();
const conflicts = [];
for (const f of fs.readdirSync(dictDir).filter((n) => n.endsWith(".ts") && n !== "index.ts")) {
  traverse(parseFile(f, fs.readFileSync(path.join(dictDir, f), "utf8")).program, (n) => {
    if (n.type === "ObjectProperty" && n.value.type === "StringLiteral") {
      const k = keyName(n.key);
      if (k === null) return;
      const prev = dict.get(k);
      if (prev && prev.value !== n.value.value) conflicts.push(`${JSON.stringify(k)}: ${JSON.stringify(prev.value)} (${prev.file}) vs ${JSON.stringify(n.value.value)} (${f})`);
      dict.set(k, { value: n.value.value, file: f });
    }
  });
}

const strict = process.argv.includes("--strict");
const args = process.argv.slice(2).filter((a) => a !== "--strict");
const sep = path.sep;
const files = (args.length ? args.map((a) => path.resolve(a)) : ["app", "components", "lib"].flatMap((d) => walk(path.join(root, d))))
  .filter((f) => !f.includes(`${sep}lib${sep}mock${sep}`) && !f.includes(`${sep}lib${sep}i18n`));

const vars = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");
let leftover = 0, missing = 0, mismatch = 0;
for (const file of files) {
  const text = fs.readFileSync(file, "utf8");
  if (!HANGUL.test(text)) continue;
  const rel = path.relative(root, file);
  const at = (n) => `${rel}:${n.loc.start.line}`;
  const visit = (n, parent) => {
    if (n.type === "ImportDeclaration" || n.type.startsWith("TS")) return false;
    if (n.type === "CallExpression" && n.callee.type === "Identifier" && (n.callee.name === "t" || n.callee.name === "tr") && n.arguments[0]?.type === "StringLiteral") {
      const a = n.arguments[0];
      const e = dict.get(a.value);
      if (!e) { missing++; console.log(`missing  ${at(a)}  ${JSON.stringify(a.value)}`); }
      else if (vars(a.value) !== vars(e.value)) { mismatch++; console.log(`vars     ${at(a)}  ${JSON.stringify(a.value)} -> ${JSON.stringify(e.value)}`); }
      n.arguments.slice(1).forEach((c) => traverse(c, visit, n));
      return false;
    }
    if (n.type === "StringLiteral" || n.type === "JSXText") {
      if (HANGUL.test(n.value) && !(parent && parent.type === "ObjectProperty" && parent.key === n)) {
        leftover++;
        console.log(`korean   ${at(n)}  ${n.value.trim().replace(/\s+/g, " ").slice(0, 90)}`);
      }
      return false;
    }
    if (n.type === "TemplateLiteral" && n.quasis.some((q) => HANGUL.test(q.value.raw))) {
      leftover++;
      console.log(`korean   ${at(n)}  \`${n.quasis.map((q) => q.value.raw).join("${…}").slice(0, 90)}\``);
      n.expressions.forEach((e) => traverse(e, visit, n));
      return false;
    }
    return true;
  };
  traverse(parseFile(rel, text).program, visit);
}
for (const c of conflicts) console.log(`conflict ${c}`);
console.log(`\n${files.length} files: ${leftover} untranslated Korean, ${missing} keys missing from lib/i18n/en, ${mismatch} placeholder mismatches, ${conflicts.length} conflicting translations`);
process.exit(missing + mismatch + conflicts.length || (strict && leftover) ? 1 : 0);
