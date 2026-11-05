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
import {
  copiedConfig,
  errorSummary,
  imageLabel,
  releaseDiff,
  rollbackLosses,
  shortImage,
} from "./releases";

/**
 * Release history: one line per release (what, short image, who, when),
 * a failure's cause under it in full, Details on a failed row and a small
 * Roll back button on every superseded row (left out while a deploy runs).
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
            <col className="w-[96px]" />
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
                      // The cause wraps: its end ("on :3000") is the useful part.
                      <div
                        className="text-xs break-words whitespace-normal text-fail"
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
                  {/* Lined up with the first text line of the row. */}
                  <td className="py-[6px] text-right">
                    {r.status === "failed" && (
                      <Link {...page(r)} className="btn sm">
                        Details
                      </Link>
                    )}
                    {r.status === "superseded" && mayDeploy && !deploying && (
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
 * what changes against the serving release, and whether the service is
 * briefly unreachable. Changes that lose data or features (a volume no
 * longer mounted, variables removed) get a warning line each and a red
 * button. The header, the releases table and a release's page use it.
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
  const changes = serving ? releaseDiff(serving, target) : [];
  const losses = serving ? rollbackLosses(serving, target) : [];
  return (
    <Confirm
      title={`Roll back ${ref.service} to r${target.number}?`}
      action={`Roll back to r${target.number}`}
      danger={losses.length > 0}
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
        {target.number}'s image and config.
      </p>
      {from && (
        <p className="mt-2">
          {stateful ? (
            <b>
              {ref.service} is down for a few seconds while {from} stops and the
              new release starts: a volume allows one writer.
            </b>
          ) : (
            `${from} keeps serving until the new release passes its health check, so ${ref.service} stays reachable.`
          )}
        </p>
      )}
      {losses.map((text) => (
        <p key={text} className="mt-2 text-warn-text">
          ▲ {text}
        </p>
      ))}
      {serving && (
        <div className="mt-3 flex flex-col gap-0.5 text-xs">
          <span className="text-mut">Changes from {from}</span>
          {changes.length === 0 ? (
            <span className="text-mut">
              Same image, settings and variable names; variable values return to
              r{target.number}'s.
            </span>
          ) : (
            changes.map((c) => (
              <span key={c.what} className="font-mono break-all">
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
                    <span className="text-warn">~</span> {c.text}
                  </>
                )}
              </span>
            ))
          )}
        </div>
      )}
      <p className="mt-2 font-mono text-2xs text-faint">
        {copiedConfig(target)}
      </p>
    </Confirm>
  );
}
