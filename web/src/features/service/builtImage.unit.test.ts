// Which images lists leave out as Dock's own build tags.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { dockBuilt } from "./builtImage.ts";

test("Dock's build tags, with or without the registry, are Dock built", () => {
  assert.equal(dockBuilt("dock/p12_e31_s34:31"), true);
  assert.equal(dockBuilt("10.0.0.2:5000/dock/p1_e2_s3:4"), true);
});

test("images from elsewhere are not", () => {
  assert.equal(dockBuilt("ghcr.io/acme/api:1.4"), false);
  assert.equal(dockBuilt("redis@sha256:615b4ce0a1"), false);
  assert.equal(dockBuilt("acme/dock:1"), false);
});
