package store

import (
	"context"
	"testing"
)

// Of live sites that share a public domain, in any orgs, the oldest keeps
// it and the others lose it; an archived site's doesn't count.
func TestSitePublicAddressesMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_site_public_addresses.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One'), (2, 'two', 'Two')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop'), (2, 2, 'theirs')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1), (2, 2, 'production', 1)`,
		`INSERT INTO sites (id, environment_id, name, settings, archived_at, created_by, created_at) VALUES
			(1, 2, 'gone', '{"public_domain":"stats.example.com"}', '2026-01-02', 'b', '2026-01-01'),
			(2, 1, 'blog', '{"public":true,"public_domain":"stats.example.com"}', NULL, 'a', '2026-01-01'),
			(3, 2, 'blog', '{"public":true,"public_domain":"stats.example.com"}', NULL, 'b', '2026-01-01'),
			(4, 2, 'shop', '{"public_domain":""}', NULL, 'b', '2026-01-01'),
			(5, 1, 'shop', '{"public_domain":""}', NULL, 'a', '2026-01-01')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if id, err := s.PublicDomainHolder(ctx, "stats.example.com"); err != nil || id != 2 {
		t.Errorf("stats.example.com is site %d's, %v; want 2's", id, err)
	}
	for id, want := range map[int64]string{1: `{"public_domain":"stats.example.com"}`, 3: `{"public":true}`, 4: `{"public_domain":""}`} {
		if site, err := s.GetSite(ctx, id); err != nil || site.Settings != want {
			t.Errorf("site %d's settings = %s, %v; want %s", id, site.Settings, err, want)
		}
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sites SET settings = '{"public_domain":"stats.example.com"}' WHERE id = 4`); !IsUnique(err) {
		t.Errorf("a second site took stats.example.com: %v", err)
	}
}
