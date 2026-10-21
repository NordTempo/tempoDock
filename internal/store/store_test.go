package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// The schema enforces one in-flight deploy per service; core relies on it.
func TestOneInFlightReleasePerService(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "p.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	srv, err := s.CreateServer(ctx, CreateServerParams{Name: "local", Role: "production", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(ctx, "shop")
	env, _ := s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: p.ID, Name: "production", ServerID: srv.ID})
	svc, err := s.CreateService(ctx, CreateServiceParams{EnvironmentID: env.ID, Name: "web"})
	if err != nil {
		t.Fatal(err)
	}

	newRelease := func(n int64) error {
		_, err := s.CreateRelease(ctx, CreateReleaseParams{ServiceID: svc.ID, Number: n, Source: "image", Config: []byte{}, CreatedBy: "t"})
		return err
	}
	if err := newRelease(1); err != nil {
		t.Fatal(err)
	}
	if err := newRelease(2); !IsUnique(err) {
		t.Fatalf("second in-flight release: err = %v, want unique violation", err)
	}

	// Reopening is a no-op migration.
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
}

// openV1 creates a database with only the initial schema and the given rows,
// like an install from before later migrations.
func openV1(t *testing.T, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	initSQL, err := migrations.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range append(append([]string{string(initSQL)}, rows...), "PRAGMA user_version = 1") {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// Environments with containers keep their pre-ID network name, so their
// running services keep reaching each other. If two already shared a name,
// only the oldest keeps it; environments never deployed get ID-based names.
func TestMigrationKeepsLegacyNetworks(t *testing.T) {
	ctx := context.Background()
	path := openV1(t,
		`INSERT INTO servers (id, name, role, local) VALUES (1, 'local', 'production', 1)`,
		`INSERT INTO projects (id, name) VALUES (1, 'a'), (2, 'a-b'), (3, 'demo')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'b-c', 1), (2, 2, 'c', 1), (3, 3, 'production', 1), (4, 3, 'staging', 1)`,
		`INSERT INTO services (id, environment_id, name) VALUES (1, 1, 'web'), (2, 2, 'web'), (3, 3, 'web'), (4, 4, 'web')`,
		`INSERT INTO releases (service_id, number, source, config, container_id, created_by) VALUES
			(1, 1, 'image', x'', 'c1', 't'), (2, 1, 'image', x'', 'c2', 't'), (3, 1, 'image', x'', 'c3', 't')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	want := map[int64]string{1: "dock-a-b-c", 2: "", 3: "dock-demo-production", 4: ""}
	for id, network := range want {
		env, err := s.GetEnvironment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if env.Network != network {
			t.Errorf("environment %d network = %q, want %q", id, env.Network, network)
		}
	}
}
