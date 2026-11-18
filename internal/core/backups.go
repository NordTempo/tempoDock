package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/backup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/walarchive"
)

// Backups (ADR 0019): every stateful service is backed up daily with
// restic, template databases with their own tool. A backup is a job
// container on the service's server (node.Containers.RunJob); its row in
// the backups table is the record. backups_schedule.go decides when,
// backups_restore.go restores into new services.

// volumeBackupWarning goes with file-level backups of generic volumes.
const volumeBackupWarning = "File-level backup of a running service's volumes: files a database is writing can be inconsistent. Database templates back up with the database's own tool."

// jobMemoryMB caps backup and restore jobs, so a large dump can't starve
// the apps next to it.
const jobMemoryMB = 768

// serviceBackupRuns is how many runs a service's backup list shows.
const serviceBackupRuns = 50

// backupLogRuns is how many of a service's newest runs keep their output
// for View log: at most 20 x 16 KB a service.
const backupLogRuns = 20

// backupState is the in-memory side of backups.
type backupState struct {
	// kick wakes the scheduler (schedule or settings changed, a server
	// reconnected, a release went active).
	kick chan struct{}

	mu sync.Mutex
	// running holds the services with a backup or restore in flight.
	running map[int64]bool
	// freed is closed (and replaced) whenever a service's claim is
	// released, waking claimServiceWait.
	freed chan struct{}
	// restic holds the servers whose restic volume was filled since the
	// daemon started.
	restic map[int64]bool
	// controlPlane is set while the control plane's own backup runs
	// (backups_controlplane.go).
	controlPlane bool
	// move is the latest move of the backups (backups_move.go); while it
	// runs, no backup or restore starts. moveTo is its storage connection.
	move   *apitypes.BackupMove
	moveTo int64
}

// moving reports whether a backup move runs. The caller holds mu.
func (s *backupState) moving() bool { return s.move != nil && s.move.Running }

// backupsMoving reports whether a backup move runs.
func (c *Core) backupsMoving() bool {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	return c.backups.moving()
}

func newBackupState() backupState {
	return backupState{kick: make(chan struct{}, 1), running: map[int64]bool{}, freed: make(chan struct{}), restic: map[int64]bool{}}
}

// kickBackups wakes the backup scheduler; it never blocks.
func (c *Core) kickBackups() {
	select {
	case c.backups.kick <- struct{}{}:
	default:
	}
}

// claimService marks a backup or restore of a service as running; false if
// one already is.
func (c *Core) claimService(serviceID int64) bool {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	if c.backups.running[serviceID] || c.backups.moving() {
		return false
	}
	c.backups.running[serviceID] = true
	return true
}

// claimServiceWait claims a service like claimService, waiting for the
// claim holding it to be released until ctx ends (false). Releases wake
// it; nothing polls.
func (c *Core) claimServiceWait(ctx context.Context, serviceID int64) bool {
	for {
		c.backups.mu.Lock()
		if !c.backups.running[serviceID] && !c.backups.moving() {
			c.backups.running[serviceID] = true
			c.backups.mu.Unlock()
			return true
		}
		freed := c.backups.freed
		c.backups.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return false
		}
	}
}

// releaseService ends a claim, waking claimServiceWait.
func (c *Core) releaseService(serviceID int64) {
	c.backups.mu.Lock()
	delete(c.backups.running, serviceID)
	close(c.backups.freed)
	c.backups.freed = make(chan struct{})
	c.backups.mu.Unlock()
}

// backupSettings returns the install's settings, or the defaults when
// nothing was configured yet (no password: repoOf creates one). With a
// storage connection, Repository is the repository URL in its bucket
// (backup.S3URL), so every comparison of a backup's repository with the
// current one works as with a URL set by hand.
func (c *Core) backupSettings(ctx context.Context) (store.BackupSetting, error) {
	s, err := c.store.GetBackupSettings(ctx)
	if store.IsNotFound(err) {
		return store.BackupSetting{ID: 1, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6, Hour: 3}, nil
	}
	if err != nil || s.StorageID == nil {
		return s, err
	}
	conn, err := c.storageByID(ctx, *s.StorageID)
	if err != nil {
		return s, err
	}
	s.Repository = backup.S3URL(conn.Endpoint, conn.Bucket, s.StoragePath)
	return s, nil
}

// s3Credentials are the sealed credentials of an S3 repository.
type s3Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region,omitempty"`
}

