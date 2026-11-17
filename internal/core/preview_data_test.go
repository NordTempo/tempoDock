package core

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/dbcopy"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// copyJobs answers the preview copies' jobs as the scripts would: the
// dump prints its numbers, unless answer says otherwise, and every job is
// recorded.
type copyJobs struct {
	mu   sync.Mutex
	jobs []node.JobSpec
	// answer, when set, answers a job instead (nil: as usual).
	answer func(node.JobSpec) *node.JobResult
}

// copyHarness is previewHarness with staging's db copied into previews
// as data says, two of its columns marked sensitive, and the copy jobs
// answered.
func copyHarness(t *testing.T, data apitypes.PreviewData) (*harness, string, *copyJobs) {
	h, _, _, appHook := previewHarness(t)
	db := ServiceRef{"shop", "staging", "db"}
	for _, col := range []string{"email", "phone"} {
		if err := h.store.AddSensitiveColumn(h.ctx, store.AddSensitiveColumnParams{ServiceID: h.serviceID(db), SchemaName: "public", TableName: "users", ColumnName: col}); err != nil {
			t.Fatal(err)
		}
	}
	h.servicePreviews(db, apitypes.SetServicePreviewsRequest{Data: data})
	cj := &copyJobs{}
	dump, _ := dbcopy.DumpScript(dbcopy.Postgres)
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		cj.mu.Lock()
		cj.jobs = append(cj.jobs, spec)
		answer := cj.answer
		cj.mu.Unlock()
		if answer != nil {
			if res := answer(spec); res != nil {
				return res
			}
		}
		if spec.Script == dump {
			return &node.JobResult{Output: "dock-copy: bytes=44040192 tables=1 columns=2\n"}
		}
		return nil
	})
	return h, appHook, cj
}

// named returns the recorded jobs whose name contains part.
func (cj *copyJobs) named(part string) []node.JobSpec {
	cj.mu.Lock()
	defer cj.mu.Unlock()
	var out []node.JobSpec
	for _, j := range cj.jobs {
		if strings.Contains(j.Name, part) {
			out = append(out, j)
		}
	}
	return out
}

// waitData waits until env's preview has its one database's copy
// finished, and returns it.
func (h *harness) waitData(env string) apitypes.PreviewDatabase {
	h.t.Helper()
	var d apitypes.PreviewDatabase
	eventually(h.t, env+"'s data copied", func() bool {
		pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", env})
		if err != nil || len(pv.Databases) != 1 {
			return false
		}
		d = pv.Databases[0]
		return d.Status != apitypes.PreviewDataCopying
	})
	return d
}

// A masked copy reads staging's live database on staging's network, in
// the template's image, with the marked columns to mask; the preview's
// database gets new credentials and is deployed before the copy loads
// into it on the preview's network, and the pull request's code builds
// only after. The preview's Data panel masks the same columns.
func TestPreviewMaskedCopy(t *testing.T) {
	h, appHook, cj := copyHarness(t, apitypes.PreviewDataMasked)
	if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 7, sha: shaA, updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	d := h.waitData("pr-7")
	if d.Status != apitypes.PreviewDataCopied || d.Mode != apitypes.PreviewDataMasked || d.From != "staging/db" || d.MaskedColumns != 2 || d.Bytes != 44040192 {
		t.Fatalf("data %+v", d)
	}
	base, db := ServiceRef{"shop", "staging", "db"}, ServiceRef{"shop", "pr-7", "db"}
	dumps, loads := cj.named("_copy_dump_"), cj.named("_copy_load_")
	if len(dumps) != 1 || len(loads) != 1 {
		t.Fatalf("dumps %d, loads %d", len(dumps), len(loads))
	}
	dump, load := dumps[0], loads[0]
	t.Run("dump", func(t *testing.T) {
		staging, _ := h.c.envByID(h.ctx, h.envID("staging"))
		if dump.Image != templates[apitypes.TemplatePostgres].image || dump.Network != networkName(staging) {
			t.Errorf("dump in %s on %s, want the template's image on staging's network", dump.Image, dump.Network)
		}
		if dump.Env["PGHOST"] != "db" || dump.Env["PGPASSWORD"] != h.revealed(base)["POSTGRES_PASSWORD"] {
			t.Errorf("dump reaches %s, want staging's db as its owner", dump.Env["PGHOST"])
		}
		if !strings.Contains(dump.Env["DOCK_MARKS"], `"c":"email"`) || !strings.Contains(dump.Env["DOCK_MARKS"], `"c":"phone"`) {
			t.Errorf("marks %s, want email and phone", dump.Env["DOCK_MARKS"])
		}
	})
	t.Run("load", func(t *testing.T) {
		pr7, _ := h.c.envByID(h.ctx, h.envID("pr-7"))
		if load.Network != networkName(pr7) || load.Env["PGPASSWORD"] != h.revealed(db)["POSTGRES_PASSWORD"] {
			t.Errorf("load on %s, want the preview's network as its db's owner", load.Network)
		}
		if h.revealed(db)["POSTGRES_PASSWORD"] == h.revealed(base)["POSTGRES_PASSWORD"] {
			t.Error("the preview's database has staging's password")
		}
		if len(load.Mounts) != 1 || load.Mounts[0] != dump.Mounts[0] {
			t.Errorf("load mounts %+v, want the dump's scratch volume", load.Mounts)
		}
	})
	ops := h.rt.Ops()
	start, loaded := indexOf(ops, "start "+h.ctr(db, 1)), indexOf(ops, "job "+load.Name)
	if start < 0 || loaded < start {
		t.Errorf("db started at %d, loaded at %d: %v", start, loaded, ops)
	}
	if i := indexOf(ops, "remove volume "+dump.Mounts[0].Volume); i < loaded {
		t.Errorf("the scratch volume was removed at %d, before the load at %d", i, loaded)
	}
	// Everything else waits for the copy.
	api := ServiceRef{"shop", "pr-7", "api"}
	h.wait(api, h.waitReleases(api, 1)[0].Number)
	if i := indexOf(h.rt.Ops(), "start "+h.ctr(api, 1)); i < loaded {
		t.Errorf("api started at %d, before the copy loaded at %d", i, loaded)
	}
	marks, err := h.store.ListSensitiveColumns(h.ctx, h.serviceID(db))
	if err != nil || len(marks) != 2 {
		t.Errorf("the preview's db has marks %+v, %v; want staging's two", marks, err)
	}
	rel := h.waitReleases(db, 1)[0]
	r, err := h.c.GetRelease(h.ctx, testSystem, db, rel.Number)
	if err != nil || !slices.ContainsFunc(r.Events, func(e apitypes.ReleaseEvent) bool {
		return strings.Contains(e.Message, "masked copy of shop/staging/db")
	}) {
		t.Errorf("release events %+v, %v; want the copy", r.Events, err)
	}
}

