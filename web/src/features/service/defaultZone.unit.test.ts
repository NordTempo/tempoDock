// The hostname "From Cloudflare" starts on in the add-domain dialog.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { defaultName, defaultZone, takenBy } from "./defaultZone.ts";

const zones = ["detoo.no", "enhet14.no", "klipit.no", "klipit.se", "saile.dev"];
const ref = (project: string, service = "web", environment = "staging") => ({
  project,
  environment,
  service,
});
const domain = (hostname: string, environment: string, service: string) => ({
  hostname,
  environment,
  service,
  generated: false,
});

test("a zone named after the project comes before the alphabet's first", () => {
  assert.equal(defaultZone(zones, ref("klipit"), []), "klipit.no");
});

test("a zone the project already uses wins, the service's own first", () => {
  const production = domain("klipit.se", "production", "web");
  assert.equal(defaultZone(zones, ref("klipit"), [production]), "klipit.se");
  // Another service's zone counts, but less than this service's.
  const api = domain("api.saile.dev", "staging", "api");
  assert.equal(defaultZone(zones, ref("shop"), [api]), "saile.dev");
  assert.equal(
    defaultZone(zones, ref("shop"), [
      api,
      domain("enhet14.no", "production", "web"),
    ]),
    "enhet14.no",
  );
  // A generated hostname's zone is the platform's choice, not the project's.
  assert.equal(
    defaultZone(zones, ref("klipit"), [
      { ...domain("web-x1.saile.dev", "staging", "web"), generated: true },
    ]),
    "klipit.no",
  );
});

test("the service's name counts after the project's", () => {
  assert.equal(defaultZone(zones, ref("shop", "enhet14"), []), "enhet14.no");
  assert.equal(defaultZone(zones, ref("klipit", "enhet14"), []), "klipit.no");
});

test("a shared part of a name counts from 3 characters", () => {
  assert.equal(defaultZone(zones, ref("saile-dev"), []), "saile.dev");
  assert.equal(
    defaultZone(["abc.no", "detoo.no"], ref("detoo-web"), []),
    "detoo.no",
  );
  // "no" is in enhet14.no's name only by its suffix, and "kl" is too short.
  assert.equal(defaultZone(zones, ref("no", "kl"), []), "detoo.no");
  assert.equal(defaultZone(["aa.no", "ab.no"], ref("ab-shop"), []), "aa.no");
});

test("nothing in common leaves the first zone by name", () => {
  assert.equal(defaultZone(zones, ref("shop"), []), "detoo.no");
  assert.equal(defaultZone([], ref("shop"), []), "");
});

test("the name starts as the environment's, except in production", () => {
  assert.equal(
    defaultName({ name: "staging", kind: "non_production" }),
    "staging",
  );
  // Production's hostname is the zone itself, whatever it is called.
  assert.equal(defaultName({ name: "live", kind: "production" }), "");
  assert.equal(defaultName(undefined), "");
});

test("a hostname the project already has says whose it is", () => {
  const domains = [domain("klipit.no", "production", "web")];
  assert.equal(
    takenBy("Klipit.no.", domains),
    "klipit.no is already a domain of web (production).",
  );
  assert.equal(takenBy("staging.klipit.no", domains), undefined);
  assert.equal(takenBy("", domains), undefined);
});
