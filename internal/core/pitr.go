package core

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// Point-in-time restore for Postgres template databases (ADR 0093). While
// it is on, the database's container archives each finished WAL segment
// with archive_command (dock-probe wal-push) to its archiver, a sidecar in
// the template's image that holds the storage keys and uploads it next to
// the backups (dock __wal serve). Base backups run with the backup
// schedule (pitr_backup.go); restores to a time are pitr_restore.go.

// Defaults and limits of the restorable window, in days.
const (
	pitrDefaultDays = 7
	pitrMaxDays     = 35
)

// walArchiveTimeout is Postgres's archive_timeout while archiving: a quiet
// database still reaches the archive within it.
const walArchiveTimeout = 60

// archiverMemoryMB caps the archiver: one segment compressed and sealed
// in memory at a time.
const archiverMemoryMB = 128

// pitrState is which databases archive, for containerSpec, which runs
// without a store read.
type pitrState struct {
	mu sync.Mutex
	on map[int64]bool
	// first holds the databases whose first base backup of a generation
	// is being taken, so two releases going active take one.
	first map[int64]bool
	// archivers holds one lock per database for starting and removing
	// its archiver (holdArchiver).
	archivers map[int64]*sync.Mutex
}

// holdArchiver serializes starting and removing one database's archiver,
// across the node calls too: a start that follows a release going active
// runs after the request that made the release has returned, and without
// this it could start the archiver again after a turn-off removed it.
func (c *Core) holdArchiver(serviceID int64) (release func()) {
	c.pitr.mu.Lock()
	if c.pitr.archivers == nil {
		c.pitr.archivers = map[int64]*sync.Mutex{}
	}
	mu := c.pitr.archivers[serviceID]
	if mu == nil {
		mu = &sync.Mutex{}
		c.pitr.archivers[serviceID] = mu
	}
	c.pitr.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// holdFirstBase marks a database's first base backup as being taken;
// false if it already is.
func (c *Core) holdFirstBase(serviceID int64) bool {
	c.pitr.mu.Lock()
	defer c.pitr.mu.Unlock()
	if c.pitr.first[serviceID] {
		return false
	}
	if c.pitr.first == nil {
		c.pitr.first = map[int64]bool{}
	}
	c.pitr.first[serviceID] = true
	return true
}

func (c *Core) releaseFirstBase(serviceID int64) {
	c.pitr.mu.Lock()
	delete(c.pitr.first, serviceID)
	c.pitr.mu.Unlock()
}

// pitrOn reports whether a database's containers start archiving.
func (c *Core) pitrOn(serviceID int64) bool {
	c.pitr.mu.Lock()
	defer c.pitr.mu.Unlock()
	return c.pitr.on[serviceID]
}

func (c *Core) setPITROn(serviceID int64, on bool) {
	c.pitr.mu.Lock()
	defer c.pitr.mu.Unlock()
	if c.pitr.on == nil {
		c.pitr.on = map[int64]bool{}
	}
	if on {
		c.pitr.on[serviceID] = true
	} else {
		delete(c.pitr.on, serviceID)
	}
}

// archiverName is the archiver container of a database.
func archiverName(ss svcScope) string {
	return fmt.Sprintf("dock_p%d_e%d_s%d_wal", ss.project.ID, ss.env.ID, ss.svc.ID)
}

// archiverHost is the archiver's name on the environment network. The
// dot keeps it apart from every service name (nameRe has none).
func archiverHost(serviceID int64) string {
	return "wal-s" + strconv.FormatInt(serviceID, 10) + ".dock"
}

// archiveFlags are the Postgres settings a container archives with.
// wal_recycle=off: a fresh segment is zero-filled, so the unused tail of
// one archive_timeout ends compresses to nothing (ADR 0093).
func archiveFlags(serviceID int64) []string {
	return []string{
		"-c", "archive_mode=on", "-c", "archive_timeout=" + strconv.Itoa(walArchiveTimeout), "-c", "wal_recycle=off",
		"-c", "archive_command=" + node.ProbePath + " wal-push " + archiverHost(serviceID) + ":" + strconv.Itoa(walarchive.Port) + " %p %f",
	}
}

// runsPostgres reports whether a start command runs the official image's
// entrypoint with postgres last, so flags can follow it.
func runsPostgres(cmd string) bool { return strings.Contains(cmd, "docker-entrypoint.sh postgres") }

// addArchiving gives a database's container the archive settings while
// point-in-time restore is on, after its start command (not in it: clones,
// previews and exports never carry them), and the ongoing check that
// mounts dock-probe, which archive_command runs.
func (c *Core) addArchiving(spec *node.ContainerSpec, ss svcScope) {
	t, ok := templateOf(ss.svc)
	if !ok || t.database != apitypes.DatabasePostgres || !c.pitrOn(ss.svc.ID) {
		return
	}
	flags := archiveFlags(ss.svc.ID)
	switch {
	case len(spec.Args) > 0:
		spec.Args = append(slices.Clone(spec.Args), flags...)
	case runsPostgres(spec.Cmd):
		var b strings.Builder
		b.WriteString(strings.TrimSpace(spec.Cmd))
		for _, f := range flags {
			b.WriteString(" '" + f + "'")
		}
		spec.Cmd = b.String()
	default:
		c.log.Warn("point-in-time restore: the start command doesn't run docker-entrypoint.sh postgres, so the database doesn't archive", "service", ss.String())
		return
	}
	if spec.Health == nil {
		spec.Health = liveness(releaseConfig{HealthPort: t.port, HealthKind: t.health})
	}
}

// pitrUnavailable says why point-in-time restore can't be on for ss now,
// "" when it can.
func pitrUnavailable(ss svcScope, set store.BackupSetting) string {
	t, ok := templateOf(ss.svc)
	switch {
	case !ok || t.database != apitypes.DatabasePostgres:
		return "point-in-time restore is for Postgres template databases"
	case set.StorageID == nil:
		return "backups are kept on each server, not off-box; move them to a storage connection first (`dock backup move --storage NAME`)"
	case !runsPostgres(ss.svc.StartCommand):
		return "the start command doesn't run docker-entrypoint.sh postgres, so Dock can't add the archive settings to it"
	}
	return ""
}

// walConfig is where generation of a database's archive is, and the
// database's key (walarchive.ServiceKey).
func (c *Core) walConfig(ctx context.Context, serviceID int64, generation string) (walarchive.Config, error) {
	set, err := c.backupSettings(ctx)
	if err != nil {
		return walarchive.Config{}, err
	}
	if set.StorageID == nil {
		return walarchive.Config{}, invalid("point-in-time restore needs backups on a storage connection; move them there with `dock backup move --storage NAME`")
	}
	conn, err := c.storageByID(ctx, *set.StorageID)
	if err != nil {
		return walarchive.Config{}, err
	}
	b, err := c.bucketOf(conn)
	if err != nil {
		return walarchive.Config{}, err
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return walarchive.Config{}, err
	}
	archive, err := walarchive.DeriveKey(repo.Password)
	if err != nil {
		return walarchive.Config{}, err
	}
	// The database's own key: an archiver can open only its archive.
	key, err := walarchive.ServiceKey(archive, serviceID)
	if err != nil {
		return walarchive.Config{}, err
	}
	return walarchive.Config{Bucket: b, Prefix: walarchive.Prefix(set.StoragePath, serviceID, generation), Key: key}, nil
}

// newGeneration names a new archive generation.
func newGeneration() string { return "g" + strconv.FormatInt(now().UnixMilli(), 10) }

// dataVolume is a template database's applied data volume.
func (c *Core) dataVolume(ctx context.Context, ss svcScope) (store.Volume, error) {
	vols, err := c.appliedVolumes(ctx, ss)
	if err != nil {
		return store.Volume{}, err
	}
	for _, v := range vols {
		if v.Name == templateVolume {
			return v, nil
		}
	}
	return store.Volume{}, invalid("%s has no %s volume yet; deploy it first", ss, templateVolume)
}

// archiverSpec is a database's archiver container.
func (c *Core) archiverSpec(ctx context.Context, ss svcScope, row store.Pitr) (node.SidecarSpec, error) {
	cfg, err := c.walConfig(ctx, ss.svc.ID, row.Generation)
	if err != nil {
		return node.SidecarSpec{}, err
	}
	vol, err := c.dataVolume(ctx, ss)
	if err != nil {
		return node.SidecarSpec{}, err
	}
	return node.SidecarSpec{
		Name: archiverName(ss), Image: jobImage(ss), Labels: c.labels(ss, 0), Env: cfg.Env(nil),
		Args:    []string{node.DockPath, walarchive.Command, "serve", "/pgdata"},
		Network: networkName(ss.envScope), Aliases: []string{archiverHost(ss.svc.ID)},
		// Read-only: the archiver only looks at pg_wal/archive_status.
		Mounts:    []node.Mount{{Volume: vol.DockerName, Path: "/pgdata", ReadOnly: true}},
		Resources: node.Resources{MemoryBytes: archiverMemoryMB << 20, NanoCPUs: 5e8},
		Health: &node.LivenessCheck{
			Port: walarchive.Port, Path: "/health", Interval: 30 * time.Second, Timeout: 5 * time.Second,
			Retries: 3, StartPeriod: 30 * time.Second, StartInterval: 2 * time.Second,
		},
	}, nil
}

// ensureArchiver runs a database's archiver on its server as row says,
// and removes the one it left on another server (an environment that
// moved). For the request that turns archiving on, whose row isn't stored
// yet; everything after it uses keepArchiver.
func (c *Core) ensureArchiver(ctx context.Context, ss svcScope, row store.Pitr) error {
	defer c.holdArchiver(ss.svc.ID)()
	return c.startArchiver(ctx, ss, row)
}

// keepArchiver runs a database's archiver as its stored row says now, and
// returns that row; on is false, and nothing starts, when archiving is
// off. Work that follows a release, a restore or a move calls this, never
// ensureArchiver with a row it read earlier: archiving may have been
// turned off since, and its archiver removed.
func (c *Core) keepArchiver(ctx context.Context, ss svcScope) (row store.Pitr, on bool, err error) {
	defer c.holdArchiver(ss.svc.ID)()
	row, err = c.store.GetPITR(ctx, ss.svc.ID)
	if err != nil || !row.Enabled {
		return row, false, nil
	}
	return row, true, c.startArchiver(ctx, ss, row)
}

// startArchiver is ensureArchiver with the database's archiver held.
func (c *Core) startArchiver(ctx context.Context, ss svcScope, row store.Pitr) error {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return err
	}
	spec, err := c.archiverSpec(ctx, ss, row)
	if err != nil {
		return err
	}
	if err := rt.EnsureNetwork(ctx, spec.Network, node.Labels{ControlPlane: c.install, Project: spec.Labels.Project, Environment: spec.Labels.Environment}); err != nil {
		return c.nodeErr(ss.env.ServerID, err)
	}
	if err := c.ensureImage(ctx, ss.env.ServerID, rt, spec.Image); err != nil {
		return err
	}
	if err := rt.EnsureSidecar(ctx, spec); err != nil {
		return fmt.Errorf("start the WAL archiver: %w", c.nodeErr(ss.env.ServerID, err))
	}
	if row.ServerID != nil && *row.ServerID != ss.env.ServerID {
		c.removeArchiverFrom(ctx, ss, *row.ServerID)
	}
	if row.ServerID == nil || *row.ServerID != ss.env.ServerID {
		return c.store.SetPITRServer(ctx, store.SetPITRServerParams{ServerID: &ss.env.ServerID, ServiceID: ss.svc.ID})
	}
	return nil
}

