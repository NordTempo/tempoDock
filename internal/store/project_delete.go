package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// rowDelete is one statement of deleting a project's or an environment's
// rows. where selects them through the sets below, written {envs},
// {services}, {releases}, {sites} and {moves}: the deleted environments'
// IDs and what hangs off them. project adds the rows tied to the project
// itself (?1 is its ID) and runs only when the whole project goes; a
// table with no where is the project's alone.
type rowDelete struct{ table, where, project string }

// The sets a rowDelete's where uses, over {envs}.
var deleteSets = []string{
	"{services}", `(SELECT id FROM services WHERE environment_id IN {envs})`,
	"{releases}", `(SELECT id FROM releases WHERE service_id IN {services})`,
	"{sites}", `(SELECT id FROM sites WHERE environment_id IN {envs})`,
	"{moves}", `(SELECT id FROM environment_moves WHERE environment_id IN {envs})`,
}

// projectDeletes deletes every row of a project (ADR 0078) or of some of
// its environments (ADR 0103), children before parents, so foreign keys
// hold after every statement. The audit log is not in it: its events
// keep the project's and environment's IDs and name their target in
// words. Neither is service_stats, keyed by service ID without an index
// that leads with it: its rows age out with the stats' retention, and no
// new service gets the ID (migration 0103).
//
// A new table holding a project's, environment's, service's, release's
// or site's ID gets a statement here; TestProjectDeletesCoverForeignKeys
// fails for one with a foreign key that is missing. Tables whose foreign
// keys cascade (error issues, uptime checks, log drains, ...) are listed
// anyway, so the list reads as the whole set.
var projectDeletes = []rowDelete{
	{"alerts", `service_id IN {services}`, `project_id = +?1`},
	{"access_requests", `environment_id IN {envs}`, `project_id = +?1`},
	// Tokens limited to the project or environment: without it they could
	// do nothing, and dropping the limit would widen them.
	{"tokens", `environment_id IN {envs}`, `project_id = +?1`},
	// Grants on the project or its environments (ADR 0080): what they
	// gave there is gone with it.
	{"grants", `environment_id IN {envs}`, `project_id = +?1`},
	{"data_policy_rules", `environment_id IN {envs}`, `project_id = +?1`},
	{"preview_data", `preview_id IN (SELECT id FROM previews WHERE environment_id IN {envs})`, `preview_id IN (SELECT id FROM previews WHERE project_id = +?1)`},
	{"previews", `environment_id IN {envs}`, `project_id = +?1`},
	{"service_previews", `service_id IN {services}`, ``},
	{"environment_previews", `environment_id IN {envs}`, ``},
	{"project_placements", ``, `project_id = +?1`},
	{"error_issues", `environment_id IN {envs}`, `project_id = +?1`},
	{"vuln_ignores", `service_id IN {services}`, `project_id = +?1`},
	{"diagnoses", `service_id IN {services} OR release_id IN {releases}`, ``},
	{"release_events", `release_id IN {releases}`, ``},
	{"service_runs", `service_id IN {services}`, ``},
	{"service_jobs", `service_id IN {services}`, ``},
	{"server_drain_items", `environment_id IN {envs} OR move_id IN {moves}`, ``},
	{"server_forwards", `service_id IN {services}`, ``},
	{"environment_move_services", `service_id IN {services} OR move_id IN {moves}`, ``},
	{"environment_moves", `environment_id IN {envs}`, ``},
	{"backup_schedules", `service_id IN {services}`, ``},
	{"pitr", `service_id IN {services}`, ``},
	{"backup_checks", `service_id IN {services}`, ``},
	{"backups", `service_id IN {services}`, ``},
	{"volume_snapshot_restores", `service_id IN {services} OR to_service_id IN {services}`, ``},
	{"volume_snapshots", `service_id IN {services}`, ``},
	{"volumes", `service_id IN {services}`, ``},
	{"changesets", `service_id IN {services}`, ``},
	{"data_access", `service_id IN {services}`, ``},
	{"service_access", `service_id IN {services}`, ``},
	{"saved_queries", `service_id IN {services}`, ``},
	{"sensitive_columns", `service_id IN {services}`, ``},
	{"sensitive_keys", `service_id IN {services}`, ``},
	{"staged_domains", `service_id IN {services}`, ``},
	{"domains", `service_id IN {services}`, ``},
	{"git_connections", `service_id IN {services}`, ``},
	{"compose_sources", `environment_id IN {envs}`, ``},
	{"ensure_running", `service_id IN {services}`, ``},
	{"uptime_checks", `service_id IN {services}`, ``},
	{"status_sources", `service_id IN {services}`, ``},
	{"metric_dashboards", `service_id IN {services}`, ``},
	{"traffic_stats", `service_id IN {services}`, ``},
	{"traffic_paths", `service_id IN {services}`, ``},
	{"variables", `environment_id IN {envs}`, ``},
	{"site_funnels", `site_id IN {sites}`, ``},
	{"site_goals", `site_id IN {sites}`, ``},
	{"site_markers", `site_id IN {sites}`, ``},
	{"sites", `environment_id IN {envs}`, ``},
	{"releases", `service_id IN {services}`, ``},
	{"log_drains", `environment_id IN {envs}`, ``},
	{"services", `environment_id IN {envs}`, ``},
	{"environments", `id IN {envs}`, `project_id = +?1`},
	{"projects", ``, `id = +?1`},
}

