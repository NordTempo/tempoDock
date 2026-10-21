package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Benchmarks for the daemon's hot read paths, on a seeded database. The
// perf budget check (scripts/perf, `make perf`) runs them and compares
// ns/op and allocs/op against scripts/perf/baseline.json.

// Seeded project shape: big has benchEnvs environments of benchServices
// services each (60 in total), with variables referencing each other,
// domains in production, one deploy per service, benchReleases releases of
// svc-00 in production and an audit log of about benchAudit events.
const (
	benchServices = 20
	benchReleases = 100
	benchAudit    = 2000
)

var benchEnvs = []string{"production", "staging", "dev"}

// benchCore is a started Core on a temp database, seeded as described above.
type benchCore struct {
	ctx context.Context
	c   *Core
}

func newBenchCore(b *testing.B) *benchCore {
	b.Helper()
	ctx := context.Background()
	dir := b.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "dock.db"))
	if err != nil {
		b.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		b.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.BaseDomain = "apps.test"
	c, err := New(ctx, Options{
		Config: cfg, Store: st, Secrets: box, Runtime: fake.New(),
		Logger: slog.New(slog.DiscardHandler), CutoverDrain: time.Nanosecond,
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		c.Close()
		_ = st.Close()
	})
	bc := &benchCore{ctx: ctx, c: c}
	bc.seed(b)
	return bc
}

func (bc *benchCore) seed(b *testing.B) {
	b.Helper()
	ctx, c := bc.ctx, bc.c
	must := func(_ any, err error) {
		b.Helper()
		if err != nil {
			b.Fatal(err)
		}
	}
	must(c.CreateProject(ctx, System, apitypes.CreateProjectRequest{Name: "big"}))
	var pending []ServiceRef
	var numbers []int64
	for _, env := range benchEnvs {
		if env != "production" {
			must(c.CreateEnvironment(ctx, System, "big", apitypes.CreateEnvironmentRequest{Name: env}))
		}
		ref := EnvRef{"big", env}
		must(c.SetVariables(ctx, System, ref, apitypes.SetVariablesRequest{Set: map[string]string{
			"LOG_LEVEL": "info", "REGION": "eu-north", "FEATURES": "a,b,c",
			"API_URL": "http://${{svc-01.DOCK_PRIVATE_DOMAIN}}:${{svc-01.PORT}}",
		}}))
		for i := range benchServices {
			name := fmt.Sprintf("svc-%02d", i)
			must(c.CreateService(ctx, System, ref, apitypes.CreateServiceRequest{Name: name, Image: "app:1", Port: 8080}))
			must(c.SetVariables(ctx, System, ref, apitypes.SetVariablesRequest{Service: name, Set: map[string]string{
				"DATABASE_URL": "postgres://app:secret@db:5432/" + name,
				"SECRET_KEY":   "0123456789abcdef0123456789abcdef",
				"UPSTREAM":     "${{svc-00.DOCK_PRIVATE_DOMAIN}} ${{shared.REGION}}",
			}}))
			svc := ServiceRef{"big", env, name}
			if env == "production" {
				must(c.AddDomain(ctx, System, svc, apitypes.AddDomainRequest{Hostname: name + ".big.example.com"}))
			}
		}
		must(c.ApplyChanges(ctx, System, ref))
		for i := range benchServices {
			svc := ServiceRef{"big", env, fmt.Sprintf("svc-%02d", i)}
			rel, err := c.DeployImage(ctx, System, svc, apitypes.DeployImageRequest{})
			if err != nil {
				b.Fatal(err)
			}
			pending, numbers = append(pending, svc), append(numbers, rel.Number)
		}
	}
	for i, svc := range pending {
		bc.wait(b, svc, numbers[i])
	}
	svc := ServiceRef{"big", "production", "svc-00"}
	for i := 1; i < benchReleases; i++ {
		rel, err := c.DeployImage(ctx, System, svc, apitypes.DeployImageRequest{Image: fmt.Sprintf("app:%d", i+1)})
		if err != nil {
			b.Fatal(err)
		}
		bc.wait(b, svc, rel.Number)
	}
	p, err := c.loadProject(ctx, "big")
	if err != nil {
		b.Fatal(err)
	}
	envs, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		b.Fatal(err)
	}
	for i := range benchAudit {
		e := envs[i%len(envs)]
		c.audit(ctx, System, "variable.set", &p, &e, fmt.Sprintf("big/%s/svc-%02d", e.Name, i%benchServices), "set PAD")
	}
}

