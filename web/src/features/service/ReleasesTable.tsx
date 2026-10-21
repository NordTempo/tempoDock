import { Link } from "@tanstack/react-router";
import { useRollback } from "../../data/mutations";
import type { Release } from "../../data/types";
import { since } from "../../ui/format";
import { Action, ErrorText, Status } from "../../ui/kit";
import { changeText, releaseStatus } from "../../ui/status";

/** Release history with failure reasons and one-click rollback. */
export function ReleasesTable({
  serviceId,
  releases,
  deploying,
}: {
  serviceId: string;
  releases: Release[];
  /** Rollback is locked while a deploy is in flight. */
  deploying: boolean;
}) {
  const rollback = useRollback();
  return (
    <>
      <table className="tbl">
        <thead>
          <tr>
            <th />
            <th>Status</th>
            <th>Change</th>
            <th>By</th>
            <th>When</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {releases.map((release) => {
            const { status } = release;
            const pruned = status.state === "superseded" && status.imagePruned;
            return (
              <tr
                key={release.id}
                className={pruned ? "text-faint" : undefined}
              >
                <td className="font-mono text-xs">r{release.number}</td>
                <td>
                  <Status {...releaseStatus(status)} colorLabel={false} />
                </td>
                <td className="w-full whitespace-normal">
                  <span
                    className={`font-mono text-xs ${pruned ? "" : "text-dim"}`}
                  >
                    {changeText(release.change)}
                  </span>
                  {status.state === "failed" && (
                    <span className="block text-xs text-bad">
                      {status.reason}
                    </span>
                  )}
                </td>
                <td>{release.actor.name}</td>
                <td className={pruned ? undefined : "text-dim"}>
                  {since(release.createdAt)}
                </td>
                <td className="text-dim">
                  {status.state === "active" && "current"}
                  {status.state === "failed" && (
                    <Link
                      to="/services/$serviceId/logs"
                      params={{ serviceId }}
                      search={{ release: release.number }}
                      className="hover:text-white"
                    >
                      logs
                    </Link>
                  )}
                  {status.state === "superseded" &&
                    (pruned ? (
                      "image pruned"
                    ) : (
                      <Action
                        disabled={deploying || rollback.isPending}
                        title={
                          deploying ? "Wait for the current deploy" : undefined
                        }
                        onClick={() =>
                          rollback.mutate({ serviceId, to: release.number })
                        }
                      >
                        rollback
                      </Action>
                    ))}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <ErrorText error={rollback.error} />
    </>
  );
}
