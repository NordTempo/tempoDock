package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/s3"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Registry is the platform registry as core uses it (ADR 0014 section 4
// and its amendment): CNCF Distribution on the control plane, which every
// server reaches as Host() (127.0.0.1:5000, through the node link on
// remote servers). Core mints a token per pull and push; no server holds
// registry credentials. registry.Platform implements it; nil in
// Options.Registry means there is no registry, and single-server installs
// build and run images locally as before.
type Registry interface {
	// Host is the registry's host:port in image references, the same on
	// every server.
	Host() string
	// Token mints a short-lived token for one repository, for subject
	// (e.g. "server homelab"): push and pull access for a build's push
	// (valid for registry.PushTokenTTL), else pull only.
	Token(subject, repo string, push bool) string
	// Digest returns the manifest digest (sha256:...) repo:tag points at.
	Digest(ctx context.Context, repo, tag string) (string, error)
	// Prune deletes the manifests whose digest isn't in keep and returns
	// how many it deleted, and the tags or manifests it skipped because
	// it couldn't read or delete them (each error names one); err only
	// when it couldn't prune at all.
	Prune(ctx context.Context, keep []string) (deleted int, skipped []error, err error)
	// GarbageCollect removes blobs no manifest references any more. The
	// registry is stopped while it runs, so core calls it only when no
	// build or pull uses the registry, and ends ctx when one waits: it
	// stops early (what it left goes next time) and returns once the
	// registry answers again.
	GarbageCollect(ctx context.Context) error
	// Stored returns the size of the registry's blobs, on disk or in its
	// bucket.
	Stored(ctx context.Context) (int64, error)
	// UseStorage points the registry at bucket (nil: the control plane's
	// disk), rewriting its config and restarting it if that changed.
	UseStorage(ctx context.Context, bucket *s3.Bucket) error
	// Migrate copies the registry's images to to (nil: the disk) while it
	// serves them read-only, calls commit once the copy is checked, and
	// then switches the registry over. Before commit, a failure leaves
	// the registry as it was. The source is left for Drop. owner (the
	// install ID) marks the copy in a bucket, so a later migration can
	// tell its own leftovers from another Dock's registry.
	Migrate(ctx context.Context, to *s3.Bucket, owner string, commit func() error) (objects int, bytes int64, err error)
	// Drop deletes the registry's data from storage it no longer uses.
	Drop(ctx context.Context, storage *s3.Bucket) error
	// PushStarted marks a build's push to the registry as running until
	// done is called: meanwhile the storage gateway reports blobs stuck in
	// the bucket missing, so the push uploads them again (ADR 0084).
	PushStarted() (done func())
	// Bucket is the bucket's health and the spool pushes land in, nil
	// when the images are on the control plane's disk (ADR 0084).
	Bucket() *apitypes.RegistryBucket
}

// GetRegistry describes the platform registry for the console: its size,
// how pruning went since the daemon started, and the services keeping
// images in it, in one query. Operators only.
func (c *Core) GetRegistry(ctx context.Context, a Actor) (apitypes.Registry, error) {
	if err := a.requirePlatform(PlatformView, "reading the registry"); err != nil {
		return apitypes.Registry{}, err
	}
	return c.registryInfo(ctx)
}

