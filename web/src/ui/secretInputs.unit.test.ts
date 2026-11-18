import assert from "node:assert/strict";
import { globSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// The web directory, whichever directory the tests run from.
const web = join(dirname(fileURLToPath(import.meta.url)), "../..");

// A password input says what the browser may do with it: "new-password"
// for a secret typed into a form (a key, a token, a value),
// "current-password" or "one-time-code" only where somebody signs in.
// Without the attribute, or with "off" (which browsers ignore on password
// inputs), a browser or password manager may fill in a saved password or
// offer to save the secret.
test("every password input sets autoComplete, and not to off", () => {
  const missing: string[] = [];
  const files = globSync(["src/**/*.tsx", "tempobase/src/**/*.tsx"], {
    cwd: web,
  });
  assert.ok(files.length > 100, "the scan found the UI's files");
  for (const file of files) {
    const source = readFileSync(join(web, file), "utf8");
    for (const m of source.matchAll(
      /type="password"|type=\{[^}]*"password"/g,
    )) {
      // The element the attribute is on: from its "<" to its "/>".
      const tag = source.slice(
        source.lastIndexOf("<", m.index),
        source.indexOf("/>", m.index),
      );
      if (!tag.includes("autoComplete") || tag.includes('autoComplete="off"')) {
        const line = source.slice(0, m.index).split("\n").length;
        missing.push(`${relative(web, join(web, file))}:${line}`);
      }
    }
  }
  assert.deepEqual(missing, []);
});
