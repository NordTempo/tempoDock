package store

import (
	"bytes"
	"context"
	"testing"
)

// Migration 0078 makes orgs' identity providers the platform's: ids,
// identities and sealed secrets kept (listed for resealing), a name taken
// by an older provider suffixed with its org, Google's issuer the google
// kind, and an org's SSO session bound to that org's provider.
func TestPlatformSSOMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_platform_sso.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One'), (2, 'two', 'Two')`,
		`UPDATE orgs SET sso_only = 1 WHERE id = 2`,
		`INSERT INTO users (id, email, name, password_hash) VALUES (7, 'ada@example.com', 'Ada', 'x')`,
		`INSERT INTO sso_providers (id, org_id, name, issuer, client_id, client_secret, jit_role, created_at, created_by)
		 VALUES (3, 1, 'corp', 'https://accounts.google.com', 'g', x'01', '', '2026-01-01', 'a'),
		        (4, 2, 'corp', 'https://id.two.example', 'c', x'02', 'read', '2026-01-02', 'b')`,
		`INSERT INTO sso_provider_domains (domain, provider_id) VALUES ('example.com', 3), ('example.com', 4)`,
		`INSERT INTO sso_identities (provider_id, subject, user_id, email, created_at) VALUES (4, 'ada-sub', 7, 'ada@example.com', '2026-01-03')`,
		`INSERT INTO sessions (user_id, hash, expires_at, sso_org_id) VALUES (7, 'h1', '2030-01-01', 2), (7, 'h2', '2030-01-01', NULL)`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ps, err := s.ListSSOProviders(ctx)
	if err != nil || len(ps) != 2 {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
	if p := ps[0]; p.ID != 3 || p.Name != "corp" || p.Kind != "google" || p.Label != "Google" || !bytes.Equal(p.ClientSecret, []byte{1}) {
		t.Errorf("one's provider = %+v", p)
	}
	if p := ps[1]; p.ID != 4 || p.Name != "corp-two" || p.Kind != "oidc" || p.Label != "Corp" || p.Issuer != "https://id.two.example" {
		t.Errorf("two's provider = %+v", p)
	}
	if id, err := s.GetSSOIdentity(ctx, GetSSOIdentityParams{ProviderID: 4, Subject: "ada-sub"}); err != nil || id.UserID != 7 {
		t.Errorf("identity = %+v, %v", id, err)
	}
	reseal, err := s.ListSSOReseal(ctx)
	if err != nil || len(reseal) != 2 || reseal[1].SsoReseal.OrgID != 2 || reseal[1].SsoReseal.Name != "corp" {
		t.Errorf("to reseal = %+v, %v", reseal, err)
	}
	var bound, password *int64
	if err := s.DB.QueryRowContext(ctx, `SELECT (SELECT sso_provider_id FROM sessions WHERE hash = 'h1'), (SELECT sso_provider_id FROM sessions WHERE hash = 'h2')`).Scan(&bound, &password); err != nil || bound == nil || *bound != 4 || password != nil {
		t.Errorf("sessions' providers = %v, %v (%v)", bound, password, err)
	}
	if set, err := s.GetSignInSettings(ctx); !IsNotFound(err) {
		t.Errorf("sign-in settings = %+v, %v; want none (both on)", set, err)
	}
	// Removing a provider takes its identities and sessions with it.
	if err := s.DeleteSSOProvider(ctx, 4); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM sso_identities) + (SELECT count(*) FROM sessions WHERE sso_provider_id IS NOT NULL)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d identities and sessions left, %v", n, err)
	}
}
