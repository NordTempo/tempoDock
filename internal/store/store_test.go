package store

import (
	"context"
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
