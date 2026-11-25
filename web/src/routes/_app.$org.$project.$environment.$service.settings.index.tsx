import { createFileRoute } from "@tanstack/react-router";
import { findService, queries, servicePath } from "../data/queries";
import { sitesQuery } from "../features/analytics/loaders";
import { snapshotsQuery } from "../features/backups/data";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import {
  isTextSettingKey,
  type TextSettingKey,
} from "../features/settings/settings";
import { uptimeChecks } from "../features/uptime/data";

// A service's Settings tab (ADR 0059, ADR 0076, ADR 0099): one page with
// the list of its sections (source, running, deploys, health and uptime,
// domains, backups when stateful, archive). #domains or #backups opens at
// that section. ?edit=port opens that setting's editor, for links that
// point at a fix (a failed release).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings/",
)({
  validateSearch: (s: Record<string, unknown>): { edit?: TextSettingKey } => ({
    edit:
      typeof s.edit === "string" && isTextSettingKey(s.edit)
        ? s.edit
        : undefined,
  }),
  loader: async ({ context: { queryClient }, params }) => {
    const ref = servicePath(params);
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    const stateful = !!findService(overview, params)?.service.stateful;
    await Promise.all([
      queryClient.ensureQueryData(sitesQuery(params.project)),
      queryClient.ensureQueryData(uptimeChecks(ref)),
      ...(stateful
        ? [
            queryClient.ensureQueryData(queries.backups(ref)),
            queryClient.ensureQueryData(snapshotsQuery(ref)),
          ]
        : []),
    ]);
  },
  component: Settings,
});

function Settings() {
  return (
    <SettingsPanel
      serviceRef={servicePath(Route.useParams())}
      edit={Route.useSearch().edit}
    />
  );
}