// removeArchiverFrom removes a database's archiver from a server, logging
// a failure: a stray archiver only waits for WAL that never comes.
func (c *Core) removeArchiverFrom(ctx context.Context, ss svcScope, serverID int64) {
	rt, err := c.runtimeFor(serverID)
	if err == nil {
		err = rt.RemoveSidecar(ctx, archiverName(ss), c.labels(ss, 0))
	}
	if err != nil {
		c.log.Warn("remove the WAL archiver", "service", ss.String(), "server", serverID, "err", err)
	}
}

// removeArchiver removes a database's archiver wherever it runs and
// records it gone.
func (c *Core) removeArchiver(ctx context.Context, ss svcScope) {
	defer c.holdArchiver(ss.svc.ID)()
	row, err := c.store.GetPITR(ctx, ss.svc.ID)
	if err != nil || row.ServerID == nil {
		return
	}
	c.removeArchiverFrom(ctx, ss, *row.ServerID)
	if err := c.store.SetPITRServer(ctx, store.SetPITRServerParams{ServerID: nil, ServiceID: ss.svc.ID}); err != nil {
		c.log.Warn("record the WAL archiver removed", "service", ss.String(), "err", err)
	}
}

// SetPITR turns a Postgres database's point-in-time restore on or off, or
// changes its window (ADR 0093). Operators, like schedules: it costs
// storage. Turning it on or off restarts the database once (a restart
// release with or without the archive settings); on, it starts the
// archiver first and takes a base backup once the database runs. Off
// when it is off already restarts nothing, and changes nothing for a
// database that never had it.
func (c *Core) SetPITR(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetPITRRequest) (apitypes.ServiceBackups, error) {
	if err := a.requirePlatform(BackupsManage, "changing point-in-time restore"); err != nil {
		return apitypes.ServiceBackups{}, err
	}
	ss, err := c.loadService(ctx, a.OrgID, ref)
	if err != nil {
		return apitypes.ServiceBackups{}, err
	}
	if err := a.checkLock(ss.envScope, "changing point-in-time restore"); err != nil {
		return apitypes.ServiceBackups{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.ServiceBackups{}, invalid("service %s is archived", ss)
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.ServiceBackups{}, err
	}
	cur, err := c.store.GetPITR(ctx, ss.svc.ID)
	exists := err == nil
	if err != nil && !store.IsNotFound(err) {
		return apitypes.ServiceBackups{}, err
	}
	if !exists {
		cur = store.Pitr{ServiceID: ss.svc.ID, Days: pitrDefaultDays}
	}
	next := cur
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if req.Days != nil {
		if *req.Days < 1 || *req.Days > pitrMaxDays {
			return apitypes.ServiceBackups{}, invalid("days must be between 1 and %d", pitrMaxDays)
		}
		next.Days = int64(*req.Days)
	}
	turnOn, turnOff := next.Enabled && !cur.Enabled, cur.Enabled && !next.Enabled
	if !exists && !next.Enabled {
		// Off for a database that never had it: nothing to record, restart
		// or audit.
		return c.serviceBackups(ctx, ss)
	}
	if turnOn {
		if why := pitrUnavailable(ss, set); why != "" {
			return apitypes.ServiceBackups{}, invalid("%s: %s", ss, why)
		}
		if _, err := c.store.GetActiveRelease(ctx, ss.svc.ID); store.IsNotFound(err) {
			return apitypes.ServiceBackups{}, invalid("%s has no active release; deploy it first", ss)
		} else if err != nil {
			return apitypes.ServiceBackups{}, err
		}
		// A new generation: what was archived before isn't one history
		// with what comes now.
		next.Generation = newGeneration()
	}
	next.UpdatedBy, next.UpdatedAt = a.Name, now()
	if turnOn {
		// The archiver first, so the database finds it when it restarts.
		if err := c.ensureArchiver(ctx, ss, next); err != nil {
			return apitypes.ServiceBackups{}, err
		}
		if row, err := c.store.GetPITR(ctx, ss.svc.ID); err == nil {
			next.ServerID = row.ServerID
		}
	}
	if err := c.store.UpsertPITR(ctx, store.UpsertPITRParams{
		ServiceID: ss.svc.ID, Enabled: next.Enabled, Days: next.Days, Generation: next.Generation, ServerID: next.ServerID,
		UpdatedBy: next.UpdatedBy, UpdatedAt: next.UpdatedAt,
	}); err != nil {
		return apitypes.ServiceBackups{}, err
	}
	c.setPITROn(ss.svc.ID, next.Enabled)
	summary := fmt.Sprintf("kept %d days of point-in-time restore", next.Days)
	switch {
	case turnOn:
		summary = fmt.Sprintf("turned on point-in-time restore, %d days", next.Days)
	case turnOff:
		summary = "turned off point-in-time restore; its window ages out"
		c.signalAlert(ctx, alertSignal{Key: pitrAlertKey(ss), Resolve: true})
	}
	if turnOn || turnOff {
		if err := c.restartForPITR(ctx, a, ss); err != nil {
			return apitypes.ServiceBackups{}, err
		}
		if turnOff {
			c.removeArchiver(ctx, ss)
		}
	}
	c.audit(ctx, a, "backup.pitr", &ss.project, &ss.env, ss.String(), summary)
	return c.serviceBackups(ctx, ss)
}

// restartForPITR starts a restart release of a database, so its new
// container starts with or without the archive settings (addArchiving).
func (c *Core) restartForPITR(ctx context.Context, a Actor, ss svcScope) error {
	active, err := c.restartable(ctx, ss)
	if err != nil {
		return err
	}
	if ss.svc.StoppedAt != nil || ss.env.AsleepAt != nil {
		return nil // its next start makes the container anew
	}
	if _, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceRestart, from: &active}); err != nil {
		return fmt.Errorf("restart %s with the new archive settings: %w", ss.svc.Name, err)
	}
	return nil
}

