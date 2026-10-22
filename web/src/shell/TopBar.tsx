import { Link } from "@tanstack/react-router";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useLogout } from "../data/mutations";
import {
  useIsAdmin,
  useProjects,
  useProjectTree,
  useServers,
  useSession,
} from "../data/queries";
import { modKey } from "./keys";
import { lastProject, useCrumb, useCurrentContext } from "./location";
import { usePalette } from "./palette";

const Sep = () => <span className="text-mut">›</span>;

/**
 * The one header: breadcrumb (project › environment › service), org links
 * outside environments, the status of the servers in view, search, user.
 */
export function TopBar() {
  const here = useCurrentContext();
  const crumb = useCrumb();
  const openPalette = usePalette();
  const inEnvironment = !!here.environment;
  const project = here.project ?? lastProject() ?? undefined;
  const admin = useIsAdmin();

  return (
    <header className="flex flex-wrap items-center gap-x-5 gap-y-1.5 border-b border-line px-5 py-2">
      <Link to="/" className="font-mono font-bold no-underline">
        dock
      </Link>
      <nav
        aria-label="Location"
        className="flex flex-wrap items-center gap-1.5"
      >
        {inEnvironment && here.project ? (
          <>
            <Link to="/$project" params={{ project: here.project }}>
              {here.project}
            </Link>
            <Sep />
            <EnvironmentCrumb />
          </>
        ) : (
          <ProjectSwitcher current={project} />
        )}
        {crumb && (
          <>
            <Sep />
            <span aria-current="page">{crumb}</span>
          </>
        )}
      </nav>
      {!inEnvironment && (
        <nav aria-label="Org" className="flex gap-4">
          {(
            [
              ["/servers", "Servers"],
              ["/tokens", "Tokens"],
              ...(admin
                ? ([
                    ["/users", "Users"],
                    ["/data-policy", "Data access"],
                  ] as const)
                : []),
              ["/audit", "Audit"],
            ] as const
          ).map(([to, label]) => (
            <Link
              key={to}
              to={to}
              className="text-mut no-underline hover:text-white"
              activeProps={{ className: "text-white!", "aria-current": "page" }}
            >
              {label}
            </Link>
          ))}
        </nav>
      )}
      <span className="ml-auto">
        <ServerDots />
      </span>
      <button
        type="button"
        onClick={() => openPalette()}
        className="flex items-center gap-1.5"
      >
        <kbd>{modKey.trim()}K</kbd>{" "}
        <span className="text-mut">search or run</span>
      </button>
      <UserMenu />
    </header>
  );
}

/** "production › api" (› r61), each part a link but the page itself. */
function EnvironmentCrumb() {
  const here = useCurrentContext();
  const crumb = useCrumb();
  if (!here.project || !here.environment) return null;
  const env = { project: here.project, environment: here.environment };
  if (!here.service) {
    return crumb ? (
      <Link to="/$project/$environment" params={env}>
        {here.environment}
      </Link>
    ) : (
      <span aria-current="page">{here.environment}</span>
    );
  }
  const service = { ...env, service: here.service };
  const atService = !here.release && !crumb;
  return (
    <>
      <span>{here.environment}</span>
      <Sep />
      {atService ? (
        <span aria-current="page">{here.service}</span>
      ) : (
        <Link to="/$project/$environment/$service" params={service}>
          {here.service}
        </Link>
      )}
      {here.release !== undefined && (
        <>
          <Sep />
          <span aria-current="page" className="font-mono">
            r{here.release}
          </span>
        </>
      )}
    </>
  );
}

/** A small popup menu anchored under its button. */
function Menu({
  label,
  button,
  right,
  children,
}: {
  label: string;
  button: ReactNode;
  /** Anchor to the right edge, for menus at the end of the bar. */
  right?: boolean;
  children: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const onMouse = (event: MouseEvent) => {
      const inside =
        event.target instanceof Node && root.current?.contains(event.target);
      if (!inside) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onMouse);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onMouse);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <span ref={root} className="relative">
      <button
        type="button"
        aria-label={label}
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="contents"
      >
        {button}
      </button>
      {open && (
        <div
          className={`absolute top-full ${right ? "right-0" : "left-0"} z-30 mt-1 flex min-w-40 flex-col rounded border border-field bg-hover py-1`}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </span>
  );
}

const menuItem =
  "px-3 py-1 text-left no-underline hover:bg-sel aria-[current=page]:font-semibold";

function ProjectSwitcher({ current }: { current: string | undefined }) {
  const projects = useProjects();
  if (projects.length === 0) return null;
  return (
    <Menu
      label="Switch project"
      button={<span className="btn sm">{current ?? "projects"} ▾</span>}
    >
      {(close) =>
        projects.map((p) => (
          <Link
            key={p.id}
            to="/$project"
            params={{ project: p.name }}
            onClick={close}
            className={menuItem}
            aria-current={p.name === current ? "page" : undefined}
          >
            {p.name}
          </Link>
        ))
      }
    </Menu>
  );
}

/**
 * ● per server in view: the environment's server, or every server the
 * project runs on. Red when offline.
 */
function ServerDots() {
  const here = useCurrentContext();
  const servers = useServers();
  const tree = useProjectTree();
  const admin = useIsAdmin();
  if (!here.project) return null;
  const environments =
    tree.find((p) => p.project.name === here.project)?.environments ?? [];
  const names = new Set(
    environments
      .filter(
        (e) => !here.environment || e.environment.name === here.environment,
      )
      .map((e) => e.environment.server),
  );
  const shown = servers.filter((s) => names.has(s.name));
  return (
    <span className="flex gap-2 font-mono text-xs">
      {shown.map((s) => (
        <Link
          key={s.id}
          // Server pages are for admins; everyone else gets the list.
          to={admin ? "/servers/$server" : "/servers"}
          params={{ server: s.name }}
          className="no-underline"
          title={`${s.name} is ${s.status}`}
        >
          <span className={s.status === "online" ? "text-ok" : "text-fail"}>
            ●
          </span>{" "}
          {s.name}
        </Link>
      ))}
    </span>
  );
}

function UserMenu() {
  const session = useSession();
  const logout = useLogout();
  if (!session) return null;
  return (
    <Menu
      label="Account"
      right
      button={<span className="text-mut">{session.name}</span>}
    >
      {() => (
        <button
          type="button"
          className={menuItem}
          disabled={logout.isPending}
          onClick={() => logout.mutate()}
        >
          Log out
        </button>
      )}
    </Menu>
  );
}
