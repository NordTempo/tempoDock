-- The finished deploys of each service (not rollbacks or restarts), for
-- the overview's last_failed_deploy: the newest one per service, when
-- it failed (ListProjectReleaseHeads).
CREATE INDEX releases_deploys ON releases (service_id, number)
  WHERE source NOT IN ('rollback', 'restart') AND status IN ('active', 'superseded', 'failed');
