import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { serviceRef, useProjects, useProjectTree } from "../data/queries";
import { orgSitesQuery } from "../features/analytics/loaders";
import { count } from "../features/analytics/parts";
import { useOrg } from "../shell/location";
import { Page, PageBar, Panel, SectionHead } from "../ui/kit";

// The Analytics area (ADR 0059, layout C): the organization's sites,
// grouped by project, each with its last 24 hours. A site opens its page
// on the same range. Projects that count nothing yet are listed too, with
// the way to turn analytics on: a domain's switch on its service's
// Domains tab.
export const Route = createFileRoute("/_app/$org/analytics/")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(orgSitesQuery()),
  component: Sites,
});

function Sites() {
  const org = useOrg();
  const sites = useSuspenseQuery(orgSitesQuery()).data;
  // By project, in the API's order.
  const projects = new Map<string, typeof sites>();
  for (const s of sites)
    projects.set(s.project, [...(projects.get(s.project) ?? []), s]);
  return (
    <>
      <PageBar
        title="Analytics"
        after={<span className="text-xs text-faint">last 24 hours</span>}
      />
      <Page>
        {sites.length === 0 && (
          <p className="text-mut">
            No site counts visits yet. Turn counting on for a domain on its
            service's Domains tab, or with{" "}
            <code className="font-mono text-fg">
              dock analytics enable &lt;hostname&gt;
            </code>
            .
          </p>
        )}
        {[...projects].map(([project, rows]) => (
          <section key={project} aria-labelledby={`p-${project}`}>
            <SectionHead id={`p-${project}`}>{project}</SectionHead>
            <Panel>
              <table className="tabular-nums">
                <thead>
                  <tr>
                    <th>Site</th>
                    <th className="text-right">Visitors</th>
                    <th className="text-right">Page views</th>
                    <th>Services</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map(({ site, last_24h: day }) => (
                    <tr key={site.name}>
                      <td className="w-[45%]">
                        <Link
                          to="/$org/analytics/$project/$site"
                          params={{ org, project, site: site.name }}
                          search={{ range: "24h" }}
                          className="font-mono text-fg hover:underline"
                        >
                          {site.name}
                        </Link>{" "}
                        <span className="text-xs text-faint">
                          {site.environment}
                        </span>
                      </td>
                      <td className="text-right">
                        {day ? count(day.visitors) : "–"}
                      </td>
                      <td className="text-right">
                        {day ? count(day.pageviews) : "–"}
                      </td>
                      <td className="text-mut">
                        {[
                          ...new Set(
                            (site.hostnames ?? []).flatMap(
                              (h) => h.service || [],
                            ),
                          ),
                        ].join(", ") || "–"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Panel>
          </section>
        ))}
        <WithoutAnalytics counted={new Set(projects.keys())} />
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
                        title="Turn counting on for a domain"
                      >
                        Turn on for {withDomain.service.name}
                      </Link>
                    ) : (
                      <span className="text-xs text-faint">
                        Add a domain to a service first
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
