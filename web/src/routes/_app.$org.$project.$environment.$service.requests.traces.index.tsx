import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { type TraceFilter, traceRanges, tracesQuery } from "../data/traces";
import { TracesView } from "../features/traces/TracesView";

// Requests' Traces view (ADR 0075): the service's traces, opening in
// place. The filter is the address, so a list can be shared.
// /shop/production/web/requests/traces?range=7d&slowest=1&errors=1&release=42
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/requests/traces/",
)({
  validateSearch: (s: Record<string, unknown>): TraceFilter => ({
    range: traceRanges.find((r) => r === s.range),
    slowest: s.slowest === 1 || s.slowest === "1" || undefined,
    errors: s.errors === 1 || s.errors === "1" || undefined,
    route: typeof s.route === "string" && s.route ? s.route : undefined,
    release:
      typeof s.release === "number" && s.release > 0 ? s.release : undefined,
  }),
  loaderDeps: ({ search }) => search,
  loader: ({ context: { queryClient }, params, deps }) =>
    queryClient.ensureQueryData(tracesQuery(servicePath(params), deps)),
  component: Traces,
});

function Traces() {
  const ref = servicePath(Route.useParams());
  const filter = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <TracesView
      serviceRef={ref}
      filter={filter}
      onFilter={(f) =>
        navigate({
          search: {
            range: f.range === "24h" ? undefined : f.range,
            slowest: f.slowest || undefined,
            errors: f.errors || undefined,
            route: f.route || undefined,
            release: f.release || undefined,
          },
          replace: true,
        })
      }
    />
  );
}
