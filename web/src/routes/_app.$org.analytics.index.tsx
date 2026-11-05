import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { orgSitesQuery } from "../features/analytics/loaders";
import { count } from "../features/analytics/parts";
import { useOrg } from "../shell/location";
import { Page, PageBar, Panel, SectionHead } from "../ui/kit";

// The Analytics area (ADR 0059, layout C): the organization's sites,
// grouped by project, each with its last 24 hours. A site opens its page.
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
      </Page>
    </>
  );
}
