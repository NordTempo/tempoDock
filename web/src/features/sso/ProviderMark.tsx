import type { components } from "../../api/schema";

type Kind = components["schemas"]["SSOProvider"]["kind"];

/**
 * A provider's mark on its sign-in button and in the console: Google's G
 * and GitHub's octocat in one colour, a plain ring for any other OpenID
 * Connect provider. Decorative: the button's text names the provider.
 */
export function ProviderMark({ kind }: { kind: Kind }) {
  return (
    <svg
      aria-hidden
      viewBox="0 0 16 16"
      className="size-[16px] shrink-0 fill-current"
    >
      {kind === "google" ? (
        <path d="M15.7 8.2c0-.6-.1-1.1-.2-1.6H8v3.1h4.3a3.7 3.7 0 0 1-1.6 2.4v2h2.6c1.5-1.4 2.4-3.4 2.4-5.9ZM8 16c2.2 0 4-.7 5.3-2l-2.6-2c-.7.5-1.6.8-2.7.8-2.1 0-3.9-1.4-4.5-3.3H.8v2.1A8 8 0 0 0 8 16Zm-4.5-6.5a4.8 4.8 0 0 1 0-3V4.4H.8a8 8 0 0 0 0 7.2l2.7-2.1ZM8 3.2c1.2 0 2.3.4 3.1 1.2l2.3-2.3A8 8 0 0 0 .8 4.4l2.7 2.1C4.1 4.6 5.9 3.2 8 3.2Z" />
      ) : kind === "github" ? (
        <path d="M8 0a8 8 0 0 0-2.5 15.6c.4 0 .5-.2.5-.4v-1.5c-2.2.5-2.7-1-2.7-1-.4-.9-.9-1.2-.9-1.2-.7-.5.1-.5.1-.5.8.1 1.2.8 1.2.8.7 1.3 1.9.9 2.3.7.1-.5.3-.9.5-1.1-1.8-.2-3.6-.9-3.6-4 0-.9.3-1.6.8-2.1-.1-.2-.4-1 .1-2.1 0 0 .7-.2 2.2.8a7.5 7.5 0 0 1 4 0c1.5-1 2.2-.8 2.2-.8.4 1.1.2 1.9.1 2.1.5.6.8 1.3.8 2.1 0 3.1-1.9 3.7-3.6 3.9.3.3.6.8.6 1.5v2.2c0 .2.1.5.6.4A8 8 0 0 0 8 0Z" />
      ) : (
        <path d="M8 1.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM0 8a8 8 0 1 1 16 0A8 8 0 0 1 0 8Zm8-2a2 2 0 1 1 0 4 2 2 0 0 1 0-4Z" />
      )}
    </svg>
  );
}

/** The kind in words, for the console. */
export const kindName: Record<Kind, string> = {
  google: "Google",
  github: "GitHub OAuth app",
  oidc: "OpenID Connect",
};
