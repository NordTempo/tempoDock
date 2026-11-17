package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// testS3Secret is the fake bucket's secret key, distinct enough to find
// wherever it leaks.
const testS3Secret = "pitr-s3-secret-0123456789"

// offBoxBackups points the backups at a storage connection (a fake
// bucket), as `dock backup move` leaves them.
func (h *harness) offBoxBackups() {
	h.t.Helper()
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "bucket", Endpoint: fakeS3(h.t), Bucket: "images", AccessKeyID: "pitr-access-key", SecretAccessKey: testS3Secret}); err != nil {
		h.t.Fatal(err)
	}
	conn, err := h.c.store.GetStorageConnection(h.ctx, "bucket")
	if err != nil {
		h.t.Fatal(err)
	}
	set, err := h.c.backupSettings(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.c.repoOf(h.ctx, set); err != nil { // the password
		h.t.Fatal(err)
	}
	if set, err = h.c.store.GetBackupSettings(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	set.StorageID, set.StoragePath = &conn.ID, "dock-backups"
	if _, err := h.c.store.UpsertBackupSettings(h.ctx, upsertParams(set)); err != nil {
		h.t.Fatal(err)
	}
}

// baseJobs answers base backup jobs like the dock binary and restic would,
// counting them; other jobs succeed quietly.
func (h *harness) baseJobs() *int {
	n := new(int)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if !strings.Contains(spec.Name, "_base") {
			return node.JobResult{}, nil
		}
		*n++
		base := walarchive.Base{Start: "000000010000000000000003", SegmentBytes: walarchive.DefaultSegmentBytes, Consistent: time.Now()}
		return node.JobResult{Output: resticOutput("base0000snap"+strings.Repeat("0", *n), 4096, "base0000snap"+strings.Repeat("0", *n)) + base.String() + "\n"}, nil
	})
	return n
}

func setPITR(on bool, days int) apitypes.SetPITRRequest {
	return apitypes.SetPITRRequest{Enabled: &on, Days: &days}
}

// TestPITROnOff: turning point-in-time restore on starts the archiver,
// restarts the database with the archive settings (in its container, not
// its start command) and takes the first base backup; off removes them.
func TestPITROnOff(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	bases := h.baseJobs()

	org := Actor{Kind: "token", OrgID: 1, Name: "admin", Scope: apitypes.ScopeAdmin}
	if _, err := h.c.SetPITR(h.ctx, org, db, setPITR(true, 7)); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin turned it on: %v", err)
	}
	if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(true, 7)); err == nil || !strings.Contains(err.Error(), "storage connection") {
		t.Fatalf("on with local backups: %v", err)
	}
	h.offBoxBackups()
	if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(true, 99)); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("99 days: %v", err)
	}
	if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(true, 7)); err != nil {
		t.Fatal(err)
	}
	if rel := h.wait(db, 2); rel.Status != apitypes.ReleaseActive {
		t.Fatalf("restart release: %s (%s)", rel.Status, rel.Error)
	}
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	side, ok := h.rt.Sidecars()[archiverName(ss)]
	if !ok {
		t.Fatal("no archiver")
	}
	if !slices.Equal(side.Aliases, []string{archiverHost(ss.svc.ID)}) || !side.Mounts[0].ReadOnly ||
		!strings.Contains(side.Env["DOCK_WAL_PREFIX"], "dock-backups/dock-wal/s") || side.Image != jobImage(ss) || side.Health == nil {
		t.Fatalf("archiver = %+v", side)
	}
	spec, _ := h.rt.Spec(h.ctr(db, 2))
	if !strings.Contains(spec.Cmd, "'archive_command=/.dock-probe wal-push "+archiverHost(ss.svc.ID)+":8432 %p %f'") ||
		!strings.Contains(spec.Cmd, "'wal_recycle=off'") || spec.Health == nil {
		t.Fatalf("container command = %s", spec.Cmd)
	}
	if strings.Contains(ss.svc.StartCommand, "archive") {
		t.Fatalf("the start command changed: %s", ss.svc.StartCommand)
	}
	for _, k := range walarchive.Secrets {
		if _, ok := spec.Env[k]; ok {
			t.Fatalf("the database's container has %s", k)
		}
	}
	h.until("the first base backup", func() bool {
		b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
		return err == nil && b.PITR != nil && b.PITR.BaseBackups == 1
	})
	if *bases != 1 {
		b, _ := h.c.ServiceBackups(h.ctx, testSystem, db)
		for _, r := range b.Runs {
			t.Logf("run %d %s %s %s %s %s", r.ID, r.Operation, r.Kind, r.Trigger, r.Status, r.CreatedBy)
		}
		t.Fatalf("%d base backups", *bases)
	}

	if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(false, 7)); err != nil {
		t.Fatal(err)
	}
	if rel := h.wait(db, 3); rel.Status != apitypes.ReleaseActive {
		t.Fatalf("release 3: %s", rel.Status)
	}
	if _, ok := h.rt.Sidecars()[archiverName(ss)]; ok {
		t.Fatal("the archiver still runs")
	}
	if spec, _ := h.rt.Spec(h.ctr(db, 3)); strings.Contains(spec.Cmd, "archive") {
		t.Fatalf("archiving after off: %s", spec.Cmd)
	}
	// What follows an earlier release going active may get here only now:
	// it starts no archiver again.
	if _, on, err := h.c.keepArchiver(h.ctx, ss); on || err != nil {
		t.Fatalf("keeping the archiver after off: %v, %v", on, err)
	}
	if _, ok := h.rt.Sidecars()[archiverName(ss)]; ok {
		t.Fatal("a late start brought the archiver back")
	}
}

