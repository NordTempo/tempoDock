// The words of checked backups (ADR 0096).
// Run by `pnpm check:unit` (node --test): pure functions, no browser.
import assert from "node:assert/strict";
import { test } from "node:test";
import { checkedParts, checkOf, repositoryChecked } from "./checked.ts";

const words = { ago: () => "3d ago", at: () => "Sunday 03:00" };
const base = {
  id: 1,
  kind: "repository",
  repository: "local",
  server: "node-1",
  trigger: "schedule",
  started_at: "2026-11-15T03:00:00Z",
} as const;
const labels = (...args: Parameters<typeof checkedParts>) =>
  checkedParts(...args).map((p) => p.label);

test("a checked repository and a passed drill, then the next check", () => {
  assert.deepEqual(
    labels(
      {
        check: { ...base, status: "succeeded" },
        drill: {
          ...base,
          kind: "drill",
          status: "succeeded",
          detail: "42 tables in 18 s",
        },
        next_check_at: "2026-11-22T03:00:00Z",
      },
      words,
    ),
    [
      "Checked 3d ago",
      "Restore drill passed 3d ago, 42 tables in 18 s",
      "Next Sunday 03:00",
    ],
  );
});

test("a failed check carries its error and the command to run", () => {
  const [part] = checkedParts(
    { check: { ...base, status: "failed", error: "pack 3f9a: bad hash" } },
    words,
  );
  assert.equal(part?.label, "Check failed 3d ago: pack 3f9a: bad hash");
  assert.equal(part?.tone, "fail");
  assert.equal(part?.hint, "dock backup check");
});

test("running, skipped and failed drills", () => {
  const drill = { ...base, kind: "drill" } as const;
  assert.deepEqual(
    labels(
      {
        check: { ...base, status: "running" },
        drill: { ...drill, status: "running" },
      },
      words,
    ),
    ["Checking now", "Restore drill running"],
  );
  assert.deepEqual(
    labels(
      { drill: { ...drill, status: "skipped", detail: "snapshot is 3.4 GB" } },
      words,
    ),
    ["Restore drill skipped: snapshot is 3.4 GB", "Weekly checks are off"],
  );
  assert.deepEqual(
    labels(
      { drill: { ...drill, status: "failed", error: "no tables" } },
      words,
    ).slice(0, 1),
    ["Restore drill failed 3d ago: no tables"],
  );
});

test("nothing checked yet: the first check, or nothing at all", () => {
  assert.deepEqual(labels({ next_check_at: "2026-11-22T03:00:00Z" }, words), [
    "First check Sunday 03:00",
  ]);
  assert.deepEqual(labels({}, words), []);
});

test("a repositories row finds its check: local by server, a bucket by name", () => {
  const local = { ...base, status: "succeeded" } as const;
  const bucket = { ...local, id: 2, repository: "s3:backups/dock" } as const;
  const checks = [local, bucket];
  assert.equal(
    checkOf(checks, { repository: "local", server: "node-1" }),
    local,
  );
  assert.equal(
    checkOf(checks, { repository: "local", server: "node-2" }),
    undefined,
  );
  assert.equal(checkOf(checks, { repository: "s3:backups/dock" }), bucket);
  assert.equal(repositoryChecked(local, undefined, words)?.label, "3d ago");
  assert.equal(
    repositoryChecked({ ...local, status: "failed" }, undefined, words)?.label,
    "failed 3d ago",
  );
  assert.equal(
    repositoryChecked(undefined, "2026-11-22T03:00:00Z", words)?.label,
    "first check Sunday 03:00",
  );
  assert.equal(repositoryChecked(undefined, undefined, words), undefined);
});
