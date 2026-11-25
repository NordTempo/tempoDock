import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { queries, servicePath, useAuditLog, useService } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { ReleaseHistory } from "../features/service/ReleaseHistory";
import { Button, SectionHead } from "../ui/kit";

// A service's History tab (ADR 0076): its releases, with rollback and
// diff, then everything else that happened to it (settings, variables,
// volumes, domains: the audit trail). A release opens under it,
// …/history/<number>. Both lists start with their newest 5, so the tab
// fits a screen; Activity's Show more shows the rest of what it loaded.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/history/",
)({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.audit(servicePath(params))),
  component: HistoryTab,
});

function HistoryTab() {
  const ref = servicePath(Route.useParams());
  const { deploying } = useService(ref);
  const events = useAuditLog(ref);
  const [all, setAll] = useState(false);
  return (
    <>
      <ReleaseHistory serviceRef={ref} deploying={!!deploying} />
      <section id="activity" aria-labelledby="activity-head">
        <SectionHead id="activity-head">Activity</SectionHead>
        <AuditTable events={all ? events : events.slice(0, 5)} />
        {!all && events.length > 5 && (
          <div className="mt-2">
            <Button small onClick={() => setAll(true)}>
              Show more
            </Button>
          </div>
        )}
      </section>
    </>
  );
}
