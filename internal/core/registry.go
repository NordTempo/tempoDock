package core

import (
	"context"
	"fmt"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
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
	// how many it deleted.
	Prune(ctx context.Context, keep []string) (int, error)
	// GarbageCollect removes blobs no manifest references any more. The
	// registry is stopped while it runs, so core calls it only when no
	// build or pull uses the registry.
	GarbageCollect(ctx context.Context) error
	// Stored returns the size of the registry's blobs on disk.
	Stored() (int64, error)
}

// GetRegistry describes the platform registry for the console: its size,
// how pruning went since the daemon started, and the services keeping
// images in it, in one query. Operators only.
func (c *Core) GetRegistry(ctx context.Context, a Actor) (apitypes.Registry, error) {
	if err := a.requireOperator("reading the registry"); err != nil {
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
	stored, err := c.reg.Stored()
	if err != nil {
		c.log.Debug("registry size", "err", err)
	}
	out := apitypes.Registry{
		Enabled:      true,
		Host:         c.reg.Host(),
		StoredBytes:  stored,
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
	defer u.mu.Unlock()
	out.Pruning = u.pruning
	out.LastPruned = u.lastPruned
	out.LastPruneError = u.lastErr
	out.CollectPending = u.collect
	if !u.lastPrune.IsZero() {
		at := u.lastPrune.UTC().Truncate(time.Second)
		out.LastPruneAt = &at
	}
	return out, nil
}

// PruneRegistry starts a registry prune now, whenever the last one ran:
// the images no release keeps are deleted and their layers collected
// (the registry stops for those seconds). It answers with the registry,
// pruning; registry.pruned or registry.prune_failed is audited when it
// ends. Refused while builds or pulls use the registry. Operators only.
func (c *Core) PruneRegistry(ctx context.Context, a Actor) (apitypes.Registry, error) {
	if err := a.requireOperator("pruning the registry"); err != nil {
		return apitypes.Registry{}, err
	}
	if c.reg == nil {
		return apitypes.Registry{}, invalid("this Dock has no registry; images stay on the server that built them")
	}
	u := &c.images
	u.mu.Lock()
	switch {
	case u.pruning:
		u.mu.Unlock()
		return apitypes.Registry{}, conflict("the registry is being pruned already; wait for it to finish")
	case !u.inUse.TryLock():
		u.mu.Unlock()
		return apitypes.Registry{}, conflict("builds or pulls are using the registry; prune once they finish")
	}
	u.pruning = true
	u.mu.Unlock()
	c.audit(ctx, a, "registry.prune", nil, nil, "registry", "started a registry prune")
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		n, err := c.pruneRegistry(c.ctx)
		if err != nil {
			c.audit(c.ctx, System, "registry.prune_failed", nil, nil, "registry", fmt.Sprintf("registry prune failed: %v", err))
			return
		}
		c.audit(c.ctx, System, "registry.pruned", nil, nil, "registry", fmt.Sprintf("pruned %d images no release keeps", n))
	}()
	return c.registryInfo(ctx)
}
