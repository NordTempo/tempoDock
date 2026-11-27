package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/node"
)

// templateDB creates a database from a template and waits for its first
// release.
func (h *harness) templateDB(name string, tpl apitypes.Template) ServiceRef {
	h.t.Helper()
	ref := h.service(name, apitypes.CreateServiceRequest{Template: tpl})
	if rel := h.wait(ref, 1); rel.Status != apitypes.ReleaseActive {
		h.t.Fatalf("%s release 1: %s (%s)", name, rel.Status, rel.Error)
	}
	return ref
}

// revealed returns a service's own live variables.
func (h *harness) revealed(ref ServiceRef) map[string]string {
	h.t.Helper()
	vars, err := h.c.ListVariables(h.ctx, testSystem, ref.Env(), ref.Service, true)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]string{}
	for _, v := range vars {
		if v.Service == ref.Service && v.Value != nil {
			out[v.Key] = *v.Value
		}
	}
	return out
}

func TestTemplatePostgres(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	svc, err := h.c.GetService(h.ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Template != apitypes.TemplatePostgres || svc.Database != apitypes.DatabasePostgres || svc.DataEngine != apitypes.EnginePostgres || !svc.Stateful {
		t.Fatalf("service = %+v", svc)
	}

	vars := h.revealed(db)
	pw := vars["POSTGRES_PASSWORD"]
	if len(pw) != 32 || vars["POSTGRES_USER"] != "postgres" || vars["POSTGRES_DB"] != "app" ||
		vars["DATABASE_URL"] != "postgresql://${{db.POSTGRES_USER}}:${{db.POSTGRES_PASSWORD}}@db:5432/${{db.POSTGRES_DB}}" {
		t.Fatalf("variables = %v", vars)
	}

	spec, ok := h.rt.Spec(h.ctr(db, 1))
	if !ok {
		t.Fatal("no container")
	}
	t1 := templates[apitypes.TemplatePostgres]
	switch {
	case spec.Env["DATABASE_URL"] != "postgresql://postgres:"+pw+"@db:5432/app":
		t.Errorf("DATABASE_URL = %q", spec.Env["DATABASE_URL"])
	case spec.Env["PORT"] != "5432":
		t.Errorf("PORT = %q", spec.Env["PORT"])
	case spec.Port != 0 || spec.HostPort != 0:
		t.Errorf("published %d:%d; databases stay private", spec.HostPort, spec.Port)
	case len(spec.Mounts) != 1 || spec.Mounts[0].Path != "/var/lib/postgresql/data":
		t.Errorf("mounts = %+v", spec.Mounts)
	case spec.Resources.MemoryBytes != 512<<20 || !strings.Contains(spec.Cmd, "shared_buffers=128MB") || !strings.HasPrefix(spec.Cmd, "exec docker-entrypoint.sh postgres"):
		t.Errorf("limits and command = %d %q", spec.Resources.MemoryBytes, spec.Cmd)
	case spec.Image == "" || !slices.Contains(h.rt.Ops(), "pull "+t1.image):
		t.Errorf("image %q not pulled pinned (%v)", spec.Image, h.rt.Ops())
	}
	ss, _ := h.c.serviceFor(h.ctx, testSystem, db, apitypes.ScopeRead, "testing")
	rel, _ := h.store.GetActiveRelease(h.ctx, ss.svc.ID)
	if cfg, err := h.c.openConfig(rel); err != nil || cfg.HealthPort != 5432 || cfg.HealthKind != node.HealthPostgres || cfg.Published {
		t.Errorf("release config: health port %d, kind %q, published %v (%v)", cfg.HealthPort, cfg.HealthKind, cfg.Published, err)
	}

	// Other services reference it; a second database gets its own password.
	api := h.service("api", apitypes.CreateServiceRequest{})
	h.setVars(api, map[string]string{"DATABASE_URL": "${{db.DATABASE_URL}}"})
	h.deploy(api, "app:1")
	if spec, _ := h.rt.Spec(h.ctr(api, 1)); spec.Env["DATABASE_URL"] != "postgresql://postgres:"+pw+"@db:5432/app" {
		t.Errorf("api DATABASE_URL = %q", spec.Env["DATABASE_URL"])
	}
	other := h.templateDB("db2", apitypes.TemplatePostgres)
	if h.revealed(other)["POSTGRES_PASSWORD"] == pw {
		t.Error("two databases share a password")
	}

	if _, err := h.c.CreateService(h.ctx, testSystem, db.Env(), apitypes.CreateServiceRequest{Name: "x", Template: "cassandra"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("unknown template: %v", err)
	}
}

func TestTemplateRedis(t *testing.T) {
	h := newHarness(t)
	cache := h.templateDB("cache", apitypes.TemplateRedis)
	vars := h.revealed(cache)
	if len(vars["REDIS_PASSWORD"]) != 32 || vars["REDIS_URL"] != "redis://default:${{cache.REDIS_PASSWORD}}@cache:6379" {
		t.Fatalf("variables = %v", vars)
	}
	spec, _ := h.rt.Spec(h.ctr(cache, 1))
	if !strings.Contains(spec.Cmd, `--requirepass "$REDIS_PASSWORD" --appendonly yes`) || spec.Mounts[0].Path != "/data" || spec.Env["PORT"] != "6379" {
		t.Errorf("spec = %q %+v", spec.Cmd, spec.Mounts)
	}
	// The template marks it for the data panel (ADR 0072).
	if svc, _ := h.c.GetService(h.ctx, testSystem, cache); svc.DataEngine != apitypes.EngineRedis || svc.Database != apitypes.DatabaseRedis {
		t.Errorf("redis detected as %q (setting %q)", svc.DataEngine, svc.Database)
	}
}

// resticOutput is what a backup job prints: the summary, then the
// snapshots retention kept.
func resticOutput(snapshot string, size int64, kept ...string) string {
	ids := make([]string, len(kept))
	for i, k := range kept {
		ids[i] = fmt.Sprintf(`{"id":%q}`, k)
	}
	return fmt.Sprintf(`{"message_type":"summary","total_bytes_processed":%d,"snapshot_id":%q}`+"\n%s\n[%s]\n",
		size, snapshot, backup.SnapshotsMarker, strings.Join(ids, ","))
}

// waitRun waits until a service's backup or restore run finished.
func (h *harness) waitRun(ref ServiceRef, id int64) apitypes.BackupRun {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := h.c.ServiceBackups(h.ctx, testSystem, ref)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, r := range b.Runs {
			if r.ID == id && r.Status != apitypes.BackupRunning {
				return r
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %d of %s still running", id, ref.Service)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) runBackup(ref ServiceRef) apitypes.BackupRun {
	h.t.Helper()
	run, err := h.c.RunBackup(h.ctx, testSystem, ref)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.waitRun(ref, run.ID)
}

func TestBackupRecordsSnapshotsAndRetention(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	pw := h.revealed(db)["POSTGRES_PASSWORD"]
	snaps := []string{"aaaa1111aaaa", "bbbb2222bbbb"}
	n := 0
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if !strings.Contains(spec.Name, "_backup") {
			return node.JobResult{}, nil // restic install
		}
		n++
		if n == 1 {
			return node.JobResult{Output: resticOutput(snaps[0], 4096, snaps[0])}, nil
		}
		return node.JobResult{Output: resticOutput(snaps[1], 8192, snaps[1])}, nil // retention dropped the first
	})

	first := h.runBackup(db)
	if first.Status != apitypes.BackupSucceeded || first.Snapshot != snaps[0] || first.SizeBytes != 4096 || first.OffBox || first.Repository != "local" {
		t.Fatalf("first backup = %+v", first)
	}
	jobs := h.rt.Jobs()
	job := jobs[len(jobs)-1]
	ss, _ := h.c.serviceFor(h.ctx, testSystem, db, apitypes.ScopeRead, "testing")
	want, _ := backup.BackupScript(apitypes.BackupPostgres)
	switch {
	case job.Image != templates[apitypes.TemplatePostgres].image:
		t.Errorf("job image %q, want the template's", job.Image)
	case job.Script != want:
		t.Errorf("script = %q", job.Script)
	case job.Network != networkName(ss.envScope):
		t.Errorf("network = %q", job.Network)
	case job.Env["PGHOST"] != "db" || job.Env["PGPASSWORD"] != pw || job.Env["PGDATABASE"] != "app" || job.Env["DOCK_TAG"] != backup.Tag(ss.svc.ID):
		t.Errorf("env = %v", job.Env)
	case !slices.ContainsFunc(job.Mounts, func(m node.Mount) bool { return m.Path == backup.BinDir && m.ReadOnly }) ||
		!slices.ContainsFunc(job.Mounts, func(m node.Mount) bool { return m.Volume == h.c.localRepoVolume() }):
		t.Errorf("mounts = %+v", job.Mounts)
	}

	h.runBackup(db)
	b, err := h.c.ServiceBackups(h.ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Runs) != 2 || b.Runs[0].Pruned || !b.Runs[1].Pruned || !b.Restorable || b.Kind != apitypes.BackupPostgres {
		t.Fatalf("backups = %+v", b)
	}
	installs := 0
	for _, op := range h.rt.Ops() {
		if strings.HasPrefix(op, "job dock_restic_install") {
			installs++
		}
	}
	if installs != 1 {
		t.Errorf("restic installed %d times", installs)
	}
	ov, err := h.c.Overview(h.ctx, testSystem, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if so := ov.Environments[0].Services[0]; so.Backup == nil || so.Backup.Snapshot != snaps[1] {
		t.Errorf("overview backup = %+v", so.Backup)
	}
	if _, err := h.c.RunBackup(h.ctx, h.user("viewer@x", apitypes.ScopeRead), db); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("read user ran a backup: %v", err)
	}
}

// Backup jobs carry the install's repository password, so they never run
// the image someone with deploy on the environment put on the service.
func TestBackupJobIgnoresTheServiceImage(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput("dddd4444dddd", 1, "dddd4444dddd")}, nil
	})
	agent := h.token(testSystem, apitypes.CreateTokenRequest{Name: "agent", Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "production"})
	if _, err := h.c.DeployImage(h.ctx, agent, db, apitypes.DeployImageRequest{Image: "evil/postgres:17"}); err != nil {
		t.Fatal(err)
	}
	if rel := h.wait(db, 2); rel.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2: %s", rel.Status)
	}
	if run, err := h.c.RunBackup(h.ctx, agent, db); err != nil {
		t.Fatal(err)
	} else if h.waitRun(db, run.ID).Status != apitypes.BackupSucceeded {
		t.Fatal("backup failed")
	}
	jobs := h.rt.Jobs()
	job := jobs[len(jobs)-1]
	if job.Image != templates[apitypes.TemplatePostgres].image || job.Env["RESTIC_PASSWORD"] == "" {
		t.Fatalf("backup job ran %q", job.Image)
	}
}

