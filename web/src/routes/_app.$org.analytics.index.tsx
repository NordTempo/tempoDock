import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { serviceRef, useProjects, useProjectTree } from "../data/queries";
import {
  type OrgSitesRange,
  orgSitesQuery,
  orgSitesRanges,
  sitesRange,
} from "../features/analytics/loaders";
import {
  SiteRows,
  SiteTiles,
  useSiteView,
  ViewSwitch,
} from "../features/analytics/SiteList";
import { RangeSwitch } from "../features/stats/parts";
import { useOrg } from "../shell/location";
import { Page, PageBar, Panel, SectionHead } from "../ui/kit";

// The Analytics area (ADR 0059, layout C): the organization's sites with
// their visitors and page views over a range (?range=, else the one last
// chosen on this browser, else 24h), as rows by project or as tiles. A
// site opens its page on the same range. Tiles fill the window's width,
// so they line up with the controls on the bar above. Projects that count
// nothing yet are listed too, with the way to turn analytics on: a
// domain's switch on its service's Domains tab.
export const Route = createFileRoute("/_app/$org/analytics/")({
  validateSearch: (s: Record<string, unknown>): { range?: OrgSitesRange } => ({
    range: orgSitesRanges.includes(s.range as OrgSitesRange)
      ? (s.range as OrgSitesRange)
      : undefined,
  }),
  loaderDeps: ({ search: { range } }) => ({
    range: range ?? sitesRange.get(),
  }),
  loader: ({ context: { queryClient }, deps: { range } }) =>
    queryClient.ensureQueryData(orgSitesQuery(range)),
  component: Sites,
});

function Sites() {
  const { range = sitesRange.get() } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const [view, setView] = useSiteView();
  const sites = useSuspenseQuery(orgSitesQuery(range)).data;
  return (
    <>
      <PageBar
        title="Analytics"
        actions={
          sites.length > 0 && (
            <>
              <RangeSwitch
                value={range}
                options={orgSitesRanges}
                onChange={(r) => {
                  sitesRange.set(r);
                  navigate({ search: { range: r } });
                }}
              />
              <ViewSwitch value={view} onChange={setView} />
            </>
          )
        }
      />
      <Page width={view === "tiles" ? "max-w-none" : undefined}>
        {sites.length === 0 ? (
          <p className="text-mut">
            No site counts visits yet. Turn counting on for a service in its
            Settings › Analytics, or with{" "}
            <code className="font-mono text-fg">
              dock analytics enable -s &lt;service&gt;
            </code>
            .
          </p>
        ) : view === "tiles" ? (
          <SiteTiles sites={sites} range={range} />
        ) : (
          <SiteRows sites={sites} range={range} />
        )}
        <WithoutAnalytics counted={new Set(sites.map((s) => s.project))} />
      </Page>
    </>
  );
}

/**
 * Projects with no site: analytics is off for them. Each links to the
 * Analytics section of its first service with a domain of its own
 * (generated ones count too): a service without one has nothing to count. The table keeps the default page
 * width (1400px less its padding) when the tiles above run wider.
 */
function WithoutAnalytics({ counted }: { counted: Set<string> }) {
  const org = useOrg();
  const overviews = new Map(
    useProjectTree().map((o) => [o.project.name, o.environments ?? []]),
  );
  const off = useProjects().filter((p) => !counted.has(p.name));
  if (off.length === 0) return null;
  return (
    <section aria-labelledby="off" className="max-w-[1344px]">
      <SectionHead id="off">Without analytics</SectionHead>
      <Panel>
        <table>
          <tbody>
            {off.map((p) => {
              const services = (overviews.get(p.name) ?? []).flatMap(
                (e) => e.services ?? [],
              );
              const withDomain =
                services.find((s) => s.domains?.some((d) => !d.generated)) ??
                services.find((s) => s.domains?.length);
              return (
                <tr key={p.id}>
                  <td className="w-[45%]">{p.name}</td>
                  <td className="text-mut">Off</td>
                  <td className="text-right">
                    {withDomain ? (
                      <Link
                        to="/$org/$project/$environment/$service/settings"
                        params={{ org, ...serviceRef(withDomain.service) }}
                        hash="analytics"
                        className="btn sm"
                        title={`Opens Settings › Analytics of ${withDomain.service.name}, ${p.name}'s service with a domain; turn counting on there`}
                      >
                        Turn on for {p.name}
                      </Link>
                    ) : (
                      <span className="text-xs text-mut">
                        Needs a domain first:{" "}
                        <Link
                          to="/$org/$project"
                          params={{ org, project: p.name }}
                          className="underline underline-offset-3"
                        >
                          open {p.name}
                        </Link>
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Panel>
    </section>
  );
}
