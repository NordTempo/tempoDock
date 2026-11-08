import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { ago } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Page,
  PageBar,
  Panel,
} from "../../ui/kit";
import {
  type GitHubInstallation,
  type GitHubStatus,
  useAssign,
  useCreateApp,
  useGitHub,
  useInstall,
  useRemoveApp,
  useSetClientSecret,
} from "./data";

// The console's GitHub page (mock G1): Dock's GitHub App as a list of
// properties, its installations below with the org each deploys for
// (ADR 0031). Creating and installing continue on github.com, and Dock
// checks with GitHub who installed it (ADR 0066).

export function GitHubPage() {
  const { data: gh, refetch, isFetching } = useGitHub();
  return (
    <>
      <PageBar title="GitHub" />
      <Page>
        {gh.hooks_hint && (
          <p className="text-warn">
            GitHub needs a public address to deliver pushes to.{" "}
            <Link to="/console/address">Set Dock's address</Link> first.
          </p>
        )}
        {gh.app ? (
          <>
            <AppPanel gh={gh} />
            <Installations gh={gh} />
            <span className="flex flex-wrap items-center gap-2">
              <InstallForm orgs={gh.orgs ?? []} />
              <Act
                small
                cli="dock github status"
                disabled={isFetching}
                onClick={() => refetch()}
              >
                Check again
              </Act>
            </span>
          </>
        ) : (
          <CreateForm disabled={!!gh.hooks_hint} />
        )}
      </Page>
    </>
  );
}

/** The App, its webhook and permissions; Remove asks first. */
function AppPanel({ gh }: { gh: GitHubStatus }) {
  const remove = useRemoveApp();
  const [asking, setAsking] = useState(false);
  const app = gh.app;
  if (!app) return null;
  return (
    <Panel>
      <table>
        <tbody className="[&>tr>td]:align-middle">
          <tr>
            <td className="w-36 py-2.5 text-faint">App</td>
            <td className="py-2.5 whitespace-normal">
              <a
                href={app.url}
                target="_blank"
                rel="noreferrer"
                className="font-mono"
              >
                {app.slug}
              </a>{" "}
              <span className="text-faint">
                on {app.owner} · created {ago(app.created_at)} by{" "}
                {app.created_by}
              </span>
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint">Webhook</td>
            <td className="py-2.5">
              {gh.webhook_url ? (
                <span className="inline-flex flex-wrap items-baseline gap-2">
                  <span className="font-mono">{gh.webhook_url}</span>
                  <CopyButton text={gh.webhook_url} />
                </span>
              ) : (
                <span className="text-warn">no public address</span>
              )}
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint">Installs</td>
            <td className="py-2.5 whitespace-normal">
              {app.verifies_installs ? (
                <span className="text-mut">
                  checked against the installer's GitHub account
                </span>
              ) : (
                <ClientSecretForm callbackURL={gh.callback_url ?? ""} />
              )}
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint">Permissions</td>
            <td className="py-2.5 text-mut">
              contents read · metadata read · statuses write · push events
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint" />
            <td className="py-1.5 whitespace-normal">
              <Button small onClick={() => setAsking(true)}>
                Remove App
              </Button>
              {asking && (
                <Confirm
                  danger
                  action={`Remove ${app.slug}`}
                  cli="dock github app remove"
                  pending={remove.isPending}
                  error={remove.error}
                  onConfirm={() => remove.mutate()}
                  onCancel={() => {
                    remove.reset();
                    setAsking(false);
                  }}
                >
                  Dock forgets the App. Services connected through it stop
                  deploying until they are reconnected. Delete it on GitHub too.
                </Confirm>
              )}
            </td>
          </tr>
        </tbody>
      </table>
    </Panel>
  );
}

/**
 * For an App made before Dock kept its OAuth client: the callback URL to
 * add on GitHub and the client secret generated there.
 */
function ClientSecretForm({ callbackURL }: { callbackURL: string }) {
  const set = useSetClientSecret();
  const [secret, setSecret] = useState("");
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-warn">
        unchecked: only operators' install links work
      </span>
      <span className="text-mut">
        In the App's settings on GitHub, add the callback URL{" "}
        <span className="font-mono">{callbackURL}</span>{" "}
        <CopyButton text={callbackURL} /> and generate a client secret.
      </span>
      <form
        aria-label="Set the GitHub App's client secret"
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          set.mutate(secret.trim());
        }}
      >
        <input
          type="password"
          aria-label="Client secret"
          placeholder="client secret"
          autoComplete="off"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          className="field min-w-0 flex-[1_1_200px] py-[5px] font-mono"
        />
        <Act
          small
          primary
          type="submit"
          cli="dock github app secret"
          disabled={!secret.trim() || set.isPending}
        >
          Save
        </Act>
        <ErrorText error={set.error} />
      </form>
    </div>
  );
}

