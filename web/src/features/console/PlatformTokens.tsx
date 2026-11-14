import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { useIsOperator } from "../../data/queries";
import { ago } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Page,
  PageBar,
} from "../../ui/kit";

// The platform's tokens (ADR 0080): credentials that run the platform
// with a platform role and act in no organization, the platform logins
// people made with `dock login --platform`, and legacy tokens from before
// that are both an org's and the platform's until split.

type Token = components["schemas"]["Token"];
type Role = components["schemas"]["CreatePlatformTokenRequest"]["role"];

const tokensKey = ["console", "tokens"] as const;

export const platformTokensQuery = () =>
  queryOptions({
    queryKey: tokensKey,
    queryFn: () => list(api.GET("/api/v1/console/tokens")),
  });

const useCreate = () =>
  useAction(
    (req: { name: string; role: Role; expires_in_days: number }) =>
      body(api.POST("/api/v1/console/tokens", { body: req })),
    () => [tokensKey, ["audit"]],
  );

const useRevoke = () =>
  useAction(
    (id: number) =>
      done(
        api.DELETE("/api/v1/console/tokens/{id}", {
          params: { path: { id } },
        }),
      ),
    () => [tokensKey, ["audit"]],
    () => "Revoked",
  );

const useSplit = () =>
  useAction(
    (id: number) =>
      body(
        api.POST("/api/v1/console/tokens/{id}/split", {
          params: { path: { id } },
        }),
      ),
    () => [tokensKey, ["audit"]],
  );

/** What a token in the platform's list is. */
function kind(t: Token) {
  if (t.platform === "login") return `platform login of ${t.user}`;
  if (t.platform) return `platform ${t.platform}`;
  if (t.origin === "cli_login") return `legacy: ${t.user}'s dock login`;
  return `legacy: org ${t.org}'s token`;
}

/** Console › Tokens. */
export function PlatformTokensPage() {
  const tokens = useSuspenseQuery(platformTokensQuery()).data;
  const operator = useIsOperator();
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; secret: string }>();
  const [asking, setAsking] = useState<{
    t: Token;
    what: "revoke" | "split";
  }>();
  const revoke = useRevoke();
  const split = useSplit();
  const close = () => setAsking(undefined);
  return (
    <>
      <PageBar
        title="Tokens"
        actions={
          operator &&
          !creating && (
            <Act
              primary
              cli="dock platform token create <name> --role operator|viewer"
              onClick={() => setCreating(true)}
            >
              Create token
            </Act>
          )
        }
      />
      <Page>
        {secret && (
          <section className="flex flex-col gap-2 rounded-panel border border-ok-edge p-4">
            <p>
              <b className="font-semibold">Copy {secret.name} now.</b>{" "}
              <span className="text-mut">The secret is never shown again.</span>
            </p>
            <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
              <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
                {secret.secret}
              </code>
              <CopyButton text={secret.secret} primary />
            </div>
            <p className="font-mono text-xs text-mut">
              export DOCK_PLATFORM_TOKEN=… beside DOCK_TOKEN, or: dock login
              &lt;url&gt; --token-stdin
            </p>
            <div>
              <Button small onClick={() => setSecret(undefined)}>
                I've saved it
              </Button>
            </div>
          </section>
        )}
        {creating && (
          <CreateForm
            onCreated={(s) => {
              setCreating(false);
              setSecret(s);
            }}
            onCancel={() => setCreating(false)}
          />
        )}
        {asking?.what === "revoke" && (
          <Confirm
            danger
            title={`Revoke ${asking.t.name}?`}
            action="Revoke"
            cli={`dock platform token revoke ${asking.t.id}`}
            pending={revoke.isPending}
            error={revoke.error}
            onConfirm={() => revoke.mutate(asking.t.id, { onSuccess: close })}
            onCancel={close}
          >
            Whatever uses it can no longer run the platform.
          </Confirm>
        )}
        {asking?.what === "split" && (
          <Confirm
            title={`Split ${asking.t.name}?`}
            action="Split"
            cli={`dock platform token split ${asking.t.id}`}
            pending={split.isPending}
            error={split.error}
            onConfirm={() =>
              split.mutate(asking.t.id, {
                onSuccess: (r) => {
                  close();
                  if (r.platform.secret)
                    setSecret({
                      name: r.platform.token.name,
                      secret: r.platform.secret,
                    });
                },
              })
            }
            onCancel={close}
          >
            It stays the organization's, with its scope and limits, and stops
            running the platform.{" "}
            {asking.t.origin === "cli_login"
              ? "Its person runs dock login --platform for the platform."
              : "A new platform token with the operator role takes that over; put it where the old one ran platform commands."}
          </Confirm>
        )}
        {tokens.length === 0 ? (
          <p className="text-faint">No platform tokens.</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Token</th>
                <th>What</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id} className="*:align-middle hover:bg-hover">
                  <td className="py-2 font-mono">{t.name}</td>
                  <td className={t.operator ? "py-2 text-warn" : "py-2"}>
                    {kind(t)}
                  </td>
                  <td className="py-2 text-mut">
                    {t.last_used_at ? ago(t.last_used_at) : "never"}
                  </td>
                  <td className="py-2 text-right whitespace-nowrap">
                    {operator && t.operator && (
                      <Act
                        small
                        cli={`dock platform token split ${t.id}`}
                        onClick={() => setAsking({ t, what: "split" })}
                      >
                        Split
                      </Act>
                    )}{" "}
                    {operator && (
                      <Act
                        small
                        cli={`dock platform token revoke ${t.id}`}
                        onClick={() => setAsking({ t, what: "revoke" })}
                      >
                        Revoke
                      </Act>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Page>
    </>
  );
}

/** Creating a platform token: a name, a role and when it expires. */
function CreateForm({
  onCreated,
  onCancel,
}: {
  onCreated: (s: { name: string; secret: string }) => void;
  onCancel: () => void;
}) {
  const create = useCreate();
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [days, setDays] = useState(90);
  return (
    <form
      className="flex max-w-[760px] flex-wrap items-end gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate(
          { name: name.trim(), role, expires_in_days: days },
          {
            onSuccess: (c) =>
              onCreated({ name: c.token.name, secret: c.secret }),
          },
        );
      }}
    >
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">Name</span>
        <input
          className="field"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="backup-check"
        />
      </label>
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">Platform role</span>
        <select
          className="field"
          value={role}
          onChange={(e) =>
            setRole(e.target.value === "operator" ? "operator" : "viewer")
          }
        >
          <option value="viewer">viewer: reads the console</option>
          <option value="operator">operator: runs the platform</option>
        </select>
      </label>
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">Expires in days (0: never)</span>
        <input
          className="field w-24"
          type="number"
          min={0}
          value={days}
          onChange={(e) =>
            setDays(Math.max(0, Math.floor(Number(e.target.value))))
          }
        />
      </label>
      <Act
        primary
        type="submit"
        disabled={!name.trim() || create.isPending}
        cli={`dock platform token create ${name.trim() || "<name>"} --role ${role}${days ? ` --expires ${days}d` : ""}`}
      >
        Create
      </Act>
      <Button onClick={onCancel}>Cancel</Button>
      {create.error && <ErrorText error={create.error} />}
    </form>
  );
}
