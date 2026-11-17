package store

import (
	"context"
	"fmt"
)

// The subqueries projectDeletes select a project's rows with: its
// environments, services, releases, sites and environment moves. ?1 is
// the project's ID.
const (
	projEnvs     = `(SELECT id FROM environments WHERE project_id = +?1)`
	projServices = `(SELECT id FROM services WHERE environment_id IN ` + projEnvs + `)`
	projReleases = `(SELECT id FROM releases WHERE service_id IN ` + projServices + `)`
	projSites    = `(SELECT id FROM sites WHERE environment_id IN ` + projEnvs + `)`
	projMoves    = `(SELECT id FROM environment_moves WHERE environment_id IN ` + projEnvs + `)`
)

// projectDeletes deletes every row of a project (ADR 0078), children
// before parents, so foreign keys hold after every statement. The audit
// log is not in it: its events keep the project's ID and name their
// target in words. Neither is service_stats, keyed by service ID without
// an index that leads with it: its rows age out with the stats'
// retention, and no new service gets the ID (migration 0103).
//
// A new table holding a project's, environment's, service's, release's
// or site's ID gets a statement here; TestProjectDeletesCoverForeignKeys
// fails for one with a foreign key that is missing. Tables whose foreign
// keys cascade (error issues, uptime checks, log drains, ...) are listed
// anyway, so the list reads as the whole set.
var projectDeletes = []struct{ table, where string }{
	{"alerts", `project_id = +?1 OR service_id IN ` + projServices},
	{"access_requests", `project_id = +?1 OR environment_id IN ` + projEnvs},
	// Tokens limited to the project: without it they could do nothing,
	// and dropping the limit would widen them to the whole org.
	{"tokens", `project_id = +?1 OR environment_id IN ` + projEnvs},
	// Grants on the project or its environments (ADR 0080): what they
	// gave there is gone with it.
	{"grants", `project_id = +?1 OR environment_id IN ` + projEnvs},
	{"data_policy_rules", `project_id = +?1 OR environment_id IN ` + projEnvs},
	{"preview_data", `preview_id IN (SELECT id FROM previews WHERE project_id = +?1)`},
	{"previews", `project_id = +?1`},
	{"service_previews", `service_id IN ` + projServices},
	{"environment_previews", `environment_id IN ` + projEnvs},
	{"project_placements", `project_id = +?1`},
	{"error_issues", `project_id = +?1`},
	{"vuln_ignores", `project_id = +?1 OR service_id IN ` + projServices},
	{"diagnoses", `service_id IN ` + projServices + ` OR release_id IN ` + projReleases},
	{"release_events", `release_id IN ` + projReleases},
	{"service_runs", `service_id IN ` + projServices},
	{"service_jobs", `service_id IN ` + projServices},
	{"server_drain_items", `environment_id IN ` + projEnvs + ` OR move_id IN ` + projMoves},
	{"server_forwards", `service_id IN ` + projServices},
	{"environment_move_services", `service_id IN ` + projServices + ` OR move_id IN ` + projMoves},
	{"environment_moves", `environment_id IN ` + projEnvs},
	{"backup_schedules", `service_id IN ` + projServices},
	{"backups", `service_id IN ` + projServices},
	{"volume_snapshot_restores", `service_id IN ` + projServices + ` OR to_service_id IN ` + projServices},
	{"volume_snapshots", `service_id IN ` + projServices},
	{"volumes", `service_id IN ` + projServices},
	{"changesets", `service_id IN ` + projServices},
	{"data_access", `service_id IN ` + projServices},
	{"service_access", `service_id IN ` + projServices},
	{"saved_queries", `service_id IN ` + projServices},
	{"sensitive_columns", `service_id IN ` + projServices},
	{"sensitive_keys", `service_id IN ` + projServices},
	{"staged_domains", `service_id IN ` + projServices},
	{"domains", `service_id IN ` + projServices},
	{"git_connections", `service_id IN ` + projServices},
	{"ensure_running", `service_id IN ` + projServices},
	{"uptime_checks", `service_id IN ` + projServices},
	{"status_sources", `service_id IN ` + projServices},
	{"metric_dashboards", `service_id IN ` + projServices},
	{"traffic_stats", `service_id IN ` + projServices},
	{"traffic_paths", `service_id IN ` + projServices},
	{"variables", `environment_id IN ` + projEnvs},
	{"site_hostnames", `site_id IN ` + projSites + ` OR service_id IN ` + projServices},
	{"site_funnels", `site_id IN ` + projSites},
	{"site_goals", `site_id IN ` + projSites},
	{"site_markers", `site_id IN ` + projSites},
	{"sites", `environment_id IN ` + projEnvs},
	{"releases", `service_id IN ` + projServices},
	{"log_drains", `environment_id IN ` + projEnvs},
	{"services", `environment_id IN ` + projEnvs},
	{"environments", `project_id = +?1`},
	{"projects", `id = +?1`},
}

// DeleteProject deletes a project and every row that belongs to it
// (projectDeletes) in one transaction, after check, which runs in it
// first and stops the delete with an error. The statements run
// unprepared, outside the statement cache: a delete is rare, and caching
// its fifty statements would keep them for the daemon's life. A backup
// restored from one of its services into another project's keeps its
// row, unlinked.
func (s *Store) DeleteProject(ctx context.Context, projectID int64, check func(q *Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := check(New(txStmts{tx: tx, s: s.stmts})); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backups SET restored_to = NULL WHERE restored_to IN `+projServices, projectID); err != nil {
		return fmt.Errorf("unlink restored backups: %w", err)
	}
	for _, d := range projectDeletes {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+d.table+" WHERE "+d.where, projectID); err != nil {
			return fmt.Errorf("delete the project's %s: %w", d.table, err)
		}
	}
	return tx.Commit()
}