func TestBackupFailureIsRecorded(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_backup") {
			return node.JobResult{ExitCode: 1, Output: "Fatal: wrong password or no key found\n"}, nil
		}
		return node.JobResult{}, nil
	})
	run := h.runBackup(db)
	if run.Status != apitypes.BackupFailed || !strings.Contains(run.Error, "wrong password") {
		t.Fatalf("run = %+v", run)
	}
	if evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "backup.failed"}); len(evs) != 1 {
		t.Error("no backup.failed audit event")
	}
	if open, _ := h.c.ListAlerts(h.ctx, testSystem, false, false, 0, 0); len(open) != 1 || open[0].Kind != apitypes.AlertBackupFailed {
		t.Fatalf("open alerts after a failed backup = %+v", open)
	}

	// The next successful backup resolves the alert.
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_backup") {
			return node.JobResult{Output: resticOutput("cccc3333cccc", 1024, "cccc3333cccc")}, nil
		}
		return node.JobResult{}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded {
		t.Fatalf("second run = %+v", run)
	}
	if open, _ := h.c.ListAlerts(h.ctx, testSystem, false, false, 0, 0); len(open) != 0 {
		t.Fatalf("open alerts after a successful backup = %+v", open)
	}
}

// A job that couldn't reach its repository fails the backup with the
// job's own message, not an exit code.
func TestBackupUnreachableRepository(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	const msg = "can't reach the backup storage at http://127.0.0.1:9000/b from a backup job (no answer in 60s)"
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_backup") {
			return node.JobResult{ExitCode: backup.UnreachableExit, Output: "dock: " + msg + "\n"}, nil
		}
		return node.JobResult{}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupFailed || run.Error != msg {
		t.Fatalf("run = %+v", run)
	}
}

