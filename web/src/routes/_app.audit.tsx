import { createFileRoute, Link } from "@tanstack/react-router";
import { queries, useAuditLog, useProjects } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { TopBar } from "../shell/TopBar";

export const Route = createFileRoute("/_app/audit")({
  validateSearch: (search: Record<string, unknown>): { project?: string } =>
    typeof search.project === "string" ? { project: search.project } : {},
  loaderDeps: ({ search }) => ({ project: search.project }),
  loader: ({ context, deps }) =>
    context.queryClient.ensureQueryData(
      queries.audit({ project: deps.project }),
    ),
  component: Audit,
});

function Audit() {
  const { project } = Route.useSearch();
  const projects = useProjects();
  const events = useAuditLog({ project });
  const filters = [undefined, ...projects.map((p) => p.name)];

  return (
    <>
      <TopBar>
        <span className="font-semibold">Audit log</span>
        <span className="flex gap-3.5">
          {filters.map((name) => (
            <Link
              key={name ?? ""}
              to="/audit"
              search={name ? { project: name } : {}}
              className={
                name === project
                  ? "border-b border-white"
                  : "text-dim hover:text-white"
              }
            >
              {name ?? "all"}
            </Link>
          ))}
        </span>
      </TopBar>
      <div className="px-5 pt-3">
        <AuditTable events={events} />
      </div>
    </>
  );
}
