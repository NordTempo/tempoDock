import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { useCreateToken, useRevokeToken } from "../data/mutations";
import { queries, useTokens } from "../data/queries";
import type { NewToken, Scope, Token } from "../data/types";
import { TopBar } from "../shell/TopBar";
import { day, since, until } from "../ui/format";
import { Action, Button, ErrorText, inputClass } from "../ui/kit";

export const Route = createFileRoute("/_app/tokens")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(queries.tokens()),
  component: Tokens,
});

function Tokens() {
  const tokens = useTokens();
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; value: string }>();

  return (
    <>
      <TopBar
        actions={
          <Button primary onClick={() => setCreating(true)}>
            New token
          </Button>
        }
      >
        <span className="font-semibold">Tokens</span>
      </TopBar>
      <div className="px-5 pt-3">
        {creating && (
          <CreateToken
            onDone={(created) => {
              setCreating(false);
              if (created) setSecret(created);
            }}
          />
        )}
        {secret && (
          <div className="mb-3 max-w-[760px] border border-line2 px-2.5 py-2">
            <p>
              Token <b>{secret.name}</b>. Copy it now, it is not shown again.
            </p>
            <div className="flex items-baseline gap-3 pt-1">
              <code className="font-mono text-xs break-all">
                {secret.value}
              </code>
              <Action
                onClick={() => navigator.clipboard.writeText(secret.value)}
              >
                copy
              </Action>
              <Action onClick={() => setSecret(undefined)}>done</Action>
            </div>
          </div>
        )}
        <table className="tbl">
          <thead>
            <tr>
              <th>Name</th>
              <th>Scope</th>
              <th>Limited to</th>
              <th>Created</th>
              <th>Last used</th>
              <th>Expires</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {tokens.map((token) => (
              <TokenRow key={token.id} token={token} />
            ))}
          </tbody>
        </table>
        {tokens.length === 0 && <p className="py-2 text-dim">No tokens.</p>}
      </div>
    </>
  );
}

/** "shop/staging", "shop/*" or "all projects". */
function limitText(token: Token) {
  if (!token.project) return "all projects";
  return `${token.project}/${token.environment ?? "*"}`;
}

/** Parses "shop", "shop/*" or "shop/staging" into a token's limit. */
function parseLimit(input: string): Pick<NewToken, "project" | "environment"> {
  const [project, environment] = input.trim().split("/");
  if (!project) return {};
  return environment && environment !== "*"
    ? { project, environment }
    : { project };
}

function TokenRow({ token }: { token: Token }) {
  const revoke = useRevokeToken();
  const [confirming, setConfirming] = useState(false);
  const expired =
    !!token.expires_at && Date.parse(token.expires_at) < Date.now();
  return (
    <tr className={token.revoked_at || expired ? "text-faint" : undefined}>
      <td className="font-semibold">{token.name}</td>
      <td>{token.scope}</td>
      <td className="font-mono text-xs">{limitText(token)}</td>
      <td className="text-dim">
        {day(token.created_at)} by {token.created_by}
      </td>
      <td className="text-dim">
        {token.last_used_at ? since(token.last_used_at) : "never"}
      </td>
      <td className="w-full text-dim">
        {!token.expires_at
          ? "never"
          : expired
            ? "expired"
            : `in ${until(token.expires_at)}`}
      </td>
      <td className="text-right">
        {token.revoked_at ? (
          <span className="text-dim">revoked</span>
        ) : confirming ? (
          <span className="flex gap-3">
            <button
              type="button"
              className="text-bad"
              disabled={revoke.isPending}
              onClick={() => revoke.mutate(token.id)}
            >
              Revoke
            </button>
            <Action onClick={() => setConfirming(false)}>Cancel</Action>
          </span>
        ) : (
          <Action onClick={() => setConfirming(true)}>revoke</Action>
        )}
        <ErrorText error={revoke.error} />
      </td>
    </tr>
  );
}

const scopes: Scope[] = ["read", "deploy", "admin"];

function CreateToken({
  onDone,
}: {
  onDone: (created?: { name: string; value: string }) => void;
}) {
  const create = useCreateToken();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Scope>("deploy");
  const [limit, setLimit] = useState("");
  const [days, setDays] = useState("30");

  return (
    <form
      className="flex flex-wrap items-baseline gap-3 pb-3"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate(
          {
            name,
            scope,
            ...parseLimit(limit),
            expires_in_days: Number(days) > 0 ? Number(days) : undefined,
          },
          {
            onSuccess: ({ token, secret }) =>
              onDone({ name: token.name, value: secret }),
          },
        );
      }}
    >
      <input
        // biome-ignore lint/a11y/noAutofocus: opened on request
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="name, e.g. agent-shop-deploy"
        aria-label="Name"
        className={`${inputClass} w-60`}
      />
      <select
        value={scope}
        aria-label="Scope"
        onChange={(e) =>
          setScope(scopes.find((s) => s === e.target.value) ?? "read")
        }
        className="border-b border-line2 bg-black outline-none focus:border-white"
      >
        {scopes.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
      </select>
      <input
        value={limit}
        onChange={(e) => setLimit(e.target.value)}
        placeholder="limit, e.g. shop/staging"
        aria-label="Limited to"
        className={`${inputClass} w-52 font-mono text-xs`}
      />
      <label className="flex items-baseline gap-1.5 text-dim">
        expires in
        <input
          value={days}
          onChange={(e) => setDays(e.target.value)}
          inputMode="numeric"
          aria-label="Expires in days"
          className={`${inputClass} w-10 text-right text-white`}
        />
        days
      </label>
      <Button primary type="submit" disabled={create.isPending}>
        Create
      </Button>
      <Action onClick={() => onDone()}>Cancel</Action>
      <ErrorText error={create.error} />
    </form>
  );
}
