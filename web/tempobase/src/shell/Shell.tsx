import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useParams } from "@tanstack/react-router";
import { lazy, Suspense } from "react";
import { initials } from "../../../src/features/data/kit";
import { queries, useIsAdmin, useLogout, useMe } from "../data/queries";

// The open connection's tables, under it in the sidebar. Lazy: it shares
// the panel's data hooks, which stay out of the entry chunk.
const ConnectionTables = lazy(() => import("./ConnectionTables"));

const roleNames = { viewer: "Viewer", editor: "Editor", admin: "Admin" };

/** Everything behind sign-in (G1): the sidebar and the page. */
export function Shell() {
  return (
    <div className="flex min-h-screen">
      <Sidebar />
      <div className="flex min-w-0 flex-[999_1_560px] flex-col">
        <Outlet />
      </div>
    </div>
  );
}

/**
 * The product, the connections (the open one with its tables), the
 * settings pages (admin ones for admins) and the account.
 */
function Sidebar() {
  const admin = useIsAdmin();
  const me = useMe();
  const connections = useQuery(queries.connections());
  const { connection: open } = useParams({ strict: false });
  const logout = useLogout();
  const navigate = useNavigate();
  const pages = [
    ["/settings/tokens", "Agents & MCP"],
    ...(admin ? ([["/settings/people", "People"]] as const) : []),
    ["/settings/policy", "Access"],
    ...(admin ? ([["/settings/audit", "Activity"]] as const) : []),
  ] as const;

  return (
    <nav
      aria-label="Connections and settings"
      className="sticky top-0 flex h-screen max-w-[264px] flex-[1_1_240px] flex-col gap-2.5 overflow-y-auto border-r border-(--g-line) bg-(--g-side) px-2 py-3"
    >
      <div className="flex h-8 items-center justify-between px-1.5">
        <Link
          to="/"
          className="font-semibold tracking-[-0.01em] text-(--g-text)!"
        >
          tempoBase
        </Link>
        {admin && (
          <Link
            to="/connections/new"
            className="g-btn ghost w-7 px-0"
            aria-label="Add connection"
            title="Add connection"
          >
            +
          </Link>
        )}
      </div>

      <div className="flex min-h-0 flex-col gap-px">
        <div className="g-label px-2.5 pt-1.5 pb-1">Connections</div>
        {connections.error && (
          <p className="px-2.5 text-xs text-(--g-fail)">
            {connections.error.message}
          </p>
        )}
        {connections.data?.length === 0 && (
          <p className="px-2.5 text-xs text-(--g-mut)">None yet.</p>
        )}
        {connections.data?.map((c) => {
          const current = c.name === open;
          return (
            <div key={c.name} className="flex flex-col gap-px">
              <Link
                to="/c/$connection"
                params={{ connection: c.name }}
                className="g-item h-8 text-(--g-text)!"
                activeOptions={{ exact: true, includeSearch: false }}
                activeProps={{ "aria-current": "page" }}
              >
                <Chevron open={current} />
                <span className="flex-1 truncate font-medium">{c.name}</span>
                {!!c.pending && (
                  <span
                    className="text-2xs text-(--g-warn)"
                    title={`${c.pending} pending changes`}
                  >
                    {c.pending}
                  </span>
                )}
                {c.production && (
                  <span className="text-2xs text-(--g-warn)">production</span>
                )}
              </Link>
              {current && admin && (
                <Link
                  to="/c/$connection/settings"
                  params={{ connection: c.name }}
                  className="g-item pl-[30px]"
                  activeProps={{ "aria-current": "page" }}
                >
                  Settings
                </Link>
              )}
              {current && (
                <Suspense fallback={null}>
                  <ConnectionTables connection={c.name} />
                </Suspense>
              )}
            </div>
          );
        })}
      </div>

      <div className="mt-auto flex flex-col gap-px border-t border-(--g-line) pt-2">
        {pages.map(([to, label]) => (
          <Link
            key={to}
            to={to}
            className="g-item"
            activeProps={{ "aria-current": "page" }}
          >
            {label}
          </Link>
        ))}
        <div className="mt-1 flex h-9 items-center gap-2 px-2.5">
          <span className="g-avatar" aria-hidden>
            {initials(me.name)}
          </span>
          <Link
            to="/settings/account"
            className="min-w-0 flex-1 truncate text-(--g-dim)!"
            activeProps={{ "aria-current": "page" }}
            title="Account"
          >
            {me.name}
          </Link>
          <span className="text-2xs text-(--g-mut)">{roleNames[me.role]}</span>
        </div>
        <button
          type="button"
          className="g-item text-xs"
          disabled={logout.isPending}
          onClick={() =>
            logout.mutate(undefined, {
              onSuccess: () => navigate({ to: "/login" }),
            })
          }
        >
          Sign out
        </button>
      </div>
    </nav>
  );
}

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="var(--g-mut)"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={open ? "rotate-90" : ""}
    >
      <path d="M9 6l6 6-6 6" />
    </svg>
  );
}
