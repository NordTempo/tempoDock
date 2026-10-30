import { type FormEvent, useState } from "react";
import { ago } from "../../ui/format";
import {
  Act,
  Button,
  ErrorText,
  Page,
  Panel,
  SectionHead,
  StateText,
  TitleRow,
} from "../../ui/kit";
import {
  type Cloudflare,
  useAddCloudflare,
  useCloudflare,
  useRefreshCloudflare,
  useRemoveCloudflare,
  useRetryCloudflare,
  useUpdateCloudflare,
} from "./data";

// The console's Cloudflare page (ADR 0035): the API tokens Dock runs
// tunnels and routes domains with. Each server page shows its tunnel,
// each domain how it is routed. Tokens are write-only.

export function CloudflarePage() {
  const connections = useCloudflare();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string>();
  const edited = connections.find((c) => c.name === editing);
  return (
    <Page>
      <TitleRow
        title="Cloudflare"
        context="API tokens Dock runs tunnels and domains with"
        actions={
          <span className="flex items-center gap-2.5">
            {connections.length > 0 && <RetryFailed />}
            {!adding && (
              <Button
                onClick={() => {
                  setEditing(undefined);
                  setAdding(true);
                }}
              >
                Add connection
              </Button>
            )}
          </span>
        }
      />
      <section aria-labelledby="connections" className="flex flex-col gap-2">
        <SectionHead
          id="connections"
          aside="domains are added from their zones, through server tunnels"
        >
          Connections
        </SectionHead>
        {connections.length > 0 ? (
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Account</th>
                  <th>Zones</th>
                  <th>Tunnels</th>
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
              No connection. Tunnels and their DNS are set up by hand.
            </p>
          )
        )}
        {adding && <TokenForm onClose={() => setAdding(false)} />}
        {edited && (
          <TokenForm
            key={edited.name}
            connection={edited}
            onClose={() => setEditing(undefined)}
          />
        )}
      </section>
    </Page>
  );
}

const faint = <span className="text-faint">·</span>;

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
  onEdit,
}: {
  connection: Cloudflare;
  onEdit: () => void;
}) {
  const refresh = useRefreshCloudflare();
  const remove = useRemoveCloudflare();
  const [confirming, setConfirming] = useState(false);
  const zones = c.zones ?? [];
  const tunnels = c.used_by ?? [];
  return (
    <tr>
      <td className="py-2 font-mono">{c.name}</td>
      <td className="py-2">{c.account_name || c.account_id}</td>
      <td className="py-2 font-mono whitespace-normal">
        {zones.length > 0 ? zones.join(", ") : faint}
      </td>
      <td className="py-2">
        {tunnels.length > 0 ? tunnels.join(", ") : faint}
      </td>
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
          <ErrorText error={refresh.error ?? remove.error} />
          <Act
            small
            cli={`dock cloudflare refresh ${c.name}`}
            disabled={refresh.isPending}
            onClick={() => refresh.mutate(c.name)}
          >
            {refresh.isPending ? "Refreshing…" : "Refresh zones"}
          </Act>
          <Button small onClick={onEdit}>
            Change token
          </Button>
          {confirming ? (
            <>
              <Act
                small
                danger
                cli={`dock cloudflare remove ${c.name}`}
                disabled={remove.isPending}
                onClick={() => remove.mutate(c.name)}
              >
                Remove now
              </Act>
              <Button small onClick={() => setConfirming(false)}>
                Cancel
              </Button>
            </>
          ) : (
            <Button
              small
              disabled={tunnels.length > 0}
              title={tunnels.length > 0 ? "a server's tunnel uses it" : ""}
              onClick={() => setConfirming(true)}
            >
              Remove
            </Button>
          )}
        </span>
      </td>
    </tr>
  );
}

const field = "field py-[7px] font-mono";

/**
 * Adds a connection, or gives one a new token. Dock checks the token and
 * lists its zones before saving.
 */
function TokenForm({
  connection,
  onClose,
}: {
  connection?: Cloudflare;
  onClose: () => void;
}) {
  const add = useAddCloudflare();
  const update = useUpdateCloudflare();
  const [name, setName] = useState(connection?.name ?? "");
  const [token, setToken] = useState("");
  const [account, setAccount] = useState("");
  const pending = add.isPending || update.isPending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
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
      { onSuccess: onClose },
    );
  };

  const cli = connection
    ? `printf %s "$TOKEN" | dock cloudflare set ${connection.name} --token-stdin`
    : `printf %s "$TOKEN" | dock cloudflare add ${name.trim() || "<name>"} --token-stdin${account.trim() ? ` --account-id ${account.trim()}` : ""}`;
  return (
    <form
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
              className={field}
            />
          </label>
        )}
        <label className="flex flex-[3_1_320px] flex-col gap-1.5">
          <span>API token</span>
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="new-password"
            className={field}
          />
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
            className={field}
          />
        </label>
      </div>
      <p className="text-mut">
        The token needs Account › Cloudflare Tunnel › Edit, and Zone › DNS ›
        Edit and Zone › Zone › Read on the zones domains may come from. Dock
        only edits records it made, and never points DNS at an IP.
      </p>
      <ErrorText error={add.error ?? update.error} />
      <div className="flex items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={cli}
          disabled={pending || !token.trim()}
        >
          {pending
            ? "Checking the token…"
            : connection
              ? "Save token"
              : "Add connection"}
        </Act>
        <Button onClick={onClose}>Cancel</Button>
      </div>
    </form>
  );
}
