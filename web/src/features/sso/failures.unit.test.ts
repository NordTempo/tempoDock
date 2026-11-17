// What the sign-in page says after a failed single sign-on.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { ssoFailure } from "./failures.ts";

test("a reason the page knows is its own sentence, with the provider's label", () => {
  assert.equal(
    ssoFailure("cancelled", "Corp ID"),
    "The sign-in was cancelled at Corp ID.",
  );
  assert.equal(
    ssoFailure("no_code", "Corp ID"),
    "Corp ID sent no sign-in code; start again.",
  );
  assert.equal(ssoFailure("failed"), "Signing in failed; try again.");
});

test("without a provider of this Dock's, the sentence names none", () => {
  assert.equal(
    ssoFailure("no_code"),
    "The identity provider sent no sign-in code; start again.",
  );
  assert.equal(
    ssoFailure("cancelled"),
    "The sign-in was cancelled at the identity provider.",
  );
});

test("anything else in the address shows nothing", () => {
  for (const reason of [
    "Your account is locked. Call 555 0100",
    "",
    "toString",
    "__proto__",
  ]) {
    assert.equal(ssoFailure(reason), undefined, JSON.stringify(reason));
  }
});
