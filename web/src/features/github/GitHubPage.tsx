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
  type GitHubAppStatus,
  type GitHubInstallation,
  useAssign,
  useCreateApp,
  useGitHub,
  useInstall,
  useRemoveApp,
  useSetClientSecret,
} from "./data";

// The console's GitHub page (mock G1, P-B for several Apps): every GitHub
// App stacked, each as a list of properties with its installations below
// and the org each deploys for (ADR 0031, 0105). The platform's Apps come
// first, then each org's own, which its admins manage. Creating and
// installing continue on github.com, and Dock checks with GitHub who
// installed a platform App (ADR 0066).

export function GitHubPage() {
  const { data: gh, refetch, isFetching } = useGitHub();
  const apps = gh.apps ?? [];
  const [creating, setCreating] = useState(false);
  return (
    <>
      <PageBar
        title="GitHub"
        actions={
          apps.length > 0 && (
            <>
              <Act
                small
                cli="dock github status"
                disabled={isFetching}
                onClick={() => refetch()}
              >
                Check again
              </Act>
              <Button
                small
                primary
                disabled={creating}
                onClick={() => setCreating(true)}
              >
                Create platform App
              </Button>
            </>
          )
        }
      />
      <Page>
        {gh.hooks_hint && (
          <p className="text-warn">
            GitHub needs a public address to deliver pushes to.{" "}
            <Link to="/console/address">Set Dock's address</Link> first.
          </p>
        )}
        {(apps.length === 0 || creating) && (
          <CreateForm
            disabled={!!gh.hooks_hint}
            onCancel={apps.length > 0 ? () => setCreating(false) : undefined}
          />
        )}
        {apps.map((a) => (
          <section key={a.app.slug} className="flex flex-col gap-2">
            <h2 className="flex flex-wrap items-baseline gap-2 font-mono">
              {a.app.slug}
              <span className="font-sans text-xs text-faint">
                {a.app.org ? `org ${a.app.org}` : "platform"}
              </span>
            </h2>
            <AppPanel app={a} callbackURL={gh.callback_url ?? ""} />
            <Installations app={a} orgs={gh.orgs ?? []} />
            {!a.app.org && (
              <InstallForm app={a.app.slug} orgs={gh.orgs ?? []} />
            )}
          </section>
        ))}
      </Page>
    </>
  );
}

/** An App, its webhook and permissions; Remove asks first. */
function AppPanel({
  app: a,
  callbackURL,
}: {
  app: GitHubAppStatus;
  callbackURL: string;
}) {
  const remove = useRemoveApp();
  const [asking, setAsking] = useState(false);
  const app = a.app;
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
                {app.name}
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
              {a.webhook_url ? (
                <span className="inline-flex flex-wrap items-baseline gap-2">
                  <span className="font-mono">{a.webhook_url}</span>
                  <CopyButton text={a.webhook_url} />
                </span>
              ) : (
                <span className="text-warn">no public address</span>
              )}
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint">Installs</td>
            <td className="py-2.5 whitespace-normal">
              {app.org ? (
                <span className="text-mut">
                  org {app.org}'s own: only {app.owner} counts, managed by its
                  admins
                </span>
              ) : app.verifies_installs ? (
                <span className="text-mut">
                  checked against the installer's GitHub account
                </span>
              ) : (
                <ClientSecretForm slug={app.slug} callbackURL={callbackURL} />
              )}
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint">Permissions</td>
            <td className="py-2.5 text-mut">
              contents read · metadata read · statuses write · pull requests
              write · push events
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
                  cli={`dock github app remove ${app.slug}`}
                  pending={remove.isPending}
                  error={remove.error}
                  onConfirm={() => remove.mutate(app.slug)}
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
function ClientSecretForm({
  slug,
  callbackURL,
}: {
  slug: string;
  callbackURL: string;
}) {
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
          set.mutate({ slug, secret: secret.trim() });
        }}
      >
        <input
          type="password"
          aria-label="Client secret"
          placeholder="client secret"
          autoComplete="new-password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          className="field min-w-0 flex-[1_1_200px] py-[5px] font-mono"
        />
        <Act
          small
          primary
          type="submit"
          cli={`dock github app secret ${slug}`}
          disabled={!secret.trim() || set.isPending}
        >
          Save
        </Act>
        <ErrorText error={set.error} />
      </form>
    </div>
  );
}

