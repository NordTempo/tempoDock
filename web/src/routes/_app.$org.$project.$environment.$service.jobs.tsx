import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { jobQueries } from "../features/jobs/data";
import { JobsPanel } from "../features/jobs/JobsPanel";
import { runQueries } from "../features/runs/data";
import { RunPanel } from "../features/runs/RunPanel";
import { SectionHead } from "../ui/kit";

// A service's Jobs tab (ADR 0076): what runs in its image besides the
// service. A command run once (ADR 0048): the box and the latest runs,
// #run. Then the scheduled jobs (ADR 0054), the selected one's editor and
// history, and Run now.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/jobs",
)({
  loader: ({ context: { queryClient }, params }) =>
    Promise.all([
      queryClient.ensureQueryData(jobQueries.list(servicePath(params))),
      queryClient.ensureQueryData(runQueries.list(servicePath(params))),
    ]),
  component: JobsTab,
});

function JobsTab() {
  const ref = servicePath(Route.useParams());
  const jobs = useSuspenseQuery(jobQueries.list(ref)).data;
  return (
    <>
      <section
        id="run"
        aria-labelledby="run-head"
        className="flex flex-col gap-3"
      >
        <SectionHead id="run-head">Run a command</SectionHead>
        <RunPanel serviceRef={ref} />
      </section>
      <section aria-labelledby="jobs-head" className="flex flex-col gap-3">
        <SectionHead id="jobs-head">Scheduled</SectionHead>
        <JobsPanel serviceRef={ref} jobs={jobs} />
      </section>
    </>
  );
}
