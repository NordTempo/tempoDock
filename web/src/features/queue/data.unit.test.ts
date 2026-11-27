import assert from "node:assert/strict";
import { test } from "node:test";
import { type DeployQueue, queueCount, withRelease } from "./queue.ts";

const ref = { project: "shop", environment: "production", service: "web" };
const at = "2026-11-27T10:00:00Z";
const queue: DeployQueue = {
  builders: [
    {
      label: "Builder 1",
      builds: "all",
      building: {
        ...ref,
        release: 4,
        status: "building",
        queued_at: at,
        since: at,
      },
      waiting: [
        { hidden: true, queued_at: at, since: at },
        {
          ...ref,
          service: "api",
          release: 9,
          status: "building",
          queued_at: at,
          since: at,
        },
      ],
    },
  ],
  deploying: [
    {
      ...ref,
      service: "db",
      release: 2,
      status: "queued",
      queued_at: at,
      since: at,
    },
  ],
};

test("the count leaves out others' releases", () => {
  assert.deepEqual(queueCount(queue), { active: 1, waiting: 2 });
  assert.deepEqual(queueCount(undefined), { active: 0, waiting: 0 });
});

test("a release that ends leaves the queue", () => {
  const next = withRelease(queue, ref, { number: 4, status: "active" });
  assert.ok(next && next !== "refetch");
  assert.equal(next.builders?.[0]?.building, undefined);
  assert.equal(next.builders?.[0]?.waiting?.length, 2);
});

test("a release the queue can't place reads it again", () => {
  // New, or past its build while still in a builder's slot.
  assert.equal(
    withRelease(queue, ref, { number: 5, status: "queued" }),
    "refetch",
  );
  assert.equal(
    withRelease(queue, ref, { number: 4, status: "starting" }),
    "refetch",
  );
  // Done and not in it: nothing to do.
  assert.equal(
    withRelease(queue, ref, { number: 5, status: "failed" }),
    undefined,
  );
});

test("a step past its build moves on in place", () => {
  const db = { ...ref, service: "db" };
  const next = withRelease(queue, db, { number: 2, status: "starting" });
  assert.ok(next && next !== "refetch");
  assert.equal(next.deploying?.[0]?.status, "starting");
});
