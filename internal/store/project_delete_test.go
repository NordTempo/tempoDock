package store

import (
	"context"
	"path/filepath"
	"testing"
)

// openTest opens a migrated database with the test org; the package's
// own tests can't use storetest, which imports it.
func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "dock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateOrg(t.Context(), CreateOrgParams{Slug: "test", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	return st
}

// Every table with a foreign key into one projectDeletes empties must be
// emptied too, or cascade or unlink: otherwise deleting a project fails on
// the first row it holds.
func TestProjectDeletesCoverForeignKeys(t *testing.T) {
	st := openTest(t)
	deleted := map[string]bool{}
	for _, d := range projectDeletes {
		deleted[d.table] = true
	}
	rows, err := st.DB.QueryContext(t.Context(), `SELECT m.name, f."from", f."table", f.on_delete
		FROM sqlite_master m, pragma_foreign_key_list(m.name) f WHERE m.type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, col, parent, onDelete string
		if err := rows.Scan(&table, &col, &parent, &onDelete); err != nil {
			t.Fatal(err)
		}
		unlinked := table == "backups" && col == "restored_to" // DeleteProject's UPDATE
		if deleted[parent] && !deleted[table] && !unlinked && onDelete != "CASCADE" && onDelete != "SET NULL" {
			t.Errorf("%s.%s references %s, which deleting a project empties: add %s to projectDeletes", table, col, parent, table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// DeleteProject leaves no row of the project and nothing dangling, keeps
// other projects and the audit log, and a project created after it never
// gets its ID.
func TestDeleteProject(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	srv, err := st.CreateServer(ctx, CreateServerParams{Name: "local", Local: true, AcceptsProduction: true, AcceptsNonProduction: true})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(name string) (Project, Service) {
		p, err := st.CreateProject(ctx, CreateProjectParams{OrgID: 1, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		e, err := st.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: p.ID, Name: "production", Kind: "production", ServerID: srv.ID})
		if err != nil {
			t.Fatal(err)
		}
		s, err := st.CreateService(ctx, CreateServiceParams{EnvironmentID: e.ID, Name: "web"})
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`INSERT INTO releases (service_id, number, source, config, created_by) VALUES (?1, 1, 'image', x'00', 'test')`,
			`INSERT INTO release_events (release_id, message) SELECT id, 'started' FROM releases WHERE service_id = ?1`,
			`INSERT INTO volumes (service_id, name, docker_name, mount_path) VALUES (?1, 'data', 'v' || ?1, '/data')`,
			`INSERT INTO domains (hostname, service_id) VALUES ('x' || ?1 || '.example.com', ?1)`,
			`INSERT INTO variables (environment_id, service_id, key, value) SELECT environment_id, id, 'K', x'00' FROM services WHERE id = ?1`,
			`INSERT INTO traffic_stats (service_id, res, at, requests, status_2xx, status_3xx, status_4xx, status_5xx, bytes, duration_ms) VALUES (?1, 60, 1, 1, 1, 0, 0, 0, 1, 1)`,
		} {
			if _, err := st.DB.ExecContext(ctx, q, s.ID); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if _, err := st.DB.ExecContext(ctx, `INSERT INTO audit_events (actor, actor_kind, action, project_id, target, summary, org_id) VALUES ('test', 'system', 'project.create', ?, ?, 'created', 1)`, p.ID, p.Name); err != nil {
			t.Fatal(err)
		}
		return p, s
	}
	keep, _ := seed("keep")
	gone, svc := seed("gone")
	if err := st.DeleteProject(ctx, gone.ID, func(*Queries) error { return nil }); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		var n int
		if err := st.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, table := range []string{"releases", "release_events", "volumes", "domains", "variables", "traffic_stats", "services", "environments", "projects"} {
		if n := count("SELECT count(*) FROM " + table); n != 1 {
			t.Errorf("%s has %d rows, want only the other project's", table, n)
		}
	}
	if n := count("SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Errorf("%d dangling foreign keys", n)
	}
	if n := count("SELECT count(*) FROM audit_events WHERE project_id = ?", gone.ID); n != 1 {
		t.Errorf("the deleted project's audit events: %d, want 1", n)
	}
	if _, err := st.GetProject(ctx, keep.ID); err != nil {
		t.Errorf("the other project: %v", err)
	}
	again, s2 := seed("gone")
	if again.ID <= gone.ID || s2.ID <= svc.ID {
		t.Errorf("the new project and service got IDs %d and %d, the deleted ones' were %d and %d: IDs must not be reused", again.ID, s2.ID, gone.ID, svc.ID)
	}
}

// Migration 0103 keeps every row of the tables it rebuilds, and IDs keep
// counting from the highest one there was.
func TestAutoincrementMigrationKeepsRows(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_autoincrement_ids.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name, archived_at) VALUES (1, 1, 'shop', '2026-11-01 00:00:00'), (7, 1, 'blog', NULL)`,
		`INSERT INTO environments (id, project_id, name, server_id, kind, network) VALUES (3, 7, 'production', 1, 'production', 'net')`,
		`INSERT INTO services (id, environment_id, name, image, database, staged) VALUES (9, 3, 'db', 'postgres:17', 'postgres', 'create')`,
		`INSERT INTO sites (id, environment_id, name, created_by, created_at, public_slug) VALUES (4, 3, 'www', 'test', '2026-11-01 00:00:00', 'blog')`,
		`INSERT INTO releases (service_id, number, source, config, created_by) VALUES (9, 1, 'image', x'00', 'test')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p, err := s.GetProject(ctx, 1)
	if err != nil || p.Name != "shop" || p.ArchivedAt == nil {
		t.Fatalf("project 1 = %+v, %v", p, err)
	}
	e, err := s.GetEnvironment(ctx, 3)
	if err != nil || e.Network != "net" || e.Kind != "production" {
		t.Fatalf("environment 3 = %+v, %v", e, err)
	}
	svc, err := s.GetService(ctx, 9)
	if err != nil || svc.Database != "postgres" || svc.Staged != "create" {
		t.Fatalf("service 9 = %+v, %v", svc, err)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM projects WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	next, err := s.CreateProject(ctx, CreateProjectParams{OrgID: 1, Name: "shop"})
	if err != nil || next.ID != 8 {
		t.Fatalf("new project = %+v, %v; want ID 8", next, err)
	}
}
