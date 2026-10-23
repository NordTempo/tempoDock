import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import {
  useAuditLog,
  useProjectTree,
  useServers,
  useUsers,
} from "../../data/queries";
import type { Server } from "../../data/types";
import { pct, plural, since, size } from "../../ui/format";
import {
  Fact,
  Facts,
  Glyph,
  Page,
  Panel,
  SectionHead,
  StateText,
  TitleRow,
} from "../../ui/kit";
import {
  serverState,
  type Tone,
  toneText,
  versionBehind,
} from "../../ui/status";
import { useOpenAlerts } from "../alerts/data";
import { AuditTable } from "../audit/AuditTable";
import { useBackupSettings } from "../backups/settings";
import { useRegistry } from "../registry/data";
import { useControlPlaneVersion, Version } from "../servers/parts";

// The console's landing page: what needs an admin now, then the numbers,
// servers at a glance, open alerts and recent activity. It reads what the
// other console pages read (each cached and kept live by events), so
// nothing here polls (ADR 0025).

/** Disk use worth a line on its own; retention prunes at 80% by default. */
const DISK_WARN = 85;

export function ConsoleOverview() {
  const servers = useServers();
  const version = useControlPlaneVersion();
  const controlPlane = servers.find((s) => s.local);
  const online = servers.filter((s) => s.status === "online").length;
  return (
    <Page width="max-w-[1240px]">
      <TitleRow
        title="Overview"
        context={[
          version && `dock ${version}`,
          plural(servers.length, "server"),
          controlPlane && `control plane on ${controlPlane.name}`,
        ]
          .filter(Boolean)
          .join(" · ")}
      />
      <Attention />
      <Numbers online={online} />
      <section aria-labelledby="servers">
        <SectionHead
          id="servers"
          right={<Link to="/console/servers">All servers</Link>}
        >
          Servers
        </SectionHead>
        <ServerTable servers={servers} />
      </section>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(380px,1fr))] gap-x-8 gap-y-7">
        <OpenAlerts />
        <section aria-labelledby="activity">
          <SectionHead
            id="activity"
            right={<Link to="/console/audit">Audit</Link>}
          >
            Recent activity
          </SectionHead>
          <AuditTable events={useAuditLog().slice(0, 6)} compact />
        </section>
      </div>
    </Page>
  );
}

type Item = { key: string; tone: Tone; text: ReactNode; to: ReactNode };

/** Everything that needs an admin, worst first; one line when nothing does. */
function Attention() {
  const servers = useServers();
  const version = useControlPlaneVersion();
  const alerts = useOpenAlerts();
  const backups = useBackupSettings();
  const registry = useRegistry();
  const items: Item[] = [];
  const serverLink = (s: Server, label: string) => (
    <Link to="/console/servers/$server" params={{ server: s.name }}>
      {label}
    </Link>
  );
  for (const a of alerts) {
    items.push({
      key: `alert-${a.id}`,
      tone: a.severity === "critical" ? "fail" : "warn",
      text: a.title,
      to: <Link to="/console/alerts">Alerts</Link>,
    });
  }
  for (const s of servers) {
    if (s.status === "offline" || s.status === "incompatible") {
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
  const latest = backups.control_plane.latest;
  if (latest?.status === "failed") {
    items.push({
      key: "backup-failed",
      tone: "fail",
      text: `The control plane backup failed ${since(latest.started_at)} ago`,
      to: <Link to="/console/backups">Backups</Link>,
    });
  }
  if (!backups.off_box) {
    items.push({
      key: "backup-local",
      tone: "warn",
      text: "Backups stay on each server; nothing is off-box",
      to: <Link to="/console/backups">Set up S3</Link>,
    });
  }
  if (registry.last_prune_error) {
    items.push({
      key: "registry",
      tone: "warn",
      text: `The last registry prune failed: ${registry.last_prune_error}`,
      to: <Link to="/console/registry">Registry</Link>,
    });
  }
  items.sort((a, b) => Number(b.tone === "fail") - Number(a.tone === "fail"));
  if (items.length === 0) {
    return (
      <p className="text-ok">
        <Glyph char="●" /> Nothing needs attention.
      </p>
    );
  }
  return (
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
          <span className="text-xs">{item.to}</span>
        </li>
      ))}
    </ul>
  );
}

/** The console's numbers: servers, projects, people, registry, backups. */
function Numbers({ online }: { online: number }) {
  const servers = useServers();
  const tree = useProjectTree();
  const users = useUsers();
  const registry = useRegistry();
  const backups = useBackupSettings();
  const alerts = useOpenAlerts();
  const envs = tree.flatMap((p) => p.environments);
  const services = envs.reduce((n, e) => n + e.services.length, 0);
  const admins = users.filter((u) => u.role === "admin" && !u.disabled_at);
  const disabled = users.filter((u) => u.disabled_at).length;
  const latest = backups.control_plane.latest;
  const critical = alerts.filter((a) => a.severity === "critical").length;
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
      <Fact
        label="Projects"
        note={`${plural(services, "service")} · ${plural(envs.length, "env")}`}
      >
        {tree.length}
      </Fact>
      <Fact
        label="Users"
        note={`${plural(admins.length, "admin")}${disabled ? ` · ${disabled} disabled` : ""}`}
      >
        <Link to="/console/users" className="text-inherit">
          {users.length - disabled}
        </Link>
      </Fact>
      <Fact
        label="Registry"
        note={
          registry.enabled
            ? `${plural(
                (registry.services ?? []).reduce((n, s) => n + s.kept, 0),
                "image",
              )} kept`
            : "images stay where built"
        }
      >
        <Link to="/console/registry" className="text-inherit">
          {registry.enabled
            ? registry.stored_bytes
              ? size(registry.stored_bytes)
              : "on"
            : "off"}
        </Link>
      </Fact>
      <Fact
        label="Control plane backup"
        note={
          latest
            ? `${latest.size_bytes ? size(latest.size_bytes) : latest.status} · ${latest.off_box ? "off-box" : "local only"}`
            : "none yet"
        }
      >
        <Link
          to="/console/backups"
          className={
            latest?.status === "failed"
              ? "text-fail"
              : latest
                ? "text-inherit"
                : "text-warn"
          }
        >
          {latest ? `${since(latest.started_at)} ago` : "never"}
        </Link>
      </Fact>
      <Fact
        label="Open alerts"
        note={critical ? `${critical} critical` : "none critical"}
      >
        <Link
          to="/console/alerts"
          className={
            critical
              ? "text-fail"
              : alerts.length
                ? "text-warn"
                : "text-inherit"
          }
        >
          {alerts.length}
        </Link>
      </Fact>
    </Facts>
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

function OpenAlerts() {
  const alerts = useOpenAlerts().slice(0, 6);
  return (
    <section aria-labelledby="alerts">
      <SectionHead id="alerts" right={<Link to="/console/alerts">Alerts</Link>}>
        Open alerts
      </SectionHead>
      {alerts.length === 0 ? (
        <p className="py-2 text-mut">None open.</p>
      ) : (
        <table>
          <tbody>
            {alerts.map((a) => (
              <tr key={a.id}>
                <td
                  className={`py-1.5 whitespace-nowrap ${toneText[a.severity === "critical" ? "fail" : "warn"]}`}
                >
                  {a.severity}
                </td>
                <td className="w-full py-1.5">{a.title}</td>
                <td className="py-1.5 whitespace-nowrap text-mut">
                  {since(a.opened_at)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
