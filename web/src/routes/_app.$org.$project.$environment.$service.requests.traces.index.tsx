import {
  createFileRoute,
  type ErrorComponentProps,
} from "@tanstack/react-router";
import { ApiError } from "../api/client";
import { servicePath } from "../data/queries";
import {
  type TraceFilter,
  traceRange,
  traceRanges,
  tracesQuery,
} from "../data/traces";
import { TracesView } from "../features/traces/TracesView";
import { RouteError } from "../ui/RouteStates";

// Requests' Traces view (ADR 0075): the service's traces, opening in
// place. The filter is the address, so a list can be shared; with no
// range there, the one last picked on this browser.
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
  loaderDeps: ({ search }) => ({
    ...search,
    range: search.range ?? traceRange.get(),
  }),
  loader: ({ context: { queryClient }, params, deps }) =>
    queryClient.ensureQueryData(tracesQuery(servicePath(params), deps)),
  component: Traces,
  errorComponent: TracesError,
});

/**
 * Tracing turned off on this Dock (the API's 409) is a state, not a
 * failure: say so the way Metrics does, with no retry.
 */
function TracesError(props: ErrorComponentProps) {
  if (!(props.error instanceof ApiError) || props.error.status !== 409)
    return <RouteError {...props} />;
  return (
    <p className="text-mut">
      Tracing is off. An operator turns it on with <code>dock tracing on</code>.
      Apps with an OpenTelemetry SDK then send their traces with their next
      deploy.
    </p>
  );
}

function Traces() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const filter = { ...search, range: search.range ?? traceRange.get() };
  const navigate = Route.useNavigate();
  return (
    <TracesView
      serviceRef={ref}
      filter={filter}
      onFilter={(f) => {
        if (f.range) traceRange.set(f.range);
        navigate({
          search: {
            range: f.range === "24h" ? undefined : f.range,
            slowest: f.slowest || undefined,
            errors: f.errors || undefined,
            route: f.route || undefined,
            release: f.release || undefined,
          },
          replace: true,
        });
      }}
    />
  );
}