// pitrAfterActive keeps a database's archiver running where its release
// went active, and takes the first base backup of a generation once the
// database archives. Called when any release of it goes active.
func (c *Core) pitrAfterActive(ctx context.Context, ss svcScope) {
	if !c.pitrOn(ss.svc.ID) {
		return
	}
	row, on, err := c.keepArchiver(ctx, ss)
	if err != nil {
		c.log.Warn("start the WAL archiver", "service", ss.String(), "err", err)
		c.signalAlert(ctx, pitrAlert(ss, "its archiver couldn't start: "+err.Error()))
		return
	}
	if !on {
		return
	}
	if !c.holdFirstBase(ss.svc.ID) {
		return
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil || slices.ContainsFunc(bases, func(b store.Backup) bool { return b.WalGeneration == row.Generation && b.WalStart != "" }) {
		c.releaseFirstBase(ss.svc.ID)
		return
	}
	c.wg.Go(func() {
		defer c.releaseFirstBase(ss.svc.ID)
		row, err := c.startBase(c.ctx, System, ss, "manual")
		if err != nil {
			c.log.Warn("first base backup", "service", ss.String(), "err", err)
			return
		}
		c.runBase(c.ctx, ss, row)
	})
}

// pitrAfterMove starts point-in-time restore over in the backups' new
// storage: the WAL wasn't copied (the move's cleanup deletes it with the
// old folder), so every database's base backups stop restoring to a
// time, and each that archives starts a new generation there with a
// base backup.
func (c *Core) pitrAfterMove(ctx context.Context) {
	if err := c.store.DetachBaseBackupsWAL(ctx); err != nil {
		c.log.Warn("detach base backups from their WAL", "err", err)
	}
	rows, err := c.store.ListPITR(ctx)
	if err != nil {
		c.log.Warn("point-in-time restore after a move", "err", err)
		return
	}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		ss, err := c.scopeOf(ctx, r.ServiceID)
		if err != nil {
			continue
		}
		c.pitrNewGeneration(ctx, ss)
		c.pitrAfterActive(ctx, ss)
	}
}

