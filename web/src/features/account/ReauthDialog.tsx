import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { getPasskey, passkeysAvailable } from "../../api/webauthn";
import { CodeInput } from "../../ui/CodeInput";
import { Dialog } from "../../ui/Dialog";
import { Button, ErrorText } from "../../ui/kit";
import { twoFactorQuery } from "./data";

type Verification = components["schemas"]["Verification"];

/**
 * "Confirm it's you" before a sensitive action (ADR 0057): a passkey or
 * the code from the authenticator app when the person has one, else their
 * password. Loaded by ui/ReauthHost when a call answers reauth_required;
 * onDone says whether to retry it.
 */
export default function ReauthDialog({
  onDone,
}: {
  onDone: (ok: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const twoFactor = useQuery(twoFactorQuery());
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const factors = twoFactor.data?.factors ?? [];
  const withPasskey =
    passkeysAvailable() && factors.some((f) => f.kind === "passkey");
  const withApp = factors.some((f) => f.kind === "totp");
  // A passkey user without an app on a page that can't use passkeys
  // still sees the code field; the daemon says what is wrong.
  const withCode = withApp || (factors.length > 0 && !withPasskey);
  const passkeyOnly = withPasskey && !withCode;

  // Not a useMutation: a wrong password answers 401, which the mutation
  // cache would take for a lost session and sign out.
  async function confirm(verification: () => Promise<Verification>) {
    setPending(true);
    setError(null);
    try {
      const v = await verification();
      await done(api.POST("/api/v1/me/reauth", { body: v }));
      void queryClient.invalidateQueries({
        queryKey: twoFactorQuery().queryKey,
      });
      onDone(true);
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
      setCode("");
      setPending(false);
    }
  }
  const confirmPasskey = () =>
    confirm(async () => ({
      passkey: await getPasskey(
        await body(api.POST("/api/v1/me/reauth/options")),
      ),
    }));

  // Opens once it knows which to ask for, so the field gets focus.
  if (twoFactor.isPending) return null;
  return (
    <Dialog title="Confirm it's you" onClose={() => onDone(false)}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (withCode) {
            if (code.length === 6) void confirm(async () => ({ code }));
          } else if (withPasskey) void confirmPasskey();
          else if (password) void confirm(async () => ({ password }));
        }}
      >
        <p className="text-mut">
          You signed in or confirmed more than 15 minutes ago.
        </p>
        {withPasskey && withCode && (
          <span>
            <Button disabled={pending} onClick={() => void confirmPasskey()}>
              Use a passkey
            </Button>
          </span>
        )}
        {withCode ? (
          <CodeInput
            label={
              withPasskey
                ? "Or a code from your authenticator app"
                : "Code from your authenticator app"
            }
            data-autofocus
            value={code}
            onChange={setCode}
            onComplete={(c) => void confirm(async () => ({ code: c }))}
            disabled={pending}
            className="px-3 py-2 text-lg"
          />
        ) : (
          !withPasskey && (
            <label className="flex flex-col gap-1.5">
              <span>Password</span>
              <input
                data-autofocus
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={pending}
                className="field py-[7px]"
              />
            </label>
          )
        )}
        <ErrorText error={error ?? twoFactor.error} />
        <div className="flex gap-2">
          <Button
            primary
            type="submit"
            data-autofocus={passkeyOnly || undefined}
            disabled={
              pending ||
              (withCode ? code.length < 6 : !passkeyOnly && !password)
            }
          >
            {passkeyOnly ? "Use a passkey" : "Confirm"}
          </Button>
          <Button onClick={() => onDone(false)}>Cancel</Button>
        </div>
      </form>
    </Dialog>
  );
}