// repoOf opens the repository settings: credentials and password. A
// missing password is created (and stored) on first use, so backups work
// without any configuration. Only the password is written, and only if
// no settings exist: s may be defaults read before another caller (a
// backup move) stored settings, which must stay; the stored password is
// used.
func (c *Core) repoOf(ctx context.Context, s store.BackupSetting) (backup.Repo, error) {
	if s.Password == nil {
		sealed, err := c.box.Seal([]byte(newPassword()), backupPasswordAAD)
		if err != nil {
			return backup.Repo{}, err
		}
		if err := c.store.CreateBackupPassword(ctx, store.CreateBackupPasswordParams{Password: sealed, UpdatedAt: now()}); err != nil {
			return backup.Repo{}, err
		}
		stored, err := c.store.GetBackupSettings(ctx)
		if err != nil {
			return backup.Repo{}, err
		}
		s.Password = stored.Password
	}
	pw, err := c.box.Open(s.Password, backupPasswordAAD)
	if err != nil {
		return backup.Repo{}, fmt.Errorf("open the backup password: %w", err)
	}
	repo := backup.Repo{URL: s.Repository, Password: string(pw)}
	if s.StorageID != nil {
		conn, err := c.storageByID(ctx, *s.StorageID)
		if err != nil {
			return backup.Repo{}, err
		}
		b, err := c.bucketOf(conn)
		if err != nil {
			return backup.Repo{}, err
		}
		repo.AccessKeyID, repo.SecretAccessKey, repo.Region, repo.PathStyle = b.AccessKeyID, b.SecretAccessKey, b.Region, b.PathStyle
		return repo, nil
	}
	if s.Credentials != nil {
		b, err := c.box.Open(s.Credentials, backupKeysAAD)
		if err != nil {
			return backup.Repo{}, fmt.Errorf("open the backup credentials: %w", err)
		}
		var cr s3Credentials
		if err := json.Unmarshal(b, &cr); err != nil {
			return backup.Repo{}, err
		}
		repo.AccessKeyID, repo.SecretAccessKey, repo.Region = cr.AccessKeyID, cr.SecretAccessKey, cr.Region
	}
	return repo, nil
}

// upsertParams writes s back. With a storage connection the repository
// URL is derived (backupSettings), so it isn't stored.
func upsertParams(s store.BackupSetting) store.UpsertBackupSettingsParams {
	if s.StorageID != nil {
		s.Repository, s.Credentials = "", nil
	}
	return store.UpsertBackupSettingsParams{
		Repository: s.Repository, Credentials: s.Credentials, Password: s.Password,
		KeepDaily: s.KeepDaily, KeepWeekly: s.KeepWeekly, KeepMonthly: s.KeepMonthly, Hour: s.Hour,
		UpdatedAt: s.UpdatedAt, UpdatedBy: s.UpdatedBy, StorageID: s.StorageID, StoragePath: s.StoragePath,
	}
}

// repoLabel is how a backup's repository shows: "local" or the URL.
func repoLabel(repository string) string { return cmp.Or(repository, "local") }

// GetBackupSettings returns the platform's backup settings, to operators
// and to anyone who can read in an org. reveal adds the repository
// password (operators only).
func (c *Core) GetBackupSettings(ctx context.Context, a Actor, reveal bool) (apitypes.BackupSettings, error) {
	if err := a.require(apitypes.ScopeRead, "reading backup settings"); err != nil && !a.Operator {
		return apitypes.BackupSettings{}, err
	}
	if reveal {
		if err := a.requirePlatform(BackupsManage, "revealing the backup password"); err != nil {
			return apitypes.BackupSettings{}, err
		}
		if err := a.requireRecent("revealing the backup password"); err != nil {
			return apitypes.BackupSettings{}, err
		}
	}
	move := c.backupMove() // before the settings: see backupMove
	s, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	out := c.toBackupSettings(s)
	if out.ControlPlane, err = c.controlPlaneBackups(ctx); err != nil {
		return out, err
	}
	if err := c.withBackupStorage(ctx, a, &out, s, move); err != nil {
		return out, err
	}
	if reveal {
		repo, err := c.repoOf(ctx, s)
		if err != nil {
			return out, err
		}
		out.Password = repo.Password
		if s.Password == nil {
			c.audit(ctx, a.platform(), "backup.configure", nil, nil, "backups", "generated the backup repository password")
		}
	}
	return out, nil
}

// localZone names the control plane's time zone, which backup hours are
// in: its name when TZ sets one (Europe/Oslo), else the zone's
// abbreviation (UTC, CET) rather than Go's "Local".
func localZone() string {
	if name := time.Local.String(); name != "Local" {
		return name
	}
	name, _ := time.Now().Zone()
	return name
}

// toBackupSettings is s as the API shows it, without what
// withBackupStorage adds.
func (c *Core) toBackupSettings(s store.BackupSetting) apitypes.BackupSettings {
	out := apitypes.BackupSettings{
		Repository: s.Repository, OffBox: s.Repository != "", HasCredentials: s.Credentials != nil || s.StorageID != nil,
		Repositories: []apitypes.BackupRepository{},
		KeepDaily:    int(s.KeepDaily), KeepWeekly: int(s.KeepWeekly), KeepMonthly: int(s.KeepMonthly),
		Hour: int(s.Hour), TimeZone: localZone(), ResticImage: c.cfg.Backup.ResticImage, UpdatedBy: s.UpdatedBy,
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = &s.UpdatedAt
	}
	return out
}