func TestVolumeBackupRestoresInPlace(t *testing.T) {
	h := newHarness(t)
	app := h.service("app", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddVolume(h.ctx, testSystem, app, apitypes.AddVolumeRequest{Name: "files", MountPath: "/srv"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(app, "app:1")
	const snap = "cccc3333cccc"
	filling, release := make(chan struct{}), make(chan struct{})
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return &node.JobResult{Output: resticOutput(snap, 10, snap)}
		case spec.Script == backup.VolumeRestoreScript:
			close(filling)
			<-release
		}
		return nil
	})
	run := h.runBackup(app)
	jobs := h.rt.Jobs()
	job := jobs[len(jobs)-1]
	if run.Kind != apitypes.BackupVolume || job.Image != h.opts.Config.Backup.ResticImage || job.Env["DOCK_SNAPSHOT_TAG"] != job.Env["DOCK_MANUAL_TAG"] ||
		!slices.ContainsFunc(job.Mounts, func(m node.Mount) bool { return m.Path == "/volumes/files" && m.ReadOnly }) {
		t.Fatalf("run %+v, job %+v", run, job)
	}
	b, _ := h.c.ServiceBackups(h.ctx, testSystem, app)
	if b.Warning == "" || b.Restorable || !b.RestoresInPlace {
		t.Errorf("volume backups = %+v", b)
	}
	// In place is the only way, and has to be asked for.
	for _, req := range []apitypes.RestoreBackupRequest{{Snapshot: snap}, {Snapshot: snap, InPlace: true, As: "copy"}, {ToTime: ptr(now()), InPlace: true}} {
		if _, err := h.c.RestoreBackup(h.ctx, testSystem, app, req); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("restore %+v: %v", req, err)
		}
	}
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap[:8], InPlace: true})
		return err
	})
	<-filling
	if _, err := h.c.Restart(h.ctx, testSystem, app); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("deploy during the restore: %v", err)
	}
	close(release)
	done := h.waitRun(app, res.Restore.ID)
	if done.Status != apitypes.BackupSucceeded || done.Operation != "restore" || done.RestoredTo != "app" || done.Snapshot != snap {
		t.Fatalf("restore = %+v", done)
	}
	// Stopped, the safety snapshot, the fill into the writable volume,
	// then started again.
	jobs = h.rt.Jobs()
	fill := slices.IndexFunc(jobs, func(j node.JobSpec) bool { return j.Script == backup.VolumeRestoreScript })
	safety := slices.IndexFunc(jobs, func(j node.JobSpec) bool { return j.Script == snapshotScript })
	if safety < 0 || fill < safety || jobs[fill].Env["DOCK_SNAPSHOT"] != snap ||
		!slices.ContainsFunc(jobs[fill].Mounts, func(m node.Mount) bool { return m.Path == "/volumes/files" && !m.ReadOnly }) {
		t.Errorf("jobs = %+v", jobs)
	}
	ops, ctr := h.rt.Ops(), h.ctr(app, 1)
	if stop := indexOf(ops, "stop "+ctr); stop < 0 || !slices.Contains(ops[stop:], "start "+ctr) || !slices.Contains(h.rt.Running(), ctr) {
		t.Errorf("ops = %v, running = %v", ops, h.rt.Running())
	}
	if _, err := h.c.ServiceBackups(h.ctx, testSystem, h.service("web", apitypes.CreateServiceRequest{})); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("service without volume: %v", err)
	}
}

