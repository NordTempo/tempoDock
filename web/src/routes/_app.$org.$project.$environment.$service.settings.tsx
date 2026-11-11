import { createFileRoute } from "@tanstack/react-router";
import { findService, queries, servicePath, useService } from "../data/queries";
import { sitesQuery } from "../features/analytics/loaders";
import { Hostnames } from "../features/analytics/ServiceSite";
import { BackupsPanel } from "../features/backups/BackupsPanel";
import { snapshotsQuery } from "../features/backups/data";
import { DomainsPanel } from "../features/service/DomainsPanel";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import {
  isTextSettingKey,
  type TextSettingKey,
} from "../features/settings/settings";

// A service's Settings tab (ADR 0076): everything that configures it on
// one page, with an index. How it builds and runs (#set-image, #set-run,
// #set-release), its domains (#domains) and, for a stateful service, its
// backups and snapshots (#backups). ?edit=port opens that setting's
// editor, for links that point at a fix (a failed release).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings",
)({
  validateSearch: (s: Record<string, unknown>): { edit?: TextSettingKey } =>
    typeof s.edit === "string" && isTextSettingKey(s.edit)
      ? { edit: s.edit }
      : {},
  loader: async ({ context: { queryClient }, params }) => {
    const ref = servicePath(params);
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    await Promise.all([
      queryClient.ensureQueryData(sitesQuery(params.project)),
      ...(findService(overview, params)?.service.stateful
        ? [
            queryClient.ensureQueryData(queries.backups(ref)),
            queryClient.ensureQueryData(snapshotsQuery(ref)),
          ]
        : []),
    ]);
  },
  component: SettingsTab,
});

function SettingsTab() {
  const ref = servicePath(Route.useParams());
  const { service } = useService(ref);
  const index = [
    ["#set-image", "Image and build"],
    ["#set-run", "Running"],
    ["#set-release", "Going live"],
    ["#domains", "Domains"],
    ...(service.stateful ? [["#backups", "Restore points"] as const] : []),
  ] as const;
  return (
    <div className="grid gap-8 xl:grid-cols-[minmax(0,1fr)_168px]">
      <div className="flex min-w-0 flex-col gap-8">
        <SettingsPanel serviceRef={ref} edit={Route.useSearch().edit} />
        <div className="flex flex-col gap-6">
          <DomainsPanel serviceRef={ref} />
          <Hostnames serviceRef={ref} />
        </div>
        {service.stateful && (
          <div id="backups">
            <BackupsPanel serviceRef={ref} />
          </div>
        )}
      </div>
      {/* Where each part is, beside it while scrolling. */}
      <nav
        aria-label="On this page"
        className="sticky top-[124px] hidden flex-col gap-0.5 self-start text-[13px] xl:flex"
      >
        {index.map(([href, label]) => (
          <a
            key={href}
            href={href}
            className="flex min-h-[24px] items-center rounded-ctl px-2 text-mut hover:bg-hover hover:text-fg"
          >
            {label}
          </a>
        ))}
      </nav>
    </div>
  );
}
