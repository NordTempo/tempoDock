// Step-ups (ADR 0051): a sensitive call answers 403 reauth_required when
// the session last proved who it is more than 15 minutes ago. The API
// client (./client.ts) asks the person to confirm here, then retries.
// The prompt itself is React (ui/ReauthHost.tsx), registered once at start-up;
// this module stays tiny because it is in the initial bundle.

type Ask = () => Promise<boolean>;

let ask: Ask | undefined;
let asking: Promise<boolean> | undefined;

/**
 * Registers the prompt that asks the person to confirm it's them. It
 * resolves true once confirmed, false when they cancel. Returns the
 * unregister function, for an effect's cleanup.
 */
export function registerReauthPrompt(prompt: Ask) {
  ask = prompt;
  return () => {
    if (ask === prompt) ask = undefined;
  };
}

/**
 * Asks the person to confirm it's them. Calls that need it at the same
 * time share one prompt and its answer; without a registered prompt the
 * answer is no.
 */
export function confirmIdentity(): Promise<boolean> {
  if (!ask) return Promise.resolve(false);
  asking ??= ask().finally(() => {
    asking = undefined;
  });
  return asking;
}
