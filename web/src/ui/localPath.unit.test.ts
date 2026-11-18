// Where the sign-in pages (Dock's and tempoBase's) send the browser on to.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { localPath } from "./localPath.ts";

test("a path on this origin is kept as it is", () => {
  for (const to of [
    "/",
    "/acme/shop",
    "/c/main?table=users#row",
    "/invite/abc?next=https://example.com",
  ]) {
    assert.equal(localPath(to), to);
  }
});

test("anything a browser could read as another site is refused", () => {
  for (const to of [
    "",
    "acme/shop",
    "https://example.com/",
    "javascript:alert(1)",
    "//example.com",
    "/\\example.com",
    "/\\/example.com",
    "\\/example.com",
    "/\t/example.com",
    "/\n/example.com",
    "/\r/example.com",
    "/acme\u0000",
    "/acme\u007f",
    " /acme",
    undefined,
    null,
    1,
    ["/acme"],
  ]) {
    assert.equal(localPath(to), undefined, JSON.stringify(to));
  }
});
