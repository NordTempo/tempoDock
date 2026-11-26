package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
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
// Upkeep runs after deploys and archives, never on a timer: a service's
// images past its rollback window are removed from its server (and its
// builder), and at most once per registryPruneEvery the registry drops
// manifests no release keeps, then collects their blobs. Archived
// services keep no release (they can't be rolled back or restored), so
// their images leave the registry with the next prune. Both only touch
// images the platform made (build tags and registry references), never
// CI images.

// registryPruneEvery is the least time between two registry prunes. A
// prune is started by a deploy or an archive, so an idle daemon never
// prunes.
const registryPruneEvery = time.Hour

// imageSweep is how many releases past a service's rollback window each
// deploy looks at to remove their images. Every deploy moves the window by
// one, so a few cover what earlier sweeps missed (an offline server).
const imageSweep = 3

// buildTagPrefix starts every build tag (buildTag).
const buildTagPrefix = node.BuildTagPrefix

// imageUpkeep is the state of registry pruning.
type imageUpkeep struct {
	// gate is used by every build that pushes to the registry and every
	// pull from it; a prune, a migration or a storage change holds it, so
	// none runs next to one (GarbageCollect stops the registry). A prune
	// yields to a build that waits (registryGate).
	gate registryGate

	mu        sync.Mutex
	pruning   bool
	lastPrune time.Time
	// collect: manifests were pruned whose blobs aren't collected yet.
	collect bool
	// lastPruned and lastErr are the last prune's outcome, for the
	// console: how many manifests it deleted, and why it failed.
	lastPruned int
	lastErr    string
	// lastSkipped is what the last prune that ran through skipped.
	lastSkipped []string
	// migration is the latest move of the images to other storage, which
	// holds the gate while it runs (MigrateRegistry); migrateIDs are its
	// connections (nil: local).
	migration  *apitypes.RegistryMigration
	migrateIDs [2]*int64
}

// migrating reports whether a migration runs. The caller holds mu.
func (u *imageUpkeep) migrating() bool { return u.migration != nil && u.migration.Running }

