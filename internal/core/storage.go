package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/s3"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Storage connections (ADR 0034): S3-compatible buckets defined once, by
// name, and pointed at by what keeps data in object storage. The registry
// is the first user (platform_settings.registry_storage_id); backups are
// to follow (the ADR's follow-up). Keys are sealed with c.box and never
// returned. Operators only.

// storageCheckTimeout bounds the bucket check when a connection is saved.
const storageCheckTimeout = 15 * time.Second

// storageKeys are a connection's sealed credentials.
type storageKeys struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// ListStorage returns the storage connections, by name.
func (c *Core) ListStorage(ctx context.Context, a Actor) ([]apitypes.Storage, error) {
	if err := a.requirePlatform(PlatformView, "listing storage connections"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListStorageConnections(ctx)
	if err != nil {
		return nil, err
	}
	uses, err := c.storageUses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Storage, len(rows))
	for i, r := range rows {
		out[i] = toStorage(r, uses)
	}
	return out, nil
}

// AddStorage saves a new storage connection after checking that its
// bucket can be listed with its keys.
func (c *Core) AddStorage(ctx context.Context, a Actor, req apitypes.CreateStorageRequest) (apitypes.Storage, error) {
	if err := a.requirePlatform(BackupsManage, "adding storage connections"); err != nil {
		return apitypes.Storage{}, err
	}
	if err := validName("storage connection", req.Name); err != nil {
		return apitypes.Storage{}, err
	}
	b := s3.Bucket{
		Endpoint: strings.TrimRight(strings.TrimSpace(req.Endpoint), "/"), Region: strings.TrimSpace(req.Region),
		Name: strings.TrimSpace(req.Bucket), PathStyle: req.PathStyle == nil || *req.PathStyle,
		AccessKeyID: strings.TrimSpace(req.AccessKeyID), SecretAccessKey: strings.TrimSpace(req.SecretAccessKey),
	}
	if _, err := c.store.GetStorageConnection(ctx, req.Name); err == nil {
		return apitypes.Storage{}, conflict("a storage connection named %s exists; pick another name or change it with `dock storage set %s`", req.Name, req.Name)
	} else if !store.IsNotFound(err) {
		return apitypes.Storage{}, err
	}
	if err := checkBucket(ctx, b); err != nil {
		return apitypes.Storage{}, err
	}
	keys, err := c.sealKeys(b, req.Name)
	if err != nil {
		return apitypes.Storage{}, err
	}
	t := now()
	row, err := c.store.CreateStorageConnection(ctx, store.CreateStorageConnectionParams{
		Name: req.Name, Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Name, PathStyle: b.PathStyle,
		Credentials: keys, CreatedAt: t, UpdatedAt: t, UpdatedBy: a.Name,
	})
	if err != nil {
		return apitypes.Storage{}, err
	}
	c.audit(ctx, a.platform(), "storage.create", nil, nil, req.Name, fmt.Sprintf("added storage %s: bucket %s at %s", req.Name, b.Name, b.Endpoint))
	return toStorage(row, nil), nil
}

