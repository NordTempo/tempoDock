import {
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { Link, useNavigate, useRouter } from "@tanstack/react-router";
import { type ComponentProps, type ReactNode, useState } from "react";
import { ApiError, api, done, setOrg } from "../../api/client";
import type { components } from "../../api/schema";
import { signedOut, useAction } from "../../data/mutations";
import { keys, queries, useSession } from "../../data/queries";
import { day } from "../../ui/format";
import { Act, Button, ErrorText } from "../../ui/kit";
import { useValidation } from "../create/form";
import { useSignInMethods, useStartSSO } from "../sso/data";
import { ProviderMark } from "../sso/ProviderMark";
import { orgQueries, roleNote, useAcceptInvite, useInviteSignUp } from "./data";

type InviteInfo = components["schemas"]["InviteInfo"];

/**
 * The frame of the invite page and its error: the same narrow column as
 * the sign-in page, outside the app shell.
 */
function Frame({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col items-center px-4 pt-[16vh] pb-10">
      <main className="flex w-full max-w-[420px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">{title}</h1>
          <span className="font-mono text-xs text-mut">
            {window.location.host}
          </span>
        </div>
        {children}
      </main>
    </div>
  );
}

/**
 * Signs out and comes back to this page, for someone signed in as
 * another person than the one invited.
 */
function SwitchAccount() {
  const queryClient = useQueryClient();
  const router = useRouter();
  const out = useAction(
    async () => {
      await done(api.POST("/api/v1/auth/logout"));
      await signedOut(queryClient, router, window.location.pathname);
    },
    () => [],
  );
  return (
    <Button disabled={out.isPending} onClick={() => out.mutate()}>
      Sign out and switch account
    </Button>
  );
}

/**
 * An invite link (/invite/<code>), signed in or out (ADR 0060): which
 * organization, as what, from whom. Signed in as the person invited,
 * joining it (`dock org join <link>`); as someone else, switching
 * account; signed out, signing in to accept or creating the account.
 */
export function InvitePage({ code }: { code: string }) {
  const invite = useSuspenseQuery(orgQueries.invite(code)).data;
  const me = useSession();
  const forYou = me?.email?.toLowerCase() === invite.email.toLowerCase();
  return (
    <Frame title={`Join ${invite.org_name}`}>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-5 gap-y-1.5 [&>dt]:text-mut">
        <dt>Organization</dt>
        <dd>
          {invite.org_name}{" "}
          <span className="font-mono text-mut">{invite.org}</span>
        </dd>
        <dt>Role</dt>
        <dd>
          {invite.role}{" "}
          <span className="text-mut">{roleNote[invite.role]}</span>
        </dd>
        <dt>For</dt>
        <dd className="font-mono">{invite.email}</dd>
        <dt>Invited by</dt>
        <dd className="font-mono">{invite.invited_by}</dd>
        <dt>Works until</dt>
        <dd>{day(invite.expires_at)}</dd>
      </dl>
      {!me ? (
        <SignedOut code={code} invite={invite} />
      ) : !forYou ? (
        <>
          <p>
            You're signed in as{" "}
            <span className="font-mono">{me.email ?? me.name}</span>. The invite
            is for <span className="font-mono">{invite.email}</span>.
          </p>
          <div>
            <SwitchAccount />
          </div>
        </>
      ) : (
        <Accept code={code} invite={invite} />
      )}
    </Frame>
  );
}

/** Joins the organization as the signed-in person the invite is for. */
function Accept({ code, invite }: { code: string; invite: InviteInfo }) {
  const accept = useAcceptInvite();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  return (
    <div className="flex flex-col gap-2">
      <div>
        <Act
          primary
          cli={`dock org join ${window.location.href}`}
          disabled={accept.isPending}
          onClick={() =>
            accept.mutate(code, {
              onSuccess: async (org) => {
                // Act in the org just joined: the session changes.
                setOrg(org.slug);
                await queryClient.refetchQueries({ queryKey: keys.session });
                await navigate({ to: "/$org", params: { org: org.slug } });
              },
            })
          }
        >
          Join {invite.org_name}
        </Act>
      </div>
      <ErrorText error={accept.error} />
    </div>
  );
}

/**
 * The choices of someone signed out: continue with an identity provider
 * (a new account accepts its invites, ADR 0065), sign in with the invited
 * email and come back, or create the account for it right here. While
 * password sign-in is off, only the providers.
 */
function SignedOut({ code, invite }: { code: string; invite: InviteInfo }) {
  const [creating, setCreating] = useState(false);
  const methods = useSignInMethods();
  const start = useStartSSO();
  const back = window.location.pathname;
  const passwords = methods.data?.password ?? true;
  const providers = methods.data?.providers ?? [];
  return (
    <div className="flex flex-col gap-4">
      {providers.length > 0 && (
        <div className="flex flex-col gap-2">
          <p className="text-sec">
            Sign in as <span className="font-mono">{invite.email}</span> to
            accept.
          </p>
          {providers.map((p) => (
            <button
              key={p.name}
              type="button"
              disabled={start.isPending || start.isSuccess}
              onClick={() => start.mutate({ provider: p.name, to: back })}
              className="btn justify-center gap-2.5 px-3.5 py-2.5 text-md"
            >
              <ProviderMark kind={p.kind} />
              Continue with {p.label}
            </button>
          ))}
          <ErrorText error={start.error} />
          <p className="text-xs text-mut">
            Signing in with another email doesn't accept the invite.
          </p>
        </div>
      )}
      {passwords && (
        <div className="flex flex-wrap gap-2">
          <Link
            to="/login"
            search={{ redirect: back }}
            className={providers.length ? "btn" : "btn pri"}
          >
            Sign in {providers.length ? "with a password" : "to accept"}
          </Link>
          {!creating && (
            <Button onClick={() => setCreating(true)}>Create an account</Button>
          )}
        </div>
      )}
      {creating && <CreateAccount code={code} invite={invite} back={back} />}
    </div>
  );
}

/** The API's minimum for a new password. */
const MIN_PASSWORD = 12;

/**
 * Creates the account for the invite's email, which joins the
 * organization and signs in; then the organization opens.
 */
function CreateAccount({
  code,
  invite,
  back,
}: {
  code: string;
  invite: InviteInfo;
  back: string;
}) {
  const signUp = useInviteSignUp();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const short = password.length > 0 && password.length < MIN_PASSWORD;
  const v = useValidation();
  const nameError = v.required(!name.trim(), "Enter your name.");
  const passwordError =
    (short ? `Use at least ${MIN_PASSWORD} characters.` : undefined) ??
    v.required(!password, "Choose a password.");
  const ready = name.trim() !== "" && password.length >= MIN_PASSWORD;
  const exists =
    signUp.error instanceof ApiError && signUp.error.status === 409;
  return (
    <form
      ref={v.form}
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (signUp.isPending || !v.check(ready)) return;
        signUp.mutate(
          { code, name: name.trim(), password },
          {
            onSuccess: () =>
              navigate({ to: "/$org", params: { org: invite.org } }),
          },
        );
      }}
    >
      <Field
        label="Email"
        value={invite.email}
        readOnly
        className="font-mono text-mut"
        autoComplete="username"
      />
      <Field
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        maxLength={64}
        autoComplete="name"
        error={nameError}
      />
      <Field
        label="Password"
        note={`at least ${MIN_PASSWORD} characters`}
        type="password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        autoComplete="new-password"
        error={passwordError}
      />
      <div className="flex flex-wrap items-center gap-3">
        <button type="submit" className="btn pri" disabled={signUp.isPending}>
          Create account and join
        </button>
        {signUp.error ? (
          <ErrorText error={signUp.error} />
        ) : (
          <span className="text-xs text-fail">
            {v.problem(
              [!name.trim() && "your name", !password && "a password"],
              short,
            )}
          </span>
        )}
        {exists && (
          <Link to="/login" search={{ redirect: back }}>
            Sign in
          </Link>
        )}
      </div>
    </form>
  );
}

