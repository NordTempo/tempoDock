-- A service's pre-deploy command (ADR 0045): run as a one-off job with
-- the new release's image, variables and environment network before the
-- release gets traffic, e.g. database migrations. A timeout of 0 means
-- the default (10 minutes).
ALTER TABLE services ADD COLUMN pre_deploy_command TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN pre_deploy_timeout_seconds INTEGER NOT NULL DEFAULT 0;
