// Which of a person's tokens the agent picker offers.
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { liveAgents } from "./agents.ts";

const now = Date.parse("2026-11-17T12:00:00Z");
const login = "cli_login" as const;

test("revoked and expired logins and manual tokens are no agents", () => {
  const tokens = [
    { id: 1, origin: login, revoked_at: "2026-11-07T10:00:00Z" },
    { id: 2, origin: login, expires_at: "2026-11-01T10:00:00Z" },
    { id: 3, origin: "manual" as const, expires_at: "2027-02-11T10:00:00Z" },
    { id: 4, origin: login, expires_at: "2027-02-11T10:00:00Z" },
    { id: 5, origin: login },
  ];
  assert.deepEqual(
    liveAgents(tokens, now).map((t) => t.id),
    [4, 5],
  );
});
