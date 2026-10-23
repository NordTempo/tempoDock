import { Link } from "@tanstack/react-router";
import { useIsAdmin, useServers } from "../../data/queries";
import type { Server } from "../../data/types";
import { ago } from "../../ui/format";
import { StateText } from "../../ui/kit";
import { serverState, versionBehind } from "../../ui/status";

/** "offline, last seen 5m ago": a server's status for titles. */
export function statusTitle(s: Server) {
  const seen =
    s.status === "offline" && s.last_seen_at
      ? `, last seen ${ago(s.last_seen_at)}`
      : "";
  return `${s.name} is ${serverState(s.status).label}${seen}`;
}

/**
 * The server an environment runs on, marked when deploys can't reach it:
 * "homelab ✕ offline". Admins get a link to its page.
 */
export function EnvServer({ name }: { name: string }) {
  const server = useServers().find((s) => s.name === name);
  const admin = useIsAdmin();
  const label = admin ? (
    <Link
      to="/console/servers/$server"
      params={{ server: name }}
      className="font-mono text-inherit"
    >
      {name}
    </Link>
  ) : (
    <span className="font-mono">{name}</span>
  );
  if (!server || server.status === "online") return label;
  return (
    <span title={statusTitle(server)}>
      {label} <StateText state={serverState(server.status)} />
    </span>
  );
}

/** The control plane's dock version, which every server should match. */
export const useControlPlaneVersion = () =>
  useServers().find((s) => s.local)?.version ?? "";

/** "0.3.1", or "▲ 0.2.0 behind 0.3.1" in yellow. */
export function Version({ server }: { server: Server }) {
  const controlPlane = useControlPlaneVersion();
  if (!server.version) return <span className="text-faint">·</span>;
  if (!versionBehind(server.version, controlPlane)) {
    return <span className="font-mono text-mut">{server.version}</span>;
  }
  return (
    <span
      className="font-mono text-warn"
      title={`The control plane runs ${controlPlane}. Upgrade dock on ${server.name}.`}
    >
      ▲ {server.version}{" "}
      <span className="font-sans text-xs text-mut">behind {controlPlane}</span>
    </span>
  );
}

/** "direct" or "tunnel", with the tunnel's hostname. */
export function Ingress({ server }: { server: Server }) {
  // Reported when it first connects.
  if (server.status === "joining") return <span className="text-faint">·</span>;
  switch (server.ingress) {
    case "direct":
      return <span>direct</span>;
    case "cloudflare_tunnel":
      return (
        <span>
          tunnel
          {server.tunnel_hostname && (
            <div className="font-mono text-xs text-mut">
              {server.tunnel_hostname}
            </div>
          )}
        </span>
      );
    default:
      return <span className="text-faint">·</span>;
  }
}
