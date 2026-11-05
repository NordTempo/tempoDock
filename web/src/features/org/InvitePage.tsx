import {
  useMutation,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { Link, useNavigate, useRouter } from "@tanstack/react-router";
import { type ComponentProps, type ReactNode, useState } from "react";
import { ApiError, api, done, setOrg } from "../../api/client";
import type { components } from "../../api/schema";
import { signedOut } from "../../data/mutations";
import { keys, queries, useSession } from "../../data/queries";
import { day } from "../../ui/format";
import { Act, Button, ErrorText } from "../../ui/kit";
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
  const out = useMutation({
    mutationFn: () => done(api.POST("/api/v1/auth/logout")),
    onSuccess: () => signedOut(queryClient, router, window.location.pathname),
  });
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
 * The choices of someone signed out: sign in with the invited email and
 * come back, or create the account for it right here.
 */
function SignedOut({ code, invite }: { code: string; invite: InviteInfo }) {
  const [creating, setCreating] = useState(false);
  const back = window.location.pathname;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-2">
        <Link to="/login" search={{ redirect: back }} className="btn pri">
          Sign in to accept
        </Link>
        {!creating && (
          <Button onClick={() => setCreating(true)}>Create an account</Button>
        )}
      </div>
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
  const ready =
    name.trim() !== "" && password.length >= MIN_PASSWORD && !signUp.isPending;
  const exists =
    signUp.error instanceof ApiError && signUp.error.status === 409;
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (!ready) return;
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
      />
      <Field
        label="Password"
        note={
          <span className={short ? "text-warn" : undefined}>
            at least {MIN_PASSWORD} characters
          </span>
        }
        type="password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        autoComplete="new-password"
      />
      <div className="flex flex-wrap items-center gap-3">
        <button type="submit" className="btn pri" disabled={!ready}>
          Create account and join
        </button>
        <ErrorText error={signUp.error} />
        {exists && (
          <Link to="/login" search={{ redirect: back }}>
            Sign in
          </Link>
        )}
      </div>
    </form>
  );
}

/** A labelled input of the account form, with a note beside the label. */
function Field({
  label,
  note,
  className = "",
  ...input
}: { label: string; note?: ReactNode } & ComponentProps<"input">) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-xs text-mut">
        {label} {note && <span className="text-faint">{note}</span>}
      </span>
      <input {...input} className={`field py-[7px] ${className}`} />
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
    <Frame title={dead ? "This invite link doesn't work" : "Invite"}>
      <ErrorText error={error instanceof Error ? error : null} />
      <div className="flex flex-wrap gap-2">
        <Link to="/" className="btn">
          {session ? "Go to projects" : "Sign in"}
        </Link>
        {session && <SwitchAccount />}
      </div>
    </Frame>
  );
}
