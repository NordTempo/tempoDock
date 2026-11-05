package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Volume snapshots (ADR 0047): a copy of a service's volumes into the
// server's snapshot volume, taken in one moment. The service's running
// containers are paused (frozen, connections kept) while a job copies the
// volumes with `cp -a`, so the copy is what a crash at that moment would
// have left: databases recover from it as from a power cut. Snapshots stay
// on the server, count against its disk, and are pruned by count and age.
// They restore in place (the service is stopped, its data snapshotted
// first, then replaced) or, for template databases, into a new service.

const (
	// snapshotVolumePrefix starts the name of a server's snapshot volume;
	// core adds the install.
	snapshotVolumePrefix = "dock_snapshots_"
	// snapshotsPath is where jobs mount the snapshot volume; each
	// snapshot is the directory named after its ID, one subdirectory per
	// service volume.
	snapshotsPath = "/snapshots"
	// snapshotVolumesPath is where jobs mount the service's volumes, each
	// at its name.
	snapshotVolumesPath = "/volumes"
	// snapshotListLimit caps the snapshots a service lists and retention
	// looks at.
	snapshotListLimit = 200
	// snapshotRestoresShown is how many restores a service lists.
	snapshotRestoresShown = 20
)

// snapshotScript copies every volume in $VOLUMES into the snapshot
// $SNAPSHOT, first in a .part directory that is renamed when complete, so
// a cut-short copy is never taken for a snapshot. It refuses a copy that
// would leave less than a tenth of the disk free, and a copy that fails
// anyway (the disk filled up meanwhile) removes what it wrote.
const snapshotScript = `set -eu
dir="` + snapshotsPath + `/$SNAPSHOT"
rm -rf "$dir" "$dir.part"
trap 'rm -rf "$dir.part"' EXIT
need=$(du -sk ` + snapshotVolumesPath + ` | cut -f1)
set -- $(df -Pk ` + snapshotsPath + ` | tail -n 1)
total=$2
avail=$4
if [ "$need" -gt $((avail - total / 10)) ]; then
	echo "dock-snapshot: no-room $need $avail $total"
	exit 3
fi
mkdir -p "$dir.part"
for v in $VOLUMES; do cp -a "` + snapshotVolumesPath + `/$v" "$dir.part/$v"; done
mv "$dir.part" "$dir"
echo "dock-snapshot: size $(du -sk "$dir" | cut -f1)"
`

// snapshotRestoreScript replaces the contents of every volume in $VOLUMES
// with the snapshot's copy, owner and mode of the volume's root included
// (Postgres insists on 0700).
const snapshotRestoreScript = `set -eu
src="` + snapshotsPath + `/$SNAPSHOT"
for v in $VOLUMES; do
	if [ ! -d "$src/$v" ]; then echo "dock-snapshot: missing $v"; exit 4; fi
done
for v in $VOLUMES; do
	dst="` + snapshotVolumesPath + `/$v"
	rm -rf "$dst"/* "$dst"/.[!.]* "$dst"/..?*
	cp -a "$src/$v/." "$dst/"
	chown "$(stat -c %u:%g "$src/$v")" "$dst"
	chmod "$(stat -c %a "$src/$v")" "$dst"
done
echo "dock-snapshot: restored"
`

// snapshotDeleteScript removes the snapshots in $SNAPSHOTS, finished or
// cut short.
const snapshotDeleteScript = `set -eu
for s in $SNAPSHOTS; do rm -rf "` + snapshotsPath + `/$s" "` + snapshotsPath + `/$s.part"; done
`

// snapshotState is the in-memory side of snapshots.
type snapshotState struct {
	mu sync.Mutex
	// inPlace holds the services an in-place restore is replacing the
	// data of: deploys to them are refused until it ends.
	inPlace map[int64]bool
}

func newSnapshotState() snapshotState { return snapshotState{inPlace: map[int64]bool{}} }

// restoringInPlace reports whether an in-place restore runs for a
// service.
func (c *Core) restoringInPlace(serviceID int64) bool {
	c.snapshots.mu.Lock()
	defer c.snapshots.mu.Unlock()
	return c.snapshots.inPlace[serviceID]
}

func (c *Core) setRestoringInPlace(serviceID int64, on bool) {
	c.snapshots.mu.Lock()
	defer c.snapshots.mu.Unlock()
	if on {
		c.snapshots.inPlace[serviceID] = true
	} else {
		delete(c.snapshots.inPlace, serviceID)
	}
}

// snapshotVolume is the volume holding a server's snapshots, named after
// the install so control planes sharing a Docker host keep their own.
func (c *Core) snapshotVolume() string {
	sum := sha256.Sum256([]byte(c.install))
	return snapshotVolumePrefix + hex.EncodeToString(sum[:4])
}

// snapshotKeep returns retention: snapshots kept per service, and days.
func (c *Core) snapshotKeep() (int, int) {
	return cmp.Or(c.cfg.Backup.SnapshotKeep, 5), cmp.Or(c.cfg.Backup.SnapshotDays, 7)
}

