package store

import (
	"context"
	"math"
	"testing"
)

// Existing alert identities are backfilled only when their names are unambiguous.
// Audit targets remain historical text, with an index on the stable suffix.
func TestProjectRenameMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_project_rename.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One'), (2, 'two', 'Two')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop'), (2, 1, 'shared'), (3, 2, 'shared')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1), (2, 2, 'production', 1), (3, 3, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'web', 'app:1'), (2, 2, 'web', 'app:1'), (3, 3, 'web', 'app:1')`,
		`INSERT INTO alerts (key, kind, severity, title, project, environment, service, opened_at, updated_at) VALUES ('deploy:shop/production/web', 'deploy_failed', 'warning', 'Deploy failed: shop/production/web', 'shop', 'production', 'web', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), ('deploy:shared/production/web', 'deploy_failed', 'warning', 'Deploy failed: shared/production/web', 'shared', 'production', 'web', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO audit_events (org_id, project_id, environment_id, actor, actor_kind, action, target, summary) VALUES (1, 1, 1, 'admin', 'user', 'service.create', 'shop/production/web', 'created')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	alerts, err := s.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("alerts: %+v", alerts)
	}
	for _, a := range alerts {
		switch a.Project {
		case "shop":
			if a.ProjectID == nil || *a.ProjectID != 1 || a.ServiceID == nil || *a.ServiceID != 1 || a.Key != "deploy:service:1" {
				t.Errorf("attributed alert: %+v", a)
			}
		case "shared":
			if a.ProjectID != nil || a.ServiceID != nil || a.Key != "deploy:shared/production/web" {
				t.Errorf("ambiguous alert was guessed: %+v", a)
			}
		}
	}
	if _, err := s.RenameProject(ctx, RenameProjectParams{ID: 1, Name: "storefront"}); err != nil {
		t.Fatal(err)
	}
	var target string
	if err := s.DB.QueryRowContext(ctx, `SELECT target FROM audit_events WHERE environment_id = +? AND substr(target, instr(target, '/') + 1) = +?`, 1, "production/web").Scan(&target); err != nil || target != "shop/production/web" {
		t.Fatalf("history = %q, %v", target, err)
	}
	checkPlans(t, s, []planCase{
		{"RenameProject", renameProject, []any{"next", 1}},
		{"RenameProjectAlerts", renameProjectAlerts, []any{"next", "shop", 1}},
		{"ListAuditEventsByTarget", listAuditEventsByTarget, []any{1, 1, "production/web", nil, nil, math.MaxInt64, 50}},
		{"ListAuditEventsByTarget/action", listAuditEventsByTarget, []any{1, 1, "production/web", "service.", "service/", math.MaxInt64, 50}},
	})
}
