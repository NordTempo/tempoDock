import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useId, useRef, useState } from "react";
import { ApiError, api, body, done } from "../api/client";
import type { components } from "../api/schema";
import { useAction, useLogin, useLoginVerify } from "../data/mutations";
import { currentMe, keys, queries } from "../data/queries";
import { useSignInMethods, useStartSSO } from "../features/sso/data";
import { ProviderMark } from "../features/sso/ProviderMark";
import { CodeInput } from "../ui/CodeInput";
import { ErrorText } from "../ui/kit";

/**
 * The sign-in page's query: where to go afterwards, and where a sign-in
 * through an identity provider comes back to (ADR 0065): `error` says why
 * it failed, `second` that it needs the Dock second factor (the pending
 * sign-in's cookie is already set).
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
  // A provider's sign-in started from a session (a step-up) comes back
  // here too: its second step or error is shown, not skipped.
  beforeLoad: async ({ context, search }) => {
    if (search.second || search.error) return;
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (session) throw redirect({ to: "/" });
  },
  component: Login,
});

const field = "field px-3 py-2.5 text-md aria-invalid:border-fail";

/** A field's own message, shown under it. */
type FieldErrors = { email?: string; password?: string };

/** Checks the form before it is sent, so the server's wording never shows. */
function check(email: string, password: string): FieldErrors {
  const e = email.trim();
  return {
    email: !e
      ? "Enter your email."
      : !/^[^\s@]+@[^\s@]+$/.test(e)
        ? "Enter an email address, like name@company.com."
        : undefined,
    password: password ? undefined : "Enter your password.",
  };
}

/** An API message as a sentence: capital first letter, full stop. */
const sentence = (text: string) => {
  const t = text.trim();
  if (!t) return t;
  const s = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
};

/** A failed sign-in in the words the page uses. */
function loginMessage(error: Error) {
  if (error instanceof ApiError && error.status === 401) {
    return "Wrong email or password.";
  }
  if (error instanceof ApiError && error.status === 422) {
    return "Check the email and password, then try again.";
  }
  return sentence(error.message);
}

/**
 * Why the visitor is asked to sign in, from where they were going: an
 * invite link, a CLI login, a protected service, an access request or any
 * other page send them here first.
 */