// ServiceSnapshots lists a stateful service's snapshots and restores.
func (c *Core) ServiceSnapshots(ctx context.Context, a Actor, ref ServiceRef) (apitypes.ServiceSnapshots, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing snapshots")
	if err != nil {
		return apitypes.ServiceSnapshots{}, err
	}
	keep, days := c.snapshotKeep()
	_, isTemplate := templateOf(ss.svc)
	out := apitypes.ServiceSnapshots{
		Service: ss.svc.Name, IntoNew: isTemplate, Keep: keep, KeepDays: days,
		Snapshots: []apitypes.Snapshot{}, Restores: []apitypes.SnapshotRestore{},
	}
	rows, err := c.store.ListServiceSnapshots(ctx, store.ListServiceSnapshotsParams{ServiceID: ss.svc.ID, Limit: snapshotListLimit})
	if err != nil {
		return out, err
	}
	names, err := c.serverNames(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Snapshots = append(out.Snapshots, toSnapshot(r, ss.svc.Name, names))
	}
	restores, err := c.store.ListServiceSnapshotRestores(ctx, store.ListServiceSnapshotRestoresParams{ServiceID: ss.svc.ID, Limit: snapshotRestoresShown})
	if err != nil {
		return out, err
	}
	svcNames := map[int64]string{ss.svc.ID: ss.svc.Name}
	if slices.ContainsFunc(restores, func(r store.VolumeSnapshotRestore) bool { return r.ToServiceID != ss.svc.ID }) {
		// Restores create services in the same environment: one query.
		svcs, err := c.store.ListServices(ctx, ss.env.ID)
		if err != nil {
			return out, err
		}
		for _, s := range svcs {
			svcNames[s.ID] = s.Name
		}
	}
	for _, r := range restores {
		out.Restores = append(out.Restores, toSnapshotRestore(r, svcNames))
	}
	return out, nil
}