// UpdateStorage changes a storage connection and checks the bucket again
// if anything but its name changed. While the registry keeps its images
// in it, the endpoint and bucket stay (the images are there; `dock
// registry migrate` moves them), and new keys or addressing are written
// into the registry's config, restarting it, so that waits until no
// build or pull uses the registry. A rename touches nothing else: the
// registry refers to the connection by ID.
func (c *Core) UpdateStorage(ctx context.Context, a Actor, name string, req apitypes.UpdateStorageRequest) (apitypes.Storage, error) {
	if err := a.requirePlatform(BackupsManage, "changing storage connections"); err != nil {
		return apitypes.Storage{}, err
	}
	row, b, err := c.storageByName(ctx, name)
	if err != nil {
		return apitypes.Storage{}, err
	}
	uses, err := c.storageUses(ctx)
	if err != nil {
		return apitypes.Storage{}, err
	}
	if c.registryMigrating(row.ID) || c.backupsMovingTo(row.ID) {
		return apitypes.Storage{}, conflict("data is moving to or from %s; change it once the move finishes", name)
	}
	// The registry follows new keys by restarting; backups read them on
	// their next run. Neither can follow its data to another bucket.
	inUse := slices.Contains(uses[row.ID], "registry")
	usedBy := uses[row.ID]
	// Keys keep every character but surrounding space: a secret may end
	// in a slash, an endpoint loses its trailing one.
	trim := func(v *string, cut string) *string {
		if v == nil {
			return nil
		}
		s := strings.TrimRight(strings.TrimSpace(*v), cut)
		return &s
	}
	old := b
	patch(&b.Endpoint, trim(req.Endpoint, "/"))
	patch(&b.Region, trim(req.Region, ""))
	patch(&b.Name, trim(req.Bucket, ""))
	patch(&b.PathStyle, req.PathStyle)
	patch(&b.AccessKeyID, trim(req.AccessKeyID, ""))
	patch(&b.SecretAccessKey, trim(req.SecretAccessKey, ""))
	if len(usedBy) > 0 && (b.Endpoint != old.Endpoint || b.Name != old.Name) {
		return apitypes.Storage{}, conflict("%s keeps its data in %s; move it elsewhere (`dock registry migrate`, `dock backup move`) before pointing it at another endpoint or bucket", strings.Join(usedBy, " and "), name)
	}
	newName := name
	if req.Name != nil && *req.Name != name {
		newName = *req.Name
		if err := validName("storage connection", newName); err != nil {
			return apitypes.Storage{}, err
		}
		if _, err := c.store.GetStorageConnection(ctx, newName); err == nil {
			return apitypes.Storage{}, conflict("a storage connection named %s exists; pick another name", newName)
		} else if !store.IsNotFound(err) {
			return apitypes.Storage{}, err
		}
	}
	changed := b != old
	if !changed && newName == name {
		return toStorage(row, uses), nil
	}
	// The keys are bound to the name (ADR 0041), so a rename seals them
	// again too.
	keys, err := c.sealKeys(b, newName)
	if err != nil {
		return apitypes.Storage{}, err
	}
	if changed {
		if err := checkBucket(ctx, b); err != nil {
			return apitypes.Storage{}, err
		}
		if inUse {
			release, err := c.holdRegistry()
			if err != nil {
				return apitypes.Storage{}, err
			}
			defer release()
		}
	}
	row, err = c.store.UpdateStorageConnection(ctx, store.UpdateStorageConnectionParams{
		Name: newName, Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Name, PathStyle: b.PathStyle,
		Credentials: keys, UpdatedAt: now(), UpdatedBy: a.Name, ID: row.ID,
	})
	if err != nil {
		return apitypes.Storage{}, err
	}
	var did []string
	if newName != name {
		did = append(did, "renamed storage "+name+" to "+newName)
	}
	if changed {
		did = append(did, "changed storage "+newName)
	}
	c.audit(ctx, a.platform(), "storage.update", nil, nil, newName, strings.Join(did, "; "))
	if changed && inUse && c.reg != nil {
		if err := c.reg.UseStorage(ctx, &b); err != nil {
			return toStorage(row, uses), fmt.Errorf("saved, but the registry didn't take the change (restarting the daemon retries): %w", err)
		}
	}
	return toStorage(row, uses), nil
}

// RemoveStorage deletes a storage connection nothing uses. The bucket and
// what is in it are left alone.
func (c *Core) RemoveStorage(ctx context.Context, a Actor, name string) error {
	if err := a.requirePlatform(BackupsManage, "removing storage connections"); err != nil {
		return err
	}
	row, err := c.store.GetStorageConnection(ctx, name)
	if store.IsNotFound(err) {
		return notFound("no storage connection named %s; `dock storage` lists them", name)
	}
	if err != nil {
		return err
	}
	// Migrations first: one that commits after this check holds the
	// setting the next check reads.
	if c.registryMigrating(row.ID) || c.backupsMovingTo(row.ID) {
		return conflict("data is moving to or from %s; wait for the move to finish", name)
	}
	uses, err := c.storageUses(ctx)
	if err != nil {
		return err
	}
	switch {
	case slices.Contains(uses[row.ID], "registry"):
		return conflict("the registry keeps its images in %s; move them with `dock registry migrate --to local` (or to other storage) first", name)
	case slices.Contains(uses[row.ID], "backups"):
		return conflict("backups go to %s; move them to other storage with `dock backup move` first", name)
	}
	if err := c.store.DeleteStorageConnection(ctx, row.ID); err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "storage.delete", nil, nil, name, "removed storage "+name+" (the bucket is untouched)")
	return nil
}