/** The App's installations, each assigned to an org from a select. */
function Installations({ gh }: { gh: GitHubStatus }) {
  const assign = useAssign();
  const { refetch } = useGitHub();
  const installations = gh.installations ?? [];
  return (
    <section className="flex flex-col gap-2">
      {gh.installations_error && (
        <ErrorBlock
          error={
            new Error(
              `GitHub couldn't list the installations: ${gh.installations_error}`,
            )
          }
          onRetry={() => refetch()}
        />
      )}
      {installations.length === 0 ? (
        <p className="text-mut">
          Not installed anywhere yet. Install it for an org below.
        </p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Installation</th>
                <th>Repositories</th>
                <th>Services</th>
                <th>Org</th>
              </tr>
            </thead>
            <tbody>
              {installations.map((i) => (
                <InstallationRow
                  key={i.id}
                  installation={i}
                  orgs={gh.orgs ?? []}
                  pending={assign.isPending}
                  onAssign={(org) => assign.mutate({ id: i.id, org })}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      <ErrorText error={assign.error} />
    </section>
  );
}

function InstallationRow({
  installation: i,
  orgs,
  pending,
  onAssign,
}: {
  installation: GitHubInstallation;
  orgs: string[];
  pending: boolean;
  onAssign: (org: string) => void;
}) {
  const cli = (org: string) =>
    org ? `dock github assign ${i.id} ${org}` : `dock github unassign ${i.id}`;
  return (
    <tr>
      <td className="py-2">
        {i.url ? (
          <a
            href={i.url}
            target="_blank"
            rel="noreferrer"
            className="font-mono"
          >
            {i.account}
          </a>
        ) : (
          <span className="font-mono">{i.account}</span>
        )}
        {i.missing && <span className="text-warn"> uninstalled</span>}
        {i.suspended && <span className="text-warn"> suspended</span>}
      </td>
      <td className="py-2 text-mut">{i.repositories || "unknown"}</td>
      <td className="py-2 text-mut">{i.connections}</td>
      <td className="w-full py-1.5">
        <select
          aria-label={`Org for ${i.account}`}
          value={i.org ?? ""}
          disabled={pending}
          title={cli(i.org ?? "")}
          onChange={(e) => onAssign(e.target.value)}
          className={i.org ? "" : "text-warn"}
        >
          <option value="">unassigned</option>
          {orgs.map((o) => (
            <option key={o}>{o}</option>
          ))}
        </select>
      </td>
    </tr>
  );
}

/** Opens the App's install page on GitHub for the chosen org. */
function InstallForm({ orgs }: { orgs: string[] }) {
  const install = useInstall();
  const [org, setOrg] = useState(orgs[0] ?? "");
  return (
    <form
      aria-label="Install the GitHub App"
      className="flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        install.mutate(org);
      }}
    >
      <select
        aria-label="Org"
        value={org}
        onChange={(e) => setOrg(e.target.value)}
      >
        {orgs.map((o) => (
          <option key={o}>{o}</option>
        ))}
      </select>
      <Act
        small
        primary
        type="submit"
        cli={`dock github install ${org || "<org>"}`}
        disabled={!org || install.isPending}
      >
        Install on GitHub
      </Act>
      <ErrorText error={install.error} />
    </form>
  );
}

/** Creates the App: Dock's page posts its manifest to GitHub. */
function CreateForm({ disabled }: { disabled: boolean }) {
  const create = useCreateApp();
  const [owner, setOwner] = useState("");
  const [isPublic, setPublic] = useState(false);
  const flag =
    (owner.trim() ? ` --owner ${owner.trim()}` : "") +
    (isPublic ? " --public" : "");
  return (
    <Panel>
      <table>
        <tbody className="[&>tr>td]:align-middle">
          <tr>
            <td className="w-36 py-2.5 text-faint">App</td>
            <td className="py-2.5 text-mut">none yet</td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint">Owner</td>
            <td className="py-1.5">
              <form
                aria-label="Create the GitHub App"
                className="flex flex-wrap items-center gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  create.mutate({
                    owner: owner.trim() || undefined,
                    public: isPublic,
                  });
                }}
              >
                <input
                  aria-label="GitHub organization"
                  placeholder="your account"
                  value={owner}
                  onChange={(e) => setOwner(e.target.value)}
                  spellCheck={false}
                  className="field min-w-0 flex-[1_1_200px] py-[5px] font-mono"
                />
                <label className="flex items-center gap-1.5 text-mut">
                  <input
                    type="checkbox"
                    checked={isPublic}
                    onChange={(e) => setPublic(e.target.checked)}
                  />
                  public
                </label>
                <Act
                  small
                  primary
                  type="submit"
                  cli={`dock github app create${flag}`}
                  disabled={disabled || create.isPending}
                >
                  Create on GitHub
                </Act>
                <ErrorText error={create.error} />
              </form>
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint" />
            <td className="py-2.5 whitespace-normal text-mut">
              An App owned by that GitHub organization, or by you. Private: only
              that account can install it. Public: any GitHub account can, so
              orgs deploy from their own; GitHub can make it public later, not
              private again. It reads code, posts statuses and comments, and
              receives pushes. GitHub asks you to confirm its name, then sends
              you back here.
            </td>
          </tr>
        </tbody>
      </table>
    </Panel>
  );
}
