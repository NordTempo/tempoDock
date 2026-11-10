package core

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// cleanupTimeout bounds cleanup after a failed or interrupted deploy, which
// runs even when the deploy's own context is cancelled.
const cleanupTimeout = time.Minute

// healthGrace is how long a container without a port must stay up. A var
// only so the real-Docker tests can shorten it (dockerHarness).
var healthGrace = 5 * time.Second

// deployment runs one release through the state machine:
// queued → building → starting → health_checking → active | failed.
type deployment struct {
	c    *Core
	ss   svcScope
	rel  store.Release
	rt   node.Runtime
	cfg  releaseConfig
	log  *logWriter
	spec releaseSpec
	// server is the environment's server, where the release runs.
	server store.Server

	old        *store.Release // active release when the deploy started
	stateful   bool
	oldRemoved bool // stateful recreate stopped (or tried to stop) the old container
	// orphans are containers already labelled for this release when it
	// started (orphanCheck): never its own, so fail leaves them alone.
	orphans []string
	// pre is how the pre-deploy command ran, for a diagnosis (ADR 0071);
	// nil when none ran.
	pre *apitypes.DiagnosisPreDeploy
}

type stepError struct {
	code apitypes.ErrorCode
	err  error
}

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func step(code apitypes.ErrorCode, format string, args ...any) error {
	return &stepError{code: code, err: fmt.Errorf(format, args...)}
}

// deploy runs in its own goroutine; the upload is removed when it finishes.
func (c *Core) deploy(ss svcScope, rel store.Release, spec releaseSpec) {
	defer c.wg.Done()
	if spec.upload != "" {
		defer func() { _ = os.Remove(spec.upload) }()
	}
	d := &deployment{c: c, ss: ss, rel: rel, rt: c.runtime(ss.env.ServerID), spec: spec}
	ctx := c.ctx
	err := d.run(ctx)
	if d.log != nil {
		d.log.Close()
	}
	if err != nil {
		d.fail(ctx, err)
	} else {
		c.afterActive(ctx, d.ss)
		c.syncRepoJobs(ctx, d.ss, d.cfg)
		c.kickBackups() // it may be newly stateful
	}
	if spec.done != nil {
		spec.done(err == nil)
	}
	if p := spec.promote; p != nil {
		p.ok = err == nil
		close(p.done)
	}
	c.gitDeployDone(ss, d.rel, err)
	c.previewReleaseDone(ss)
	c.touchSleep(ss.env.ID)
}