// toSnapshot converts a snapshots row; servers names servers by ID.
func toSnapshot(r store.VolumeSnapshot, service string, servers map[int64]string) apitypes.Snapshot {
	return apitypes.Snapshot{
		ID: r.ID, Service: service, Trigger: apitypes.SnapshotTrigger(r.Trigger), Status: apitypes.SnapshotStatus(r.Status),
		Server: servers[r.ServerID], Volumes: strings.Fields(r.Volumes), SizeBytes: r.SizeBytes, PausedMS: r.PausedMs,
		Note: r.Note, Error: r.Error, CreatedBy: r.CreatedBy, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// toSnapshotRestore converts a snapshot_restores row; names resolves
// service IDs.
func toSnapshotRestore(r store.VolumeSnapshotRestore, names map[int64]string) apitypes.SnapshotRestore {
	return apitypes.SnapshotRestore{
		ID: r.ID, Snapshot: deref(r.SnapshotID), Service: names[r.ServiceID], To: names[r.ToServiceID], InPlace: r.InPlace,
		Safety: deref(r.SafetyID), Status: apitypes.BackupStatus(r.Status), Error: r.Error, CreatedBy: r.CreatedBy,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// CreateSnapshot takes a snapshot of a service's volumes now, in the
// background; the answer is the snapshot, running. snapshot.created or
// snapshot.failed is audited at the end. Scope: deploy.
func (c *Core) CreateSnapshot(ctx context.Context, a Actor, ref ServiceRef, req apitypes.CreateSnapshotRequest) (apitypes.Snapshot, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "taking a snapshot")
	if err != nil {
		return apitypes.Snapshot{}, err
	}
	note := cleanLine(req.Note, 200)
	row, vols, err := c.startSnapshot(ctx, a, ss, apitypes.SnapshotManual, note)
	if err != nil {
		return apitypes.Snapshot{}, err
	}
	c.audit(ctx, a, "snapshot.create", &ss.project, &ss.env, ss.String(), fmt.Sprintf("taking snapshot %d of %s", row.ID, strings.Join(volumeNames(vols), ", ")))
	c.wg.Go(func() {
		defer c.releaseService(ss.svc.ID)
		_ = c.runSnapshot(c.ctx, ss, row, vols)
	})
	names, err := c.serverNames(ctx)
	return toSnapshot(row, ss.svc.Name, names), err
}

// SnapshotBefore takes a snapshot of a stateful service before a risky
// action and waits for it: the seam for the pre-deploy command
// (trigger before_deploy) and `dock run` (before_run). A service without
// an applied volume has nothing to keep: no snapshot and no error. Any
// failure is an error, and the caller should not go on with the action.
func (c *Core) SnapshotBefore(ctx context.Context, a Actor, ss svcScope, trigger apitypes.SnapshotTrigger, note string) (*apitypes.Snapshot, error) {
	vols, err := c.appliedVolumes(ctx, ss)
	if err != nil || len(vols) == 0 {
		return nil, err
	}
	row, vols, err := c.startSnapshot(ctx, a, ss, trigger, note)
	if err != nil {
		return nil, err
	}
	defer c.releaseService(ss.svc.ID)
	row, err = c.takeClaimed(ctx, ss, row, vols, true)
	if err != nil {
		return nil, err
	}
	names, err := c.serverNames(ctx)
	snap := toSnapshot(row, ss.svc.Name, names)
	return &snap, err
}

// appliedVolumes lists a service's volumes that are in use (not staged).
func (c *Core) appliedVolumes(ctx context.Context, ss svcScope) ([]store.Volume, error) {
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	return slices.DeleteFunc(vols, func(v store.Volume) bool { return v.Pending }), err
}

func volumeNames(vols []store.Volume) []string {
	out := make([]string, len(vols))
	for i, v := range vols {
		out[i] = v.Name
	}
	return out
}

// startSnapshot checks a snapshot can be taken, claims the service and
// records it running. The caller must release the claim
// (releaseService) once runSnapshot or takeClaimed returns.
func (c *Core) startSnapshot(ctx context.Context, a Actor, ss svcScope, trigger apitypes.SnapshotTrigger, note string) (store.VolumeSnapshot, []store.Volume, error) {
	if err := notArchived(ss.project); err != nil {
		return store.VolumeSnapshot{}, nil, err
	}
	if ss.svc.ArchivedAt != nil {
		return store.VolumeSnapshot{}, nil, invalid("service %s is archived", ss)
	}
	vols, err := c.appliedVolumes(ctx, ss)
	if err != nil {
		return store.VolumeSnapshot{}, nil, err
	}
	if len(vols) == 0 {
		return store.VolumeSnapshot{}, nil, invalid("%s has no volume, so there is nothing to snapshot", ss)
	}
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return store.VolumeSnapshot{}, nil, err
	}
	if !c.claimService(ss.svc.ID) {
		return store.VolumeSnapshot{}, nil, conflict("a backup, snapshot or restore of %s is running; wait for it to finish", ss)
	}
	row, err := c.store.CreateSnapshot(ctx, store.CreateSnapshotParams{
		ServiceID: ss.svc.ID, ServerID: ss.env.ServerID, Trigger: string(trigger), Volumes: strings.Join(volumeNames(vols), " "),
		Note: note, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return store.VolumeSnapshot{}, nil, err
	}
	return row, vols, nil
}

// runSnapshot takes a recorded snapshot to the end, audits the outcome and
// prunes the service's old snapshots. The caller holds the claim.
func (c *Core) runSnapshot(ctx context.Context, ss svcScope, row store.VolumeSnapshot, vols []store.Volume) error {
	_, err := c.takeClaimed(ctx, ss, row, vols, true)
	return err
}

// takeClaimed runs the snapshot job for a recorded snapshot, pausing the
// service's running containers for it, records and audits how it went,
// and with prune applies retention. The caller holds the claim on the
// service.
func (c *Core) takeClaimed(ctx context.Context, ss svcScope, row store.VolumeSnapshot, vols []store.Volume, prune bool) (store.VolumeSnapshot, error) {
	size, paused, err := c.snapshotJob(ctx, ss, row, vols)
	if ctx.Err() != nil {
		return row, ctx.Err() // shutting down: the next start marks it failed
	}
	fin := store.FinishSnapshotParams{Status: string(apitypes.SnapshotReady), SizeBytes: size, PausedMs: paused.Milliseconds(), FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.SnapshotFailed), err.Error()
	}
	// Recorded before the audit event announces it, so whoever the event
	// wakes (MCP's create_snapshot) reads the outcome.
	if ferr := c.store.FinishSnapshot(ctx, fin); ferr != nil {
		c.log.Error("record snapshot", "snapshot", row.ID, "err", ferr)
	}
	if err != nil {
		c.audit(ctx, System, "snapshot.failed", &ss.project, &ss.env, ss.String(), fmt.Sprintf("snapshot %d failed: %v", row.ID, err))
	} else {
		summary := fmt.Sprintf("snapshot %d (%s): %s", row.ID, row.Trigger, humanBytes(size))
		if paused > 0 {
			summary += fmt.Sprintf(", paused %s", paused.Round(time.Millisecond))
		}
		c.audit(ctx, System, "snapshot.created", &ss.project, &ss.env, ss.String(), summary)
	}
	row.Status, row.Error, row.SizeBytes, row.PausedMs, row.FinishedAt = fin.Status, fin.Error, fin.SizeBytes, fin.PausedMs, fin.FinishedAt
	if err == nil && prune {
		c.pruneSnapshots(ctx, ss)
	}
	return row, err
}

// noRoom is the job's answer when the copy wouldn't fit.
var noRoom = regexp.MustCompile(`dock-snapshot: no-room (\d+) (\d+) (\d+)`)

// snapshotSize is the job's report of the snapshot's size, in KiB.
var snapshotSize = regexp.MustCompile(`dock-snapshot: size (\d+)`)

// snapshotJob runs the copy on the service's server and returns the
// snapshot's size and how long the service was paused.
func (c *Core) snapshotJob(ctx context.Context, ss svcScope, row store.VolumeSnapshot, vols []store.Volume) (int64, time.Duration, error) {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return 0, 0, err
	}
	pause, err := c.runningContainers(ctx, ss, rt)
	if err != nil {
		return 0, 0, c.nodeErr(ss.env.ServerID, err)
	}
	spec := node.JobSpec{
		Name:   fmt.Sprintf("dock_p%d_e%d_s%d_snapshot%d", ss.project.ID, ss.env.ID, ss.svc.ID, row.ID),
		Labels: c.labels(ss, 0), Script: snapshotScript, Pause: pause,
		Env: map[string]string{"SNAPSHOT": strconv.FormatInt(row.ID, 10), "VOLUMES": row.Volumes},
	}
	for _, v := range vols {
		spec.Mounts = append(spec.Mounts, node.Mount{Volume: v.DockerName, Path: snapshotVolumesPath + "/" + v.Name, ReadOnly: true})
	}
	start := now()
	out, err := c.runSnapshotJob(ctx, ss.env.ServerID, rt, spec)
	var paused time.Duration
	if len(pause) > 0 {
		paused = now().Sub(start)
	}
	if m := noRoom.FindStringSubmatch(out); m != nil {
		need, _ := strconv.ParseInt(m[1], 10, 64)
		avail, _ := strconv.ParseInt(m[2], 10, 64)
		return 0, paused, fmt.Errorf("not enough disk on the server: the snapshot needs %s and %s is free, of which a tenth of the disk stays free; delete old snapshots (`dock volume snapshot delete`) or free disk first",
			humanBytes(need<<10), humanBytes(avail<<10))
	}
	if err != nil {
		return 0, paused, err
	}
	m := snapshotSize.FindStringSubmatch(out)
	if m == nil {
		return 0, paused, fmt.Errorf("the snapshot job didn't report a size%s", withOutput(out))
	}
	kib, _ := strconv.ParseInt(m[1], 10, 64)
	return kib << 10, paused, nil
}

// runningContainers lists the IDs of the running containers of a
// service's active release, the ones a snapshot pauses.
func (c *Core) runningContainers(ctx context.Context, ss svcScope, rt node.Runtime) ([]string, error) {
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ctrs, err := rt.List(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ct := range c.ofRelease(ctrs, ss, active.ID) {
		if ct.State == node.StateRunning {
			ids = append(ids, ct.ID)
		}
	}
	return ids, nil
}

// runSnapshotJob runs spec on a server with the snapshot volume mounted,
// in the restic image (a small Alpine with cp, du and tar). A job that
// exits non-zero is an error with the end of its output.
func (c *Core) runSnapshotJob(ctx context.Context, serverID int64, rt node.Runtime, spec node.JobSpec) (string, error) {
	vol := c.snapshotVolume()
	if err := rt.EnsureVolume(ctx, vol, node.Labels{ControlPlane: c.install}); err != nil {
		return "", fmt.Errorf("create the snapshot volume: %w", c.nodeErr(serverID, err))
	}
	spec.Image = c.cfg.Backup.ResticImage
	if err := c.ensureImage(ctx, serverID, rt, spec.Image); err != nil {
		return "", err
	}
	spec.Mounts = append(spec.Mounts, node.Mount{Volume: vol, Path: snapshotsPath})
	spec.Resources = node.Resources{MemoryBytes: jobMemoryMB << 20, NanoCPUs: 1e9}
	res, err := rt.RunJob(ctx, spec)
	if err != nil {
		return res.Output, c.nodeErr(serverID, err)
	}
	if res.ExitCode != 0 {
		return res.Output, fmt.Errorf("exit code %d%s", res.ExitCode, withOutput(res.Output))
	}
	return res.Output, nil
}

// RestoreSnapshot restores snapshot id of a service. A template database
// restores into a new service by default (req.As, default
// <service>-snapshot-<id>): created with the snapshot's credentials, its
// volume filled from the snapshot before its first deploy, the service
// itself untouched. With req.InPlace the service's own volumes are
// replaced: it is stopped, its current data is snapshotted first (the
// restore's safety snapshot, restore it to undo), the snapshot is copied
// in and the release starts again. Services that aren't template
// databases restore in place only. Both run in the background; the
// answer is the restore, running. Scope: deploy.
func (c *Core) RestoreSnapshot(ctx context.Context, a Actor, ref ServiceRef, id int64, req apitypes.RestoreSnapshotRequest) (apitypes.SnapshotRestoreResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "restoring a snapshot")
	if err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	if err := notArchived(ss.project); err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	snap, err := c.serviceSnapshot(ctx, ss, id)
	if err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	if snap.Status != string(apitypes.SnapshotReady) {
		return apitypes.SnapshotRestoreResult{}, invalid("snapshot %d is %s; only a ready snapshot restores", id, snap.Status)
	}
	if snap.ServerID != ss.env.ServerID {
		return apitypes.SnapshotRestoreResult{}, invalid("snapshot %d is on another server than %s now; snapshots stay on the server they were taken on", id, ss)
	}
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	t, isTemplate := templateOf(ss.svc)
	if req.InPlace {
		if req.As != "" {
			return apitypes.SnapshotRestoreResult{}, invalid("an in-place restore keeps the service; leave out the new service's name")
		}
		return c.restoreInPlace(ctx, a, ss, snap)
	}
	if !isTemplate {
		return apitypes.SnapshotRestoreResult{}, invalid("%s isn't a template database, so it restores in place only: its volumes are replaced, after a snapshot of what they hold now. Restore with --in-place (in_place: true)", ss)
	}
	return c.restoreIntoNew(ctx, a, ss, t, snap, req.As)
}

// serviceSnapshot loads snapshot id, which must be of ss.
func (c *Core) serviceSnapshot(ctx context.Context, ss svcScope, id int64) (store.VolumeSnapshot, error) {
	snap, err := c.store.GetSnapshot(ctx, id)
	if store.IsNotFound(err) || (err == nil && snap.ServiceID != ss.svc.ID) {
		return snap, notFound("%s has no snapshot %d; `dock volume snapshot list` shows them", ss, id)
	}
	return snap, err
}

// snapshotMounts mounts the volumes the snapshot holds from vols (the
// target service's), each at its name; every one must be there.
func snapshotMounts(snap store.VolumeSnapshot, vols []store.Volume, target string) ([]node.Mount, error) {
	var mounts []node.Mount
	for _, name := range strings.Fields(snap.Volumes) {
		i := slices.IndexFunc(vols, func(v store.Volume) bool { return v.Name == name })
		if i < 0 {
			return nil, invalid("%s has no volume %s, which snapshot %d holds; add it back first", target, name, snap.ID)
		}
		mounts = append(mounts, node.Mount{Volume: vols[i].DockerName, Path: snapshotVolumesPath + "/" + name})
	}
	return mounts, nil
}

// restoreInPlace starts an in-place restore.
func (c *Core) restoreInPlace(ctx context.Context, a Actor, ss svcScope, snap store.VolumeSnapshot) (apitypes.SnapshotRestoreResult, error) {
	vols, err := c.appliedVolumes(ctx, ss)
	if err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	mounts, err := snapshotMounts(snap, vols, ss.String())
	if err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	if r, err := c.store.GetInFlightRelease(ctx, ss.svc.ID); err == nil {
		return apitypes.SnapshotRestoreResult{}, deployInProgress(ss, r)
	}
	// The safety snapshot is recorded now, so the claim covers both.
	safety, safetyVols, err := c.startSnapshot(ctx, a, ss, apitypes.SnapshotBeforeRestore, fmt.Sprintf("before restoring snapshot %d", snap.ID))
	if err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	c.setRestoringInPlace(ss.svc.ID, true)
	// Read the service again now that StopService refuses to run: a stop
	// that went ahead after ss was loaded is seen here, so the restore
	// leaves the service stopped instead of starting it again.
	svc, err := c.store.GetService(ctx, ss.svc.ID)
	if err != nil {
		c.setRestoringInPlace(ss.svc.ID, false)
		c.failSafety(ctx, safety, "the service couldn't be read")
		c.releaseService(ss.svc.ID)
		return apitypes.SnapshotRestoreResult{}, err
	}
	ss.svc = svc
	row, err := c.store.CreateSnapshotRestore(ctx, store.CreateSnapshotRestoreParams{
		SnapshotID: &snap.ID, ServiceID: ss.svc.ID, ToServiceID: ss.svc.ID, InPlace: true, SafetyID: &safety.ID,
		CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.setRestoringInPlace(ss.svc.ID, false)
		c.releaseService(ss.svc.ID)
		return apitypes.SnapshotRestoreResult{}, err
	}
	c.audit(ctx, a, "snapshot.restore", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("restoring snapshot %d in place; %s stops, and snapshot %d keeps the data it holds now", snap.ID, ss.svc.Name, safety.ID))
	c.wg.Go(func() {
		defer c.releaseService(ss.svc.ID)
		err := c.runRestoreInPlace(c.ctx, ss, snap, safety, safetyVols, mounts)
		// Deploys and stops may go ahead once the restore reads as done.
		c.setRestoringInPlace(ss.svc.ID, false)
		c.finishSnapshotRestore(c.ctx, ss, row, ss.svc.Name, err)
		if c.ctx.Err() == nil {
			c.pruneSnapshots(c.ctx, ss)
		}
	})
	return apitypes.SnapshotRestoreResult{Restore: toSnapshotRestore(row, map[int64]string{ss.svc.ID: ss.svc.Name})}, nil
}

// runRestoreInPlace stops the service, takes the safety snapshot, copies
// the snapshot in and starts the active release again. If the copy fails,
// the safety snapshot is copied back before the start, so a failure leaves
// the service as it was.
func (c *Core) runRestoreInPlace(ctx context.Context, ss svcScope, snap, safety store.VolumeSnapshot, safetyVols []store.Volume, mounts []node.Mount) error {
	active, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	// A service stopped on purpose (ADR 0050) has no container to stop and
	// stays stopped afterwards: its restored data waits for its start.
	running := err == nil && ss.svc.StoppedAt == nil
	if running {
		if err := c.removeReleaseContainers(ctx, ss, active.ID, keepData); err != nil {
			c.failSafety(ctx, safety, "the service couldn't be stopped")
			return fmt.Errorf("stop %s: %w", ss.svc.Name, c.nodeErr(ss.env.ServerID, err))
		}
		c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceStopped})
	}
	start := func() error {
		if !running {
			return nil
		}
		if err := c.restartRelease(ctx, ss, active); err != nil {
			return fmt.Errorf("start release %d again: %w", active.Number, c.nodeErr(ss.env.ServerID, err))
		}
		return nil
	}
	// Stopped, so nothing is paused for it. Retention waits for the end:
	// it could take the snapshot being restored.
	if _, err := c.takeClaimed(ctx, ss, safety, safetyVols, false); err != nil {
		return errors.Join(fmt.Errorf("snapshot the data it replaces, so nothing was restored: %w", err), start())
	}
	if err := c.copySnapshot(ctx, ss, snap, mounts); err != nil {
		if back := c.copySnapshot(ctx, ss, safety, mounts); back != nil {
			return fmt.Errorf("copy the snapshot in: %w; and putting the data back from snapshot %d failed too (%w), so %s stays stopped: restore snapshot %d in place again", err, safety.ID, back, ss.svc.Name, safety.ID)
		}
		return errors.Join(fmt.Errorf("copy the snapshot in: %w; the data was put back from snapshot %d", err, safety.ID), start())
	}
	return start()
}

