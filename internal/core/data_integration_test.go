//go:build integration

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The database panel against a real Postgres. Most tests run on the fake
// runtime, whose Dial goes to a postgres:17-alpine container started per
// test on a loopback port; TestIntegrationDataRealDial deploys Postgres on
// the real Docker runtime and dials it on its environment network.

const ownerPassword = "owner-pw"

// startPostgres runs a Postgres container for one test and returns its
// loopback address and a function running SQL in it as the owner.
func startPostgres(t *testing.T) (string, func(sql string)) {
	t.Helper()
	name := fmt.Sprintf("dock-coretest-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_PASSWORD="+ownerPassword, "-p", "127.0.0.1::5432", "postgres:17-alpine").CombinedOutput()
	if err != nil {
		t.Fatalf("start postgres: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	out, err = exec.Command("docker", "port", name, "5432/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	// The image's init runs a server without TCP first; wait for the real one.
	for start := time.Now(); exec.Command("docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run() != nil; time.Sleep(200 * time.Millisecond) {
		if time.Since(start) > time.Minute {
			t.Fatal("postgres didn't start")
		}
	}
	psql := func(sql string) {
		t.Helper()
		if out, err := exec.Command("docker", "exec", name, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput(); err != nil {
			t.Fatalf("psql: %v: %s", err, out)
		}
	}
	return addr, psql
}

const dataSeed = `
CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL, email text, password_hash text);
CREATE TABLE orders (id int PRIMARY KEY, customer_id int REFERENCES customers (id), total numeric(10,2), status text);
INSERT INTO customers SELECT g, 'c' || g, 'c' || g || '@x.io', 'hash' || g FROM generate_series(1, 3) g;
INSERT INTO orders SELECT g, 1 + g % 3, g * 10, 'new' FROM generate_series(1, 6) g;
ANALYZE;`

// dataHarness is a harness with Postgres deployed as service db in
// environment env (created as non-production unless it is production).
func dataHarness(t *testing.T, env string) (*harness, ServiceRef, func(string)) {
	t.Helper()
	h := newHarness(t)
	addr, psql := startPostgres(t)
	psql(dataSeed)
	h.rt.DialTo(addr)
	if _, err := h.c.CreateProject(h.ctx, System, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if env != "production" {
		if _, err := h.c.CreateEnvironment(h.ctx, System, "shop", apitypes.CreateEnvironmentRequest{Name: env}); err != nil {
			t.Fatal(err)
		}
	}
	db := ServiceRef{"shop", env, "db"}
	if _, err := h.c.CreateService(h.ctx, System, db.Env(), apitypes.CreateServiceRequest{Name: "db", Image: "postgres:17-alpine"}); err != nil {
		t.Fatal(err)
	}
	h.setVars(db, map[string]string{"POSTGRES_PASSWORD": ownerPassword})
	h.deploy(db, "postgres:17-alpine")
	return h, db, psql
}

// query runs SQL through the panel and collects the frames.
func (h *harness) query(a Actor, ref ServiceRef, sql string) ([]apitypes.DataQueryFrame, error) {
	h.t.Helper()
	stream, err := h.c.DataQuery(h.ctx, a, ref, apitypes.DataQueryRequest{SQL: sql})
	if err != nil {
		return nil, err
	}
	var frames []apitypes.DataQueryFrame
	err = stream(h.ctx, func() {}, func(fs ...apitypes.DataQueryFrame) error {
		for _, f := range fs {
			// The engine reuses its buffers once send returns.
			b, _ := json.Marshal(f)
			var c apitypes.DataQueryFrame
			_ = json.Unmarshal(b, &c)
			frames = append(frames, c)
		}
		return nil
	})
	return frames, err
}

func (h *harness) auditCount(action string) int {
	h.t.Helper()
	evs, err := h.c.ListAudit(h.ctx, System, AuditFilter{Action: action})
	if err != nil {
		h.t.Fatal(err)
	}
	return len(evs)
}

func TestIntegrationDataReads(t *testing.T) {
	h, db, psql := dataHarness(t, "staging")
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	viewer := h.user("viewer@x", apitypes.ScopeRead)

	if info, err := h.c.DataInfo(h.ctx, admin, db); err != nil || info.Status != apitypes.DataSetupNeeded || !info.Access.Manage {
		t.Fatalf("before setup: %+v %v", info, err)
	}
	if _, err := h.c.DataSchema(h.ctx, dev, db); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "dock db setup") {
		t.Fatalf("schema before setup: %v", err)
	}
	info, err := h.c.SetupData(h.ctx, admin, db)
	if err != nil || info.Status != apitypes.DataReady || !strings.HasPrefix(info.ServerVersion, "17") || info.SetUpBy != "admin@x" {
		t.Fatalf("setup: %+v %v", info, err)
	}
	if info, err := h.c.DataInfo(h.ctx, dev, db); err != nil || info.Status != apitypes.DataReady || info.Access.Level != apitypes.LevelRead {
		t.Fatalf("dev info: %+v %v", info, err)
	}
	if h.auditCount(apitypes.AuditDataSetup) != 1 {
		t.Fatal("setup not audited")
	}

	if _, err := h.c.SetSensitiveColumns(h.ctx, dev, db, apitypes.SetSensitiveColumnsRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("dev marks columns: %v", err)
	}
	cols, err := h.c.SetSensitiveColumns(h.ctx, admin, db, apitypes.SetSensitiveColumnsRequest{Columns: []apitypes.DataColumnRef{{Schema: "public", Table: "customers", Column: "password_hash"}}})
	if err != nil || len(cols) != 1 {
		t.Fatalf("mark: %+v %v", cols, err)
	}
	schema, err := h.c.DataSchema(h.ctx, dev, db)
	if err != nil || len(schema.Tables) != 2 || !schema.Tables[0].Columns[3].Sensitive {
		t.Fatalf("schema: %+v %v", schema, err)
	}

	page, err := h.c.DataRows(h.ctx, dev, db, apitypes.DataPageRequest{Table: "public.customers", Limit: 1})
	if err != nil || string(page.Rows[0]) != `[1,"c1","c1@x.io",{"masked":true}]` || page.Next == "" {
		t.Fatalf("masked page: %+v %v", page, err)
	}
	if _, err := h.c.DataRows(h.ctx, dev, db, apitypes.DataPageRequest{Table: "public.customers", After: page.Next, Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if page, _ := h.c.DataRows(h.ctx, admin, db, apitypes.DataPageRequest{Table: "public.customers", Limit: 1}); string(page.Rows[0]) != `[1,"c1","c1@x.io","hash1"]` {
		t.Fatalf("admin page: %s", page.Rows[0])
	}
	if n := h.auditCount(apitypes.AuditDataBrowse); n != 2 {
		t.Fatalf("browse events = %d, want one per actor and table", n)
	}
	refs, err := h.c.DataReferences(h.ctx, dev, db, apitypes.DataReferencesRequest{Table: "public.customers", Key: map[string]string{"id": "1"}})
	if err != nil || len(refs) != 1 || refs[0].Table != "orders" || len(refs[0].Page.Rows) != 2 {
		t.Fatalf("references: %+v %v", refs, err)
	}
	if _, err := h.c.DataRows(h.ctx, viewer, db, apitypes.DataPageRequest{Table: "public.customers"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read user rows: %v", err)
	}
	if _, err := h.c.DataRows(h.ctx, dev, db, apitypes.DataPageRequest{Table: "public.nope"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("unknown table: %v", err)
	}

	// Masked SQL runs as dock_reader_masked: the sensitive column is denied.
	frames, err := h.query(dev, db, "SELECT id, email FROM customers ORDER BY id")
	if err != nil || frames[len(frames)-1].Type != apitypes.FrameEnd || frames[len(frames)-1].RowCount != 3 {
		t.Fatalf("masked query: %+v %v", frames, err)
	}
	frames, err = h.query(dev, db, "SELECT password_hash FROM customers")
	if last := frames[len(frames)-1]; err != nil || last.Type != apitypes.FrameError || last.Error.SQLState != "42501" {
		t.Fatalf("masked query of a sensitive column: %+v %v", frames, err)
	}
	frames, _ = h.query(admin, db, "SELECT password_hash FROM customers ORDER BY id LIMIT 1")
	if len(frames) != 3 || string(frames[1].Rows[0]) != `["hash1"]` {
		t.Fatalf("admin query: %+v", frames)
	}
	if _, err := h.query(dev, db, "SELECT 1; SELECT 2"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("two statements: %v", err)
	}
	if _, err := h.query(viewer, db, "SELECT 1"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read user query: %v", err)
	}
	// A table created after the last sync: once the schema is read again,
	// the masked reader is synced again.
	psql("CREATE TABLE notes (id int PRIMARY KEY, body text)")
	ss, _ := h.c.loadService(h.ctx, db)
	h.c.data.Forget(ss.svc.ID)
	if frames, err := h.query(dev, db, "SELECT count(*) FROM notes"); err != nil || frames[len(frames)-1].Type != apitypes.FrameEnd {
		t.Fatalf("new table: %+v %v", frames, err)
	}

	hist, err := h.c.DataHistory(h.ctx, dev, db, 0, 0)
	if err != nil || len(hist) != 4 || hist[0].SQL != "SELECT count(*) FROM notes" || hist[1].Outcome != apitypes.OutcomeError ||
		hist[3].SQL != "SELECT id, email FROM customers ORDER BY id" || hist[3].Rows != 3 || hist[3].Outcome != apitypes.OutcomeOK {
		t.Fatalf("history: %+v %v", hist, err)
	}
	if older, _ := h.c.DataHistory(h.ctx, dev, db, hist[1].AuditID, 10); len(older) != 2 {
		t.Fatalf("older history: %+v", older)
	}
	evs, _ := h.c.ListAudit(h.ctx, viewer, AuditFilter{Action: apitypes.AuditDataQuery})
	if len(evs) != 5 || evs[0].DetailID == nil {
		t.Fatalf("query events: %+v", evs)
	}
	if d, err := h.c.GetAuditDetail(h.ctx, viewer, *evs[0].DetailID); err != nil || d.SQL != "SELECT count(*) FROM notes" {
		t.Fatalf("detail: %+v %v", d, err)
	}

	// Saved queries, per user.
	saved, err := h.c.SaveDataQuery(h.ctx, dev, db, 0, apitypes.SaveDataQueryRequest{Name: "open", SQL: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SaveDataQuery(h.ctx, dev, db, 0, apitypes.SaveDataQueryRequest{Name: "open", SQL: "SELECT 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SaveDataQuery(h.ctx, dev, db, saved.ID, apitypes.SaveDataQueryRequest{Name: "open orders", SQL: "SELECT 3"}); err != nil {
		t.Fatal(err)
	}
	list, _ := h.c.ListSavedQueries(h.ctx, dev, db)
	if len(list) != 1 || list[0].Name != "open orders" || list[0].SQL != "SELECT 3" {
		t.Fatalf("saved: %+v", list)
	}
	if list, _ := h.c.ListSavedQueries(h.ctx, admin, db); len(list) != 0 {
		t.Fatalf("someone else's saved queries: %+v", list)
	}
	if _, err := h.c.SaveDataQuery(h.ctx, admin, db, saved.ID, apitypes.SaveDataQueryRequest{Name: "x", SQL: "SELECT 1"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("edit someone else's: %v", err)
	}
	if err := h.c.DeleteSavedQuery(h.ctx, dev, db, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DeleteSavedQuery(h.ctx, dev, db, saved.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("delete twice: %v", err)
	}

	// Tokens get nothing by default; an override grants reading.
	plain := h.token(admin, apitypes.CreateTokenRequest{Name: "plain", Scope: apitypes.ScopeDeploy})
	if _, err := h.c.DataSchema(h.ctx, plain, db); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("token without access: %v", err)
	}
	bot := h.token(admin, apitypes.CreateTokenRequest{Name: "bot", Scope: apitypes.ScopeDeploy, Data: &apitypes.DataTokenAccess{
		Production: apitypes.LevelNone, NonProduction: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked,
	}})
	if page, err := h.c.DataRows(h.ctx, bot, db, apitypes.DataPageRequest{Table: "public.orders", Order: "-id", Limit: 2}); err != nil || len(page.Rows) != 2 {
		t.Fatalf("token rows: %+v %v", page, err)
	}
	if _, err := h.c.SaveDataQuery(h.ctx, bot, db, 0, apitypes.SaveDataQueryRequest{Name: "x", SQL: "SELECT 1"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("token saves a query: %v", err)
	}

	// Rotating the roles keeps working; a redeploy drops connections to
	// the old container.
	if _, err := h.c.SetupData(h.ctx, admin, db); err != nil {
		t.Fatal(err)
	}
	h.deploy(db, "postgres:17-alpine")
	if frames, err := h.query(dev, db, "SELECT 1"); err != nil || frames[len(frames)-1].Type != apitypes.FrameEnd {
		t.Fatalf("after rotation and redeploy: %+v %v", frames, err)
	}
}

func TestIntegrationDataChangesets(t *testing.T) {
	h, db, psql := dataHarness(t, "production")
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	dev2 := h.user("dev2@x", apitypes.ScopeDeploy)
	if _, err := h.c.SetupData(h.ctx, admin, db); err != nil {
		t.Fatal(err)
	}
	policy := func(level apitypes.DataLevel) {
		t.Helper()
		if _, err := h.c.SetDataPolicy(h.ctx, admin, EnvRef{}, apitypes.UpdateDataPolicyRequest{Rules: []apitypes.DataPolicyRule{
			{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindProduction, Level: level, Sensitive: apitypes.SensitiveMasked},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	s := func(v string) *string { return &v }
	pay := func(id string) apitypes.DataChangeset {
		t.Helper()
		cs, err := h.c.CreateDataChangeset(h.ctx, dev, db, apitypes.DataChangesetRequest{Message: "pay " + id, Changes: []apitypes.DataChange{{
			Op: apitypes.DataChangeUpdate, Table: "public.orders", Key: map[string]string{"id": id},
			Old: map[string]*string{"status": s("new")}, New: map[string]*string{"status": s("paid")},
		}}})
		if err != nil || cs.Status != apitypes.ChangesetPending || !cs.ConfirmCommit {
			t.Fatalf("propose: %+v %v", cs, err)
		}
		return cs
	}
	status := func(id string) string {
		t.Helper()
		frames, err := h.query(admin, db, "SELECT status FROM orders WHERE id = "+id)
		if err != nil || len(frames) != 3 {
			t.Fatalf("read status: %+v %v", frames, err)
		}
		var row []string
		_ = json.Unmarshal(frames[1].Rows[0], &row)
		return row[0]
	}

	if _, err := h.c.CreateDataChangeset(h.ctx, dev, db, apitypes.DataChangesetRequest{Changes: []apitypes.DataChange{{Op: apitypes.DataChangeDelete, Table: "public.orders", Key: map[string]string{"id": "1"}}}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("propose without access: %v", err)
	}
	policy(apitypes.LevelPropose)

	// Preview, propose, then an admin approves with the confirmation.
	preview, err := h.c.CreateDataChangeset(h.ctx, dev, db, apitypes.DataChangesetRequest{DryRun: true, Changes: []apitypes.DataChange{{
		Op: apitypes.DataChangeDelete, Table: "public.orders", Key: map[string]string{"id": "1"},
	}}})
	if err != nil || preview.Status != apitypes.ChangesetPreview || preview.ID != 0 || !strings.Contains(preview.SQL, `DELETE FROM "public"."orders"`) {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := h.c.CreateDataChangeset(h.ctx, dev, db, apitypes.DataChangesetRequest{DryRun: true, Changes: []apitypes.DataChange{{
		Op: apitypes.DataChangeUpdate, Table: "public.customers", Key: map[string]string{"id": "1"}, Old: map[string]*string{"password_hash": s("hash1")}, New: map[string]*string{"password_hash": s("x")},
	}}}); err != nil {
		t.Fatal(err) // nothing is marked sensitive yet
	}
	cs := pay("1")
	if _, err := h.c.CommitDataChangeset(h.ctx, dev, db, cs.ID, apitypes.DataCommitRequest{Confirm: "production"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("commit own with propose: %v", err)
	}
	if _, err := h.c.CommitDataChangeset(h.ctx, admin, db, cs.ID, apitypes.DataCommitRequest{}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("commit on production without confirm: %v", err)
	}
	done, err := h.c.CommitDataChangeset(h.ctx, admin, db, cs.ID, apitypes.DataCommitRequest{Confirm: "production"})
	if err != nil || done.Status != apitypes.ChangesetCommitted || len(done.RowsAffected) != 1 || done.RowsAffected[0] != 1 || done.DecidedBy != "admin@x" {
		t.Fatalf("approve: %+v %v", done, err)
	}
	if status("1") != "paid" {
		t.Fatal("approved change not written")
	}
	if h.auditCount(apitypes.AuditDataPropose) != 1 || h.auditCount(apitypes.AuditDataCommit) != 1 || h.auditCount(apitypes.AuditDataApprove) != 1 {
		t.Fatal("lifecycle not audited")
	}
	if _, err := h.c.CommitDataChangeset(h.ctx, admin, db, cs.ID, apitypes.DataCommitRequest{Confirm: "production"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("commit twice: %v", err)
	}

	// Rejecting: others can't, the author withdraws.
	cs = pay("2")
	if _, err := h.c.RejectDataChangeset(h.ctx, dev2, db, cs.ID, apitypes.DataRejectRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("reject by another dev: %v", err)
	}
	if rej, err := h.c.RejectDataChangeset(h.ctx, dev, db, cs.ID, apitypes.DataRejectRequest{Reason: "wrong order"}); err != nil || rej.Status != apitypes.ChangesetRejected || rej.Reason != "wrong order" {
		t.Fatalf("withdraw: %+v %v", rej, err)
	}

	// A row changed since the proposal: the whole changeset conflicts.
	cs = pay("3")
	psql("UPDATE orders SET status = 'shipped' WHERE id = 3")
	conflicted, err := h.c.CommitDataChangeset(h.ctx, admin, db, cs.ID, apitypes.DataCommitRequest{Confirm: "production"})
	if err != nil || conflicted.Status != apitypes.ChangesetConflict || len(conflicted.Conflicts) != 1 ||
		conflicted.Conflicts[0].Reason != apitypes.ConflictChanged || *conflicted.Conflicts[0].Current["status"] != "shipped" {
		t.Fatalf("conflict: %+v %v", conflicted, err)
	}
	if status("3") != "shipped" {
		t.Fatal("conflicting changeset wrote")
	}

	// Pending changesets expire.
	cs = pay("4")
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE changesets SET expires_at = ? WHERE id = ?", now().Add(-time.Minute), cs.ID); err != nil {
		t.Fatal(err)
	}
	if pending, _ := h.c.ListDataChangesets(h.ctx, admin, db, apitypes.ChangesetPending); len(pending) != 0 {
		t.Fatalf("expired changeset still pending: %+v", pending)
	}
	if got, _ := h.c.GetDataChangeset(h.ctx, admin, db, cs.ID); got.Status != apitypes.ChangesetExpired {
		t.Fatalf("expired: %+v", got)
	}
	all, _ := h.c.ListDataChangesets(h.ctx, dev, db, "")
	if len(all) != 4 || all[0].ID != cs.ID || all[3].Status != apitypes.ChangesetCommitted {
		t.Fatalf("all changesets: %+v", all)
	}

	// Tokens only propose on production, whatever their override says.
	bot := h.token(admin, apitypes.CreateTokenRequest{Name: "bot", Scope: apitypes.ScopeDeploy, Data: &apitypes.DataTokenAccess{
		Production: apitypes.LevelWrite, NonProduction: apitypes.LevelNone, Sensitive: apitypes.SensitiveMasked,
	}})
	policy(apitypes.LevelWrite)
	proposal, err := h.c.CreateDataChangeset(h.ctx, bot, db, apitypes.DataChangesetRequest{Changes: []apitypes.DataChange{{
		Op: apitypes.DataChangeInsert, Table: "public.orders", New: map[string]*string{"id": s("100"), "status": s("new")},
	}}})
	if err != nil || proposal.AuthorKind != "token" {
		t.Fatalf("token proposal: %+v %v", proposal, err)
	}
	if _, err := h.c.CommitDataChangeset(h.ctx, bot, db, proposal.ID, apitypes.DataCommitRequest{Confirm: "production"}); CodeOf(err) != apitypes.CodeForbidden || !strings.Contains(err.Error(), "tokens only propose") {
		t.Fatalf("token commits on production: %v", err)
	}

	// With write, people commit their own (confirming production).
	cs = pay("5")
	if own, err := h.c.CommitDataChangeset(h.ctx, dev, db, cs.ID, apitypes.DataCommitRequest{Confirm: "production"}); err != nil || own.Status != apitypes.ChangesetCommitted {
		t.Fatalf("commit own with write: %+v %v", own, err)
	}
	if h.auditCount(apitypes.AuditDataApprove) != 1 {
		t.Fatal("committing one's own is not an approval")
	}

	// Masked callers can't change sensitive columns.
	if _, err := h.c.SetSensitiveColumns(h.ctx, admin, db, apitypes.SetSensitiveColumnsRequest{Columns: []apitypes.DataColumnRef{{Schema: "public", Table: "customers", Column: "password_hash"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateDataChangeset(h.ctx, dev, db, apitypes.DataChangesetRequest{Changes: []apitypes.DataChange{{
		Op: apitypes.DataChangeUpdate, Table: "public.customers", Key: map[string]string{"id": "1"}, Old: map[string]*string{"password_hash": s("hash1")}, New: map[string]*string{"password_hash": s("x")},
	}}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("masked change to a sensitive column: %v", err)
	}
}

// TestIntegrationDataRealDial deploys Postgres on the real Docker runtime
// and uses the panel through node.Runtime.Dial on the environment network.
func TestIntegrationDataRealDial(t *testing.T) {
	c := dockerHarness(t)
	ctx := context.Background()
	project := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if _, err := c.CreateProject(ctx, System, apitypes.CreateProjectRequest{Name: project}); err != nil {
		t.Fatal(err)
	}
	db := ServiceRef{project, "production", "db"}
	if _, err := c.CreateService(ctx, System, db.Env(), apitypes.CreateServiceRequest{Name: "db", Port: 5432, HealthCheckTimeoutSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetVariables(ctx, System, db.Env(), apitypes.SetVariablesRequest{Service: "db", Set: map[string]string{"POSTGRES_PASSWORD": ownerPassword, "POSTGRES_DB": "shop"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	rel, err := c.DeployImage(ctx, System, db, apitypes.DeployImageRequest{Image: "postgres:17-alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitRelease(t, c, db, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("deploy: %s %s", r.Status, r.Error)
	}
	// The health check only waits for the port; setup retries while the
	// image's init restarts the server.
	var info apitypes.DataInfo
	for start := time.Now(); ; time.Sleep(500 * time.Millisecond) {
		if info, err = c.SetupData(ctx, System, db); err == nil || time.Since(start) > time.Minute {
			break
		}
	}
	if err != nil || info.Status != apitypes.DataReady {
		t.Fatalf("setup: %+v %v", info, err)
	}
	stream, err := c.DataQuery(ctx, System, db, apitypes.DataQueryRequest{SQL: "SELECT current_database(), current_user"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	err = stream(ctx, func() {}, func(fs ...apitypes.DataQueryFrame) error {
		for _, f := range fs {
			for _, r := range f.Rows {
				rows = append(rows, string(r))
			}
		}
		return nil
	})
	if err != nil || len(rows) != 1 || rows[0] != `["shop","dock_reader"]` {
		t.Fatalf("query over the real dial: %v %v", rows, err)
	}
}
