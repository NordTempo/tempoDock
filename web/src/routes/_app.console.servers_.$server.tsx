import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, notFound } from "@tanstack/react-router";
import { lazy, memo, Suspense, useEffect, useMemo, useState } from "react";
import { ApiError } from "../api/client";
import { queries, useServer, useServers } from "../data/queries";
import type {
  Server,
  ServerDetail,
  ServerService,
  ServerUsage,
  StatsRange,
} from "../data/types";
import { DiskBreakdown } from "../features/servers/DiskBreakdown";
import { DrainSection } from "../features/servers/DrainSection";
import { drainQuery } from "../features/servers/drain";
import { LinkSection } from "../features/servers/LinkSection";
import { MemoryBreakdown } from "../features/servers/MemoryBreakdown";
import { useServerSwitches } from "../features/servers/switches";
import { TunnelSection } from "../features/servers/TunnelSection";
import { CpuUsage, MemoryUsage } from "../features/service/Usage";
import { LineChart } from "../features/stats/LineChart";
import { useLiveServer } from "../features/stats/live";
import { LiveNote, RangeSwitch, Stat } from "../features/stats/parts";
import {
  type Point,
  rangeLabel,
  rangeMs,
  ranges,
  validateRange,
} from "../features/stats/series";
import { useOrg } from "../shell/location";
import { ago, bytes, pct, plural, size } from "../ui/format";
import {
  Act,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
  SectionHead,
  StateText,
} from "../ui/kit";
import { containerState, kindLabel } from "../ui/status";

const RenameServer = lazy(() => import("../features/servers/RenameServer"));

// One server: live usage, history, what is placed on it, where its memory
// goes and the services there by memory. /console/servers/homelab?range=24h
export const Route = createFileRoute("/_app/console/servers_/$server")({
  validateSearch: validateRange,
  loaderDeps: ({ search }) => ({ range: search.range ?? "1h" }),
  loader: async ({ context: { queryClient }, params, deps }) => {
    try {
      await Promise.all([
        queryClient.ensureQueryData(queries.server(params.server)),
        queryClient.ensureQueryData(
          queries.serverStats(params.server, deps.range),
        ),
        queryClient.ensureQueryData(drainQuery(params.server)),
      ]);
    } catch (error) {
      // Caught by this route's notFoundComponent, inside the console shell.
      if (error instanceof ApiError && error.status === 404) throw notFound();
      throw error;
    }
  },
  component: ServerPage,
  notFoundComponent: ServerNotFound,
});

/** A server name with no server (removed, or a typo), with the way back. */
function ServerNotFound() {
  const { server } = Route.useParams();
  return (
    <>
      <PageBar
        parent={
          <Link to="/console/servers" className="hover:text-fg">
            Servers
          </Link>
        }
        title={<span className="font-mono">{server}</span>}
      />
      <Page gap="gap-2">
        <h2 className="text-md font-normal">
          Server <b className="font-mono">{server}</b> not found
        </h2>
        <p className="text-mut">It may have been removed or renamed.</p>
        <Link to="/console/servers" className="btn pri mt-2 self-start">
          Back to servers
        </Link>
      </Page>
    </>
  );
}

function ServerPage() {
  const { server: name } = Route.useParams();
  const range = Route.useSearch().range ?? "1h";
  const navigate = Route.useNavigate();
  const detail = useServer(name);
  const { server } = detail;
  const live = useLiveServer(name);
  const { accepts, placement, builder, confirm, error } =
    useServerSwitches(server);
  const [renaming, setRenaming] = useState(false);
  // Renamed elsewhere: the list has the new name (by ID); follow it.
  const listed = useServers().find((s) => s.id === server.id)?.name;
  useEffect(() => {
    if (server.id > 0 && listed && listed !== name) {
      navigate({ params: { server: listed }, search: true, replace: true });
    }
  }, [server.id, listed, name, navigate]);
  // What the server is, under its status: "dock 0.27.0 · control plane".
  const facts = [
    server.version && `dock ${server.version}`,
    server.local && "control plane",
    server.builder && "builds images",
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <>
      <PageBar
        parent={
          <Link to="/console/servers" className="hover:text-fg">
            Servers
          </Link>
        }
        title={<span className="font-mono">{name}</span>}
        actions={
          // A joining server has no row to rename yet.
          server.id > 0 && (
            <Act
              small
              cli={`dock server rename ${name} <name>`}
              onClick={() => setRenaming(true)}
            >
              Rename
            </Act>
          )
        }
      />
      {renaming && (
        <Suspense fallback={null}>
          <RenameServer server={server} onClose={() => setRenaming(false)} />
        </Suspense>
      )}
      <Page>
        <div className="flex flex-col gap-1">
          <StatusSentence detail={detail} />
          {facts && <p className="text-mut">{facts}</p>}
        </div>

        <LinkSection server={server} />
        {(server.tunnel || server.tunnel_port > 0) && (
          <TunnelSection server={server} />
        )}

        <section aria-label="Now" className="flex flex-col gap-2">
          <Now usage={server.usage} server={server} />
          <LiveNote state={server.status === "online" ? live : "lost"} />
        </section>

        <section aria-labelledby="history">
          <SectionHead
            id="history"
            aside={rangeLabel[range]}
            right={
              <RangeSwitch
                value={range}
                options={ranges}
                onChange={(r) =>
                  navigate({
                    search: r === "1h" ? {} : { range: r },
                    replace: true,
                  })
                }
              />
            }
          >
            History
          </SectionHead>
          <History
            name={name}
            range={range}
            usage={server.usage}
            server={server}
          />
        </section>

        <section aria-labelledby="placement" className="flex flex-col gap-2">
          <SectionHead id="placement">Placement</SectionHead>
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
            <span>
              <span className="text-mut">Accepts</span> {accepts}
            </span>
            {placement}
            {builder}
          </div>
          {confirm}
          <ErrorText error={error} />
          <p className="font-mono text-2xs text-mut">
            dock server update {name} --production | --no-production |
            --non-production | --no-non-production | --disable-placement |
            --enable-placement | --builder | --no-builder
          </p>
          <Placements detail={detail} />
        </section>

        {server.status !== "joining" && <DrainSection detail={detail} />}

        <MemoryBreakdown usage={server.usage} />
        {server.status === "online" && <DiskBreakdown server={name} />}

        <Services name={name} services={detail.services} />
      </Page>
    </>
  );
}

