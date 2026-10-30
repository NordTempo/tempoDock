package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// testOrg creates an org for a test's projects and returns its ID.
func testOrg(t testing.TB, s *Store) int64 {
	t.Helper()
	o, err := s.CreateOrg(context.Background(), CreateOrgParams{Slug: "test", Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID
}

// Every migration has a number of its own: migrate skips one numbered at or
// below the version already applied, so two branches that both add the
// next number would leave one unapplied. Renumber the later one.
func TestMigrationNumbersUnique(t *testing.T) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		num, _, _ := strings.Cut(base, "_")
		if other, ok := seen[num]; ok {
			t.Errorf("migrations %s and %s share number %s; give the newer one the next free number", other, base, num)
		}
		seen[num] = base
	}
}

// The schema enforces one in-flight deploy per service; core relies on it.
func TestOneInFlightReleasePerService(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "p.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	srv, err := s.CreateServer(ctx, CreateServerParams{Name: "local", AcceptsProduction: true, Local: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(ctx, CreateProjectParams{OrgID: testOrg(t, s), Name: "shop"})
	env, _ := s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: p.ID, Name: "production", Kind: "production", ServerID: srv.ID})
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

// Servers' single role becomes the two accept switches; environments get
// their kind and keep their server, and each project's placement per kind
// comes from its environments (the newest one's server where they are
// split across servers).
func TestMigrationToServerPools(t *testing.T) {
	ctx := context.Background()
	path := openV1(t,
		`INSERT INTO servers (id, name, role, local) VALUES (1, 'hetzner', 'production', 1), (2, 'homelab', 'non-production', 0)`,
		`INSERT INTO projects (id, name) VALUES (1, 'shop'), (2, 'split')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES
			(1, 1, 'production', 1), (2, 1, 'staging', 2), (3, 1, 'dev', 2),
			(4, 2, 'staging', 2), (5, 2, 'preview', 1)`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	servers, err := s.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !servers[0].AcceptsProduction || servers[0].AcceptsNonProduction || servers[1].AcceptsProduction || !servers[1].AcceptsNonProduction {
		t.Errorf("accepts = %+v", servers)
	}
	for id, want := range map[int64]struct {
		kind   string
		server int64
	}{1: {"production", 1}, 2: {"non_production", 2}, 3: {"non_production", 2}, 4: {"non_production", 2}, 5: {"non_production", 1}} {
		e, err := s.GetEnvironment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != want.kind || e.ServerID != want.server {
			t.Errorf("environment %d = %s on %d, want %s on %d", id, e.Kind, e.ServerID, want.kind, want.server)
		}
	}
	for _, want := range []ProjectPlacement{
		{ProjectID: 1, Kind: "production", ServerID: 1},
		{ProjectID: 1, Kind: "non_production", ServerID: 2},
		{ProjectID: 2, Kind: "non_production", ServerID: 1},
	} {
		got, err := s.GetPlacement(ctx, GetPlacementParams{ProjectID: want.ProjectID, Kind: want.Kind})
		if err != nil || got.ServerID != want.ServerID || got.Pinned {
			t.Errorf("placement %d/%s = %+v, %v; want server %d", want.ProjectID, want.Kind, got, err, want.ServerID)
		}
	}
	if _, err := s.GetPlacement(ctx, GetPlacementParams{ProjectID: 2, Kind: "production"}); !IsNotFound(err) {
		t.Errorf("project without production environments has a production placement: %v", err)
	}
}

// A lone server took every environment under the old rules, so it keeps
// accepting both kinds whatever its role said.
func TestMigrationLoneServerAcceptsBoth(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, openV1(t, `INSERT INTO servers (id, name, role, local) VALUES (1, 'vps', 'production', 1)`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	srv, err := s.GetServer(ctx, 1)
	if err != nil || !srv.AcceptsProduction || !srv.AcceptsNonProduction {
		t.Fatalf("server = %+v, %v", srv, err)
	}
}

// Processes opening one database at once during an upgrade (a new daemon
// starting while install.sh runs dock admin commands) all migrate it
// without applying a migration twice.
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	path := openBefore(t, "_token_user.sql", "PRAGMA journal_mode = WAL")
	const n = 4
	errs := make(chan error, n)
	for range n {
		go func() {
			s, err := Open(context.Background(), path)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