func (d *deployment) run(ctx context.Context) error {
	c := d.c
	var err error
	if p := d.spec.promote; p != nil && p.prev != nil {
		// Promoted services deploy one at a time; one failing stops the rest.
		select {
		case <-p.prev.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if !p.prev.ok {
			return step(apitypes.CodeDeployFailed, "not promoted: %s failed first", p.prev.failed())
		}
	}
	if err := d.waitForMove(ctx); err != nil {
		return err
	}
	// Deploying is a request to run it: an asleep environment wakes
	// first, its other services too (sleep.go).
	if d.ss.env.AsleepAt != nil || c.isAsleep(d.ss.env.ID) {
		d.event(ctx, "Waking environment "+d.ss.env.Name+" first")
		c.wakeFor(ctx, d.ss.envScope, "deploy")
		if env, err := c.store.GetEnvironment(ctx, d.ss.env.ID); err == nil {
			d.ss.env = env
		}
	}
	if d.server, err = c.store.GetServer(ctx, d.ss.env.ServerID); err != nil {
		return err
	}
	if d.cfg, err = c.openConfig(d.rel); err != nil {
		return err
	}
	if d.log, err = c.newBuildLog(d.ss, d.rel, newRedactor(d.cfg.Env)); err != nil {
		return err
	}

	if err := d.orphanCheck(ctx); err != nil {
		return err
	}

	// building: build from source or make sure the image is on the server.
	d.setStatus(ctx, apitypes.ReleaseBuilding)
	imageID, err := d.image(ctx)
	if err != nil {
		return err
	}
	d.rel.ImageID = imageID
	if err := c.store.SetReleaseImage(ctx, store.SetReleaseImageParams{ImageRef: d.rel.ImageRef, ImageID: imageID, ID: d.rel.ID}); err != nil {
		return err
	}

	// starting: the pre-deploy command first (ADR 0045), then a new
	// container next to the old one, or instead of it for stateful
	// services.
	d.setStatus(ctx, apitypes.ReleaseStarting)
	network := networkName(d.ss.envScope)
	if err := d.rt.EnsureNetwork(ctx, network, c.labels(d.ss, 0)); err != nil {
		return step(apitypes.CodeDeployFailed, "create network %s: %w", network, err)
	}
	if err := d.preDeploy(ctx); err != nil {
		return err
	}
	if old, err := c.store.GetActiveRelease(ctx, d.ss.svc.ID); err == nil {
		d.old = &old
	} else if !store.IsNotFound(err) {
		return err
	}
	d.stateful = len(d.cfg.Mounts) > 0
	for _, m := range d.cfg.Mounts {
		if err := d.rt.EnsureVolume(ctx, m.DockerName, c.labels(d.ss, 0)); err != nil {
			return step(apitypes.CodeDeployFailed, "create volume %s: %w", m.Volume, err)
		}
	}
	if d.stateful && d.old != nil {
		d.event(ctx, fmt.Sprintf("Stopping release %d first: services with volumes are replaced, never run side by side", d.old.Number))
		d.oldRemoved = true // from here on, failing brings it back
		if err := c.removeReleaseContainers(ctx, d.ss, d.old.ID, keepData); err != nil {
			return step(apitypes.CodeDeployFailed, "stop release %d: %w", d.old.Number, err)
		}
	}
	ctr, err := d.rt.Start(ctx, c.containerSpec(d.ss, d.rel, d.cfg))
	if err != nil {
		if strings.Contains(err.Error(), "already in use") {
			// A container of the same name that no release owns (see
			// orphanCheck): it is left alone.
			return step(apitypes.CodeDeployFailed, "start container: %w; Dock doesn't manage that container (it predates a database restored from an older backup): a platform operator has to remove it with `docker rm -f` on the server, then deploy again", err)
		}
		return step(apitypes.CodeDeployFailed, "start container: %w", err)
	}
	if err := c.store.SetReleaseContainer(ctx, store.SetReleaseContainerParams{ContainerID: ctr.ID, ID: d.rel.ID}); err != nil {
		return err
	}
	d.rel.ContainerID = ctr.ID
	d.event(ctx, "Started container "+ctr.Name)
	if paths := ctr.AnonymousVolumes; len(paths) > 0 {
		// Each is a fresh anonymous volume, so whatever the service wrote
		// there is not in this container (ADR 0052).
		d.event(ctx, fmt.Sprintf("Warning: no volume covers %s, where the image keeps data: every new release starts with it empty, and only the last replaced container's copy is kept on the server. Add a volume at that path to keep the data", strings.Join(paths, ", ")))
	}

	// health_checking
	d.setStatus(ctx, apitypes.ReleaseHealthChecking)
	hc := healthCheckOf(d.cfg)
	if err := d.rt.HealthCheck(ctx, ctr, hc); err != nil {
		return step(apitypes.CodeHealthCheckFailed, "health check failed (%s, timeout %s): %w", healthWhat(d.cfg, hc), hc.Timeout, err)
	}
	d.event(ctx, "Health check passed")
	return d.cutover(ctx)
}

// waitForMove holds a deploy while its environment moves to another
// server (ADR 0063), so a deploy during server maintenance isn't
// refused: it goes on once the move ended, on the server the environment
// is on then, with a host port there. The environment is read again even
// when no move is open any more: one may have ended between the release
// being made and its deploy getting here.
func (d *deployment) waitForMove(ctx context.Context) error {
	c := d.c
	if hook := c.moves.deployHook; hook != nil {
		hook()
	}
	if _, err := c.store.GetOpenEnvironmentMove(ctx, d.ss.env.ID); err == nil {
		d.event(ctx, "Waiting for server maintenance: the environment is moving to another server, and the deploy goes on once it is there")
		if _, err := c.waitForMove(ctx, d.ss.env.ID); err != nil {
			return err
		}
	} else if !store.IsNotFound(err) {
		return err
	}
	env, err := c.store.GetEnvironment(ctx, d.ss.env.ID)
	if err != nil || env.ServerID == d.ss.env.ServerID {
		return err
	}
	d.ss.env, d.rt = env, c.runtime(env.ServerID)
	if d.rel.HostPort == 0 {
		return nil
	}
	return c.store.Tx(ctx, func(q *store.Queries) error {
		port, err := c.allocateHostPort(ctx, q, env.ServerID)
		if err != nil {
			return err
		}
		d.rel.HostPort = port
		return q.MoveReleaseServing(ctx, store.MoveReleaseServingParams{HostPort: port, ContainerID: d.rel.ContainerID, ID: d.rel.ID})
	})
}

// healthCheckOf is the check a release's new container must pass before
// it gets traffic: a deploy's, and a moved environment's (ADR 0053).
func healthCheckOf(cfg releaseConfig) node.HealthCheck {
	hc := node.HealthCheck{
		Path:     cfg.HealthCheckPath,
		Timeout:  time.Duration(cfg.HealthCheckTimeout) * time.Second,
		Interval: time.Second,
		Grace:    min(healthGrace, time.Duration(cfg.HealthCheckTimeout)*time.Second),
	}
	if !cfg.Published {
		hc.Port = cfg.HealthPort
	}
	return hc
}

// healthWhat says what a health check probes, for its failure.
func healthWhat(cfg releaseConfig, hc node.HealthCheck) string {
	switch {
	case hc.Path != "":
		return "HTTP check on " + hc.Path
	case hc.Port != 0:
		return "TCP check on port " + strconv.Itoa(hc.Port) + " in the environment network"
	case !cfg.Published:
		return "container must stay running"
	}
	return "TCP check on port " + strconv.Itoa(cfg.Port)
}

// image builds or fetches the release's image and returns its ID.
func (d *deployment) image(ctx context.Context) (string, error) {
	ref := d.rel.ImageRef
	switch apitypes.ReleaseSource(d.rel.Source) {
	case apitypes.SourceUpload:
		return d.build(ctx)
	case apitypes.SourceImage:
		d.event(ctx, "Pulling "+ref)
		id, err := d.pull(ctx, ref)
		if err != nil {
			return "", step(apitypes.CodeBuildFailed, "pull %s: %w", ref, err)
		}
		return id, nil
	}
	// Config changes, rollbacks, restarts and promotions reuse an image:
	// it is on the server already, or comes from the registry by digest.
	ok, err := d.rt.HasImage(ctx, ref)
	if err != nil {
		return "", err
	}
	from := ref
	if d.rel.ImageDigest != "" && d.c.reg != nil {
		if from = d.c.registryRef(ref, d.rel.ImageDigest); !ok && from != ref {
			if ok, err = d.rt.HasImage(ctx, from); err != nil {
				return "", err
			}
		}
	}
	if ok {
		return d.rel.ImageID, nil
	}
	if d.rel.ImageID != "" {
		d.event(ctx, fmt.Sprintf("Image %s is no longer on the server; pulling it", ref))
	} else {
		d.event(ctx, "Pulling "+from)
	}
	id, err := d.pull(ctx, from)
	if err != nil {
		return "", step(apitypes.CodeBuildFailed, "image %s is not on the server and could not be pulled: %w", from, err)
	}
	return id, nil
}

// pull pulls ref onto the release's server, with a pull token when it is
// in the platform registry: only for a repository of the release's own
// project (see pullAllowed). Any other registry gets the org's credential
// for its host, if there is one (ADR 0051).
func (d *deployment) pull(ctx context.Context, ref string) (string, error) {
	if repo, ok := d.c.inRegistry(ref); ok {
		if !pullAllowed(d.ss, repo) {
			return "", fmt.Errorf("image %s belongs to another project; deploy it in that project, or push it to a registry of your own and deploy it with --image", ref)
		}
		defer d.c.useRegistry()()
		return d.rt.Pull(ctx, ref, d.log, node.PullOptions{Token: d.c.reg.Token("server "+d.server.Name, repo, false)})
	}
	login, host, err := d.c.registryLogin(ctx, d.ss.project.OrgID, ref)
	if err != nil {
		return "", err
	}
	if login.Username == "" {
		id, err := d.rt.Pull(ctx, ref, d.log)
		if err != nil && host != "" && pullRefused(err) {
			err = fmt.Errorf("%w; if the image is private, an admin adds credentials for its registry with `dock registry login %s`", err, host)
		}
		return id, err
	}
	_, _ = fmt.Fprintf(d.log, "Signing in to %s as %s\n", host, login.Username)
	id, err := d.rt.Pull(ctx, ref, d.log, login)
	if err != nil && pullRefused(err) {
		err = fmt.Errorf("%w; check the credentials for %s, or replace them with `dock registry login %s`", err, host, host)
	}
	return id, err
}

// build builds the uploaded source on a builder (builderFor), one build at
// a time per builder. With the registry the image is pushed, its digest
// recorded on the release, and a release server that didn't build it
// pulls it by digest.
func (d *deployment) build(ctx context.Context) (string, error) {
	c, ref := d.c, d.rel.ImageRef
	builder, brt, err := c.builderFor(ctx, d.server, apitypes.EnvironmentKind(d.ss.env.Kind))
	if err != nil {
		return "", step(apitypes.CodeBuildFailed, "%w", err)
	}
	f, err := os.Open(d.spec.upload)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// The build log is the org's: it says where without naming servers.
	where := "this server"
	if builder.ID != d.server.ID {
		where = "the build server"
	}
	sem := c.buildLock(builder.ID)
	select {
	case sem <- struct{}{}:
	default:
		d.event(ctx, "Waiting for another build on "+where+" to finish")
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	unlock := sync.OnceFunc(func() { <-sem })
	defer unlock()
	req := node.BuildRequest{
		Source: f, Dockerfile: d.ss.svc.DockerfilePath, BuildCommand: d.ss.svc.BuildCommand,
		StartCommand: d.cfg.Cmd, BuildArgs: d.cfg.Env, Tag: ref, Logs: d.log,
		// Build caches are per environment, so one can't poison another's.
		CacheScope: fmt.Sprintf("p%d_e%d", d.ss.project.ID, d.ss.env.ID),
	}
	if rc := d.spec.repo; rc != nil {
		d.event(ctx, "Using settings from dock.toml")
		// The file wins over the service's build settings (ADR 0007).
		if rc.Build.Dockerfile != "" {
			req.Dockerfile, req.BuildCommand = rc.Build.Dockerfile, ""
		}
		if rc.Build.Command != "" {
			req.Dockerfile, req.BuildCommand = "", rc.Build.Command
		}
	}
	var repo string
	if c.reg != nil {
		repo = repoOf(ref, c.reg.Host())
		req.Push = &node.PushTarget{Ref: c.reg.Host() + "/" + ref, Token: c.reg.Token("server "+builder.Name, repo, true)}
		defer c.useRegistry()()
	}
	if builder.ID != d.server.ID {
		d.event(ctx, fmt.Sprintf("Building %s on %s", ref, where))
	} else {
		d.event(ctx, "Building "+ref)
	}
	timeout := c.buildTimeout()
	bctx, cancel := context.WithTimeout(ctx, timeout)
	id, err := brt.Build(bctx, req)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(bctx.Err(), context.DeadlineExceeded) {
			return "", step(apitypes.CodeBuildFailed, "build took longer than %s and was stopped; raise [buildkit] timeout_minutes on the control plane if it needs longer", timeout)
		}
		return "", step(apitypes.CodeBuildFailed, "build failed: %w", c.nodeErr(builder.ID, err))
	}
	d.event(ctx, "Built "+ref)
	if req.Push == nil {
		return id, nil
	}
	unlock() // the builder is free; pushing is done
	digest, err := c.reg.Digest(ctx, repo, ref[strings.LastIndexByte(ref, ':')+1:])
	if err != nil {
		c.connectionFailed(ctx, "registry:push", "registry:push", "The platform registry is failing",
			fmt.Sprintf("A build pushed its image, but the registry couldn't find it: %v. Builds fail until the registry answers; see `dock registry`.", err), "dock registry")
		return "", step(apitypes.CodeBuildFailed, "find the pushed image in the registry: %w", err)
	}
	c.connectionWorks(ctx, "registry:push")
	d.rel.ImageDigest, d.rel.BuiltOnServerID = digest, &builder.ID
	if err := c.store.SetReleaseDigest(ctx, store.SetReleaseDigestParams{ImageDigest: digest, BuiltOnServerID: &builder.ID, ID: d.rel.ID}); err != nil {
		return "", err
	}
	if builder.ID == d.server.ID {
		return id, nil
	}
	from := c.registryRef(ref, digest)
	d.event(ctx, "Pulling "+from)
	if id, err = d.pull(ctx, from); err != nil {
		return "", step(apitypes.CodeBuildFailed, "pull %s: %w", from, err)
	}
	return id, nil
}

// buildTimeout is how long a build may run ([buildkit] timeout_minutes,
// 30 minutes if unset): a builder that hangs fails the deploy instead of
// holding its build lock forever.
func (c *Core) buildTimeout() time.Duration {
	if m := c.cfg.BuildKit.TimeoutMinutes; m > 0 {
		return time.Duration(m) * time.Minute
	}
	return 30 * time.Minute
}

// cutover switches traffic to the new release, records it as active, then
// retires the old container. Routes are rendered with the new release's port
// and applied before the DB changes, under the routes lock, so "active"
// always means "receiving traffic" and no other render can interleave.
func (d *deployment) cutover(ctx context.Context) error {
	c := d.c
	serverID := d.ss.env.ServerID
	canFallBack := d.old != nil && !d.oldRemoved

	// Its health so far, read before taking the lock, so the release
	// reads as active with it; adoptHealth takes what changes after.
	health := c.containerHealth(ctx, d.ss, d.rel.ContainerID)
	c.routesMu.Lock()
	routeErr := c.applyRoutesLocked(ctx, serverID, map[int64]routeOverride{d.ss.svc.ID: {port: d.rel.HostPort, release: d.rel.ID}})
	if routeErr != nil && canFallBack {
		c.routesMu.Unlock()
		// The old release never stopped serving and keeps doing so.
		return step(apitypes.CodeDeployFailed, "routes could not be updated: %w", routeErr)
	}
	// Running before the release reads as active, so whoever sees it
	// active (a deploy's caller polling, a backup, the data panel) sees
	// the service running too.
	prev := c.getStatus(d.ss.svc.ID)
	c.setStatus(d.ss, apitypes.ServiceStatus{State: apitypes.ServiceRunning, Health: health})
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if d.old != nil {
			if err := q.SupersedeRelease(ctx, d.old.ID); err != nil {
				return err
			}
		}
		if d.ss.svc.StoppedAt != nil {
			// Any release that goes live runs the service again (ADR 0050).
			if err := q.ClearServiceStopped(ctx, d.ss.svc.ID); err != nil {
				return err
			}
		}
		return q.ActivateRelease(ctx, store.ActivateReleaseParams{FinishedAt: ptr(now()), ID: d.rel.ID})
	})
	if err != nil {
		c.setStatus(d.ss, prev)
		// Point routes back at what the DB says is active.
		if rerr := c.applyRoutesLocked(ctx, serverID, nil); rerr != nil {
			c.log.Error("restore routes", "err", rerr)
		}
		c.routesMu.Unlock()
		return err
	}
	c.routesMu.Unlock()
	d.ss.svc.StoppedAt, d.ss.svc.StoppedBy = nil, ""

	if routeErr != nil {
		// Nothing to fall back to (first release, or stateful): keep it.
		d.event(ctx, "Warning: routes could not be updated: "+routeErr.Error())
	} else {
		d.event(ctx, fmt.Sprintf("Traffic switched to release %d", d.rel.Number))
	}
	c.adoptHealth(ctx, d.ss, d.rel.ContainerID)
	if d.old == nil {
		// Its first live release: verified domains get uptime checks
		// now that something answers them (ADR 0059), before the release
		// reads as live so whoever waits for it sees them.
		c.autoUptimeChecks(ctx, d.ss, "")
	}
	d.event(ctx, fmt.Sprintf("Release %d is live", d.rel.Number))
	d.publish(ctx)
	if d.old != nil {
		if old, err := c.store.GetRelease(ctx, d.old.ID); err == nil {
			c.publishRelease(ctx, d.ss, old)
		}
		if d.oldRemoved {
			c.freePort(ctx, d.old.ID) // stateful: removed before the new one started
		} else {
			// Let connections still on the old upstream finish. On shutdown,
			// leave it: it gets no traffic, keeps its port, and recovery
			// stops it on the next start.
			select {
			case <-time.After(c.drain):
			case <-ctx.Done():
				return nil
			}
			d.event(ctx, fmt.Sprintf("Stopping release %d", d.old.Number))
			if err := c.retire(ctx, d.ss, d.old.ID); err != nil {
				d.event(ctx, fmt.Sprintf("Could not stop release %d (it keeps its port until it is removed): %v", d.old.Number, err))
			}
		}
	}
	// Retired containers are gone, so their images can go too.
	c.removeOldImages(ctx, d.ss)
	c.maybePruneRegistry()
	return nil
}

