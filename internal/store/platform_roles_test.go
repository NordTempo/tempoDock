package store

import (
	"context"
	"testing"
)

// TestPlatformRolesMigration: operators get the platform's operator role
// and others none, and operators' live `dock login` tokens become legacy
// tokens that keep running the platform (ADR 0080), while other logins
// and org tokens don't.
func TestPlatformRolesMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_platform_roles.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO users (id, email, name, password_hash, operator) VALUES (1, 'op@x', 'Op', 'h', 1), (2, 'bob@x', 'Bob', 'h', 0)`,
		`INSERT INTO tokens (id, name, hash, scope, created_by, origin, user_id) VALUES
			(1, 'cli: op', 'h1', 'admin', 'op@x', 'cli_login', 1),
			(2, 'cli: bob', 'h2', 'admin', 'bob@x', 'cli_login', 2)`,
		`INSERT INTO tokens (id, name, hash, scope, created_by, origin, user_id, revoked_at) VALUES
			(3, 'cli: old', 'h3', 'admin', 'op@x', 'cli_login', 1, '2026-01-01')`,
		`INSERT INTO tokens (id, name, hash, scope, created_by, org_id, operator) VALUES
			(4, 'bootstrap', 'h4', 'admin', 'op@x', 1, 1), (5, 'ci', 'h5', 'deploy', 'op@x', 1, 0)`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	for id, want := range map[int64]string{1: "operator", 2: ""} {
		u, err := s.GetUser(ctx, id)
		got := ""
		if u.PlatformRole != nil {
			got = *u.PlatformRole
		}
		if err != nil || got != want {
			t.Errorf("user %d: platform role %q, %v; want %q", id, got, err, want)
		}
	}
	for id, legacy := range map[int64]bool{1: true, 2: false, 3: false, 4: true, 5: false} {
		tok, err := s.GetToken(ctx, id)
		if err != nil || tok.Operator != legacy || tok.Platform != nil {
			t.Errorf("token %d: operator %v platform %v, %v; want legacy %v", id, tok.Operator, tok.Platform, err, legacy)
		}
	}
}
