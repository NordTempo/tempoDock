import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError, api, body, setOrg } from "../api/client";
import type { components } from "../api/schema";
import { useLogin, useLoginVerify } from "../data/mutations";
import { currentMe, keys, queries } from "../data/queries";
import { type SSOChoice, useStartSSO } from "../features/sso/data";
import { CodeInput } from "../ui/CodeInput";
import { ErrorText } from "../ui/kit";

/**
 * The sign-in page's query: where to go afterwards, and where a single
 * sign-on comes back to (ADR 0058): `error` says why it failed, `second`
 * that it needs the Dock second factor (the pending sign-in's cookie is
 * already set).
 */
type LoginSearch = { redirect?: string; error?: string; second?: true };

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>): LoginSearch => ({
    ...(typeof search.redirect === "string" && { redirect: search.redirect }),
    ...(typeof search.error === "string" && { error: search.error }),
    ...((search.second === 1 || search.second === true) && {
      second: true,
    }),
  }),
  // A single sign-on started from a session (an org requiring it) comes
  // back here too: its second step or error is shown, not skipped.
  beforeLoad: async ({ context, search }) => {
    if (search.second || search.error) return;
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

/**
 * The factors a single sign-on's second step offers: the page can't know
 * which the person has, so the app comes first and a passkey is a switch.
 */
const ssoFactors: SecondFactor[] = ["totp", "passkey", "recovery"];

/** Whether this page can offer passkeys (https or localhost, WebAuthn). */
const passkeys = window.isSecureContext && "PublicKeyCredential" in window;

function Login() {
  const { redirect: back, error, second } = Route.useSearch();
  const navigate = useNavigate();
  const login = useLogin();
  const passkeyLogin = usePasskeyLogin();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  // Signing in with SSO instead of a password; where a failed one comes
  // back to.
  const [sso, setSSO] = useState(!!error);
  // Set while the password (or single sign-on) was right and a second
  // factor is due.
  const [factors, setFactors] = useState<SecondFactor[] | null>(
    second ? ssoFactors : null,
  );
  // Why the second step ended (expired, too many wrong codes).
  const [ended, setEnded] = useState<Error | null>(null);
  // Same-origin paths only, never an absolute URL from the query.
  const to = back?.startsWith("/") ? back : undefined;
  const done = () => navigate({ href: to ?? "/" });
  // A single sign-on's answer is read once; starting over drops it.
  const fresh = () =>
    navigate({ to: "/login", search: { redirect: back }, replace: true });
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
            email={second ? undefined : email}
            factors={factors}
            onDone={done}
            onEnded={(error) => {
              setFactors(null);
              setPassword("");
              setEnded(error);
              if (second) void fresh();
            }}
          />
        ) : sso ? (
          <SSOForm
            email={email}
            onEmail={setEmail}
            to={to}
            error={error}
            onPassword={() => {
              setSSO(false);
              if (error) void fresh();
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
            <button
              type="button"
              className="btn justify-center px-3.5 py-2.5 text-md"
              onClick={() => {
                login.reset();
                passkeyLogin.reset();
                setEnded(null);
                setSSO(true);
              }}
            >
              Sign in with SSO
            </button>
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
  /** Who signed in; unknown after a single sign-on. */
  email?: string;
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
        {email ? (
          <span className="font-mono">{email}</span>
        ) : (
          "Signed in with SSO"
        )}{" "}
        ·{" "}
        <button
          type="button"
          className="text-info hover:underline"
          onClick={() => onEnded(null)}
        >
          {email ? "not you?" : "start over"}
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

/**
 * Signing in with SSO (ADR 0058): the email's domain finds the
 * organization's identity provider and the browser goes there. When
 * several organizations or providers sign that domain in, the person
 * picks one. `error` is why the last single sign-on failed.
 */
function SSOForm({
  email,
  onEmail,
  to,
  error,
  onPassword,
}: {
  email: string;
  onEmail: (email: string) => void;
  to: string | undefined;
  error: string | undefined;
  onPassword: () => void;
}) {
  const start = useStartSSO();
  const [choices, setChoices] = useState<SSOChoice[]>([]);
  const leaving = start.isPending || (start.isSuccess && start.data === null);
  // The session lands in the organization it signed in through.
  const go = (req: { email: string } | SSOChoice) => {
    setOrg("org" in req ? req.org : undefined);
    start.mutate(
      "org" in req
        ? { org: req.org, provider: req.provider, to }
        : { email: req.email, to },
      { onSuccess: (c) => setChoices(c ?? []) },
    );
  };
  return (
    <form
      className="flex flex-col gap-3.5"
      onSubmit={(e) => {
        e.preventDefault();
        go({ email: email.trim() });
      }}
    >
      <label className="flex flex-col gap-1.5">
        <span>Work email</span>
        <input
          // biome-ignore lint/a11y/noAutofocus: the only thing to do in this step
          autoFocus
          type="email"
          autoComplete="username"
          value={email}
          onChange={(e) => {
            onEmail(e.target.value);
            setChoices([]);
          }}
          className={field}
        />
      </label>
      {choices.length > 0 ? (
        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1.5">Sign in through</legend>
          {choices.map((c) => (
            <button
              key={`${c.org}/${c.provider}`}
              type="button"
              disabled={leaving}
              onClick={() => go(c)}
              className="btn justify-between px-3.5 py-2.5 text-md"
            >
              <span>{c.org_name}</span>
              <span className="font-mono text-xs text-mut">{c.provider}</span>
            </button>
          ))}
        </fieldset>
      ) : (
        <button
          type="submit"
          disabled={leaving || !email.includes("@")}
          className="btn pri justify-center px-3.5 py-2.5 text-md"
        >
          Continue with SSO{" "}
          <span className="font-mono text-2xs opacity-70">↵</span>
        </button>
      )}
      <ErrorText error={start.error} />
      {!start.error && error && <span className="text-fail">{error}</span>}
      <button
        type="button"
        className="self-start text-info hover:underline"
        onClick={onPassword}
      >
        Use your password
      </button>
    </form>
  );
}
