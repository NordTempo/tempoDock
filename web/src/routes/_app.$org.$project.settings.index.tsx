import { createFileRoute } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { queries, useProjectOverview, useSession } from "../data/queries";
import { Placements } from "../features/overview/Placements";
import {
  ArchiveProject,
  Export,
  ProjectName,
} from "../features/overview/ProjectSettings";
import { Page, SectionHead } from "../ui/kit";

const ApplyFile = lazy(() => import("../features/apply/ApplyFile"));

// A project's General settings (ADR 0059): its name, where its
// environments are placed, its config file and archiving it.
export const Route = createFileRoute("/_app/$org/$project/settings/")({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(queries.placements(params.project)),
  component: General,
});

function General() {
  const { project } = Route.useParams();
  const overview = useProjectOverview(project);
  const session = useSession();
  const [applying, setApplying] = useState(false);
  // Staging a file needs deploy in the project (environment-limited
  // tokens only their own environment, which the dialog fixes).
  const canApply =
    !!session &&
    session.scope !== "read" &&
    (!session.project || session.project === project);
  return (
    <Page>
      <ProjectName project={overview.project} />
      <Placements project={project} />
      <section aria-labelledby="config-file">
        <SectionHead id="config-file">Config file</SectionHead>
        <Export
          project={project}
          onApply={canApply ? () => setApplying(true) : undefined}
        />
      </section>
      <ArchiveProject project={project} />
      {applying && (
        <Suspense fallback={null}>
          <ApplyFile project={project} onClose={() => setApplying(false)} />
        </Suspense>
      )}
    </Page>
  );
}
