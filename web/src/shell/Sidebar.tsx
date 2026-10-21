import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { useLogout } from "../data/mutations";
import { useOrg, useProjects, useServers, useSession } from "../data/queries";
import { Glyph, Kbd } from "../ui/kit";
import { serverStatus, serviceStatus } from "../ui/status";
import { modKey } from "./keys";
import { useCurrentProjectId } from "./location";
import { usePalette } from "./palette";

const row =
  "flex items-center justify-between gap-2 px-3.5 py-0.5 whitespace-nowrap hover:bg-hover";
const active = { className: "bg-sel" };
const note = "font-mono text-2xs text-dim";

export function Sidebar() {
  const org = useOrg();
  const session = useSession();
  const openPalette = usePalette();

  return (
    <aside className="overflow-auto border-r border-line pt-2.5 pb-15">
      <div className="flex items-center justify-between border-b border-line px-3.5 pt-1 pb-2.5">
        <b className="font-semibold">{org.name}</b>
        <span className="text-dim">{session?.username}</span>
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
          <span className={note}>{org.tokenCount}</span>
        </Link>
        <Link to="/audit" className={row} activeProps={active}>
          <span>Audit log</span>
        </Link>
        <Link to="/users" className={row} activeProps={active}>
          <span>Users</span>
          <span className={note}>{org.userCount}</span>
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

function ProjectTree() {
  const projects = useProjects();
  const current = useCurrentProjectId();
  // Keep the last visited project open on org pages.
  const [lastOpen, setLastOpen] = useState(current);
  if (current && current !== lastOpen) setLastOpen(current);
  const open = current ?? lastOpen ?? projects[0]?.id;

  return (
    <nav>
      {projects.map((project) => (
        <div key={project.id}>
          <Link
            to="/projects/$projectId"
            params={{ projectId: project.id }}
            className={`${row} mt-1 font-semibold`}
            activeOptions={{ exact: true }}
            activeProps={active}
          >
            <span>{project.name}</span>
            <span className={note}>
              {project.failing + project.warning > 0 ? (
                <>
                  {project.failing > 0 && (
                    <span className="text-bad">{project.failing} </span>
                  )}
                  {project.warning > 0 && (
                    <span className="text-warn">{project.warning}</span>
                  )}
                </>
              ) : (
                project.serviceCount
              )}
            </span>
          </Link>
          {open === project.id &&
            project.environments.map((env) => (
              <div key={env.id}>
                <Link
                  to="/projects/$projectId/variables"
                  params={{ projectId: project.id }}
                  search={{ env: env.name }}
                  className={`${row} pl-[26px] text-dim`}
                  title={`${env.name} variables`}
                >
                  <span>{env.name}</span>
                  <span className={note}>{env.serverName}</span>
                </Link>
                {env.services.map((svc) => (
                  <Link
                    key={svc.id}
                    to="/services/$serviceId"
                    params={{ serviceId: svc.id }}
                    className={`${row} pl-[38px]`}
                    activeProps={active}
                  >
                    <span className="flex items-baseline">
                      <Glyph tone={serviceStatus(svc.state).tone} />
                      {svc.name}
                    </span>
                    <span className={note}>
                      {svc.release ? `r${svc.release}` : ""}
                    </span>
                  </Link>
                ))}
              </div>
            ))}
        </div>
      ))}
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
            hash={server.id}
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
  const navigate = useNavigate();
  return (
    <button
      type="button"
      className={`${row} w-full text-dim`}
      onClick={() =>
        logout.mutate(undefined, {
          onSuccess: () => navigate({ to: "/login" }),
        })
      }
    >
      Log out
    </button>
  );
}
