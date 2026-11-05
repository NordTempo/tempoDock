import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useEffect } from "react";
import { queries, useProjectOverview } from "../data/queries";
import { rememberProject, useOrg } from "../shell/location";
import { PageBar } from "../ui/kit";

// A project's settings (ADR 0059): General (name, placement, config
// file, archive), the shared variables of each environment and preview
// environments, as tabs.
export const Route = createFileRoute("/_app/$org/$project/settings")({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(queries.overview(params.project)),
  component: ProjectSettings,
});

function ProjectSettings() {
  const org = useOrg();
  const { project } = Route.useParams();
  const { environments } = useProjectOverview(project);
  useEffect(() => rememberProject(project), [project]);
  return (
    <>
      <PageBar
        parent={
          <Link
            to="/$org/$project"
            params={{ org, project }}
            className="hover:text-fg"
          >
            {project}
          </Link>
        }
        title="Settings"
      />
      <nav
        aria-label="Project settings"
        className="tabs flex gap-0.5 overflow-x-auto border-b border-line px-4 py-2 whitespace-nowrap"
      >
        <Link
          to="/$org/$project/settings"
          params={{ org, project }}
          activeOptions={{ exact: true }}
        >
          General
        </Link>
        {environments.length > 0 && (
          <Link
            to="/$org/$project/settings/variables"
            params={{ org, project }}
          >
            Variables
            {environments.some((e) => e.pending_changes > 0) && (
              <span className="text-warn"> ▲</span>
            )}
          </Link>
        )}
        <Link to="/$org/$project/settings/previews" params={{ org, project }}>
          Previews
        </Link>
      </nav>
      <Outlet />
    </>
  );
}
