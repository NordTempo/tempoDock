import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useIsAdmin } from "../../data/queries";
import { Act, Button, Confirm, ErrorText, Page, Panel } from "../../ui/kit";
import { OrgSettingsBar } from "../org/SettingsBar";
import {
  type GitHubAppStatus,
  type GitHubInstallation,
  githubOrgQuery,
  useAssign,
  useCreateApp,
  useInstall,
  useRemoveApp,
} from "./data";

// GitHub in the org's settings (ADR 0062, 0105; mock O-A): one table of
// the Apps the org deploys through, each with its installations under it.
// The org's own Apps are its admins' to create, install and remove; their
// installations on the owner account are the org's. The platform's Apps
// stay the platform's: the org installs them on its own GitHub accounts
// and manages those installations. Services connect repositories from
// any of them, the org's own first.

export function OrgGitHubPage() {
  const { data } = useSuspenseQuery(githubOrgQuery());
  const admin = useIsAdmin();
  const [creating, setCreating] = useState(false);
  const apps = data.apps ?? [];
  return (
    <>
      <OrgSettingsBar
        actions={
          admin && (
            <Button
              primary
              disabled={creating}
              onClick={() => setCreating(true)}
            >
              Create App
            </Button>
          )
        }
      />
      <Page>
        {creating && <CreateForm onCancel={() => setCreating(false)} />}
        {apps.length === 0 ? (
          !creating && (
            <p className="text-mut">
              No GitHub App yet. Create the organization's own, or ask a
              platform operator for one. Until then services connect
              repositories with a token.
            </p>
          )
        ) : (
          <div className="overflow-x-auto border-t border-line">
            <table>
              <thead>
                <tr>
                  <th>App / account</th>
                  <th>Owner</th>
                  <th>Repositories</th>
                  <th>Used by</th>
                  <th>State</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {apps.map((a) => (
                  <AppRows key={a.app.slug} app={a} admin={admin} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Page>
    </>
  );
}

/** An App's row, then a row per installation (or a line saying none). */
function AppRows({ app: a, admin }: { app: GitHubAppStatus; admin: boolean }) {
  const install = useInstall();
  const remove = useRemoveApp();
  const [asking, setAsking] = useState(false);
  const own = !!a.app.org;
  const installations = a.installations ?? [];
  return (
    <>
      <tr>
        <td className="pt-3.5 pb-1.5 font-semibold">
          <a
            href={a.app.url}
            target="_blank"
            rel="noreferrer"
            className="font-mono"
          >
            {a.app.slug}
          </a>{" "}
          <span className="text-xs font-normal text-faint">
            {own ? "this organization" : "platform"}
          </span>
        </td>
        <td className="pt-3.5 pb-1.5 text-mut">{a.app.owner}</td>
        <td />
        <td />
        <td />
        <td className="w-full pt-3 pb-1 text-right whitespace-nowrap">
          {admin && (
            <span className="inline-flex flex-wrap justify-end gap-1.5">
              <Act
                small
                cli={`dock github install --app ${a.app.slug}`}
                disabled={install.isPending}
                onClick={() => install.mutate({ app: a.app.slug })}
              >
                Install
              </Act>
              {own && (
                <Button small onClick={() => setAsking(true)}>
                  Remove
                </Button>
              )}
            </span>
          )}
          {asking && (
            <Confirm
              danger
              action={`Remove ${a.app.slug}`}
              cli={`dock github app remove ${a.app.slug}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() => remove.mutate(a.app.slug)}
              onCancel={() => {
                remove.reset();
                setAsking(false);
              }}
            >
              Dock forgets the App. Services connected through it stop deploying
              until they connect another way. Delete it on GitHub too.
            </Confirm>
          )}
        </td>
      </tr>
      {(install.error || a.installations_error) && (
        <tr>
          <td colSpan={6} className="py-1 pl-5 whitespace-normal">
            <ErrorText error={install.error} />
            {a.installations_error && (
              <span className="text-xs text-warn">
                GitHub couldn't list the installations: {a.installations_error}
              </span>
            )}
          </td>
        </tr>
      )}
      {installations.length === 0 ? (
        <tr>
          <td colSpan={6} className="py-2 pl-5 text-mut">
            Not installed for this organization.
          </td>
        </tr>
      ) : (
        installations.map((i) => (
          <InstallationRow
            key={i.id}
            installation={i}
            owner={own ? a.app.owner : undefined}
            admin={admin}
          />
        ))
      )}
    </>
  );
}

/**
 * An installation of an App. owner is set for the org's own App: an
 * installation on another account counts for nothing.
 */
function InstallationRow({
  installation: i,
  owner,
  admin,
}: {
  installation: GitHubInstallation;
  owner?: string;
  admin: boolean;
}) {
  const unassign = useAssign();
  const [asking, setAsking] = useState(false);
  return (
    <tr>
      <td className="py-2 pl-5 font-mono">
        {i.url ? (
          <a href={i.url} target="_blank" rel="noreferrer">
            {i.account}
          </a>
        ) : (
          i.account
        )}
      </td>
      <td />
      <td className="py-2 text-mut">{i.repositories ?? "·"}</td>
      <td className="py-2">
        {i.connections} {i.connections === 1 ? "service" : "services"}
      </td>
      <td className="py-2">
        {!i.org ? (
          <span className="text-mut">not counted: not on {owner}</span>
        ) : i.missing ? (
          <span className="text-fail">removed on GitHub</span>
        ) : i.suspended ? (
          <span className="text-warn">suspended on GitHub</span>
        ) : (
          <span className="text-ok">installed</span>
        )}
      </td>
      <td className="w-full py-1.5 text-right">
        {admin && i.org && (
          <Button small onClick={() => setAsking(true)}>
            Remove
          </Button>
        )}
        {asking && (
          <Confirm
            danger
            action="Remove"
            cli={`dock github unassign ${i.id}`}
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

/**
 * Creates the org's own App, private, under a GitHub organization: Dock's
 * page posts its manifest to GitHub.
 */
function CreateForm({ onCancel }: { onCancel: () => void }) {
  const create = useCreateApp(false);
  const [owner, setOwner] = useState("");
  const flag = owner.trim() ? ` --owner ${owner.trim()}` : "";
  return (
    <Panel>
      <form
        aria-label="Create the organization's GitHub App"
        className="flex flex-col gap-2 py-2.5"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate({ owner: owner.trim() || undefined });
        }}
      >
        <span className="flex flex-wrap items-center gap-2">
          <input
            aria-label="GitHub organization"
            placeholder="GitHub organization (default: your account)"
            value={owner}
            onChange={(e) => setOwner(e.target.value)}
            spellCheck={false}
            className="field min-w-0 flex-[1_1_240px] py-[5px] font-mono"
          />
          <Act
            small
            primary
            type="submit"
            cli={`dock github app create${flag}`}
            disabled={create.isPending}
          >
            Create on GitHub
          </Act>
          <Button small onClick={onCancel}>
            Cancel
          </Button>
        </span>
        <ErrorText error={create.error} />
        <span className="text-mut">
          A private App of this organization's: only that GitHub account can
          install it, and its installations there are this organization's.
          GitHub asks you to confirm its name, then sends you back here.
        </span>
      </form>
    </Panel>
  );
}
