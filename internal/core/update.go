package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Updating Dock from the console (ADR 0028). The control plane checks its
// release source when asked (cached for checkTTL), stages a release for
// every server it updates, and hands its own update to the root updater
// (internal/updater) by writing a request; the updater restarts it and
// reports back through UpdateFinished. Nodes are updated over their link
// afterwards, one at a time. Nothing here runs while nobody updates.

const (
	// checkTTL is how long a release check is reused.
	checkTTL = 15 * time.Minute
	// stageTimeout bounds downloading a release.
	stageTimeout = 15 * time.Minute
	// pickupTimeout is how long the updater has to take a request before
	// the control plane withdraws it: dock-update.path isn't running.
	pickupTimeout = 30 * time.Second
	// nodeOnlineWait is how long a node that is offline when its turn
	// comes may take to connect (nodes reconnect within 30 s of a control
	// plane restart) before it is skipped.
	nodeOnlineWait = time.Minute
	// nodeUpdateTimeout bounds updating one node: sending the release,
	// its updater's health check (90 s) and, on failure, its rollback.
	nodeUpdateTimeout = 6 * time.Minute
)

// Node update states in apitypes.UpdateServer.State.
const (
	nodeUpdating = "updating"
	nodeUpdated  = "updated"
	nodeFailed   = "failed"
	nodeSkipped  = "skipped"
)

// updates is the control plane's update state, in memory.
type updates struct {
	src update.Source
	// pathUnit is update.PathUnit; tests point it elsewhere.
	pathUnit string

	// checkMu makes one check at a time; the others wait for it.
	checkMu sync.Mutex

	mu        sync.Mutex
	checkedAt time.Time
	releases  []update.Release
	checkErr  error
	// running is the update this daemon is driving; nil when none.
	running *apitypes.UpdateRun
	// last is the control plane's last update result.
	last *update.Result
	// nodes are the node update states since the daemon started, by
	// server ID: bounded by the servers.
	nodes map[int64]nodeUpdate
}

type nodeUpdate struct{ state, err string }

func newUpdates(o Options) updates {
	src := o.Updates
	if src == nil {
		src = update.NewSource(o.Config.Updates.Source, o.Config.Updates.TokenFile)
	}
	return updates{src: src, pathUnit: cmp.Or(o.UpdaterUnit, update.PathUnit), nodes: map[int64]nodeUpdate{}}
}

// UpdateStatus describes updating Dock: this version, the newer releases
// with their notes, each server's version and the update running or last
// finished. The release check is reused for 15 minutes unless refresh.
// Operators only.
func (c *Core) UpdateStatus(ctx context.Context, a Actor, refresh bool) (apitypes.UpdateStatus, error) {
	if err := a.requirePlatform(PlatformView, "checking for updates"); err != nil {
		return apitypes.UpdateStatus{}, err
	}
	c.checkReleases(ctx, refresh)
	return c.updateStatus(ctx, true)
}

// checkReleases refreshes the release list when it is older than
// checkTTL, or now with refresh. Concurrent callers share one check.
func (c *Core) checkReleases(ctx context.Context, refresh bool) {
	u := &c.updates
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	u.mu.Lock()
	fresh := !u.checkedAt.IsZero() && time.Since(u.checkedAt) < checkTTL
	u.mu.Unlock()
	if fresh && !refresh {
		return
	}
	rs, err := u.src.Releases(ctx)
	if err != nil && ctx.Err() != nil {
		return // the caller went away; the next one checks
	}
	if err != nil {
		c.log.Warn("check for releases", "source", u.src.String(), "err", err)
	}
	u.mu.Lock()
	u.checkedAt, u.checkErr = time.Now(), err
	if err == nil {
		u.releases = rs
	}
	u.mu.Unlock()
}

// checkWords splits a failed release check into one sentence for people
// ("Couldn't check for releases: ..., what to do.") and its technical
// detail (update.SourceError); other errors are their own detail.
func checkWords(err error) (plain, detail string) {
	why, detail := "something went wrong; see the detail and try again", err.Error()
	if se, ok := errors.AsType[*update.SourceError](err); ok {
		why, detail = se.Plain, se.Detail
	}
	return "Couldn't check for releases: " + why + ".", detail
}

