import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useLogout } from "../data/mutations";
import { domainNeedsAttention, isFailing } from "../data/problems";
import {
  useOrgCounts,
  useProjectTree,
  useServers,
  useSession,
} from "../data/queries";
import type { ProjectOverview } from "../data/types";
import { Glyph, Kbd } from "../ui/kit";
import { serverStatus, serviceStatus } from "../ui/status";
import { modKey } from "./keys";
import { useCurrentContext } from "./location";
import { usePalette } from "./palette";

const row =
  "flex items-center justify-between gap-2 px-3.5 py-0.5 whitespace-nowrap hover:bg-hover";
const active = { className: "bg-sel" };
const note = "font-mono text-2xs text-dim";

export function Sidebar() {
  const session = useSession();
  const counts = useOrgCounts();
  const openPalette = usePalette();

  return (
    <aside className="overflow-auto border-r border-line pt-2.5 pb-15">
      <div className="flex items-center justify-between border-b border-line px-3.5 pt-1 pb-2.5">
        <b className="font-semibold">Tempo Dock</b>
        <span className="text-dim">{session?.name}</span>
      </div>
      <button
        type="button"
        onClick={openPalette}
        className="mx-3.5 mt-2.5 mb-1.5 flex w-[calc(100%-28px)] justify-between border-b border-line2 py-1 text-dim"
      >
        <span>Search or run</span>
        <Kbd>{modKey}K</Kbd>
      </button>

      <SectionLabel>Projects</SectionLabel>
      <ProjectTree />

      <SectionLabel>
        <Link to="/servers" className="hover:text-white">
          Servers
        </Link>
      </SectionLabel>
      <ServerList />

      <SectionLabel>Org</SectionLabel>
      <nav>
        <Link to="/tokens" className={row} activeProps={active}>
          <span>Tokens</span>
          <span className={note}>{counts.tokens}</span>
        </Link>
        <Link to="/audit" className={row} activeProps={active}>
          <span>Audit log</span>
        </Link>
        <Link to="/users" className={row} activeProps={active}>
          <span>Users</span>
          <span className={note}>{counts.users}</span>
        </Link>
        <LogoutButton />
      </nav>
    </aside>
  );
}

function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <div className="px-3.5 pt-3 pb-1 text-2xs tracking-[.06em] text-dim uppercase">
      {children}
    </div>
  );
}

/** Failing services and domains that need attention, for the badge. */
function health(project: ProjectOverview) {
  const services = project.environments.flatMap((e) => e.services);
  return {
    count: services.length,
    failing: services.filter(isFailing).length,
    warning: services.flatMap((s) => s.domains).filter(domainNeedsAttention)
      .length,
  };
}

function ProjectTree() {
  const projects = useProjectTree();
  const current = useCurrentContext().project;
  // Keep the last visited project open on org pages.
  const [lastOpen, setLastOpen] = useState(current);
  if (current && current !== lastOpen) setLastOpen(current);
  const open = current ?? lastOpen ?? projects[0]?.project.name;

  return (
    <nav>
      {projects.map((overview) => {
        const project = overview.project.name;
        const { count, failing, warning } = health(overview);
        return (
          <div key={project}>
            <Link
              to="/$project"
              params={{ project }}
              className={`${row} mt-1 font-semibold`}
              activeOptions={{ exact: true }}
              activeProps={active}
            >
              <span>{project}</span>
              <span className={note}>
                {failing + warning > 0 ? (
                  <>
                    {failing > 0 && (
                      <span className="text-bad">{failing} </span>
                    )}
                    {warning > 0 && (
                      <span className="text-warn">{warning}</span>
                    )}
                  </>
                ) : (
                  count
                )}
              </span>
            </Link>
            {open === project &&
              overview.environments.map(({ environment, services }) => (
                <div key={environment.id}>
                  <Link
                    to="/$project/$environment"
                    params={{ project, environment: environment.name }}
                    className={`${row} pl-[26px] text-dim`}
                    activeProps={active}
                    title={`${environment.name} shared variables`}
                  >
                    <span>{environment.name}</span>
                    <span className={note}>{environment.server}</span>
                  </Link>
                  {services.map((svc) => (
                    <Link
                      key={svc.service.id}
                      to="/$project/$environment/$service"
                      params={{
                        project,
                        environment: environment.name,
                        service: svc.service.name,
                      }}
                      className={`${row} pl-[38px]`}
                      activeProps={active}
                    >
                      <span className="flex items-baseline">
                        <Glyph tone={serviceStatus(svc).tone} />
                        {svc.service.name}
                      </span>
                      <span className={note}>
                        {svc.service.active_release
                          ? `r${svc.service.active_release}`
                          : ""}
                      </span>
                    </Link>
                  ))}
                </div>
              ))}
          </div>
        );
      })}
    </nav>
  );
}

function ServerList() {
  const servers = useServers();
  return (
    <div>
      {servers.map((server) => {
        const status = serverStatus(server);
        return (
          <Link
            key={server.id}
            to="/servers"
            hash={server.name}
            className={`${row} py-[3px]`}
          >
            <span className="flex items-baseline">
              <Glyph tone={status.tone} />
              {server.name}
            </span>
            <span className={note}>{status.label}</span>
          </Link>
        );
      })}
    </div>
  );
}

function LogoutButton() {
  const logout = useLogout();
  return (
    <button
      type="button"
      className={`${row} w-full text-dim`}
      disabled={logout.isPending}
      onClick={() => logout.mutate()}
    >
      Log out
    </button>
  );
}
