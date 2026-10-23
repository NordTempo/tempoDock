import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { setOrg } from "../../api/client";
import { keys, useSession } from "../../data/queries";
import { day } from "../../ui/format";
import { Act, ErrorText, Page } from "../../ui/kit";
import { orgQueries, useAcceptInvite } from "./data";

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
    <Page width="max-w-[560px]" gap="gap-4">
      <h1 className="pt-10 text-xl font-semibold">Join {invite.org_name}</h1>
      <p className="text-mut">
        You're invited as <b className="font-medium text-fg">{invite.role}</b>{" "}
        to <span className="font-mono text-fg">{invite.org}</span>, for{" "}
        <span className="font-mono text-fg">{invite.email}</span>.
      </p>
      {invite.status !== "open" ? (
        <p className="text-fail">
          This invite{" "}
          {invite.status === "expired" ? "expired" : `was ${invite.status}`}.
          Ask an admin of {invite.org_name} for a new one.
        </p>
      ) : !forYou ? (
        <p className="text-fail">
          You're signed in as {me?.email}. Sign in as {invite.email} to accept
          it.
        </p>
      ) : (
        <p className="flex flex-wrap items-center gap-3">
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
          <span className="text-faint">
            The link works until {day(invite.expires_at)}.
          </span>
          <ErrorText error={accept.error} />
        </p>
      )}
    </Page>
  );
}
