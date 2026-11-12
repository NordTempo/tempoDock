import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  createContext,
  type FormEvent,
  lazy,
  Suspense,
  useContext,
  useState,
} from "react";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Page,
  PageBar,
  Panel,
  SectionHead,
  StateText,
} from "../../ui/kit";
import { useValidation } from "../create/form";
import { OrgSettingsBar } from "../org/SettingsBar";
import {
  type Cloudflare,
  type CloudflareScope,
  redirectsQuery,
  useAddCloudflare,
  useCloudflare,
  useRefreshCloudflare,
  useRemoveCloudflare,
  useRetryCloudflare,
  useUpdateCloudflare,
} from "./data";

// Cloudflare connections (ADR 0036): the API tokens Dock runs tunnels and
// routes domains with. The console's page holds the platform's (Dock's
// address and server tunnels; "shared" lets orgs add domains from their
// zones), an org's settings its own (ADR 0062). Each server page shows its
// tunnel, each domain how it is routed. Tokens are write-only. The
// Redirects tab (ADR 0068) holds the level's redirects (Redirects.tsx).

const Redirects = lazy(() => import("./Redirects"));

/** The page's tabs; connections is the address without ?tab. */
export type CloudflareTab = "connections" | "redirects";

/** The page's scope, for its rows and form. */
const Scope = createContext<CloudflareScope>("platform");

/** The CLI flag of the scope. */
const flag = (scope: CloudflareScope) =>
  scope === "platform" ? " --platform" : "";

export function CloudflarePage({
  scope,
  tab,
}: {
  scope: CloudflareScope;
  tab: CloudflareTab;
}) {
  const connections = useCloudflare(scope);
  const [adding, setAdding] = useState(false);
  const [addingRedirect, setAddingRedirect] = useState(false);
  const [editing, setEditing] = useState<string>();
  // The answer to the last new token: what it changed in the zones.
  const [saved, setSaved] = useState<Cloudflare>();
  const edited = connections.find((c) => c.name === editing);
  const actions =
    tab === "redirects" ? (
      <Button primary onClick={() => setAddingRedirect(true)}>
        Add redirect
      </Button>
    ) : (
      <>
        {scope === "platform" && connections.length > 0 && <RetryFailed />}
        {!adding && (
          <Button
            primary
            onClick={() => {
              setEditing(undefined);
              setAdding(true);
            }}
          >
            Add connection
          </Button>
        )}
      </>
    );
  return (
    <Scope.Provider value={scope}>
      {scope === "platform" ? (
        <PageBar title="Cloudflare" actions={actions} />
      ) : (
        <OrgSettingsBar actions={actions} />
      )}
      <Tabs />
      {tab === "redirects" ? (
        <Suspense>
          <Redirects
            scope={scope}
            adding={addingRedirect}
            onAdding={setAddingRedirect}
          />
        </Suspense>
      ) : (
        <Page>
          <section
            aria-labelledby="connections"
            className="flex flex-col gap-2"
          >
            <SectionHead id="connections">Connections</SectionHead>
            {connections.length > 0 ? (
              <Panel>
                <table>
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Account</th>
                      <th>Zones</th>
                      <th>{scope === "platform" ? "Tunnels" : "Used by"}</th>
                      {scope === "platform" && <th>Shared</th>}
                      <th>Status</th>
                      <th>Changed</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {connections.map((c) => (
                      <ConnectionRow
                        key={c.name}
                        connection={c}
                        saved={saved?.name === c.name ? saved : undefined}
                        onEdit={() => {
                          setAdding(false);
                          setEditing(c.name);
                        }}
                      />
                    ))}
                  </tbody>
                </table>
              </Panel>
            ) : (
              !adding && (
                <p className="py-2 text-mut">
                  {scope === "platform"
                    ? "No connection. With a Cloudflare API token, Dock runs each server's tunnel and adds domains from your zones; without one, tunnels and their DNS are set up by hand. Add one with Add connection."
                    : "No connection of this organization's own. Domains can use the zones the platform shares; to add domains from your own Cloudflare zones, add a token here. It needs to be in the same Cloudflare account as the platform's tunnels."}
                </p>
              )
            )}
            {adding && <TokenForm onClose={() => setAdding(false)} />}
            {edited && (
              <TokenForm
                key={edited.name}
                connection={edited}
                onSaved={setSaved}
                onClose={() => setEditing(undefined)}
              />
            )}
          </section>
        </Page>
      )}
    </Scope.Provider>
  );
}