func TestVolumeBackupRestorePutsTheDataBackOnFailure(t *testing.T) {
	h := newHarness(t)
	const snap = "dddd4444dddd"
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return &node.JobResult{Output: resticOutput(snap, 10, snap)}
		case spec.Script == backup.VolumeRestoreScript:
			return &node.JobResult{ExitCode: 1, Output: "dock: the backup holds the volume cache, which the service no longer has"}
		}
		return nil
	})
	app := h.volumeService("app")
	h.runBackup(app)
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap, InPlace: true})
		return err
	})
	done := h.waitRun(app, res.Restore.ID)
	if done.Status != apitypes.BackupFailed || !strings.Contains(done.Error, "no longer has") || !strings.Contains(done.Error, "put back") {
		t.Fatalf("restore = %+v", done)
	}
	if !slices.ContainsFunc(h.rt.Jobs(), func(j node.JobSpec) bool { return j.Script == snapshotRestoreScript }) {
		t.Error("the safety snapshot wasn't copied back")
	}
	if !slices.Contains(h.rt.Running(), h.ctr(app, 1)) {
		t.Error("the service wasn't started again")
	}
}

// forgetEngine stands in for one engine kind and records which services
// the Data panel forgot.
type forgetEngine struct {
	data.Engine
	mu     sync.Mutex
	forgot []int64
}

