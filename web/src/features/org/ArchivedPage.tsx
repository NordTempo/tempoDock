import { useQuery } from "@tanstack/react-query";
import { queries, useIsAdmin } from "../../data/queries";
import { ago } from "../../ui/format";
import { ErrorText, Page, Panel } from "../../ui/kit";
import { ArchivedActions } from "../overview/ArchivedActions";
import { OrgSettingsBar } from "./SettingsBar";

/**
 * Settings › Archived projects (`dock project list --all`): each with
 * Rename and Delete permanently (ADR 0078), the way to free an archived
 * project's name. Moved here from the project tree's foot, so archived
 * projects have one home. Admins only; the API refuses everyone else.
 */
export function ArchivedPage() {
  const admin = useIsAdmin();
  const archived = useQuery({
    ...queries.archivedProjects(),
    enabled: admin,
  });
  return (
    <>
      <OrgSettingsBar />
      <Page>
        {!admin ? (
          <p className="text-mut">
            Only an admin of the organization can see archived projects.
          </p>
        ) : archived.error ? (
          <ErrorText error={archived.error} />
        ) : !archived.data ? (
          <p className="text-mut">Loading.</p>
        ) : archived.data.length === 0 ? (
          <p className="text-mut">
            None. Archiving a project (its Settings tab) stops its services and
            keeps its volumes and history; it shows up here.
          </p>
        ) : (
          <Panel>
            <table>
              <tbody>
                {archived.data.map((p) => (
                  <tr key={p.id}>
                    <td className="py-2.5 font-mono">{p.name}</td>
                    <td className="py-2.5 text-mut" title={p.archived_at}>
                      {p.archived_at && `archived ${ago(p.archived_at)}`}
                    </td>
                    <td className="py-1.5">
                      <div className="flex justify-end">
                        <ArchivedActions project={p} />
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        )}
      </Page>
    </>
  );
}