// fail cleans up and marks the release failed. The old release keeps
// serving; for stateful services (already stopped) it is started again
// before the failure is recorded, so "failed" means "back to the previous
// state". A stopped service (ADR 0050) stays stopped.
//
// On shutdown it does nothing: the release stays in flight and recovery
// finishes it on the next start, so stopping the daemon never waits on
// containers (systemd gives it TimeoutStopSec=20).
func (d *deployment) fail(ctx context.Context, err error) {
	c := d.c
	if c.ctx.Err() != nil {
		c.log.Warn("deploy abandoned on shutdown; it fails on the next start", "service", d.ss.String(), "release", d.rel.Number)
		return
	}
	code, msg := apitypes.CodeInternal, err.Error()
	if se, ok := errors.AsType[*stepError](err); ok {
		code = se.code
	}
	serverID := d.ss.env.ServerID
	if isOffline(err) && CodeOf(err) != apitypes.CodeServerOffline {
		// The link dropped mid-deploy; the previous release keeps serving.
		if code == apitypes.CodeInternal {
			code = apitypes.CodeDeployFailed
		}
		msg = fmt.Sprintf("the server went offline during %s", d.rel.Status)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	// What explains the failure, read while its container still exists.
	captured := d.captureContainer(ctx)
	if !c.keepsLogs(serverID) {
		// Removing the container archives its output on servers that can.
		c.saveContainerLogs(ctx, d.ss, d.rel.ID)
	}
	// It never went live, so nothing it wrote is anyone's data.
	removeErr := c.removeReleaseContainers(ctx, d.ss, d.rel.ID, node.RemoveOptions{}, d.orphans...)
	if removeErr != nil {
		d.event(ctx, fmt.Sprintf("Could not remove the container of release %d: %v", d.rel.Number, c.nodeErr(serverID, removeErr)))
	}
	// A stopped service's old release had no container to bring back: it
	// stays stopped (ADR 0050).
	if d.oldRemoved && d.old != nil && d.ss.svc.StoppedAt == nil {
		later := isOffline(removeErr)
		switch {
		case removeErr == nil:
			later = isOffline(c.ensureRunning(ctx, d.ss, *d.old))
		case !later:
			// Two containers must never share a volume.
			d.event(ctx, fmt.Sprintf("Not starting release %d again while the new container may still be running; redeploy or roll back once it is gone", d.old.Number))
		}
		if later {
			// Any new container goes when the server reconnects; then this
			// one starts again, even after a daemon restart.
			c.ensureLater(ctx, d.ss.svc.ID)
			d.event(ctx, fmt.Sprintf("Release %d starts again when the server is back", d.old.Number))
		}
	}
	// Before the release reads as failed, so a failed release always
	// carries its cause (ADR 0071).
	d.recordDiagnosis(ctx, code, msg, isOffline(err), captured)
	// Before the release reads as failed, so "failed" means the changes it
	// carried are staged again.
	c.restage(ctx, d.ss, d.rel, msg)
	c.markFailed(d.rel, code, msg)
	if removeErr == nil {
		c.freePort(ctx, d.rel.ID)
	}
	d.event(ctx, fmt.Sprintf("Release %d failed: %s", d.rel.Number, msg))
	d.publish(ctx)
}

// markFailed's attempts: the first wait is failRetryDelay, doubling after
// each, about 3 s in all; each write gets failWriteTimeout.
const (
	failAttempts     = 6
	failRetryDelay   = 100 * time.Millisecond
	failWriteTimeout = 15 * time.Second
)

// markFailed records a failed release. A write that fails would leave the
// release in flight, and the one-deploy-per-service index would refuse
// every deploy of the service until a restart, so it is retried with a
// short backoff and a fresh context per attempt. The timer runs only after
// a failed write, never while idle. If the daemon stops meanwhile, or every
// attempt fails, recovery fails the release on the next start.
func (c *Core) markFailed(rel store.Release, code apitypes.ErrorCode, msg string) {
	p := store.FailReleaseParams{Error: msg, ErrorCode: string(code), FinishedAt: ptr(now()), ID: rel.ID}
	delay := failRetryDelay
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(c.ctx, failWriteTimeout)
		err := c.store.FailRelease(ctx, p)
		cancel()
		if err == nil || c.ctx.Err() != nil {
			return
		}
		c.log.Error("mark release failed", "release", rel.ID, "attempt", attempt, "err", err)
		if attempt == failAttempts {
			c.log.Error("release stays in flight until the daemon restarts; no deploy of its service starts before", "release", rel.ID)
			return
		}
		select {
		case <-time.After(delay):
		case <-c.ctx.Done():
			return
		}
		delay *= 2
	}
}