func (c *Core) registryInfo(ctx context.Context) (apitypes.Registry, error) {
	if c.reg == nil {
		return apitypes.Registry{Services: []apitypes.RegistryService{}}, nil
	}
	rows, err := c.store.ListRegistryServices(ctx, int64(c.keepReleases()))
	if err != nil {
		return apitypes.Registry{}, err
	}
	// The size lists a bucket, which mustn't hold the page when the bucket
	// is slow: it gets storedTimeout, and isn't asked while failing (0
	// shows as unknown).
	bucket := c.reg.Bucket()
	var stored int64
	if bucket == nil || bucket.State != "failing" {
		sctx, cancel := context.WithTimeout(ctx, storedTimeout)
		stored, err = c.reg.Stored(sctx)
		cancel()
		if err != nil {
			c.log.Debug("registry size", "err", err)
		}
	}
	out := apitypes.Registry{
		Enabled:      true,
		Host:         c.reg.Host(),
		StoredBytes:  stored,
		Bucket:       bucket,
		KeepReleases: c.keepReleases(),
		Services:     make([]apitypes.RegistryService, len(rows)),
	}
	for i, r := range rows {
		out.Services[i] = apitypes.RegistryService{
			Project: r.Project, Environment: r.Environment, Service: r.Service,
			Repository: fmt.Sprintf("%s%d_e%d_s%d", buildTagPrefix, r.ProjectID, r.EnvironmentID, r.ServiceID),
			Archived:   r.ArchivedAt != nil,
			Kept:       int(r.Kept),
			Newest:     r.Newest,
			Active:     r.ActiveNumber,
		}
	}
	u := &c.images
	u.mu.Lock()
	out.Pruning = u.pruning
	out.LastPruned = u.lastPruned
	out.LastPruneError = u.lastErr
	out.LastPruneSkipped = slices.Clone(u.lastSkipped)
	out.CollectPending = u.collect
	if m := u.migration; m != nil {
		cp := *m
		out.Migration = &cp
	}
	if !u.lastPrune.IsZero() {
		at := u.lastPrune.UTC().Truncate(time.Second)
		out.LastPruneAt = &at
	}
	u.mu.Unlock()
	// The setting after the migration state: a migration commits before
	// it reads as finished, so a finished one shows with its storage.
	if out.Storage, err = c.registryStorageName(ctx); err != nil {
		return apitypes.Registry{}, err
	}
	return out, nil
}

// PruneRegistry starts a registry prune now, whenever the last one ran:
// the images no release keeps are deleted and their layers collected
// (the registry stops for those seconds). It answers with the registry,
// pruning; registry.pruned or registry.prune_failed is audited when it
// ends. Refused while builds or pulls use the registry. Operators only.
func (c *Core) PruneRegistry(ctx context.Context, a Actor) (apitypes.Registry, error) {
	if err := a.requirePlatform(RegistryManage, "pruning the registry"); err != nil {
		return apitypes.Registry{}, err
	}
	if c.reg == nil {
		return apitypes.Registry{}, invalid("this Dock has no registry; images stay on the server that built them")
	}
	u := &c.images
	pctx, yield := context.WithCancel(c.ctx)
	u.mu.Lock()
	switch {
	case u.pruning:
		u.mu.Unlock()
		yield()
		return apitypes.Registry{}, conflict("the registry is being pruned already; wait for it to finish")
	case u.migrating():
		u.mu.Unlock()
		yield()
		return apitypes.Registry{}, conflict("the registry's images are moving to other storage; prune once that finishes")
	case !u.gate.tryHold(yield):
		u.mu.Unlock()
		yield()
		return apitypes.Registry{}, conflict("builds or pulls are using the registry; prune once they finish")
	}
	u.pruning = true
	u.mu.Unlock()
	c.audit(ctx, a.platform(), "registry.prune", nil, nil, "registry", "started a registry prune")
	// The answer is read before the prune starts, so it says pruning
	// however fast the prune is.
	info, err := c.registryInfo(ctx)
	c.wg.Add(1)
	c.goCaught("pruning the registry", func() {
		defer c.wg.Done()
		defer yield()
		n, err := c.pruneRegistry(pctx)
		if errors.Is(err, errPruneYielded) {
			c.audit(c.ctx, System, "registry.pruned", nil, nil, "registry", fmt.Sprintf("pruned %d images no release keeps; a build needed the registry before its garbage collection ended, so the next prune collects", n))
			return
		}
		if err != nil {
			c.audit(c.ctx, System, "registry.prune_failed", nil, nil, "registry", fmt.Sprintf("registry prune failed: %v", err))
			return
		}
		c.audit(c.ctx, System, "registry.pruned", nil, nil, "registry", fmt.Sprintf("pruned %d images no release keeps", n))
	})
	return info, err
}