// updateStatus assembles the status from memory and one server query.
// withReleases adds the releases' notes (left out of live events).
func (c *Core) updateStatus(ctx context.Context, withReleases bool) (apitypes.UpdateStatus, error) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return apitypes.UpdateStatus{}, err
	}
	u := &c.updates
	out := apitypes.UpdateStatus{
		Version: version.Version, Source: u.src.String(), Releases: []apitypes.UpdateRelease{},
		Updater: fileExists(u.pathUnit), Servers: make([]apitypes.UpdateServer, 0, len(servers)),
	}
	u.mu.Lock()
	if !u.checkedAt.IsZero() {
		at := u.checkedAt.UTC().Truncate(time.Second)
		out.CheckedAt = &at
	}
	if u.checkErr != nil {
		out.CheckError, out.CheckDetail = checkWords(u.checkErr)
	}
	pre := update.IsRelease(version.Version) && strings.Contains(version.Version, "-")
	for _, r := range u.releases {
		if r.Prerelease && !pre {
			continue
		}
		if out.Latest == "" {
			out.Latest = r.Version
		}
		if withReleases && newerThanRunning(r.Version) {
			out.Releases = append(out.Releases, apitypes.UpdateRelease{
				Version: r.Version, Name: r.Name, Notes: r.Notes, URL: r.URL, PublishedAt: r.PublishedAt, Prerelease: r.Prerelease,
			})
		}
	}
	out.Available = out.Latest != "" && newerThanRunning(out.Latest)
	if u.running != nil {
		run := *u.running
		out.Running = &run
	}
	if u.last != nil {
		out.Last = toUpdateResult(*u.last)
	}
	for _, s := range servers {
		out.Servers = append(out.Servers, c.updateServer(s, u.nodes[s.ID]))
	}
	u.mu.Unlock()
	if out.Running == nil {
		// The updater may be applying an update this daemon didn't start
		// (it was restarted), or rolling back to this one.
		var req update.Request
		if update.ReadJSON(update.Dir(c.cfg.DataDir), update.ApplyingFile, &req) == nil {
			out.Running = &apitypes.UpdateRun{Version: req.Version, Stage: apitypes.UpdateApplying, RequestedBy: req.RequestedBy, StartedAt: req.RequestedAt}
		}
	}
	return out, nil
}

// updateServer is one server's line in the status. Call with u.mu held.
func (c *Core) updateServer(s store.Server, nu nodeUpdate) apitypes.UpdateServer {
	out := apitypes.UpdateServer{Name: s.Name, Local: s.Local, Status: c.serverStatus(s), Version: s.Version, State: nu.state, Error: nu.err}
	if s.Local {
		out.Version = version.Version
		return out
	}
	if c.nodes != nil {
		if h := c.nodes.State(s.ID).Hello; h.Version != "" {
			out.Version = h.Version
			if out.Status == apitypes.ServerOnline && (h.Protocol < remote.UpdateProtocol || h.Arch == "") {
				out.Manual = manualUpdate(version.Version)
			}
		}
	}
	out.Behind = out.Version != "" && out.Version != version.Version && s.RevokedAt == nil
	if !out.Behind {
		out.Manual = ""
	}
	return out
}

// manualUpdate is what to run on a node that can't update over its link.
func manualUpdate(v string) string {
	return fmt.Sprintf("on it, with the %s release's dock-install tarball unpacked: sudo --preserve-env=GITHUB_TOKEN ./deploy/install.sh", v)
}

func newerThanRunning(v string) bool {
	return !update.IsRelease(version.Version) || update.Newer(v, version.Version)
}