/**
 * A labelled input of the account form, with a note beside the label and
 * what is wrong with it under it (useValidation).
 */
function Field({
  label,
  note,
  error,
  className = "",
  ...input
}: {
  label: string;
  note?: ReactNode;
  error?: string;
} & ComponentProps<"input">) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-xs text-mut">
        {label} {note && <span className="text-faint">{note}</span>}
      </span>
      <input
        {...input}
        aria-invalid={!!error}
        className={`field ${error ? "border-fail" : ""} ${className}`}
      />
      {error && (
        <span role="alert" className="text-xs text-fail">
          {error}
        </span>
      )}
    </label>
  );
}

/**
 * An invite link that didn't load: unknown (mistyped, or its organization
 * is gone), used, revoked or expired, each with the daemon's words on what
 * to do. Shown in place of InvitePage.
 */
export function InviteError({ error }: { error: unknown }) {
  const dead = error instanceof ApiError && error.status === 404;
  const session = useQuery(queries.session()).data;
  return (
    <Frame
      title={dead ? "This invite link doesn't work" : "The invite didn't load"}
    >
      {dead ? (
        // The API can't tell these apart (a dead code reads as unknown),
        // so the page lists them with what to do.
        <div className="flex flex-col gap-2">
          <p>Usually one of these:</p>
          <ul className="flex list-disc flex-col gap-1 pl-5 text-sec">
            <li>
              Part of the link is missing. Copy the whole link from the message
              again.
            </li>
            <li>It expired. Invites last 7 days.</li>
            <li>
              It was used already, or the admin revoked it or made a new one.
            </li>
          </ul>
          <p className="text-sec">
            Ask the admin who invited you for a new link.
          </p>
        </div>
      ) : (
        <ErrorText error={error instanceof Error ? error : null} />
      )}
      <div className="flex flex-wrap gap-2">
        <Link to="/" className="btn">
          {session ? "Go to projects" : "Sign in"}
        </Link>
        {session && <SwitchAccount />}
      </div>
    </Frame>
  );
}
