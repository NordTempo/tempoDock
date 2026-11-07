import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useRollback } from "../../data/mutations";
import { useCanDeploy, useReleases, useService } from "../../data/queries";
import type { Release, ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { since } from "../../ui/format";
import {
  Actor,
  actorKind,
  Button,
  Confirm,
  Panel,
  StateText,
} from "../../ui/kit";
import { releaseState, releaseWhat } from "../../ui/status";
import {
  copiedConfig,
  errorSummary,
  imageLabel,
  releaseDiff,
  rollbackLosses,
  servesCopyOf,
  shortImage,
} from "./releases";

/**
 * Release history: one line per release (what, short image, who, when),
 * a failure's cause under it in full, Details on a failed row and a small
 * Roll back button on every superseded row (left out while a deploy runs,
 * and where the serving release already is a copy of it).
 */
export function ReleasesTable({
  serviceRef: ref,
  releases,
  deploying,
}: {
  serviceRef: ServiceRef;
  releases: Release[];
  /** Rollback is locked while a deploy is in flight. */
  deploying: boolean;
}) {
  const org = useOrg();
  // A stopped service's active release serves nothing (ADR 0050), and it
  // is starting until its health check first passes.
  const { service } = useService(ref);
  const stopped = !!service.stopped_at;
  const starting = service.status.health === "starting";
  const mayDeploy = useCanDeploy(ref);
  // A rollback first shows the image and config it brings back.
  const [confirming, setConfirming] = useState<number | null>(null);
  const target = releases.find((r) => r.number === confirming);
  const serving = releases.find((r) => r.status === "active");
  if (releases.length === 0) {
    return <p className="text-mut">Not deployed yet.</p>;
  }
  const page = (r: Release) => ({
    to: "/$org/$project/$environment/$service/releases/$number" as const,
    params: { org, ...ref, number: String(r.number) },
  });
  return (
    <>
      <Panel>
        <table className="table-fixed">
          <colgroup>
            <col className="w-[68px]" />
            <col className="w-[100px]" />
            <col />
            <col className="w-[150px]" />
            <col className="w-[44px]" />
            <col className="w-[124px]" />
          </colgroup>
          <thead>
            <tr>
              <th>Release</th>
              <th>Status</th>
              <th>What</th>
              <th>By</th>
              <th>Age</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {releases.map((r) => {
              const image = imageLabel(r);
              return (
                <tr key={r.id} className="group">
                  <td className="font-mono">
                    {/* A 24px target without making the row taller. */}
                    <Link
                      {...page(r)}
                      className="-my-[4px] inline-flex min-h-[24px] min-w-[24px] items-center hover:underline"
                    >
                      r{r.number}
                    </Link>
                  </td>
                  <td>
                    <StateText
                      state={
                        r.status === "active"
                          ? stopped
                            ? { tone: "mut", glyph: "○", label: "stopped" }
                            : starting
                              ? { tone: "mut", glyph: "◐", label: "starting" }
                              : { tone: "ok", glyph: "●", label: "serving" }
                          : releaseState(r.status)
                      }
                    />
                  </td>
                  {/* What wraps to two lines; the title has it in full. */}
                  <td className="whitespace-normal">
                    <span
                      className="line-clamp-2 break-words"
                      title={`${releaseWhat(r)} ${image}`}
                    >
                      {releaseWhat(r)}{" "}
                      <span className="font-mono text-xs whitespace-nowrap text-mut">
                        {shortImage(image)}
                      </span>
                    </span>
                    {r.status === "failed" && r.error && (
                      // The cause wraps: its end ("on :3000") is the useful part.
                      <div
                        className="text-xs break-words whitespace-normal text-fail"
                        title={r.error}
                      >
                        {errorSummary(r)}
                      </div>
                    )}
                  </td>
                  <td className="truncate" title={r.created_by}>
                    <Actor name={r.created_by} kind={actorKind(r.created_by)} />
                  </td>
                  <td className="text-faint">{since(r.created_at)}</td>
                  {/* Lined up with the first text line of the row. */}
                  <td className="py-[6px] text-right">
                    {r.status === "failed" && (
                      <Link {...page(r)} className="btn sm">
                        Details
                      </Link>
                    )}
                    {r.status === "superseded" && servesCopyOf(serving, r) && (
                      <span
                        className="text-xs text-faint"
                        title={`r${serving?.number} already runs r${r.number}'s image and config`}
                      >
                        same as serving
                      </span>
                    )}
                    {r.status === "superseded" &&
                      mayDeploy &&
                      !deploying &&
                      !servesCopyOf(serving, r) && (
                        <Button
                          small
                          title={`dock rollback ${r.number} -s ${ref.service} -e ${ref.environment}`}
                          aria-label={`Roll back to r${r.number}`}
                          onClick={() => setConfirming(r.number)}
                        >
                          Roll back
                        </Button>
                      )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Panel>
      {target && (
        <RollbackConfirm
          serviceRef={ref}
          target={target}
          serving={serving}
          blocked={deploying}
          onDone={() => setConfirming(null)}
        />
      )}
    </>
  );
}

/**
 * Asks before rolling back: why this release in one line, then what the
 * service loses (a volume no longer mounted, variables removed) in red,
 * the new release and any downtime, and every change against the serving
 * release (folded when losses lead). One style wherever it opens: the header, the releases table
 * and a release's page.
 */
export function RollbackConfirm({
  serviceRef: ref,
  target,
  serving,
  blocked,
  onDone,
}: {
  serviceRef: ServiceRef;
  target: Release;
  serving: Release | undefined;
  /** A deploy in flight; the button waits. */
  blocked?: boolean;
  onDone: () => void;
}) {
  const rollback = useRollback();
  const stateful = useService(ref).service.stateful;
  // Newer replaced releases the one-click target passes over: the serving
  // release already runs them.
  const skipped = useReleases(ref).filter(
    (r) =>
      r.status === "superseded" &&
      r.number > target.number &&
      servesCopyOf(serving, r),
  );
  const from = serving ? `r${serving.number}` : undefined;
  const changes = serving ? releaseDiff(serving, target) : [];
  const losses = serving ? rollbackLosses(serving, target) : [];
  // Failed releases between the target and the serving one: never served.
  const failed = useReleases(ref).filter(
    (r) =>
      r.status === "failed" &&
      r.number > target.number &&
      r.number < (serving?.number ?? 0),
  );
  const passed = [
    ...skipped.map((r) => `r${r.number} is the same as ${from}`),
    ...failed.map((r) => `r${r.number} failed`),
  ];
  return (
    <Confirm
      danger={losses.length > 0}
      title={`Roll back ${ref.service} to r${target.number}?`}
      action={`Roll back to r${target.number}`}
      cli={`dock rollback ${target.number} -s ${ref.service} -e ${ref.environment}`}
      pending={blocked || rollback.isPending}
      error={rollback.error}
      onConfirm={() =>
        rollback.mutate({ ref, to: target.number }, { onSuccess: onDone })
      }
      onCancel={() => {
        rollback.reset();
        onDone();
      }}
    >
      {/* Why this release, first and short: the header button names no
          release, so the dialog says which and why before anything else. */}
      {passed.length > 0 && (
        <p className="mb-3 text-mut">
          r{target.number} is the last release that served and differs from{" "}
          {from}: {passed.join(", ")}.
        </p>
      )}
      {/* What it takes away comes first, in red: the reason to stop. */}
      {losses.length > 0 && (
        <div className="mb-3 rounded-[6px] border border-fail-edge bg-fail-bg px-3 py-2">
          <p className="font-semibold text-fail">
            {ref.service} loses {losses.length === 1 ? "this" : "these"}
          </p>
          <ul className="mt-1 flex flex-col gap-1 text-fg">
            {losses.map((text) => (
              <li key={text}>{text}</li>
            ))}
          </ul>
        </div>
      )}
      <p>
        A new release of <b>{ref.service}</b> in {ref.environment} with r
        {target.number}'s image and settings.{" "}
        {from &&
          (stateful ? (
            <b>
              {ref.service} is down for a few seconds while {from} stops and the
              new one starts: only one copy can use its volume at a time.
            </b>
          ) : (
            `${from} keeps serving until the new release passes its health check.`
          ))}
      </p>
      {/* Every change, folded when the losses above already say the part
          that matters, so the dialog stays short. */}
      {serving && (
        <details open={losses.length === 0} className="mt-3 text-xs">
          <summary className="cursor-pointer text-mut">
            {changes.length === 0
              ? `Same as ${from}`
              : `All ${changes.length} ${changes.length === 1 ? "change" : "changes"} from ${from}`}
          </summary>
          <div className="mt-1 flex flex-col gap-0.5">
            {changes.length === 0 ? (
              <span className="text-mut">
                Same image, settings and variable names; variable values return
                to r{target.number}'s.
              </span>
            ) : (
              changes.map((c) => (
                <span key={c.what + c.text} className="font-mono break-words">
                  {c.what === "added" ? (
                    <>
                      <span className="text-ok">+ back</span> {c.text}
                    </>
                  ) : c.what === "removed" ? (
                    <>
                      <span className="text-fail">− removed</span> {c.text}
                    </>
                  ) : (
                    <>
                      <span className="text-warn">~ changed</span> {c.text}
                    </>
                  )}
                </span>
              ))
            )}
          </div>
        </details>
      )}
      <p className="mt-2 font-mono text-2xs text-faint">
        {copiedConfig(target)}
      </p>
    </Confirm>
  );
}
