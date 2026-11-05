import { createFileRoute, Link } from "@tanstack/react-router";
import { memo, type ReactNode, useState } from "react";
import { queries, useServers } from "../data/queries";
import type { Server } from "../data/types";
import { AddServer } from "../features/servers/AddServer";
import { Ingress, statusTitle, Version } from "../features/servers/parts";
import { useServerSwitches } from "../features/servers/switches";
import { useShortcuts } from "../shell/keys";
import { pct, plural, since, size, until } from "../ui/format";
import { Act, ErrorText, Page, PageBar, Panel, StateText } from "../ui/kit";
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
  component: Servers,
});

function Servers() {
  const servers = useServers();
  const [adding, setAdding] = useState(false);
  const count = (status: Server["status"]) =>
    servers.filter((s) => s.status === status).length;
  const context = (
    [
      ["online", count("online")],
      ["offline", count("offline")],
      ["joining", count("joining")],
      ["incompatible", count("incompatible")],
      ["revoked", count("revoked")],
    ] as const
  )
    .filter(([word, n]) => n > 0 || word === "online")
    .map(([word, n]) => `${n} ${word}`)
    .join(" · ");

  useShortcuts([{ key: "a", label: "add server", run: () => setAdding(true) }]);

  return (
    <>
      <PageBar
        title="Servers"
        after={<span className="text-xs text-faint">{context}</span>}
        actions={
          <Act
            primary
            cli="dock server add"
            disabled={adding}
            onClick={() => setAdding(true)}
          >
            Add server <kbd>A</kbd>
          </Act>
        }
      />
      <Page width="max-w-[1400px]" gap="gap-[18px]">
        {adding && <AddServer onClose={() => setAdding(false)} />}
        <Kinds servers={servers} />
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Server</th>
                <th>Status</th>
                <th>Version</th>
                <th>Ingress</th>
                <th>Builds</th>
                <th>Accepts</th>
                <th>Placement</th>
                <th>CPU</th>
                <th>Memory</th>
                <th title="Memory limits of the services placed here">
                  Reserved
                </th>
                <th>Disk</th>
                <th>Placed</th>
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

const COLUMNS = 12;

/**
 * Where each kind of environment can go (B1's pools): the servers that
 * accept it and how much of their memory is reserved, and a warning
 * when one server carries everything.
 */
function Kinds({ servers }: { servers: Server[] }) {
  const live = servers.filter((s) => s.status !== "revoked");
  const kinds = [
    ["Production", live.filter((s) => s.accepts_production)],
    ["Non-production", live.filter((s) => s.accepts_non_production)],
  ] as const;
  return (
    <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-3">
      {kinds.map(([label, accepting]) => {
        const memory = accepting.reduce((n, s) => n + s.memory_bytes, 0);
        const reserved = accepting.reduce(
          (n, s) => n + s.reserved_memory_bytes,
          0,
        );
        const share = memory ? Math.min(1, reserved / memory) : 0;
        return (
          <section
            key={label}
            aria-label={`${label} environments`}
            className="flex flex-col gap-2 rounded-panel border border-line px-3.5 py-3"
          >
            <div className="flex items-baseline gap-2">
              <h2 className="flex-1 font-medium">{label}</h2>
              <span className="text-xs text-faint">
                {plural(accepting.length, "server")}
              </span>
            </div>
            <div className="h-1 overflow-hidden rounded-full bg-chip">
              <div
                className="h-full rounded-full bg-sec"
                style={{ width: `${share * 100}%` }}
              />
            </div>
            <span className="text-xs text-mut">
              {memory
                ? `${pct(share * 100)} of memory reserved`
                : "memory unknown"}{" "}
              · {accepting.map((s) => s.name).join(", ") || "no server"}
            </span>
          </section>
        );
      })}
      {live.length === 1 && (
        <section
          aria-label="Single point of failure"
          className="flex flex-col gap-1.5 rounded-panel border border-warn-edge bg-warn-bg/60 px-3.5 py-3"
        >
          <h2 className="font-medium">Single point of failure</h2>
          <span className="text-xs text-warn-text">
            Everything, the control plane included, runs on one server. Add a
            second to spread the load.
          </span>
        </section>
      )}
    </div>
  );
}

/** A server's row with its switches, and a confirm row when asked. */
const ServerRow = memo(function ServerRow({ server }: { server: Server }) {
  const { accepts, placement, builder, confirm, error } =
    useServerSwitches(server);
  return (
    <>
      <Row
        server={server}
        accepts={accepts}
        placement={placement}
        builder={builder}
      />
      {(confirm || error) && (
        <tr>
          <td colSpan={COLUMNS} className="border-0 pb-3">
            {confirm ?? <ErrorText error={error} />}
          </td>
        </tr>
      )}
    </>
  );
});

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

const Row = memo(function Row({
  server: s,
  accepts,
  placement,
  builder,
}: {
  server: Server;
  accepts: ReactNode;
  placement: ReactNode;
  builder: ReactNode;
}) {
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
      <td className={`py-2.5 ${s.builder ? "" : "text-mut"}`}>{builder}</td>
      <td className="py-2.5">{accepts}</td>
      <td className="py-2.5">{placement}</td>
      <td className="py-2.5 font-mono">
        {usage ? pct(usage.cpu_percent) : dash}
        {usage && (
          <span className="font-sans text-xs text-mut"> of {usage.cpus}</span>
        )}
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
            <span className="font-sans text-xs text-mut">
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
      <td className="w-full py-2.5">
        {s.environments ? (
          <>
            {s.projects} <span className="text-mut">projects ·</span>{" "}
            {s.environments} <span className="text-mut">envs</span>
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