// MigrateRegistry moves the registry's images to other storage (ADR
// 0034): to "local" is the control plane's disk, to "s3" a storage
// connection (req.Storage, or the only one). It answers with the
// registry, migrating, and copies in the background: the registry serves
// the images read-only meanwhile, and builds and pulls wait for it, like
// they do for a prune. registry.migrated or registry.migrate_failed is
// audited when it ends; the old copy is deleted once the registry runs
// on the new one. Refused while builds or pulls use the registry.
// Operators only.
func (c *Core) MigrateRegistry(ctx context.Context, a Actor, req apitypes.MigrateRegistryRequest) (apitypes.Registry, error) {
	if err := a.requirePlatform(RegistryManage, "moving the registry's images"); err != nil {
		return apitypes.Registry{}, err
	}
	if c.reg == nil {
		return apitypes.Registry{}, invalid("this Dock has no registry; images stay on the server that built them")
	}
	var to registryEnd
	switch req.To {
	case "local":
		to = registryEnd{name: "local"}
	case "s3":
		row, err := c.pickStorage(ctx, req.Storage)
		if err != nil {
			return apitypes.Registry{}, err
		}
		b, err := c.bucketOf(row)
		if err != nil {
			return apitypes.Registry{}, err
		}
		to = registryEnd{name: row.Name, id: &row.ID, bucket: &b}
	default:
		return apitypes.Registry{}, invalid("to must be local or s3")
	}

	u := &c.images
	u.mu.Lock()
	switch {
	case u.migrating():
		u.mu.Unlock()
		return apitypes.Registry{}, conflict("the registry's images are moving already; wait for that to finish")
	case u.pruning:
		u.mu.Unlock()
		return apitypes.Registry{}, conflict("the registry is being pruned; migrate once that finishes")
	case !u.gate.tryHold(nil):
		u.mu.Unlock()
		return apitypes.Registry{}, conflict("builds or pulls are using the registry; migrate once they finish")
	}
	// Read where the images are only now: a migration commits while it
	// holds the gate, so the setting can't change under this one.
	fromID, err := c.registryStorageID(ctx)
	var from registryEnd
	if err == nil {
		from, err = c.registryEnd(ctx, fromID)
	}
	if err == nil && to.name == from.name {
		err = invalid("the registry keeps its images in %s already", from.name)
	}
	if err != nil {
		u.gate.release()
		u.mu.Unlock()
		return apitypes.Registry{}, err
	}
	u.migration = &apitypes.RegistryMigration{From: from.name, To: to.name, Running: true, StartedAt: now().UTC().Truncate(time.Second)}
	u.migrateIDs = [2]*int64{fromID, to.id}
	u.mu.Unlock()
	c.audit(ctx, a.platform(), "registry.migrate", nil, nil, "registry", fmt.Sprintf("started moving the registry's images from %s to %s", from.name, to.name))
	info, err := c.registryInfo(ctx)
	c.wg.Add(1)
	c.goCaught("moving the registry's images", func() {
		defer c.wg.Done()
		c.migrateRegistry(c.ctx, a, from, to)
	})
	return info, err
}

// registryBucketRoot is where the registry keeps its images in a bucket
// (registry.BucketRoot; core doesn't import the registry package).
const registryBucketRoot = "dock-registry"

// registryEnd is one side of a migration: "local" or a connection.
type registryEnd struct {
	name   string
	id     *int64
	bucket *s3.Bucket
}

