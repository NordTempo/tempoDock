import { Link } from "@tanstack/react-router";
import { useRollback } from "../../data/mutations";
import type { Release, ServiceRef } from "../../data/types";
import { since } from "../../ui/format";
import { Action, ErrorText, Status } from "../../ui/kit";
import { changeText, releaseStatus, shortId } from "../../ui/status";

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
                {release.status === "superseded" && (
                  <Action
                    disabled={deploying || rollback.isPending}
                    title={
                      deploying ? "Wait for the current deploy" : undefined
                    }
                    onClick={() => rollback.mutate({ ref, to: release.number })}
                  >
                    rollback
                  </Action>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <ErrorText error={rollback.error} />
    </>
  );
}