// backupMove returns a copy of the latest backup move, nil if none. A
// move switches the settings before it is marked finished, so a caller
// that reads it before the settings never shows a finished move with the
// settings from before it.
func (c *Core) backupMove() *apitypes.BackupMove {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	m := c.backups.move
	if m == nil {
		return nil
	}
	cp := *m
	cp.Sources = slices.Clone(m.Sources)
	return &cp
}

// withBackupStorage adds the storage connection backups go to, and for
// operators the repositories holding backups and the latest move
// (backupMove, read before s).
func (c *Core) withBackupStorage(ctx context.Context, a Actor, out *apitypes.BackupSettings, s store.BackupSetting, move *apitypes.BackupMove) error {
	if s.StorageID != nil {
		conn, err := c.storageByID(ctx, *s.StorageID)
		if err != nil {
			return err
		}
		out.Storage, out.Path = conn.Name, s.StoragePath
	}
	if !a.Operator {
		return nil
	}
	repos, err := c.backupRepositories(ctx, s.Repository)
	if err != nil {
		return err
	}
	out.Repositories, out.Move = repos, move
	return nil
}

// ConfigureBackups changes the platform's backup settings (operators). The
// answer carries the repository password when this call generated it:
// without it, an off-box repository can't be read after losing the server.
func (c *Core) ConfigureBackups(ctx context.Context, a Actor, req apitypes.ConfigureBackupRequest) (apitypes.BackupSettings, error) {
	if err := a.requirePlatform(BackupsManage, "configuring backups"); err != nil {
		return apitypes.BackupSettings{}, err
	}
	s, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.BackupSettings{}, err
	}
	var changed []string
	if req.Repository != nil || req.AccessKeyID != nil || req.SecretAccessKey != nil || req.Region != nil {
		return apitypes.BackupSettings{}, invalid("buckets are storage connections now: add one with `dock storage add` (or the Storage page), then move the backups there with `dock backup move --storage <name>`")
	}
	for _, f := range []struct {
		name     string
		v        *int
		dst      *int64
		min, max int
	}{
		{"keep_daily", req.KeepDaily, &s.KeepDaily, 1, 365},
		{"keep_weekly", req.KeepWeekly, &s.KeepWeekly, 0, 520},
		{"keep_monthly", req.KeepMonthly, &s.KeepMonthly, 0, 240},
		{"hour", req.Hour, &s.Hour, 0, 23},
	} {
		if f.v == nil {
			continue
		}
		if *f.v < f.min || *f.v > f.max {
			return apitypes.BackupSettings{}, invalid("%s must be between %d and %d", f.name, f.min, f.max)
		}
		if int64(*f.v) != *f.dst {
			*f.dst = int64(*f.v)
			changed = append(changed, fmt.Sprintf("%s %d", f.name, *f.v))
		}
	}
	generated := s.Password == nil
	var password string
	if generated {
		password = newPassword()
		if s.Password, err = c.box.Seal([]byte(password), backupPasswordAAD); err != nil {
			return apitypes.BackupSettings{}, err
		}
		changed = append(changed, "generated the repository password")
	}
	s.UpdatedAt, s.UpdatedBy = now(), a.Name
	if _, err = c.store.UpsertBackupSettings(ctx, upsertParams(s)); err != nil {
		return apitypes.BackupSettings{}, err
	}
	// Read back resolved: the stored row has no repository URL when a
	// storage connection is set.
	move := c.backupMove()
	if s, err = c.backupSettings(ctx); err != nil {
		return apitypes.BackupSettings{}, err
	}
	summary := "no changes"
	if len(changed) > 0 {
		summary = "set " + strings.Join(changed, ", ")
	}
	c.audit(ctx, a.platform(), "backup.configure", nil, nil, "backups", summary)
	c.kickBackups()
	out := c.toBackupSettings(s)
	out.Password = password
	if out.ControlPlane, err = c.controlPlaneBackups(ctx); err != nil {
		return out, err
	}
	return out, c.withBackupStorage(ctx, a, &out, s, move)
}

// backupKindOf says how a service is backed up: with its template's tool,
// or its volumes file by file. A service without an applied volume has
// nothing to back up.
func (c *Core) backupKindOf(ctx context.Context, ss svcScope) (apitypes.BackupKind, []store.Volume, error) {
	vols, err := c.store.ListVolumes(ctx, ss.svc.ID)
	if err != nil {
		return "", nil, err
	}
	vols = slices.DeleteFunc(vols, func(v store.Volume) bool { return v.Pending })
	if len(vols) == 0 {
		return "", nil, invalid("%s has no volume, so there is nothing to back up", ss)
	}
	if t, ok := templateOf(ss.svc); ok {
		return t.backup, vols, nil
	}
	return apitypes.BackupVolume, vols, nil
}

