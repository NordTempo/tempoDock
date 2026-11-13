import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, body, done } from "../api/client";
import { ErrorText } from "../ui/kit";

// A password reset link from the sign-in page's mail (ADR 0079), outside
// the app shell: whoever holds it sets the account's password once,
// within an hour. Setting it ends every session; it doesn't sign in.
export const Route = createFileRoute("/reset-password/$code")({
  component: ResetPassword,
});

/** The rule for every password a person sets (checkNewPassword). */
const MIN_PASSWORD = 12;

const field = "field px-3 py-2.5 text-md aria-invalid:border-fail";

function ResetPassword() {
  const { code } = Route.useParams();
  const path = { params: { path: { code } } };
  const info = useQuery({
    queryKey: ["password-reset", code],
    queryFn: () => body(api.GET("/api/v1/auth/password-reset/{code}", path)),
    retry: false,
  });
  const set = useMutation({
    mutationFn: (password: string) =>
      done(
        api.POST("/api/v1/auth/password-reset/{code}", {
          ...path,
          body: { password },
        }),
      ),
  });
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [tried, setTried] = useState(false);
  const problem =
    password.length < MIN_PASSWORD
      ? `Use at least ${MIN_PASSWORD} characters.`
      : password !== again
        ? "The two passwords differ."
        : undefined;
  return (
    <div className="flex min-h-screen flex-col items-center px-4 pt-[16vh] pb-10">
      <main className="flex w-full max-w-[360px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">Set a new password</h1>
          <span className="font-mono text-xs text-mut">
            {window.location.host}
          </span>
        </div>
        {set.isSuccess ? (
          <div className="flex flex-col gap-3.5">
            <p>
              <span className="text-ok">✓</span> Your password is set. You were
              signed out everywhere; sign in with the new one.
            </p>
            <Link to="/login" className="btn pri justify-center px-3.5 py-2.5">
              Sign in
            </Link>
          </div>
        ) : info.isError ? (
          <div className="flex flex-col gap-3.5">
            <ErrorText error={info.error} />
            <Link to="/login" className="text-info">
              Back to sign in
            </Link>
          </div>
        ) : !info.data ? (
          <p className="text-mut">Checking the link.</p>
        ) : (
          <form
            className="flex flex-col gap-3.5"
            noValidate
            onSubmit={(e) => {
              e.preventDefault();
              setTried(true);
              if (problem || set.isPending) return;
              set.mutate(password);
            }}
          >
            <label className="flex flex-col gap-1.5">
              <span>Email</span>
              <input
                value={info.data.email}
                readOnly
                autoComplete="username"
                className={`${field} font-mono text-mut`}
              />
            </label>
            <label className="flex flex-col gap-1.5">
              <span>
                New password{" "}
                <span className="text-xs text-faint">
                  at least {MIN_PASSWORD} characters
                </span>
              </span>
              <input
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                aria-invalid={(tried && !!problem) || undefined}
                className={field}
              />
            </label>
            <label className="flex flex-col gap-1.5">
              <span>Again</span>
              <input
                type="password"
                autoComplete="new-password"
                value={again}
                onChange={(e) => setAgain(e.target.value)}
                aria-invalid={(tried && !!problem) || undefined}
                className={field}
              />
            </label>
            {tried && problem ? (
              <p role="alert" className="text-xs text-fail">
                {problem}
              </p>
            ) : (
              <ErrorText error={set.error} />
            )}
            <p className="text-xs text-mut">
              Setting it signs you out everywhere. Your second factors stay.
            </p>
            <button
              type="submit"
              disabled={set.isPending}
              className="btn pri justify-center px-3.5 py-2.5 text-md"
            >
              Set password
            </button>
          </form>
        )}
      </main>
    </div>
  );
}
