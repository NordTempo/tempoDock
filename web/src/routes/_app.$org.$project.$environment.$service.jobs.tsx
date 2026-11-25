import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useLocation } from "@tanstack/react-router";
import { useEffect } from "react";
import { servicePath } from "../data/queries";
import { jobQueries } from "../features/jobs/data";
import { JobsPanel } from "../features/jobs/JobsPanel";
import { runQueries } from "../features/runs/data";
import { RunPanel } from "../features/runs/RunPanel";
import { SectionHead } from "../ui/kit";

// A service's Jobs tab (ADR 0076): what runs in its image besides the
// service. A command run once (ADR 0048): the box and the latest runs,
// #run. Then the scheduled jobs (ADR 0054), the selected one's editor and
// history, and Run now. The header's "···" › Run a command opens #run and
// lands in the box.
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
  const hash = useLocation({ select: (l) => l.hash });
  // Arriving at #run (the header menu's Run a command): the box has focus.
  useEffect(() => {
    if (hash !== "run") return;
    document
      .querySelector<HTMLElement>("#run input, #run textarea")
      ?.focus({ preventScroll: false });
  }, [hash]);
  return (
    <>
      {/* Headings keep their own margin; the gap is between what's under them. */}
      <section id="run" aria-labelledby="run-head">
        <SectionHead id="run-head">Run a command</SectionHead>
        <div className="flex flex-col gap-3">
          <RunPanel serviceRef={ref} />
        </div>
      </section>
      <section aria-labelledby="jobs-head">
        <SectionHead id="jobs-head">Scheduled</SectionHead>
        <div className="flex flex-col gap-3">
          <JobsPanel serviceRef={ref} jobs={jobs} />
        </div>
      </section>
    </>
  );
}