// A pull request from a fork starts empty unless the database gives
// forks the data: nothing of staging is read.
func TestPreviewCopyFork(t *testing.T) {
	h, appHook, cj := copyHarness(t, apitypes.PreviewDataMasked)
	h.servicePreviews(ServiceRef{"shop", "staging", "web"}, apitypes.SetServicePreviewsRequest{Build: true, AllowForks: true})
	if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 9, sha: shaA, updated: time.Now(), fork: true}); err != nil {
		t.Fatal(err)
	}
	d := h.waitData("pr-9")
	if d.Status != apitypes.PreviewDataStartedEmpty || !strings.Contains(d.Message, "forks") {
		t.Fatalf("data %+v, want empty for a fork", d)
	}
	if len(cj.named("_copy_")) != 0 {
		t.Error("a fork's preview read staging's database")
	}
	db := ServiceRef{"shop", "pr-9", "db"}
	if r := h.wait(db, h.waitReleases(db, 1)[0].Number); r.Status != apitypes.ReleaseActive {
		t.Errorf("db release %+v, want it deployed empty", r)
	}
}

// Whatever fails, the database starts empty and running: past the size
// limit nothing is loaded; a load that fails partway is emptied again.
func TestPreviewCopyFailsClosed(t *testing.T) {
	t.Run("too big", func(t *testing.T) {
		h, appHook, cj := copyHarness(t, apitypes.PreviewDataMasked)
		cj.answer = func(spec node.JobSpec) *node.JobResult {
			if strings.Contains(spec.Name, "_copy_dump_") {
				return &node.JobResult{ExitCode: dbcopy.ExitTooBig, Output: "dock: the database is 3000 MB, past the 2048 MB previews copy ([previews] copy_max_mb)\n"}
			}
			return nil
		}
		if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 7, sha: shaA, updated: time.Now()}); err != nil {
			t.Fatal(err)
		}
		d := h.waitData("pr-7")
		if d.Status != apitypes.PreviewDataStartedEmpty || !strings.Contains(d.Message, "past the 2048 MB") {
			t.Fatalf("data %+v", d)
		}
		if len(cj.named("_copy_load_")) != 0 || len(cj.named("_wipe_")) != 0 {
			t.Error("loaded or emptied a database nothing was copied into")
		}
		db := ServiceRef{"shop", "pr-7", "db"}
		if r := h.wait(db, h.waitReleases(db, 1)[0].Number); r.Status != apitypes.ReleaseActive {
			t.Errorf("db release %+v", r)
		}
	})
	t.Run("load fails", func(t *testing.T) {
		h, appHook, cj := copyHarness(t, apitypes.PreviewDataCopy)
		cj.answer = func(spec node.JobSpec) *node.JobResult {
			if strings.Contains(spec.Name, "_copy_load_") {
				return &node.JobResult{ExitCode: 1, Output: "pg_restore: error: out of disk\n"}
			}
			return nil
		}
		if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 7, sha: shaA, updated: time.Now()}); err != nil {
			t.Fatal(err)
		}
		d := h.waitData("pr-7")
		if d.Status != apitypes.PreviewDataStartedEmpty || !strings.Contains(d.Message, "out of disk") {
			t.Fatalf("data %+v", d)
		}
		wipes := cj.named("_wipe_")
		if len(wipes) != 1 {
			t.Fatalf("wipes %d, want the partial copy emptied", len(wipes))
		}
		db := ServiceRef{"shop", "pr-7", "db"}
		ops := h.rt.Ops()
		wiped, started := indexOf(ops, "job "+wipes[0].Name), slices.IndexFunc(ops, func(op string) bool { return op == "start "+h.ctr(db, 1) })
		if again := slices.Index(ops[wiped+1:], "start "+h.ctr(db, 1)); wiped < started || again < 0 {
			t.Errorf("not started again on the emptied volume: %v", ops)
		}
	})
}