// registryEnd loads the storage a connection ID names (nil: local).
func (c *Core) registryEnd(ctx context.Context, id *int64) (registryEnd, error) {
	if id == nil {
		return registryEnd{name: "local"}, nil
	}
	rows, err := c.store.ListStorageConnections(ctx)
	if err != nil {
		return registryEnd{}, err
	}
	for _, r := range rows {
		if r.ID == *id {
			b, err := c.bucketOf(r)
			return registryEnd{name: r.Name, id: id, bucket: &b}, err
		}
	}
	return registryEnd{}, fmt.Errorf("the registry's storage connection %d is gone", *id)
}

// pickStorage is the connection named name, or the only one if name is
// empty.
func (c *Core) pickStorage(ctx context.Context, name string) (store.StorageConnection, error) {
	if name != "" {
		row, err := c.store.GetStorageConnection(ctx, name)
		if store.IsNotFound(err) {
			return row, notFound("no storage connection named %s; `dock storage` lists them", name)
		}
		return row, err
	}
	rows, err := c.store.ListStorageConnections(ctx)
	if err != nil {
		return store.StorageConnection{}, err
	}
	switch len(rows) {
	case 0:
		return store.StorageConnection{}, invalid("there is no storage connection yet; add the bucket with `dock storage add` first")
	case 1:
		return rows[0], nil
	}
	return store.StorageConnection{}, invalid("there are %d storage connections; name one with --storage", len(rows))
}

// migrateRegistry runs a migration MigrateRegistry started, which holds
// the gate, and records how it went.
func (c *Core) migrateRegistry(ctx context.Context, a Actor, from, to registryEnd) {
	u := &c.images
	committed := false
	objects, size, err := c.reg.Migrate(ctx, to.bucket, c.install, func() error {
		err := c.store.SetRegistryStorage(ctx, store.SetRegistryStorageParams{RegistryStorageID: to.id, UpdatedBy: a.Name, UpdatedAt: now()})
		committed = err == nil
		return err
	})
	summary := fmt.Sprintf("moved the registry's images from %s to %s: %d objects, %s", from.name, to.name, objects, humanBytes(size))
	if err == nil {
		if derr := c.reg.Drop(ctx, from.bucket); derr != nil {
			c.log.Warn("registry: remove the old copy", "from", from.name, "err", derr)
			summary += fmt.Sprintf("; the old copy in %s couldn't be removed (%v), delete it by hand", from.name, derr)
		}
	}
	u.gate.release()
	u.mu.Lock()
	m := u.migration
	m.Running, m.Objects, m.Bytes = false, objects, size
	t := now().UTC().Truncate(time.Second)
	m.FinishedAt = &t
	if err != nil {
		m.Error = err.Error()
	}
	u.migrateIDs = [2]*int64{}
	u.mu.Unlock()
	switch {
	case err == nil:
		c.log.Info("registry: images moved", "from", from.name, "to", to.name, "objects", objects, "bytes", size)
		c.audit(ctx, System, "registry.migrated", nil, nil, "registry", summary)
	case committed:
		c.log.Error("registry: images moved, the registry didn't switch", "to", to.name, "err", err)
		c.audit(ctx, System, "registry.migrate_failed", nil, nil, "registry", fmt.Sprintf("moved the images to %s, but %v; the copy in %s is kept", to.name, err, from.name))
	default:
		c.log.Warn("registry: migration failed", "to", to.name, "err", err)
		c.audit(ctx, System, "registry.migrate_failed", nil, nil, "registry", fmt.Sprintf("moving the registry's images to %s failed, they stay in %s: %v", to.name, from.name, err))
	}
}

// holdRegistry takes the registry for a change that restarts it, the way
// a prune does: refused while a build, pull, prune or migration uses it.
func (c *Core) holdRegistry() (release func(), err error) {
	u := &c.images
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.pruning || u.migrating() || !u.gate.tryHold(nil) {
		return nil, conflict("the registry is in use (builds, pulls, a prune or a migration); try again once they finish")
	}
	return u.gate.release, nil
}

