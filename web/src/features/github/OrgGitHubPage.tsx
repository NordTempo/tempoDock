import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useIsAdmin } from "../../data/queries";
import { Button, Confirm, ErrorText, Page } from "../../ui/kit";
import { OrgSettingsBar } from "../org/SettingsBar";
import {
  type GitHubInstallation,
  githubOrgQuery,
  useAssign,
  useInstallForOrg,
} from "./data";

// GitHub in the org's settings (ADR 0062): the platform's App stays the
// platform's; the org installs it on its own GitHub accounts and manages
// those installations. Services connect repositories from them.

export function OrgGitHubPage() {
  const { data } = useSuspenseQuery(githubOrgQuery());
  const admin = useIsAdmin();
  const install = useInstallForOrg();
  const installations = data.installations ?? [];
  return (
    <>
      <OrgSettingsBar
        actions={
          admin &&
          data.app && (
            <Button
              primary
              disabled={install.isPending}
              onClick={() => install.mutate()}
            >
              Install on GitHub
            </Button>
          )
        }
      />
      <Page>
        {data.app ? (
          <p className="max-w-2xl text-[13px] text-sec">
            Dock's GitHub App is{" "}
            <a href={data.app.url} target="_blank" rel="noreferrer">
              {data.app.name}
            </a>{" "}
            (owned by {data.app.owner}). Install it on the GitHub accounts whose
            repositories this organization deploys; services then connect a
            repository from them, and pushes deploy.
          </p>
        ) : (
          <p className="text-mut">
            The platform has no GitHub App yet; ask a platform operator to
            create one. Until then services connect repositories with a token.
          </p>
        )}
        <ErrorText error={install.error} />
        {data.installations_error && (
          <p className="text-xs text-warn">{data.installations_error}</p>
        )}
        {installations.length > 0 ? (
          <div className="overflow-x-auto border-t border-line">
            <table>
              <thead>
                <tr>
                  <th>Account</th>
                  <th>Repositories</th>
                  <th>Used by</th>
                  <th>State</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {installations.map((i) => (
                  <InstallationRow key={i.id} installation={i} admin={admin} />
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          data.app && (
            <p className="text-mut">Not installed on any account yet.</p>
          )
        )}
      </Page>
    </>
  );
}

function InstallationRow({
  installation: i,
  admin,
}: {
  installation: GitHubInstallation;
  admin: boolean;
}) {
  const unassign = useAssign();
  const [asking, setAsking] = useState(false);
  return (
    <tr>
      <td className="py-2 font-mono">
        {i.url ? (
          <a href={i.url} target="_blank" rel="noreferrer">
            {i.account}
          </a>
        ) : (
          i.account
        )}
      </td>
      <td className="py-2 text-mut">{i.repositories ?? "·"}</td>
      <td className="py-2">
        {i.connections} {i.connections === 1 ? "service" : "services"}
      </td>
      <td className="py-2">
        {i.missing ? (
          <span className="text-fail">removed on GitHub</span>
        ) : i.suspended ? (
          <span className="text-warn">suspended on GitHub</span>
        ) : (
          <span className="text-ok">installed</span>
        )}
      </td>
      <td className="w-full py-1.5 text-right">
        {admin && (
          <Button small onClick={() => setAsking(true)}>
            Remove
          </Button>
        )}
        {asking && (
          <Confirm
            danger
            action="Remove"
            title={`Remove the installation on ${i.account}?`}
            pending={unassign.isPending}
            error={unassign.error}
            onConfirm={() =>
              unassign.mutate(
                { id: i.id, org: "" },
                { onSuccess: () => setAsking(false) },
              )
            }
            onCancel={() => setAsking(false)}
          >
            This organization stops using it. Services connected through it stop
            deploying on push until they connect another way. Uninstall the App
            on GitHub to remove it there too.
          </Confirm>
        )}
      </td>
    </tr>
  );
}
