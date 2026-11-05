import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useRollback } from "../../data/mutations";
import { useCanDeploy, useService } from "../../data/queries";
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
import { copiedConfig, errorSummary, imageLabel, shortImage } from "./releases";

/**
 * Release history: one line per release (what, short image, who, when),
 * a failure's cause under it, and Roll back on a superseded row, shown on
 * hover or focus so eight identical buttons don't fill the column.
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
  // A stopped service's active release serves nothing (ADR 0050).
  const stopped = !!useService(ref).service.stopped_at;
  const mayDeploy = useCanDeploy(ref);
  // A rollback first shows the image and config it brings back.
  const [confirming, setConfirming] = useState<number | null>(null);
  const target = releases.find((r) => r.number === confirming);
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
            <col className="w-[100px]" />
            <col className="w-[44px]" />
            <col className="w-[84px]" />
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
                    <Link {...page(r)} className="hover:underline">
                      r{r.number}
                    </Link>
                  </td>
                  <td>
                    <StateText
                      state={
                        r.status === "active"
                          ? stopped
                            ? { tone: "mut", glyph: "○", label: "stopped" }
                            : { tone: "ok", glyph: "●", label: "serving" }
                          : releaseState(r.status)
                      }
                    />
                  </td>
                  <td className="truncate">
                    <span title={image}>
                      {releaseWhat(r)}{" "}
                      <span className="font-mono text-xs text-mut">
                        {shortImage(image)}
                      </span>
                    </span>
                    {r.status === "failed" && r.error && (
                      <div
                        className="truncate text-xs text-fail"
                        title={r.error}
                      >
                        {errorSummary(r)}
                      </div>
                    )}
                  </td>
                  <td className="truncate">
                    <Actor name={r.created_by} kind={actorKind(r.created_by)} />
                  </td>
                  <td className="text-faint">{since(r.created_at)}</td>
                  <td className="py-1 text-right">
                    {r.status === "failed" && (
                      <Link {...page(r)} className="lnk">
                        Details
                      </Link>
                    )}
                    {r.status === "superseded" && mayDeploy && (
                      <Button
                        small
                        className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 aria-pressed:opacity-100"
                        disabled={deploying}
                        title={
                          deploying
                            ? "Wait for the current deploy"
                            : `Roll back to r${r.number}`
                        }
                        aria-label={`Roll back to r${r.number}`}
                        aria-pressed={confirming === r.number}
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
          serving={releases.find((r) => r.status === "active")}
          blocked={deploying}
          onDone={() => setConfirming(null)}
        />
      )}
    </>
  );
}

/**
 * Asks before rolling back: which service, from which release to which,
 * what the new release copies and what happens to the serving one. The
 * header, the releases table and a release's page all use it.
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
  const from = serving ? `r${serving.number}` : undefined;
  return (
    <Confirm
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
      <p>
        A new release of <b>{ref.service}</b> in {ref.environment} with r
        {target.number}'s image and config.{" "}
        {from &&
          (stateful
            ? `${from} stops first, then it starts (the volume allows one writer).`
            : `${from} keeps serving until it passes its health check.`)}
      </p>
      <p className="mt-2 font-mono text-xs text-mut">{copiedConfig(target)}</p>
    </Confirm>
  );
}