// deleteStatements are projectDeletes as statements over envs, the
// subquery of the environments that go (its parameter is ?1); whole adds
// the project's own rows.
func deleteStatements(envs string, whole bool) (unlink string, stmts []string) {
	r := strings.NewReplacer(deleteSets...)
	expand := func(where string) string {
		// Sets refer to sets: replace until none is left.
		for strings.Contains(where, "{") {
			where = strings.ReplaceAll(r.Replace(where), "{envs}", envs)
		}
		return where
	}
	for _, d := range projectDeletes {
		where := d.where
		if whole && d.project != "" {
			where = strings.TrimPrefix(where+" OR "+d.project, " OR ")
		}
		if where == "" {
			continue
		}
		stmts = append(stmts, "DELETE FROM "+d.table+" WHERE "+expand(where))
	}
	return `UPDATE backups SET restored_to = NULL WHERE restored_to IN ` + expand("{services}"), stmts
}

// DeleteProject deletes a project and every row that belongs to it
// (projectDeletes) in one transaction, after check, which runs in it
// first and stops the delete with an error. The statements run
// unprepared, outside the statement cache: a delete is rare, and caching
// its fifty statements would keep them for the daemon's life. A backup
// restored from one of its services into another project's keeps its
// row, unlinked.
func (s *Store) DeleteProject(ctx context.Context, projectID int64, check func(q *Queries) error) error {
	unlink, stmts := deleteStatements(`(SELECT id FROM environments WHERE project_id = +?1)`, true)
	return s.deleteRows(ctx, unlink, stmts, projectID, check)
}

// DeleteEnvironments deletes environments of one project and every row
// that belongs to them, as DeleteProject does for a whole project; the
// project's own rows stay.
func (s *Store) DeleteEnvironments(ctx context.Context, envIDs []int64, check func(q *Queries) error) error {
	ids, err := json.Marshal(envIDs)
	if err != nil {
		return err
	}
	unlink, stmts := deleteStatements(`(SELECT value FROM json_each(?1))`, false)
	return s.deleteRows(ctx, unlink, stmts, string(ids), check)
}

// deleteRows runs check, unlink and stmts in one transaction, each with
// arg as ?1.
func (s *Store) deleteRows(ctx context.Context, unlink string, stmts []string, arg any, check func(q *Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := check(New(txStmts{tx: tx, s: s.stmts})); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, unlink, arg); err != nil {
		return fmt.Errorf("unlink restored backups: %w", err)
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, arg); err != nil {
			return fmt.Errorf("%s: %w", q[:strings.Index(q, " WHERE")], err)
		}
	}
	return tx.Commit()
}
