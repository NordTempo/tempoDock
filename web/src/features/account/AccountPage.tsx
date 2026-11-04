import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { api, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useIsAdmin, useSession } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { ErrorText, Page, PageBar, SectionHead } from "../../ui/kit";
import { roleNote } from "../org/data";
import { useOwnTokens } from "../tokens/data";
import { TokenTable } from "../tokens/TokenTable";

// The signed-in person's own account (`dock whoami`, `dock token list
// --mine`, `dock me password`): who they are, their role, their tokens and
// changing their password.

/** The API's minimum for a new password. */
const MIN_PASSWORD = 12;

/**
 * Changes the caller's password. The answer sets a fresh session cookie;
 * every other session and `dock login` token of theirs ends.
 */
const useChangePassword = () =>
  useMutation({
    mutationFn: (req: components["schemas"]["ChangePasswordRequest"]) =>
      done(api.POST("/api/v1/me/password", { body: req })),
  });

export function AccountPage() {
  const me = useSession();
  if (!me) return null;
  return (
    <>
      <PageBar
        title="Account"
        actions={
          <span className="font-mono text-2xs text-mut">dock whoami</span>
        }
      />
      <Page width="max-w-[760px]">
        <dl className="props">
          <dt>Name</dt>
          <dd>{me.name}</dd>
          {me.email && (
            <>
              <dt>Email</dt>
              <dd className="font-mono">{me.email}</dd>
            </>
          )}
          {me.org && (
            <>
              <dt>Organization</dt>
              <dd className="font-mono">{me.org}</dd>
            </>
          )}
          {me.scope && (
            <>
              <dt>Role</dt>
              <dd>
                <b className="font-medium">{me.scope}</b>{" "}
                <span className="text-mut">{roleNote[me.scope]}</span>
              </dd>
            </>
          )}
          {me.operator && (
            <>
              <dt>Platform</dt>
              <dd>
                <b className="font-medium">operator</b>{" "}
                <span className="text-mut">
                  Runs servers, backups and accounts in the console.
                </span>
              </dd>
            </>
          )}
          {me.project && (
            <>
              <dt>Limited to</dt>
              <dd className="font-mono">
                {me.project}
                {me.environment && `/${me.environment}`}
              </dd>
            </>
          )}
        </dl>
        {me.kind === "user" ? (
          <>
            <OwnTokens />
            <ChangePassword />
          </>
        ) : (
          <p className="text-mut">
            This session is a token; tokens have no password.
          </p>
        )}
      </Page>
    </>
  );
}

/**
 * The caller's `dock login` tokens and those they created, which they may
 * revoke with any role. Admins create tokens in the organization's settings.
 */
function OwnTokens() {
  const tokens = useOwnTokens();
  const admin = useIsAdmin();
  const org = useOrg();
  return (
    <section aria-labelledby="own-tokens" className="flex flex-col gap-3">
      <SectionHead id="own-tokens" aside="dock token list --mine">
        Your tokens
      </SectionHead>
      <TokenTable tokens={tokens} mine />
      {admin && (
        <p className="text-mut">
          Create tokens for agents, CI and scripts in{" "}
          <Link to="/$org/settings/tokens" params={{ org }}>
            the organization's tokens
          </Link>
          .
        </p>
      )}
    </section>
  );
}

function ChangePassword() {
  const change = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const short = next.length > 0 && next.length < MIN_PASSWORD;
  const mismatch = again.length > 0 && again !== next;
  const ready =
    !!current &&
    next.length >= MIN_PASSWORD &&
    again === next &&
    !change.isPending;
  // Typing again clears the last answer.
  const edit = (set: (value: string) => void) => (value: string) => {
    set(value);
    change.reset();
  };

  return (
    <section aria-labelledby="password" className="flex flex-col gap-3">
      <SectionHead id="password" aside="dock me password">
        Change password
      </SectionHead>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!ready) return;
          change.mutate(
            { current_password: current, new_password: next },
            {
              onSuccess: () => {
                setCurrent("");
                setNext("");
                setAgain("");
              },
            },
          );
        }}
      >
        <Password
          label="Current password"
          autoComplete="current-password"
          value={current}
          onChange={edit(setCurrent)}
        />
        <Password
          label="New password"
          note={
            <span className={short ? "text-warn" : undefined}>
              at least {MIN_PASSWORD} characters
            </span>
          }
          value={next}
          onChange={edit(setNext)}
        />
        <Password
          label="New password again"
          note={mismatch && <span className="text-warn">doesn't match</span>}
          value={again}
          onChange={edit(setAgain)}
        />
        <div className="flex flex-wrap items-center gap-3">
          <button type="submit" className="btn pri" disabled={!ready}>
            Change password
          </button>
          {change.isSuccess && (
            <span>
              <span className="text-ok">✓</span> Changed. Your other sessions
              and <span className="font-mono">dock login</span> tokens ended;
              log in to the CLI again with{" "}
              <span className="font-mono">dock login</span>.
            </span>
          )}
          <ErrorText error={change.error} />
        </div>
      </form>
    </section>
  );
}

/** A password input with its label and a note after the label. */
function Password({
  label,
  note,
  value,
  onChange,
  autoComplete = "new-password",
}: {
  label: string;
  note?: ReactNode;
  value: string;
  onChange: (value: string) => void;
  autoComplete?: string;
}) {
  return (
    <label className="flex max-w-[360px] flex-col gap-1.5">
      <span>
        {label} {note && <span className="text-xs text-faint">{note}</span>}
      </span>
      <input
        type="password"
        autoComplete={autoComplete}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="field py-[7px]"
      />
    </label>
  );
}
