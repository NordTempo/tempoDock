//go:build integration

package core

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// ADR 0069 with real Docker: a Postgres template database starts with
// pg_stat_statements loaded and Dock creates the extension once its
// release is active, so the queries view works without anyone asking;
// a cancel reaches the app's query through Dock's own dial path, as the
// owner.
func TestIntegrationTemplateQueryStats(t *testing.T) {
	t.Parallel()
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()

	project := fmt.Sprintf("qs-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, EnvRef{project, "production"}, apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	var ins apitypes.DataInsights
	for start := time.Now(); ; time.Sleep(300 * time.Millisecond) {
		var err error
		ins, err = c.DataInsights(ctx, testSystem, db, apitypes.DataInsightsRequest{})
		if err == nil && ins.Overview != nil && ins.Overview.QueryStats == apitypes.QueryStatsOn {
			break
		}
		if time.Since(start) > 60*time.Second {
			t.Fatalf("query stats of a new template database: %+v %v", ins.Overview, err)
		}
	}

	// An app's slow query, as the superuser the template's apps use.
	ss, err := c.serviceFor(ctx, testSystem, db, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	slept := make(chan string, 1)
	go func() {
		out, _ := exec.Command("docker", "exec", rel.ContainerID, "psql", "-U", "postgres", "-d", "app", "-c", "SELECT pg_sleep(60)").CombinedOutput()
		slept <- string(out)
	}()
	var sleeper *apitypes.DataSession
	for start := time.Now(); sleeper == nil; time.Sleep(200 * time.Millisecond) {
		ins, err = c.DataInsights(ctx, testSystem, db, apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightSessions}})
		if err != nil {
			t.Fatal(err)
		}
		for i, s := range ins.Sessions {
			if strings.Contains(s.Text, "pg_sleep") {
				sleeper = &ins.Sessions[i]
			}
		}
		if time.Since(start) > 30*time.Second {
			t.Fatalf("no sleeping session in %+v", ins.Sessions)
		}
	}
	// Production: a cancel needs no confirm, ending a session does.
	if _, err := c.CancelDataSession(ctx, testSystem, db, sleeper.ID, apitypes.DataCancelRequest{Terminate: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("ending a production session without confirm: %v", err)
	}
	res, err := c.CancelDataSession(ctx, testSystem, db, sleeper.ID, apitypes.DataCancelRequest{QueryStartedAt: sleeper.QueryAt})
	if err != nil || res.User != "postgres" {
		t.Fatalf("cancel: %+v %v", res, err)
	}
	select {
	case out := <-slept:
		if !strings.Contains(out, "canceling statement") {
			t.Fatalf("the app's query: %s", out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the app's query wasn't cancelled")
	}

	ins, err = c.DataInsights(ctx, testSystem, db, apitypes.DataInsightsRequest{Views: []apitypes.DataInsightView{apitypes.InsightQueries}})
	if err != nil || len(ins.Unavailable) > 0 {
		t.Fatalf("queries: %+v %v", ins.Unavailable, err)
	}
	if err := c.ResetDataQueryStats(ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
}