// failSafety records a safety snapshot that was never taken.
func (c *Core) failSafety(ctx context.Context, safety store.VolumeSnapshot, why string) {
	if err := c.store.FinishSnapshot(ctx, store.FinishSnapshotParams{Status: string(apitypes.SnapshotFailed), Error: why, FinishedAt: ptr(now()), ID: safety.ID}); err != nil {
		c.log.Error("record snapshot", "snapshot", safety.ID, "err", err)
	}
}

// copySnapshot copies snapshot snap into mounts on ss's server.
func (c *Core) copySnapshot(ctx context.Context, ss svcScope, snap store.VolumeSnapshot, mounts []node.Mount) error {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return err
	}
	_, err = c.runSnapshotJob(ctx, ss.env.ServerID, rt, node.JobSpec{
		Name:   fmt.Sprintf("dock_p%d_e%d_s%d_restore_snapshot%d_%d", ss.project.ID, ss.env.ID, ss.svc.ID, snap.ID, now().UnixNano()),
		Labels: c.labels(ss, 0), Script: snapshotRestoreScript, Mounts: mounts,
		Env: map[string]string{"SNAPSHOT": strconv.FormatInt(snap.ID, 10), "VOLUMES": strings.Join(mountNames(mounts), " ")},
	})
	return err
}