// An unmasked copy, or any data for forks, is for whoever may read the
// sensitive columns; copies are for template databases.
func TestPreviewDataSettings(t *testing.T) {
	h, _, _ := copyHarness(t, apitypes.PreviewDataMasked)
	db := ServiceRef{"shop", "staging", "db"}
	sp, err := h.c.GetServicePreviews(h.ctx, testSystem, db)
	if err != nil || sp.Data != apitypes.PreviewDataMasked || sp.Masked == nil || sp.Masked.Columns != 2 || sp.Masked.Tables != 1 || sp.CopyMaxMB != defaultCopyMaxMB {
		t.Fatalf("settings %+v %+v, %v", sp, sp.Masked, err)
	}
	// A project manager who sees sensitive columns masked may hand pull
	// requests only a masked copy.
	h.enforce()
	mgr := grantedPerson(h, h.user("ada@example.com", apitypes.ScopeAdmin), "mgr@example.com",
		apitypes.GrantRequest{Role: "projects:edit"}, apitypes.GrantRequest{Role: "deploy:view"})
	if _, err := h.c.SetServicePreviews(h.ctx, mgr, db, apitypes.SetServicePreviewsRequest{Data: apitypes.PreviewDataCopy}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a manager seeing masked columns set an unmasked copy: %v", err)
	}
	if _, err := h.c.SetServicePreviews(h.ctx, mgr, db, apitypes.SetServicePreviewsRequest{Data: apitypes.PreviewDataMasked, DataForks: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a manager seeing masked columns gave forks data: %v", err)
	}
	if _, err := h.c.SetServicePreviews(h.ctx, mgr, db, apitypes.SetServicePreviewsRequest{Data: apitypes.PreviewDataMasked}); err != nil {
		t.Errorf("a manager set a masked copy: %v", err)
	}
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, ServiceRef{"shop", "staging", "api"}, apitypes.SetServicePreviewsRequest{Data: apitypes.PreviewDataMasked}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a copy of api, which is no database: %v", err)
	}
}

// Refreshing copies the base again into the preview's database, emptied
// first, then restarts the preview's other services; the base is only
// read.
func TestRefreshPreviewData(t *testing.T) {
	h, appHook, cj := copyHarness(t, apitypes.PreviewDataMasked)
	if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 7, sha: shaA, updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h.waitData("pr-7")
	api := ServiceRef{"shop", "pr-7", "api"}
	h.waitReleases(api, 1)
	h.settled("pr-7")
	before := h.envSnapshot("staging")
	deployer := Actor{Kind: "token", OrgID: 1, Name: "deploy", Scope: apitypes.ScopeDeploy}
	if _, err := h.c.RefreshPreviewData(h.ctx, deployer, EnvRef{"shop", "pr-7"}, apitypes.RefreshPreviewDataRequest{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a second load", func() bool { return len(cj.named("_copy_load_")) == 2 })
	if d := h.waitData("pr-7"); d.Status != apitypes.PreviewDataCopied {
		t.Fatalf("data %+v", d)
	}
	if len(cj.named("_wipe_")) != 1 {
		t.Error("the preview's database wasn't emptied before the new copy")
	}
	h.waitReleases(api, 2)
	if after := h.envSnapshot("staging"); after != before {
		t.Errorf("the base was touched: before\n%s\nafter\n%s", before, after)
	}
	if _, err := h.c.RefreshPreviewData(h.ctx, testSystem, EnvRef{"shop", "staging"}, apitypes.RefreshPreviewDataRequest{}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("refreshed staging: %v", err)
	}
	reader := Actor{Kind: "token", OrgID: 1, Name: "read", Scope: apitypes.ScopeRead}
	if _, err := h.c.RefreshPreviewData(h.ctx, reader, EnvRef{"shop", "pr-7"}, apitypes.RefreshPreviewDataRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a reader refreshed: %v", err)
	}
}