// loadPITR reads which databases archive, at start, and makes sure their
// archivers run (and that those of databases turned off are gone).
func (c *Core) loadPITR(ctx context.Context) error {
	rows, err := c.store.ListPITR(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Enabled {
			c.setPITROn(r.ServiceID, true)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	c.wg.Go(func() {
		for _, r := range rows {
			ss, err := c.scopeOf(c.ctx, r.ServiceID)
			if err != nil {
				continue
			}
			switch {
			case !r.Enabled || ss.svc.ArchivedAt != nil:
				c.removeArchiver(c.ctx, ss)
			case c.online(ss.env.ServerID):
				if _, _, err := c.keepArchiver(c.ctx, ss); err != nil {
					c.log.Warn("start the WAL archiver", "service", ss.String(), "err", err)
				}
			}
		}
	})
	return nil
}

// pitrArchived turns point-in-time restore off for a database that was
// archived or is going away, and removes its archiver.
func (c *Core) pitrArchived(ctx context.Context, ss svcScope) {
	if !c.pitrOn(ss.svc.ID) {
		return
	}
	c.setPITROn(ss.svc.ID, false)
	if row, err := c.store.GetPITR(ctx, ss.svc.ID); err == nil {
		row.Enabled = false
		if err := c.store.UpsertPITR(ctx, store.UpsertPITRParams{
			ServiceID: row.ServiceID, Enabled: false, Days: row.Days, Generation: row.Generation, ServerID: row.ServerID,
			UpdatedBy: "system", UpdatedAt: now(),
		}); err != nil {
			c.log.Warn("turn off point-in-time restore", "service", ss.String(), "err", err)
		}
	}
	c.removeArchiver(ctx, ss)
	c.signalAlert(ctx, alertSignal{Key: pitrAlertKey(ss), Resolve: true})
}

// archiverStatus asks a database's archiver how archiving does, through
// the node link.
func (c *Core) archiverStatus(ctx context.Context, ss svcScope) (walarchive.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return walarchive.Status{}, err
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return rt.Dial(ctx, node.DialRequest{ContainerID: archiverName(ss), Port: walarchive.Port})
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://archiver/status", nil)
	if err != nil {
		return walarchive.Status{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return walarchive.Status{}, fmt.Errorf("the WAL archiver doesn't answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var st walarchive.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return walarchive.Status{}, fmt.Errorf("the WAL archiver's status: %w", err)
	}
	return st, nil
}

// pitrInfo is what the Backups page shows of point-in-time restore, for
// Postgres template databases (nil for others).
func (c *Core) pitrInfo(ctx context.Context, ss svcScope, set store.BackupSetting) (*apitypes.PITR, error) {
	if t, ok := templateOf(ss.svc); !ok || t.database != apitypes.DatabasePostgres {
		return nil, nil
	}
	out := &apitypes.PITR{Days: pitrDefaultDays}
	row, err := c.store.GetPITR(ctx, ss.svc.ID)
	switch {
	case err == nil:
		out.Enabled, out.Days = row.Enabled, int(row.Days)
	case !store.IsNotFound(err):
		return nil, err
	}
	if !out.Enabled {
		out.Unavailable = pitrUnavailable(ss, set)
	}
	bases, err := c.store.ListPITRBases(ctx, ss.svc.ID)
	if err != nil {
		return nil, err
	}
	for _, b := range bases {
		if b.WalStart == "" || b.ConsistentAt == nil || b.Repository != set.Repository {
			continue // copied by a move without its WAL
		}
		out.BaseBackups++
		if out.From == nil {
			out.From = b.ConsistentAt
		}
	}
	out.WALBytes = row.WalBytes // zero without a row
	out.UsedBy, out.Deploys = []string{}, []apitypes.PITRDeploy{}
	if out.From != nil {
		if out.UsedBy, out.Deploys, err = c.dbUsers(ctx, ss, *out.From); err != nil {
			return nil, err
		}
	}
	if !out.Enabled || ss.svc.StoppedAt != nil {
		return out, nil
	}
	st, err := c.archiverStatus(ctx, ss)
	if err != nil {
		out.Archiver = &apitypes.PITRArchiver{Problem: err.Error()}
		return out, nil
	}
	out.Archiver = &apitypes.PITRArchiver{
		Healthy: st.Healthy, Problem: st.Problem, Waiting: st.Waiting, WaitingBytes: st.WaitingBytes, OldestWaiting: st.OldestWaiting,
		DiskBytes: st.DiskBytes, DiskFree: st.DiskFree, LastError: st.LastError, LastErrorAt: st.LastErrorAt,
	}
	if out.From != nil && st.Through != nil && st.Through.After(*out.From) {
		out.Through = st.Through
	}
	return out, nil
}

// maxPITRDeploys caps the deploy marks of a window.
const maxPITRDeploys = 50

// dbUsers are the live services of ss's environment whose variables
// reference it (a shared variable reaches them all), and their releases
// that went live since: two queries.
func (c *Core) dbUsers(ctx context.Context, ss svcScope, since time.Time) ([]string, []apitypes.PITRDeploy, error) {
	svcs, err := c.store.ListServices(ctx, ss.env.ID)
	if err != nil {
		return nil, nil, err
	}
	vars, err := c.store.ListVariables(ctx, ss.env.ID)
	if err != nil {
		return nil, nil, err
	}
	names := map[int64]string{}
	for _, s := range svcs {
		if s.ArchivedAt == nil && s.ID != ss.svc.ID {
			names[s.ID] = s.Name
		}
	}
	users := map[string]bool{}
	for _, v := range vars {
		if v.Value == nil || (v.ServiceID != nil && names[*v.ServiceID] == "") {
			continue
		}
		val, err := c.openValue(v.Value, variableAAD(v.EnvironmentID, v.ServiceID, v.Key), v.Key)
		if err != nil || val == nil {
			continue
		}
		hit := false
		for _, m := range refRe.FindAllStringSubmatch(*val, -1) {
			hit = hit || m[1] == ss.svc.Name
		}
		switch {
		case !hit:
		case v.ServiceID == nil:
			for _, n := range names {
				users[n] = true
			}
		default:
			users[names[*v.ServiceID]] = true
		}
	}
	out := slices.Sorted(maps.Keys(users))
	deploys := []apitypes.PITRDeploy{}
	if len(out) == 0 {
		return out, deploys, nil
	}
	rows, err := c.store.ListEnvironmentDeploysSince(ctx, store.ListEnvironmentDeploysSinceParams{EnvironmentID: ss.env.ID, Since: &since, Lim: 4 * maxPITRDeploys})
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if users[r.Service] && r.FinishedAt != nil && len(deploys) < maxPITRDeploys {
			deploys = append(deploys, apitypes.PITRDeploy{Service: r.Service, Number: r.Number, At: *r.FinishedAt})
		}
	}
	return out, deploys, nil
}

// pitrAlertKey names a database's archiving alert.
func pitrAlertKey(ss svcScope) string {
	return "pitr:" + alertServiceKey(ss.svc.ID, ss.project.Name, ss.env.Name, ss.svc.Name)
}

// pitrAlert is the alert of a database whose archiving is behind.
func pitrAlert(ss svcScope, problem string) alertSignal {
	svc := serviceKey(ss.project.Name, ss.env.Name, ss.svc.Name)
	return alertSignal{
		Key: pitrAlertKey(ss), serviceID: ss.svc.ID, projectID: ss.project.ID, Kind: apitypes.AlertBackupFailed, Severity: apitypes.SeverityWarning,
		Title: "Archiving of " + svc + " is behind",
		Message: fmt.Sprintf("Point-in-time archiving of %s is behind: %s. Postgres keeps every WAL file it couldn't archive, so its disk fills until archiving catches up; "+
			"fix the backup storage, or turn point-in-time restore off (`dock backup pitr off`), which restarts it without archiving and frees the space.",
			ss.svc.Name, strings.TrimRight(problem, ". ")),
		Command: "dock backup list " + svcFlags(ss.project.Name, ss.env.Name, ss.svc.Name),
		Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
	}
}

// onArchiverEvent follows a database archiver's health check (Docker's
// health_status events): unhealthy opens the database's archiving alert
// with what the archiver says, healthy resolves it. Nothing polls.
func (c *Core) onArchiverEvent(ctx context.Context, serverID int64, ev node.ContainerEvent) {
	if ev.Action != "health_status" {
		return
	}
	id, err := strconv.ParseInt(ev.Labels.Service, 10, 64)
	if err != nil {
		return
	}
	ss, err := c.scopeOf(ctx, id)
	if err != nil || ss.env.ServerID != serverID {
		return
	}
	if !c.pitrOn(id) {
		return
	}
	switch ev.Health {
	case node.HealthHealthy:
		c.signalAlert(ctx, alertSignal{Key: pitrAlertKey(ss), Resolve: true})
	case node.HealthUnhealthy:
		problem := "its archiver's health check fails"
		if st, err := c.archiverStatus(ctx, ss); err != nil {
			problem = err.Error()
		} else if st.Problem != "" {
			problem = st.Problem
		}
		c.signalAlert(ctx, pitrAlert(ss, problem))
	}
}
