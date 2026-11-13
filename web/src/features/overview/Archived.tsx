import { useSuspenseQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { queries } from "../../data/queries";
import type { EnvironmentView, ServiceView } from "../../data/types";
import { ago, day } from "../../ui/format";
import { Page, PageBar, Panel, SectionHead } from "../../ui/kit";
import { kindLabel } from "../../ui/status";
import { EnvServer } from "../servers/parts";
import { imageLabel } from "../service/releases";
import { ArchivedPageActions } from "./ArchivedActions";
import { Activity } from "./Live";

/** What an archived service ran: its last release's image, or its own. */
const lastImage = ({ service, active_release, latest_release }: ServiceView) =>
  (active_release && imageLabel(active_release)) ||
  (latest_release && imageLabel(latest_release)) ||
  service.image;

/**
 * An environment's archived services on one faint line under its table
 * (`dock service list --all`). They have no pages: stopped, no routes.
 */
export function ArchivedServices({ env }: { env: EnvironmentView }) {
  if (env.archived.length === 0) return null;
  const { name, project } = env.environment;
  return (
    <p className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-1 px-0.5 text-xs text-faint">
      <span title={`dock service list --all -e ${name} -p ${project}`}>
        Archived
      </span>
      {env.archived.map((s) => (
        <span key={s.service.id} title={lastImage(s) || undefined}>
          <span className="font-mono">{s.service.name}</span>
          {s.service.archived_at && ` ${ago(s.service.archived_at)}`}
        </span>
      ))}
    </p>
  );
}

/**
 * An archived project's page, read-only: when it was archived, what each
 * environment held, and its activity, with Rename and Delete permanently
 * for org admins. `children` go at the end (export).
 */
export function ArchivedProject({
  project,
  children,
}: {
  project: string;
  children?: ReactNode;
}) {
  const overview = useSuspenseQuery(queries.overview(project)).data;
  const archivedAt = overview.project.archived_at ?? "";
  return (
    <>
      <PageBar
        title={<span className="text-mut">{project}</span>}
        actions={<ArchivedPageActions project={overview.project} />}
      />
      <Page>
        <p className="text-mut">
          Archived <span title={archivedAt}>{day(archivedAt)}</span> (
          {ago(archivedAt)}). Services are stopped and have no routes; volumes
          and history are kept.
        </p>
        {overview.environments.map(({ environment, archived }) => (
          <section
            key={environment.id}
            aria-labelledby={`env-${environment.id}`}
          >
            <SectionHead
              id={`env-${environment.id}`}
              aside={
                <>
                  {environment.server && (
                    <>
                      on <EnvServer environment={environment} /> ·{" "}
                    </>
                  )}
                  {kindLabel[environment.kind]}
                </>
              }
            >
              {environment.name}
            </SectionHead>
            {archived.length === 0 ? (
              <p className="text-faint">No services.</p>
            ) : (
              <Panel>
                <table className="text-mut">
                  <thead>
                    <tr>
                      <th>Service</th>
                      <th>Image</th>
                      <th>Last release</th>
                      <th>Archived</th>
                    </tr>
                  </thead>
                  <tbody>
                    {archived.map((s) => (
                      <tr key={s.service.id}>
                        <td className="font-mono">{s.service.name}</td>
                        <td className="max-w-[40ch] truncate font-mono text-xs">
                          {lastImage(s) || "none"}
                        </td>
                        <td className="font-mono">
                          {s.latest_release
                            ? `r${s.latest_release.number}`
                            : "none"}
                        </td>
                        <td className="text-xs">
                          {s.service.archived_at && ago(s.service.archived_at)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Panel>
            )}
          </section>
        ))}
        <Activity project={project} />
        {children}
      </Page>
    </>
  );
}