func mountNames(mounts []node.Mount) []string {
	out := make([]string, len(mounts))
	for i, m := range mounts {
		out[i] = strings.TrimPrefix(m.Path, snapshotVolumesPath+"/")
	}
	return out
}

// restoreIntoNew starts a restore of a template database's snapshot into
// a new service.
func (c *Core) restoreIntoNew(ctx context.Context, a Actor, ss svcScope, t dbTemplate, snap store.VolumeSnapshot, as string) (apitypes.SnapshotRestoreResult, error) {
	name := cmp.Or(as, fmt.Sprintf("%s-snapshot-%d", ss.svc.Name, snap.ID))
	if err := validName("service", name); err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	if strings.Fields(snap.Volumes)[0] != templateVolume || len(strings.Fields(snap.Volumes)) != 1 {
		return apitypes.SnapshotRestoreResult{}, invalid("snapshot %d holds volumes %s, not the template's %s alone; restore it in place", snap.ID, snap.Volumes, templateVolume)
	}
	keep, err := c.templateLiterals(ctx, ss, t)
	if err != nil {
		return apitypes.SnapshotRestoreResult{}, err
	}
	if !c.claimService(ss.svc.ID) {
		return apitypes.SnapshotRestoreResult{}, conflict("a backup, snapshot or restore of %s is running; wait for it to finish", ss)
	}
	to, err := c.createFromTemplate(ctx, a, ss.envScope, name, t, templateOptions{noDeploy: true, image: ss.svc.Image, keep: keep})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.SnapshotRestoreResult{}, err
	}
	row, err := c.store.CreateSnapshotRestore(ctx, store.CreateSnapshotRestoreParams{
		SnapshotID: &snap.ID, ServiceID: ss.svc.ID, ToServiceID: to.svc.ID, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return apitypes.SnapshotRestoreResult{}, err
	}
	c.audit(ctx, a, "snapshot.restore", &ss.project, &ss.env, ss.String(), fmt.Sprintf("restoring snapshot %d into new service %s", snap.ID, name))
	c.wg.Go(func() {
		err := c.fillFromSnapshot(c.ctx, to, snap)
		c.releaseService(ss.svc.ID) // the copy is done; the deploy is the new service's
		if err != nil {
			c.finishSnapshotRestore(c.ctx, ss, row, name, err)
			return
		}
		if err := c.deployTemplate(c.ctx, a, to, func(ok bool) {
			var err error
			if !ok {
				err = fmt.Errorf("%s failed to start with the snapshot's data; see its release", name)
			}
			c.finishSnapshotRestore(c.ctx, ss, row, name, err)
		}); err != nil {
			c.finishSnapshotRestore(c.ctx, ss, row, name, err)
		}
	})
	svc, err := c.toService(ctx, to, apitypes.PendingChanges{})
	names := map[int64]string{ss.svc.ID: ss.svc.Name, to.svc.ID: name}
	return apitypes.SnapshotRestoreResult{Restore: toSnapshotRestore(row, names), Service: &svc}, err
}

