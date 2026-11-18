package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
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
	h.offBoxBackupsAt(fakeS3(h.t))
}

// offBoxBackupsAt is offBoxBackups with the bucket "images" at endpoint.
func (h *harness) offBoxBackupsAt(endpoint string) {
	h.t.Helper()
	if _, err := h.c.AddStorage(h.ctx, testSystem, apitypes.CreateStorageRequest{Name: "bucket", Endpoint: endpoint, Bucket: "images", AccessKeyID: "pitr-access-key", SecretAccessKey: testS3Secret}); err != nil {
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
	var kept []string
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if !strings.Contains(spec.Name, "_base") {
			return node.JobResult{}, nil
		}
		*n++
		base := walarchive.Base{Start: "000000010000000000000003", SegmentBytes: walarchive.DefaultSegmentBytes, Consistent: time.Now()}
		kept = append(kept, "base0000snap"+strings.Repeat("0", *n)) // retention keeps the earlier ones
		return node.JobResult{Output: resticOutput(kept[len(kept)-1], 4096, kept...) + base.String() + "\n"}, nil
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
	// It outlives the old container, which archives its last segment as
	// it stops, and goes once the restart is live.
	h.until("the archiver is removed", func() bool {
		_, ok := h.rt.Sidecars()[archiverName(ss)]
		return !ok
	})
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

// TestPITRWindowAcrossOffAndOn: turned off, the window still ends where
// the archive does (read from the bucket, there is no archiver to ask);
// turned on again, the window is two ranges with the gap between them,
// a time in the gap is refused naming them, and a time before it still
// restores. The ended range is read from the bucket once; "latest" is
// where the newest archive ends.
func TestPITRWindowAcrossOffAndOn(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.baseJobs()
	var keys atomic.Pointer[[]string] // the bucket's objects
	keys.Store(&[]string{})
	var lists atomic.Int64 // the bucket's listings
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lists.Add(1)
		_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`)
		for _, k := range *keys.Load() {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				_, _ = io.WriteString(w, `<Contents><Key>`+k+`</Key><Size>1</Size></Contents>`)
			}
		}
		_, _ = io.WriteString(w, `</ListBucketResult>`)
	}))
	t.Cleanup(srv.Close)
	h.offBoxBackupsAt(srv.URL)
	window := func() *apitypes.PITR {
		t.Helper()
		b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
		if err != nil || b.PITR == nil {
			t.Fatalf("backups: %+v, %v", b.PITR, err)
		}
		return b.PITR
	}
	turn := func(on bool, release int64, bases int) {
		t.Helper()
		if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(on, 7)); err != nil {
			t.Fatal(err)
		}
		if rel := h.wait(db, release); rel.Status != apitypes.ReleaseActive {
			t.Fatalf("release %d: %s (%s)", release, rel.Status, rel.Error)
		}
		h.until("the base backup", func() bool { return window().BaseBackups == bases })
	}
	turn(true, 2, 1)
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	bases, err := h.c.store.ListPITRBases(h.ctx, ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := h.c.walConfig(h.ctx, ss.svc.ID, bases[0].WalGeneration)
	if err != nil {
		t.Fatal(err)
	}
	// One segment archived a moment after the base backup, then off.
	end := bases[0].ConsistentAt.Add(time.Millisecond).Truncate(time.Millisecond)
	gap := end.Add(time.Millisecond)
	keys.Store(&[]string{walarchive.Key(cfg.Prefix, bases[0].WalStart, end)})
	turn(false, 3, 1)
	if w := window(); w.Through == nil || !w.Through.Equal(end) || w.UsedBy == nil {
		t.Fatalf("the window while off: through %v, want %v; used by %v", w.Through, end, w.UsedBy)
	}
	for !time.Now().After(gap.Add(time.Millisecond)) { // the next base backup is after the gap's time
		time.Sleep(time.Millisecond)
	}
	turn(true, 4, 2)
	if bases, err = h.c.store.ListPITRBases(h.ctx, ss.svc.ID); err != nil || len(bases) != 2 {
		t.Fatalf("base backups: %d, %v", len(bases), err)
	}
	// The new generation's archiver says where it ends.
	end2 := bases[1].ConsistentAt.Add(time.Millisecond).Truncate(time.Millisecond)
	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(walarchive.Status{Healthy: true, Through: &end2})
	}))
	t.Cleanup(status.Close)
	h.rt.DialTo(strings.TrimPrefix(status.URL, "http://"))
	w := window()
	if len(w.Ranges) != 2 || !w.Ranges[0].From.Equal(*bases[0].ConsistentAt) || !w.Ranges[0].Through.Equal(end) ||
		!w.Ranges[1].From.Equal(*bases[1].ConsistentAt) || !w.Ranges[1].Through.Equal(end2) {
		t.Fatalf("ranges = %+v, want %v to %v and %v to %v", w.Ranges, bases[0].ConsistentAt, end, bases[1].ConsistentAt, end2)
	}
	if !w.From.Equal(w.Ranges[0].From) || !w.Through.Equal(end2) {
		t.Fatalf("outer bounds = %v to %v", w.From, w.Through)
	}
	// The ended generation was listed once; the page costs no listing now.
	before := lists.Load()
	window()
	if n := lists.Load() - before; n != 0 {
		t.Fatalf("%d bucket listings for a window that was read before", n)
	}
	_, err = h.c.planToTime(h.ctx, ss, gap)
	if err == nil || !strings.Contains(err.Error(), "wasn't archived from "+end.UTC().Format(time.RFC3339)) ||
		!strings.Contains(err.Error(), "it restores from "+bases[0].ConsistentAt.UTC().Format(time.RFC3339)+" to "+end.UTC().Format(time.RFC3339)+" and from ") {
		t.Fatalf("a time in the gap: %v", err)
	}
	if plan, err := h.c.planToTime(h.ctx, ss, end); err != nil || plan.base.ID != bases[0].ID {
		t.Fatalf("a time before the gap: base %d, %v", plan.base.ID, err)
	}

	// Latest: nothing archived past the newest base backup yet refuses;
	// then it is where that archive ends.
	if _, err := h.c.latestRestorable(h.ctx, ss); err == nil || !strings.Contains(err.Error(), "try again in a minute") {
		t.Fatalf("latest with nothing archived: %v", err)
	}
	cfg2, err := h.c.walConfig(h.ctx, ss.svc.ID, bases[1].WalGeneration)
	if err != nil {
		t.Fatal(err)
	}
	keys.Store(&[]string{walarchive.Key(cfg.Prefix, bases[0].WalStart, end), walarchive.Key(cfg2.Prefix, bases[1].WalStart, end2)})
	if at, err := h.c.latestRestorable(h.ctx, ss); err != nil || !at.Equal(end2) {
		t.Fatalf("latest = %v, %v; want %v", at, err, end2)
	}
	if _, err := h.c.RestoreLatest(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{ToLatest: true, ToTime: &end}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("latest and a time: %v", err)
	}

	// The storage down: a refusal that says so, not an internal error.
	srv.Close()
	if _, err := h.c.planToTime(h.ctx, ss, end); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "can't be read (connection refused)") {
		t.Fatalf("with the storage down: %v", err)
	}
	if _, err := h.c.latestRestorable(h.ctx, ss); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "can't be read (connection refused)") {
		t.Fatalf("latest with the storage down: %v", err)
	}
}

// TestPITROffRestartFails: a turn-off whose restart release fails never
// leaves a database archiving without its archiver. With the old
// container still running it stays on, archiver and all, and the audit
// log says so; with the old container stopped (its last segment handed
// over) the turn-off stands, and the old release comes back without
// archiving.
func TestPITROffRestartFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stopped bool
	}{{"the old container still runs", false}, {"the old container stopped", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			db := h.templateDB("db", apitypes.TemplatePostgres)
			h.baseJobs()
			h.offBoxBackups()
			if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(true, 7)); err != nil {
				t.Fatal(err)
			}
			if rel := h.wait(db, 2); rel.Status != apitypes.ReleaseActive {
				t.Fatalf("release 2: %s (%s)", rel.Status, rel.Error)
			}
			ss, err := h.c.loadService(h.ctx, 1, db)
			if err != nil {
				t.Fatal(err)
			}
			if tc.stopped {
				h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("not ready") })
			} else {
				h.rt.FailStop(errors.New("docker is busy"))
			}
			if _, err := h.c.SetPITR(h.ctx, testSystem, db, setPITR(false, 7)); err != nil {
				t.Fatal(err)
			}
			if rel := h.wait(db, 3); rel.Status != apitypes.ReleaseFailed {
				t.Fatalf("release 3: %s", rel.Status)
			}
			h.rt.FailHealthCheck(nil)
			h.rt.FailStop(nil)
			row, err := h.c.store.GetPITR(h.ctx, ss.svc.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, archiver := h.rt.Sidecars()[archiverName(ss)]
			spec, _ := h.rt.Spec(h.ctr(db, 2))
			archives := strings.Contains(spec.Cmd, "archive_mode=on")
			b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
			if err != nil {
				t.Fatal(err)
			}
			// Whatever the outcome, the four agree: the row, what new
			// containers get, the archiver, and the running container.
			want := !tc.stopped
			if row.Enabled != want || h.c.pitrOn(ss.svc.ID) != want || archiver != want || archives != want || b.PITR.Enabled != want || h.c.isLeaving(ss.svc.ID) {
				t.Fatalf("row on %v, containers archive %v, archiver %v, release 2 archives %v, page on %v, leaving %v; want all %v and not leaving",
					row.Enabled, h.c.pitrOn(ss.svc.ID), archiver, archives, b.PITR.Enabled, h.c.isLeaving(ss.svc.ID), want)
			}
			evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "backup.pitr"})
			if err != nil {
				t.Fatal(err)
			}
			said := slices.ContainsFunc(evs, func(e apitypes.AuditEvent) bool { return strings.Contains(e.Summary, "stays on: release 3") })
			if said != want {
				t.Fatalf("audit says it stays on: %v, want %v", said, want)
			}
		})
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
