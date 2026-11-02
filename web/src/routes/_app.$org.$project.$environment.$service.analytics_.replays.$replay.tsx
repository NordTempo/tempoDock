import { createFileRoute } from "@tanstack/react-router";
import { ReplayPlayer } from "../features/analytics/ReplayPlayer";

// A session replay (ADR 0037, phase A3), inside the service's tabs:
// /shop/production/web/analytics/replays/9f3c0a1b2c3d4e5f?site=shop.example.com&t=146
// t is seconds into the replay; at a moment (Unix ms), as an error's link
// gives it, which opens a few seconds before.
type Search = { site: string; t?: number; at?: number };

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics_/replays/$replay",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    site: typeof s.site === "string" ? s.site : "",
    ...(typeof s.t === "number" && s.t >= 0 && { t: s.t }),
    ...(typeof s.at === "number" && s.at > 0 && { at: s.at }),
  }),
  component: Replay,
});

function Replay() {
  const params = Route.useParams();
  const { site, t, at } = Route.useSearch();
  return (
    <ReplayPlayer
      project={params.project}
      site={site}
      replay={params.replay}
      at={t}
      atTime={at}
      serviceLink={{
        to: "/$org/$project/$environment/$service/analytics",
        params: {
          org: params.org,
          project: params.project,
          environment: params.environment,
          service: params.service,
        },
      }}
    />
  );
}
