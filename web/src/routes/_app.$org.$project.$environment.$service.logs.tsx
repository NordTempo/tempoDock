import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { servicePath, useService } from "../data/queries";
import {
  type LogFilter,
  LogsPane,
  noFilter,
} from "../features/service/LogsPane";

type LogSearch = {
  release?: number;
  build?: boolean;
  level?: "warn" | "error";
  q?: string;
  /** Present (false) only when following is off. */
  follow?: boolean;
  /** Backlog lines, when not the default 500; 0 for all. */
  tail?: number;
  since?: string;
};

// Full-height logs. Filters live in the URL so a filtered view can be shared.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/logs",
)({
  validateSearch: (search: Record<string, unknown>): LogSearch => ({
    release:
      typeof search.release === "number" && search.release > 0
        ? search.release
        : undefined,
    build: search.build === true ? true : undefined,
    level:
      search.level === "warn" || search.level === "error"
        ? search.level
        : undefined,
    q: typeof search.q === "string" && search.q ? search.q : undefined,
    follow: search.follow === false ? false : undefined,
    tail:
      typeof search.tail === "number" &&
      Number.isInteger(search.tail) &&
      search.tail >= 0
        ? search.tail
        : undefined,
    since:
      typeof search.since === "string" && search.since
        ? search.since
        : undefined,
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
    level: search.level ?? null,
    query: search.q ?? "",
    follow: search.follow ?? true,
    tail: search.tail ?? noFilter.tail,
    since: search.since ?? "",
  };

  // The same view from a terminal.
  const cli = [
    "dock logs",
    filter.build && "--build",
    filter.release && `-r ${filter.release}`,
    !filter.build && `-f -n ${filter.tail}`,
    !filter.build && filter.since && `--since ${filter.since}`,
    `-s ${ref.service} -e ${ref.environment}`,
  ]
    .filter(Boolean)
    .join(" ");

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
            level: next.level ?? undefined,
            q: next.query || undefined,
            follow: next.follow ? undefined : false,
            tail: next.tail === noFilter.tail ? undefined : next.tail,
            since: next.since || undefined,
          },
          replace: true,
          // The window scrolls the log: a filter change keeps its place.
          resetScroll: false,
        })
      }
      cli={cli}
    />
  );
}
