import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath, useAuditLog, useService } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { ReleaseHistory } from "../features/service/ReleaseHistory";
import { SectionHead } from "../ui/kit";

// A service's History tab (ADR 0076): its releases, with rollback and
// diff, then everything else that happened to it (settings, variables,
// volumes, domains: the audit trail). A release opens under it,
// …/history/<number>.
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
  return (
    <>
      <ReleaseHistory serviceRef={ref} deploying={!!deploying} />
      <section
        id="activity"
        aria-labelledby="activity-head"
        className="scroll-mt-28"
      >
        <SectionHead id="activity-head">Activity</SectionHead>
        <AuditTable events={useAuditLog(ref)} />
      </section>
    </>
  );
}