// wait polls until a release finishes deploying.
func (bc *benchCore) wait(b *testing.B, ref ServiceRef, number int64) {
	b.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rel, err := bc.c.GetRelease(bc.ctx, System, ref, number)
		if err != nil {
			b.Fatal(err)
		}
		if rel.Status.Done() {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("release %d of %s stuck in %s", number, ref, rel.Status)
		}
		time.Sleep(time.Millisecond)
	}
}

func BenchmarkCoreReads(b *testing.B) {
	bc := newBenchCore(b)
	ctx, c := bc.ctx, bc.c
	svc := ServiceRef{"big", "production", "svc-00"}
	prod := EnvRef{"big", "production"}

	run := func(name string, fn func() error) {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := fn(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	run("Overview/services=60", func() error {
		_, err := c.Overview(ctx, System, "big")
		return err
	})
	// Eight clients at once, as the API latency budget assumes: shows
	// contention (connection pool, SQLite, GC) that one client doesn't.
	b.Run("Overview/services=60,parallel=8", func(b *testing.B) {
		b.SetParallelism(max(1, 8/runtime.GOMAXPROCS(0)))
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := c.Overview(ctx, System, "big"); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
	// A staged variable makes the overview compute each environment's
	// pending changes, which resolves every service's variables.
	b.Run("Overview/services=60,staged", func(b *testing.B) {
		if _, err := c.SetVariables(ctx, System, prod, apitypes.SetVariablesRequest{Set: map[string]string{"REGION": "eu-west"}}); err != nil {
			b.Fatal(err)
		}
		defer func() {
			if _, err := c.DiscardChanges(ctx, System, prod, apitypes.DiscardChangesRequest{}); err != nil {
				b.Fatal(err)
			}
		}()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.Overview(ctx, System, "big"); err != nil {
				b.Fatal(err)
			}
		}
	})
	run("ListServices/services=20", func() error {
		_, err := c.ListServices(ctx, System, prod)
		return err
	})
	run("ListReleases/limit=20", func() error {
		_, err := c.ListReleases(ctx, System, svc, 20)
		return err
	})
	run("ListReleases/limit=100", func() error {
		_, err := c.ListReleases(ctx, System, svc, 100)
		return err
	})
	run("GetRelease", func() error {
		_, err := c.GetRelease(ctx, System, svc, benchReleases/2)
		return err
	})
	run("ListAudit/all,limit=100", func() error {
		_, err := c.ListAudit(ctx, System, AuditFilter{Limit: 100})
		return err
	})
	run("ListAudit/project,limit=100", func() error {
		_, err := c.ListAudit(ctx, System, AuditFilter{Project: "big", Limit: 100})
		return err
	})
	run("ListAudit/service,limit=100", func() error {
		_, err := c.ListAudit(ctx, System, AuditFilter{Project: "big", Environment: "production", Service: "svc-00", Limit: 100})
		return err
	})
	run("ListVariables/services=20", func() error {
		_, err := c.ListVariables(ctx, System, prod, "", false)
		return err
	})
	es, err := c.loadEnv(ctx, prod)
	if err != nil {
		b.Fatal(err)
	}
	// What a config release and the pending-changes diff run per environment.
	run("ResolveVariables/services=20", func() error {
		_, broken, err := c.resolveEnvs(ctx, c.store.Queries, es, false)
		if len(broken) > 0 {
			return fmt.Errorf("broken references: %v", broken)
		}
		return err
	})
}

// BenchmarkBusFanOut publishes one release event to n subscribers, each of
// which takes it and encodes it, as the SSE handler does. JSON is encoded
// once per event however many subscribers there are.
func BenchmarkBusFanOut(b *testing.B) {
	rel := &apitypes.Release{
		ID: 42, Number: 42, Project: "big", Environment: "production", Service: "svc-00",
		Status: apitypes.ReleaseActive, Source: apitypes.SourceImage, ImageRef: "registry.local/big/svc-00:42",
		ImageID: "sha256:4be1c0ffee", CreatedBy: "deploy-token",
		CreatedAt: time.Unix(1_800_000_000, 0).UTC(),
	}
	ev := busEvent{projectID: 1, envID: 1, serviceID: 1, ev: apitypes.Event{
		Type: apitypes.EventRelease, Project: "big", Environment: "production", Service: "svc-00", Release: rel,
	}}
	for _, n := range []int{1, 20, 100} {
		b.Run(fmt.Sprintf("subscribers=%d", n), func(b *testing.B) {
			bus := newBus()
			subs := make([]*subscription, n)
			for i := range subs {
				subs[i] = bus.subscribe(func(busEvent) bool { return true })
				defer subs[i].close()
			}
			b.ReportAllocs()
			for b.Loop() {
				bus.publish(ev)
				for _, s := range subs {
					for _, live := range s.drain(<-s.ch) {
						if _, err := live.JSON(); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
