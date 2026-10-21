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
      queries.audit({ projectId: deps.project }),
    ),
  component: Audit,
});

function Audit() {
  const { project } = Route.useSearch();
  const projects = useProjects();
  const events = useAuditLog({ projectId: project });
  const filters = [{ id: undefined, name: "all" }, ...projects];

  return (
    <>
      <TopBar>
        <span className="font-semibold">Audit log</span>
        <span className="flex gap-3.5">
          {filters.map((p) => (
            <Link
              key={p.name}
              to="/audit"
              search={p.id ? { project: p.id } : {}}
              className={
                p.id === project
                  ? "border-b border-white"
                  : "text-dim hover:text-white"
              }
            >
              {p.name}
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
