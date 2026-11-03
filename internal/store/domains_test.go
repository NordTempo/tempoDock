package store

import (
	"context"
	"testing"
)

// Domains from before verification (ADR 0042) stay live: verified, still
// routed, and their Cloudflare record still points at them.
func TestDomainVerificationMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_domain_verification.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'web', 'app:1')`,
		`INSERT INTO domains (id, hostname, service_id, status, created_at) VALUES (1, 'shop.example.com', 1, 'active', '2026-01-01 10:00:00')`,
		`INSERT INTO cloudflare_connections (id, name, token, account_id, created_at, updated_at, updated_by) VALUES (1, 'cf', x'00', 'acc', '2026-01-01', '2026-01-01', 'op')`,
		`INSERT INTO cloudflare_records (hostname, domain_id, connection_id, zone_id, updated_at) VALUES ('shop.example.com', 1, 1, 'z', '2026-01-01')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	d, err := s.GetDomainByHostname(ctx, "shop.example.com")
	if err != nil || d.VerifiedAt == nil || !d.VerifiedAt.Equal(d.CreatedAt) || d.Status != "active" {
		t.Fatalf("domain = %+v, %v; want verified when added", d, err)
	}
	routes, err := s.ListRoutableDomains(ctx, 1)
	if err != nil || len(routes) != 1 || routes[0].Hostname != "shop.example.com" {
		t.Fatalf("routable = %+v, %v", routes, err)
	}
	if r, err := s.GetCloudflareRecord(ctx, "shop.example.com"); err != nil || r.DomainID == nil || *r.DomainID != 1 {
		t.Fatalf("cloudflare record = %+v, %v", r, err)
	}
}

// Lookups of a hostname's claims read through an index.
func TestDomainQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	checkPlans(t, f.s, []planCase{
		{"GetDomainByHostname", getDomainByHostname, []any{"shop.example.com"}},
		{"GetServiceDomain", getServiceDomain, []any{"shop.example.com", 1}},
		{"ListHostnameClaims", listHostnameClaims, []any{"shop.example.com"}},
		{"VerifyDomain", verifyDomain, []any{nil, 1}},
		{"DomainScope", domainScope, []any{"shop.example.com", 1}},
	})
}
