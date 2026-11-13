package store

import (
	"context"
	"testing"
)

// A project's preview settings move to its base's services and the base
// itself (ADR 0077): connected services build pull requests with the
// project's forks setting, web.KEY copies go to web, shared.KEY ones and
// the numbers to the base, and seeded services keep seeding.
func TestServicePreviewsMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_service_previews.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1), (2, 1, 'staging', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES
		   (1, 2, 'web', ''), (2, 2, 'api', ''), (3, 2, 'db', 'postgres:17'), (4, 2, 'cache', 'redis:8'), (5, 1, 'web', '')`,
		`INSERT INTO git_connections (service_id, hook_id, repo, branch, dir, secret, created_by) VALUES
		   (1, 'h1', 'acme/shop', 'main', 'apps/web', x'00', 'ada'), (2, 'h2', 'acme/shop', 'main', 'apps/api', x'00', 'ada'),
		   (5, 'h5', 'acme/shop', 'main', 'apps/web', x'00', 'ada')`,
		`INSERT INTO preview_settings (project_id, base_environment_id, max_previews, copy_variables, allow_forks, seed_from_snapshot, sleep_after_minutes, updated_by, updated_at)
		 VALUES (1, 2, 5, '["web.API_URL","web.FLAG","shared.SENTRY_DSN"]', 1, '["db"]', 45, 'ada', '2026-11-01 10:00:00')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ep, err := s.GetEnvironmentPreviews(ctx, 2)
	if err != nil || ep.MaxPreviews != 5 || ep.SleepAfterMinutes != 45 || ep.CopyShared != `["SENTRY_DSN"]` || ep.UpdatedBy != "ada" {
		t.Fatalf("staging = %+v, %v", ep, err)
	}
	want := map[int64]ServicePreview{
		1: {Build: true, AllowForks: true, CopyVariables: `["API_URL","FLAG"]`},
		2: {Build: true, AllowForks: true, CopyVariables: `[]`},
		3: {CopyVariables: `[]`, Seed: true},
	}
	for id, w := range want {
		sp, err := s.GetServicePreviews(ctx, id)
		if err != nil || sp.Build != w.Build || sp.AllowForks != w.AllowForks || sp.CopyVariables != w.CopyVariables || sp.Seed != w.Seed {
			t.Fatalf("service %d = %+v, %v; want %+v", id, sp, err, w)
		}
	}
	// cache has nothing to keep, and production's web isn't in the base.
	for _, id := range []int64{4, 5} {
		if _, err := s.GetServicePreviews(ctx, id); !IsNotFound(err) {
			t.Fatalf("service %d has preview settings: %v", id, err)
		}
	}
	if _, err := s.GetEnvironmentPreviews(ctx, 1); !IsNotFound(err) {
		t.Fatalf("production has preview numbers: %v", err)
	}
}
