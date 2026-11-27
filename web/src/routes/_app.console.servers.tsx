import { createFileRoute, Link } from "@tanstack/react-router";
import { memo, useState } from "react";
import { queries, useServers } from "../data/queries";
import type { Server } from "../data/types";
import { ConsoleTabs } from "../features/console/ConsoleTabs";
import { AddServer } from "../features/servers/AddServer";
import { Ingress, statusTitle, Version } from "../features/servers/parts";
import { useShortcuts } from "../shell/keys";
import { pct, since, size, until } from "../ui/format";
import { Act, Glyph, Page, PageBar, Panel, StateText } from "../ui/kit";
import { serverState } from "../ui/status";

// Every server: link status, version, ingress, what it accepts, how loaded
// it is. Add servers (A), switch accepted kinds and builds inline, and open
// a server for its link, statistics and placements. The list carries each
// server's latest sample; usage and server status events keep it current.
export const Route = createFileRoute("/_app/console/servers")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(queries.session()),
      queryClient.ensureQueryData(queries.servers()),
    ]),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs): Servers, Security. */
function Tabbed() {
  return (
    <ConsoleTabs>
      <Servers />
    </ConsoleTabs>
  );
}

function Servers() {
  const servers = useServers();
  const [adding, setAdding] = useState(false);

  useShortcuts([{ key: "n", label: "add server", run: () => setAdding(true) }]);

  return (
    <>
      <PageBar
        title="Servers"
        actions={
          <Act
            primary
            cli="dock server add"
            disabled={adding}
            onClick={() => setAdding(true)}
          >
            Add server <kbd>N</kbd>
          </Act>
        }
      />
      <Page>
        {adding && <AddServer onClose={() => setAdding(false)} />}
        <Kinds servers={servers} onAdd={() => setAdding(true)} />
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Server</th>
                <th>Status</th>
                <th>Version</th>
                <th>Reached at</th>
                <th>Runs</th>
                <th>CPU</th>
                <th>Memory</th>
                <th title="Memory limits of the services placed here">
                  Memory reserved
                </th>
                <th>Disk</th>
                <th>Hosts</th>
              </tr>
            </thead>
            <tbody>
              {servers.map((s) => (
                <ServerRow key={s.id} server={s} />
              ))}
            </tbody>
          </table>
        </Panel>
      </Page>
    </>
  );
}

/**
 * Where each kind of environment can go (B1's pools), as one line: how
 * much memory the servers accepting it have reserved. With one server, an
 * amber line says it carries everything.
 */
function Kinds({ servers, onAdd }: { servers: Server[]; onAdd: () => void }) {
  const live = servers.filter((s) => s.status !== "revoked");
  const kinds = [
    ["Production", live.filter((s) => s.accepts_production)],
    ["Non-production", live.filter((s) => s.accepts_non_production)],
  ] as const;
  return (
    <div className="flex flex-col gap-1.5">
      <p className="flex flex-wrap gap-x-5 gap-y-1">
        {kinds.map(([label, accepting]) => {
          const memory = accepting.reduce((n, s) => n + s.memory_bytes, 0);
          const reserved = accepting.reduce(
            (n, s) => n + s.reserved_memory_bytes,
            0,
          );
          return (
            <span key={label} title={accepting.map((s) => s.name).join(", ")}>
              {label}{" "}
              <span className="text-mut">
                {!accepting.length
                  ? "no server"
                  : memory
                    ? `${pct((100 * reserved) / memory)} reserved`
                    : "memory unknown"}
              </span>
            </span>
          );
        })}
      </p>
      {live.length === 1 && (
        <p className="text-warn-text">
          <span className="text-warn">
            <Glyph char="▲" />
          </span>{" "}
          Single server: the control plane and every app run on one. Add a
          second to spread the load.{" "}
          <button
            type="button"
            className="text-fg underline underline-offset-3"
            onClick={onAdd}
          >
            Add server
          </button>
        </p>
      )}
    </div>
  );
}

