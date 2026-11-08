import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useIsOperator } from "../../data/queries";
import { Button, CopyButton } from "../../ui/kit";
import { addressQuery } from "../address/data";

// An invite's link, shown once when it is made: the org's Members page
// and the console's Organizations page both make them.

/** A created invite: whom for, and the code its link carries. */
export type InviteLinkInfo = { email: string; code: string };

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

/** The link of an invite just created, with copy; it isn't shown again. */
export function InviteLink({
  link,
  url,
  note,
  again = "New link on their row makes another.",
  onDone,
}: {
  link: InviteLinkInfo;
  url: string;
  /** Why the link may not open for them, when it may not. */
  note: ReactNode;
  /** How to get another link, since this one isn't shown again. */
  again?: string;
  onDone: () => void;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <section
      aria-labelledby="invite-link"
      className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
    >
      <h2 id="invite-link" className="font-semibold">
        <span className="text-ok">✓</span> Invite for{" "}
        <span className="font-mono font-medium">{link.email}</span>
      </h2>
      {note}
      <p className="text-mut">
        Dock doesn't send email: copy this link and send it to them yourself. It
        works once, for 7 days, and only signed in as {link.email}. It isn't
        shown again; {again}
      </p>
      {/* Copying is noticed, so closing before it says what is lost. */}
      <div
        onClickCapture={() => setCopied(true)}
        className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5"
      >
        <code className="min-w-0 flex-1 font-mono break-all">{url}</code>
        <CopyButton text={url} primary />
      </div>
      <div>
        <Button small onClick={onDone}>
          {copied ? "Done" : "Close without copying"}
        </Button>
      </div>
    </section>
  );
}
