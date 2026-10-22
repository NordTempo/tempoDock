package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Images across servers (ADR 0014 section 4). Source builds run on
// builder servers. With the registry, every build is pushed to it as
// <host>/dock/p<project>_e<env>_s<service>:<release>, the release records
// the manifest digest, and any other server pulls <host>/<repo>@<digest>
// with a pull token minted for that one repository. Without a registry
// (single-server installs) images stay where they were built.
//
// Upkeep runs after deploys, never on a timer: a service's images past its
// rollback window are removed from its server (and its builder), and at
// most once per registryPruneEvery the registry drops manifests no release
// keeps, then collects their blobs. Both only touch images the platform
// made (build tags and registry references), never CI images.

// registryPruneEvery is the least time between two registry prunes. A
// prune is started by a deploy, so an idle daemon never prunes.
const registryPruneEvery = time.Hour

// imageSweep is how many releases past a service's rollback window each
// deploy looks at to remove their images. Every deploy moves the window by
// one, so a few cover what earlier sweeps missed (an offline server).
const imageSweep = 3

// buildTagPrefix starts every build tag (buildTag).
const buildTagPrefix = "dock/p"

// imageUpkeep is the state of registry pruning.
type imageUpkeep struct {
	// inUse is read-held by every build that pushes to the registry and
	// every pull from it; pruning takes it with TryLock, so it never runs
	// next to one (GarbageCollect stops the registry).
	inUse sync.RWMutex

	mu        sync.Mutex
	pruning   bool
	lastPrune time.Time
	// collect: manifests were pruned whose blobs aren't collected yet.
	collect bool
}

// useRegistry marks a build or pull that uses the registry until release
// is called. Uses may nest (a build's pull): the write side only ever
// TryLocks, so no writer waits between two read locks.
func (c *Core) useRegistry() (release func()) {
	c.images.inUse.RLock()
	return c.images.inUse.RUnlock
}

// repoOf returns the repository of a platform image reference, a build
// tag (dock/p1_e2_s3:7) or a registry reference (<host>/dock/p1_e2_s3@sha256:...),
// without the registry host, tag or digest.
func repoOf(ref, host string) string {
	ref = strings.TrimPrefix(ref, host+"/")
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		return ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i]
	}
	return ref
}

// pullAllowed reports whether a deploy of ss may get a pull token for repo:
// the service's own repository, or another of its project's (where a
// promotion comes from: Promote checks read access on the source). Users
// can't name registry images themselves (validateImageRef), so this is
// the last check that a release can't carry another project's image.
func pullAllowed(ss svcScope, repo string) bool {
	rest, ok := strings.CutPrefix(repo, buildTagPrefix)
	if !ok {
		return false
	}
	project, _, ok := strings.Cut(rest, "_")
	return ok && project == strconv.FormatInt(ss.project.ID, 10)
}

// registryRef is where an image is in the registry: by digest.
func (c *Core) registryRef(ref, digest string) string {
	host := c.reg.Host()
	return host + "/" + repoOf(ref, host) + "@" + digest
}

// inRegistry returns the repository of a reference into the platform
// registry; ok is false for any other reference.
func (c *Core) inRegistry(ref string) (repo string, ok bool) {
	if c.reg == nil || !strings.HasPrefix(ref, c.reg.Host()+"/") {
		return "", false
	}
	return repoOf(ref, c.reg.Host()), true
}

// ownImage reports whether the platform made the image ref names: a
// build tag or a registry reference. Only those are ever removed.
func (c *Core) ownImage(ref string) bool {
	_, ok := c.inRegistry(ref)
	return ok || strings.HasPrefix(ref, buildTagPrefix)
}

// canBuild reports whether a server is a builder: set as one, not
// revoked, and (when connected) running BuildKit. An offline builder
// still counts, so errors can name it.
func (c *Core) canBuild(s store.Server) bool {
	if !s.Builder || s.RevokedAt != nil {
		return false
	}
	if s.Local || c.nodes == nil {
		return s.Local
	}
	st := c.nodes.State(s.ID)
	return st.Status != remote.StatusOnline || st.Hello.Builder
}

