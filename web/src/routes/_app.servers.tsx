import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { memo, type ReactNode } from "react";
import { queries, useIsAdmin, useServers } from "../data/queries";
import type { Server } from "../data/types";
import { useServerSwitches } from "../features/servers/switches";
import { pct, since, size } from "../ui/format";
import { ErrorText, Page, Scroll, TitleRow } from "../ui/kit";
import { serverKinds } from "../ui/status";

// Every server: status, what it accepts, how loaded it is. Admins switch
// accepted kinds inline and open a server for statistics and placements.
export const Route = createFileRoute("/_app/servers")({
  loader: async ({ context: { queryClient } }) => {
    const [session, servers] = await Promise.all([
      queryClient.ensureQueryData(queries.session()),
      queryClient.ensureQueryData(queries.servers()),
    ]);
    // Live numbers come with each server's detail, which needs admin.
    if (session?.scope === "admin") {
      for (const s of servers)
        queryClient.prefetchQuery(queries.server(s.name));
    }
  },
  component: Servers,
});

function Servers() {
  const servers = useServers();
  const admin = useIsAdmin();
  const online = servers.filter((s) => s.status === "online").length;
  const offline = servers.length - online;

  return (
    <Page gap="gap-[18px]">
      <TitleRow
        title="Servers"
        context={`${online} online${offline ? ` · ${offline} offline` : ""}`}
        actions={
          <span className="self-center font-mono text-2xs text-mut">
            dock server add
          </span>
        }
      />
      <Scroll>
        <table>
          <thead>
            <tr>
              <th>Server</th>
              <th>Status</th>
              <th>Accepts</th>
              <th>Placement</th>
              <th>Builds</th>
              <th>CPU</th>
              <th>Memory</th>
              <th title="Memory limits of the services placed here">
                Reserved
              </th>
              <th>Disk</th>
              <th>Placed</th>
              <th>Version</th>
            </tr>
          </thead>
          <tbody>
            {servers.map((s) =>
              admin ? (
                <AdminRow key={s.id} server={s} />
              ) : (
                <Row key={s.id} server={s} />
              ),
            )}
          </tbody>
        </table>
      </Scroll>
      {servers.length > 0 && !admin && (
        <p className="text-xs text-mut">
          Admins see live usage and manage placement.
        </p>
      )}
    </Page>
  );
}

const COLUMNS = 11;

/** A row with the admin's switches, and a confirm row when asked. */
const AdminRow = memo(function AdminRow({ server }: { server: Server }) {
  const { accepts, placement, confirm, error } = useServerSwitches(server);
  return (
    <>
      <Row server={server} accepts={accepts} placement={placement} admin />
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

const Row = memo(function Row({
  server: s,
  accepts,
  placement,
  admin,
}: {
  server: Server;
  accepts?: ReactNode;
  placement?: ReactNode;
  admin?: boolean;
}) {
  const offline = s.status === "offline";
  // Admins get the latest sample with the server's detail.
  const usage = useQuery({ ...queries.server(s.name), enabled: !!admin }).data
    ?.usage;
  const disk = usage?.disks?.reduce(
    (sum, d) => ({
      used: sum.used + d.used_bytes,
      total: sum.total + d.total_bytes,
    }),
    { used: 0, total: 0 },
  );
  const dash = <span className="text-faint">·</span>;
  return (
    <tr id={s.name} className={offline ? "bg-[#140808]" : undefined}>
      <td className="py-2.5 font-mono font-medium">
        {admin ? (
          <Link to="/servers/$server" params={{ server: s.name }}>
            {s.name}
          </Link>
        ) : (
          s.name
        )}
        {s.local && (
          <div className="font-sans text-xs font-normal text-mut">
            control plane
          </div>
        )}
      </td>
      <td className="py-2.5">
        {offline ? (
          <>
            <span className="text-fail">✕ offline</span>
            {s.last_seen_at && (
              <div className="text-xs text-mut">
                last seen {since(s.last_seen_at)} ago
              </div>
            )}
          </>
        ) : (
          <span className="text-ok">● online</span>
        )}
      </td>
      <td className="py-2.5">{accepts ?? serverKinds(s)}</td>
      <td className="py-2.5">
        {placement ??
          (s.disabled_for_placement ? (
            <span className="text-warn">disabled</span>
          ) : (
            <span className="text-mut">open</span>
          ))}
      </td>
      <td className={`py-2.5 ${s.builder ? "" : "text-mut"}`}>
        {s.builder ? "yes" : "no"}
      </td>
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
        {disk?.total ? (
          <>
            {size(disk.used)}
            <span className="text-mut"> / {size(disk.total)}</span>
          </>
        ) : (
          dash
        )}
      </td>
      <td className="py-2.5">
        {s.environments ? (
          <>
            {s.projects} <span className="text-mut">projects ·</span>{" "}
            {s.environments} <span className="text-mut">envs</span>
          </>
        ) : (
          <span className="text-mut">
            nothing{offline ? ", safe to remove" : ""}
          </span>
        )}
      </td>
      <td className="w-full py-2.5 font-mono text-mut">{s.version}</td>
    </tr>
  );
});
