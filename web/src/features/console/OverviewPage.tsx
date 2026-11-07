import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useServers } from "../../data/queries";
import type { Server } from "../../data/types";
import { pct, plural, since, size } from "../../ui/format";
import {
  Fact,
  Facts,
  Glyph,
  Page,
  PageBar,
  Panel,
  SectionHead,
  StateText,
} from "../../ui/kit";
import {
  serverState,
  type Tone,
  toneText,
  versionBehind,
} from "../../ui/status";
import { type Alert, useOpenAlerts } from "../alerts/data";
import { AlertLink, alertText } from "../alerts/source";
import { useBackupSettings } from "../backups/settings";
import { cloudflareQuery } from "../cloudflare/data";
import { githubQuery } from "../github/data";
import { useConsoleOrgs } from "../orgs/data";
import { useRegistry } from "../registry/data";
import { useControlPlaneVersion, Version } from "../servers/parts";
import { useStorage } from "../storage/data";
import { useUpdateNote } from "../update/data";

// The console's landing page answers "is the platform healthy?" (ADR
// 0062, mock O): the platform's open alerts and what else needs the
// operator, the servers and their use, the state of each connection as
// Dock last used it, and the organizations using the most. Org alerts and
// errors are their admins', not here. It reads what the other console
// pages read (each cached and kept live by events), so nothing here polls
// (ADR 0025).

/** Disk use worth a line on its own; retention prunes at 80% by default. */
const DISK_WARN = 85;

/** A section's link to its page: quiet, with an arrow, underlined on hover. */
const more =
  "text-xs text-mut hover:text-fg hover:underline underline-offset-3";

export function ConsoleOverview() {
  const servers = useServers();
  const alerts = useOpenAlerts("platform");
  return (
    <>
      <PageBar title="Overview" />
      <Page>
        <Health alerts={alerts} />
        <Numbers servers={servers} />
        <section aria-labelledby="servers">
          <SectionHead
            id="servers"
            right={
              <Link to="/console/servers" className={more}>
                All servers →
              </Link>
            }
          >
            Servers
          </SectionHead>
          <ServerTable servers={servers} />
        </section>
        <div className="grid grid-cols-[repeat(auto-fit,minmax(380px,1fr))] gap-x-8 gap-y-7 *:min-w-0">
          <Connections alerts={alerts} />
          <TopOrgs />
        </div>
      </Page>
    </>
  );
}

type Item = { key: string; tone: Tone; text: ReactNode; to: ReactNode };

/**
 * The platform's open alerts, worst first, as tinted strips, then what
 * else needs the operator that isn't an alert (an update out, a server
 * behind or offline, backups kept on the servers); one line when
 * nothing does.
 */