// builderFor picks the server that builds source for an environment of
// kind on target (ADR 0010): target itself when it is an online builder,
// else the oldest online builder, which pushes the image to the registry
// for target to pull. Without a registry an image can't move between
// servers, so target builds, as on single-server installs.
//
// A build receives the environment's variables (build args and secrets),
// so a production build only runs on a builder that accepts production
// environments: a non-production server never sees production secrets
// (ADR 0014 amendment, compromised node).
func (c *Core) builderFor(ctx context.Context, target store.Server, kind apitypes.EnvironmentKind) (store.Server, node.Runtime, error) {
	if c.reg == nil {
		rt, err := c.runtimeFor(target.ID)
		if err == nil && !target.Local && !c.canBuild(target) {
			err = invalid("server %s doesn't build images, and without the registry ([registry] enabled on the control plane) they can't come from another server; deploy a prebuilt image (--image) or make %s a builder", target.Name, target.Name)
		}
		return target, rt, err
	}
	trusted := func(s store.Server) bool { return kind != apitypes.KindProduction || s.AcceptsProduction }
	if c.canBuild(target) && trusted(target) {
		if rt := c.connected(target.ID); rt != nil {
			return target, rt, nil
		}
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return store.Server{}, nil, err
	}
	var offline, untrusted []string
	for _, s := range servers {
		if !c.canBuild(s) {
			continue
		}
		if !trusted(s) {
			untrusted = append(untrusted, s.Name)
			continue
		}
		if rt := c.connected(s.ID); rt != nil {
			return s, rt, nil
		}
		offline = append(offline, s.Name)
	}
	switch {
	case len(offline) > 0:
		return store.Server{}, nil, errf(apitypes.CodeServerOffline, "no builder is online (builders: %s); try again when one is back, or deploy a prebuilt image (--image)", strings.Join(offline, ", "))
	case len(untrusted) > 0:
		return store.Server{}, nil, invalid("production builds only run on servers that accept production environments, so production secrets never reach other servers, and no builder does (builders: %s); make a production server a builder with `dock server update <name> --builder`, or deploy an image built in CI (--image)", strings.Join(untrusted, ", "))
	}
	return store.Server{}, nil, invalid("no server builds images; make one a builder with `dock server update <name> --builder`, or deploy a prebuilt image (--image)")
}

// removeOldImages removes the images of a service's releases that fell
// out of its rollback window (Defaults.KeepReleases) from its server, and
// build tags from the builder that made them. Images a kept, active or
// deploying release still uses stay, and so does anything a container
// uses (the runtime refuses). Offline servers are skipped. At most
// imageSweep releases per deploy, in one query.
func (c *Core) removeOldImages(ctx context.Context, ss svcScope) {
	keep := max(c.cfg.Defaults.KeepReleases, 1)
	rows, err := c.store.ListReleases(ctx, store.ListReleasesParams{ServiceID: ss.svc.ID, Limit: int64(keep + imageSweep)})
	if err != nil || len(rows) <= keep {
		return
	}
	keptRef, keptID := map[string]bool{}, map[string]bool{}
	for i, r := range rows {
		if i < keep || !apitypes.ReleaseStatus(r.Status).Done() || r.Status == string(apitypes.ReleaseActive) {
			for _, ref := range c.imageNames(r) {
				keptRef[ref] = true
			}
			keptID[r.ImageID] = true
		}
	}
	type removal struct {
		server int64
		ref    string
	}
	seen := map[removal]bool{}
	var todo []removal
	add := func(server int64, ref string) {
		if rm := (removal{server, ref}); c.ownImage(ref) && !keptRef[ref] && !seen[rm] {
			seen[rm] = true
			todo = append(todo, rm)
		}
	}
	for _, r := range rows[keep:] {
		if keptRef[r.ImageRef] || (r.ImageID != "" && keptID[r.ImageID]) {
			continue
		}
		for _, ref := range c.imageNames(r) {
			add(ss.env.ServerID, ref)
		}
		if b := r.BuiltOnServerID; b != nil && *b != ss.env.ServerID {
			add(*b, r.ImageRef)
		}
	}
	for _, rm := range todo {
		rt := c.connected(rm.server)
		if rt == nil {
			continue
		}
		if err := rt.RemoveImage(ctx, rm.ref); err != nil && !errors.Is(err, node.ErrNotFound) {
			c.log.Debug("remove old image", "image", rm.ref, "err", err)
		}
	}
}

