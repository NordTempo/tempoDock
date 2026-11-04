-- Automatic snapshots (ADR 0047): a service's pre_deploy_snapshot
-- snapshots every stateful service in its environment before its
-- pre-deploy command runs (opt-in), and a run that mounts a service's
-- volumes for writing records the snapshot taken before it (NULL: none).
ALTER TABLE services ADD COLUMN pre_deploy_snapshot BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE service_runs ADD COLUMN snapshot_id INTEGER;