// orphanCheck fails a new release that already has containers: they can't
// be its own, so they survived a database restored from an older backup,
// which hands out the same release IDs and numbers again (a manual
// rollback of an update). One of them may be what still serves the
// service's domains, so they are never removed here: an operator removes
// them, and the next deploy goes ahead.
func (d *deployment) orphanCheck(ctx context.Context) error {
	ids, err := d.rt.ReleaseContainers(ctx, d.c.labels(d.ss, d.rel.ID))
	if err != nil {
		return step(apitypes.CodeDeployFailed, "list the containers of release %d: %w", d.rel.Number, d.c.nodeErr(d.ss.env.ServerID, err))
	}
	if len(ids) == 0 {
		return nil
	}
	d.orphans = ids
	short := make([]string, len(ids))
	for i, id := range ids {
		short[i] = id[:min(len(id), 12)]
	}
	return step(apitypes.CodeDeployFailed, "the server already runs containers labelled for release %d of %s that Dock doesn't manage, left from before the database was restored from an older backup. Nothing was changed; a platform operator has to remove them with `docker rm -f %s` on the server, then deploy again",
		d.rel.Number, d.ss, strings.Join(short, " "))
}

func (d *deployment) setStatus(ctx context.Context, s apitypes.ReleaseStatus) {
	if err := d.c.store.SetReleaseStatus(ctx, store.SetReleaseStatusParams{Status: string(s), ID: d.rel.ID}); err != nil {
		d.c.log.Error("set release status", "release", d.rel.ID, "err", err)
	}
	d.rel.Status = string(s)
	d.publish(ctx)
}