// imageNames are the local names a release's image can have on a server:
// its reference, and its registry reference when it was pushed.
func (c *Core) imageNames(r store.Release) []string {
	names := []string{r.ImageRef}
	if r.ImageDigest != "" && c.reg != nil {
		if rr := c.registryRef(r.ImageRef, r.ImageDigest); rr != r.ImageRef {
			names = append(names, rr)
		}
	}
	return names
}

// maybePruneRegistry starts a registry prune in the background if none
// ran within registryPruneEvery. Deploys call it when they finish.
func (c *Core) maybePruneRegistry() {
	if c.reg == nil {
		return
	}
	u := &c.images
	u.mu.Lock()
	due := !u.pruning && time.Since(u.lastPrune) >= registryPruneEvery
	u.pruning = u.pruning || due
	u.mu.Unlock()
	if !due {
		return
	}
	c.wg.Add(1) // callers run in a deploy goroutine, itself in wg
	go func() {
		defer c.wg.Done()
		ok := c.pruneRegistry(c.ctx)
		u.mu.Lock()
		u.pruning = false
		if ok {
			u.lastPrune = time.Now()
		}
		u.mu.Unlock()
	}()
}

// pruneRegistry deletes the manifests no release keeps (ListKeptDigests),
// then collects unreferenced blobs if anything was deleted (now or by an
// earlier prune whose collection failed). It returns false without doing
// anything while a build or pull uses the registry; the next deploy tries
// again.
func (c *Core) pruneRegistry(ctx context.Context) bool {
	u := &c.images
	if !u.inUse.TryLock() {
		return false
	}
	defer u.inUse.Unlock()
	keep, err := c.store.ListKeptDigests(ctx, int64(max(c.cfg.Defaults.KeepReleases, 1)))
	if err != nil {
		c.log.Warn("registry prune", "err", err)
		return false
	}
	if len(keep) == 0 {
		return true // nothing was ever pushed, or nothing is kept: leave it
	}
	n, err := c.reg.Prune(ctx, keep)
	if n > 0 {
		u.collect = true
		c.log.Info("registry: pruned images no release keeps", "manifests", n)
	}
	if err != nil {
		c.log.Warn("registry prune", "err", err)
		return false
	}
	if u.collect {
		if err := c.reg.GarbageCollect(ctx); err != nil {
			c.log.Warn("registry garbage collection", "err", err)
			return false
		}
		u.collect = false
	}
	return true
}

// cgnat is 100.64.0.0/10 (RFC 6598), where Tailscale puts its addresses:
// private in practice, though netip doesn't call it that.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// validateImageRef refuses image references a user gives that are under
// the platform registry's host (registry, "" without one: those images
// are pulled with tokens the control plane mints, see pullAllowed), or
// whose registry is this machine or a private network (localhost,
// loopback, link-local, RFC 1918, CGNAT/Tailscale and ULA addresses): a
// pull makes the Docker daemon connect there, which would let a deploy
// token probe internal services. Docker Hub refs and public registries
// (ghcr.io, registry.example.com:5000) pass. Hostnames are not resolved,
// so a public name pointing at a private address is not caught.
func validateImageRef(ref, registry string) error {
	host := registryHost(ref)
	if host == "" {
		return nil
	}
	if registry != "" && strings.EqualFold(host, registry) {
		return invalid("image %q is in the platform registry, which only holds images the platform built; deploy from source, promote, or use another registry", ref)
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return invalid("image %q is on a localhost registry; use a public registry", ref)
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() || cgnat.Contains(ip) {
			return invalid("image %q is on a registry at a private or loopback address (%s); use a public registry", ref, ip)
		}
	}
	return nil
}

// registryHost is the platform registry's host:port, "" without one.
func (c *Core) registryHost() string {
	if c.reg == nil {
		return ""
	}
	return c.reg.Host()
}

// registryHost returns the registry part (host[:port]) of an image
// reference, or "" for Docker Hub. Like Docker, the first path component is
// a registry only if it contains "." or ":" or is "localhost".
func registryHost(ref string) string {
	first, _, ok := strings.Cut(ref, "/")
	if !ok || !strings.ContainsAny(first, ".:") && first != "localhost" {
		return ""
	}
	return first
}
