// A project as a pipeline: the order of its environments and what lies
// between a service in two of them. Run by `pnpm check:unit`.
import assert from "node:assert/strict";
import { test } from "node:test";
import type {
  EnvironmentView,
  ReleaseSummary,
  ServiceView,
} from "../../data/types";
import { servedElsewhere, stages, step } from "./pipeline.ts";

const release = (
  number: number,
  image: string,
  more: Partial<ReleaseSummary> = {},
): ReleaseSummary => ({
  number,
  image_id: `sha256:${image}`,
  image_ref: `shop-web:${image}`,
  // Later releases were made later: r9 on the 19th, r14 on the 24th.
  created_at: `2026-11-${10 + number}T10:00:00Z`,
  created_by: "kari",
  source: "image",
  status: "active",
  ...more,
});

const service = (
  environment: string,
  serving: ReleaseSummary | undefined,
  more: Partial<ServiceView> = {},
  port = 3000,
) =>
  ({
    service: {
      name: "web",
      environment,
      port,
      effective_memory_mb: 512,
      effective_cpus: 1,
      start_command: "",
      health_check_path: "/healthz",
      active_release: serving?.number,
    },
    active_release: serving,
    latest_release: serving,
    domains: [],
    ...more,
  }) as unknown as ServiceView;

const env = (
  name: string,
  kind: "production" | "non_production",
  services: ServiceView[],
  preview = false,
) =>
  ({
    environment: {
      name,
      kind,
      ...(preview && { preview: { number: 1 } }),
    },
    services,
    archived: [],
    pending_changes: 0,
  }) as unknown as EnvironmentView;

test("previews go apart and production runs last", () => {
  const { previews, lanes } = stages([
    env("production", "production", []),
    env("pr-1", "non_production", [], true),
    env("staging", "non_production", []),
  ]);
  assert.deepEqual(
    previews.map((e) => e.environment.name),
    ["pr-1"],
  );
  assert.deepEqual(
    lanes.map((e) => e.environment.name),
    ["staging", "production"],
  );
});

test("a newer image in staging is ahead, the same one is the same", () => {
  const staging = service("staging", release(14, "1.15.0"));
  const prod = service("production", release(9, "1.14.2"));
  assert.equal(step(staging, prod)?.kind, "ahead");
  assert.equal(step(prod, staging)?.kind, "behind");
  const same = service("production", release(10, "1.15.0"));
  assert.equal(step(staging, same)?.kind, "same");
});

test("a failed deploy of staging's image is a failed promote, with the port that differs", () => {
  const staging = service("staging", release(14, "1.15.0"), {}, 8080);
  const failed = release(8, "1.15.0", {
    status: "failed",
    error_code: "health_check_failed",
  });
  // r9 rolled back to r7's image after r8 failed.
  const prod = service(
    "production",
    release(9, "1.14.2", { source: "rollback" }),
    { last_failed_deploy: failed },
  );
  const s = step(staging, prod);
  assert.equal(s?.kind, "failed");
  assert.deepEqual(s?.diffs, [{ label: "port", from: "8080", to: "3000" }]);

  const where = servedElsewhere(
    [env("staging", "non_production", [staging])],
    prod,
    failed,
  );
  assert.equal(where?.view, staging);
  assert.deepEqual(where?.diffs, [{ label: "port", from: "8080", to: "3000" }]);
});

test("nothing lies between a service and one that never deployed", () => {
  assert.equal(
    step(service("staging", undefined), service("production", undefined)),
    undefined,
  );
});
