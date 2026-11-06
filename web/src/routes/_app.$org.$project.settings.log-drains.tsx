import { createFileRoute } from "@tanstack/react-router";
import { queries, useProjectOverview } from "../data/queries";
import { logDrainQueries } from "../features/logdrains/data";
import { LogDrains } from "../features/logdrains/LogDrains";
import { Page } from "../ui/kit";

// A project's log drains, every environment's on one tab (their one home;
// the project page's environment menu links here). An environment's
// section is #drains-<environment>.
export const Route = createFileRoute("/_app/$org/$project/settings/log-drains")(
  {
    loader: async ({ context: { queryClient }, params }) => {
      const { environments } = await queryClient.ensureQueryData(
        queries.overview(params.project),
      );
      await Promise.all(
        environments.map((e) =>
          queryClient.ensureQueryData(
            logDrainQueries.list({
              project: params.project,
              environment: e.environment.name,
            }),
          ),
        ),
      );
    },
    component: ProjectLogDrains,
  },
);

function ProjectLogDrains() {
  const { project } = Route.useParams();
  const { environments } = useProjectOverview(project);
  return (
    <Page gap="gap-6">
      <p className="text-mut">
        Drains send every service's output in an environment to another log
        store. Without one, output stays on the server, in{" "}
        <span className="font-mono">dock logs</span>.
      </p>
      {environments.map((e) => (
        <LogDrains
          key={e.environment.id}
          env={{ project, environment: e.environment.name }}
        />
      ))}
    </Page>
  );
}
