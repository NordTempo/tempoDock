import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import type { components } from "../../api/schema";
import { useIsOperator } from "../../data/queries";
import { clock } from "../../ui/format";
import { Button, CopyButton } from "../../ui/kit";
import { addressQuery } from "../address/data";
import type { InviteMail } from "../console/email";

// An invite's link, shown once when it is made: the org's Members page
// and the console's Organizations page both make them.

/**
 * A created invite: which, whom for, the code its link carries, and its
 * mail as the API answered (live events update it in the invites list).
 */
export type InviteLinkInfo = {
  id: number;
  email: string;
  code: string;
  mail: InviteMail;
};

/** The link info of an invite just created. */
export const linkInfo = (c: components["schemas"]["CreatedInvite"]) => ({
  id: c.invite.id,
  email: c.invite.email,
  code: c.code,
  mail: c.mail,
});

/** Hostnames only this machine (or its private network) can open. */
const privateHost =
  /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.|\[?::1\]?$)/;

/**
 * Where invite links point: Dock's public address when one is set, else
 * the address this page was opened at. `local` when that is an address
 * others likely can't open (no public address, or a private one).
 * Operators read the public address (ADR 0029, only they may); for
 * everyone else the page's own address decides. Loaded once inviting.
 */
export function useInviteBase(enabled: boolean) {
  const operator = useIsOperator();
  const address = useQuery({
    ...addressQuery(),
    enabled: enabled && operator,
  }).data;
  const base = address?.url ?? window.location.origin;
  const local = operator
    ? !!address && !address.url
    : privateHost.test(window.location.hostname);
  return { base, local, operator };
}

/**
 * The warning beside an invite when its link won't open for others, with
 * the way to fix it: the console's Address page, or whom to ask.
 */
export function LocalLinkNote({ operator }: { operator: boolean }) {
  return (
    <p className="text-warn-text">
      <span className="text-warn">▲</span> This link only opens on this machine
      or its network: Dock has no public address.{" "}
      {operator ? (
        <Link to="/console/address" className="text-warn-text underline">
          Set a public address
        </Link>
      ) : (
        "Ask an operator to set a public address."
      )}
    </p>
  );
}

/**
 * An invite just created: whether its link was emailed (ADR 0079), and
 * the link to copy, shown this once. `live` is the invite's mail from the
 * invites list, which `email` events patch; until it is there, the
 * answer to creating it.
 */
export function InviteLink({
  link,
  url,
  note,
  live,
  again = "New link on their row makes another.",
  onDone,
}: {
  link: InviteLinkInfo;
  url: string;
  /** Why the link may not open for them, when it may not. */
  note: ReactNode;
  live?: InviteMail;
  /** How to get another link, since this one isn't shown again. */
  again?: string;
  onDone: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const operator = useIsOperator();
  const mail = live ?? link.mail;
  const who = <span className="font-mono font-medium">{link.email}</span>;
  const once = `The link works once, for 7 days, only for ${link.email}, and isn't shown again; ${again}`;
  // Copying is noticed, so closing before it says what is lost.
  const box = (primary: boolean) => (
    <div
      onClickCapture={() => setCopied(true)}
      className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5"
    >
      <code className="min-w-0 flex-1 font-mono break-all">{url}</code>
      <CopyButton text={url} primary={primary} />
    </div>
  );
  const close = (
    <div>
      <Button small onClick={onDone}>
        {copied || mail.status === "sent" ? "Done" : "Close without copying"}
      </Button>
    </div>
  );
  const frame = (tone: "ok" | "warn" | "plain", body: ReactNode) => (
    <section
      aria-labelledby="invite-link"
      className={`flex flex-col gap-2.5 rounded-panel border p-4 ${tone === "ok" ? "border-ok-edge bg-ok-bg/30" : tone === "warn" ? "border-warn-edge bg-warn-bg/40" : "border-line bg-panel"}`}
    >
      {body}
    </section>
  );
  switch (mail.status) {
    case "sent":
      return frame(
        "ok",
        <>
          <h2 id="invite-link" className="font-semibold">
            <span className="text-ok">✓</span> Invite emailed to {who}
          </h2>
          <p className="text-mut">
            Sent{mail.at && ` ${clock(mail.at)}`}. The link works once, for 7
            days, only for them.
          </p>
          <details>
            <summary className="cursor-pointer text-info">
              Copy the link instead
            </summary>
            <div className="mt-2">{box(false)}</div>
          </details>
          {close}
        </>,
      );
    case "queued":
      return frame(
        "plain",
        <>
          <h2 id="invite-link" className="font-semibold">
            {mail.error
              ? "Couldn't reach the mail server yet"
              : "Emailing the invite to "}
            {!mail.error && who}
          </h2>
          <p className="text-mut">
            {mail.error
              ? `${mail.error}. Dock keeps trying for a few hours; send them the link yourself if it's urgent. `
              : ""}
            {once}
          </p>
          {box(false)}
          {close}
        </>,
      );
    case "failed":
    case "refused":
      return frame(
        "warn",
        <>
          <h2 id="invite-link" className="font-semibold">
            <span className="text-warn">▲</span> Couldn't email {who}
          </h2>
          <p className="text-warn-text">
            {mail.error}. {mail.status === "failed" && "Check the address, or "}
            {mail.status === "failed" ? "send" : "Send"} them this link
            yourself.
          </p>
          <p className="text-mut">{once}</p>
          {box(true)}
          {close}
        </>,
      );
    default:
      return frame(
        "ok",
        <>
          <h2 id="invite-link" className="font-semibold">
            <span className="text-ok">✓</span> Invite for {who}
          </h2>
          {note}
          <p className="text-mut">
            {mail.status === "no_address"
              ? "Dock can't email it without a public address, so send them this link yourself. "
              : "Email isn't set up on this Dock, so send them this link yourself. "}
            {once}
          </p>
          {box(true)}
          {mail.status === "not_set_up" && operator && (
            <p className="text-xs text-faint">
              <Link to="/console/email" className="text-info">
                Set up email
              </Link>{" "}
              in the console to send invites by email.
            </p>
          )}
          {close}
        </>,
      );
  }
}