func (d *deployment) publish(ctx context.Context) {
	if r, err := d.c.store.GetRelease(ctx, d.rel.ID); err == nil {
		d.c.publishRelease(ctx, d.ss, r)
	}
}

func (d *deployment) event(ctx context.Context, msg string) { d.c.releaseEvent(ctx, d.ss, d.rel, msg) }

// restartRelease starts an existing release's container again (after a
// stateful deploy failed, or when recovery finds it missing) and reads
// the new container's health.
func (c *Core) restartRelease(ctx context.Context, ss svcScope, rel store.Release) error {
	cfg, err := c.openConfig(rel)
	if err != nil {
		return err
	}
	ctr, err := c.runtime(ss.env.ServerID).Start(ctx, c.containerSpec(ss, rel, cfg))
	if err != nil {
		return err
	}
	if err := c.store.SetReleaseContainer(ctx, store.SetReleaseContainerParams{ContainerID: ctr.ID, ID: rel.ID}); err != nil {
		return err
	}
	c.setStatus(ss, apitypes.ServiceStatus{State: apitypes.ServiceRunning})
	c.adoptHealth(ctx, ss, ctr.ID)
	return nil
}

// labels are what this control plane puts on a service's resources: its
// network and volumes (releaseID 0) and a release's containers.
func (c *Core) labels(ss svcScope, releaseID int64) node.Labels {
	l := node.Labels{
		ControlPlane: c.install,
		Project:      strconv.FormatInt(ss.project.ID, 10),
		Environment:  strconv.FormatInt(ss.env.ID, 10),
		Service:      strconv.FormatInt(ss.svc.ID, 10),
	}
	if releaseID != 0 {
		l.Release = strconv.FormatInt(releaseID, 10)
	}
	return l
}

