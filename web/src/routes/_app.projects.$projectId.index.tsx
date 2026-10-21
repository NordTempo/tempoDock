import { createFileRoute, Link } from "@tanstack/react-router";
import { queries, useProjectOverview } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { EnvironmentTable } from "../features/overview/EnvironmentTable";
import { Problems } from "../features/overview/Problems";
import { TopBar } from "../shell/TopBar";
import { Heading } from "../ui/kit";

export const Route = createFileRoute("/_app/projects/$projectId/")({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.overview(params.projectId)),
  component: Overview,
});

const topAction = "text-dim hover:text-white";

function Overview() {
  const { projectId } = Route.useParams();
  const { project, environments, problems, recent } =
    useProjectOverview(projectId);
  const serviceCount = environments.reduce((n, e) => n + e.services.length, 0);
  const firstEnv = environments[0]?.name;

  return (
    <>
      <TopBar
        actions={
          <>
            <Link
              to="/audit"
              search={{ project: projectId }}
              className={topAction}
            >
              Audit
            </Link>
            <Link
              to="/projects/$projectId/variables"
              params={{ projectId }}
              search={{ env: firstEnv }}
              className={topAction}
            >
              Variables
            </Link>
          </>
        }
      >
        <span className="font-semibold">{project.name}</span>
        <span className="text-dim">
          {serviceCount} services · {environments.length} environments
        </span>
      </TopBar>

      <Problems problems={problems} />

      <div className="px-5">
        {environments.map((env) => (
          <section key={env.id}>
            <Heading
              note={`${env.server.name} · ${env.production ? env.server.address : "non-production"}`}
            >
              {env.name}
            </Heading>
            {env.services.length > 0 ? (
              <EnvironmentTable services={env.services} />
            ) : (
              <p className="text-dim">No services.</p>
            )}
          </section>
        ))}

        <Heading>Recent</Heading>
        <AuditTable events={recent} />
      </div>
    </>
  );
}
