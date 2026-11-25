// How the logs table splits a line into level, message and fields.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { parseLog } from "./logParse.ts";

test("logfmt: level and msg come out, quoted values unescape", () => {
  assert.deepEqual(
    parseLog(
      'time=2026-11-25T17:04:30Z level=WARN msg="set up data" err="can\'t reach \\"db\\""',
    ),
    {
      level: "warn",
      msg: "set up data",
      fields: [
        ["time", "2026-11-25T17:04:30Z"],
        ["err", 'can\'t reach "db"'],
      ],
    },
  );
});

test("JSON: pino's numeric level is named, nested values stay JSON", () => {
  assert.deepEqual(
    parseLog('{"level":50,"msg":"boom","req":{"id":7},"ok":false}'),
    {
      level: "error",
      msg: "boom",
      fields: [
        ["req", '{"id":7}'],
        ["ok", "false"],
      ],
    },
  );
});

test("plain text stays whole, even with a key=value in it", () => {
  for (const text of [
    "GET /api/cart 200 18ms",
    "ready port=80",
    "a=1 b",
    'a=1 b="unterminated',
    "{not json",
    "[1,2]",
  ]) {
    assert.deepEqual(parseLog(text), { level: null, msg: text, fields: [] });
  }
});
