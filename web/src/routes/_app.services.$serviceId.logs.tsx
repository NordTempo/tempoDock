import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { queries, useService } from "../data/queries";
import { type LogFilter, LogsPane } from "../features/service/LogsPane";

type LogSearch = { release?: number | "build"; warn?: boolean; q?: string };

// Full-height logs. Filters live in the URL so a filtered view can be shared.
export const Route = createFileRoute("/_app/services/$serviceId/logs")({
  validateSearch: (search: Record<string, unknown>): LogSearch => ({
    release:
      search.release === "build" || typeof search.release === "number"
        ? search.release
        : undefined,
    warn: search.warn === true ? true : undefined,
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.logs(params.serviceId)),
  component: LogsTab,
});

function LogsTab() {
  const { serviceId } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const service = useService(serviceId);
  const filter: LogFilter = {
    release: search.release ?? "all",
    warnUp: search.warn ?? false,
    query: search.q ?? "",
  };

  return (
    <div className="px-5 pt-3">
      <LogsPane
        serviceId={serviceId}
        inFlight={
          service.state.state === "deploying" ? service.state.release : null
        }
        filter={filter}
        onFilter={(next) =>
          navigate({
            search: {
              release: next.release === "all" ? undefined : next.release,
              warn: next.warnUp || undefined,
              q: next.query || undefined,
            },
            replace: true,
          })
        }
        className="h-[calc(100vh-190px)]"
      />
    </div>
  );
}
