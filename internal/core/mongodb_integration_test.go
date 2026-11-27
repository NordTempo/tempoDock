//go:build integration

package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The MongoDB template on real Docker (ADR 0110): its root user and
// MONGODB_URI work, a backup restores into a new service with its own
// password and the data, a restore drill passes and leaves nothing
// behind, and a preview's database starts with a copy of the base's.
func TestIntegrationMongoDB(t *testing.T) {
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
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{Name: db.Service, Template: apitypes.TemplateMongoDB}); err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, 1); r.Status != apitypes.ReleaseActive {
		t.Fatalf("db: %s %s", r.Status, r.Error)
	}
	// mongosh as the root user, with the URI an app gets.
	// mongosh with MONGODB_URI as the container has it: references
	// resolved, as an app gets it.
	mongosh := func(ref ServiceRef, js string) string {
		t.Helper()
		return execIn(t, c, ref, "sh", "-c", `exec mongosh --quiet "$MONGODB_URI" --eval "$0"`, js)
	}
	mongosh(db, `db.notes.insertMany([{title: "hello"}, {title: "world"}]); db.getSiblingDB("other").things.insertOne({n: 1})`)

	// The Data panel is set up once it runs (afterActive) and reads the
	// collections through Dock's own users.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(500 * time.Millisecond) {
		s, err := c.DataSchema(ctx, testSystem, db, "")
		if err == nil && userTables(s) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Data panel's schema: %+v, %v", s, err)
		}
	}
	page, err := c.DataRows(ctx, testSystem, db, apitypes.DataPageRequest{Table: "app.notes", Order: "title"})
	if err != nil || len(page.Rows) != 2 || !strings.Contains(string(page.Rows[0]), "hello") {
		t.Fatalf("a page of notes: %+v, %v", page, err)
	}

	run, err := c.RunBackup(ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	done := waitBackupRun(t, c, db, run.ID)
	if done.Status != apitypes.BackupSucceeded || done.Kind != apitypes.BackupMongoDB || done.SizeBytes == 0 {
		t.Fatalf("backup: %+v", done)
	}
	res, err := c.RestoreBackup(ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: done.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitBackupRun(t, c, db, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore: %+v", r)
	}
	restored := ServiceRef{project, "production", "db-restored"}
	if liveVar(t, c, restored, "MONGO_INITDB_ROOT_PASSWORD") == liveVar(t, c, db, "MONGO_INITDB_ROOT_PASSWORD") {
		t.Error("the restored MongoDB reuses the live password")
	}
	if got := mongosh(restored, `db.notes.countDocuments() + " " + db.getSiblingDB("other").things.countDocuments()`); got != "2 1" {
		t.Errorf("restored MongoDB holds %q documents", got)
	}

	if _, err := c.RunBackupDrill(ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		b, err := c.ServiceBackups(ctx, testSystem, db)
		if err != nil {
			t.Fatal(err)
		}
		if d := b.Drill; d != nil && d.Status != apitypes.BackupRunning {
			if d.Status != apitypes.BackupSucceeded || !strings.HasPrefix(d.Detail, "2 collections in ") {
				t.Fatalf("drill: %+v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the drill still running")
		}
	}

	// A preview copies the base's databases (masked: nothing is marked, so
	// it is the same copy).
	if _, err := c.CloneEnvironment(ctx, testSystem, db.Env(), apitypes.CloneEnvironmentRequest{Name: "pr-1"}); err != nil {
		t.Fatal(err)
	}
	from, err := c.serviceFor(ctx, testSystem, db, apitypes.ScopeRead, "testing")
	if err != nil {
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
	plan := previewDataPlan{from: from, set: previewDataSetting{mode: apitypes.PreviewDataMasked}}
	c.beginPreviewData(ctx, dst, pv.ID, plan, "test")
	c.fillPreviewDB(ctx, testSystem, dst, pv, plan, false)
	got, err := c.GetPreview(ctx, testSystem, EnvRef{project, "pr-1"})
	if err != nil || len(got.Databases) != 1 || got.Databases[0].Status != apitypes.PreviewDataCopied {
		t.Fatalf("preview %+v, %v", got, err)
	}
	preview := ServiceRef{project, "pr-1", "db"}
	if got := mongosh(preview, `db.notes.find().sort({title: 1}).toArray().map(d => d.title).join(",")`); got != "hello,world" {
		t.Errorf("the preview's notes: %q", got)
	}
}
