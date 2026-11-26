# 0102: Nodes over any network

Status: accepted (Saile, 2026-11-26). Amends
[0014](0014-node-protocol.md), whose "Node port exposure: Tailscale only"
left public "possible later".

## Context

Since #500 a new server installs with one pasted command. On 2026-11-26
the first try installed everything and then failed to join: the token
named the control plane's Tailscale address, and the new server wasn't
on the tailnet. Now that Dock is open source, most people adding a
server won't have Tailscale, and the guide and the Add server screen
said nodes must reach the control plane "over Tailscale or the LAN".

The link never depended on Tailscale for its safety: it is TLS 1.3 with
an Ed25519 key on each side, each pinning the other's fingerprint. A
client without a known key fails inside the handshake, and a join token
pins the control plane's key before its secret is sent. Tailscale only
kept the port off the internet.

## Decision

| Topic | Decision |
|---|---|
| Listen | `[node] listen` may be every interface (`":7443"`). A new control plane's install.sh writes that unless `NODE_LISTEN` says otherwise (`NODE_LISTEN=` turns nodes off). A specific address works as before |
| Addresses | Join tokens carry an ordered list (they always could). `[node] addrs` sets it; empty means `listen` when it names an address, else this server's Tailscale address (read from its interfaces when needed, so a Tailscale that starts later counts) and then `public_ip`, on listen's port |
| Dialing | A node tries the addresses in order: 3 s for each but the last, 10 s for the last. A server off the tailnet falls through to the public address, and one on it uses Tailscale |
| Keeping current | Every hello sends the control plane's current list (an optional field, no protocol bump). The node saves it in `node.toml`, plus the address it connected on if that isn't in it, so a path that works is never dropped. A node that joins the tailnet later moves to it on its next reconnect; nothing probes for it meanwhile. `[node] control_plane` on the node still overrides everything |
| Shown | The hub classifies each connection's source address: 100.64.0.0/10 and `fd7a:115c:a1e0::/48` are Tailscale, private, loopback and link-local are LAN, anything else the internet. `Server.link_via`, "online, connected 3h ago via Tailscale" on the server page, `online (via tailscale)` in `dock server list` |
| Before installing | install.sh downloads `dock` first and runs `dock node join --check`, which dials the token's addresses over TCP and leaves the token unused. A server that reaches none fails in seconds, naming the addresses and what to open, before anything is installed |
| Public port hardening | Connections in their handshake are capped at 128, and at 8 per client (an IPv4 address, or an IPv6 /48 as `httpkit.ClientKey`); past a cap a connection is closed at once and a node retries with backoff. Join attempts count IPv6 clients by /48 too. A rejected connection that never offered a dock protocol in TLS (a port scanner) logs at debug, not warn |

## Consequences

- A server joins from anywhere that reaches the control plane's public
  address, with or without Tailscale; Tailscale stays the preferred
  path when both work.
- A new control plane's node port is open to the internet. What it
  exposes before authentication is Go's TLS 1.3 handshake, bounded by
  the caps above and the 10 s handshake timeout. An install that wants it
  private sets `NODE_LISTEN` to its Tailscale address, or blocks 7443 in
  its firewall.
- A cloud firewall in front of the control plane (Hetzner's, say) must
  allow 7443 for nodes off the tailnet; Dock doesn't manage it.
- Existing installs keep their `[node] listen`. Switching one to
  `":7443"` makes every connected node learn the new list on its next
  hello.