// storageByName loads a connection and opens its keys.
func (c *Core) storageByName(ctx context.Context, name string) (store.StorageConnection, s3.Bucket, error) {
	row, err := c.store.GetStorageConnection(ctx, name)
	if store.IsNotFound(err) {
		return row, s3.Bucket{}, notFound("no storage connection named %s; `dock storage` lists them", name)
	}
	if err != nil {
		return row, s3.Bucket{}, err
	}
	b, err := c.bucketOf(row)
	return row, b, err
}

// storageByID loads the connection a setting points at.
func (c *Core) storageByID(ctx context.Context, id int64) (store.StorageConnection, error) {
	row, err := c.store.GetStorageConnectionByID(ctx, id)
	if store.IsNotFound(err) {
		return row, fmt.Errorf("storage connection %d is gone", id)
	}
	return row, err
}

// bucketOf is a connection's bucket with its keys opened.
func (c *Core) bucketOf(row store.StorageConnection) (s3.Bucket, error) {
	plain, err := c.box.Open(row.Credentials, sealedStorageKeys.AAD(row.Name))
	if err != nil {
		return s3.Bucket{}, fmt.Errorf("open the keys of storage %s: %w", row.Name, err)
	}
	var k storageKeys
	if err := json.Unmarshal(plain, &k); err != nil {
		return s3.Bucket{}, fmt.Errorf("keys of storage %s: %w", row.Name, err)
	}
	return s3.Bucket{
		Endpoint: row.Endpoint, Region: row.Region, Name: row.Bucket, PathStyle: row.PathStyle,
		AccessKeyID: k.AccessKeyID, SecretAccessKey: k.SecretAccessKey,
	}, nil
}

// sealKeys seals b's keys for storage_connections.credentials of the
// connection called name.
func (c *Core) sealKeys(b s3.Bucket, name string) ([]byte, error) {
	plain, err := json.Marshal(storageKeys{AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey})
	if err != nil {
		return nil, err
	}
	return c.box.Seal(plain, sealedStorageKeys.AAD(name))
}

// checkBucket validates b and lists its bucket once, so mistakes show
// when a connection is saved.
func checkBucket(ctx context.Context, b s3.Bucket) error {
	if err := b.Validate(); err != nil {
		return invalid("%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, storageCheckTimeout)
	defer cancel()
	err := (&s3.Client{Bucket: b}).Check(ctx)
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*s3.Error](err); ok {
		switch e.Code {
		case "NoSuchBucket":
			return invalid("bucket %s doesn't exist at %s; create it in the provider's console first", b.Name, b.Endpoint)
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied":
			return invalid("%s refused the keys for bucket %s (%s); check the access key, the secret and the region", b.Endpoint, b.Name, e.Code)
		case "AuthorizationHeaderMalformed", "InvalidRegion", "IllegalLocationConstraintException":
			return invalid("%s wants another region for bucket %s (%s); set the region the provider names", b.Endpoint, b.Name, e.Code)
		}
	}
	return invalid("can't list bucket %s at %s: %v; check the endpoint, the bucket and whether it needs path-style addressing", b.Name, b.Endpoint, err)
}

// registryStorageID is the connection the registry uses, nil for the
// control plane's disk.
func (c *Core) registryStorageID(ctx context.Context) (*int64, error) {
	id, err := c.store.GetRegistryStorageID(ctx)
	if store.IsNotFound(err) {
		return nil, nil
	}
	return id, err
}

// toStorage is a connection as the API shows it, with what keeps data in
// it (storageUses) as used_by. Keys stay out.
func toStorage(r store.StorageConnection, uses map[int64][]string) apitypes.Storage {
	return apitypes.Storage{
		Name: r.Name, Endpoint: r.Endpoint, Region: r.Region, Bucket: r.Bucket, PathStyle: r.PathStyle,
		UsedBy: append([]string{}, uses[r.ID]...), UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy,
	}
}

// storageUses says what keeps data in each storage connection: "registry"
// and "backups", by connection ID. Two reads, however many connections.
func (c *Core) storageUses(ctx context.Context) (map[int64][]string, error) {
	uses := map[int64][]string{}
	regID, err := c.registryStorageID(ctx)
	if err != nil {
		return nil, err
	}
	if regID != nil {
		uses[*regID] = append(uses[*regID], "registry")
	}
	set, err := c.store.GetBackupSettings(ctx)
	if err != nil && !store.IsNotFound(err) {
		return nil, err
	}
	if set.StorageID != nil {
		uses[*set.StorageID] = append(uses[*set.StorageID], "backups")
	}
	return uses, nil
}