func (c *Core) containerSpec(ss svcScope, rel store.Release, cfg releaseConfig) node.ContainerSpec {
	image := rel.ImageRef
	if rel.ImageID != "" {
		// The content-addressed ID pins exactly what was deployed, even if
		// the tag moved since.
		image = rel.ImageID
	}
	spec := node.ContainerSpec{
		Name:    containerName(ss, rel.Number),
		Image:   image,
		Labels:  c.labels(ss, rel.ID),
		Env:     cfg.Env,
		Cmd:     cfg.Cmd,
		Network: networkName(ss.envScope),
		Aliases: cfg.Aliases,
		Resources: node.Resources{
			MemoryBytes: int64(cfg.MemoryMB) << 20,
			NanoCPUs:    int64(cfg.CPUs * 1e9),
		},
	}
	if cfg.Published {
		spec.Port, spec.HostPort = cfg.Port, int(rel.HostPort)
	}
	for _, m := range cfg.Mounts {
		spec.Mounts = append(spec.Mounts, node.Mount{Volume: m.DockerName, Path: m.Path})
	}
	spec.Health = liveness(cfg)
	return spec
}

// livenessStartInterval is how often the ongoing check probes during its
// start period, so a healthy start is seen quickly (ADR 0043).
const livenessStartInterval = 2 * time.Second

