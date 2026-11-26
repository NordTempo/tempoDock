-- A direct server the control plane sends through its Cloudflare tunnel
-- (ADR 0036, amendment of 2026-11-26): `dock server update --tunnel`. It
-- then counts as a tunnel server, its generated hostnames get their own
-- records to the tunnel, and switching needs no new install.
ALTER TABLE servers ADD COLUMN through_tunnel BOOLEAN NOT NULL DEFAULT 0;