// ServiceBackups returns a stateful service's backup schedule and runs.
func (c *Core) ServiceBackups(ctx context.Context, a Actor, ref ServiceRef) (apitypes.ServiceBackups, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "listing backups")
	if err != nil {
		return apitypes.ServiceBackups{}, err
	}
	return c.serviceBackups(ctx, ss)
}

func (c *Core) serviceBackups(ctx context.Context, ss svcScope) (apitypes.ServiceBackups, error) {
	kind, _, err := c.backupKindOf(ctx, ss)
	if err != nil {
		return apitypes.ServiceBackups{}, err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return apitypes.ServiceBackups{}, err
	}
	out := apitypes.ServiceBackups{
		Service: ss.svc.Name, Kind: kind, OffBox: set.Repository != "",
		Restorable: kind != apitypes.BackupVolume, Runs: []apitypes.BackupRun{},
	}
	if kind == apitypes.BackupVolume {
		out.Warning = volumeBackupWarning
	}
	if out.Schedule, err = c.scheduleOf(ctx, ss, set); err != nil {
		return out, err
	}
	if out.PITR, err = c.pitrInfo(ctx, ss, set); err != nil {
		return out, err
	}
	rows, err := c.store.ListServiceBackups(ctx, store.ListServiceBackupsParams{ServiceID: ss.svc.ID, Limit: serviceBackupRuns})
	if err != nil {
		return out, err
	}
	names := map[int64]string{}
	if slices.ContainsFunc(rows, func(r store.Backup) bool { return r.RestoredTo != nil }) {
		// Restores create services in the same environment: one query.
		svcs, err := c.store.ListServices(ctx, ss.env.ID)
		if err != nil {
			return out, err
		}
		for _, s := range svcs {
			names[s.ID] = s.Name
		}
	}
	for _, r := range rows {
		run := toBackupRun(r, ss.svc.Name, names)
		if out.Restorable && r.Operation == "backup" && r.Status == string(apitypes.BackupSucceeded) && !r.Pruned && r.SnapshotID != "" {
			run.NotRestorable = notRestorableHere(r.Repository, set.Repository)
		}
		out.Runs = append(out.Runs, run)
	}
	return out, nil
}

// notRestorableHere says why a backup whose snapshot is in repository
// from can't be restored while backups go to current, and what to do; ""
// when it can. Restores open only the current repository. Repositories
// compare by repoLabel, so a local one reads the same however recorded.
func notRestorableHere(from, current string) string {
	if repoLabel(from) == repoLabel(current) {
		return ""
	}
	return fmt.Sprintf("its snapshot is in %s, but Dock restores only from where backups go now, %s; restore it by hand with restic and the repository password (docs/guide/databases.md#restore)",
		repoWords(from), repoWords(current))
}

// repoWords names a repository in a sentence: the local one, or its URL.
func repoWords(repository string) string {
	if repoLabel(repository) == "local" {
		return "the local repository on the service's server"
	}
	return repository
}

// BackupLog returns what a backup or restore run of a service printed
// (apitypes.BackupLog). Like a one-off command's output it can hold what
// a database printed, so it takes the scope running one does: deploy.
// A run that succeeded and kept no log says so: a restore that works
// prints nothing (pg_restore is silent), which is not a job that never ran.
func (c *Core) BackupLog(ctx context.Context, a Actor, ref ServiceRef, id int64) (apitypes.BackupLog, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "reading a backup's log")
	if err != nil {
		return apitypes.BackupLog{}, err
	}
	row, err := c.store.GetBackup(ctx, id)
	if store.IsNotFound(err) || (err == nil && row.ServiceID != ss.svc.ID) {
		return apitypes.BackupLog{}, notFound("%s has no backup or restore %d; `dock backup list` shows its runs", ss, id)
	}
	if err != nil {
		return apitypes.BackupLog{}, err
	}
	out, err := c.store.GetBackupLog(ctx, store.GetBackupLogParams{BackupID: id, ServiceID: ss.svc.ID})
	if err != nil && !store.IsNotFound(err) {
		return apitypes.BackupLog{}, err
	}
	if out == "" && row.Status == string(apitypes.BackupSucceeded) {
		out = fmt.Sprintf("The %s succeeded and kept no log: its job printed nothing, or it is older than the service's newest %d runs.\n", row.Operation, backupLogRuns)
	}
	return apitypes.BackupLog{ID: id, Output: out}, nil
}

// jobLog is what a backup or restore job's output keeps for View log: the
// repository's and database's secrets from its environment masked, then
// its last apitypes.MaxRunOutputBytes.
func jobLog(env map[string]string, out string) string {
	if out == "" {
		return ""
	}
	secrets := map[string]string{}
	for _, k := range append([]string{"RESTIC_PASSWORD", "AWS_SECRET_ACCESS_KEY", "PGPASSWORD", "REDISCLI_AUTH", "MYSQL_PWD"}, walarchive.Secrets...) {
		if v := env[k]; v != "" {
			secrets[k] = v
		}
	}
	if r := newRedactor(secrets); r != nil {
		b, _ := r.appendMasked(nil, []byte(out), true)
		out = string(b)
	}
	return runTail(out)
}