func (e *forgetEngine) Kind() apitypes.DataEngine { return apitypes.EngineRedis }

func (e *forgetEngine) Forget(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.forgot = append(e.forgot, id)
}

func (e *forgetEngine) Close() {}

func TestRestorePostgresIntoNewService(t *testing.T) {
	h := newHarness(t)
	forgets := &forgetEngine{}
	h.opts.DataEngines = []data.Engine{pg.New(pg.Options{}), forgets}
	h.restart()
	db := h.templateDB("db", apitypes.TemplatePostgres)
	snap := "dddd4444dddd4444"
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	h.runBackup(db)

	res, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: snap[:8], As: "db-old"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Service.Name != "db-old" || res.Service.Template != apitypes.TemplatePostgres || res.Restore.RestoredTo != "db-old" {
		t.Fatalf("result = %+v", res)
	}
	done := h.waitRun(db, res.Restore.ID)
	if done.Status != apitypes.BackupSucceeded || done.Operation != "restore" || done.Snapshot != snap {
		t.Fatalf("restore = %+v", done)
	}
	old := ServiceRef{"shop", "production", "db-old"}
	// Its schema, cached when its first release went live, is read again
	// now that the dump filled it.
	forgets.mu.Lock()
	forgot := slices.Clone(forgets.forgot)
	forgets.mu.Unlock()
	if ss, err := h.c.loadService(h.ctx, 1, old); err != nil || !slices.Contains(forgot, ss.svc.ID) {
		t.Fatalf("the Data panel kept db-old's schema: %v, forgot %v", err, forgot)
	}
	jobs := h.rt.Jobs()
	job := jobs[len(jobs)-1]
	newPw := h.revealed(old)["POSTGRES_PASSWORD"]
	if !strings.Contains(job.Name, "_restore") || job.Env["PGHOST"] != "db-old" || job.Env["PGPASSWORD"] != newPw || job.Env["DOCK_SNAPSHOT"] != snap {
		t.Fatalf("restore job = %+v", job)
	}
	if newPw == h.revealed(db)["POSTGRES_PASSWORD"] {
		t.Error("the restored service reuses the live one's password")
	}
	// The live database is untouched: still its first release.
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, db, 10); len(rels) != 1 {
		t.Errorf("live database got %d releases", len(rels))
	}
	if _, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: snap, As: "db-old"}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("restore over an existing name: %v", err)
	}
	if _, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: "ffffffff"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("unknown snapshot: %v", err)
	}
}

