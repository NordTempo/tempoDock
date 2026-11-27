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
import { DiskBreakdown, DiskSummary } from "../features/servers/DiskBreakdown";
import { DrainSection } from "../features/servers/DrainSection";
import { drainQuery } from "../features/servers/drain";
import { Fold } from "../features/servers/Fold";
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
  rangeMs,
  ranges,
  serverRange,
  validateRange,
} from "../features/stats/series";
import { useOrg } from "../shell/location";
import { ago, bytes, pct, plural, size } from "../ui/format";
import {
  Act,
  ErrorText,
  Glyph,
  MoreItem,
  MoreMenu,
  Page,
  PageBar,
  Panel,
  StateText,
} from "../ui/kit";
import { containerState, kindLabel, serverState } from "../ui/status";

const RenameServer = lazy(() => import("../features/servers/RenameServer"));

// One server: live usage and history, then a row per section (placement,
// services, memory, disk, network, drain) that opens in place (ADR 0099).
// /console/servers/homelab?range=24h
export const Route = createFileRoute("/_app/console/servers_/$server")({
  validateSearch: validateRange,
  loaderDeps: ({ search }) => ({
    range: search.range ?? serverRange.get(),
  }),
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
        title="Not found"
      />
      <Page gap="gap-2">
        <p className="text-md">
          There's no server called <b className="font-mono">{server}</b>.
        </p>
        <p className="text-mut">It may have been removed or renamed.</p>
        <Link to="/console/servers" className="btn pri mt-2 self-start">
          Back to servers
        </Link>
      </Page>
    </>
  );
}

/**
 * One server (ADR 0099): its state and what it runs in a sentence, live
 * usage with the range's lines, then one row per section that says its
 * state and opens in place. Drain is a button in the bar, since moving
 * everything off a server is the big thing done here; Rename is in "···".
 */
function ServerPage() {
  const { server: name } = Route.useParams();
  const range = Route.useSearch().range ?? serverRange.get();
  const navigate = Route.useNavigate();
  const detail = useServer(name);
  const { server } = detail;
  const live = useLiveServer(name);
  const { accepts, placement, builder, scans, confirm, error } =
    useServerSwitches(server);
  const [renaming, setRenaming] = useState(false);
  // The rows opened by hand; Drain opens from the bar's button too.
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set());
  const toggle = (id: string) =>
    setOpen((was) => {
      const next = new Set(was);
      if (!next.delete(id)) next.add(id);
      return next;
    });
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
  const canDrain = server.status !== "joining";
  const openDrain = () => {
    setOpen((was) => new Set(was).add("drain"));
    requestAnimationFrame(() =>
      document.getElementById("drain")?.scrollIntoView({ block: "start" }),
    );
  };

  return (
    <>
      <PageBar
        parent={
          <Link to="/console/servers" className="hover:text-fg">
            Servers
          </Link>
        }
        title={<span className="font-mono">{name}</span>}
        after={<StateText state={serverState(server.status)} />}
        actions={
          <>
            {canDrain && (
              <Act
                cli={`dock server drain ${name}`}
                disabled={!!server.drain}
                onClick={openDrain}
              >
                {server.drain ? "Draining" : "Drain"}
              </Act>
            )}
            {/* A joining server has no row to rename yet. */}
            {server.id > 0 && (
              <MoreMenu>
                {(close) => (
                  <MoreItem
                    close={close}
                    cli={`dock server rename ${name} <name>`}
                    onClick={() => setRenaming(true)}
                  >
                    Rename…
                  </MoreItem>
                )}
              </MoreMenu>
            )}
          </>
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

        <section aria-label="Usage" className="flex flex-col gap-2">
          <div className="flex flex-wrap items-start gap-x-6 gap-y-2">
            <Now usage={server.usage} server={server} />
            <span className="ml-auto">
              <RangeSwitch
                value={range}
                options={ranges}
                onChange={(r) => {
                  serverRange.set(r);
                  navigate({
                    search: { range: r },
                    replace: true,
                  });
                }}
              />
            </span>
          </div>
          <History
            name={name}
            range={range}
            usage={server.usage}
            server={server}
          />
          <LiveNote state={server.status === "online" ? live : "lost"} />
        </section>

        <div>
          <Fold
            id="placement"
            label="Placement"
            summary={placementSummary(detail)}
            open={open.has("placement")}
            onToggle={() => toggle("placement")}
          >
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
              <span>
                <span className="text-mut">Accepts</span> {accepts}
              </span>
              {placement}
              {builder}
              {scans}
            </div>
            {confirm}
            <ErrorText error={error} />
            <Placements detail={detail} />
          </Fold>
          <Fold
            id="services"
            label="Services"
            summary={servicesSummary(detail.services)}
            open={open.has("services")}
            onToggle={() => toggle("services")}
          >
            <Services services={detail.services} />
          </Fold>
          <Fold
            id="memory"
            label="Memory"
            summary={memorySummary(server)}
            open={open.has("memory")}
            onToggle={() => toggle("memory")}
          >
            {server.usage?.memory_parts?.length ? (
              <MemoryBreakdown usage={server.usage} />
            ) : (
              <p className="text-mut">
                No breakdown from this server yet: it needs cgroup v2 and a dock
                that reports one.
              </p>
            )}
          </Fold>
          {server.status === "online" && (
            <Fold
              id="disk"
              label="Disk"
              summary={<DiskSummary server={name} />}
              open={open.has("disk")}
              onToggle={() => toggle("disk")}
            >
              <DiskBreakdown server={name} />
            </Fold>
          )}
          <Fold
            id="network"
            label="Network"
            summary={networkSummary(server)}
            open={open.has("network")}
            onToggle={() => toggle("network")}
          >
            <LinkSection server={server} />
            {(server.tunnel || server.tunnel_port > 0) && (
              <TunnelSection server={server} />
            )}
          </Fold>
          {canDrain && (
            <Fold
              id="drain"
              label="Drain"
              summary={
                server.drain
                  ? "draining now"
                  : "move everything off this server without downtime"
              }
              open={open.has("drain")}
              onToggle={() => toggle("drain")}
            >
              <DrainSection detail={detail} />
            </Fold>
          )}
        </div>
      </Page>
    </>
  );
}