// keepBackupLog keeps a finished run's log (jobLog) and drops the
// service's logs older than its newest backupLogRuns. A failure is
// logged: a lost log never fails the run.
func (c *Core) keepBackupLog(ctx context.Context, serviceID, backupID int64, log string) {
	if log == "" {
		return
	}
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.SaveBackupLog(ctx, store.SaveBackupLogParams{BackupID: backupID, ServiceID: serviceID, Output: log}); err != nil {
			return err
		}
		return q.TrimBackupLogs(ctx, store.TrimBackupLogsParams{ServiceID: serviceID, Keep: backupLogRuns})
	})
	if err != nil {
		c.log.Warn("keep backup log", "backup", backupID, "err", err)
	}
}

// toBackupRun converts a backups row; names resolves restored_to.
func toBackupRun(r store.Backup, service string, names map[int64]string) apitypes.BackupRun {
	out := apitypes.BackupRun{
		ID: r.ID, Service: service, Operation: r.Operation, Kind: apitypes.BackupKind(r.Kind), Trigger: r.Trigger,
		Status: apitypes.BackupStatus(r.Status), Snapshot: r.SnapshotID, SizeBytes: r.SizeBytes,
		Repository: repoLabel(r.Repository), OffBox: r.Repository != "", Pruned: r.Pruned, Error: r.Error,
		CreatedBy: r.CreatedBy, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		ConsistentAt: r.ConsistentAt, ToTime: r.RestoreToTime,
	}
	if r.RestoredTo != nil {
		out.RestoredTo = names[*r.RestoredTo]
	}
	return out
}

// RunBackup starts a backup of a stateful service now. It runs in the
// background; the answer is the running backup.
func (c *Core) RunBackup(ctx context.Context, a Actor, ref ServiceRef) (apitypes.BackupRun, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "running a backup")
	if err != nil {
		return apitypes.BackupRun{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.BackupRun{}, invalid("service %s is archived", ss)
	}
	if _, err := c.runtimeFor(ss.env.ServerID); err != nil {
		return apitypes.BackupRun{}, err
	}
	row, err := c.startBackup(ctx, a, ss, "manual")
	if err != nil {
		return apitypes.BackupRun{}, err
	}
	c.audit(ctx, a, "backup.run", &ss.project, &ss.env, ss.String(), fmt.Sprintf("started backup %d", row.ID))
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runBackup(c.ctx, ss, row)
	}()
	return toBackupRun(row, ss.svc.Name, nil), nil
}

// startBackup claims the service and records a running backup. The caller
// must call runBackup with it, which releases the claim.
func (c *Core) startBackup(ctx context.Context, a Actor, ss svcScope, trigger string) (store.Backup, error) {
	kind, _, err := c.backupKindOf(ctx, ss)
	if err != nil {
		return store.Backup{}, err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return store.Backup{}, err
	}
	if c.backupsMoving() {
		return store.Backup{}, conflict("the backups are moving to other storage; back up %s once the move finishes", ss)
	}
	if err := c.notMoving(ctx, ss.envScope, "back it up"); err != nil {
		return store.Backup{}, err
	}
	if !c.claimService(ss.svc.ID) {
		return store.Backup{}, conflict("a backup or restore of %s is already running; wait for it to finish", ss)
	}
	row, err := c.store.CreateBackup(ctx, store.CreateBackupParams{
		ServiceID: ss.svc.ID, Operation: "backup", Kind: string(kind), Trigger: trigger,
		Repository: set.Repository, CreatedBy: a.Name, StartedAt: now(),
	})
	if err != nil {
		c.releaseService(ss.svc.ID)
		return store.Backup{}, err
	}
	return row, nil
}