// TestStalledArchiverAlerts: an archiver whose health check fails opens
// the database's alert with what it says (how much WAL waits and how
// long), and its recovery resolves it.
func TestStalledArchiverAlerts(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.baseJobs()
	h.offBoxBackups()
	if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(true, 7)); err != nil {
		t.Fatal(err)
	}
	h.wait(db, 2)
	ss, _ := h.c.loadService(h.ctx, 1, db)

	oldest := time.Now().Add(-12 * time.Minute)
	st := walarchive.Status{Waiting: 70, WaitingBytes: 70 << 24, OldestWaiting: &oldest, DiskBytes: 20 << 30, DiskFree: 18 << 30, LastError: "s3: connection refused"}
	st.Healthy, st.Problem = false, "70 WAL files wait to be archived, the oldest for 12m0s; unarchived WAL takes 1.1 GB of the database's disk (18.0 GB free); the last upload failed: s3: connection refused"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(st) }))
	defer srv.Close()
	h.rt.DialTo(strings.TrimPrefix(srv.URL, "http://"))

	h.rt.SidecarHealth(archiverName(ss), node.HealthUnhealthy)
	a := h.openAlert(pitrAlertKey(ss), true)
	if !strings.Contains(a.Message, "unarchived WAL takes 1.1 GB") || !strings.Contains(a.Message, "dock backup pitr off") {
		t.Fatalf("alert = %s", a.Message)
	}
	b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
	if err != nil || b.PITR.Archiver == nil || b.PITR.Archiver.Healthy || b.PITR.Archiver.WaitingBytes != 70<<24 {
		t.Fatalf("backups page: %+v, %v", b.PITR, err)
	}
	h.rt.SidecarHealth(archiverName(ss), node.HealthHealthy)
	h.openAlert(pitrAlertKey(ss), false)
}

func TestPITRForget(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	base := func(id int64, daysAgo int) store.Backup {
		at := now.Add(-time.Duration(daysAgo) * 24 * time.Hour)
		return store.Backup{ID: id, SnapshotID: "s", ConsistentAt: &at}
	}
	bases := []store.Backup{base(1, 10), base(2, 9), base(3, 8), base(4, 3), base(5, 0)}
	ids := func(bs []store.Backup) []int64 {
		var out []int64
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	// On: the newest before the window stays, the window's start replays from it.
	if got := ids(pitrForget(bases, now, 7, true)); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("on: forget %v", got)
	}
	// Off: the window ages out.
	if got := ids(pitrForget(bases, now, 7, false)); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("off: forget %v", got)
	}
}