function purpose(back: string | undefined): { title: string; note?: string } {
  const path = back?.split("?")[0] ?? "";
  const query = new URLSearchParams(back?.split("?")[1]);
  if (path.startsWith("/invite/")) {
    return {
      title: "Sign in to see your invite",
      note: "Use the email address the invite was sent to. No account yet? Go back to the invite link and create one there.",
    };
  }
  if (path === "/cli-login") {
    return {
      title: "Sign in to approve a dock login",
      note: "Then type the code your terminal shows.",
    };
  }
  if (path === "/access") {
    const host = query.get("host");
    return {
      title: host ? `Sign in to open ${host}` : "Sign in to open a service",
      note: "The service is protected by this Dock: only its members get in.",
    };
  }
  if (/^\/[^/]+\/(requests|inbox)$/.test(path)) {
    const id = query.get("request") ?? query.get("id");
    return {
      title: id
        ? `Sign in to review request #${id}`
        : "Sign in to review access requests",
      note: "Admins of the organization approve or reject them.",
    };
  }
  if (path === "/no-org") {
    return {
      title: "Sign in to Dock",
      note: "Signing in opens your organization, or says how to join one.",
    };
  }
  if (path && path !== "/") {
    return { title: "Sign in", note: `Signing in takes you on to ${path}.` };
  }
  return { title: "Sign in" };
}

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
  return useAction(
    async () => {
      const passkey = await askPasskey("login");
      await body(api.POST("/api/v1/auth/passkey", { body: passkey }));
      queryClient.setQueryData(keys.session, await currentMe());
    },
    () => [],
  );
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
  // Set by a submit the page checked first; cleared as the field changes.
  const [invalid, setInvalid] = useState<FieldErrors>({});
  const emailRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const ids = useId();
  // The buttons, one per identity provider, and whether passwords work.
  const methods = useSignInMethods();
  const start = useStartSSO();
  const passwords = methods.data?.password ?? true;
  const providers = methods.data?.providers ?? [];
  // Set while the password (or single sign-on) was right and a second
  // factor is due.
  const [factors, setFactors] = useState<SecondFactor[] | null>(
    second ? ssoFactors : null,
  );
  // Why the second step ended (expired, too many wrong codes).
  const [ended, setEnded] = useState<Error | null>(null);
  // Same-origin paths only, never an absolute URL from the query.
  const to = back?.startsWith("/") && !back.startsWith("//") ? back : undefined;
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
  const why = purpose(back);
  const failed = ended ?? login.error ?? passkeyLogin.error;

  return (
    <div className="flex min-h-screen flex-col items-center px-4 pt-[16vh] pb-10">
      <main className="flex w-full max-w-[420px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">{why.title}</h1>
          <span className="font-mono text-xs text-mut">{host}</span>
          {why.note && <p className="pt-1 text-sec">{why.note}</p>}
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
        ) : (
          <div className="flex flex-col gap-3.5">
            {error && (
              <p role="alert" className="text-fail">
                {sentence(error)}
              </p>
            )}
            {providers.length > 0 && (
              <div className="flex flex-col gap-2">
                {providers.map((p) => (
                  <button
                    key={p.name}
                    type="button"
                    disabled={start.isPending || start.isSuccess}
                    onClick={() => {
                      if (error) void fresh();
                      start.mutate({ provider: p.name, to });
                    }}
                    className="btn justify-center gap-2.5 px-3.5 py-2.5 text-md"
                  >
                    <ProviderMark kind={p.kind} />
                    Continue with {p.label}
                  </button>
                ))}
                <ErrorText error={start.error} />
              </div>
            )}
            {passwords && providers.length > 0 && (
              <div
                aria-hidden
                className="flex items-center gap-2.5 text-xs text-faint before:h-px before:flex-1 before:bg-line after:h-px after:flex-1 after:bg-line"
              >
                or
              </div>
            )}
          </div>
        )}
        {!factors && passwords && (
          <form
            className="flex flex-col gap-3.5"
            noValidate
            onSubmit={(e) => {
              e.preventDefault();
              setEnded(null);
              const found = check(email, password);
              setInvalid(found);
              if (found.email || found.password) {
                login.reset();
                (found.email ? emailRef : passwordRef).current?.focus();
                return;
              }
              login.mutate(
                { email: email.trim(), password },
                {
                  onSuccess: (second) => {
                    if (second) setFactors(second);
                    else done();
                  },
                  onError: (error) => {
                    // A wrong password: type it again, the email stays.
                    if (error instanceof ApiError && error.status === 401) {
                      passwordRef.current?.select();
                      passwordRef.current?.focus();
                    }
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
                ref={emailRef}
                value={email}
                onChange={(e) => {
                  setEmail(e.target.value);
                  setInvalid((v) => ({ ...v, email: undefined }));
                }}
                aria-invalid={!!invalid.email || undefined}
                aria-describedby={invalid.email ? `${ids}-email` : undefined}
                className={field}
              />
              <FieldError id={`${ids}-email`} text={invalid.email} />
            </label>
            <label className="flex flex-col gap-1.5">
              <span>Password</span>
              <input
                type="password"
                autoComplete="current-password"
                ref={passwordRef}
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value);
                  setInvalid((v) => ({ ...v, password: undefined }));
                }}
                aria-invalid={!!invalid.password || undefined}
                aria-describedby={
                  invalid.password ? `${ids}-password` : undefined
                }
                className={field}
              />
              <FieldError id={`${ids}-password`} text={invalid.password} />
            </label>
            <Forgot
              email={email}
              canEmail={methods.data?.password_reset ?? false}
            />
            {/* A line kept for the answer, so a failed sign-in appears in
                place, as a red line above the button, and nothing moves. */}
            <p role="alert" className="min-h-[1lh] text-fail">
              {failed && loginMessage(failed)}
            </p>
            <button
              type="submit"
              disabled={login.isPending || passkeyLogin.isPending}
              className="btn pri -mt-1.5 justify-center px-3.5 py-2.5 text-md"
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
          </form>
        )}
        {!factors && !passwords && (
          <div className="flex flex-col gap-3.5">
            {passkeys && (
              <button
                type="button"
                disabled={passkeyLogin.isPending}
                onClick={() =>
                  passkeyLogin.mutate(undefined, { onSuccess: done })
                }
                className="btn justify-center px-3.5 py-2.5 text-md"
              >
                Sign in with a passkey
              </button>
            )}
            <ErrorText error={passkeyLogin.error} />
            <p className="text-xs text-mut">
              This Dock signs in only through
              {providers.length === 1 ? " the provider" : " the providers"}{" "}
              above.
            </p>
          </div>
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
  const passkey = useAction(
    () => askPasskey("second"),
    () => [],
  );
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
 * "Forgot password?" under the password: Dock sends no email, so it says
 * who can reset one.
 */
/**
 * "Forgot password?": with email that carries links set up (ADR 0079),
 * it emails a reset link to the address typed above; without, it says
 * who resets passwords. The answer is the same whether or not the
 * address has an account.
 */
function Forgot({ email, canEmail }: { email: string; canEmail: boolean }) {
  const [open, setOpen] = useState(false);
  const ask = useAction(
    (address: string) =>
      done(
        api.POST("/api/v1/auth/password-reset", { body: { email: address } }),
      ),
    () => [],
  );
  const address = email.trim();
  if (!open) {
    return (
      <button
        type="button"
        aria-expanded={false}
        className="-mt-1.5 min-h-[24px] self-start text-xs text-mut hover:text-fg hover:underline"
        onClick={() => setOpen(true)}
      >
        Forgot password?
      </button>
    );
  }
  if (!canEmail) {
    return (
      <p className="-mt-1.5 text-xs text-sec">
        This Dock can't email reset links. Ask an operator to reset it on the
        console's Users page, or with{" "}
        <code className="font-mono">
          dock user reset-password &lt;email&gt;
        </code>
        .
      </p>
    );
  }
  if (ask.isSuccess && ask.variables === address) {
    return (
      <p role="status" className="-mt-1.5 text-xs text-sec">
        <span className="text-ok">✓</span> If {address} has an account here, a
        link to set a new password is on its way. It works for an hour.
      </p>
    );
  }
  const valid = /^[^\s@]+@[^\s@]+$/.test(address);
  return (
    <div className="-mt-1.5 flex flex-col gap-1.5 text-xs text-sec">
      <span>
        {valid
          ? `Email a link that sets a new password to ${address}?`
          : "Type your email above, and Dock emails you a link that sets a new password."}
      </span>
      {valid && (
        <button
          type="button"
          disabled={ask.isPending}
          onClick={() => ask.mutate(address)}
          className="btn sm self-start"
        >
          Email me a reset link
        </button>
      )}
      <ErrorText error={ask.error} />
    </div>
  );
}

/** A field's message under it, or nothing. */
function FieldError({ id, text }: { id: string; text?: string }) {
  if (!text) return null;
  return (
    <span id={id} className="text-xs text-fail">
      {text}
    </span>
  );
}
