-- Security headers (ADR 0107): Dock's Caddy adds a
-- safe set of response headers (HSTS, nosniff, a referrer policy and
-- SAMEORIGIN framing) to a service's responses that lack them. A staged
-- setting like any other; a release seals what it was deployed with, and
-- its routes follow the active release. Existing services keep serving
-- as they did (0): an app that is framed elsewhere or served over plain
-- HTTP on purpose shouldn't change under its owner. New services get it
-- on (CreateService).
ALTER TABLE services ADD COLUMN security_headers BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE releases ADD COLUMN security_headers BOOLEAN NOT NULL DEFAULT 0;
