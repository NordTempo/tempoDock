import { useQueryErrorResetBoundary } from "@tanstack/react-query";
import {
  type ErrorComponentProps,
  Link,
  useRouter,
} from "@tanstack/react-router";
import { liveStatus, reconnectLive } from "../data/liveStatus";
import { ErrorBlock } from "./ErrorBlock";

/**
 * A page that isn't there, in place of the page so the sidebar stays: the
 * routes' loaders throw notFound at the org (or console) route, which
 * renders this in the shell.
 */
export function NotFound() {
  return (
    <div className="flex w-full max-w-[1100px] flex-col items-start gap-2 px-4 py-16 sm:px-7">
      <p className="font-mono text-2xs text-faint">404</p>
      <h1 className="text-lg font-semibold">Nothing at this address</h1>
      <p className="text-mut">It may have been renamed or archived.</p>
      <Link to="/" className="btn pri mt-2">
        Go to projects
      </Link>
    </div>
  );
}

/**
 * A route that failed to load, in place of the page so the sidebar stays
 * usable. Try again refetches the failed queries and reruns the loaders,
 * and reopens the live stream if it gave up meanwhile. A 404 from the API
 * is the same as an address with no page.
 */
export function RouteError({ error, reset }: ErrorComponentProps) {
  const router = useRouter();
  const queries = useQueryErrorResetBoundary();
  if (
    typeof error === "object" &&
    error &&
    "status" in error &&
    error.status === 404
  ) {
    return <NotFound />;
  }
  return (
    <div className="flex w-full max-w-[1100px] flex-col gap-1 px-4 py-16 sm:px-7">
      <h1 className="text-lg font-semibold">This page didn't load</h1>
      <ErrorBlock
        error={error}
        onRetry={() => {
          queries.reset();
          if (liveStatus() === "closed") reconnectLive();
          reset();
          void router.invalidate();
        }}
      />
    </div>
  );
}

/**
 * The shell's frame while its first data loads: the sidebar's column and a
 * header bar, empty and still. Static on purpose (no shimmer).
 */
export function ShellPending() {
  return (
    <div className="flex min-h-screen" aria-busy="true">
      <div className="hidden w-[248px] flex-none border-r border-line lg:block" />
      <div className="flex flex-1 flex-col">
        <div className="h-[52px] border-b border-line" />
        <p className="px-7 py-6 text-mut">Loading…</p>
      </div>
    </div>
  );
}