func TestRestoreRedisFillsVolumeBeforeStart(t *testing.T) {
	h := newHarness(t)
	cache := h.templateDB("cache", apitypes.TemplateRedis)
	snap := "eeee5555eeee"
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	h.runBackup(cache)
	res, err := h.c.RestoreBackup(h.ctx, testSystem, cache, apitypes.RestoreBackupRequest{Snapshot: snap})
	if err != nil {
		t.Fatal(err)
	}
	if done := h.waitRun(cache, res.Restore.ID); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore = %+v", done)
	}
	restored := ServiceRef{"shop", "production", "cache-restored"}
	ops := h.rt.Ops()
	fill := slices.IndexFunc(ops, func(op string) bool { return strings.HasPrefix(op, "job ") && strings.Contains(op, "_restore") })
	start := indexOf(ops, "start "+h.ctr(restored, 1))
	if fill < 0 || start < 0 || fill > start {
		t.Fatalf("volume filled at %d, started at %d: %v", fill, start, ops)
	}
	job := h.rt.Jobs()[len(h.rt.Jobs())-1]
	ss, _ := h.c.serviceFor(h.ctx, testSystem, restored, apitypes.ScopeRead, "testing")
	if !slices.ContainsFunc(job.Mounts, func(m node.Mount) bool {
		return m.Volume == volumeName(ss, templateVolume) && m.Path == "/data" && !m.ReadOnly
	}) {
		t.Errorf("mounts = %+v", job.Mounts)
	}
}

// The scheduler backs up a service once its hour has passed since it was
// created or last backed up, and not again that day.
func TestScheduledBackupRunsWhenDue(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput("ffff6666", 1, "ffff6666")}, nil
	})
	id := h.serviceID(db)
	if due, next, err := h.c.dueBackups(h.ctx); err != nil || len(due) != 0 || next.IsZero() {
		t.Fatalf("new service: due %v, next %v, %v", due, next, err)
	}
	// Created two days ago: due now.
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE services SET created_at = ? WHERE id = ?", time.Now().UTC().Add(-48*time.Hour), id); err != nil {
		t.Fatal(err)
	}
	h.c.kickBackups()
	deadline := time.Now().Add(5 * time.Second)
	var b apitypes.ServiceBackups
	for {
		var err error
		if b, err = h.c.ServiceBackups(h.ctx, testSystem, db); err != nil {
			t.Fatal(err)
		}
		if len(b.Runs) == 1 && b.Runs[0].Status == apitypes.BackupSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no scheduled backup: %+v", b.Runs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if b.Runs[0].Trigger != "schedule" || b.Runs[0].CreatedBy != "system" {
		t.Errorf("run = %+v", b.Runs[0])
	}
	due, _, err := h.c.dueBackups(h.ctx)
	if err != nil || len(due) != 0 || b.Schedule.NextAt == nil || time.Until(*b.Schedule.NextAt) > 25*time.Hour {
		t.Fatalf("after the backup: due %v, next %v", due, b.Schedule.NextAt)
	}

	// Off: never due; a custom hour shows.
	off := false
	if _, err := h.c.UpdateBackupSchedule(h.ctx, testSystem, db, apitypes.UpdateBackupScheduleRequest{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, next, _ := h.c.dueBackups(h.ctx); !next.IsZero() {
		t.Errorf("disabled schedule still has a next run: %v", next)
	}
	on, hour := true, 5
	got, err := h.c.UpdateBackupSchedule(h.ctx, testSystem, db, apitypes.UpdateBackupScheduleRequest{Enabled: &on, Hour: &hour})
	if err != nil || got.Schedule.Hour != 5 || got.Schedule.DefaultHour || got.Schedule.NextAt.In(time.Local).Hour() != 5 {
		t.Errorf("schedule = %+v, %v", got.Schedule, err)
	}
}

func TestConfigureBackups(t *testing.T) {
	h := newHarness(t)
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	daily := 14
	if _, err := h.c.ConfigureBackups(h.ctx, dev, apitypes.ConfigureBackupRequest{KeepDaily: &daily}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy user configured backups: %v", err)
	}
	// Buckets are storage connections now (ADR 0039).
	repo := "s3:https://s3.example.com/bucket/dock"
	if _, err := h.c.ConfigureBackups(h.ctx, testSystem, apitypes.ConfigureBackupRequest{Repository: &repo}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("set a repository URL: %v", err)
	}
	set, err := h.c.ConfigureBackups(h.ctx, testSystem, apitypes.ConfigureBackupRequest{KeepDaily: &daily})
	if err != nil {
		t.Fatal(err)
	}
	if set.OffBox || set.KeepDaily != 14 || len(set.Password) != 32 {
		t.Fatalf("settings = %+v", set)
	}
	// The password is shown once; again only to admins who ask.
	again, err := h.c.ConfigureBackups(h.ctx, testSystem, apitypes.ConfigureBackupRequest{})
	if err != nil || again.Password != "" {
		t.Fatalf("second configure: %+v %v", again, err)
	}
	if got, _ := h.c.GetBackupSettings(h.ctx, testSystem, true); got.Password != set.Password {
		t.Errorf("revealed %q, want %q", got.Password, set.Password)
	}
	if _, err := h.c.GetBackupSettings(h.ctx, dev, true); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("deploy user revealed the password: %v", err)
	}
	s, _ := h.c.backupSettings(h.ctx)
	if r, err := h.c.repoOf(h.ctx, s); err != nil || !r.Local() || r.Password != set.Password {
		t.Errorf("repo = %+v, %v", r, err)
	}
}

