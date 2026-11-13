import { createFileRoute } from "@tanstack/react-router";
import {
  type AnalyticsSearch,
  analyticsSearch,
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
    ...(s.errors === true && { errors: true }),
    ...(["desktop", "mobile", "tablet"].includes(s.device as string) && {
      device: s.device as string,
    }),
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
