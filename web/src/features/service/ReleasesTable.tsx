import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useRollback } from "../../data/mutations";
import type { Release, ServiceRef } from "../../data/types";
import { since } from "../../ui/format";
import {
  Actor,
  actorKind,
  Button,
  ErrorText,
  Scroll,
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
  const rollback = useRollback();
  // A rollback first shows the image and config it brings back.
  const [confirming, setConfirming] = useState<number | null>(null);
  if (releases.length === 0) {
    return <p className="py-2 text-mut">Not deployed yet.</p>;
  }
  return (
    <>
      <Scroll>
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
                    to="/$project/$environment/$service/releases/$number"
                    params={{ ...ref, number: String(r.number) }}
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
                <td className="text-mut">{since(r.created_at)}</td>
                <td className="py-1">
                  {r.status === "active" && (
                    <span className="text-mut">serving</span>
                  )}
                  {r.status === "failed" && (
                    <Link
                      to="/$project/$environment/$service/releases/$number"
                      params={{ ...ref, number: String(r.number) }}
                    >
                      Why
                    </Link>
                  )}
                  {r.status === "superseded" &&
                    (confirming === r.number ? (
                      <span className="flex items-center gap-2">
                        <span className="font-mono text-xs text-mut">
                          {copiedConfig(r)}
                        </span>
                        <Button
                          small
                          primary
                          disabled={deploying || rollback.isPending}
                          onClick={() =>
                            rollback.mutate(
                              { ref, to: r.number },
                              { onSuccess: () => setConfirming(null) },
                            )
                          }
                        >
                          Roll back
                        </Button>
                        <Button small onClick={() => setConfirming(null)}>
                          Cancel
                        </Button>
                      </span>
                    ) : (
                      <Button
                        small
                        disabled={deploying || rollback.isPending}
                        title={
                          deploying ? "Wait for the current deploy" : undefined
                        }
                        onClick={() => setConfirming(r.number)}
                      >
                        Roll back to r{r.number}
                      </Button>
                    ))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Scroll>
      <ErrorText error={rollback.error} />
    </>
  );
}
