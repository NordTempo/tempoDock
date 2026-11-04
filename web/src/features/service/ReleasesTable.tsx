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
import { copiedConfig, errorSummary, imageLabel } from "./releases";

/** Release history with failure reasons and inline rollback. */
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
  const rollback = useRollback();
  const mayDeploy = useCanDeploy(ref);
  // A rollback first shows the image and config it brings back.
  const [confirming, setConfirming] = useState<number | null>(null);
  const target = releases.find((r) => r.number === confirming);
  if (releases.length === 0) {
    return <p className="text-mut">Not deployed yet.</p>;
  }
  return (
    <>
      <Panel>
        <table>
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
            {releases.map((r) => (
              <tr key={r.id}>
                <td className="font-mono">
                  <Link
                    to="/$org/$project/$environment/$service/releases/$number"
                    params={{ org, ...ref, number: String(r.number) }}
                    className="no-underline hover:underline"
                  >
                    r{r.number}
                  </Link>
                </td>
                <td>
                  <StateText state={releaseState(r.status)} />
                </td>
                <td className="w-full whitespace-normal">
                  {releaseWhat(r)}{" "}
                  <span className="font-mono text-mut">{imageLabel(r)}</span>
                  {r.status === "failed" && r.error && (
                    <span className="text-fail"> · {errorSummary(r)}</span>
                  )}
                </td>
                <td>
                  <Actor name={r.created_by} kind={actorKind(r.created_by)} />
                </td>
                <td className="text-faint">{since(r.created_at)}</td>
                <td className="py-1">
                  {r.status === "active" &&
                    (stopped ? (
                      <span className="pill border border-line text-mut">
                        stopped
                      </span>
                    ) : (
                      <span className="pill border border-ok-edge bg-ok-bg text-ok">
                        serving
                      </span>
                    ))}
                  {r.status === "failed" && (
                    <Link
                      to="/$org/$project/$environment/$service/releases/$number"
                      params={{ org, ...ref, number: String(r.number) }}
                    >
                      Why
                    </Link>
                  )}
                  {r.status === "superseded" && mayDeploy && (
                    <Button
                      small
                      disabled={deploying || rollback.isPending}
                      title={
                        deploying ? "Wait for the current deploy" : undefined
                      }
                      aria-pressed={confirming === r.number}
                      onClick={() =>
                        setConfirming(confirming === r.number ? null : r.number)
                      }
                    >
                      Roll back to r{r.number}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      {target && (
        <Confirm
          action={`Roll back to r${target.number}`}
          cli={`dock rollback ${target.number} -s ${ref.service} -e ${ref.environment}`}
          pending={deploying || rollback.isPending}
          error={rollback.error}
          onConfirm={() =>
            rollback.mutate(
              { ref, to: target.number },
              { onSuccess: () => setConfirming(null) },
            )
          }
          onCancel={() => {
            rollback.reset();
            setConfirming(null);
          }}
        >
          A new release with r{target.number}'s image and config:{" "}
          <span className="font-mono text-mut">{copiedConfig(target)}</span>
        </Confirm>
      )}
    </>
  );
}
