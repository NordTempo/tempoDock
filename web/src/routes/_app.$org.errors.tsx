import { createFileRoute } from "@tanstack/react-router";
import { type ErrorFilter, errorsQuery } from "../features/errors/data";
import { ErrorsView } from "../features/errors/ErrorsView";
import { Page, PageBar } from "../ui/kit";

// The org's errors (ADR 0062): open alerts, then error issues from every
// service grouped by service, with a tree of projects and services.
// /acme/errors?project=shop&environment=production&service=web&all=1
const text = (v: unknown) => (typeof v === "string" && v ? v : undefined);

export const Route = createFileRoute("/_app/$org/errors")({
  validateSearch: (s: Record<string, unknown>): ErrorFilter => ({
    project: text(s.project),
    environment: text(s.environment),
    service: text(s.service),
    all: s.all === true || s.all === "1" || undefined,
  }),
  loaderDeps: ({ search }) => search,
  loader: ({ context: { queryClient }, deps }) =>
    queryClient.ensureQueryData(errorsQuery(deps)),
  component: Errors,
});

function Errors() {
  const filter = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <>
      <PageBar
        title="Errors"
        after={
          filter.environment && (
            <span className="text-sm text-mut">{filter.environment}</span>
          )
        }
      />
      <Page>
        <ErrorsView
          filter={filter}
          onFilter={(next) => navigate({ search: next, replace: true })}
        />
      </Page>
    </>
  );
}
