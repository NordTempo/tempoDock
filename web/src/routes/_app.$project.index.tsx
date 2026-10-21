import { createFileRoute, Link } from "@tanstack/react-router";
import {
  queries,
  serviceRef,
  useAuditLog,
  useProjectOverview,
  useServers,
} from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { EnvironmentTable } from "../features/overview/EnvironmentTable";
import { Problems } from "../features/overview/Problems";
import { TopBar } from "../shell/TopBar";
import { Heading } from "../ui/kit";

export const Route = createFileRoute("/_app/$project/")({
  loader: async ({ context: { queryClient }, params }) => {
    const [overview] = await Promise.all([
      queryClient.ensureQueryData(queries.overview(params.project)),
      queryClient.ensureQueryData(queries.audit({ project: params.project })),
    ]);
    // Problems read each service's latest release.
    await Promise.all(
      overview.environments
        .flatMap((e) => e.services)
        .map((s) =>
          queryClient.ensureQueryData(queries.releases(serviceRef(s.service))),
        ),
    );
  },
  component: Overview,
});

const topAction = "text-dim hover:text-white";

function Overview() {
  const { project } = Route.useParams();
  const { environments } = useProjectOverview(project);
  const servers = useServers();
  const recent = useAuditLog({ project }).slice(0, 10);
  const serviceCount = environments.reduce((n, e) => n + e.services.length, 0);
  const role = (server: string) =>
    servers.find((s) => s.name === server)?.role ?? "unknown role";

  return (
    <>
      <TopBar
        actions={
          <Link to="/audit" search={{ project }} className={topAction}>
            Audit
          </Link>
        }
      >
        <span className="font-semibold">{project}</span>
        <span className="text-dim">
          {serviceCount} services · {environments.length} environments
        </span>
      </TopBar>

      <Problems project={project} />

      <div className="px-5">
        {environments.map(({ environment, services }) => (
          <section key={environment.id}>
            <Heading
              note={`${environment.server} · ${role(environment.server)}`}
              actions={
                <Link
                  to="/$project/$environment"
                  params={{ project, environment: environment.name }}
                  className={topAction}
                >
                  Shared variables
                </Link>
              }
            >
              {environment.name}
            </Heading>
            {services.length > 0 ? (
              <EnvironmentTable services={services} />
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
