package store

import (
	"context"
	"testing"
)

// Sites become their service's (ADR 0103): a site takes the service with
// most of its hostnames, another service it counted gets a site of its
// own, a service that led two live sites keeps the older, and a live site
// with no service left is archived.
func TestSitesPerServiceMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_sites_per_service.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'web', ''), (2, 1, 'blog', ''), (3, 1, 'api', '')`,
		`INSERT INTO sites (id, environment_id, name, settings, archived_at, created_by, created_at) VALUES
			(1, 1, 'shop.example.com', '{"event_retention_days":30,"public_domain":"stats.example.com"}', NULL, 'a', '2026-01-01'),
			(2, 1, 'shop.example.org', '{}', NULL, 'a', '2026-01-02'),
			(3, 1, 'gone', '{}', NULL, 'a', '2026-01-03'),
			(4, 1, 'old', '{}', '2026-01-05', 'a', '2026-01-04'),
			(5, 1, 'blog.example.com', '{}', NULL, 'a', '2026-01-04')`,
		`INSERT INTO site_hostnames (hostname, site_id, service_id, added_at) VALUES
			('shop.example.com', 1, 1, '2026-01-01'), ('www.shop.example.com', 1, 1, '2026-01-01'),
			('blog.example.com', 1, 2, '2026-01-01'),
			('shop.example.org', 2, 1, '2026-01-02'),
			('removed.example.com', 3, NULL, '2026-01-03'),
			('old.example.com', 4, 3, '2026-01-04')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	type want struct {
		service  int64
		archived bool
	}
	for id, w := range map[int64]want{
		1: {1, false}, // web has two of its three hostnames
		2: {1, true},  // web led site 1 already
		3: {0, true},  // no service
		4: {3, true},  // archived, still api's: turning it on brings it back
		5: {0, true},  // counted nothing
	} {
		site, err := s.GetSite(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var svc int64
		if site.ServiceID != nil {
			svc = *site.ServiceID
		}
		if svc != w.service || (site.ArchivedAt != nil) != w.archived {
			t.Errorf("site %d: service %d, archived %v; want %+v", id, svc, site.ArchivedAt != nil, w)
		}
	}
	// blog's hostname was counted by web's site: blog gets its own, named
	// after it (site 5, which had the name, is archived by then), with
	// the settings but not the public domain.
	blog, err := s.LiveSiteOfService(ctx, ptr(int64(2)))
	if err != nil || blog.Name != "blog.example.com" || blog.Settings != `{"event_retention_days":30}` || blog.PublicSlug != nil {
		t.Errorf("blog's site = %+v, %v", blog, err)
	}
	if hosts, err := s.ListSiteHostnames(ctx, []int64{1}); err != nil || len(hosts) != 0 {
		t.Errorf("site 1 counts %+v, %v; web has no domains", hosts, err)
	}
}
