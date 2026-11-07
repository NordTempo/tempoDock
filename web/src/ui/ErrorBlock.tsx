import type { ReactNode } from "react";

// Kept out of kit.tsx: route errors render it from the entry chunk, which
// shouldn't carry the whole kit.

/**
 * Something failed to load: what went wrong, what to do next and a Try
 * again button. Pages, panels and route errors all use it.
 */
export function ErrorBlock({
  error,
  onRetry,
  next,
}: {
  error: unknown;
  /** Loads it again; without it there is no button. */
  onRetry?: () => void;
  /** The next step; by default one that fits the error. */
  next?: ReactNode;
}) {
  return (
    <div role="alert" className="flex flex-col items-start gap-2 py-2">
      <p className="text-fail">
        <span aria-hidden>✕</span> {errorMessage(error)}
      </p>
      {(next ?? nextStep(error)) && (
        <p className="text-mut">{next ?? nextStep(error)}</p>
      )}
      {onRetry && (
        <button type="button" className="btn sm" onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  );
}

/** The error's message, starting with a capital like a sentence. */
const errorMessage = (error: unknown) => {
  const m = error instanceof Error ? error.message : "";
  // SQLite's busy error is Dock's own database being busy, not the user's.
  if (/database is locked/i.test(m)) {
    return "Dock's database was busy and the request timed out.";
  }
  return m ? m[0]?.toUpperCase() + m.slice(1) : "Something went wrong.";
};

/**
 * A plain next step for an error, from its HTTP status; none when there is
 * no status to go on (the message says it, and Try again is the button).
 */
function nextStep(error: unknown) {
  const status =
    typeof error === "object" && error && "status" in error
      ? Number(error.status)
      : undefined;
  if (status === undefined) return undefined;
  if (status === 0)
    return "Check your connection and that Dock is running, then try again.";
  if (status === 403) return "Ask an admin of this organization for access.";
  if (status === 404) return "It may have been renamed or removed.";
  if (status >= 500)
    return (
      <>
        Try again in a moment. If it keeps failing, Dock's log on the server
        says why: <code className="font-mono text-sec">journalctl -u dock</code>
      </>
    );
  return undefined;
}
