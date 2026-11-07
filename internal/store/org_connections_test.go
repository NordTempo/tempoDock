package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// Migration 0073 keeps an existing Cloudflare connection as the
// platform's, shared, with its token and everything that points at it
// (zones, records, join tokens); names are unique per org from then on.
func TestOrgConnectionsMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_org_connections.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO cloudflare_connections (id, name, account_id, token, created_at, updated_at, updated_by) VALUES (5, 'home', 'acc', x'0102', '2026-01-01', '2026-01-01', 'admin')`,
		`INSERT INTO cloudflare_zones (connection_id, zone_id, name) VALUES (5, 'z1', 'tempo.run')`,
		`INSERT INTO cloudflare_tunnels (server_id, server_name, connection_id, updated_at) VALUES (1, 'local', 5, '2026-01-01')`,
		`INSERT INTO cloudflare_records (hostname, server_id, connection_id, zone_id, updated_at) VALUES ('shop.tempo.run', 1, 5, 'z1', '2026-01-01')`,
		`INSERT INTO join_tokens (hash, server_name, created_by, created_at, expires_at, cloudflare_id) VALUES ('h', 'lab', 'admin', '2026-01-01', '2027-01-01', 5)`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	home, err := s.GetCloudflareConnection(ctx, GetCloudflareConnectionParams{Org: 0, Name: "home"})
	if err != nil || home.ID != 5 || home.OrgID != nil || !home.Shared || !bytes.Equal(home.Token, []byte{1, 2}) {
		t.Fatalf("home = %+v, %v; want the platform's, shared, its token as it was", home, err)
	}
	zones, err := s.ListCloudflareZones(ctx)
	if err != nil || len(zones) != 1 || zones[0].CloudflareZone.ConnectionID != 5 || zones[0].OrgID != 0 || !zones[0].Shared {
		t.Fatalf("zones = %+v, %v", zones, err)
	}
	if n, err := s.CountConnectionUse(ctx, 5); err != nil || n != 1 {
		t.Errorf("tunnels with home = %d, %v", n, err)
	}
	if r, err := s.GetCloudflareRecord(ctx, "shop.tempo.run"); err != nil || r.ConnectionID != 5 {
		t.Errorf("record = %+v, %v", r, err)
	}
	var joinConn int64
	if err := s.DB.QueryRowContext(ctx, `SELECT cloudflare_id FROM join_tokens`).Scan(&joinConn); err != nil || joinConn != 5 {
		t.Errorf("join token's connection = %d, %v", joinConn, err)
	}

	now := time.Now()
	create := func(org *int64) error {
		_, err := s.CreateCloudflareConnection(ctx, CreateCloudflareConnectionParams{
			OrgID: org, Name: "home", AccountID: "acc", Token: []byte{0}, CreatedAt: now, UpdatedAt: now, UpdatedBy: "a",
		})
		return err
	}
	if err := create(new(int64(1))); err != nil {
		t.Errorf("an org's connection named like a platform one: %v", err)
	}
	if err := create(nil); !IsUnique(err) {
		t.Errorf("a second platform connection home: %v, want a unique violation", err)
	}
	if mine, err := s.ListCloudflareConnectionsOf(ctx, 1); err != nil || len(mine) != 1 || mine[0].Shared {
		t.Errorf("org 1's connections = %+v, %v; a new one isn't shared", mine, err)
	}
}

// An org's Cloudflare connections are read through their index.
func TestOrgConnectionQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	checkPlans(t, f.s, []planCase{
		{"ListCloudflareConnectionsOf", listCloudflareConnectionsOf, []any{1}},
		{"GetCloudflareConnection", getCloudflareConnection, []any{1, "home"}},
		{"RetryConnectionRecords", retryConnectionRecords, []any{time.Now(), 1}},
	})
}