function Health({ alerts }: { alerts: Alert[] }) {
  const servers = useServers();
  const version = useControlPlaneVersion();
  const backups = useBackupSettings();
  const update = useUpdateNote();
  const items: Item[] = [];
  const serverLink = (s: Server, label: string) => (
    <Link to="/console/servers/$server" params={{ server: s.name }}>
      {label}
    </Link>
  );
  if (update?.available && !update.running) {
    items.push({
      key: "update",
      tone: "warn",
      text: (
        <>
          dock <span className="font-mono">{update.latest}</span> is out; this
          Dock runs <span className="font-mono">{update.version}</span>
        </>
      ),
      to: <Link to="/console/updates">Updates</Link>,
    });
  }
  // An offline server is an alert after a few minutes; until then, here.
  const alerted = new Set(alerts.map((a) => a.server));
  for (const s of servers) {
    if (
      (s.status === "offline" || s.status === "incompatible") &&
      !alerted.has(s.name)
    ) {
      items.push({
        key: `server-${s.id}`,
        tone: s.status === "offline" ? "fail" : "warn",
        text: `${s.name} is ${serverState(s.status).label}`,
        to: serverLink(s, "Server"),
      });
    } else if (s.version && versionBehind(s.version, version)) {
      items.push({
        key: `server-${s.id}`,
        tone: "warn",
        text: `${s.name} runs dock ${s.version}, behind ${version}`,
        to: serverLink(s, "Server"),
      });
    }
  }
  if (!backups.off_box) {
    items.push({
      key: "backup-local",
      tone: "warn",
      text: "Backups stay on each server; nothing is off-box",
      to: <Link to="/console/backups">Move them to a storage bucket</Link>,
    });
  }
  items.sort((a, b) => Number(b.tone === "fail") - Number(a.tone === "fail"));
  if (alerts.length === 0 && items.length === 0) {
    return (
      <p className="text-ok">
        <Glyph char="●" /> The platform is healthy: no open alerts, nothing
        needs attention.
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      {alerts.map((a) => (
        <AlertStrip key={a.id} alert={a} />
      ))}
      {items.length > 0 && (
        <ul aria-label="Needs attention" className="flex flex-col">
          {items.map((item) => (
            <li
              key={item.key}
              className="flex items-baseline gap-3 border-b border-soft py-2 last:border-0"
            >
              <span className={toneText[item.tone]}>
                <Glyph char={item.tone === "fail" ? "✕" : "▲"} />
              </span>
              <span className="flex-1">{item.text}</span>
              <span className="text-xs text-mut [&_a:hover]:text-fg [&_a:hover]:underline [&_a]:underline-offset-3">
                {item.to} <span aria-hidden>→</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** One open platform alert: a ruled, tinted strip with the way to it. */
function AlertStrip({ alert: a }: { alert: Alert }) {
  const critical = a.severity === "critical";
  const { text } = alertText(a);
  return (
    <div
      className={`flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-r-[6px] border-l-2 px-3 py-2 ${critical ? "border-fail bg-fail-bg" : "border-warn bg-warn-bg"}`}
    >
      <span className={critical ? "text-fail" : "text-warn"}>{a.severity}</span>
      <span className="min-w-0 flex-1">
        {a.title}
        {text && <span className="block text-xs text-sec">{text}</span>}
      </span>
      <span className="text-xs text-mut">{since(a.opened_at)}</span>
      <AlertLink alert={a} className="btn sm no-underline" />
    </div>
  );
}

/** The platform's totals: servers, CPU, memory, disk, orgs and version. */
function Numbers({ servers }: { servers: Server[] }) {
  const orgs = useConsoleOrgs();
  const update = useUpdateNote();
  const online = servers.filter((s) => s.status === "online").length;
  const t = servers.reduce(
    (t, s) => {
      const u = s.usage;
      if (!u) return t;
      return {
        cpus: t.cpus + u.cpus,
        busy: t.busy + (u.cpus * u.cpu_percent) / 100,
        memUsed: t.memUsed + u.memory_used_bytes,
        memTotal: t.memTotal + u.memory_total_bytes,
        diskUsed: t.diskUsed + u.disk_used_bytes,
        diskTotal: t.diskTotal + u.disk_total_bytes,
      };
    },
    { cpus: 0, busy: 0, memUsed: 0, memTotal: 0, diskUsed: 0, diskTotal: 0 },
  );
  const services = orgs.reduce((n, o) => n + o.services, 0);
  return (
    <Facts>
      <Fact
        label="Servers"
        note={plural(servers.filter((s) => s.builder).length, "builder")}
      >
        {online}
        <span className="text-xs font-normal text-mut">
          {" "}
          / {servers.length} online
        </span>
      </Fact>
      <Fact label="CPU" note={`of ${plural(t.cpus, "CPU")}`}>
        {t.busy.toFixed(1)}
      </Fact>
      <Fact label="Memory" note={`of ${size(t.memTotal)}`}>
        {size(t.memUsed)}
      </Fact>
      <Fact label="Disk" note={`of ${size(t.diskTotal)}`}>
        {size(t.diskUsed)}
      </Fact>
      <Fact label="Organizations" note={plural(services, "service")}>
        <Link to="/console/orgs" className="text-inherit">
          {orgs.length}
        </Link>
      </Fact>
      <Fact
        label="Version"
        note={
          update?.running ? (
            <span className="text-prog">
              updating to {update.running.version}
            </span>
          ) : update?.available ? (
            <span className="text-warn">{update.latest} available</span>
          ) : update?.latest ? (
            "up to date"
          ) : undefined
        }
      >
        <Link to="/console/updates" className="font-mono text-inherit">
          {update?.version ?? "·"}
        </Link>
      </Fact>
    </Facts>
  );
}

type Conn = {
  key: string;
  tone: Tone;
  name: ReactNode;
  detail: ReactNode;
  to: ReactNode;
};

/**
 * Each of the platform's connections as Dock last used it: failing while
 * its connection_failed alert is open (or Cloudflare refused its token),
 * fine otherwise. Nothing is checked to draw this: a connection nobody
 * used lately shows what was last seen. GitHub and Cloudflare load
 * without holding up the page.
 */
function Connections({ alerts }: { alerts: Alert[] }) {
  const cloudflare = useQuery(cloudflareQuery()).data ?? [];
  const github = useQuery(githubQuery()).data;
  const storage = useStorage();
  const registry = useRegistry();
  const backups = useBackupSettings();
  const failing = new Map(
    alerts
      .filter((a) => a.kind === "connection_failed" && a.target?.connection)
      .map((a) => [a.target?.connection ?? "", a]),
  );
  const alertFor = (match: (conn: string) => boolean) =>
    [...failing.entries()].find(([conn]) => match(conn))?.[1];
  const rows: Conn[] = [];
  for (const cf of cloudflare) {
    const alert = failing.get(`cloudflare:${cf.name}`);
    rows.push({
      key: `cf-${cf.name}`,
      tone: cf.status !== "ok" || alert ? "warn" : "ok",
      name: `Cloudflare ${cf.name}`,
      detail:
        cf.status_reason ||
        alert?.message ||
        `${plural(cf.zones?.length ?? 0, "zone")}, used by ${(cf.used_by ?? []).length || "nothing"}`,
      to: <Link to="/console/cloudflare">Cloudflare</Link>,
    });
  }
  if (github?.app) {
    const alert = failing.get("github:app");
    rows.push({
      key: "github",
      tone: alert ? "warn" : "ok",
      name: "GitHub App",
      detail:
        alert?.message ??
        `${github.app.slug} on ${github.app.owner}, ${plural(github.installations?.length ?? 0, "installation")}`,
      to: <Link to="/console/github">GitHub</Link>,
    });
  }
  for (const s of storage) {
    const alert = alertFor(
      (conn) => conn.startsWith("storage:") && conn.includes(s.bucket),
    );
    rows.push({
      key: `storage-${s.name}`,
      tone: alert ? "warn" : "ok",
      name: `Storage ${s.name}`,
      detail:
        alert?.message ??
        `${s.bucket}, used by ${(s.used_by ?? []).join(", ") || "nothing"}`,
      to: <Link to="/console/storage">Storage</Link>,
    });
  }
  if (registry.enabled) {
    const alert = alertFor((conn) => conn.startsWith("registry:"));
    rows.push({
      key: "registry",
      tone: alert || registry.last_prune_error ? "warn" : "ok",
      name: "Registry",
      detail:
        alert?.message ??
        registry.last_prune_error ??
        (registry.stored_bytes ? size(registry.stored_bytes) : "on"),
      to: <Link to="/console/registry">Registry</Link>,
    });
  }
  const latest = backups.control_plane.latest;
  rows.push({
    key: "cp-backup",
    tone: latest?.status === "failed" ? "fail" : latest ? "ok" : "warn",
    name: "Control plane backup",
    detail: latest
      ? `${latest.status === "failed" ? "failed" : latest.size_bytes ? size(latest.size_bytes) : latest.status} ${since(latest.started_at)} ago${latest.off_box ? "" : ", local only"}`
      : "none yet",
    to: <Link to="/console/backups">Backups</Link>,
  });
  return (
    <section aria-labelledby="connections">
      <SectionHead id="connections">Connections</SectionHead>
      <ul className="border-t border-line">
        {rows.map((r) => (
          <li
            key={r.key}
            className="grid grid-cols-[14px_minmax(120px,max-content)_minmax(0,1fr)_auto] items-baseline gap-x-3 border-b border-soft py-2"
          >
            <span className={toneText[r.tone]}>
              <Glyph
                char={r.tone === "ok" ? "●" : r.tone === "fail" ? "✕" : "▲"}
              />
            </span>
            <span className="font-semibold">{r.name}</span>
            <span
              className={`min-w-0 break-words text-xs ${r.tone === "ok" ? "text-mut" : "text-sec"}`}
            >
              {r.detail}
            </span>
            <span className="text-xs text-mut [&_a:hover]:text-fg [&_a:hover]:underline [&_a]:underline-offset-3">
              {r.to} <span aria-hidden>→</span>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** The organizations using the most CPU, then memory. */
function TopOrgs() {
  const orgs = useConsoleOrgs().slice(0, 5);
  return (
    <section aria-labelledby="top-orgs">
      <SectionHead
        id="top-orgs"
        right={
          <Link to="/console/orgs" className={more}>
            Organizations →
          </Link>
        }
      >
        Top organizations by use
      </SectionHead>
      <div className="overflow-x-auto border-t border-line">
        <table>
          <thead>
            <tr>
              <th>Organization</th>
              <th className="text-right">CPU</th>
              <th className="text-right">Memory</th>
              <th className="text-right">Volumes</th>
              <th className="text-right">Bandwidth 30d</th>
            </tr>
          </thead>
          <tbody>
            {orgs.map((o) => (
              <tr key={o.slug}>
                <td className="py-2">
                  <Link to="/console/orgs" search={{ org: o.slug }}>
                    {o.slug}
                  </Link>{" "}
                  {o.status !== "active" && (
                    <StateText
                      state={
                        o.status === "suspended"
                          ? { tone: "mut", label: "suspended" }
                          : { tone: "warn", label: "no admin" }
                      }
                    />
                  )}
                </td>
                <td className="py-2 text-right tabular-nums">
                  {(o.usage.cpu_percent / 100).toFixed(2)}
                </td>
                <td className="py-2 text-right tabular-nums">
                  {size(o.usage.memory_bytes)}
                </td>
                <td className="py-2 text-right tabular-nums">
                  {o.usage.volume_read_at ? size(o.usage.volume_bytes) : "·"}
                </td>
                <td className="py-2 text-right tabular-nums">
                  {size(o.usage.bandwidth_bytes)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** "45%" with a thin bar; yellow from warn up. */
function Meter({ percent, warn = 101 }: { percent: number; warn?: number }) {
  const high = percent >= warn;
  return (
    <span
      className={`inline-flex items-center gap-2 ${high ? "text-warn" : ""}`}
    >
      <span className="h-1 w-14 overflow-hidden rounded-full bg-chip">
        <span
          className={`block h-full rounded-full ${high ? "bg-warn" : "bg-sec"}`}
          style={{ width: `${Math.min(100, percent)}%` }}
        />
      </span>
      {pct(percent)}
    </span>
  );
}

const dash = <span className="text-faint">·</span>;

function ServerTable({ servers }: { servers: Server[] }) {
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Server</th>
            <th>Status</th>
            <th>Runs</th>
            <th>CPU</th>
            <th>Memory</th>
            <th>Disk</th>
            <th>Version</th>
          </tr>
        </thead>
        <tbody>
          {servers.map((s) => {
            const u = s.usage;
            const runs = [
              s.local && "control plane",
              s.builder && "builds",
              s.environments ? plural(s.environments, "env") : "nothing",
            ]
              .filter(Boolean)
              .join(" · ");
            return (
              <tr key={s.id}>
                <td className="py-2 font-mono">
                  <Link
                    to="/console/servers/$server"
                    params={{ server: s.name }}
                  >
                    {s.name}
                  </Link>
                </td>
                <td className="py-2">
                  <StateText state={serverState(s.status)} />
                </td>
                <td className="py-2 text-mut">{runs}</td>
                <td className="py-2">
                  {u ? <Meter percent={u.cpu_percent} /> : dash}
                </td>
                <td className="py-2">
                  {u?.memory_total_bytes ? (
                    <Meter
                      percent={
                        (100 * u.memory_used_bytes) / u.memory_total_bytes
                      }
                    />
                  ) : (
                    dash
                  )}
                </td>
                <td className="py-2">
                  {u?.disk_total_bytes ? (
                    <Meter
                      percent={(100 * u.disk_used_bytes) / u.disk_total_bytes}
                      warn={DISK_WARN}
                    />
                  ) : (
                    dash
                  )}
                </td>
                <td className="w-full py-2">
                  <Version server={s} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </Panel>
  );
}
