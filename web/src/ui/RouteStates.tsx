import { useQueryErrorResetBoundary } from "@tanstack/react-query";
import {
  type ErrorComponentProps,
  Link,
  useRouter,
} from "@tanstack/react-router";
import { liveStatus, reconnectLive } from "../data/liveStatus";
import { ErrorBlock } from "./ErrorBlock";

export function NotFound() {
  return (
    <div className="flex w-full max-w-[1100px] flex-col gap-2 px-4 py-6 sm:px-7">
      <h1 className="text-xl font-semibold">Not found</h1>
      <p className="text-mut">
        Nothing lives at this address. It may have been renamed or archived.
      </p>
      <Link to="/">Go to the overview</Link>
    </div>
  );
}

/**
 * A route that failed to load, in place of the page so the sidebar stays
 * usable. Try again refetches the failed queries and reruns the loaders,
 * and reopens the live stream if it gave up meanwhile.
 */
export function RouteError({ error, reset }: ErrorComponentProps) {
  const router = useRouter();
  const queries = useQueryErrorResetBoundary();
  return (
    <div className="w-full max-w-[1100px] px-4 py-6 sm:px-7">
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
