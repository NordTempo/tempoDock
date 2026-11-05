import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError, api, body } from "../api/client";
import type { components } from "../api/schema";
import { useLogin, useLoginVerify } from "../data/mutations";
import { currentMe, keys, queries } from "../data/queries";
import { CodeInput } from "../ui/CodeInput";
import { ErrorText } from "../ui/kit";

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>): { redirect?: string } =>
    typeof search.redirect === "string" ? { redirect: search.redirect } : {},
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (session) throw redirect({ to: "/" });
  },
  component: Login,
});

const field = "field px-3 py-2.5 text-md";

/** What finishes a sign-in after the password (ADR 0057). */
type SecondFactor = NonNullable<
  components["schemas"]["LoginResult"]["second_factor"]
>[number];

/**
 * Asks the browser for a passkey answering the challenge purpose starts:
 * "login" signs in with it alone, "second" finishes a password sign-in.
 * The WebAuthn helpers load on first use, not with the page.
 */
async function askPasskey(purpose: "login" | "second") {
  const [{ getPasskey }, challenge] = await Promise.all([
    import("../api/webauthn"),
    body(api.POST("/api/v1/auth/passkey/options", { body: { purpose } })),
  ]);
  return getPasskey(challenge);
}

/** Signs in with a passkey alone (no password): it is both factors. */
function usePasskeyLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const passkey = await askPasskey("login");
      await body(api.POST("/api/v1/auth/passkey", { body: passkey }));
      queryClient.setQueryData(keys.session, await currentMe());
    },
  });
}

/** Whether this page can offer passkeys (https or localhost, WebAuthn). */
const passkeys = window.isSecureContext && "PublicKeyCredential" in window;

function Login() {
  const { redirect: back } = Route.useSearch();
  const navigate = useNavigate();
  const login = useLogin();
  const passkeyLogin = usePasskeyLogin();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  // Set while the password was right and a second factor is due.
  const [factors, setFactors] = useState<SecondFactor[] | null>(null);
  // Why the second step ended (expired, too many wrong codes).
  const [ended, setEnded] = useState<Error | null>(null);
  // Same-origin paths only, never an absolute URL from the query.
  const done = () => navigate({ href: back?.startsWith("/") ? back : "/" });
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => body(api.GET("/api/v1/health")),
    retry: false,
  });
  const host = window.location.host;

  return (
    <div className="flex min-h-screen flex-col items-center justify-center px-4 py-10">
      <main className="flex w-full max-w-[360px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">Sign in</h1>
          <span className="font-mono text-xs text-mut">{host}</span>
        </div>
        {factors ? (
          <SecondStep
            email={email}
            factors={factors}
            onDone={done}
            onEnded={(error) => {
              setFactors(null);
              setPassword("");
              setEnded(error);
            }}
          />
        ) : (
          <form
            className="flex flex-col gap-3.5"
            onSubmit={(e) => {
              e.preventDefault();
              setEnded(null);
              login.mutate(
                { email, password },
                {
                  onSuccess: (second) => {
                    if (second) setFactors(second);
                    else done();
                  },
                },
              );
            }}
          >
            <label className="flex flex-col gap-1.5">
              <span>Email</span>
              <input
                // biome-ignore lint/a11y/noAutofocus: the only thing to do on this page
                autoFocus
                type="email"
                autoComplete="username"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className={field}
              />
            </label>
            <label className="flex flex-col gap-1.5">
              <span>Password</span>
              <input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className={field}
              />
            </label>
            <button
              type="submit"
              disabled={login.isPending || passkeyLogin.isPending}
              className="btn pri justify-center px-3.5 py-2.5 text-md"
            >
              Sign in <span className="font-mono text-2xs opacity-70">↵</span>
            </button>
            {passkeys && (
              <button
                type="button"
                disabled={login.isPending || passkeyLogin.isPending}
                onClick={() => {
                  setEnded(null);
                  login.reset();
                  passkeyLogin.mutate(undefined, { onSuccess: done });
                }}
                className="btn justify-center px-3.5 py-2.5 text-md"
              >
                Sign in with a passkey
              </button>
            )}
            <ErrorText error={ended ?? login.error ?? passkeyLogin.error} />
          </form>
        )}
        <div className="flex flex-col gap-1 border-t border-line pt-4">
          <span className="text-mut">From a terminal</span>
          <code className="font-mono">dock login {window.location.origin}</code>
        </div>
        <p className="text-xs text-mut">
          {health.isSuccess ? (
            <>
              <span className="text-ok">●</span> control plane online ·{" "}
              {health.data.version}
            </>
          ) : health.isError ? (
            <>
              <span className="text-fail">●</span> control plane not reachable
            </>
          ) : (
            " "
          )}
        </p>
      </main>
    </div>
  );
}

