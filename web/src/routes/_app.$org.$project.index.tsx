import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { ArchivedProject } from "../features/overview/Archived";
import { Export } from "../features/overview/ProjectSettings";
import { loadProject, ProjectView } from "../features/overview/ProjectView";

export const Route = createFileRoute("/_app/$org/$project/")({
  loader: ({ context: { queryClient }, params }) =>
    loadProject(queryClient, params.project),
  component: Overview,
});

/** An archived project gets a read-only page; a live one, the overview. */
function Overview() {
  const { project } = Route.useParams();
  const archived = useSuspenseQuery({
    ...queries.overview(project),
    select: (o) => !!o.project.archived_at,
  }).data;
  if (archived) {
    return (
      <ArchivedProject project={project}>
        <Export project={project} />
      </ArchivedProject>
    );
  }
  return <ProjectView project={project} />;
}
