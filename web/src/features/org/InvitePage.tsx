import {
  useMutation,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { Link, useNavigate, useRouter } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { ApiError, api, done, setOrg } from "../../api/client";
import { signedOut } from "../../data/mutations";
import { keys, useSession } from "../../data/queries";
import { day } from "../../ui/format";
import { Act, Button, ErrorText } from "../../ui/kit";
import { orgQueries, useAcceptInvite } from "./data";

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
 * An invite link (/invite/<code>): which organization, as what, and
 * joining it (`dock org join <link>`). Only for the email it names.
 */
export function InvitePage({ code }: { code: string }) {
  const invite = useSuspenseQuery(orgQueries.invite(code)).data;
  const me = useSession();
  const accept = useAcceptInvite();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
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
        <dd>{invite.role}</dd>
        <dt>For</dt>
        <dd className="font-mono">{invite.email}</dd>
        {invite.status === "open" && (
          <>
            <dt>Works until</dt>
            <dd>{day(invite.expires_at)}</dd>
          </>
        )}
      </dl>
      {invite.status !== "open" ? (
        <p className="text-fail">
          This invite{" "}
          {invite.status === "expired"
            ? "expired"
            : invite.status === "accepted"
              ? "was already used"
              : "was revoked"}
          . Ask an admin of {invite.org_name} for a new one.
        </p>
      ) : !forYou ? (
        <>
          <p>
            You're signed in as <span className="font-mono">{me?.email}</span>.
            The invite is for <span className="font-mono">{invite.email}</span>.
          </p>
          <div>
            <SwitchAccount />
          </div>
        </>
      ) : (
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
                    await queryClient.refetchQueries({
                      queryKey: keys.session,
                    });
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
      )}
    </Frame>
  );
}

/**
 * An invite link that didn't load: unknown (mistyped, or its organization
 * is gone), or opened by a token session. Shown in place of InvitePage.
 */
export function InviteError({ error }: { error: unknown }) {
  const notFound = error instanceof ApiError && error.status === 404;
  return (
    <Frame title={notFound ? "This invite link doesn't work" : "Invite"}>
      {notFound ? (
        <p>
          Check that the whole link was copied, or ask an admin for a new
          invite.
        </p>
      ) : (
        <ErrorText error={error instanceof Error ? error : null} />
      )}
      <div className="flex flex-wrap gap-2">
        <Link to="/" className="btn">
          Go to projects
        </Link>
        <SwitchAccount />
      </div>
    </Frame>
  );
}