func TestWALDroppable(t *testing.T) {
	p := "dock-backups/dock-wal/s7/"
	from := map[string]string{"g2": "000000010000000000000010"}
	for key, drop := range map[string]bool{
		p + "g2/00000001000000000000000F@1": true,  // before the oldest kept base
		p + "g2/000000010000000000000010@1": false, // its start
		p + "g2/00000002.history@1":         false, // history stays
		p + "g1/000000010000000000000099@1": true,  // no base of g1 left
		p + "g3/000000010000000000000001@1": false, // the current one, before its first base
		"elsewhere/x":                       false,
	} {
		if got := walDroppable(p, key, from, true, "g3"); got != drop {
			t.Errorf("%s: drop %v", key, got)
		}
	}
	if !walDroppable(p, p+"g3/000000010000000000000001@1", from, false, "g3") {
		t.Error("off: the old current generation was kept")
	}
}

// TestPITRSecretsStayWithDock: the archive's key and the bucket's secret
// reach only the environment of Dock's own containers, the archiver and
// the base backup and restore jobs (the template's image, Dock's
// command). The database's container, which anyone with deploy can
// point at any image, never gets them, nor does any command line: not
// its archive_command, the archiver's arguments or a job's script, which
// docker inspect and ps show.
func TestPITRSecretsStayWithDock(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.baseJobs()
	h.offBoxBackups()
	if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(true, 7)); err != nil {
		t.Fatal(err)
	}
	h.wait(db, 2)
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	side := h.rt.Sidecars()[archiverName(ss)]
	key := side.Env["DOCK_WAL_KEY"]
	if len(key) != 2*walarchive.KeySize || side.Env["DOCK_WAL_SECRET_ACCESS_KEY"] != testS3Secret {
		t.Fatalf("the archiver's environment lacks the key or the secret: %v", side.Env)
	}
	secrets := []string{key, testS3Secret}
	leaks := func(what string, texts ...string) {
		t.Helper()
		for _, text := range texts {
			for _, sec := range secrets {
				if strings.Contains(text, sec) {
					t.Errorf("%s holds a secret: %q", what, text)
				}
			}
		}
	}
	leaks("the archiver's command line", side.Args...)

	spec, _ := h.rt.Spec(h.ctr(db, 2))
	leaks("the database's command line", append([]string{spec.Cmd}, spec.Args...)...)
	for k, v := range spec.Env {
		if slices.Contains(walarchive.Secrets, k) || strings.HasPrefix(k, "DOCK_WAL_") {
			t.Errorf("the database's container has %s", k)
		}
		leaks("the database's variable "+k, v)
	}

	// A restore job's: its script and name hold none, its environment
	// all (it runs Postgres for the recovery itself).
	tpl, _ := templateOf(ss.svc)
	cfg, err := h.c.walConfig(h.ctx, ss.svc.ID, "g1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	plan := pitrPlan{base: store.Backup{SnapshotID: "base0000snap", WalStart: "000000010000000000000003", WalSegmentBytes: walarchive.DefaultSegmentBytes, ConsistentAt: &at}, cfg: cfg, target: at}
	if _, err := h.c.recoverJob(h.ctx, ss, tpl, plan, 99, "vol", map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": "pw"}); err != nil {
		t.Fatal(err)
	}
	jobs := h.rt.Jobs()
	var restore *node.JobSpec
	for i, j := range jobs {
		leaks("job "+j.Name, j.Name, j.Script)
		if strings.HasSuffix(j.Name, "_pitr99") {
			restore = &jobs[i]
		}
	}
	if restore == nil || restore.Env["DOCK_WAL_KEY"] != key || !restore.Dock || restore.Image != tpl.image {
		t.Fatalf("restore job = %+v", restore)
	}
}
