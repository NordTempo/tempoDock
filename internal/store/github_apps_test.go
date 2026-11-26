package store

import (
	"context"
	"testing"
)

// The install's one GitHub App becomes the platform's App 1, its
// installations stay with their orgs and become App 1's, and its open
// alert gets the App's own key.
func TestGitHubAppsMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_github_apps.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO github_app (id, app_id, hook_id, slug, name, owner, html_url, private_key, webhook_secret, client_id, client_secret, created_by, created_at)
			VALUES (1, 99, 'h00k', 'tempo-dock', 'Tempo Dock', 'acme', 'https://github.com/apps/tempo-dock', x'02aa', x'02bb', 'Iv1', x'02cc', 'ops', '2026-01-01')`,
		`INSERT INTO github_installations (installation_id, org_id, account, assigned_by, assigned_at) VALUES (7, 1, 'acme', 'ops', '2026-01-01')`,
		`INSERT INTO alerts (key, kind, severity, title, opened_at, updated_at) VALUES ('github:app', 'connection_failed', 'warning', 'The GitHub App is failing', '2026-01-01', '2026-01-01')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	app, err := s.GetGitHubAppByHook(ctx, "h00k")
	if err != nil || app.ID != 1 || app.OrgID != nil || app.AppID != 99 || app.Slug != "tempo-dock" || string(app.ClientSecret) != "\x02\xcc" {
		t.Fatalf("migrated App: %+v, %v", app, err)
	}
	if inst, err := s.GetGitHubInstallation(ctx, 7); err != nil || inst.AppID != 1 || inst.OrgID != 1 {
		t.Fatalf("migrated installation: %+v, %v", inst, err)
	}
	var key string
	if err := s.DB.QueryRowContext(ctx, `SELECT key FROM alerts`).Scan(&key); err != nil || key != "github:app:1" {
		t.Fatalf("alert key %q, %v", key, err)
	}
	// Removing the App takes its installations with it.
	if err := s.DeleteGitHubApp(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGitHubInstallation(ctx, 7); !IsNotFound(err) {
		t.Fatalf("installation of a removed App: %v", err)
	}
}
