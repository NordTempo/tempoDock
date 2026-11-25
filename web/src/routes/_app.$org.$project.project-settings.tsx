import { createFileRoute } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { queries, useProjectOverview, useSession } from "../data/queries";
import { defaultEnvironment } from "../features/overview/load";
import { Placements } from "../features/overview/Placements";
import { ProjectHeader } from "../features/overview/ProjectHeader";
import {
  ArchiveProject,
  Export,
  ProjectName,
} from "../features/overview/ProjectSettings";
import { previewQueries } from "../features/previews/data";
import { PreviewsSection } from "../features/previews/PreviewsSection";
import { rememberProject } from "../shell/location";
import { Page } from "../ui/kit";
import { SettingRow } from "../ui/sections";

const ApplyFile = lazy(() => import("../features/apply/ApplyFile"));
const SaveTemplate = lazy(() => import("../features/templates/SaveTemplate"));

// A project's own settings, for every environment (ADR 0099): its name,
// previews, where new environments go, its config file and archiving
// it. The header's environment switch doesn't scope them, so it's
// greyed out here; ?env= only keeps the pick for the other tabs.
export const Route = createFileRoute("/_app/$org/$project/project-settings")({
  validateSearch: (search: Record<string, unknown>): { env?: string } => ({
    env: typeof search.env === "string" && search.env ? search.env : undefined,
  }),
  loader: async ({ context: { queryClient }, params }) => {
    const [, session] = await Promise.all([
      queryClient.ensureQueryData(queries.overview(params.project)),
      queryClient.ensureQueryData(queries.session()),
    ]);
    queryClient.prefetchQuery(previewQueries.settings(params.project));
    if (session?.platform) {
      queryClient.prefetchQuery(queries.placements(params.project));
    }
  },
  component: ProjectSettings,
});

function ProjectSettings() {
  const { project } = Route.useParams();
  const { env } = Route.useSearch();
  const overview = useProjectOverview(project);
  const session = useSession();
  const [applying, setApplying] = useState(false);
  const [saving, setSaving] = useState(false);
  useEffect(() => rememberProject(project), [project]);
  const environment = overview.environments.some(
    (e) => e.environment.name === env,
  )
    ? env
    : defaultEnvironment(overview);
  // Staging a file needs deploy in the project (environment-limited
  // tokens only their own environment, which the dialog fixes).
  const canApply =
    !!session &&
    session.scope !== "read" &&
    (!session.project || session.project === project);
  return (
    <>
      <ProjectHeader
        project={project}
        environment={environment}
        tab="project"
      />
      <Page>
        <div>
          <SettingRow id="general" label="Name">
            <ProjectName project={overview.project} />
          </SettingRow>
          <SettingRow id="previews" label="Previews">
            <PreviewsSection project={project} />
          </SettingRow>
          {session?.platform && (
            <SettingRow id="placement" label="New environments">
              <Suspense fallback={null}>
                <Placements project={project} />
              </Suspense>
            </SettingRow>
          )}
          <SettingRow id="config" label="Config file">
            <Export
              project={project}
              onApply={canApply ? () => setApplying(true) : undefined}
              onSaveTemplate={
                session?.scope === "admin" ? () => setSaving(true) : undefined
              }
            />
          </SettingRow>
          <SettingRow id="archive" label="Archive" danger>
            <ArchiveProject project={project} />
          </SettingRow>
        </div>
        {saving && (
          <Suspense fallback={null}>
            <SaveTemplate project={project} onClose={() => setSaving(false)} />
          </Suspense>
        )}
        {applying && (
          <Suspense fallback={null}>
            <ApplyFile project={project} onClose={() => setApplying(false)} />
          </Suspense>
        )}
      </Page>
    </>
  );
}
