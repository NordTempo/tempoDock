package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
)

// BenchmarkPreparedQuery reads one environment's 20 services with a
// prepared statement, three ways, from one client and from eight at once:
//
//   - sqlc: the generated ListServices (`environment_id = ?`).
//   - param: the same SQL on a raw prepared statement.
//   - plus-param: `environment_id = +?`.
//
// The SQLite in modernc.org/sqlite (built with SQLITE_ENABLE_STAT4) looks
// at bound values while planning `column = ?` on an indexed column, which
// marks the statement to be re-prepared (re-parsed and re-planned) on every
// bind. That undoes what preparing saves, and the parser's many small
// allocations go through modernc's libc malloc, which has one global lock,
// so concurrent queries serialize on it. A unary plus on the parameter
// avoids the re-prepare. See docs/performance.md.
func BenchmarkPreparedQuery(b *testing.B) {
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
	p, err := s.CreateProject(ctx, CreateProjectParams{OrgID: testOrg(b, s), Name: "big"})
	if err != nil {
		b.Fatal(err)
	}
	env, err := s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: p.ID, Name: "production", Kind: "production", ServerID: srv.ID})
	if err != nil {
		b.Fatal(err)
	}
	for i := range 20 {
		if _, err := s.CreateService(ctx, CreateServiceParams{EnvironmentID: env.ID, Name: fmt.Sprintf("svc-%02d", i), Image: "app:1"}); err != nil {
			b.Fatal(err)
		}
	}

	raw := func(stmt *sql.Stmt) func() error {
		return func() error {
			rows, err := stmt.QueryContext(ctx, env.ID)
			if err != nil {
				return err
			}
			n := 0
			for rows.Next() {
				n++
			}
			if err := rows.Close(); err != nil || n != 20 {
				return fmt.Errorf("%d rows, err %w", n, err)
			}
			return nil
		}
	}
	prepare := func(query string) *sql.Stmt {
		stmt, err := s.DB.PrepareContext(ctx, query)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = stmt.Close() })
		return stmt
	}
	queries := []struct {
		name string
		run  func() error
	}{
		{"sqlc", func() error {
			svcs, err := s.ListServices(ctx, env.ID)
			if err == nil && len(svcs) != 20 {
				err = fmt.Errorf("%d services", len(svcs))
			}
			return err
		}},
		{"param", raw(prepare("SELECT * FROM services WHERE environment_id = ? ORDER BY name"))},
		{"plus-param", raw(prepare("SELECT * FROM services WHERE environment_id = +? ORDER BY name"))},
	}
	for _, q := range queries {
		b.Run(q.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := q.run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	for _, q := range queries {
		b.Run(q.name+",parallel=8", func(b *testing.B) {
			b.SetParallelism(max(1, 8/runtime.GOMAXPROCS(0)))
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := q.run(); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
