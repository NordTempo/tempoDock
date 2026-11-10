package store

import (
	"context"
	"testing"
)

// Services keep their database setting through the column swap, and the
// setting takes mysql and redis.
func TestDatabaseEnginesMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_database_engines.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image, database, database_url_variable) VALUES
		   (1, 1, 'db', 'postgres:17', 'postgres', 'DATABASE_URL'), (2, 1, 'web', 'app:1', 'none', ''), (3, 1, 'cache', 'redis:8', '', '')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for id, want := range map[int64]string{1: "postgres", 2: "none", 3: ""} {
		svc, err := s.GetService(ctx, id)
		if err != nil || svc.Database != want {
			t.Fatalf("service %d: %q %v, want %q", id, svc.Database, err, want)
		}
	}
	for _, setting := range []string{"mysql", "redis"} {
		if _, err := s.SetServiceDatabaseSettings(ctx, SetServiceDatabaseSettingsParams{ID: 3, Database: setting}); err != nil {
			t.Fatalf("set %s: %v", setting, err)
		}
	}
	if _, err := s.SetServiceDatabaseSettings(ctx, SetServiceDatabaseSettingsParams{ID: 3, Database: "oracle"}); err == nil {
		t.Fatal("an unknown engine was stored")
	}
}
