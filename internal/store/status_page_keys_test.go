package store

import (
	"context"
	"testing"
	"time"
)

// TestStatusPageKeysMigration: pages keep their addresses (no key), old
// names move to their page's organization, and a name is then unique in
// an organization only (ADR 0087).
func TestStatusPageKeysMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_status_page_keys.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One'), (2, 'two', 'Two')`,
		`INSERT INTO status_pages (id, org_id, slug, title, created_at, created_by) VALUES
			(1, 1, 'api', 'API', '2026-11-01', 'a'), (2, 2, 'status', 'Status', '2026-11-01', 'b')`,
		`INSERT INTO status_page_old_slugs (slug, page_id, changed_at) VALUES ('old-api', 1, '2026-11-02')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	for _, id := range []int64{1, 2} {
		if p, err := s.GetStatusPageByID(ctx, id); err != nil || p.AddressKey != "" {
			t.Errorf("page %d: key %q, %v; want none", id, p.AddressKey, err)
		}
	}
	if p, err := s.GetStatusPageByOldSlug(ctx, GetStatusPageByOldSlugParams{OrgID: 1, Slug: "old-api"}); err != nil || p.ID != 1 {
		t.Errorf("org one's old name: page %d, %v", p.ID, err)
	}
	if _, err := s.GetStatusPageByOldSlug(ctx, GetStatusPageByOldSlugParams{OrgID: 2, Slug: "old-api"}); !IsNotFound(err) {
		t.Errorf("org two found org one's old name: %v", err)
	}
	if _, err := s.CreateStatusPage(ctx, CreateStatusPageParams{OrgID: 2, Slug: "api", AddressKey: "abcdefg", Title: "API", CreatedAt: time.Now(), CreatedBy: "b"}); err != nil {
		t.Errorf("org two's page called api: %v", err)
	}
	if _, err := s.CreateStatusPage(ctx, CreateStatusPageParams{OrgID: 1, Slug: "api", AddressKey: "gfedcba", Title: "API", CreatedAt: time.Now(), CreatedBy: "a"}); !IsUnique(err) {
		t.Errorf("a second page called api in org one: %v", err)
	}
}