/** What the server is doing, in a sentence. */
function StatusSentence({ detail }: { detail: ServerDetail }) {
  const { server: s } = detail;
  const reserved = s.memory_bytes
    ? `${bytes(s.reserved_memory_bytes)} of ${bytes(s.memory_bytes)} memory reserved (${pct((100 * s.reserved_memory_bytes) / s.memory_bytes)}).`
    : "";
  const what = s.environments
    ? `${plural(s.environments, "environment")} of ${plural(s.projects, "project")}`
    : "nothing yet";
  switch (s.status) {
    case "offline":
      return (
        <p className="text-md leading-normal">
          <span className="text-fail">✕</span> Offline
          {s.last_seen_at && `, last seen ${ago(s.last_seen_at)}`}.{" "}
          <span className="text-mut">
            {s.environments
              ? `Its ${what} keep running there but can't be deployed until it is back.`
              : "Nothing is placed here; it is safe to remove."}
          </span>
        </p>
      );
    case "joining":
      return (
        <p className="text-md leading-normal">
          <span className="text-prog">
            <Glyph char="◐" />
          </span>{" "}
          Joining.{" "}
          <span className="text-mut">
            Waiting for <span className="font-mono">dock node join</span> to
            connect with the token shown when it was added. Lost the token?
            Remove it and add it again.
          </span>
        </p>
      );
    case "incompatible":
      return (
        <p className="text-md leading-normal">
          <span className="text-warn">▲</span> Connected, but dock{" "}
          {s.version || "there"} is too far from this control plane's.{" "}
          <span className="text-mut">
            Nothing is placed or deployed here until dock is upgraded on it.
          </span>
        </p>
      );
    case "revoked":
      return (
        <p className="text-md leading-normal">
          <span className="text-mut">○</span> Revoked
          {s.revoked_at && ` ${ago(s.revoked_at)}`}.{" "}
          <span className="text-mut">
            {s.environments
              ? `Its ${what} keep running there but can't be reached or deployed. Add it again under this name to re-join.`
              : "Nothing is placed here; it is safe to remove."}
          </span>
        </p>
      );
    case "online":
      break;
  }
  return (
    <p className="text-md leading-normal">
      <span className="text-ok">●</span> Online, running {what}.{" "}
      <span className="text-mut">
        {reserved} {s.disabled_for_placement && "Takes no new projects."}
        {!s.accepts_production &&
          !s.accepts_non_production &&
          "Accepts no environment kinds."}
      </span>
    </p>
  );
}

/**
 * The latest sample, live while the page is open. Disk is the filesystem
 * holding Docker's data root; dock's data dir shows separately when it is
 * on another one.
 */
function Now({ usage, server }: { usage?: ServerUsage; server: Server }) {
  if (!usage) {
    return (
      <p className="text-mut">No sample yet. One is taken every minute.</p>
    );
  }
  const dataDir = usage.disks?.[1];
  return (
    <div className="flex flex-wrap gap-x-10 gap-y-3">
      <Stat
        label="CPU"
        value={pct(usage.cpu_percent)}
        of={`of ${usage.cpus} CPUs`}
      />
      <Stat
        label="Memory"
        value={size(usage.memory_used_bytes)}
        of={`/ ${size(usage.memory_total_bytes || server.memory_bytes)}`}
      />
      <Stat
        label="Disk"
        value={size(usage.disk_used_bytes)}
        of={
          usage.disk_total_bytes
            ? `/ ${size(usage.disk_total_bytes)}`
            : undefined
        }
      />
      {dataDir && (
        <Stat
          label="Data dir disk"
          value={size(dataDir.used_bytes)}
          of={`/ ${size(dataDir.total_bytes)}`}
        />
      )}
      <Stat label="Load" value={usage.load1.toFixed(2)} of="1 min avg" />
    </div>
  );
}

