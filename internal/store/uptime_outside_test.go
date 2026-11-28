package store

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// TestOutsideUptimeChecksMigration: a service's checks keep their ID,
// history and status page sources through the rebuild (ADR 0055), a
// check is then a service's or an organization's, never both or neither,
// and a project named status is renamed out of the Status page's way.
func TestOutsideUptimeChecksMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_outside_uptime_checks.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'web', 'app:1')`,
		`INSERT INTO uptime_checks (id, service_id, url, expect_status, interval_s, timeout_ms, created_at, created_by, auto)
		   VALUES (7, 1, 'https://shop.example.com/', 200, 60, 10000, '2026-11-01', 'a', 1)`,
		`INSERT INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum) VALUES (7, 60, 600, 1, 1, 20)`,
		`INSERT INTO status_pages (id, org_id, slug, title, created_at, created_by) VALUES (1, 1, 'acme', 'Acme', '2026-11-01', 'a')`,
		`INSERT INTO status_components (id, page_id, position, name) VALUES (1, 1, 0, 'Web')`,
		`INSERT INTO status_sources (component_id, check_id) VALUES (1, 7)`,
		`INSERT INTO orgs (id, slug, name) VALUES (2, 'two', 'Two')`,
		`INSERT INTO projects (id, org_id, name) VALUES (2, 1, 'status'), (3, 1, 'status-2'), (4, 2, 'status')`,
		// Check 9 was removed; its alert stays.
		`INSERT INTO alerts (key, kind, severity, title, opened_at, updated_at, resolved_at) VALUES ('uptime:9', 'uptime_down', 'critical', 'gone', '2026-11-01', '2026-11-01', '2026-11-01')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	c, err := s.GetUptimeCheck(ctx, 7)
	if err != nil || c.ServiceID == nil || *c.ServiceID != 1 || c.OrgID != nil || !c.Auto || c.Url != "https://shop.example.com/" {
		t.Fatalf("check 7 = %+v, %v", c, err)
	}
	var stats, sources int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM uptime_stats WHERE check_id = 7), (SELECT count(*) FROM status_sources WHERE check_id = 7)`).Scan(&stats, &sources); err != nil || stats != 1 || sources != 1 {
		t.Fatalf("history %d, sources %d, %v; want 1 and 1", stats, sources, err)
	}
	// A project named status moves aside for the Status page, and says so.
	// Org one has a status-2 already, so its status gets a suffix.
	for id, want := range map[int64]string{2: `^status-2-[0-9a-f]{6}$`, 4: `^status-4$`} {
		var name string
		var audits int
		if err := s.DB.QueryRow(`SELECT name, (SELECT count(*) FROM audit_events WHERE action = 'project.rename' AND project_id = ?1 AND target = projects.name) FROM projects WHERE id = ?1`, id).Scan(&name, &audits); err != nil ||
			!regexp.MustCompile(want).MatchString(name) || audits != 1 {
			t.Errorf("project %d: %q, %d audit events, %v; want %s and 1", id, name, audits, err, want)
		}
	}
	// New IDs start past every one an alert names, and a removed check's
	// ID is never given again: its alerts would become the new check's.
	org := int64(1)
	outside := CreateUptimeCheckParams{OrgID: &org, Url: "https://status.vendor.example/", ExpectStatus: 200, IntervalS: 60, TimeoutMs: 10000, CreatedAt: time.Now()}
	c1, err := s.CreateUptimeCheck(ctx, outside)
	if err != nil || c1.ID != 10 {
		t.Fatalf("an outside check: %d, %v; want ID 10", c1.ID, err)
	}
	if err := s.DeleteUptimeCheck(ctx, c1.ID); err != nil {
		t.Fatal(err)
	}
	if c2, err := s.CreateUptimeCheck(ctx, outside); err != nil || c2.ID != 11 {
		t.Errorf("after removing check 10: ID %d, %v; want 11", c2.ID, err)
	}
	for name, p := range map[string]CreateUptimeCheckParams{
		"both":    {ServiceID: &org, OrgID: &org},
		"neither": {},
	} {
		p.Url, p.CreatedAt = "https://x.example/", time.Now()
		if _, err := s.CreateUptimeCheck(ctx, p); err == nil {
			t.Errorf("a check with %s a service and an org was added", name)
		}
	}
}
