import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { servicePath, useService } from "../data/queries";
import { type LogFilter, LogsPane } from "../features/service/LogsPane";

type LogSearch = {
  release?: number;
  build?: boolean;
  errors?: boolean;
  q?: string;
  /** Present (false) only when following is off. */
  follow?: boolean;
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
    errors: search.errors === true ? true : undefined,
    q: typeof search.q === "string" && search.q ? search.q : undefined,
    follow: search.follow === false ? false : undefined,
  }),
  component: LogsTab,
});

function LogsTab() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const { deploying } = useService(ref);
  const navigate = useNavigate({ from: Route.fullPath });
  const filter: LogFilter = {
    release: search.release ?? null,
    build: search.build ?? false,
    errors: search.errors ?? false,
    query: search.q ?? "",
    follow: search.follow ?? true,
  };

  return (
    <LogsPane
      // A fresh stream per deploy step: the container may not exist yet.
      key={`${deploying?.number}:${deploying?.status}`}
      serviceRef={ref}
      filter={filter}
      onFilter={(next) =>
        navigate({
          search: {
            release: next.release ?? undefined,
            build: next.build || undefined,
            errors: next.errors || undefined,
            q: next.query || undefined,
            follow: next.follow ? undefined : false,
          },
          replace: true,
        })
      }
      height="h-[calc(100vh-300px)] min-h-[320px]"
    />
  );
}
