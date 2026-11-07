import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { serviceRef, useProjects, useProjectTree } from "../data/queries";
import {
  type OrgSitesRange,
  orgSitesQuery,
  orgSitesRanges,
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
// their visitors and page views over a range (24h unless ?range= says
// otherwise), as rows by project or as tiles. A site opens its page on
// the same range. Projects that count nothing yet are listed too, with
// the way to turn analytics on: a domain's switch on its service's
// Domains tab.
export const Route = createFileRoute("/_app/$org/analytics/")({
  validateSearch: (s: Record<string, unknown>): { range?: OrgSitesRange } =>
    orgSitesRanges.includes(s.range as OrgSitesRange)
      ? { range: s.range as OrgSitesRange }
      : {},
  loaderDeps: ({ search: { range } }) => ({ range }),
  loader: ({ context: { queryClient }, deps: { range } }) =>
    queryClient.ensureQueryData(orgSitesQuery(range)),
  component: Sites,
});

function Sites() {
  const { range = "24h" } = Route.useSearch();
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
                onChange={(r) =>
                  navigate({ search: r === "24h" ? {} : { range: r } })
                }
              />
              <ViewSwitch value={view} onChange={setView} />
            </>
          )
        }
      />
      <Page>
        {sites.length === 0 ? (
          <p className="text-mut">
            No site counts visits yet. Turn counting on for a domain on its
            service's Domains tab, or with{" "}
            <code className="font-mono text-fg">
              dock analytics enable &lt;hostname&gt;
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
 * Projects with no site: analytics is off for them. Turning it on is a
 * domain's switch, so each links to the first service with a domain of
 * its own (generated ones count too).
 */
function WithoutAnalytics({ counted }: { counted: Set<string> }) {
  const org = useOrg();
  const overviews = new Map(
    useProjectTree().map((o) => [o.project.name, o.environments ?? []]),
  );
  const off = useProjects().filter((p) => !counted.has(p.name));
  if (off.length === 0) return null;
  return (
    <section aria-labelledby="off">
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
                        to="/$org/$project/$environment/$service/domains"
                        params={{ org, ...serviceRef(withDomain.service) }}
                        className="btn sm"
                        title={`Opens the domains of ${withDomain.service.name}, ${p.name}'s service with a domain; turn counting on there`}
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
