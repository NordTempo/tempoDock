package store

import (
	"context"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Services keep their database setting through the column swap, and the
// setting takes every engine.
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
	for _, engine := range apitypes.DataEngines {
		if _, err := s.SetServiceDatabaseSettings(ctx, SetServiceDatabaseSettingsParams{ID: 3, Database: string(engine)}); err != nil {
			t.Fatalf("set %s: %v", engine, err)
		}
	}
	if _, err := s.SetServiceDatabaseSettings(ctx, SetServiceDatabaseSettingsParams{ID: 3, Database: "oracle"}); err == nil {
		t.Fatal("an unknown engine was stored")
	}
}

// Data access keeps its engine through the column swap, and takes every
// engine: setting up MySQL and Redis stored their roles here and failed
// on 0010's CHECK, which #218's fix of the other one missed.
func TestDataAccessEnginesMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_data_access_engines.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'db', 'postgres:17'), (2, 1, 'cache', 'redis:8')`,
		`INSERT INTO data_access (service_id, engine, reader_secret, writer_secret, set_up_at, set_up_by) VALUES (1, 'postgres', x'01', x'02', '2026-11-11 00:00:00', 'user:1')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if a, err := s.GetDataAccess(ctx, 1); err != nil || a.Engine != "postgres" || string(a.ReaderSecret) != "\x01" {
		t.Fatalf("kept: %+v %v", a, err)
	}
	upsert := func(id int64, engine string) error {
		_, err := s.UpsertDataAccess(ctx, UpsertDataAccessParams{ServiceID: id, Engine: engine, ReaderSecret: []byte{1}, WriterSecret: []byte{2}, SetUpAt: time.Now(), SetUpBy: "user:1"})
		return err
	}
	for _, engine := range apitypes.DataEngines {
		if err := upsert(2, string(engine)); err != nil {
			t.Fatalf("set up %s: %v", engine, err)
		}
	}
	if err := upsert(2, "oracle"); err == nil {
		t.Fatal("an unknown engine was stored")
	}
}
