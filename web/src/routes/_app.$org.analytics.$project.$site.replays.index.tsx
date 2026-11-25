import { createFileRoute } from "@tanstack/react-router";
import {
  type AnalyticsSearch,
  analyticsSearch,
  devices,
} from "../features/analytics/loaders";
import { ReplaysView } from "../features/analytics/ReplaysView";
import { useSiteView } from "../features/analytics/SitePage";

// A site's session replays (ADR 0037): …/replays?errors=true&device=mobile.
type Search = AnalyticsSearch & { errors?: boolean; device?: string };

export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/replays/",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    errors: s.errors === true ? true : undefined,
    device: devices.includes(s.device as string)
      ? (s.device as string)
      : undefined,
  }),
  component: Replays,
});

function Replays() {
  const { project, site, range, filters } = useSiteView();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <ReplaysView
      project={project}
      site={site}
      range={range}
      filters={filters}
      errorsOnly={!!search.errors}
      device={search.device}
      onErrorsOnly={(on) =>
        navigate({
          search: (s) => ({ ...s, errors: on || undefined }),
          replace: true,
        })
      }
      onDevice={(device) =>
        navigate({ search: (s) => ({ ...s, device }), replace: true })
      }
    />
  );
}
