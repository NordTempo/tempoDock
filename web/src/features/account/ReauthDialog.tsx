import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, done } from "../../api/client";
import type { components } from "../../api/schema";
import { CodeInput } from "../../ui/CodeInput";
import { Dialog } from "../../ui/Dialog";
import { Button, ErrorText } from "../../ui/kit";
import { twoFactorQuery } from "./data";

/**
 * "Confirm it's you" before a sensitive action (ADR 0050): the code from
 * the authenticator app when the person has one, else their password.
 * Loaded by ui/ReauthHost when a call answers reauth_required; onDone
 * says whether to retry it.
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
  const withApp = (twoFactor.data?.factors.length ?? 0) > 0;

  // Not a useMutation: a wrong password answers 401, which the mutation
  // cache would take for a lost session and sign out.
  async function confirm(v: components["schemas"]["Verification"]) {
    setPending(true);
    setError(null);
    try {
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

  // Opens once it knows which to ask for, so the field gets focus.
  if (twoFactor.isPending) return null;
  return (
    <Dialog title="Confirm it's you" onClose={() => onDone(false)}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (withApp) {
            if (code.length === 6) void confirm({ code });
          } else if (password) void confirm({ password });
        }}
      >
        <p className="text-mut">
          You signed in or confirmed more than 15 minutes ago.
        </p>
        {withApp ? (
          <CodeInput
            label="Code from your authenticator app"
            data-autofocus
            value={code}
            onChange={setCode}
            onComplete={(c) => void confirm({ code: c })}
            disabled={pending}
            className="px-3 py-2 text-lg"
          />
        ) : (
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
        )}
        <ErrorText error={error ?? twoFactor.error} />
        <div className="flex gap-2">
          <Button
            primary
            type="submit"
            disabled={pending || (withApp ? code.length < 6 : !password)}
          >
            Confirm
          </Button>
          <Button onClick={() => onDone(false)}>Cancel</Button>
        </div>
      </form>
    </Dialog>
  );
}