/** How each second factor is offered when it isn't the one shown. */
const useInstead: Record<SecondFactor, string> = {
  passkey: "Use a passkey",
  totp: "Use your authenticator app",
  recovery: "Use a recovery code",
};

/**
 * The sign-in's second step: a passkey, the code from the authenticator
 * app (sent as soon as six digits are in), or one of the recovery codes.
 */
function SecondStep({
  email,
  factors,
  onDone,
  onEnded,
}: {
  email: string;
  factors: SecondFactor[];
  onDone: () => void;
  /** Back to the password, with why (null: the person went back). */
  onEnded: (error: Error | null) => void;
}) {
  const verify = useLoginVerify();
  // The browser's part of a passkey; verify sends its answer.
  const passkey = useMutation({ mutationFn: () => askPasskey("second") });
  const [mode, setMode] = useState<SecondFactor>(
    factors.find((f) => f !== "passkey" || passkeys) ?? "recovery",
  );
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState("");
  // The pending sign-in expired or saw too many wrong factors.
  const ended = (error: Error) => {
    if (!(error instanceof ApiError && error.status === 410)) return false;
    onEnded(error);
    return true;
  };
  const send = (v: components["schemas"]["Verification"]) =>
    verify.mutate(v, {
      onSuccess: onDone,
      onError: (error) => {
        if (!ended(error)) setCode("");
      },
    });
  const withPasskey = () =>
    passkey.mutate(undefined, {
      onSuccess: (answer) => send({ passkey: answer }),
      onError: ended,
    });
  const pending = verify.isPending || passkey.isPending;

  return (
    <form
      className="flex flex-col gap-3.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (mode === "passkey") withPasskey();
        if (mode === "totp" && code.length === 6) send({ code });
        if (mode === "recovery" && recovery.trim()) {
          send({ recovery_code: recovery.trim() });
        }
      }}
    >
      <p className="text-mut">
        <span className="font-mono">{email}</span> ·{" "}
        <button
          type="button"
          className="text-info hover:underline"
          onClick={() => onEnded(null)}
        >
          not you?
        </button>
      </p>
      {mode === "passkey" ? null : mode === "totp" ? (
        <CodeInput
          label="Code from your authenticator app"
          autoFocus
          value={code}
          onChange={setCode}
          onComplete={(c) => send({ code: c })}
          disabled={pending}
          className="px-3 py-2.5 text-center text-xl"
        />
      ) : (
        <label className="flex flex-col gap-1.5">
          <span>Recovery code</span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the only thing to do in this step
            autoFocus
            value={recovery}
            onChange={(e) => setRecovery(e.target.value)}
            disabled={pending}
            autoComplete="off"
            spellCheck={false}
            className={`${field} font-mono`}
          />
        </label>
      )}
      <button
        type="submit"
        // biome-ignore lint/a11y/noAutofocus: the only thing to do in this step
        autoFocus={mode === "passkey"}
        disabled={
          pending ||
          (mode === "totp" && code.length < 6) ||
          (mode === "recovery" && !recovery.trim())
        }
        className="btn pri justify-center px-3.5 py-2.5 text-md"
      >
        {mode === "passkey" ? "Use a passkey" : "Verify"}{" "}
        <span className="font-mono text-2xs opacity-70">↵</span>
      </button>
      {factors
        .filter((f) => f !== mode && (f !== "passkey" || passkeys))
        .map((f) => (
          <button
            key={f}
            type="button"
            className="self-start text-info hover:underline"
            onClick={() => {
              verify.reset();
              passkey.reset();
              setMode(f);
            }}
          >
            {useInstead[f]}
          </button>
        ))}
      {mode === "recovery" && (
        <p className="text-xs text-mut">Each recovery code works once.</p>
      )}
      <ErrorText error={passkey.error ?? verify.error} />
    </form>
  );
}