/**
 * Connections and Redirects, as links: the current one is the one whose
 * ?tab matches exactly (explicitUndefined), so Connections isn't current
 * on ?tab=redirects too.
 */
function Tabs() {
  const scope = useContext(Scope);
  const org = useOrg();
  const tabs = [
    { label: "Connections", search: { tab: undefined } },
    { label: "Redirects", search: { tab: "redirects" as const } },
  ];
  const active = { includeSearch: true, explicitUndefined: true };
  return (
    <nav
      aria-label="Cloudflare"
      className="tabs flex gap-0.5 border-b border-line px-4 py-2 sm:px-7"
    >
      {tabs.map((t) =>
        scope === "platform" ? (
          <Link
            key={t.label}
            to="/console/cloudflare"
            search={t.search}
            activeOptions={active}
          >
            {t.label}
          </Link>
        ) : (
          <Link
            key={t.label}
            to="/$org/settings/cloudflare"
            params={{ org }}
            search={t.search}
            activeOptions={active}
          >
            {t.label}
          </Link>
        ),
      )}
    </nav>
  );
}

const faint = <span className="text-faint">·</span>;

/**
 * What a new token or a refresh changed: the zones added and dropped, and
 * the domains no other connection could take.
 */
function ZoneChange({ change: c }: { change: Cloudflare }) {
  const added = c.zones_added ?? [];
  const dropped = c.zones_dropped ?? [];
  const failed = c.domains_failed ?? [];
  if (added.length + dropped.length === 0)
    return <div className="text-mut">zones unchanged</div>;
  return (
    <div className="text-mut">
      {added.length > 0 && `added ${added.join(", ")}`}
      {added.length > 0 && dropped.length > 0 && " · "}
      {dropped.length > 0 && `dropped ${dropped.join(", ")}`}
      {failed.length > 0 && (
        <div className="text-fail">
          no longer routed, no other connection has their zone:{" "}
          {failed.join(", ")}
        </div>
      )}
    </div>
  );
}

/** Tries every failed tunnel and domain again, and says how many. */
function RetryFailed() {
  const retry = useRetryCloudflare();
  return (
    <>
      {retry.data && (
        <span className="text-mut">
          {retry.data.retried === 0
            ? "nothing failed"
            : `retrying ${retry.data.retried}`}
        </span>
      )}
      <ErrorText error={retry.error} />
      <Act
        cli="dock cloudflare retry"
        disabled={retry.isPending}
        onClick={() => retry.mutate(undefined)}
      >
        Retry failed
      </Act>
    </>
  );
}

