// The addresses the previews' pull request links open.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { httpsHref } from "./httpsHref.ts";

test("an https address is a link", () => {
  const url = "https://github.com/acme/shop/pull/7";
  assert.equal(httpsHref(url), url);
});

test("anything else is not", () => {
  for (const url of [
    undefined,
    "",
    "http://github.com/acme/shop/pull/7",
    "javascript:alert(1)",
    " javascript:alert(1)",
    "data:text/html,hello",
    "//github.com/acme/shop/pull/7",
    "/acme/shop",
    "github.com/acme/shop/pull/7",
  ]) {
    assert.equal(httpsHref(url), undefined, JSON.stringify(url));
  }
});