// templateLiterals are the source's live values of the variables its
// template generates as literals (user, password, database): a copy of
// its data files only opens with them. References (DATABASE_URL) are
// made anew for the new name.
func (c *Core) templateLiterals(ctx context.Context, ss svcScope, t dbTemplate) (map[string]string, error) {
	vars, err := c.store.ListVariables(ctx, ss.env.ID)
	if err != nil {
		return nil, err
	}
	gen := t.vars(ss.svc.Name, "")
	keep := map[string]string{}
	for _, v := range vars {
		g, ok := gen[v.Key]
		if v.ServiceID == nil || *v.ServiceID != ss.svc.ID || !ok || strings.Contains(g, "${{") || v.Value == nil {
			continue
		}
		val, err := c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key)
		if err != nil {
			return nil, err
		}
		keep[v.Key] = *val
	}
	return keep, nil
}

// fillFromSnapshot copies a snapshot into a new template service's
// volume, before its first deploy.
func (c *Core) fillFromSnapshot(ctx context.Context, to svcScope, snap store.VolumeSnapshot) error {
	rt, err := c.runtimeFor(to.env.ServerID)
	if err != nil {
		return err
	}
	vol := volumeName(to, templateVolume)
	if err := rt.EnsureVolume(ctx, vol, c.labels(to, 0)); err != nil {
		return fmt.Errorf("create volume: %w", c.nodeErr(to.env.ServerID, err))
	}
	return c.copySnapshot(ctx, to, snap, []node.Mount{{Volume: vol, Path: snapshotVolumesPath + "/" + templateVolume}})
}