// A restart marks runs it cut short as failed.
func TestRestartFailsRunningBackups(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	hold := make(chan struct{})
	h.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		if strings.Contains(spec.Name, "_backup") {
			<-hold
			return node.JobResult{}, context.Canceled
		}
		return node.JobResult{}, nil
	})
	run, err := h.c.RunBackup(h.ctx, testSystem, db)
	if err != nil {
		t.Fatal(err)
	}
	// Stopping waits for the job, which ends once the daemon stopped.
	go func() { time.Sleep(20 * time.Millisecond); close(hold) }()
	h.restart()
	if got := h.waitRun(db, run.ID); got.Status != apitypes.BackupFailed || !strings.Contains(got.Error, "restarted") {
		t.Fatalf("run = %+v", got)
	}
}

// A MySQL template backs up with the dump tools of its own image as root,
// and restores into a new MySQL with its own root password.
func TestTemplateMySQLBackupAndRestore(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplateMySQL)
	vars := h.revealed(db)
	if vars["MYSQL_DATABASE"] != "app" || len(vars["MYSQL_ROOT_PASSWORD"]) != 32 || !strings.HasPrefix(vars["DATABASE_URL"], "mysql://root:${{db.MYSQL_ROOT_PASSWORD}}@db:3306/") {
		t.Fatalf("variables = %v", vars)
	}
	snap := "eeee5555eeee5555"
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded || run.Kind != apitypes.BackupMySQL {
		t.Fatalf("backup = %+v", run)
	}
	jobs := h.rt.Jobs()
	job := jobs[len(jobs)-1]
	if !strings.HasPrefix(job.Image, "mysql:8.4") || job.Env["MYSQL_PWD"] != vars["MYSQL_ROOT_PASSWORD"] || job.Env["DOCK_DB_HOST"] != "db" || job.Env["DOCK_DB_PORT"] != "3306" {
		t.Fatalf("backup job = %+v", job)
	}

	res, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: snap[:8], As: "db-old"})
	if err != nil {
		t.Fatal(err)
	}
	if done := h.waitRun(db, res.Restore.ID); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore = %+v", done)
	}
	jobs = h.rt.Jobs()
	job = jobs[len(jobs)-1]
	newPw := h.revealed(ServiceRef{"shop", "production", "db-old"})["MYSQL_ROOT_PASSWORD"]
	if !strings.Contains(job.Name, "_restore") || job.Env["DOCK_DB_HOST"] != "db-old" || job.Env["MYSQL_PWD"] != newPw || newPw == vars["MYSQL_ROOT_PASSWORD"] {
		t.Fatalf("restore job = %+v", job)
	}
}

