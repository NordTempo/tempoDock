// Open alerts in words: the Overview's line and the Inbox's grouped rows.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { components } from "../../api/schema";
import { groupAlerts, groupTitle, needsPhrase } from "./summary.ts";

type Alert = components["schemas"]["Alert"];

let id = 0;
const alert = (kind: string, more: Partial<Alert> = {}): Alert => ({
  id: ++id,
  kind,
  title: `${kind} ${id}`,
  severity: "warning",
  count: 1,
  opened_at: "2026-11-24T10:00:00Z",
  updated_at: "2026-11-24T10:00:00Z",
  ...more,
});

const siteError = () =>
  alert("new_error", { project: "shop", target: { site: "shop.example.com" } });

test("the line names each kind once, counting the errors", () => {
  const open = [
    alert("access_request"),
    alert("backup_failed"),
    siteError(),
    siteError(),
    siteError(),
  ];
  assert.equal(
    needsPhrase(open),
    "an access request, a failed backup and 3 new errors",
  );
  assert.equal(needsPhrase([alert("backup_failed")]), "a failed backup");
  assert.equal(needsPhrase([]), "");
});

test("errors about one site are one row; other alerts keep their own", () => {
  const groups = groupAlerts([
    siteError(),
    alert("backup_failed"),
    siteError(),
    alert("backup_failed"),
  ]);
  const [errors, backup] = groups;
  assert.ok(errors && backup);
  assert.equal(groups.length, 3);
  assert.equal(errors.alerts.length, 2);
  assert.equal(groupTitle(errors), "2 new errors on shop.example.com");
  assert.equal(backup.alerts.length, 1);
});

test("a critical alert leads its group", () => {
  const critical = alert("error_spike", {
    severity: "critical",
    project: "shop",
    target: { site: "shop.example.com" },
  });
  const [group] = groupAlerts([siteError(), critical]);
  assert.ok(group);
  assert.equal(group.lead.id, critical.id);
  assert.equal(groupTitle(group), "2 errors on shop.example.com");
});
