import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { errorsQuery } from "../features/errors/data";
import { ErrorsView } from "../features/errors/ErrorsView";

// A service's Errors tab (ADR 0062): the org's errors view fixed to this
// service: its open alerts, then its issues from the browser, its logs
// and 5xx spikes. …/web/errors?all=1 shows resolved ones too.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/errors",
)({
  validateSearch: (s: Record<string, unknown>): { all?: boolean } => ({
    all: s.all === true || s.all === "1" || undefined,
  }),
  loaderDeps: ({ search }) => search,
  loader: ({ context: { queryClient }, params, deps }) =>
    queryClient.ensureQueryData(
      errorsQuery({ ...servicePath(params), all: deps.all }),
    ),
  component: ErrorsTab,
});

function ErrorsTab() {
  const ref = servicePath(Route.useParams());
  const { all } = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <ErrorsView
      fixed
      filter={{ ...ref, all }}
      onFilter={(next) =>
        navigate({ search: { all: next.all || undefined }, replace: true })
      }
    />
  );
}