// A MongoDB template backs up with mongodump of its own image as its root
// user, and restores into a new MongoDB with its own root password.
func TestTemplateMongoDBBackupAndRestore(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplateMongoDB)
	vars := h.revealed(db)
	if vars["MONGO_INITDB_ROOT_USERNAME"] != "root" || len(vars["MONGO_INITDB_ROOT_PASSWORD"]) != 32 ||
		vars["MONGODB_URI"] != "mongodb://${{db.MONGO_INITDB_ROOT_USERNAME}}:${{db.MONGO_INITDB_ROOT_PASSWORD}}@db:27017/app?authSource=admin" {
		t.Fatalf("variables = %v", vars)
	}
	snap := "ffff6666ffff6666"
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	if run := h.runBackup(db); run.Status != apitypes.BackupSucceeded || run.Kind != apitypes.BackupMongoDB {
		t.Fatalf("backup = %+v", run)
	}
	jobs := h.rt.Jobs()
	job := jobs[len(jobs)-1]
	if !strings.HasPrefix(job.Image, "mongo:8.0") || job.Env["DOCK_DB_USER"] != "root" || job.Env[backup.MongoPassword] != vars["MONGO_INITDB_ROOT_PASSWORD"] ||
		job.Env["DOCK_DB_HOST"] != "db" || job.Env["DOCK_DB_PORT"] != "27017" {
		t.Fatalf("backup job = %+v", job)
	}

	res, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: snap[:8], As: "db-old"})
	if err != nil {
		t.Fatal(err)
	}
	if done := h.waitRun(db, res.Restore.ID); done.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore = %+v", done)
	}
	jobs = h.rt.Jobs()
	job = jobs[len(jobs)-1]
	newPw := h.revealed(ServiceRef{"shop", "production", "db-old"})["MONGO_INITDB_ROOT_PASSWORD"]
	if !strings.Contains(job.Name, "_restore") || job.Env["DOCK_DB_HOST"] != "db-old" || job.Env[backup.MongoPassword] != newPw || newPw == vars["MONGO_INITDB_ROOT_PASSWORD"] {
		t.Fatalf("restore job = %+v", job)
	}
}

// An archived service still holds its name, so a restore into the default
// name says that and how to pick another, not just "already exists".
func TestRestoreIntoAnArchivedServicesNameSaysSo(t *testing.T) {
	h := newHarness(t)
	db := h.templateDB("db", apitypes.TemplatePostgres)
	snap := "dddd4444dddd4444"
	h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) {
		return node.JobResult{Output: resticOutput(snap, 100, snap)}, nil
	})
	h.runBackup(db)
	res, err := h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: snap})
	if err != nil {
		t.Fatal(err)
	}
	h.waitRun(db, res.Restore.ID)
	if _, err := h.c.ArchiveService(h.ctx, testSystem, ServiceRef{"shop", "production", "db-restored"}); err != nil {
		t.Fatal(err)
	}
	_, err = h.c.RestoreBackup(h.ctx, testSystem, db, apitypes.RestoreBackupRequest{Snapshot: snap})
	if CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "is archived") || !strings.Contains(err.Error(), "--as <name>") {
		t.Fatalf("restore into an archived service's name: %v", err)
	}
}

// A restart during an in-place restore leaves the service stopped on
// data that may be half replaced: the failed row says how to finish or
// undo it.
func TestVolumeBackupRestoreInterruptedByCrash(t *testing.T) {
	h := newHarness(t)
	const snap = "eeee5555eeee"
	filling, stuck := make(chan struct{}), make(chan struct{})
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return &node.JobResult{Output: resticOutput(snap, 10, snap)}
		case spec.Script == backup.VolumeRestoreScript:
			close(filling)
			<-stuck // the daemon dies first
		}
		return nil
	})
	app := h.volumeService("app")
	h.runBackup(app)
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap, InPlace: true})
		return err
	})
	<-filling
	h.snapshotJobs(nil)
	h.crash()
	// Before the dead daemon's Close (cleanups run last first), which
	// waits for its job.
	t.Cleanup(func() { close(stuck) })
	if r := h.waitRun(app, res.Restore.ID); r.Status != apitypes.BackupFailed || !strings.Contains(r.Error, "in place again") || !strings.Contains(r.Error, "to undo") {
		t.Fatalf("restore = %+v", r)
	}
}
