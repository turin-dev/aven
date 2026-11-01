// Run: node --test lib/dataSql.test.mjs  (Node 22.6+ strips the TypeScript types of the imported file.)
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  asJson,
  buildSelect,
  compareCells,
  dangerOf,
  maskSecrets,
  MASK,
  pushHistory,
  quoteIdent,
  quoteLiteral,
  rowMatches,
  sortRows,
  tableRef,
  toCsv,
} from "./dataSql.ts";

test("quoteIdent doubles the quote character", () => {
  assert.equal(quoteIdent("postgres", 'we"ird'), '"we""ird"');
  assert.equal(quoteIdent("mysql", "we`ird"), "`we``ird`");
  assert.equal(quoteIdent("mysql", 'a"b'), "`a\"b`");
});

test("quoteLiteral escapes quotes and MySQL backslashes", () => {
  assert.equal(quoteLiteral("postgres", "it's"), "'it''s'");
  assert.equal(quoteLiteral("postgres", "a\\b"), "'a\\b'");
  assert.equal(quoteLiteral("mysql", "a\\' OR 1=1 -- "), "'a\\\\'' OR 1=1 -- '");
});

test("tableRef drops public and empty schemas", () => {
  assert.equal(tableRef("postgres", "public", "users"), '"users"');
  assert.equal(tableRef("postgres", "audit", "log"), '"audit"."log"');
  assert.equal(tableRef("mysql", "app", "users"), "`app`.`users`");
  assert.equal(tableRef("mysql", "", "users"), "`users`");
});

test("buildSelect quotes everything and escapes LIKE wildcards", () => {
  assert.equal(
    buildSelect("postgres", { schema: "public", name: "users", sort: { column: "created at", desc: true }, filter: { columns: ["email"], value: "50%_o'k" }, limit: 50, offset: 100 }),
    `SELECT * FROM "users" WHERE "email"::text ILIKE '%50\\%\\_o''k%' ORDER BY "created at" DESC LIMIT 50 OFFSET 100;`,
  );
  assert.equal(
    buildSelect("mysql", { schema: "app", name: "t", filter: { columns: ["c", "d"], value: "x" }, limit: 10 }),
    "SELECT * FROM `app`.`t` WHERE (CAST(`c` AS CHAR) LIKE '%x%' OR CAST(`d` AS CHAR) LIKE '%x%') LIMIT 10;",
  );
  assert.equal(buildSelect("postgres", { schema: "public", name: "t", filter: { columns: ["c"], value: " " }, limit: 5 }), 'SELECT * FROM "t" LIMIT 5;');
});

test("compareCells and sortRows: numbers numerically, NULLs last in both directions, stable", () => {
  assert.ok(compareCells("9", "10") < 0);
  assert.ok(compareCells("b", "A") > 0);
  const rows = [["10"], [null], ["9"], ["x"], ["9"]];
  assert.deepEqual(sortRows(rows, 0, false), [2, 4, 0, 3, 1]);
  assert.deepEqual(sortRows(rows, 0, true), [3, 0, 2, 4, 1]);
});

test("rowMatches searches all cells or one column; NULL matches the word null", () => {
  assert.ok(rowMatches(["Alice", "a@x.io"], "X.IO"));
  assert.ok(!rowMatches(["Alice", "a@x.io"], "x.io", 0));
  assert.ok(rowMatches([null], "null"));
  assert.ok(rowMatches([{ a: 1 }], '"a"'));
  assert.ok(rowMatches(["z"], "  "));
});

test("asJson only parses objects and arrays", () => {
  assert.deepEqual(asJson(' {"a":1} '), { a: 1 });
  assert.equal(asJson("42"), undefined);
  assert.equal(asJson("{not json}"), undefined);
});

test("maskSecrets hides URL passwords and known secrets", () => {
  assert.equal(maskSecrets("postgresql://postgres:s3cr3t@db.internal:5432/app"), `postgresql://postgres:${MASK}@db.internal:5432/app`);
  assert.equal(maskSecrets('psql "postgresql://u:p@ss@h/db"'), `psql "postgresql://u:${MASK}@h/db"`);
  assert.equal(maskSecrets("redis://default:abcdef@h:6379", ["abcdef"]), `redis://default:${MASK}@h:6379`);
  assert.equal(maskSecrets("host=h password=abcdef", ["abcdef"]), `host=h password=${MASK}`);
  assert.equal(maskSecrets("https://example.com/path"), "https://example.com/path");
});

test("dangerOf flags writes, ignores keywords inside strings and comments", () => {
  assert.equal(dangerOf("postgres", "SELECT 'drop table x' -- delete from y"), null);
  assert.equal(dangerOf("postgres", "DROP TABLE users")?.reason, "drop");
  assert.equal(dangerOf("postgres", "delete from users")?.reason, "delete-all");
  assert.equal(dangerOf("postgres", "DELETE FROM users WHERE id = 1")?.level, "warn");
  assert.equal(dangerOf("mysql", "update t set a=1; truncate t2")?.reason, "update-all");
  assert.equal(dangerOf("postgres", "select 1; update t set a = 1 where id = 2")?.reason, "update");
  assert.equal(dangerOf("redis", "GET a\nflushall")?.reason, "flush");
  assert.deepEqual(dangerOf("redis", "DEL a"), { level: "warn", reason: "delete" });
  assert.equal(dangerOf("mongo", "db.users.drop()")?.reason, "mongo-drop");
  assert.equal(dangerOf("mongo", "db.users.find().toArray()"), null);
});

test("toCsv quotes fields that need it", () => {
  assert.equal(toCsv(["a", "b"], [['x,y', null], ['say "hi"', 2]]), 'a,b\r\n"x,y",\r\n"say ""hi""",2\r\n');
});

test("pushHistory de-duplicates and bounds", () => {
  assert.deepEqual(pushHistory(["b", "a"], " a "), ["a", "b"]);
  assert.deepEqual(pushHistory(["a"], "   "), ["a"]);
  assert.equal(pushHistory(Array.from({ length: 40 }, (_, i) => String(i)), "x").length, 30);
});