/**
 * An App's installations: a platform App's each assigned to an org from a
 * select, an org's own App's with the org they count for.
 */
function Installations({
  app: a,
  orgs,
}: {
  app: GitHubAppStatus;
  orgs: string[];
}) {
  const assign = useAssign();
  const { refetch } = useGitHub();
  const installations = a.installations ?? [];
  return (
    <section className="flex flex-col gap-2">
      {a.installations_error && (
        <ErrorBlock
          error={
            new Error(
              `GitHub couldn't list the installations: ${a.installations_error}`,
            )
          }
          onRetry={() => refetch()}
        />
      )}
      {installations.length === 0 ? (
        <p className="text-mut">
          {a.app.org
            ? `Not installed yet. Org ${a.app.org}'s admins install it.`
            : "Not installed anywhere yet. Install it for an org below."}
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
                  owner={a.app.org ? a.app.owner : undefined}
                  orgs={orgs}
                  pending={assign.isPending}
                  onAssign={(org) =>
                    assign.mutate({ id: i.id, org, app: a.app.slug })
                  }
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

/**
 * An installation and its org. owner is set for an org's own App, whose
 * installations aren't assigned: they count for its org on owner's
 * account only.
 */
function InstallationRow({
  installation: i,
  owner,
  orgs,
  pending,
  onAssign,
}: {
  installation: GitHubInstallation;
  owner?: string;
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
        {owner !== undefined ? (
          i.org ? (
            <span>{i.org}</span>
          ) : (
            <span className="text-mut">not counted: not on {owner}</span>
          )
        ) : (
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
        )}
      </td>
    </tr>
  );
}

/** Opens a platform App's install page on GitHub for the chosen org. */
function InstallForm({ app, orgs }: { app: string; orgs: string[] }) {
  const install = useInstall();
  const [org, setOrg] = useState(orgs[0] ?? "");
  return (
    <form
      aria-label={`Install ${app}`}
      className="flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        install.mutate({ org, app });
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
        cli={`dock github install ${org || "<org>"} --app ${app}`}
        disabled={!org || install.isPending}
      >
        Install on GitHub
      </Act>
      <ErrorText error={install.error} />
    </form>
  );
}

/**
 * Creates a platform App: Dock's page posts its manifest to GitHub.
 * onCancel closes it, when there are Apps already.
 */
function CreateForm({
  disabled,
  onCancel,
}: {
  disabled: boolean;
  onCancel?: () => void;
}) {
  const create = useCreateApp(true);
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
            <td className="py-2.5 text-mut">a new platform App</td>
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
                  cli={`dock github app create --platform${flag}`}
                  disabled={disabled || create.isPending}
                >
                  Create on GitHub
                </Act>
                {onCancel && (
                  <Button small onClick={onCancel}>
                    Cancel
                  </Button>
                )}
                <ErrorText error={create.error} />
              </form>
            </td>
          </tr>
          <tr>
            <td className="py-2.5 text-faint" />
            <td className="py-2.5 whitespace-normal text-mut">
              An App for every org, owned by that GitHub organization or by you.
              Private: only that account can install it. Public: any GitHub
              account can, so orgs deploy from their own; GitHub can make it
              public later, not private again. An org can also make its own in
              its settings. It reads code, posts statuses and comments, and
              receives pushes. GitHub asks you to confirm its name, then sends
              you back here.
            </td>
          </tr>
        </tbody>
      </table>
    </Panel>
  );
}