// registryMigrating reports whether a running migration moves images to
// or from storage connection id.
func (c *Core) registryMigrating(id int64) bool {
	u := &c.images
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.migrating() {
		return false
	}
	for _, m := range u.migrateIDs {
		if m != nil && *m == id {
			return true
		}
	}
	return false
}

// registryStorageName is "local" or the name of the registry's storage
// connection.
func (c *Core) registryStorageName(ctx context.Context) (string, error) {
	id, err := c.registryStorageID(ctx)
	if err != nil || id == nil {
		return "local", err
	}
	end, err := c.registryEnd(ctx, id)
	return end.name, err
}

// RegistryBucket returns the bucket the registry keeps its images in, nil
// while they are on the control plane's disk. `dock admin
// registry-config` renders the registry's config with it.
func (c *Core) RegistryBucket(ctx context.Context) (*s3.Bucket, error) {
	id, err := c.registryStorageID(ctx)
	if err != nil {
		return nil, err
	}
	end, err := c.registryEnd(ctx, id)
	return end.bucket, err
}

// applyRegistryStorage points the registry at the storage the settings
// name, on start: the config follows the setting even if the daemon
// stopped in the middle of a migration.
func (c *Core) applyRegistryStorage(ctx context.Context) {
	if c.reg == nil {
		return
	}
	b, err := c.RegistryBucket(ctx)
	if err == nil {
		err = c.reg.UseStorage(ctx, b)
	}
	if err != nil {
		c.log.Error("registry storage", "err", err)
	}
}

