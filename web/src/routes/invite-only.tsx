import { createFileRoute, Link } from "@tanstack/react-router";
import { useSignInMethods } from "../features/sso/data";

// Where an invite-only Dock sends someone an identity provider signed in
// who has no account and no open invite (ADR 0065). They aren't signed
// in: the query only says who the provider vouched for, for the page.
// Anyone can make a link to this page, so it shows the provider only as
// one of this Dock's own, by its label, and the email only if it is
// shaped like one.
type Search = { provider?: string; email?: string };

/** An address as the page shows it: one email address, of a sane length. */
const emailShape = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export const Route = createFileRoute("/invite-only")({
  validateSearch: (search: Record<string, unknown>): Search => ({
    ...(typeof search.provider === "string" && { provider: search.provider }),
    ...(typeof search.email === "string" && { email: search.email }),
  }),
  component: InviteOnly,
});

function InviteOnly() {
  const { provider, email } = Route.useSearch();
  const methods = useSignInMethods();
  const label = methods.data?.providers?.find(
    (p) => p.name === provider,
  )?.label;
  const who =
    email && email.length <= 254 && emailShape.test(email)
      ? email
      : "your email";
  return (
    <div className="flex min-h-screen flex-col items-center px-4 pt-[16vh] pb-10">
      <main className="flex w-full max-w-[420px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">This Dock is invite only</h1>
          <span className="font-mono text-xs text-mut">
            {window.location.host}
          </span>
        </div>
        <p>
          You're signed in with {label ?? "your identity provider"} as{" "}
          <span className="font-mono">{who}</span>, but you have no account here
          and no open invite.
        </p>
        <p className="text-sec">
          Ask an admin of the organization you're joining to invite{" "}
          <span className="font-mono">{who}</span>, then open the link they
          send.
        </p>
        <div>
          <Link to="/login" className="btn">
            Back to sign in
          </Link>
        </div>
      </main>
    </div>
  );
}
