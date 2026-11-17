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
	"github.com/NordTempo/tempoDock/internal/store"
)

// A preview's Postgres starts with a masked copy of its base's on real
// Docker: the dump job on the base's network, the load job on the
// preview's, through a scratch volume that is gone afterwards. The marked
// columns are masked in the preview, the rest copied. A refresh with an
// unmasked copy empties the database, then copies the real values.
func TestIntegrationPreviewDataCopy(t *testing.T) {
	t.Parallel()
	var install string
	t.Cleanup(func() { removeInstallVolumes(t, install) })
	c := dockerHarness(t)
	install = c.install
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, testSystem, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	base := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, base.Env(), apitypes.CreateServiceRequest{Name: base.Service, Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, base, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	psql := func(ref ServiceRef, sql string) string {
		t.Helper()
		return execIn(t, c, ref, "psql", "-U", "postgres", "-d", "app", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
	}
	psql(base, `CREATE TABLE users (id serial PRIMARY KEY, email text NOT NULL UNIQUE, phone text, name text NOT NULL);
		INSERT INTO users (email, phone, name) VALUES ('ada@example.com', '555-1', 'Ada'), ('bob@example.com', NULL, 'Bob')`)
	from, err := c.serviceFor(ctx, testSystem, base, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"email", "phone"} {
		if err := c.store.AddSensitiveColumn(ctx, store.AddSensitiveColumnParams{ServiceID: from.svc.ID, SchemaName: "public", TableName: "users", ColumnName: col}); err != nil {
			t.Fatal(err)
		}
	}

	// A preview environment cloned from production, as createPreview
	// makes one, without the GitHub side.
	if _, err := c.CloneEnvironment(ctx, testSystem, base.Env(), apitypes.CloneEnvironmentRequest{Name: "pr-1"}); err != nil {
		t.Fatal(err)
	}
	dst, err := c.envFor(ctx, testSystem, EnvRef{project, "pr-1"}, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := c.store.CreatePreview(ctx, store.CreatePreviewParams{
		ProjectID: dst.project.ID, EnvironmentID: dst.env.ID, BaseEnvironmentID: from.env.ID, Repo: "acme/shop", Number: 1,
		HeadBranch: "fix", HeadSha: strings.Repeat("a", 40), PrUpdatedAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	copyInto := func(mode apitypes.PreviewData, refresh bool) apitypes.PreviewDatabase {
		t.Helper()
		plan := previewDataPlan{from: from, set: previewDataSetting{mode: mode}}
		c.beginPreviewData(ctx, dst, pv.ID, plan, "test")
		c.fillPreviewDB(ctx, testSystem, dst, pv, plan, refresh)
		got, err := c.GetPreview(ctx, testSystem, EnvRef{project, "pr-1"})
		if err != nil || len(got.Databases) != 1 {
			t.Fatalf("preview %+v, %v", got, err)
		}
		return got.Databases[0]
	}
	preview := ServiceRef{project, "pr-1", "db"}

	d := copyInto(apitypes.PreviewDataMasked, false)
	if d.Status != apitypes.PreviewDataCopied || d.MaskedColumns != 2 || d.MaskedTables != 1 {
		t.Fatalf("masked copy: %+v", d)
	}
	rows := psql(preview, "SELECT email || '|' || coalesce(phone, '<null>') || '|' || name FROM users ORDER BY id")
	if strings.Contains(rows, "example.com") || strings.Contains(rows, "555-1") || strings.Count(rows, "masked-") != 2 ||
		!strings.Contains(rows, "|<null>|Ada") || !strings.Contains(rows, "|<null>|Bob") {
		t.Errorf("masked preview rows:\n%s", rows)
	}
	if liveVar(t, c, preview, "POSTGRES_PASSWORD") == liveVar(t, c, base, "POSTGRES_PASSWORD") {
		t.Error("the preview's database has the base's password")
	}

	d = copyInto(apitypes.PreviewDataCopy, true)
	if d.Status != apitypes.PreviewDataCopied || d.MaskedColumns != 0 {
		t.Fatalf("refreshed copy: %+v", d)
	}
	if rows := psql(preview, "SELECT email FROM users ORDER BY id"); rows != "ada@example.com\nbob@example.com" {
		t.Errorf("unmasked preview rows after the refresh:\n%s", rows)
	}
	if rows := psql(base, "SELECT count(*) FROM users"); rows != "2" {
		t.Errorf("the base has %s users", rows)
	}
	to, err := c.serviceFor(ctx, testSystem, preview, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	scratch := (&previewCopy{to: to}).scratch()
	if out, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+scratch).Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("the scratch volume %s is left", scratch)
	}
}
