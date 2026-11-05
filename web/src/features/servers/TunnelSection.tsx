import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import type { Server } from "../../data/types";
import { ago } from "../../ui/format";
import { Act, ErrorText, Panel, SectionHead, StateText } from "../../ui/kit";
import type { State } from "../../ui/status";
import { cloudflareQuery, useSetServerTunnel } from "../cloudflare/data";

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

type Tunnel = NonNullable<Server["tunnel"]>;

const tunnelState: Record<Tunnel["status"], State> = {
  ready: { tone: "ok", glyph: "●", label: "ready" },
  pending: { tone: "prog", glyph: "◐", label: "setting up" },
  error: { tone: "fail", glyph: "✕", label: "error" },
  deleting: { tone: "mut", glyph: "○", label: "deleting" },
};

/**
 * A server's Cloudflare tunnel (ADR 0036), in tunnel mode or next to a
 * direct server's public IP: the one Dock runs, with the cloudflared
 * processes connected to it (read when the page opens), or how to have
 * Dock run it.
 */
export function TunnelSection({ server: s }: { server: Server }) {
  const t = s.tunnel;
  return (
    <section aria-labelledby="tunnel" className="flex flex-col gap-2">
      <SectionHead
        id="tunnel"
        aside={t ? `Cloudflare, connection ${t.connection}` : "Cloudflare"}
      >
        Tunnel
      </SectionHead>
      {t ? <Managed server={s} tunnel={t} /> : <Unmanaged server={s} />}
    </section>
  );
}

function Managed({ server: s, tunnel: t }: { server: Server; tunnel: Tunnel }) {
  const connectors = t.connectors ?? [];
  // Adopted from install.sh's cloudflared: that one connects too until a
  // re-run of install.sh removes it.
  const oldOne = t.adopted && connectors.length > 1;
  return (
    <>
      <dl className="props max-w-[960px] grid-cols-[140px_minmax(0,1fr)]">
        <Fact label="Tunnel">
          {t.hostname ? (
            <span className="font-mono">{t.hostname}</span>
          ) : (
            <span className="text-mut">not created yet</span>
          )}
        </Fact>
        <Fact label="State">
          <StateText state={tunnelState[t.status]} />{" "}
          <span className="text-mut">
            {t.adopted ? "adopted; Dock never deletes it" : "created by Dock"}
          </span>
          {t.reason && <div className="text-fail">{t.reason}</div>}
        </Fact>
        <Fact label="Routes to">
          <span className="font-mono">dock-cloudflared</span>
          <span className="text-mut">
            {" "}
            on {s.name}, then Caddy
            {s.ingress === "direct" &&
              "; only domains added from Cloudflare, the rest stay on the public IP"}
          </span>
        </Fact>
      </dl>
      {connectors.length > 0 && (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Connector</th>
                <th>Version</th>
                <th>Edge</th>
                <th>Since</th>
              </tr>
            </thead>
            <tbody>
              {connectors.map((c) => (
                <tr key={c.id}>
                  <td className="py-2 font-mono">{c.id.slice(0, 8)}</td>
                  <td className="py-2 font-mono">{c.version}</td>
                  <td className="py-2 font-mono">
                    {(c.edge ?? []).join(", ")}
                  </td>
                  <td className="py-2 text-mut">{ago(c.since)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      {oldOne && (
        <p className="text-warn-text">
          {connectors.length} connectors: the cloudflared service install.sh set
          up still runs next to Dock's. Run install.sh on {s.name} again to
          remove it.
        </p>
      )}
    </>
  );
}

function Unmanaged({ server: s }: { server: Server }) {
  const set = useSetServerTunnel();
  const connections = useQuery(cloudflareQuery()).data ?? [];
  const [picked, setPicked] = useState("");
  const connection = picked || connections[0]?.name || "";
  const several = connections.length > 1;
  const action = s.tunnel_hostname ? "adopt" : "create";
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span className="text-mut">
        {s.tunnel_hostname ? (
          <>
            {s.name} runs{" "}
            <span className="font-mono text-fg">{s.tunnel_hostname}</span>{" "}
            itself. Dock takes it over, routes kept, when a domain is added from
            Cloudflare, or now.
          </>
        ) : s.ingress === "direct" ? (
          <>
            {s.name} serves its domains on its public IP. Dock can run a tunnel
            next to it, for domains added from Cloudflare: it creates one when
            the first is added, or now.
          </>
        ) : (
          <>
            {s.name} runs no tunnel. Dock creates one when a domain is added
            from Cloudflare, or now.
          </>
        )}
      </span>
      {several && (
        <select
          aria-label="Cloudflare connection"
          value={connection}
          onChange={(e) => setPicked(e.target.value)}
          className="field py-[3px] font-mono"
        >
          {connections.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name}
            </option>
          ))}
        </select>
      )}
      <Act
        small
        cli={`dock server tunnel ${action} ${s.name}${several ? ` --cloudflare ${connection}` : ""}`}
        disabled={
          set.isPending || s.status !== "online" || connections.length === 0
        }
        reason={
          connections.length === 0
            ? "add a Cloudflare connection first"
            : undefined
        }
        onClick={() =>
          set.mutate({
            server: s.name,
            action,
            ...(several && { cloudflare: connection }),
          })
        }
      >
        {action === "adopt" ? "Let Dock run it" : "Create a tunnel"}
      </Act>
      <ErrorText error={set.error} />
    </div>
  );
}
