import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useRevokeToken } from "../../data/mutations";
import { useIsAdmin, useIsOperator } from "../../data/queries";
import type { Token } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, isoDay } from "../../ui/format";
import { Act, Actor, actorKind, Button, ErrorText, Panel } from "../../ui/kit";
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
              <TokenRow key={token.id} token={token} mine={mine} />
            ))}
          </tbody>
        </table>
      </Panel>
      {tokens.length === 0 && <p className="py-2 text-mut">No tokens yet.</p>}
    </>
  );
}

function TokenRow({ token, mine }: { token: Token; mine: boolean }) {
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
      <tr className={dead ? "text-faint" : undefined}>
        <td className={`py-2.5 ${dead ? "" : "text-info"}`}>
          ◇ {token.name}
          {mine && token.origin === "cli_login" && (
            <span className="text-mut"> via dock login</span>
          )}
        </td>
        <td className="py-2.5">
          {token.scope}
          {token.operator && <span className="text-warn"> operator</span>}
        </td>
        <td className="py-2.5 font-mono">
          {token.project ? (
            `${token.project}${token.environment ? `/${token.environment}` : ""}`
          ) : (
            <span className="font-sans text-mut">all projects</span>
          )}
        </td>
        {!mine && (
          <td className="py-2.5">
            <Actor name={token.created_by} kind={actorKind(token.created_by)} />
            {token.origin === "cli_login" && (
              <span className="text-mut"> via dock login</span>
            )}
          </td>
        )}
        <td
          className={`py-2.5 font-mono text-xs ${token.data ? "" : "font-sans text-mut"}`}
        >
          {tokenDataText(token.data)}
        </td>
        <td className={`py-2.5 ${token.last_used_at ? "" : "text-mut"}`}>
          {token.last_used_at ? ago(token.last_used_at) : "never"}
        </td>
        <td className="py-2.5 font-mono">
          {token.revoked_at ? (
            <span className="font-sans">revoked</span>
          ) : !token.expires_at ? (
            <span className="font-sans text-mut">never</span>
          ) : expired ? (
            <span className="font-sans">expired</span>
          ) : (
            isoDay(token.expires_at)
          )}
        </td>
        <td className="w-full py-1.5 text-right">
          <span className="inline-flex items-center gap-3">
            <Link
              to={operator ? "/console/audit" : "/$org/audit"}
              params={{ org }}
              search={{ actor: token.name }}
            >
              Activity
            </Link>
            {admin && !dead && (
              <Button small onClick={() => setEditingData((e) => !e)}>
                Data access
              </Button>
            )}
            {!token.revoked_at &&
              (confirming ? (
                <>
                  <Act
                    small
                    danger
                    cli={`dock token revoke ${token.id}`}
                    disabled={revoke.isPending}
                    onClick={() => revoke.mutate(token.id)}
                  >
                    Revoke now
                  </Act>
                  <Button small onClick={() => setConfirming(false)}>
                    Cancel
                  </Button>
                </>
              ) : (
                <Button small onClick={() => setConfirming(true)}>
                  Revoke
                </Button>
              ))}
            <ErrorText error={revoke.error} />
          </span>
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