/** CPU, memory and disk over the range, one small chart each. */
function History({
  name,
  range,
  usage,
  server,
}: {
  name: string;
  range: StatsRange;
  usage?: ServerUsage;
  server: Server;
}) {
  // Usage events append each new minute point (data/live.ts).
  const { data: stats, dataUpdatedAt } = useSuspenseQuery(
    queries.serverStats(name, range),
  );
  const series = useMemo(() => {
    const of = (pick: (p: (typeof stats.points)[number]) => number): Point[] =>
      stats.points.map((p) => ({ t: Date.parse(p.time), v: pick(p) }));
    return {
      cpu: of((p) => p.cpu_percent),
      memory: of((p) => p.memory_used_bytes),
      disk: of((p) => p.disk_used_bytes),
    };
  }, [stats]);
  const to = dataUpdatedAt;
  const from = to - rangeMs[range];
  const memoryTotal = usage?.memory_total_bytes || server.memory_bytes;
  const diskTotal = usage?.disk_total_bytes;
  const common = { from, to, resolution: stats.resolution_seconds };
  return (
    <div className="mt-2 grid gap-x-8 gap-y-5 lg:grid-cols-3">
      <LineChart
        {...common}
        title="CPU"
        points={series.cpu}
        max={100}
        format={pct}
      />
      <LineChart
        {...common}
        title="Memory"
        points={series.memory}
        max={memoryTotal || peak(series.memory)}
        format={size}
      />
      <LineChart
        {...common}
        title="Disk"
        points={series.disk}
        max={diskTotal || peak(series.disk)}
        format={size}
      />
    </div>
  );
}

/** Highest value, with headroom, for charts without a natural top. */
const peak = (points: Point[]) => Math.max(1, ...points.map((p) => p.v)) * 1.2;

/** Projects and environments placed here, per kind. */
function Placements({ detail }: { detail: ServerDetail }) {
  const org = useOrg();
  if (detail.placements.length === 0) {
    return <p className="text-mut">No projects are placed here yet.</p>;
  }
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Project</th>
            <th>Kind</th>
            <th>Environments</th>
            <th>Placed</th>
          </tr>
        </thead>
        <tbody>
          {detail.placements.map((p) => (
            <tr key={`${p.project}:${p.kind}`}>
              <td>
                <Link to="/$org/$project" params={{ org, project: p.project }}>
                  {p.project}
                </Link>
              </td>
              <td>{kindLabel[p.kind]}</td>
              <td className="font-mono">
                {(p.environments ?? []).join(", ") || (
                  <span className="font-sans text-mut">none yet</span>
                )}
              </td>
              <td className="w-full text-mut">
                {p.pinned ? "pinned by an operator" : "automatically"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  );
}

const memoryOf = (s: ServerService) => s.usage?.memory_bytes ?? -1;

/**
 * Every service on the server, the biggest memory users first (again
 * after usage events patch them).
 */
function Services({
  name,
  services: all,
}: {
  name: string;
  services: ServerService[];
}) {
  const services = useMemo(
    () => all.toSorted((a, b) => memoryOf(b) - memoryOf(a)),
    [all],
  );
  return (
    <section aria-labelledby="services">
      {/* Sorted by memory, from the latest minute's sample. */}
      <SectionHead id="services">Services on {name}</SectionHead>
      {services.length === 0 ? (
        <p className="text-mut">No services run here.</p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Service</th>
                <th>Environment</th>
                <th>State</th>
                <th>Memory</th>
                <th>CPU</th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <ServiceRow
                  key={`${s.project}/${s.environment}/${s.name}`}
                  service={s}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
    </section>
  );
}

const ServiceRow = memo(function ServiceRow({
  service: s,
}: {
  service: ServerService;
}) {
  const org = useOrg();
  const ref = {
    project: s.project,
    environment: s.environment,
    service: s.name,
  };
  return (
    <tr>
      <td>
        <Link
          to="/$org/$project/$environment/$service/metrics"
          params={{ org, ...ref }}
        >
          {ref.service}
        </Link>
      </td>
      <td className="font-mono">
        {ref.project}/{ref.environment}
      </td>
      <td>
        <StateText state={containerState(s)} />
      </td>
      <td className="font-mono">
        <MemoryUsage service={s} />
      </td>
      <td className="w-full font-mono">
        <CpuUsage service={s} />
      </td>
    </tr>
  );
});
