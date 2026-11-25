import { createFileRoute } from "@tanstack/react-router";
import {
  type AnalyticsSearch,
  analyticsSearch,
  devices,
  type Vital,
  vitals,
} from "../features/analytics/loaders";
import { useSiteView } from "../features/analytics/SitePage";
import { VitalsView } from "../features/analytics/VitalsView";

// A site's Web Vitals (ADR 0037): …/vitals?m=inp&device=mobile, m the
// vital charted (LCP if none), device the devices shown.
type Search = AnalyticsSearch & { m?: Vital; device?: string };

export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/vitals",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    m: vitals.includes(s.m as Vital) ? (s.m as Vital) : undefined,
    device: devices.includes(s.device as string)
      ? (s.device as string)
      : undefined,
  }),
  component: Vitals,
});

function Vitals() {
  const { project, site, range, filters } = useSiteView();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <VitalsView
      project={project}
      site={site.name}
      range={range}
      filters={filters}
      metric={search.m ?? "lcp"}
      device={search.device}
      onMetric={(m) =>
        navigate({
          search: (s) => ({ ...s, m: m === "lcp" ? undefined : m }),
          replace: true,
        })
      }
      onDevice={(device) =>
        navigate({ search: (s) => ({ ...s, device }), replace: true })
      }
    />
  );
}
