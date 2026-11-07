import { Link } from "@tanstack/react-router";
import type { AuditEvent } from "../../data/types";
import { useOrg } from "../../shell/location";

/**
 * Where a failed run's output is, for audit rows that record one: a failed
 * backup opens its service's Backups tab, where the failed run has View
 * log. Nothing for other actions.
 */
export function AuditLogLink({ event: e }: { event: AuditEvent }) {
  const org = useOrg();
  const service = e.target.split("/").at(-1);
  if (e.action !== "backup.failed" || !e.project || !e.environment || !service)
    return null;
  return (
    <Link
      to="/$org/$project/$environment/$service/backups"
      params={{ org, project: e.project, environment: e.environment, service }}
      className="text-fail underline underline-offset-3"
    >
      View log
    </Link>
  );
}
