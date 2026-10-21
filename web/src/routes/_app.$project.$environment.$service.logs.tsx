import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { type LogFilter, LogsPane } from "../features/service/LogsPane";

type LogSearch = {
  release?: number;
  build?: boolean;
  stderr?: boolean;
  q?: string;
};

// Full-height logs. Filters live in the URL so a filtered view can be shared.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/logs",
)({
  validateSearch: (search: Record<string, unknown>): LogSearch => ({
    release:
      typeof search.release === "number" && search.release > 0
        ? search.release
        : undefined,
    build: search.build === true ? true : undefined,
    stderr: search.stderr === true ? true : undefined,
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
  component: LogsTab,
});

function LogsTab() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const filter: LogFilter = {
    release: search.release ?? null,
    build: search.build ?? false,
    stderr: search.stderr ?? false,
    query: search.q ?? "",
  };

  return (
    <div className="px-5 pt-3">
      <LogsPane
        serviceRef={ref}
        filter={filter}
        onFilter={(next) =>
          navigate({
            search: {
              release: next.release ?? undefined,
              build: next.build || undefined,
              stderr: next.stderr || undefined,
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
