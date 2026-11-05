import { type FormEvent, useState } from "react";
import { ago, plural } from "../../ui/format";
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
import {
  type Cloudflare,
  useAddCloudflare,
  useCloudflare,
  useRefreshCloudflare,
  useRemoveCloudflare,
  useRetryCloudflare,
  useUpdateCloudflare,
} from "./data";

// The console's Cloudflare page (ADR 0036): the API tokens Dock runs
// tunnels and routes domains with. Each server page shows its tunnel,
// each domain how it is routed. Tokens are write-only.

export function CloudflarePage() {
  const connections = useCloudflare();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string>();
  const edited = connections.find((c) => c.name === editing);
  return (
    <>
      <PageBar
        title="Cloudflare"
        after={
          <span className="text-xs text-faint">
            {plural(connections.length, "connection")}
          </span>
        }
        actions={
          <>
            {connections.length > 0 && <RetryFailed />}
            {!adding && connections.length > 0 && (
              <Button
                onClick={() => {
                  setEditing(undefined);
                  setAdding(true);
                }}
              >
                Add connection
              </Button>
            )}
          </>
        }
      />
      <Page>
        <section aria-labelledby="connections" className="flex flex-col gap-2">
          <SectionHead id="connections">Connections</SectionHead>
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
              <div className="flex flex-col items-start gap-3 py-2">
                <p className="text-mut">
                  No connection. With a Cloudflare API token, Dock runs each
                  server's tunnel and adds domains from your zones; without one,
                  tunnels and their DNS are set up by hand.
                </p>
                <Button primary onClick={() => setAdding(true)}>
                  Add connection
                </Button>
              </div>
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
    </>
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
          <ErrorText error={refresh.error} />
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
          <Button
            small
            disabled={tunnels.length > 0}
            reason="a server's tunnel uses it"
            onClick={() => setConfirming(true)}
          >
            Remove
          </Button>
          {confirming && (
            <Confirm
              danger
              action={`Remove ${c.name}`}
              cli={`dock cloudflare remove ${c.name}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(c.name, { onSuccess: () => setConfirming(false) })
              }
              onCancel={() => {
                remove.reset();
                setConfirming(false);
              }}
            >
              Dock forgets this Cloudflare token. DNS records it made stay in
              Cloudflare.
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
      { onSuccess: onClose },
    );
  };

  const cli = connection
    ? `printf %s "$TOKEN" | dock cloudflare set ${connection.name} --token-stdin`
    : `printf %s "$TOKEN" | dock cloudflare add ${name.trim() || "<name>"} --token-stdin${account.trim() ? ` --account-id ${account.trim()}` : ""}`;
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
              className={`field py-[7px] font-mono ${nameError ? "border-fail" : ""}`}
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
            className={`field py-[7px] font-mono ${tokenError ? "border-fail" : ""}`}
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
            className="field py-[7px] font-mono"
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
