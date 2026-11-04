import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { jobQueries } from "../features/jobs/data";
import { JobsPanel } from "../features/jobs/JobsPanel";

// Scheduled jobs (ADR 0052): the service's jobs, the selected one's
// schedule editor and history, and Run now.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/jobs",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(jobQueries.list(servicePath(params))),
  component: JobsTab,
});

function JobsTab() {
  const ref = servicePath(Route.useParams());
  const jobs = useSuspenseQuery(jobQueries.list(ref)).data;
  return <JobsPanel serviceRef={ref} jobs={jobs} />;
}
