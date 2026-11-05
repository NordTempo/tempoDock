import { useQueryErrorResetBoundary } from "@tanstack/react-query";
import {
  type ErrorComponentProps,
  Link,
  useRouter,
  useRouterState,
} from "@tanstack/react-router";
import { liveStatus, reconnectLive } from "../data/liveStatus";
import { ErrorBlock } from "./ErrorBlock";

/** The header bar every page has (PageBar's look, kept out of the kit's chunk). */
function Bar({ title }: { title: string }) {
  return (
    <header className="sticky top-0 z-10 flex min-h-[52px] items-center border-b border-line bg-page px-4 py-2.5 sm:px-5">
      <h1 className="text-md font-semibold">{title}</h1>
    </header>
  );
}

/**
 * A page that isn't there, in place of the page so the sidebar stays: the
 * routes' loaders throw notFound at the org (or console) route, which
 * renders this in the shell. It names the address and links one level up,
 * where what's there can be seen.
 */
export function NotFound() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const parts = path.split("/").filter(Boolean);
  const missing = parts.at(-1) ?? "";
  const up = parts.length > 1 ? `/${parts.slice(0, -1).join("/")}` : "/";
  return (
    <>
      <Bar title="Not found" />
      <div className="flex w-full max-w-[1100px] flex-col items-start gap-2 px-4 py-8 sm:px-7">
        <p className="text-md">
          There's nothing called <b className="font-mono">{missing}</b> at{" "}
          <span className="font-mono text-mut">{path}</span>.
        </p>
        <p className="text-mut">It may have been renamed or archived.</p>
        <div className="mt-2 flex gap-2">
          <a href={up} className="btn pri">
            Go to {up === "/" ? "projects" : (parts.at(-2) ?? "projects")}
          </a>
          {up !== "/" && (
            <Link to="/" className="btn">
              All projects
            </Link>
          )}
        </div>
      </div>
    </>
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
    <>
      <Bar title="This page didn't load" />
      <div className="flex w-full max-w-[1100px] flex-col gap-1 px-4 py-8 sm:px-7">
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
    </>
  );
}

/**
 * The shell's frame while its first data loads (a cold start): the
 * sidebar's column with still placeholder rows, the header bar and a
 * quiet line. Static on purpose (no shimmer).
 */
export function ShellPending() {
  const bar = (w: string) => (
    <span className={`block h-[10px] rounded-[3px] bg-sel ${w}`} />
  );
  return (
    <div className="flex min-h-screen" aria-busy="true">
      <div className="hidden w-[248px] flex-none flex-col gap-4 border-r border-line px-4 py-4 lg:flex">
        {bar("w-24")}
        <span className="block h-[30px] rounded-ctl border border-line" />
        <div className="flex flex-col gap-3 pt-2">
          {bar("w-16")}
          {bar("w-28")}
          {bar("w-20")}
          {bar("w-24")}
        </div>
      </div>
      <div className="flex flex-1 flex-col">
        <div className="flex h-[52px] items-center border-b border-line px-5">
          {bar("w-40")}
        </div>
        <p role="status" className="px-7 py-6 text-mut">
          Loading Dock…
        </p>
      </div>
    </div>
  );
}
