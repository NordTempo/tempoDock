package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// copySource is a running service with a volume, a variable of its own
// and a domain, and snap as its one volume backup.
func (h *harness) copySource(snap string, fill func(node.JobSpec) *node.JobResult) ServiceRef {
	h.t.Helper()
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		switch {
		case strings.Contains(spec.Name, "_backup"):
			return &node.JobResult{Output: resticOutput(snap, 10, snap)}
		case spec.Script == backup.VolumeRestoreScript:
			return fill(spec)
		}
		return nil
	})
	app := h.service("app", apitypes.CreateServiceRequest{Port: 8080, StartCommand: "serve --files /srv"})
	if _, err := h.c.AddVolume(h.ctx, testSystem, app, apitypes.AddVolumeRequest{Name: "files", MountPath: "/srv"}); err != nil {
		h.t.Fatal(err)
	}
	env := EnvRef{app.Project, app.Environment}
	vars := apitypes.SetVariablesRequest{Service: "app", Set: map[string]string{"TOKEN": "s3cret", "SELF": "${{app.TOKEN}}"}}
	if _, err := h.c.SetVariables(h.ctx, testSystem, env, vars); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, app, apitypes.AddDomainRequest{Hostname: "files.example.com"}); err != nil {
		h.t.Fatal(err)
	}
	h.deploy(app, "app:1")
	h.runBackup(app)
	return app
}

func TestRestoreCopyFillsANewServiceBeforeItStarts(t *testing.T) {
	h := newHarness(t)
	const snap = "eeee5555eeee"
	filling, release := make(chan struct{}), make(chan struct{})
	app := h.copySource(snap, func(node.JobSpec) *node.JobResult {
		close(filling)
		<-release
		return nil
	})
	old := ServiceRef{app.Project, app.Environment, "app-old"}
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap[:8], As: "app-old"})
		return err
	})
	<-filling
	// Nothing of the copy runs while its volumes are filled, and nothing
	// can start it.
	srcCtr := h.ctr(app, 1)
	if got := h.rt.Running(); !slices.Equal(got, []string{srcCtr}) {
		t.Errorf("running during the fill = %v", got)
	}
	if _, err := h.c.DeployImage(h.ctx, testSystem, old, apitypes.DeployImageRequest{Image: "app:1"}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("deploy of the copy during the fill: %v", err)
	}
	close(release)
	done := h.waitRun(app, res.Restore.ID)
	if done.Status != apitypes.BackupSucceeded || done.Operation != "restore" || done.Kind != apitypes.BackupVolume || done.RestoredTo != "app-old" || done.Snapshot != snap {
		t.Fatalf("restore = %+v", done)
	}

	src, err := h.c.serviceFor(h.ctx, testSystem, app, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	to, err := h.c.serviceFor(h.ctx, testSystem, old, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	// The fill wrote the copy's own new volume, not the source's.
	jobs := h.rt.Jobs()
	fill := jobs[slices.IndexFunc(jobs, func(j node.JobSpec) bool { return j.Script == backup.VolumeRestoreScript })]
	want := node.Mount{Volume: volumeName(to, "files"), Path: "/volumes/files"}
	if fill.Env["DOCK_SNAPSHOT"] != snap || !slices.Contains(fill.Mounts, want) ||
		slices.ContainsFunc(fill.Mounts, func(m node.Mount) bool { return m.Volume == volumeName(src, "files") }) {
		t.Errorf("fill job = %+v, want mount %+v", fill, want)
	}
	// The copy runs the source's image with its settings and volume; the
	// source was never stopped.
	h.waitRunning(srcCtr, h.ctr(old, 1))
	if slices.Contains(h.rt.Ops(), "stop "+srcCtr) {
		t.Errorf("the source was stopped: %v", h.rt.Ops())
	}
	rel, err := h.c.store.GetActiveRelease(h.ctx, to.svc.ID)
	if err != nil || rel.ImageRef != "app:1" {
		t.Errorf("the copy's release = %+v, %v", rel, err)
	}
	if to.svc.Port != 8080 || to.svc.StartCommand != src.svc.StartCommand {
		t.Errorf("the copy's settings = %+v", to.svc)
	}
	vols, err := h.c.ListVolumes(h.ctx, testSystem, old)
	if err != nil || len(vols) != 1 || vols[0].Name != "files" || vols[0].MountPath != "/srv" || vols[0].Pending {
		t.Errorf("the copy's volumes = %+v, %v", vols, err)
	}
	vars, err := h.c.ListVariables(h.ctx, testSystem, EnvRef{app.Project, app.Environment}, "app-old", true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range vars {
		if v.Service == "app-old" {
			got[v.Key] = deref(v.Value)
		}
	}
	if len(got) != 2 || got["TOKEN"] != "s3cret" || got["SELF"] != "${{app.TOKEN}}" {
		t.Errorf("the copy's variables = %v", got)
	}
	if domains, err := h.c.ListDomains(h.ctx, testSystem, old); err != nil || len(domains) != 0 {
		t.Errorf("the copy's domains = %+v, %v", domains, err)
	}
	// The name is taken now, and the source is free for the next restore.
	if _, err := h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap, As: "app-old"}); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a second copy of the same name: %v", err)
	}
}

func TestRestoreCopyThatFailsToFillNeverStarts(t *testing.T) {
	h := newHarness(t)
	const snap = "ffff6666ffff"
	app := h.copySource(snap, func(node.JobSpec) *node.JobResult {
		return &node.JobResult{ExitCode: 1, Output: "dock: the backup holds the volume cache, which the service no longer has"}
	})
	old := ServiceRef{app.Project, app.Environment, "app-old"}
	var res apitypes.RestoreResult
	h.untilFree(func() (err error) {
		res, err = h.c.RestoreBackup(h.ctx, testSystem, app, apitypes.RestoreBackupRequest{Snapshot: snap, As: "app-old"})
		return err
	})
	done := h.waitRun(app, res.Restore.ID)
	if done.Status != apitypes.BackupFailed || done.RestoredTo != "app-old" || !strings.Contains(done.Error, "no longer has") || !strings.Contains(done.Error, "never started") {
		t.Fatalf("restore = %+v", done)
	}
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(app, 1)}) {
		t.Errorf("running = %v", got)
	}
	if rels, err := h.c.store.ListReleases(h.ctx, store.ListReleasesParams{ServiceID: h.serviceID(old), Limit: 5}); err != nil || len(rels) != 0 {
		t.Errorf("the copy's releases = %+v, %v", rels, err)
	}
}