// runBackup runs a recorded backup to the end and records the outcome:
// the job, then retention's result (snapshots it removed are marked
// pruned). Failures are audited, raise an alert and show on the overview. The claim on
// the service ends with the job, before the row says so, so whoever sees
// it finished can start the next run.
func (c *Core) runBackup(ctx context.Context, ss svcScope, row store.Backup) {
	defer c.kickBackups() // a due backup may have waited for this one
	// A database whose point-in-time restore is off lets its base
	// backups age out with its dumps (ADR 0093).
	var forget []store.Backup
	if f, pitr := c.baseForget(ctx, ss); !pitr.Enabled && row.Kind == string(apitypes.BackupPostgres) {
		forget = f
	}
	res, log, err := c.backupJob(ctx, ss, row, snapshotIDs(forget))
	c.releaseService(ss.svc.ID)
	if ctx.Err() != nil {
		return // shutting down: the next start marks it failed
	}
	c.keepBackupLog(ctx, ss.svc.ID, row.ID, log)
	// The row is written last, so whoever sees the run finished also sees
	// its audit event and the snapshots retention removed.
	fin := store.FinishBackupParams{Status: string(apitypes.BackupSucceeded), FinishedAt: ptr(now()), ID: row.ID}
	if err != nil {
		fin.Status, fin.Error = string(apitypes.BackupFailed), err.Error()
		c.audit(ctx, System, "backup.failed", &ss.project, &ss.env, ss.String(), fmt.Sprintf("backup %d failed: %v", row.ID, err))
		s := backupFailed(ss.project.Name, ss.env.Name, ss.svc.Name, row.ID, err.Error(), ss.svc.ID)
		s.projectID = ss.project.ID
		c.signalAlert(ctx, s)
	} else {
		c.signalAlert(ctx, alertSignal{Key: "backup:" + alertServiceKey(ss.svc.ID, ss.project.Name, ss.env.Name, ss.svc.Name), Resolve: true})
		fin.SnapshotID, fin.SizeBytes = res.SnapshotID, res.Bytes
		if res.Kept != nil {
			c.markPruned(ctx, ss.svc.ID, row.Kind, row.Repository, res.Kept)
		}
		if len(forget) > 0 {
			c.afterForget(ctx, ss, row.Repository, log, forget, nil)
			defer c.pruneWAL(ctx, ss)
		}
		short := res.SnapshotID[:min(8, len(res.SnapshotID))]
		c.audit(ctx, System, "backup.succeeded", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("backup %d: snapshot %s, %s, to %s", row.ID, short, humanBytes(res.Bytes), repoLabel(row.Repository)))
	}
	if err := c.store.FinishBackup(ctx, fin); err != nil {
		c.log.Error("record backup", "backup", row.ID, "err", err)
	}
}

// markPruned marks the service's snapshots of a kind in repository that
// retention removed: every one not in kept. Dumps and base backups have
// their own tags (backup.Tag, BaseTag), so their own retention.
func (c *Core) markPruned(ctx context.Context, serviceID int64, kind, repository string, kept []string) {
	rows, err := c.store.ListUnprunedBackups(ctx, store.ListUnprunedBackupsParams{ServiceID: serviceID, Repository: repository})
	if err != nil {
		c.log.Warn("list backups for retention", "service", serviceID, "err", err)
		return
	}
	for _, r := range rows {
		if (r.Kind == string(apitypes.BackupPITR)) == (kind == string(apitypes.BackupPITR)) && !slices.Contains(kept, r.SnapshotID) {
			if err := c.store.PruneBackup(ctx, r.ID); err != nil {
				c.log.Warn("mark backup pruned", "backup", r.ID, "err", err)
			}
		}
	}
}

// humanBytes renders a size for audit summaries.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// backupJob runs the backup job of row on the service's server and parses
// its output; log is what View log keeps of it (jobLog), "" when the job
// never ran. forget are base backup snapshots its retention removes too.
func (c *Core) backupJob(ctx context.Context, ss svcScope, row store.Backup, forget []string) (res backup.Result, log string, err error) {
	rt, err := c.runtimeFor(ss.env.ServerID)
	if err != nil {
		return backup.Result{}, "", err
	}
	set, err := c.backupSettings(ctx)
	if err != nil {
		return backup.Result{}, "", err
	}
	if set.Repository != row.Repository {
		return backup.Result{}, "", errors.New("the backup repository changed after this backup started; run it again")
	}
	repo, err := c.repoOf(ctx, set)
	if err != nil {
		return backup.Result{}, "", err
	}
	kind, vols, err := c.backupKindOf(ctx, ss)
	if err != nil {
		return backup.Result{}, "", err
	}
	src := backup.Source{Kind: kind, ServiceID: ss.svc.ID, Host: ss.svc.Name}
	image := c.cfg.Backup.ResticImage
	var mounts []node.Mount
	switch kind {
	case apitypes.BackupPostgres:
		conn, err := c.dbConnection(ctx, ss)
		if err != nil {
			return backup.Result{}, "", notRunningErr(ss, err)
		}
		src.Port, src.User, src.Password, src.Database = conn.port, conn.owner.User, conn.owner.Password, conn.database
		image = jobImage(ss)
	case apitypes.BackupRedis:
		env, err := c.activeEnv(ctx, ss)
		if err != nil {
			return backup.Result{}, "", err
		}
		t, _ := templateOf(ss.svc)
		src.Port, src.Password, image = t.port, env["REDIS_PASSWORD"], jobImage(ss)
	case apitypes.BackupMySQL:
		env, err := c.activeEnv(ctx, ss)
		if err != nil {
			return backup.Result{}, "", err
		}
		t, _ := templateOf(ss.svc)
		src.Port, src.Password, image = t.port, mysqlRootPassword(env), jobImage(ss)
	case apitypes.BackupVolume:
		for _, v := range vols {
			mounts = append(mounts, node.Mount{Volume: v.DockerName, Path: backup.VolumesPath + "/" + v.Name, ReadOnly: true})
		}
	}
	script, err := backup.BackupScript(kind)
	if err != nil {
		return backup.Result{}, "", err
	}
	spec := node.JobSpec{
		Name:   fmt.Sprintf("dock_p%d_e%d_s%d_backup%d", ss.project.ID, ss.env.ID, ss.svc.ID, row.ID),
		Image:  image,
		Labels: c.labels(ss, 0),
		Env:    backup.BackupEnv(src, repo, backup.Retention{Daily: int(set.KeepDaily), Weekly: int(set.KeepWeekly), Monthly: int(set.KeepMonthly)}),
		Script: script, Network: networkName(ss.envScope), Mounts: mounts,
	}
	spec.Env["DOCK_FORGET"] = strings.Join(forget, " ")
	out, err := c.runResticJob(ctx, ss.env.ServerID, rt, repo, spec)
	log = jobLog(spec.Env, out)
	if err != nil {
		return backup.Result{}, log, err
	}
	res, err = backup.ParseBackup(out)
	if err != nil {
		return res, log, fmt.Errorf("%w%s", err, withOutput(out))
	}
	return res, log, nil
}

