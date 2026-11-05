import { createFileRoute } from "@tanstack/react-router";
import type { AnalyticsSearch } from "../features/analytics/data";
import { analyticsSearch } from "../features/analytics/loaders";
import { ReplayPlayer } from "../features/analytics/ReplayPlayer";

// A session replay (ADR 0037, phase A3), inside its site's page:
// /nordtempo/analytics/shop/shop.example.com/replays/9f3c0a1b2c3d4e5f?t=146
// t is seconds into the replay; at a moment (Unix ms), as an error's link
// gives it, which opens a few seconds before. The list's range, filters,
// errors and device come along for Previous and Next.
type Search = AnalyticsSearch & {
  t?: number;
  at?: number;
  errors?: boolean;
  device?: string;
};

export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/replays/$replay",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    ...(s.errors === true && { errors: true }),
    ...(["desktop", "mobile", "tablet"].includes(s.device as string) && {
      device: s.device as string,
    }),
    ...(typeof s.t === "number" && s.t >= 0 && { t: s.t }),
    ...(typeof s.at === "number" && s.at > 0 && { at: s.at }),
  }),
  component: Replay,
});

function Replay() {
  const params = Route.useParams();
  const { t, at, ...list } = Route.useSearch();
  return (
    <ReplayPlayer
      project={params.project}
      site={params.site}
      replay={params.replay}
      at={t}
      atTime={at}
      list={list}
    />
  );
}
