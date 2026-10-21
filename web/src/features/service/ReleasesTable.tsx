import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useRollback } from "../../data/mutations";
import type { Release, ServiceRef } from "../../data/types";
import { since } from "../../ui/format";
import { Action, ErrorText, Status } from "../../ui/kit";
import { changeText, releaseStatus, shortId } from "../../ui/status";
import { copiedConfig } from "./releases";

/** Release history with failure reasons and one-click rollback. */
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
    return <p className="py-2 text-dim">Not deployed yet.</p>;
  }
  return (
    <>
      <table className="tbl">
        <thead>
          <tr>
            <th />
            <th>Status</th>
            <th>Change</th>
            <th>Image</th>
            <th>By</th>
            <th>When</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {releases.map((release) => (
            <tr key={release.id}>
              <td className="font-mono text-xs">r{release.number}</td>
              <td>
                <Status {...releaseStatus(release.status)} colorLabel={false} />
              </td>
              <td className="w-full whitespace-normal">
                <span className="font-mono text-xs text-dim">
                  {changeText(release)}
                </span>
                {release.status === "failed" && release.error && (
                  <span className="block text-xs text-bad">
                    {release.error}
                  </span>
                )}
              </td>
              <td className="font-mono text-xs text-dim">
                {release.image_id ? shortId(release.image_id) : "-"}
              </td>
              <td>{release.created_by}</td>
              <td className="text-dim">{since(release.created_at)}</td>
              <td className="text-dim">
                {release.status === "active" && "current"}
                {release.status === "failed" && (
                  <Link
                    to="/$project/$environment/$service/logs"
                    params={ref}
                    search={{ release: release.number, build: true }}
                    className="hover:text-white"
                  >
                    build log
                  </Link>
                )}
                {release.status === "superseded" &&
                  (confirming === release.number ? (
                    <span className="flex justify-end gap-3">
                      <span className="font-mono text-xs">
                        {copiedConfig(release)}
                      </span>
                      <button
                        type="button"
                        className="text-white"
                        disabled={deploying || rollback.isPending}
                        onClick={() =>
                          rollback.mutate(
                            { ref, to: release.number },
                            { onSuccess: () => setConfirming(null) },
                          )
                        }
                      >
                        Roll back to r{release.number}
                      </button>
                      <Action onClick={() => setConfirming(null)}>
                        Cancel
                      </Action>
                    </span>
                  ) : (
                    <Action
                      disabled={deploying || rollback.isPending}
                      title={
                        deploying ? "Wait for the current deploy" : undefined
                      }
                      onClick={() => setConfirming(release.number)}
                    >
                      rollback
                    </Action>
                  ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <ErrorText error={rollback.error} />
    </>
  );
}