// jobOutput is what a job's error shows of its output (backup.ErrorText)
// without control characters: it reaches the audit log, alert channels
// and terminals, and a database's error messages are its owner's text.
func jobOutput(out string) string { return cleanLine(backup.ErrorText(out), 2048) }

// withOutput is ": <jobOutput>" to end an error with, or "" when the job
// printed nothing worth showing.
func withOutput(out string) string {
	if text := jobOutput(out); text != "" {
		return ": " + text
	}
	return ""
}

// notRunningErr says a database must be running to be backed up or
// restored into.
func notRunningErr(ss svcScope, err error) error {
	if errors.Is(err, errNotRunning) {
		return fmt.Errorf("%s isn't running; start it (deploy or restart) and run the backup again", ss.svc.Name)
	}
	return err
}

// jobImage is the image a template database's backup and restore jobs
// run in: the template's pinned image (its pg_dump or redis-cli), never
// the service's active image. Jobs carry the install's repository
// password and S3 credentials, and anyone with deploy on the environment
// can deploy any image to the service, so that image must not run with
// them (ADR 0019, amendment).
func jobImage(ss svcScope) string {
	t, _ := templateOf(ss.svc)
	return t.image
}

// activeEnv returns the resolved variables of a service's active release.
func (c *Core) activeEnv(ctx context.Context, ss svcScope) (map[string]string, error) {
	rel, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return nil, notRunningErr(ss, errNotRunning)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := c.openConfig(rel)
	if err != nil {
		return nil, err
	}
	return cfg.Env, nil
}

// runResticJob runs spec on a server with restic and the repository
// mounted (filling the server's restic volume first if needed). A job
// that exits non-zero is an error with the end of its output; one that
// couldn't reach a repository (backup.UnreachableExit) is just the job's
// own message saying which and what to do.
func (c *Core) runResticJob(ctx context.Context, serverID int64, rt node.Runtime, repo backup.Repo, spec node.JobSpec) (string, error) {
	bin, err := c.ensureRestic(ctx, serverID, rt)
	if err != nil {
		return "", err
	}
	spec.Mounts = append(spec.Mounts, node.Mount{Volume: bin, Path: backup.BinDir, ReadOnly: true})
	if repo.Local() {
		vol := c.localRepoVolume()
		if err := rt.EnsureVolume(ctx, vol, node.Labels{ControlPlane: c.install}); err != nil {
			return "", fmt.Errorf("create the local backup volume: %w", c.nodeErr(serverID, err))
		}
		spec.Mounts = append(spec.Mounts, node.Mount{Volume: vol, Path: backup.LocalPath})
	}
	// restic reaches the storage from a managed network, which the app
	// firewall covers: the service's environment's, or the install's own
	// for a job of no environment (a job without a network has none).
	netLabels := node.Labels{ControlPlane: c.install, Project: spec.Labels.Project, Environment: spec.Labels.Environment}
	if spec.Network == "" {
		spec.Network, netLabels = c.jobsNetwork(), node.Labels{ControlPlane: c.install}
	}
	if err := rt.EnsureNetwork(ctx, spec.Network, netLabels); err != nil {
		return "", c.nodeErr(serverID, err)
	}
	if err := c.ensureImage(ctx, serverID, rt, spec.Image); err != nil {
		return "", err
	}
	spec.Resources = node.Resources{MemoryBytes: jobMemoryMB << 20, NanoCPUs: 1e9}
	res, err := rt.RunJob(ctx, spec)
	if err != nil {
		return res.Output, c.nodeErr(serverID, err)
	}
	storage := "storage:" + repo.URL
	if res.ExitCode == backup.UnreachableExit {
		if text := jobOutput(res.Output); text != "" {
			err := errors.New(strings.TrimPrefix(text, "dock: "))
			if !repo.Local() {
				where := "a server"
				if srv, gerr := c.store.GetServer(ctx, serverID); gerr == nil {
					where = "server " + srv.Name
				}
				c.connectionFailed(ctx, storage, storage, "Backup storage "+repo.URL+" can't be reached",
					fmt.Sprintf("A backup job on %s got no answer from it: %v. Backups and restores there fail until it answers; check the bucket and its keys with `dock storage`.", where, err), "dock storage")
			}
			return res.Output, err
		}
	}
	if res.ExitCode != 0 {
		return res.Output, fmt.Errorf("exit code %d%s", res.ExitCode, withOutput(res.Output))
	}
	if !repo.Local() {
		c.connectionWorks(ctx, storage)
	}
	return res.Output, nil
}

