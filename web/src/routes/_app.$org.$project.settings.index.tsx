import { createFileRoute } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import {
  queries,
  useProjectOverview,
  useSeesPlatform,
  useSession,
} from "../data/queries";
import { logDrainQueries } from "../features/logdrains/data";
import { LogDrains } from "../features/logdrains/LogDrains";
import { defaultEnvironment } from "../features/overview/load";
import { Placements } from "../features/overview/Placements";
import {
  ArchiveProject,
  Export,
  ProjectName,
} from "../features/overview/ProjectSettings";
import { projectSettingSections } from "../features/overview/sections";
import { previewQueries } from "../features/previews/data";
import { PreviewsSection } from "../features/previews/PreviewsSection";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { Act, Page } from "../ui/kit";
import { SectionsLayout, SettingsSection } from "../ui/sections";

const ApplyFile = lazy(() => import("../features/apply/ApplyFile"));
const SaveTemplate = lazy(() => import("../features/templates/SaveTemplate"));
const MovePanel = lazy(() => import("../features/move/MovePanel"));

// Everything that configures a project, on one page with a list of its
// sections (projectSettingSections): its name, the picked environment's
// shared variables, previews, where its environments run, the picked
// environment's log drains, its config file and archiving it.
export const Route = createFileRoute("/_app/$org/$project/settings/")({
  loaderDeps: ({ search }) => ({ env: search.env }),
  loader: async ({ context: { queryClient }, params, deps }) => {
    const [overview, session] = await Promise.all([
      queryClient.ensureQueryData(queries.overview(params.project)),
      queryClient.ensureQueryData(queries.session()),
    ]);
    const known = overview.environments.some(
      (e) => e.environment.name === deps.env,
    );
    const environment = known ? deps.env : defaultEnvironment(overview);
    queryClient.prefetchQuery(previewQueries.settings(params.project));
    if (session?.platform) {
      queryClient.prefetchQuery(queries.placements(params.project));
    }
    if (!environment) return;
    const env = { project: params.project, environment };
    queryClient.prefetchQuery(logDrainQueries.list(env));
    await Promise.all([
      queryClient.ensureQueryData(queries.variables(env, null)),
      queryClient.ensureQueryData(queries.changes(env)),
    ]);
  },
  component: Settings,
});

function Settings() {
  const { project } = Route.useParams();
  const { env: asked } = Route.useSearch();
  const overview = useProjectOverview(project);
  const session = useSession();
  const [applying, setApplying] = useState(false);
  const [saving, setSaving] = useState(false);
  const environment = overview.environments.some(
    (e) => e.environment.name === asked,
  )
    ? asked
    : defaultEnvironment(overview);
  const env = environment ? { project, environment } : undefined;
  // Staging a file needs deploy in the project (environment-limited
  // tokens only their own environment, which the dialog fixes).
  const canApply =
    !!session &&
    session.scope !== "read" &&
    (!session.project || session.project === project);
  // Per-environment sections need an environment.
  const sections = projectSettingSections.filter(
    (s) => env || (s.id !== "variables" && s.id !== "log-drains"),
  );
  return (
    <SectionsLayout label={`${project} settings`} sections={[...sections]}>
      <Page>
        <SettingsSection id="general" title="General">
          <ProjectName project={overview.project} />
        </SettingsSection>
        {env && (
          <SettingsSection
            id="variables"
            title={`Variables in ${env.environment}`}
          >
            <p className="mb-2 text-xs text-mut">
              Shared with every service in {env.environment}, unless a service
              sets the same name. Each service's own are on its Variables tab.
            </p>
            <VariablesEditor key={env.environment} env={env} service={null} />
          </SettingsSection>
        )}
        <SettingsSection id="previews" title="Previews">
          <p className="mb-2 text-xs text-mut">
            A preview environment for each pull request. They turn on per
            service, in its Settings › Source, which also sets what it copies
            into a preview.
          </p>
          <PreviewsSection project={project} />
        </SettingsSection>
        <SettingsSection id="placement" title="Placement">
          <EnvironmentServers project={project} />
          {session?.platform && (
            <Suspense fallback={null}>
              <Placements project={project} />
            </Suspense>
          )}
        </SettingsSection>
        {env && (
          <SettingsSection
            id="log-drains"
            title={`Log drains in ${env.environment}`}
          >
            <Suspense fallback={null}>
              <LogDrains key={env.environment} env={env} />
            </Suspense>
          </SettingsSection>
        )}
        <SettingsSection id="config" title="Config file">
          <Export
            project={project}
            onApply={canApply ? () => setApplying(true) : undefined}
            onSaveTemplate={
              session?.scope === "admin" ? () => setSaving(true) : undefined
            }
          />
        </SettingsSection>
        <SettingsSection id="archive" title="Archive">
          <ArchiveProject project={project} />
        </SettingsSection>
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
    </SectionsLayout>
  );
}

/**
 * Which server each environment runs on, with Move for operators (the
 * move's form and progress open under its row).
 */
function EnvironmentServers({ project }: { project: string }) {
  const { environments } = useProjectOverview(project);
  const operator = useSeesPlatform();
  const [moving, setMoving] = useState<string | null>(null);
  if (environments.length === 0) {
    return <p className="text-mut">No environments yet.</p>;
  }
  return (
    <div className="border-t border-line">
      {environments.map(({ environment: e }) => (
        <div key={e.id} className="border-b border-line py-2.5">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <span className="w-40 font-mono">{e.name}</span>
            <span className="min-w-0 flex-1">
              {e.server ? (
                <span className="font-mono">{e.server}</span>
              ) : (
                <span className="text-mut">not placed yet</span>
              )}
              {e.moving && (
                <span className="ml-2 text-xs text-prog">moving</span>
              )}
            </span>
            {operator && (
              <Act
                small
                cli={`dock environment move ${e.name} --to <server> -p ${project}`}
                aria-expanded={moving === e.name}
                onClick={() => setMoving(moving === e.name ? null : e.name)}
              >
                {moving === e.name ? "Hide" : "Move"}
              </Act>
            )}
          </div>
          {moving === e.name && (
            <div className="pt-2.5">
              <Suspense fallback={null}>
                <MovePanel
                  env={{ project, environment: e.name }}
                  server={e.server ?? ""}
                  kind={e.kind}
                  operator={operator}
                />
              </Suspense>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}