function ConnectionRow({
  connection: c,
  saved,
  onEdit,
}: {
  connection: Cloudflare;
  /** The answer to this connection's new token, when just saved. */
  saved?: Cloudflare;
  onEdit: () => void;
}) {
  const scope = useContext(Scope);
  const refresh = useRefreshCloudflare(scope);
  const remove = useRemoveCloudflare(scope);
  const update = useUpdateCloudflare(scope);
  // Removing asks once; a connection with redirects asks again, typed,
  // before it goes with them (force).
  const [confirming, setConfirming] = useState<"remove" | "force">();
  const redirects = (useQuery(redirectsQuery(scope)).data ?? []).filter(
    (r) => r.connection === c.name,
  );
  const zones = c.zones ?? [];
  const tunnels = c.used_by ?? [];
  // The later of a refresh and a new token.
  const change =
    refresh.data &&
    (!saved ||
      Date.parse(refresh.data.updated_at) >= Date.parse(saved.updated_at))
      ? refresh.data
      : saved;
  return (
    <tr>
      <td className="py-2 font-mono">{c.name}</td>
      <td className="py-2">{c.account_name || c.account_id}</td>
      <td className="py-2 font-mono whitespace-normal">
        {zones.length > 0 ? zones.join(", ") : faint}
        {change && <ZoneChange change={change} />}
      </td>
      <td className="py-2">
        {tunnels.length > 0 ? tunnels.join(", ") : faint}
      </td>
      {scope === "platform" && (
        <td className="py-2">
          <Act
            small
            cli={`dock cloudflare set ${c.name} --platform --shared=${!c.shared}`}
            disabled={update.isPending}
            title="Shared: organizations may add domains from its zones"
            onClick={() =>
              update.mutate({ name: c.name, change: { shared: !c.shared } })
            }
          >
            {c.shared ? "Shared" : "Not shared"}
          </Act>
        </td>
      )}
      <td className="py-2 whitespace-normal">
        {c.status === "ok" ? (
          <StateText state={{ tone: "ok", glyph: "●", label: "working" }} />
        ) : (
          <>
            <StateText state={{ tone: "fail", glyph: "✕", label: "invalid" }} />{" "}
            <span className="text-mut">{c.status_reason}</span>
          </>
        )}
      </td>
      <td className="py-2 text-mut">
        {ago(c.updated_at)} · {c.updated_by}
      </td>
      <td className="w-full py-1.5 text-right">
        <span className="inline-flex items-center gap-3">
          <ErrorText error={refresh.error ?? update.error} />
          <Act
            small
            cli={`dock cloudflare refresh ${c.name}${flag(scope)}`}
            disabled={refresh.isPending}
            onClick={() => refresh.mutate(c.name)}
          >
            {refresh.isPending ? "Refreshing…" : "Refresh zones"}
          </Act>
          <Button small onClick={onEdit}>
            Change token
          </Button>
          <Button
            danger
            small
            disabled={tunnels.length > 0}
            reason="a server's tunnel uses it; to take zones off it, give it a token that reaches fewer with Change token"
            onClick={() => setConfirming("remove")}
          >
            Remove
          </Button>
          {confirming === "remove" && redirects.length === 0 && (
            <Confirm
              danger
              action={`Remove ${c.name}`}
              cli={`dock cloudflare remove ${c.name}${flag(scope)}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(
                  { name: c.name },
                  { onSuccess: () => setConfirming(undefined) },
                )
              }
              onCancel={() => {
                remove.reset();
                setConfirming(undefined);
              }}
            >
              Dock forgets this Cloudflare token. Its domains move to another
              connection that has their zone; DNS records of the rest stay in
              Cloudflare.
            </Confirm>
          )}
          {confirming === "remove" && redirects.length > 0 && (
            <Confirm
              danger
              title={`Remove ${c.name}?`}
              action="Force remove"
              onConfirm={() => setConfirming("force")}
              onCancel={() => setConfirming(undefined)}
            >
              It has {redirects.length}{" "}
              {redirects.length === 1 ? "redirect" : "redirects"}. Remove them
              first, or force remove to take them out of Cloudflare with it.
              <ul className="mt-2 flex max-h-[240px] flex-col overflow-y-auto font-mono text-xs">
                {redirects.map((r) => (
                  <li
                    key={r.hostname}
                    className="truncate border-b border-soft py-1"
                  >
                    {r.hostname} <span className="text-faint">→</span>{" "}
                    <span className="text-sec">{r.target}</span>
                  </li>
                ))}
              </ul>
            </Confirm>
          )}
          {confirming === "force" && (
            <Confirm
              danger
              typed={c.name}
              title={`Force remove ${c.name}?`}
              action="Remove with redirects"
              cli={`dock cloudflare remove ${c.name} --force${flag(scope)}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(
                  { name: c.name, force: true },
                  { onSuccess: () => setConfirming(undefined) },
                )
              }
              onCancel={() => {
                remove.reset();
                setConfirming(undefined);
              }}
            >
              Dock deletes its {redirects.length}{" "}
              {redirects.length === 1 ? "redirect" : "redirects"} in Cloudflare,
              then forgets the token. The hostnames stop redirecting.
            </Confirm>
          )}
        </span>
      </td>
    </tr>
  );
}