/**
 * What a server takes, in words: the kinds of environment, new projects
 * and builds. Its page (Console, Servers, the server) changes them.
 */
function takes(s: Server): string {
  const kinds = [
    s.accepts_production && "production",
    s.accepts_non_production && "non-production",
  ].filter(Boolean);
  return [
    kinds.length ? kinds.join(" and ") : "no environments",
    s.disabled_for_placement ? "closed to new projects" : "new projects",
    s.builder && "builds",
  ]
    .filter(Boolean)
    .join(", ");
}

/** Under the status word: what the status means right now. */
function statusNote(s: Server) {
  switch (s.status) {
    case "offline":
      return s.last_seen_at
        ? `last seen ${since(s.last_seen_at)} ago`
        : "never connected";
    case "joining":
      if (!s.join_expires_at) return "waiting for it to connect";
      return Date.parse(s.join_expires_at) > Date.now()
        ? `token expires in ${until(s.join_expires_at)}`
        : "token expired, remove and add again";
    case "incompatible":
      return "upgrade dock there";
    case "revoked":
      return s.revoked_at ? `${since(s.revoked_at)} ago` : undefined;
    case "online":
      return undefined;
  }
}

const ServerRow = memo(function ServerRow({ server: s }: { server: Server }) {
  const offline = s.status === "offline";
  const usage = s.usage;
  const dash = <span className="text-faint">·</span>;
  const note = statusNote(s);
  return (
    <tr id={s.name} className={offline ? "bg-fail-bg/50" : undefined}>
      <td className="py-2.5 font-mono font-medium">
        <Link to="/console/servers/$server" params={{ server: s.name }}>
          {s.name}
        </Link>
        {s.local && (
          <div className="font-sans text-xs font-normal text-mut">
            control plane
          </div>
        )}
      </td>
      <td className="py-2.5" title={statusTitle(s)}>
        <StateText state={serverState(s.status)} />
        {note && <div className="text-xs text-mut">{note}</div>}
      </td>
      <td className="py-2.5">
        <Version server={s} />
      </td>
      <td className="py-2.5">
        <Ingress server={s} />
      </td>
      <td className="w-full min-w-48 py-2.5 whitespace-normal text-sec">
        {takes(s)}
      </td>
      <td className="py-2.5 font-mono">
        {usage ? pct(usage.cpu_percent) : dash}
        {usage && <span className="text-mut"> of {usage.cpus}</span>}
      </td>
      <td className="py-2.5 font-mono">
        {usage ? (
          <>
            {size(usage.memory_used_bytes)}
            <span className="text-mut">
              {" "}
              / {size(usage.memory_total_bytes)}
            </span>
          </>
        ) : s.memory_bytes ? (
          <span className="text-mut">{size(s.memory_bytes)}</span>
        ) : (
          dash
        )}
      </td>
      <td className="py-2.5 font-mono">
        {s.memory_bytes ? (
          <>
            {size(s.reserved_memory_bytes)}
            <span className="text-mut">
              {" "}
              {pct((100 * s.reserved_memory_bytes) / s.memory_bytes)}
            </span>
          </>
        ) : (
          dash
        )}
      </td>
      <td className="py-2.5 font-mono">
        {usage?.disk_total_bytes ? (
          <>
            {size(usage.disk_used_bytes)}
            <span className="text-mut"> / {size(usage.disk_total_bytes)}</span>
          </>
        ) : (
          dash
        )}
      </td>
      <td className="py-2.5">
        {s.environments ? (
          <>
            {s.projects} <span className="text-mut">projects</span>
            <span className="block">
              {s.environments} <span className="text-mut">envs</span>
            </span>
          </>
        ) : (
          <span className="text-mut">
            nothing{offline && !s.local ? ", safe to remove" : ""}
          </span>
        )}
      </td>
    </tr>
  );
});
