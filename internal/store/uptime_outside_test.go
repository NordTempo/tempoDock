package store

import (
	"context"
	"testing"
	"time"
)

// TestOutsideUptimeChecksMigration: a service's checks keep their ID,
// history and status page sources through the rebuild (ADR 0055), and a
// check is then a service's or an organization's, never both or neither.
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
	org := int64(1)
	if _, err := s.CreateUptimeCheck(ctx, CreateUptimeCheckParams{OrgID: &org, Url: "https://status.vendor.example/", ExpectStatus: 200, IntervalS: 60, TimeoutMs: 10000, CreatedAt: time.Now()}); err != nil {
		t.Errorf("an outside check: %v", err)
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
