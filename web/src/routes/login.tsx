import { useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError, api, body } from "../api/client";
import type { components } from "../api/schema";
import { useLogin, useLoginVerify } from "../data/mutations";
import { queries } from "../data/queries";
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

/** What finishes a sign-in after the password (ADR 0050). */
type SecondFactor = NonNullable<
  components["schemas"]["LoginResult"]["second_factor"]
>[number];

function Login() {
  const { redirect: back } = Route.useSearch();
  const navigate = useNavigate();
  const login = useLogin();
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
              disabled={login.isPending}
              className="btn pri justify-center px-3.5 py-2.5 text-md"
            >
              Sign in <span className="font-mono text-2xs opacity-70">↵</span>
            </button>
            <ErrorText error={ended ?? login.error} />
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

/**
 * The sign-in's second step: the code from the authenticator app (sent
 * as soon as six digits are in), or one of the recovery codes.
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
  const [mode, setMode] = useState<SecondFactor>(
    factors.includes("totp") ? "totp" : "recovery",
  );
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState("");
  const send = (v: { code: string } | { recovery_code: string }) =>
    verify.mutate(v, {
      onSuccess: onDone,
      onError: (error) => {
        // The pending sign-in expired or saw too many wrong codes.
        if (error instanceof ApiError && error.status === 410) onEnded(error);
        else setCode("");
      },
    });
  const other = mode === "totp" ? "recovery" : "totp";

  return (
    <form
      className="flex flex-col gap-3.5"
      onSubmit={(e) => {
        e.preventDefault();
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
      {mode === "totp" ? (
        <CodeInput
          label="Code from your authenticator app"
          autoFocus
          value={code}
          onChange={setCode}
          onComplete={(c) => send({ code: c })}
          disabled={verify.isPending}
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
            disabled={verify.isPending}
            autoComplete="off"
            spellCheck={false}
            className={`${field} font-mono`}
          />
        </label>
      )}
      <button
        type="submit"
        disabled={
          verify.isPending ||
          (mode === "totp" ? code.length < 6 : !recovery.trim())
        }
        className="btn pri justify-center px-3.5 py-2.5 text-md"
      >
        Verify <span className="font-mono text-2xs opacity-70">↵</span>
      </button>
      {factors.includes(other) && (
        <button
          type="button"
          className="self-start text-info hover:underline"
          onClick={() => {
            verify.reset();
            setMode(other);
          }}
        >
          {other === "recovery"
            ? "Use a recovery code"
            : "Use your authenticator app"}
        </button>
      )}
      {mode === "recovery" && (
        <p className="text-xs text-mut">Each recovery code works once.</p>
      )}
      <ErrorText error={verify.error} />
    </form>
  );
}
