import {
  useQueryClient,
  useQueryErrorResetBoundary,
} from "@tanstack/react-query";
import {
  type ErrorComponentProps,
  Link,
  useRouter,
  useRouterState,
} from "@tanstack/react-router";
import { liveStatus, reconnectLive } from "../data/liveStatus";
import { ErrorBlock } from "./ErrorBlock";

// Words after /<org>/ that are pages of the organization, not projects.
const orgPages: Record<string, string> = {
  analytics: "Analytics",
  audit: "Activity",
  requests: "Requests",
  settings: "Organization settings",
};

type Crumb = { label: string; href: string };

/**
 * Where a path sits, as PageBar's breadcrumb would show it: the project
 * (a project's page itself is the root), an organization area, or the
 * console's servers. Only levels known to exist get a crumb.
 */
function crumbsFor(path: string, projects: string[]): Crumb[] {
  const parts = path.split("/").filter(Boolean);
  const [first, second, third] = parts;
  if (first === "console") {
    return second === "servers" && third
      ? [{ label: "Servers", href: "/console/servers" }]
      : [];
  }
  if (!first || !second || first === "account") return [];
  const area = orgPages[second];
  if (area) {
    return third ? [{ label: area, href: `/${first}/${second}` }] : [];
  }
  return third && projects.includes(second)
    ? [{ label: second, href: `/${first}/${second}` }]
    : [];
}

/**
 * The header bar every page has, in PageBar's look (kept out of the kit's
 * chunk), with the breadcrumb of the address the page was meant to show.
 */
function Bar({ title }: { title: string }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const projects =
    useQueryClient()
      .getQueryData<{ name: string }[]>(["projects"])
      ?.map((p) => p.name) ?? [];
  const crumbs = crumbsFor(path, projects);
  return (
    <header className="sticky top-0 z-10 flex min-h-[52px] items-center border-b border-line bg-page px-4 py-2.5 sm:px-5">
      <nav aria-label="Breadcrumb" className="flex items-center gap-1.5">
        {crumbs.map((c) => (
          <span key={c.href} className="flex items-center gap-1.5">
            <Link to={c.href} className="text-mut hover:text-fg">
              {c.label}
            </Link>
            <span aria-hidden className="text-faint">
              /
            </span>
          </span>
        ))}
        <h1 className="text-md font-semibold">{title}</h1>
      </nav>
    </header>
  );
}

/**
 * A page that isn't there, in place of the page so the sidebar stays: the
 * routes' loaders throw notFound at the org (or console) route, which
 * renders this in the shell. The header keeps where the address points;
 * the page names what is missing and links one level up, where what's
 * there can be seen.
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
          There's nothing called <b className="font-mono">{missing}</b> here.
        </p>
        <p className="text-mut">
          It may have been renamed, archived or removed, or the link has a typo.
          Archived projects are under Archived in the sidebar.
        </p>
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
