package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkListSiteDeploys reads a week of deploy markers for a site
// served by four services, each with a year of releases (2,000 apiece, a
// busy CI pipeline), as a site dashboard does on every load.
func BenchmarkListSiteDeploys(b *testing.B) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	srv, err := s.CreateServer(ctx, CreateServerParams{Name: "local", AcceptsProduction: true, AcceptsNonProduction: true, Local: true})
	if err != nil {
		b.Fatal(err)
	}
	p, err := s.CreateProject(ctx, CreateProjectParams{OrgID: testOrg(b, s), Name: "shop"})
	if err != nil {
		b.Fatal(err)
	}
	env, err := s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: p.ID, Name: "production", Kind: "production", ServerID: srv.ID})
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	site, err := s.CreateSite(ctx, CreateSiteParams{EnvironmentID: env.ID, Name: "shop", CreatedBy: "bench", CreatedAt: now})
	if err != nil {
		b.Fatal(err)
	}
	const services, releases = 4, 2000
	ids := make([]int64, services)
	for i := range ids {
		svc, err := s.CreateService(ctx, CreateServiceParams{EnvironmentID: env.ID, Name: fmt.Sprintf("web-%d", i), Image: "app:1"})
		if err != nil {
			b.Fatal(err)
		}
		if err := s.AddSiteHostname(ctx, AddSiteHostnameParams{Hostname: fmt.Sprintf("web-%d.example.com", i), SiteID: site.ID, ServiceID: &svc.ID, AddedAt: now}); err != nil {
			b.Fatal(err)
		}
		ids[i] = svc.ID
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	for _, id := range ids {
		for n := range releases {
			finished := now.Add(-time.Duration(releases-n) * 365 * 24 * time.Hour / releases)
			if _, err := tx.ExecContext(ctx, `INSERT INTO releases (service_id, number, status, source, config, created_by, created_at, finished_at)
				VALUES (?, ?, 'superseded', 'image', x'7b7d', 'bench', ?, ?)`, id, n+1, finished, finished); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	q := ListSiteDeploysParams{SiteID: site.ID, FromAt: ptr(now.Add(-7 * 24 * time.Hour)), ToAt: ptr(now)}
	b.ReportAllocs()
	for b.Loop() {
		rows, err := s.ListSiteDeploys(ctx, q)
		if err != nil || len(rows) < 4*38 {
			b.Fatalf("%d rows, %v", len(rows), err)
		}
	}
}

// ptr is a pointer to v.
func ptr[T any](v T) *T { return &v }
