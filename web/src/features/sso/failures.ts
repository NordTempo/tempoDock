// Why a single sign-on failed, in the sign-in page's words. The server
// sends the browser back with a reason (core's SSO* reasons,
// internal/core/sso_signin.go; a Go test holds the two in step) and the
// provider's name, never with a sentence: one in the address could be
// written by whoever made the link.

/** A sentence per reason; p is the provider's label. */
const failures: Record<string, (p: string) => string> = {
  expired: () => "This sign-in expired or was used already; start again.",
  provider_changed: () =>
    "The identity provider changed during the sign-in; start again.",
  other_browser: () =>
    "This sign-in was started in another browser, or its cookie was lost; start again here.",
  cancelled: (p) => `The sign-in was cancelled at ${p}.`,
  refused: (p) =>
    `${p} refused the sign-in; ask an operator to check the ${p} provider in the console.`,
  no_code: (p) => `${p} sent no sign-in code; start again.`,
  not_accepted: (p) =>
    `${p} didn't accept the sign-in; start again, or ask an operator to check the ${p} provider's client ID and secret.`,
  bad_answer: (p) =>
    `${p}'s answer didn't check out; try again, or ask an operator to check the ${p} provider in the console.`,
  no_email: (p) =>
    `${p} sent no email address; ask an operator to let Dock read it (the email scope).`,
  no_email_github: (p) =>
    `GitHub sent no primary email address; ask an operator to check the ${p} provider asks for the user:email scope.`,
  unverified: (p) =>
    `${p} hasn't verified your email address; verify it there and try again.`,
  unverified_github: () =>
    "GitHub hasn't verified your primary email; verify it at GitHub and try again.",
  unverified_microsoft: () =>
    "Microsoft didn't say your email address is verified; ask an operator to add the optional claims email and xms_edov to the ID token of Dock's app registration (Token configuration).",
  disabled: () => "Your Dock account is disabled; ask an operator.",
  start_again: () =>
    "Another sign-in created your account meanwhile; start again.",
  failed: () => "Signing in failed; try again.",
};

/**
 * The sentence for a failed single sign-on, or undefined for a reason
 * the page doesn't know: it then shows nothing. label is the provider's,
 * from the page's own list of providers.
 */
export function ssoFailure(reason: string, label?: string) {
  if (!Object.hasOwn(failures, reason)) return undefined;
  const text = failures[reason]?.(label ?? "the identity provider") ?? "";
  return text.charAt(0).toUpperCase() + text.slice(1);
}
