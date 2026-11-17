// The CSV exports' cells (the data panel's results, Activity).
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { csv, csvField, csvText } from "./csv.ts";

test("a cell a spreadsheet would run is marked as text", () => {
  for (const cell of [
    '=HYPERLINK("https://example.com","open")',
    "+SUM(A1:A9)",
    "-2+3",
    "@SUM(1+1)",
    "\t=1+1",
    "\r=1+1",
    "-1 day",
    "+47 912 34 567",
  ]) {
    assert.equal(csvText(cell), `'${cell}`, JSON.stringify(cell));
  }
});

test("plain numbers and ordinary text stay as they are", () => {
  for (const cell of [
    "-12.5",
    "+4712345678",
    "-0",
    ".5",
    "-1e-9",
    "42",
    "",
    "Ada",
    "a=b",
    "2026-11-17 10:00:00+00",
    "ada@example.com",
  ]) {
    assert.equal(csvText(cell), cell, JSON.stringify(cell));
  }
});

test("a field is quoted when it has to be, after it is marked", () => {
  assert.equal(csvField("plain"), "plain");
  assert.equal(csvField("a,b"), '"a,b"');
  assert.equal(csvField('say "hi"'), '"say ""hi"""');
  assert.equal(csvField("two\nlines"), '"two\nlines"');
  assert.equal(csvField(" padded"), '" padded"');
  assert.equal(csvField('=A1&",x"'), `"'=A1&"",x"""`);
});

test("rows are joined with CRLF, the header first", () => {
  assert.equal(
    csv([
      ["id", "name"],
      ["1", "=cmd"],
      ["-2", "Ada, Countess"],
    ]),
    `id,name\r\n1,'=cmd\r\n-2,"Ada, Countess"`,
  );
});