// finishSnapshotRestore records and audits a restore's outcome; to is the
// service restored into.
func (c *Core) finishSnapshotRestore(ctx context.Context, ss svcScope, row store.VolumeSnapshotRestore, to string, err error) {
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	fin := store.FinishSnapshotRestoreParams{Status: string(apitypes.BackupSucceeded), FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
	}
	// Recorded before the audit event announces it, so whoever the event
	// wakes (MCP's restore_snapshot) reads the outcome.
	if ferr := c.store.FinishSnapshotRestore(ctx, fin); ferr != nil {
		c.log.Error("record snapshot restore", "restore", row.ID, "err", ferr)
	}
	snapID := deref(row.SnapshotID)
	if err != nil {
		msg := fmt.Sprintf("restoring snapshot %d into %s failed: %v", snapID, to, err)
		if !row.InPlace {
			msg += fmt.Sprintf("; %s is kept for a look, archive it when done", to)
		}
		c.audit(ctx, System, "snapshot.restore_failed", &ss.project, &ss.env, ss.String(), msg)
	} else if row.InPlace {
		c.audit(ctx, System, "snapshot.restored", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("restored snapshot %d in place; snapshot %d holds the data it replaced", snapID, deref(row.SafetyID)))
	} else {
		t, _ := templateOf(ss.svc)
		c.audit(ctx, System, "snapshot.restored", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("restored snapshot %d into %s; point references at ${{%s.%s}} when it checks out", snapID, to, to, t.exports[0]))
	}
}

// DeleteSnapshot deletes a snapshot's files and its record. Refused while
// a backup, snapshot or restore of the service runs, or while the server
// that holds it is offline. Scope: deploy.
func (c *Core) DeleteSnapshot(ctx context.Context, a Actor, ref ServiceRef, id int64) error {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "deleting a snapshot")
	if err != nil {
		return err
	}
	snap, err := c.serviceSnapshot(ctx, ss, id)
	if err != nil {
		return err
	}
	if !c.claimService(ss.svc.ID) {
		return conflict("a backup, snapshot or restore of %s is running; delete the snapshot once it finishes", ss)
	}
	defer c.releaseService(ss.svc.ID)
	if err := c.deleteSnapshots(ctx, snap.ServerID, []store.VolumeSnapshot{snap}); err != nil {
		return err
	}
	c.audit(ctx, a, "snapshot.delete", &ss.project, &ss.env, ss.String(), fmt.Sprintf("deleted snapshot %d (%s)", id, humanBytes(snap.SizeBytes)))
	return nil
}