// InstallRegistryStorage is install.sh's way to keep a new registry's
// images in a bucket (`dock admin registry-storage`): it saves the
// connection (adding it, or updating the one with the same name) and
// points the registry at it. Images already on disk would be left
// behind, so install.sh only calls it while the storage dir is empty; a
// registry already using another connection is refused. The daemon
// applies it when it starts.
func (c *Core) InstallRegistryStorage(ctx context.Context, a Actor, req apitypes.CreateStorageRequest) (apitypes.Storage, error) {
	if err := a.requirePlatform(RegistryManage, "setting the registry's storage"); err != nil {
		return apitypes.Storage{}, err
	}
	regID, err := c.registryStorageID(ctx)
	if err != nil {
		return apitypes.Storage{}, err
	}
	row, err := c.store.GetStorageConnection(ctx, req.Name)
	var st apitypes.Storage
	switch {
	case regID != nil && (err != nil || *regID != row.ID):
		if err == nil || store.IsNotFound(err) {
			err = conflict("the registry already keeps its images in another storage connection; move them with `dock registry migrate`")
		}
	case store.IsNotFound(err):
		st, err = c.AddStorage(ctx, a, req)
	case err != nil:
	default:
		st, err = c.UpdateStorage(ctx, a, req.Name, apitypes.UpdateStorageRequest{
			Endpoint: &req.Endpoint, Region: &req.Region, Bucket: &req.Bucket, PathStyle: req.PathStyle,
			AccessKeyID: &req.AccessKeyID, SecretAccessKey: &req.SecretAccessKey,
		})
	}
	if err != nil {
		return apitypes.Storage{}, err
	}
	row, err = c.store.GetStorageConnection(ctx, req.Name)
	if err != nil {
		return apitypes.Storage{}, err
	}
	if regID == nil || *regID != row.ID {
		// Two registries in one bucket would collect each other's layers.
		b, err := c.bucketOf(row)
		if err != nil {
			return apitypes.Storage{}, err
		}
		used, err := (&s3.Client{Bucket: b}).HasPrefix(ctx, registryBucketRoot+"/")
		if err != nil {
			return apitypes.Storage{}, invalid("can't list bucket %s: %v", b.Name, err)
		}
		if used {
			return apitypes.Storage{}, conflict("bucket %s already holds registry data under %s/, maybe another Dock's; use another bucket, or empty that folder", b.Name, registryBucketRoot)
		}
		if err := c.store.SetRegistryStorage(ctx, store.SetRegistryStorageParams{RegistryStorageID: &row.ID, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
			return apitypes.Storage{}, err
		}
		c.audit(ctx, a.platform(), "registry.storage", nil, nil, "registry", "the registry keeps its images in "+req.Name)
	}
	st.UsedBy = []string{"registry"}
	return st, nil
}

// storedTimeout bounds measuring the registry's size for GetRegistry.
const storedTimeout = 5 * time.Second

// registryStorageSays starts what a failed push's error adds about the
// registry's bucket (bucketTrouble); the push_failed diagnosis reads it.
const registryStorageSays = "the registry's storage: "

// bucketTrouble says what is wrong with the registry's bucket or spool
// (ADR 0084) and what to do, for a push that failed; "" when nothing is.
func bucketTrouble(b *apitypes.RegistryBucket) string {
	switch {
	case b == nil:
		return ""
	case b.Refusing == "spool":
		return fmt.Sprintf("the bucket is too slow, and the %s of pushes waiting on the control plane's disk filled the spool; deploys work again as uploads free it, see `dock registry`", humanBytes(b.WaitingBytes))
	case b.Refusing == "disk":
		return "the control plane's disk is nearly full, so the registry takes no more pushes; free space on it, see `dock registry`"
	case b.State != "healthy":
		return fmt.Sprintf("the bucket is %s: %d of its last %d calls failed or took over 10 s; check the storage provider's status, see `dock registry`", b.State, b.Bad, b.Calls)
	}
	return ""
}

// registryBucketAlertKey names the alert of the registry's bucket.
const registryBucketAlertKey = "registry:bucket"

// RegistryBucketChanged raises, updates or resolves the platform alert
// for the registry's bucket when the storage gateway's view of it
// changes (ADR 0084): degraded or failing from the calls it makes anyway,
// or refusing pushes because the spool or the disk is full. No timer
// checks it; the gateway calls this on its transitions.
func (c *Core) RegistryBucketChanged(ctx context.Context, b *apitypes.RegistryBucket) {
	if b == nil || b.State == "healthy" && b.Refusing == "" {
		c.signalAlert(ctx, alertSignal{Key: registryBucketAlertKey, Resolve: true})
		return
	}
	sev, title := apitypes.SeverityWarning, "The registry's bucket is "+b.State
	msg := fmt.Sprintf("%d of the last %d calls to the bucket failed or took over 10 s. Pushes land on the control plane's disk and upload when the bucket answers, so deploys keep working; %d objects (%s) wait to upload. If it lasts, check the storage provider's status.",
		b.Bad, b.Calls, b.Waiting, humanBytes(b.WaitingBytes))
	switch b.Refusing {
	case "spool":
		sev, title = apitypes.SeverityCritical, "The registry refuses pushes: its spool is full"
		msg = fmt.Sprintf("The bucket is too slow to take what was pushed: %s waits on the control plane's disk, the spool's limit. Builds fail at the push until uploads free space. Check the storage provider's status, or move the images to this server with `dock registry migrate --to local` once the bucket answers.", humanBytes(b.WaitingBytes))
	case "disk":
		sev, title = apitypes.SeverityCritical, "The registry refuses pushes: the disk is nearly full"
		msg = fmt.Sprintf("Less than %d%% of the control plane's disk would stay free, so the registry takes no more pushes; %s of them wait to upload to the bucket. Free space on the disk.", b.DiskMinFreePercent, humanBytes(b.WaitingBytes))
	}
	c.signalAlert(ctx, alertSignal{
		Key: registryBucketAlertKey, Kind: apitypes.AlertConnection, Severity: sev,
		Title: title, Message: msg, Command: "dock registry", Target: &apitypes.AlertTarget{Connection: registryBucketAlertKey},
	})
}
