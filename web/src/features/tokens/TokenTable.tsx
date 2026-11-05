import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useRevokeToken } from "../../data/mutations";
import { useIsAdmin, useIsOperator } from "../../data/queries";
import type { Token } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { Actor, actorKind, Button, Confirm, Panel } from "../../ui/kit";
import { useSaveTokenData } from "../data/dockPolicy";
import {
  TokenDataEditor,
  tokenDataFlags,
  tokenDataText,
} from "../data/TokenData";

/**
 * Tokens with revoke, data access (admins) and their activity. `mine`
 * leaves out who created them, for the caller's own on their account.
 */
export function TokenTable({
  tokens,
  mine = false,
}: {
  tokens: Token[];
  mine?: boolean;
}) {
  return (
    <>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Scope</th>
              <th>Limited to</th>
              {!mine && <th>Created by</th>}
              <th>Data</th>
              <th>Last used</th>
              <th>Expires</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {tokens.map((token) => (
              <TokenRow
                key={token.id}
                token={token}
                mine={mine}
                made={
                  tokens.filter(
                    (t) => t.created_by === token.name && !t.revoked_at,
                  ).length
                }
              />
            ))}
          </tbody>
        </table>
      </Panel>
      {tokens.length === 0 && <p className="py-2 text-mut">No tokens yet.</p>}
    </>
  );
}

/** "Feb 3, 2027": a token's expiry, far enough out to need the year. */
const longDay = (iso: string) =>
  new Date(iso).toLocaleDateString("en", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });

/**
 * One token. `made` counts the live tokens it created, which revoking it
 * leaves working; the confirmation says so.
 */
function TokenRow({
  token,
  mine,
  made,
}: {
  token: Token;
  mine: boolean;
  made: number;
}) {
  const revoke = useRevokeToken();
  const admin = useIsAdmin();
  const org = useOrg();
  const operator = useIsOperator();
  const [confirming, setConfirming] = useState(false);
  const [editingData, setEditingData] = useState(false);
  const saveData = useSaveTokenData();
  const expired =
    !!token.expires_at && Date.parse(token.expires_at) < Date.now();
  const dead = !!token.revoked_at || expired;
  return (
    <>
      <tr className={`*:align-middle ${dead ? "text-faint" : ""}`}>
        <td className={`py-2 ${dead ? "" : "text-info"}`}>
          ◇ {token.name}
          {mine && token.origin === "cli_login" && (
            <span className="text-mut"> via dock login</span>
          )}
        </td>
        <td className="py-2">
          {token.scope}
          {token.operator && <span className="text-warn"> operator</span>}
        </td>
        <td className="py-2 font-mono">
          {token.project ? (
            `${token.project}${token.environment ? `/${token.environment}` : ""}`
          ) : (
            <span className="text-mut">all projects</span>
          )}
        </td>
        {!mine && (
          <td className="py-2">
            <Actor name={token.created_by} kind={actorKind(token.created_by)} />
            {token.origin === "cli_login" && (
              <span className="text-mut"> via dock login</span>
            )}
          </td>
        )}
        <td className={`py-2 ${token.data ? "" : "text-mut"}`}>
          {tokenDataText(token.data)}
        </td>
        <td className={`py-2 ${token.last_used_at ? "" : "text-mut"}`}>
          {token.last_used_at ? ago(token.last_used_at) : "never"}
        </td>
        <td className="py-2">
          {token.revoked_at ? (
            "revoked"
          ) : !token.expires_at ? (
            <span className="text-mut">never</span>
          ) : expired ? (
            "expired"
          ) : (
            longDay(token.expires_at)
          )}
        </td>
        <td className="w-full py-1.5 text-right">
          <span className="inline-flex items-center gap-4">
            <Link
              to={operator ? "/console/audit" : "/$org/audit"}
              params={{ org }}
              search={{ actor: token.name }}
              className="lnk"
            >
              Activity
            </Link>
            {admin && !dead && (
              <button
                type="button"
                className="lnk"
                aria-expanded={editingData}
                onClick={() => setEditingData((e) => !e)}
              >
                Data access
              </button>
            )}
            {!token.revoked_at && (
              <Button small onClick={() => setConfirming(true)}>
                Revoke
              </Button>
            )}
          </span>
          {/* The modal inherits the cell's alignment and nowrap. */}
          <div className="contents text-left whitespace-normal">
            {confirming && (
              <Confirm
                danger
                action={`Revoke ${token.name}`}
                cli={`dock token revoke ${token.id}`}
                pending={revoke.isPending}
                error={revoke.error}
                onConfirm={() =>
                  revoke.mutate(token.id, {
                    onSuccess: () => setConfirming(false),
                  })
                }
                onCancel={() => setConfirming(false)}
              >
                Anything using it stops working right away
                {token.last_used_at
                  ? ` (last used ${ago(token.last_used_at)})`
                  : ""}
                .{made === 1 && " The token it created keeps working."}
                {made > 1 && ` The ${made} tokens it created keep working.`}{" "}
                This can't be undone.
              </Confirm>
            )}
          </div>
        </td>
      </tr>
      {editingData && (
        <tr>
          <td colSpan={mine ? 7 : 8}>
            <TokenDataEditor
              data={token.data}
              cap={tokenCap}
              cli={(data) =>
                data
                  ? `dock token update ${token.id} ${tokenDataFlags(data)}`
                  : `dock token update ${token.id} --data-clear`
              }
              onSave={(data) => saveData(token.id, data)}
              onDone={() => setEditingData(false)}
            />
          </td>
        </tr>
      )}
    </>
  );
}

/** What caps a token's own data access in Dock. */
export const tokenCap =
  "never more than a user with its scope, or than its creator has. On production, write acts as propose.";