// liveness is the ongoing health check of a release's container (ADR
// 0043), probing what the deploy's check does: a published service's port
// (HTTP GET of the health check path when it has one, else a TCP
// connect), or an unpublished template database's port over TCP. It is
// nil for a container with nothing to probe (a worker without a port) and
// when the check is off. Zeros, as in configs sealed before the check
// existed, mean the defaults.
func liveness(cfg releaseConfig) *node.LivenessCheck {
	if cfg.HealthCheckInterval == livenessOff {
		return nil
	}
	lc := node.LivenessCheck{
		Interval:    time.Duration(cmp.Or(cfg.HealthCheckInterval, defaultLivenessInterval)) * time.Second,
		Timeout:     time.Duration(cmp.Or(cfg.HealthCheckProbeTimeout, defaultProbeTimeout)) * time.Second,
		Retries:     cmp.Or(cfg.HealthCheckRetries, defaultLivenessRetries),
		StartPeriod: time.Duration(cmp.Or(cfg.HealthCheckStartPeriod, cfg.HealthCheckTimeout)) * time.Second,
	}
	lc.StartInterval = min(livenessStartInterval, lc.Interval)
	switch {
	case cfg.Published:
		lc.Port, lc.Path = cfg.Port, cfg.HealthCheckPath
	case cfg.HealthPort != 0:
		lc.Port = cfg.HealthPort
	default:
		return nil
	}
	return &lc
}

// keepData removes the container of a release that went live: its
// anonymous volumes may hold the service's data (a Postgres image without
// a volume), so they are kept as the service's one kept copy (ADR 0052).
// Only a release that never went live is removed without it.
var keepData = node.RemoveOptions{KeepAnonymousVolumes: true}

