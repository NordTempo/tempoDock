-- The ongoing health check's settings (ADR 0043): Docker probes a running
-- service every interval and the control plane restarts one that stays
-- unhealthy. 0 means the default (30 s interval, 5 s probe timeout, 3
-- retries, the deploy health check's timeout as start period); an interval
-- of -1 turns the ongoing check off.
ALTER TABLE services ADD COLUMN health_check_interval_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN health_check_probe_timeout_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN health_check_retries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN health_check_start_period_seconds INTEGER NOT NULL DEFAULT 0;