// deleteSnapshots removes snapshots' files on their server, then their
// rows. The caller holds the claim on their services.
func (c *Core) deleteSnapshots(ctx context.Context, serverID int64, snaps []store.VolumeSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	rt, err := c.runtimeFor(serverID)
	if err != nil {
		return err
	}
	ids := make([]string, len(snaps))
	for i, s := range snaps {
		ids[i] = strconv.FormatInt(s.ID, 10)
	}
	_, err = c.runSnapshotJob(ctx, serverID, rt, node.JobSpec{
		Name:   fmt.Sprintf("dock_snapshot_delete_%d", now().UnixNano()),
		Labels: node.Labels{ControlPlane: c.install}, Script: snapshotDeleteScript,
		Env: map[string]string{"SNAPSHOTS": strings.Join(ids, " ")},
	})
	if err != nil {
		return fmt.Errorf("delete the snapshot's files: %w", err)
	}
	for _, s := range snaps {
		if err := c.store.DeleteSnapshot(ctx, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// pruneSnapshots applies retention to a service after a new snapshot:
// the newest ready ones up to keep, none older than the age limit, and
// no failed ones but the newest (its error stays readable). Failures are
// logged; the next snapshot or the daily pass tries again. The caller
// holds the claim on the service.
func (c *Core) pruneSnapshots(ctx context.Context, ss svcScope) {
	keep, days := c.snapshotKeep()
	rows, err := c.store.ListServiceSnapshots(ctx, store.ListServiceSnapshotsParams{ServiceID: ss.svc.ID, Limit: snapshotListLimit})
	if err != nil {
		c.log.Warn("list snapshots for retention", "service", ss.String(), "err", err)
		return
	}
	cutoff := now().AddDate(0, 0, -days)
	ready, failed := 0, 0
	byServer := map[int64][]store.VolumeSnapshot{}
	for _, r := range rows { // newest first
		switch r.Status {
		case string(apitypes.SnapshotReady):
			ready++
			if ready <= keep && !r.StartedAt.Before(cutoff) {
				continue
			}
		case string(apitypes.SnapshotFailed):
			failed++
			if failed <= 1 && !r.StartedAt.Before(cutoff) {
				continue
			}
		default:
			continue
		}
		byServer[r.ServerID] = append(byServer[r.ServerID], r)
	}
	for _, serverID := range slices.Sorted(maps.Keys(byServer)) {
		snaps := byServer[serverID]
		if err := c.deleteSnapshots(ctx, serverID, snaps); err != nil {
			c.log.Warn("prune snapshots", "service", ss.String(), "err", err)
			continue
		}
		c.audit(ctx, System, "snapshot.pruned", &ss.project, &ss.env, ss.String(), fmt.Sprintf("retention deleted %d snapshots (keeps %d, for %d days)", len(snaps), keep, days))
	}
}

// pruneExpiredSnapshots deletes every finished snapshot older than the
// age limit, service by service; services busy now and offline servers
// wait for the next pass. The backup scheduler calls it on each pass, at
// least daily: no timer of its own.
func (c *Core) pruneExpiredSnapshots(ctx context.Context) {
	_, days := c.snapshotKeep()
	rows, err := c.store.ListExpiredSnapshots(ctx, store.ListExpiredSnapshotsParams{StartedAt: now().AddDate(0, 0, -days), Limit: snapshotListLimit})
	if err != nil || len(rows) == 0 {
		if err != nil {
			c.log.Warn("list expired snapshots", "err", err)
		}
		return
	}
	bySvc := map[int64]bool{}
	for _, r := range rows {
		if bySvc[r.ServiceID] || !c.online(r.ServerID) {
			continue
		}
		bySvc[r.ServiceID] = true
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			c.log.Warn("prune snapshots", "service", r.ServiceID, "err", err)
			continue
		}
		if !c.claimService(ss.svc.ID) {
			continue
		}
		c.pruneSnapshots(ctx, ss)
		c.releaseService(ss.svc.ID)
	}
}

// failInterruptedSnapshots marks snapshots and restores a restart cut
// short as failed. An in-place restore stopped its service and may have
// replaced part of its data: it stays stopped, and the error says how to
// finish (restore again) or undo (restore the safety snapshot).
func (c *Core) failInterruptedSnapshots(ctx context.Context) error {
	n, err := c.store.FailRunningSnapshots(ctx, store.FailRunningSnapshotsParams{Error: "the daemon restarted while this was running; take it again", FinishedAt: ptr(now())})
	if err != nil {
		return err
	}
	if n > 0 {
		c.log.Warn("snapshots interrupted by a restart marked failed", "count", n)
	}
	restores, err := c.store.ListRunningSnapshotRestores(ctx)
	if err != nil {
		return err
	}
	for _, r := range restores {
		msg := "the daemon restarted during the restore; restore the snapshot again"
		if r.InPlace {
			msg = fmt.Sprintf("the daemon restarted during the restore, which may have replaced part of the data and left the service stopped: restore snapshot %d in place again, or snapshot %d (the data it replaced) to undo", deref(r.SnapshotID), deref(r.SafetyID))
		}
		if err := c.store.FinishSnapshotRestore(ctx, store.FinishSnapshotRestoreParams{Status: string(apitypes.BackupFailed), Error: msg, FinishedAt: ptr(now()), ID: r.ID}); err != nil {
			return err
		}
		if ss, err := c.scopeOf(ctx, r.ServiceID); err == nil {
			c.audit(ctx, System, "snapshot.restore_failed", &ss.project, &ss.env, ss.String(), msg)
		}
	}
	return nil
}