// useRegistry marks a build or pull that uses the registry until release
// is called, waiting while a prune or migration holds it (registryGate);
// an error only when ctx ends first. Uses may nest (a build's pull).
func (c *Core) useRegistry(ctx context.Context) (release func(), err error) {
	return c.images.gate.use(ctx)
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
	project, ok := buildTagProject(repo)
	return ok && project == ss.project.ID
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

// buildTagRef reads a build tag (buildTag), in any spelling Docker takes
// for the same name: with or without Docker Hub's host, where a name
// without a registry resolves.
var buildTagRef = regexp.MustCompile(`^(?i:(?:(?:index\.|registry-1\.)?docker\.io/)?)dock/p([0-9]+)_e[0-9]+_s[0-9]+(?:[:@].*)?$`)

// buildTagProject is the project a build tag names; ok is false for any
// other reference. A build tag is a local name: it is what Dock calls an
// image it built on a server, never something to pull, and to Docker it
// reads as a repository on Docker Hub that isn't Dock's.
func buildTagProject(ref string) (project int64, ok bool) {
	m := buildTagRef.FindStringSubmatch(ref)
	if m == nil {
		return 0, false
	}
	project, err := strconv.ParseInt(m[1], 10, 64)
	return project, err == nil
}

// dockNamed reports whether ref is a name Dock gives images it builds: a
// build tag or a registry reference, whoever's. It says nothing about
// whose the image is: ownImage does.
func (c *Core) dockNamed(ref string) bool {
	_, inReg := c.inRegistry(ref)
	_, tag := buildTagProject(ref)
	return inReg || tag
}

// ownImage reports whether ref names an image the platform built for
// project projectID: its build tag or its registry reference. Only those
// are removed, or read from a server by name, for one of the project's
// releases: the name alone isn't enough, since anyone can write a
// reference into a release that fails.
func (c *Core) ownImage(projectID int64, ref string) bool {
	if repo, ok := c.inRegistry(ref); ok {
		p, ok := buildTagProject(repo)
		return ok && p == projectID
	}
	p, ok := buildTagProject(ref)
	return ok && p == projectID
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

// warmBuilder has a builder fetch what builds need before its first one
// (Railpack's images, ADR 0089 amendment), in the background. It is
// called whenever a server may have become a builder that can (the
// control plane at start, a server on connect, one set to build); the
// server skips what it has fetched already. Failures only cost the first
// Railpack build the fetch.
func (c *Core) warmBuilder(ctx context.Context, serverID int64) {
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil || !c.canBuild(srv) {
		return
	}
	rt := c.connected(serverID)
	if rt == nil {
		return
	}
	c.goCounted("warming a builder", func() {
		if err := rt.WarmBuilder(c.ctx); err != nil && c.ctx.Err() == nil {
			c.log.Warn("fetch Railpack's images ahead of builds; the first Railpack build fetches them", "server", srv.Name, "err", c.nodeErr(serverID, err))
		}
	})
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
		if rm := (removal{server, ref}); c.ownImage(ss.project.ID, ref) && !keptRef[ref] && !seen[rm] {
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

// errPruneYielded ends a prune that stopped early because a build or pull
// waited for the registry (registryGate's yield).
var errPruneYielded = errors.New("the prune stopped early for a build or pull that needs the registry; the next prune goes on")

// maybePruneRegistry starts a registry prune in the background if none
// ran within registryPruneEvery and no build or pull uses the registry.
// Deploys call it when they finish, and archives once they are committed.
// A build or pull that comes meanwhile stops it (yieldPrune).
func (c *Core) maybePruneRegistry() {
	if c.reg == nil {
		return
	}
	ctx, yield := context.WithCancel(c.ctx)
	u := &c.images
	u.mu.Lock()
	due := !u.pruning && time.Since(u.lastPrune) >= registryPruneEvery && u.gate.tryHold(yield)
	u.pruning = u.pruning || due
	u.mu.Unlock()
	if !due {
		yield()
		return
	}
	c.wg.Add(1) // like finishArchive's stops
	c.goCaught("pruning the registry", func() {
		defer c.wg.Done()
		defer yield()
		_, _ = c.pruneRegistry(ctx)
	})
}

// pruneRegistry deletes the manifests no release keeps (ListKeptDigests),
// then collects unreferenced blobs if anything was deleted (now or by an
// earlier prune whose collection failed), and returns how many manifests
// it deleted. The caller has set pruning and holds the gate, which ends
// ctx when a build or pull waits for the registry; pruneRegistry releases
// both and records the outcome. A failed or stopped prune doesn't move
// lastPrune, so the next deploy tries again; a stopped one (errPruneYielded)
// is no failure: stopping a collection midway only leaves blobs for the
// next one. A prune that skipped images it couldn't read or delete
// (registry.Client.Prune) still counts as done, with an alert naming
// them; the next prune tries them again.
func (c *Core) pruneRegistry(ctx context.Context) (n int, err error) {
	u := &c.images
	var skipped []error
	defer func() {
		if err != nil && ctx.Err() != nil && c.ctx.Err() == nil {
			err = errPruneYielded
		}
		u.gate.release()
		if errors.Is(err, errPruneYielded) {
			u.mu.Lock()
			u.pruning, u.lastPruned = false, n
			u.mu.Unlock()
			c.log.Info("registry prune stopped early: a build or pull needs the registry; the next prune goes on")
			return
		}
		if err != nil {
			c.log.Warn("registry prune", "err", err)
		}
		for _, e := range skipped {
			c.log.Warn("registry prune skipped an image", "err", e)
		}
		u.mu.Lock()
		u.pruning, u.lastPruned, u.lastErr = false, n, ""
		if err != nil {
			u.lastErr = err.Error()
		} else {
			u.lastPrune = time.Now()
			u.lastSkipped = nil
			for _, e := range skipped {
				u.lastSkipped = append(u.lastSkipped, e.Error())
			}
		}
		u.mu.Unlock()
		// The alert is written without u.mu, which the console's reads take.
		switch {
		case err != nil:
			c.connectionFailed(ctx, "registry:prune", "registry:prune", "The registry's prune is failing",
				fmt.Sprintf("Pruning old images from the platform registry failed: %v. Its storage keeps growing until a prune works; the next deploy tries again.", err), "dock registry prune")
		case len(skipped) > 0:
			c.connectionFailed(ctx, "registry:prune", "registry:prune", "The registry's prune skipped images it couldn't read",
				fmt.Sprintf("The prune passed over what it couldn't read or delete, and pruned the rest: %s. Those images stay in storage until a prune gets through them; the next one tries again. One that keeps failing is likely an object stuck in the registry's bucket.", skippedSummary(skipped)), "dock registry prune")
		default:
			c.connectionWorks(ctx, "registry:prune")
		}
	}()
	keep, err := c.store.ListKeptDigests(ctx, int64(c.keepReleases()))
	if err != nil {
		return 0, err
	}
	if len(keep) == 0 {
		return 0, nil // nothing was ever pushed, or nothing is kept: leave it
	}
	n, skipped, err = c.reg.Prune(ctx, keep)
	u.mu.Lock()
	collect := u.collect || n > 0
	u.collect = collect
	u.mu.Unlock()
	if n > 0 {
		c.log.Info("registry: pruned images no release keeps", "manifests", n)
	}
	if err != nil || !collect {
		return n, err
	}
	if err := c.reg.GarbageCollect(ctx); err != nil {
		return n, fmt.Errorf("garbage collection: %w", err)
	}
	u.mu.Lock()
	u.collect = false
	u.mu.Unlock()
	return n, nil
}

// skippedSummary names what a prune skipped for its alert: the first
// few, each with its reason, and how many more.
func skippedSummary(skipped []error) string {
	const shown = 3
	parts := make([]string, 0, shown)
	for _, e := range skipped[:min(len(skipped), shown)] {
		parts = append(parts, e.Error())
	}
	s := strings.Join(parts, "; ")
	if more := len(skipped) - shown; more > 0 {
		s += fmt.Sprintf("; and %d more (see the daemon's log)", more)
	}
	return s
}

// keepReleases is the rollback window: how many of a service's newest
// releases keep their images.
func (c *Core) keepReleases() int { return max(c.cfg.Defaults.KeepReleases, 1) }

// validateImageRef refuses image references a user gives that are under
// the platform registry's host (registry, "" without one: those images
// are pulled with tokens the control plane mints, see pullAllowed), or
// whose registry is localhost or an address g refuses (imageAddrErr).
// A reference Docker couldn't parse is refused too, before a release is
// made for it; "" (no image) passes. Docker Hub refs and public
// registries (ghcr.io, registry.example.com:5000) pass. Hostnames are
// resolved when pulling (checkRegistry), not here.
func validateImageRef(ref, registry string, g *egressGuard) error {
	if _, ok := buildTagProject(ref); ok {
		return invalid("image %q is a name Dock gives the images it builds, which are deployed by their service, not by name; deploy from source or promote, or use an image's name on its own registry", ref)
	}
	if _, err := reference.ParseNormalizedNamed(ref); ref != "" && err != nil {
		return invalid("image %q is not a valid image reference; write it in lower case as name[:tag] or registry/name[:tag], like nginx:1.27 or ghcr.io/acme/app:v1", ref)
	}
	host := registryHost(ref)
	if host == "" {
		return nil
	}
	if registry != "" && strings.EqualFold(host, registry) {
		return invalid("image %q is in the platform registry, which only holds images the platform built; deploy from source, promote, or use another registry", ref)
	}
	h := registryAddr(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return invalid("image %q is on a localhost registry; use a public registry", ref)
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return imageAddrErr(g, ref, ip)
	}
	return nil
}

// registryAddr is a registry host[:port] without the port and brackets,
// lower-cased: a name or an address.
func registryAddr(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// imageAddrErr refuses a registry at ip that g refuses. A server's Docker
// daemon pulls from the host, outside the app firewall, so a pull from
// such a registry would let anyone who can name an image reach the
// server's own loopback services (the API, the registry), cloud metadata
// or, unless the operator allows them for apps (`[network]
// allow_private`), the LAN and tailnet.
func imageAddrErr(g *egressGuard, ref string, ip netip.Addr) error {
	if g.addrErr(ip) == nil {
		return nil
	}
	if ip = ip.Unmap(); httpkit.IsSelfAddr(ip) || httpkit.IsInternalAddr(ip) {
		return invalid("image %q is on a registry at %s, a loopback, link-local or cloud metadata address, which servers never pull from; push it to a public registry", ref, ip)
	}
	return invalid("image %q is on a registry at a private address (%s); push it to a public registry, or an operator lets apps and pulls reach that network with [network] allow_private", ref, ip)
}

// pullLookupTimeout bounds resolving a registry's name before a pull.
const pullLookupTimeout = 5 * time.Second

// checkRegistry refuses a pull of ref from its registry (host, as
// imageRegistry returns it) when the host is, or its name resolves to, an
// address imageAddrErr refuses. Every pull outside the platform registry
// passes it through registryLogin. Docker Hub isn't looked up. The name
// is resolved on the control plane, and a name it can't resolve is
// refused: it may be one only a remote server's DNS knows, on its LAN.
// Docker resolves the name again for the pull, and follows a registry's
// token realm and redirects wherever they point; see docs/safety-model.md
// for what that leaves open.
func (c *Core) checkRegistry(ctx context.Context, ref, host string) error {
	if host == dockerHub {
		return nil
	}
	h := registryAddr(host)
	if ip, err := netip.ParseAddr(h); err == nil {
		return imageAddrErr(&c.pulls, ref, ip)
	}
	lctx, cancel := context.WithTimeout(ctx, pullLookupTimeout)
	defer cancel()
	addrs, err := c.lookupIP(lctx, h)
	if err != nil {
		return fmt.Errorf("look up registry %s before pulling %s from it (check the image name): %w", host, ref, err)
	}
	for _, ip := range addrs {
		if err := imageAddrErr(&c.pulls, ref, ip); err != nil {
			return err
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