func toUpdateResult(r update.Result) *apitypes.UpdateResult {
	return &apitypes.UpdateResult{
		From: r.From, To: r.Version, Status: string(r.Status), Error: r.Error, RequestedBy: r.RequestedBy,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Backup: r.Backup, Log: r.Log,
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// StartUpdate updates Dock to version (the newest release when empty):
// the control plane first, through the root updater, then every node
// behind it. It answers once the update is under way; the update event
// follows it, and platform.updated or platform.update_failed ends it.
// Operators only.
func (c *Core) StartUpdate(ctx context.Context, a Actor, req apitypes.StartUpdateRequest) (apitypes.UpdateStatus, error) {
	if err := a.requirePlatform(UpdatesApply, "updating Dock"); err != nil {
		return apitypes.UpdateStatus{}, err
	}
	if !update.IsRelease(version.Version) {
		return apitypes.UpdateStatus{}, invalid("this control plane runs a development build (%s), not a release; update it by hand with install.sh", version.Version)
	}
	target := req.Version
	if target == "" {
		c.checkReleases(ctx, false)
		st, err := c.updateStatus(ctx, false)
		if err != nil {
			return st, err
		}
		if st.Latest == "" && st.CheckError != "" {
			return st, invalid("no release to update to. %s", st.CheckError)
		}
		if st.Latest == "" {
			return st, invalid("no release to update to: the source lists none")
		}
		target = st.Latest
	}
	if !update.IsRelease(target) {
		return apitypes.UpdateStatus{}, invalid("%q is not a release version like v1.2.3", target)
	}
	if update.Newer(version.Version, target) {
		return apitypes.UpdateStatus{}, invalid("this control plane runs %s, newer than %s; updates only go forward (migrations can't be undone)", version.Version, target)
	}
	self := target != version.Version
	if !self {
		st, err := c.updateStatus(ctx, false)
		if err != nil || !slices.ContainsFunc(st.Servers, func(s apitypes.UpdateServer) bool { return s.Behind }) {
			return st, err // up to date: nothing to start
		}
	}
	if self && !fileExists(c.updates.pathUnit) {
		return apitypes.UpdateStatus{}, invalid("this control plane has no updater (dock-update.path): update it by hand once with install.sh from the new release, and the next update can run from here")
	}
	dir := update.Dir(c.cfg.DataDir)
	u := &c.updates
	u.mu.Lock()
	if u.running != nil || update.Exists(dir, update.RequestFile) || update.Exists(dir, update.ApplyingFile) {
		u.mu.Unlock()
		return apitypes.UpdateStatus{}, conflict("an update is already running; wait for it to finish")
	}
	stage := apitypes.UpdateDownloading
	if !self {
		stage = apitypes.UpdateServers
	}
	u.running = &apitypes.UpdateRun{Version: target, Stage: stage, RequestedBy: a.Name, StartedAt: now()}
	clear(u.nodes)
	u.mu.Unlock()
	if self {
		c.audit(ctx, a.platform(), "platform.update_started", nil, nil, "dock", fmt.Sprintf("started updating dock from %s to %s", version.Version, target))
	} else {
		c.audit(ctx, a.platform(), "platform.update_started", nil, nil, "dock", fmt.Sprintf("started updating the servers behind to %s", target))
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		r := update.Request{Version: target, RequestedBy: a.Name, RequestedAt: now(), Servers: true}
		if self {
			c.updateControlPlane(r)
		} else {
			c.updateNodes(r)
		}
	}()
	st, err := c.updateStatus(ctx, false)
	c.publishUpdate(st)
	return st, err
}

// updateControlPlane stages the release for this server and every node,
// then asks the updater to apply it here. The updater stops this daemon;
// the new one finishes the job (UpdateFinished).
func (c *Core) updateControlPlane(r update.Request) {
	ctx, cancel := context.WithTimeout(c.ctx, stageTimeout)
	defer cancel()
	if _, err := c.stage(ctx, r.Version); err != nil {
		c.endUpdate(fmt.Errorf("download %s: %w", r.Version, err), r)
		return
	}
	dir := update.Dir(c.cfg.DataDir)
	if err := update.WriteJSON(dir, update.RequestFile, r, nil); err != nil {
		c.endUpdate(fmt.Errorf("ask the updater: %w", err), r)
		return
	}
	c.setStage(apitypes.UpdateApplying)
	c.log.Info("update staged; the updater takes over", "version", r.Version)
	// The updater renames the request as it starts, then stops this
	// daemon. A request still there after pickupTimeout was never seen.
	select {
	case <-c.ctx.Done():
		return
	case <-time.After(pickupTimeout):
	}
	if !update.Exists(dir, update.RequestFile) {
		return
	}
	_ = update.RemoveIfExists(dir, update.RequestFile)
	c.endUpdate(errors.New("the updater didn't start; check it is installed and enabled: sudo systemctl enable --now dock-update.path"), r)
}

// stage downloads release v for this server and for every node it
// updates, so a missing or mismatched file stops the update before
// anything changes.
func (c *Core) stage(ctx context.Context, v string) (update.Sums, error) {
	archs := []string{runtime.GOARCH}
	if c.nodes != nil {
		servers, err := c.store.ListServers(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range servers {
			if h := c.nodes.State(s.ID).Hello; !s.Local && h.Arch != "" && !slices.Contains(archs, h.Arch) {
				archs = append(archs, h.Arch)
			}
		}
	}
	return update.Stage(ctx, c.updates.src, c.cfg.DataDir, v, archs...)
}

// UpdateFinished takes the updater's result: it audits it, keeps it for
// the status, and goes on with the nodes when the control plane updated.
// The updater calls it (POST /internal/update) and Start runs it, for a
// result written while this daemon wasn't listening. Running it twice
// does nothing the second time.
func (c *Core) UpdateFinished(ctx context.Context) {
	dir := update.Dir(c.cfg.DataDir)
	u := &c.updates
	u.mu.Lock()
	var res update.Result
	err := update.ReadJSON(dir, update.ResultFile, &res)
	if err == nil {
		err = os.Rename(filepath.Join(dir, update.ResultFile), filepath.Join(dir, update.LastResultFile))
	}
	if err != nil {
		u.mu.Unlock()
		if !errors.Is(err, fs.ErrNotExist) {
			c.log.Error("read the update result", "err", err)
		}
		return
	}
	u.last, u.running = &res, nil
	u.mu.Unlock()
	took := res.FinishedAt.Sub(res.StartedAt).Round(time.Second)
	switch res.Status {
	case update.Succeeded:
		c.audit(ctx, System, "platform.updated", nil, nil, "dock",
			fmt.Sprintf("updated dock from %s to %s in %s (asked by %s); backup %s", res.From, res.Version, took, res.RequestedBy, res.Backup))
		c.signalAlert(ctx, alertSignal{Key: updateAlertKey, Resolve: true})
	default:
		how := strings.ReplaceAll(string(res.Status), "_", " ")
		c.audit(ctx, System, "platform.update_failed", nil, nil, "dock",
			fmt.Sprintf("updating dock from %s to %s %s after %s: %s", res.From, res.Version, how, took, res.Error))
		c.signalAlert(ctx, updateFailed(fmt.Sprintf("Updating the control plane from %s to %s %s: %s.", res.From, res.Version, how, strings.TrimRight(res.Error, ". "))))
	}
	if res.Status == update.Succeeded && res.Servers && res.Version == version.Version {
		u.mu.Lock()
		u.running = &apitypes.UpdateRun{Version: res.Version, Stage: apitypes.UpdateServers, RequestedBy: res.RequestedBy, StartedAt: res.StartedAt}
		u.mu.Unlock()
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.updateNodes(res.Request)
		}()
	}
	if st, err := c.updateStatus(ctx, false); err == nil {
		c.publishUpdate(st)
	}
}

// loadLastUpdate reads the last audited result, for the status after a
// restart.
func (c *Core) loadLastUpdate() {
	var res update.Result
	if update.ReadJSON(update.Dir(c.cfg.DataDir), update.LastResultFile, &res) == nil {
		c.updates.mu.Lock()
		c.updates.last = &res
		c.updates.mu.Unlock()
	}
}

// updateNodes brings every node behind to r.Version, which the control
// plane runs, one at a time over their links. Offline nodes get a minute
// to connect, then are skipped; the next update takes them.
func (c *Core) updateNodes(r update.Request) {
	defer c.endUpdate(nil, r)
	if c.nodes == nil {
		return
	}
	ctx := c.ctx
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Error("update nodes", "err", err)
		return
	}
	var sums update.Sums
	for _, s := range servers {
		if s.Local || s.RevokedAt != nil {
			continue
		}
		state := c.nodes.State(s.ID)
		hello := state.Hello
		if hello.Version == r.Version {
			continue
		}
		if state.Status == remote.StatusIncompatible {
			c.setNode(s.ID, nodeSkipped, "runs "+hello.Version+", too old for this control plane: "+manualUpdate(r.Version))
			continue
		}
		c.setNode(s.ID, nodeUpdating, "")
		wait, cancel := context.WithTimeout(ctx, nodeOnlineWait)
		err := c.nodes.WaitOnline(wait, s.ID)
		cancel()
		if err != nil {
			c.setNode(s.ID, nodeSkipped, "offline; the next update takes it")
			continue
		}
		if hello = c.nodes.State(s.ID).Hello; hello.Version == r.Version {
			c.setNode(s.ID, nodeUpdated, "")
			continue
		}
		if hello.Protocol < remote.UpdateProtocol || hello.Arch == "" {
			c.setNode(s.ID, nodeSkipped, "runs "+hello.Version+", which can't update over its link: "+manualUpdate(r.Version))
			continue
		}
		if sums == nil {
			stageCtx, cancel := context.WithTimeout(ctx, stageTimeout)
			sums, err = c.stage(stageCtx, r.Version)
			cancel()
			if err != nil {
				c.setNode(s.ID, nodeFailed, "download "+r.Version+": "+err.Error())
				c.audit(ctx, System, "server.update_failed", nil, nil, "server "+s.Name, fmt.Sprintf("updating server %s to %s failed: %v", s.Name, r.Version, err))
				c.nodeUpdateAlert(ctx, s, r.Version, "download: "+err.Error())
				return
			}
		}
		uctx, cancel := context.WithTimeout(ctx, nodeUpdateTimeout)
		_, err = c.nodes.UpdateNode(uctx, s.ID, remote.NodeUpdate{
			Version: r.Version, RequestedBy: r.RequestedBy, Dir: update.ReleaseDir(c.cfg.DataDir, r.Version), Sums: sums,
		})
		cancel()
		if err != nil {
			c.setNode(s.ID, nodeFailed, err.Error())
			c.audit(ctx, System, "server.update_failed", nil, nil, "server "+s.Name, fmt.Sprintf("updating server %s from %s to %s failed: %v", s.Name, hello.Version, r.Version, err))
			c.nodeUpdateAlert(ctx, s, r.Version, err.Error())
			continue
		}
		c.setNode(s.ID, nodeUpdated, "")
		c.nodeUpdateAlert(ctx, s, r.Version, "")
		c.audit(ctx, System, "server.updated", nil, nil, "server "+s.Name, fmt.Sprintf("updated server %s from %s to %s", s.Name, hello.Version, r.Version))
	}
}

// endUpdate ends the update this daemon drives. A non-nil err means it
// stopped before the control plane changed: it is audited and kept as
// the last result.
func (c *Core) endUpdate(err error, r update.Request) {
	u := &c.updates
	u.mu.Lock()
	u.running = nil
	if err != nil {
		u.last = &update.Result{Request: r, From: version.Version, Status: update.Failed, Error: err.Error(), StartedAt: r.RequestedAt, FinishedAt: now()}
	}
	u.mu.Unlock()
	if err != nil {
		c.log.Error("update failed", "version", r.Version, "err", err)
		c.audit(c.ctx, System, "platform.update_failed", nil, nil, "dock", fmt.Sprintf("updating dock from %s to %s failed: %v", version.Version, r.Version, err))
		c.signalAlert(c.ctx, updateFailed(fmt.Sprintf("Updating the control plane from %s to %s failed before it changed anything: %v.", version.Version, r.Version, err)))
	}
	if st, err := c.updateStatus(c.ctx, false); err == nil {
		c.publishUpdate(st)
	}
}

// updateAlertKey names the control plane's update_failed alert; a
// server's is "update:<server ID>".
const updateAlertKey = "update:dock"

// updateFailed is the platform alert for a failed update of the control
// plane. The next update that succeeds resolves it.
func updateFailed(msg string) alertSignal {
	return alertSignal{
		Key: updateAlertKey, Kind: apitypes.AlertUpdateFailed, Severity: apitypes.SeverityWarning,
		Title: "Dock update failed", Message: msg + " Dock keeps running the version it had.", Command: "dock update",
	}
}

// nodeUpdateAlert raises or resolves a server's update_failed alert as
// its update ends: msg is why it failed, "" when it updated.
func (c *Core) nodeUpdateAlert(ctx context.Context, s store.Server, version, msg string) {
	key := "update:" + strconv.FormatInt(s.ID, 10)
	if msg == "" {
		c.signalAlert(ctx, alertSignal{Key: key, Resolve: true})
		return
	}
	c.signalAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertUpdateFailed, Severity: apitypes.SeverityWarning, Server: s.Name,
		Title:   "Dock update failed on " + s.Name,
		Message: fmt.Sprintf("Updating server %s to %s failed: %s. It keeps running its version; the next update tries again.", s.Name, version, strings.TrimRight(msg, ". ")),
		Command: "dock update",
	})
}

func (c *Core) setStage(s apitypes.UpdateStage) {
	u := &c.updates
	u.mu.Lock()
	if u.running != nil {
		u.running.Stage = s
	}
	u.mu.Unlock()
	if st, err := c.updateStatus(c.ctx, false); err == nil {
		c.publishUpdate(st)
	}
}

func (c *Core) setNode(id int64, state, msg string) {
	u := &c.updates
	u.mu.Lock()
	u.nodes[id] = nodeUpdate{state: state, err: msg}
	u.mu.Unlock()
	if st, err := c.updateStatus(c.ctx, false); err == nil {
		c.publishUpdate(st)
	}
}

// publishUpdate sends the status to operators.
func (c *Core) publishUpdate(st apitypes.UpdateStatus) {
	st.Releases = nil
	c.bus.publish(busEvent{serverStats: true, ev: apitypes.Event{Type: apitypes.EventUpdate, Update: &st}})
}