/**
 * Adds a connection, or gives one a new token. Dock checks the token and
 * lists its zones before saving.
 */
function TokenForm({
  connection,
  onSaved,
  onClose,
}: {
  connection?: Cloudflare;
  /** Gets the answer to a new token, with the zones it changed. */
  onSaved?: (saved: Cloudflare) => void;
  onClose: () => void;
}) {
  const scope = useContext(Scope);
  const add = useAddCloudflare(scope);
  const update = useUpdateCloudflare(scope);
  const [name, setName] = useState(connection?.name ?? "");
  const [token, setToken] = useState("");
  const [account, setAccount] = useState("");
  const pending = add.isPending || update.isPending;
  const v = useValidation();
  const nameError = v.required(
    !connection && !name.trim(),
    "Name the connection, like home.",
  );
  const tokenError = v.required(!token.trim(), "Paste the API token.");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!v.check((!!connection || !!name.trim()) && !!token.trim())) return;
    const acct = account.trim();
    if (!connection) {
      add.mutate(
        {
          name: name.trim(),
          token: token.trim(),
          ...(acct && { account_id: acct }),
        },
        { onSuccess: onClose },
      );
      return;
    }
    update.mutate(
      {
        name: connection.name,
        change: { token: token.trim(), ...(acct && { account_id: acct }) },
      },
      {
        onSuccess: (saved) => {
          onSaved?.(saved);
          onClose();
        },
      },
    );
  };

  const cli = connection
    ? `printf %s "$TOKEN" | dock cloudflare set ${connection.name} --token-stdin${flag(scope)}`
    : `printf %s "$TOKEN" | dock cloudflare add ${name.trim() || "<name>"} --token-stdin${account.trim() ? ` --account-id ${account.trim()}` : ""}${flag(scope)}`;
  return (
    <form
      ref={v.form}
      aria-label={
        connection ? `New token for ${connection.name}` : "Add connection"
      }
      className="flex max-w-[760px] flex-col gap-3.5 border-t border-line pt-3.5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={submit}
    >
      <div className="flex flex-wrap gap-3">
        {!connection && (
          <label className="flex flex-[1_1_160px] flex-col gap-1.5">
            <span>Name</span>
            <input
              // biome-ignore lint/a11y/noAutofocus: the form opens on request
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="home"
              aria-invalid={!!nameError}
              className={`field font-mono ${nameError ? "border-fail" : ""}`}
            />
            {nameError && (
              <span role="alert" className="text-xs text-fail">
                {nameError}
              </span>
            )}
          </label>
        )}
        <label className="flex flex-[3_1_320px] flex-col gap-1.5">
          <span>API token</span>
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="new-password"
            aria-invalid={!!tokenError}
            className={`field font-mono ${tokenError ? "border-fail" : ""}`}
          />
          {tokenError && (
            <span role="alert" className="text-xs text-fail">
              {tokenError}
            </span>
          )}
        </label>
        <label className="flex flex-[1_1_200px] flex-col gap-1.5">
          <span>
            Account ID <span className="text-mut">optional</span>
          </span>
          <input
            value={account}
            onChange={(e) => setAccount(e.target.value)}
            placeholder={
              connection?.account_id ?? "if the token reaches several"
            }
            className="field font-mono"
          />
        </label>
      </div>
      <p className="text-mut">
        The token needs Account › Cloudflare Tunnel › Edit, and Zone › DNS ›
        Edit and Zone › Zone › Read on the zones domains may come from; for
        redirects, Zone › Single Redirect › Edit too. Dock only edits records
        and rules it made, and never points DNS at an IP.
      </p>
      <ErrorText error={add.error ?? update.error} />
      <div className="flex items-center gap-2.5">
        <Act primary type="submit" cli={cli} disabled={pending}>
          {pending
            ? "Checking the token…"
            : connection
              ? "Save token"
              : "Add connection"}
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <span className="text-xs text-fail">
          {v.problem([
            !connection && !name.trim() && "a name",
            !token.trim() && "a token",
          ])}
        </span>
      </div>
    </form>
  );
}