/** Placement's few words: "production, non-production and builds · 5 environments, 1 pinned". */
function placementSummary({ server: s, placements }: ServerDetail) {
  const kinds = [
    s.accepts_production && "production",
    s.accepts_non_production && "non-production",
  ].filter(Boolean);
  const takes = s.disabled_for_placement
    ? "takes nothing new"
    : kinds.length
      ? `takes ${kinds.join(" and ")}${s.builder ? ", builds images" : ""}`
      : "takes no environment kinds";
  const envs = placements.reduce(
    (n, p) => n + (p.environments?.length ?? 0),
    0,
  );
  const pinned = placements.filter((p) => p.pinned).length;
  return `${takes} · ${plural(envs, "environment")} placed${pinned ? `, ${pinned} pinned` : ""}`;
}

/** Services' few words: "11: 7 running, 1 stopped, 3 not deployed". */
function servicesSummary(services: ServerService[]) {
  if (services.length === 0) return "none run here";
  const counts = new Map<string, number>();
  for (const s of services) {
    const label = containerState(s).label;
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return `${services.length}: ${[...counts].map(([l, n]) => `${n} ${l}`).join(", ")}`;
}

/** Memory's few words: used of total, and what is reserved for services. */
function memorySummary(s: Server) {
  const u = s.usage;
  const total = u?.memory_total_bytes || s.memory_bytes;
  const parts = [
    u && total ? `${size(u.memory_used_bytes)} of ${size(total)} used` : "",
    s.memory_bytes
      ? `${size(s.reserved_memory_bytes)} reserved for services`
      : "",
  ].filter(Boolean);
  return parts.join(" · ") || "no sample yet";
}

/** Network's few words: how traffic reaches it, its base domain, a tunnel. */
function networkSummary(s: Server) {
  const ingress =
    s.status === "joining"
      ? "reported when it connects"
      : s.ingress === "cloudflare_tunnel"
        ? "Cloudflare tunnel"
        : `direct${s.public_ips?.length ? `, DNS at ${s.public_ips.join(", ")}` : ""}`;
  return [
    ingress,
    s.base_domain && `hostnames under ${s.base_domain}`,
    s.tunnel
      ? "tunnel set up"
      : s.ingress !== "cloudflare_tunnel" && "no tunnel",
  ]
    .filter(Boolean)
    .join(" · ");
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
 * after usage events patch them): the Services row, opened.
 */
function Services({ services: all }: { services: ServerService[] }) {
  const services = useMemo(
    () => all.toSorted((a, b) => memoryOf(b) - memoryOf(a)),
    [all],
  );
  if (services.length === 0) {
    return <p className="text-mut">No services run here.</p>;
  }
  return (
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