// removeContainer stops (gracefully) and removes a container, with opts
// (keepData for a release that went live). Missing containers are fine.
// Until its destroy event arrives, the container's events are ignored
// (see onContainerEvent): stopping an active release's container
// (stateful recreate, archive) is not a crash or drift. Once stopped, its
// output is archived on its server (archiveLogs, ADR 0046).
func (c *Core) removeContainer(ctx context.Context, serverID int64, id string, opts node.RemoveOptions) error {
	c.setRemoving(id, true)
	rt := c.runtime(serverID)
	if err := rt.Stop(ctx, id, stopTimeout); errors.Is(err, node.ErrNotFound) {
		c.setRemoving(id, false) // already gone: no destroy event to wait for
		return nil
	} else if err != nil {
		c.setRemoving(id, false)
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	c.archiveLogs(ctx, serverID, rt, id)
	if err := rt.Remove(ctx, id, opts); err != nil && !errors.Is(err, node.ErrNotFound) {
		c.setRemoving(id, false)
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// isRemoving reports whether the daemon is stopping and removing a
// container (removeContainer).
func (c *Core) isRemoving(id string) bool {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	return c.removing[id]
}

func (c *Core) setRemoving(id string, on bool) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if on {
		c.removing[id] = true
	} else {
		delete(c.removing, id)
	}
}

// ownRemoval reports whether ev belongs to a container the daemon is
// removing, forgetting the container once it is destroyed.
func (c *Core) ownRemoval(ev node.ContainerEvent) bool {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if !c.removing[ev.ContainerID] {
		return false
	}
	if ev.Action == "destroy" {
		delete(c.removing, ev.ContainerID)
	}
	return true
}

func (c *Core) removeContainers(ctx context.Context, serverID int64, ctrs []node.Container, opts node.RemoveOptions) error {
	var errs []error
	for _, ct := range ctrs {
		errs = append(errs, c.removeContainer(ctx, serverID, ct.ID, opts))
	}
	return errors.Join(errs...)
}

// ofRelease filters the containers of a release of ss: labelled for it by
// this control plane, or from before the control plane label with every
// other label matching (node.Labels.Matches).
func (c *Core) ofRelease(ctrs []node.Container, ss svcScope, releaseID int64) []node.Container {
	want := c.labels(ss, releaseID)
	var out []node.Container
	for _, ct := range ctrs {
		if ct.Labels.Matches(want) {
			out = append(out, ct)
		}
	}
	return out
}

// removeReleaseContainers stops and removes every container of a release
// of ss with opts (removeContainer), except the IDs in keep. They are
// found by label, not by the saved container ID, so a container started
// just before a crash (ID never saved) is found too; another control
// plane's containers never are.
func (c *Core) removeReleaseContainers(ctx context.Context, ss svcScope, releaseID int64, opts node.RemoveOptions, keep ...string) error {
	serverID := ss.env.ServerID
	ids, err := c.runtime(serverID).ReleaseContainers(ctx, c.labels(ss, releaseID))
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if slices.Contains(keep, id) {
			continue
		}
		errs = append(errs, c.removeContainer(ctx, serverID, id, opts))
	}
	return errors.Join(errs...)
}

// retire removes the containers of a release that went live (superseded
// by a newer one, or archived) and then frees its host port. If removal
// fails the port stays taken, so no new release is handed a port the old
// container still holds.
func (c *Core) retire(ctx context.Context, ss svcScope, releaseID int64) error {
	if err := c.removeReleaseContainers(ctx, ss, releaseID, keepData); err != nil {
		return err
	}
	c.freePort(ctx, releaseID)
	return nil
}

// freePort releases a retired release's host port (a no-op for releases
// that are still deploying or active).
func (c *Core) freePort(ctx context.Context, releaseID int64) {
	if err := c.store.ClearReleaseHostPort(ctx, releaseID); err != nil {
		c.log.Error("free host port", "release", releaseID, "err", err)
	}
}

func ptr[T any](v T) *T { return &v }

// buildLogPath is the build (and pull) log of a release.
func (c *Core) buildLogPath(releaseID int64) string {
	return filepath.Join(c.cfg.DataDir, "builds", strconv.FormatInt(releaseID, 10)+".log")
}

// newBuildLog opens a release's build log for writing; each line goes out
// as a live event too, with red's secrets masked for those who can't
// reveal them (publishBuildLog).
func (c *Core) newBuildLog(ss svcScope, rel store.Release, red *redactor) (*logWriter, error) {
	f, err := os.OpenFile(c.buildLogPath(rel.ID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &logWriter{f: f, emit: func(seq int, line string) {
		c.publishBuildLog(ss, red, apitypes.BuildLogLine{ReleaseNumber: rel.Number, Seq: seq, Text: line})
	}}, nil
}

// maxLogLine caps a build log line. Longer output without a newline is
// split into lines of this size, so a build printing megabytes on one line
// can't grow the writer's buffer or a log event without bound.
const maxLogLine = 64 << 10

// maxBuildLog caps a release's build (and pull) log. Output comes from the
// builder, which may be another server: past this, one marker line says
// the rest was dropped, and the build goes on.
const maxBuildLog = 16 << 20

// logTruncated is the last line of a log that hit its limit.
const logTruncated = "[build log truncated at %d MB; the rest of the output was dropped]"

// logWriter appends build output to the log file and publishes each
// complete line with its zero-based line number. The file and the events
// always agree on line boundaries.
type logWriter struct {
	mu   sync.Mutex
	f    *os.File
	buf  []byte // the current line so far, at most maxLogLine
	out  []byte // what one Write appends to the file
	seq  int
	emit func(seq int, line string)
	// limit caps the bytes written (maxBuildLog when 0); a write that
	// starts past it ends the log with the logTruncated line, and full
	// drops everything after.
	limit   int64
	written int64
	full    bool
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if w.full {
		return n, nil
	}
	if w.written >= cmp.Or(w.limit, maxBuildLog) {
		w.truncate()
		return n, nil
	}
	w.out = w.out[:0]
	for len(p) > 0 {
		room := maxLogLine - len(w.buf)
		i := bytes.IndexByte(p, '\n')
		switch {
		case i >= 0 && i <= room:
			w.out = append(w.out, p[:i+1]...)
			w.buf = append(w.buf, p[:i]...)
			p = p[i+1:]
		case len(p) <= room && i < 0:
			w.out = append(w.out, p...)
			w.buf = append(w.buf, p...)
			p = nil
			continue
		default: // the line is too long: break it here
			w.out = append(append(w.out, p[:room]...), '\n')
			w.buf = append(w.buf, p[:room]...)
			p = p[room:]
		}
		w.emit(w.seq, string(w.buf))
		w.seq++
		w.buf = w.buf[:0]
	}
	_, err := w.f.Write(w.out)
	w.written += int64(len(w.out))
	if cap(w.out) > 4*maxLogLine {
		w.out = nil // don't keep a huge write's buffer
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

// truncate ends a log that reached its limit: the partial line, then the
// logTruncated line.
func (w *logWriter) truncate() {
	w.full = true
	if len(w.buf) > 0 {
		_, _ = w.f.Write([]byte("\n"))
		w.emit(w.seq, string(w.buf))
		w.seq++
		w.buf = nil
	}
	line := fmt.Sprintf(logTruncated, cmp.Or(w.limit, maxBuildLog)>>20)
	_, _ = w.f.Write([]byte(line + "\n"))
	w.emit(w.seq, line)
	w.seq++
}

// Close terminates a trailing partial line and closes the file.
func (w *logWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		_, _ = w.f.Write([]byte("\n"))
		w.emit(w.seq, string(w.buf))
		w.buf = nil
	}
	_ = w.f.Close()
}
