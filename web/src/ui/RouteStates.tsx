import {
  isCancelledError,
  useQueryClient,
  useQueryErrorResetBoundary,
} from "@tanstack/react-query";
import {
  type ErrorComponentProps,
  Link,
  useRouter,
  useRouterState,
} from "@tanstack/react-router";
import { useEffect } from "react";
import { liveStatus, reconnectLive } from "../data/liveStatus";
import { ErrorBlock } from "./ErrorBlock";

// Words after /<org>/ that are pages of the organization, not projects.
const orgPages: Record<string, string> = {
  alerts: "Alerts",
  analytics: "Analytics",
  audit: "Activity",
  errors: "Errors",
  help: "Help",
  images: "Images",
  inbox: "Inbox",
  requests: "Requests",
  settings: "Organization settings",
  "status-pages": "Status pages",
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
 * chunk): the way up, small, over the title, with the breadcrumb of the
 * address the page was meant to show. Same height as PageBar, so a page
 * that fails doesn't jump.
 */
function Bar({ title }: { title: string }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const projects =
    useQueryClient()
      .getQueryData<{ name: string }[]>(["projects"])
      ?.map((p) => p.name) ?? [];
  const crumbs = crumbsFor(path, projects);
  return (
    <header className="page-bar sticky top-(--shell-top) z-10 flex min-h-[64px] items-center border-b border-line bg-page px-4 py-2.5 sm:px-7">
      <div className="flex min-w-0 flex-col">
        {crumbs.length > 0 ? (
          <nav
            aria-label="Breadcrumb"
            className="flex items-center gap-1.5 text-xs text-mut"
          >
            {crumbs.map((c, i) => (
              <span key={c.href} className="flex items-center gap-1.5">
                {i > 0 && (
                  <span aria-hidden className="text-faint">
                    /
                  </span>
                )}
                <Link to={c.href} className="hover:text-fg">
                  {c.label}
                </Link>
              </span>
            ))}
          </nav>
        ) : (
          <span aria-hidden className="h-[18px]" />
        )}
        <h1 className="text-2xl leading-7 font-semibold tracking-[-0.01em]">
          {title}
        </h1>
      </div>
    </header>
  );
}

/**
 * A page's frame while it loads, still (no shimmer): PageBar's two lines
 * and a table's rows under a hairline, as most pages are, so the page
 * doesn't jump when it arrives.
 */
export function PagePending() {
  const bar = (w: string, h = "h-[10px]") => (
    <span className={`block rounded-tag bg-dash ${h} ${w}`} />
  );
  return (
    <div aria-busy="true" className="flex flex-1 flex-col">
      <div className="flex min-h-[64px] flex-col justify-center gap-2 border-b border-line px-4 py-2.5 sm:px-7">
        {bar("w-20", "h-[8px]")}
        {bar("w-48", "h-[16px]")}
      </div>
      <div className="flex w-full max-w-[1400px] flex-col gap-3 px-4 pt-6 sm:px-7">
        <p role="status" className="sr-only">
          Loading…
        </p>
        <div className="flex flex-col border-t border-line">
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="flex gap-6 border-b border-line py-3">
              {bar("w-28")}
              {bar("w-20")}
              {bar("w-48")}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

type Missing = {
  /** What it is, when the address says: project, environment, service. */
  kind?: "project" | "environment" | "service";
  name: string;
  /** Where it was looked for: shop, shop/production. */
  where?: string;
  /** The parts of the address one level up. */
  up: string[];
};

/**
 * What a missing address names: /<org>/<project>/<environment>/<service>
 * by position (a project the sidebar doesn't list is the missing one),
 * else only the last word, for organization pages, the console and
 * deeper pages.
 */
function missingFor(parts: string[], projects: string[]): Missing {
  const [first = "", second, third, fourth] = parts;
  const generic = { name: parts.at(-1) ?? "", up: parts.slice(0, -1) };
  if (first === "console" || first === "account" || !second) return generic;
  if (orgPages[second]) return generic;
  if (parts.length === 2 || (projects.length && !projects.includes(second)))
    return { kind: "project", name: second, up: [first] };
  if (parts.length === 3 && third)
    return {
      kind: "environment",
      name: third,
      where: second,
      up: [first, second],
    };
  if (parts.length === 4 && third && fourth)
    return {
      kind: "service",
      name: fourth,
      where: `${second}/${third}`,
      up: [first, second, third],
    };
  return generic;
}

/**
 * A page that isn't there, in place of the page so the sidebar stays: the
 * routes' loaders throw notFound at the org (or console) route, which
 * renders this in the shell. The header keeps where the address points;
 * the page names what is missing (a project, an environment, a service
 * when the address says) and links one level up, where what's there can
 * be seen.
 */
export function NotFound() {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const projects =
    useQueryClient()
      .getQueryData<{ name: string }[]>(["projects"])
      ?.map((p) => p.name) ?? [];
  const parts = path.split("/").filter(Boolean);
  const m = missingFor(parts, projects);
  const up = `/${m.up.join("/")}`;
  // One level under an organization is its Overview; the console's and
  // the account's top pages go by their own names.
  const upName =
    m.up.length === 1 && m.up[0] !== "console" && m.up[0] !== "account"
      ? "overview"
      : (m.up.at(-1) ?? "projects");
  return (
    <>
      <Bar title="Not found" />
      <div className="flex w-full max-w-[1400px] flex-col items-start gap-2 px-4 py-8 sm:px-7">
        <p className="text-md">
          {m.kind ? (
            <>
              No {m.kind} called <b className="font-mono">{m.name}</b>
              {m.where && (
                <>
                  {" in "}
                  <span className="font-mono">{m.where}</span>
                </>
              )}
              .
            </>
          ) : (
            <>
              There's nothing called <b className="font-mono">{m.name}</b> here.
            </>
          )}
        </p>
        <p className="text-mut">
          It may have been renamed, archived or removed, or the link has a typo.
          {m.kind === "project" &&
            " Archived projects are under Archived in the sidebar."}
        </p>
        <div className="mt-2 flex gap-2">
          <a href={up} className="btn pri">
            Go to {upName}
          </a>
          {m.up.length > 1 && (
            <Link to="/" className="btn">
              Go to overview
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
 * is the same as an address with no page. A query dropped while the page
 * loaded it (the cache is cleared when the organization changes) didn't
 * fail: the page loads again at once.
 */
export function RouteError({ error, reset }: ErrorComponentProps) {
  const router = useRouter();
  const queries = useQueryErrorResetBoundary();
  // Invalidating gives the match a new state, which resets this boundary.
  const cancelled = isCancelledError(error);
  useEffect(() => {
    if (cancelled) void router.invalidate();
  }, [cancelled, router]);
  if (cancelled) return <Bar title="Loading…" />;
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
      <div className="flex w-full max-w-[1400px] flex-col gap-1 px-4 py-8 sm:px-7">
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
 * The shell's frame while its first data loads (a cold start): the top
 * bar, the project rail with still tiles, and a page's frame
 * (PagePending). Static on purpose (no shimmer).
 */
export function ShellPending() {
  return (
    <div className="flex min-h-screen flex-col">
      <div className="h-[52px] flex-none border-b border-line" />
      <div className="flex flex-1">
        <div className="hidden w-[60px] flex-none flex-col items-center gap-2 border-r border-line py-2.5 lg:flex">
          {[0, 1, 2].map((i) => (
            <span
              key={i}
              className="block size-[30px] rounded-ctl border border-line"
            />
          ))}
        </div>
        <PagePending />
      </div>
    </div>
  );
}