// jobsNetwork is the network of the install's storage jobs that belong to
// no environment (the control plane's backup, moving backups to other
// storage): a managed network like an environment's, so the app firewall
// covers it. It is named after the install, so control planes sharing a
// Docker host keep their own.
func (c *Core) jobsNetwork() string {
	sum := sha256.Sum256([]byte(c.install))
	return "dock_jobs_" + hex.EncodeToString(sum[:4])
}

// localRepoVolume is the volume of a server's local repository. It is
// named after the install, so control planes sharing a Docker host keep
// their own.
func (c *Core) localRepoVolume() string {
	sum := sha256.Sum256([]byte(c.install))
	return backup.LocalVolumePrefix + hex.EncodeToString(sum[:4])
}

// ensureRestic returns the server's restic volume, copying the binary
// out of the restic image into it once per daemon start. It is named
// after the install and the image, so an upgrade gets a fresh one and
// control planes sharing a Docker host keep their own.
func (c *Core) ensureRestic(ctx context.Context, serverID int64, rt node.Runtime) (string, error) {
	sum := sha256.Sum256([]byte(c.install + "\x00" + c.cfg.Backup.ResticImage))
	vol := "dock_restic_" + hex.EncodeToString(sum[:6])
	c.backups.mu.Lock()
	ready := c.backups.restic[serverID]
	c.backups.mu.Unlock()
	if ready {
		return vol, nil
	}
	labels := node.Labels{ControlPlane: c.install}
	if err := rt.EnsureVolume(ctx, vol, labels); err != nil {
		return "", fmt.Errorf("create the restic volume: %w", c.nodeErr(serverID, err))
	}
	if err := c.ensureImage(ctx, serverID, rt, c.cfg.Backup.ResticImage); err != nil {
		return "", err
	}
	res, err := rt.RunJob(ctx, node.JobSpec{
		Name: fmt.Sprintf("dock_restic_install_%d", now().UnixNano()), Image: c.cfg.Backup.ResticImage, Labels: labels,
		Script: backup.InstallScript, Mounts: []node.Mount{{Volume: vol, Path: backup.BinDir}},
	})
	if err != nil {
		return "", fmt.Errorf("install restic: %w", c.nodeErr(serverID, err))
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("install restic: exit code %d%s", res.ExitCode, withOutput(res.Output))
	}
	c.backups.mu.Lock()
	c.backups.restic[serverID] = true
	c.backups.mu.Unlock()
	return vol, nil
}

// ensureImage pulls a job's image unless the server has it: an active
// release's image (by ID) always is, a pinned one only after its first
// pull.
func (c *Core) ensureImage(ctx context.Context, serverID int64, rt node.Runtime, image string) error {
	ok, err := rt.HasImage(ctx, image)
	if err != nil || ok {
		return c.nodeErr(serverID, err)
	}
	if _, err := rt.Pull(ctx, image, io.Discard); err != nil {
		return fmt.Errorf("pull %s: %w", image, c.nodeErr(serverID, err))
	}
	return nil
}

// failInterruptedBackups marks runs a restart cut short as failed, the
// control plane's own backup included.
func (c *Core) failInterruptedBackups(ctx context.Context) error {
	const msg = "the daemon restarted while this was running; run it again"
	n, err := c.store.FailRunningBackups(ctx, store.FailRunningBackupsParams{Error: msg, FinishedAt: ptr(now())})
	if err != nil {
		return err
	}
	cp, err := c.store.FailRunningControlPlaneBackup(ctx, store.FailRunningControlPlaneBackupParams{Error: msg, FinishedAt: ptr(now())})
	n += cp
	if n > 0 {
		c.log.Warn("backups interrupted by a restart marked failed", "count", n)
	}
	return err
}

// latestBackups returns each service's latest backup in a project, by
// service ID, for the overview.
func (c *Core) latestBackups(ctx context.Context, projectID int64) (map[int64]store.Backup, error) {
	rows, err := c.store.ListProjectLatestBackups(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]store.Backup, len(rows))
	for _, r := range rows {
		out[r.ServiceID] = r
	}
	return out, nil
}
