package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// The analytics store's container (ADR 0037). It runs the release's
// dock-analytics binary, which the control plane downloads for the
// analytics server's arch when analytics runs, mounted into a small base
// image: nothing to publish or pull from a private registry, and the
// store always matches this dock.
//
// Moving the store copies its database: the forwarders hold off, the old
// store streams its file through the control plane into a new, empty
// store on the new server, which restarts on it; once its events match,
// the new server becomes the analytics server and the old one's store,
// data and binary are removed. Events wait in every server's spool
// meanwhile, so none are lost.

// analyticsMove is a move of the store that runs.
type analyticsMove struct {
	from, to store.Server
}

// Bounds of the store's background work.
const (
	// storeStartTimeout covers downloading the binary (~70 MB), sending
	// it to a remote server and starting the container.
	storeStartTimeout = 15 * time.Minute
	// storeMoveTimeout covers a move: a start and the copy, gigabytes
	// for a store with years of events.
	storeMoveTimeout = 2 * time.Hour
	// storeReadyTimeout is how long a started or restored store may take
	// to answer.
	storeReadyTimeout = 2 * time.Minute
	// storeRetryAfter is how long after a failed start reading the status
	// tries again: the page refetches when a start ends, which mustn't
	// start the next one at once.
	storeRetryAfter = 30 * time.Second
)

// ensureAnalyticsStore starts (or updates) the store on serverID, in the
// background, unless a start or a move is running.
func (c *Core) ensureAnalyticsStore(serverID int64) {
	cl, _ := c.analyticsClient()
	if cl == nil || !c.online(serverID) {
		return
	}
	c.analytics.mu.Lock()
	// The client again, under the lock turning analytics off takes it
	// away under (SetAnalytics): a start never begins after that.
	if c.analytics.starting || c.analytics.moving != nil || c.analytics.client != cl {
		c.analytics.mu.Unlock()
		return
	}
	c.analytics.starting = true
	c.analytics.startedAt = now()
	c.analytics.mu.Unlock()
	c.goCounted("starting the analytics store", func() {
		ctx, cancel := context.WithTimeout(c.ctx, storeStartTimeout)
		defer cancel()
		// Through caught: a start that panics still clears starting, or
		// no later one would run.
		err := caughtErr(c.log, "starting the analytics store", func() error { return c.startStore(ctx, serverID, cl.Token) })
		c.analytics.mu.Lock()
		c.analytics.starting = false
		c.analytics.mu.Unlock()
		if c.ctx.Err() != nil {
			return
		}
		c.setStoreErr(err)
		c.publishAnalytics()
		if err != nil {
			c.log.Error("analytics: start the store", "server", serverID, "err", err)
			return
		}
		if err := c.restoreStoreBackup(ctx, cl); err != nil {
			c.setStoreErr(err)
			c.log.Error("analytics: fill the store from the control plane's backup", "err", err)
		}
		c.wakeForwarders()
		c.updateGeoIP()
		if serverID != c.localID {
			c.removeStrayStore(c.localID, true)
		}
	})
}

// publishAnalytics tells operators' pages the store's state changed.
func (c *Core) publishAnalytics() {
	c.bus.publish(busEvent{serverStats: true, ev: apitypes.Event{Type: apitypes.EventAnalytics}})
}

// setStoreErr records why the store isn't running, "" when it is.
func (c *Core) setStoreErr(err error) {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	c.analytics.storeErr = ""
	if err != nil {
		c.analytics.storeErr = err.Error()
	}
}

// startStore runs the store on serverID from the release's binary for its
// arch, and drops the binaries of other versions once it does.
func (c *Core) startStore(ctx context.Context, serverID int64, token string) error {
	spec, err := c.storeSpec(ctx, serverID, token)
	if err != nil {
		return err
	}
	if err := c.runtime(serverID).EnsureAnalyticsStore(ctx, spec); err != nil {
		return err
	}
	if c.cfg.Analytics.StoreBinary == "" {
		_ = update.PruneAnalytics(c.cfg.DataDir, version.Version)
	}
	return nil
}

// storeSpec is how the store runs on serverID: the binary for its arch
// (downloaded now, the first time), the token and the memory limit.
func (c *Core) storeSpec(ctx context.Context, serverID int64, token string) (node.AnalyticsStoreSpec, error) {
	arch := runtime.GOARCH
	if serverID != c.localID {
		h := c.nodes.State(serverID).Hello
		if h.Protocol < remote.AnalyticsBinaryProtocol || h.Arch == "" {
			return node.AnalyticsStoreSpec{}, fmt.Errorf("the server runs dock %s, which can't run the analytics store; update it with `dock update`", h.Version)
		}
		arch = h.Arch
	}
	bin, sum, err := c.storeBinary(ctx, arch)
	if err != nil {
		return node.AnalyticsStoreSpec{}, err
	}
	return node.AnalyticsStoreSpec{
		Binary: bin, SHA256: sum, Token: token,
		MemoryBytes: max(int64(c.cfg.Analytics.StoreMemoryMB)<<20, analyticsMinMemory),
	}, nil
}

// storeBinary is the store's binary for arch and its SHA-256: [analytics]
// store_binary for a development build, otherwise this version's release
// asset, downloaded once.
func (c *Core) storeBinary(ctx context.Context, arch string) (string, string, error) {
	c.analytics.binMu.Lock()
	defer c.analytics.binMu.Unlock()
	if p := c.cfg.Analytics.StoreBinary; p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", "", err
		}
		sum, err := update.HashFile(abs)
		if err != nil {
			return "", "", fmt.Errorf("[analytics] store_binary: %w", err)
		}
		return abs, sum, nil
	}
	bin, sum, err := update.StageAnalytics(ctx, c.updates.src, c.cfg.DataDir, version.Version, arch)
	if err != nil {
		return "", "", fmt.Errorf("download the analytics store for %s: %w", arch, err)
	}
	return bin, sum, nil
}

// removeStrayStore removes the store from serverID, which isn't the
// analytics server, in the background: what a move that stopped part way
// left, or the old server of a move that started empty. Only once the
// analytics server's store answers (waiting for it after a start, else
// asking once), so the store that holds the data is never the one in
// doubt. A server without a store has nothing to remove.
func (c *Core) removeStrayStore(serverID int64, wait bool) {
	cl, storeServer := c.analyticsClient()
	if cl == nil || serverID == storeServer {
		return
	}
	if serverID != c.localID && c.nodes.State(serverID).Hello.Protocol < remote.AnalyticsBinaryProtocol {
		return // too old to have run one from a binary, or to remove it
	}
	c.goCounted("removing an analytics store left behind", func() {
		ctx, cancel := context.WithTimeout(c.ctx, storeStartTimeout)
		defer cancel()
		if wait {
			if _, err := c.readyStore(ctx, cl, 0); err != nil {
				return
			}
		} else if _, err := cl.Health(ctx); err != nil {
			return
		}
		// The store may have moved onto serverID meanwhile (the wait can be
		// long): not while a move runs, nor once one has. The read lock
		// holds a move that starts now off the stores until this is done.
		if !c.analytics.writing.TryRLock() {
			return
		}
		defer c.analytics.writing.RUnlock()
		c.analytics.mu.Lock()
		busy := c.analytics.moving != nil || c.analytics.serverID == serverID
		c.analytics.mu.Unlock()
		if busy {
			return
		}
		if err := c.runtime(serverID).RemoveAnalyticsStore(ctx, node.StoreAnalytics); err != nil && c.ctx.Err() == nil {
			c.log.Warn("analytics: remove the store from a server that isn't the analytics server", "server", serverID, "err", err)
		}
	})
}

// readyStore waits up to storeReadyTimeout for the store to answer, with
// at least minEvents events when minEvents > 0 (a restored store
// restarting on its copy), and returns its health.
func (c *Core) readyStore(ctx context.Context, cl *analytics.Client, minEvents int64) (analytics.Health, error) {
	ctx, cancel := context.WithTimeout(ctx, storeReadyTimeout)
	defer cancel()
	wait := 250 * time.Millisecond
	for {
		h, err := cl.Health(ctx)
		if err == nil && h.Events >= minEvents {
			return h, nil
		}
		select {
		case <-ctx.Done():
			if err == nil {
				err = fmt.Errorf("it has %d events, not %d", h.Events, minEvents)
			}
			return h, fmt.Errorf("the store didn't answer in time: %w", err)
		case <-time.After(wait):
			wait = min(2*wait, 5*time.Second)
		}
	}
}

// AnalyticsStatus is Dock Analytics' state, for operators. A store that
// should run but doesn't answer is started again, in the background: the
// page shows it starting, and why it failed if it does.
func (c *Core) AnalyticsStatus(ctx context.Context, a Actor) (apitypes.AnalyticsStatus, error) {
	if err := a.requirePlatform(PlatformView, "reading analytics status"); err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	out := apitypes.AnalyticsStatus{}
	// The move first: one that ends while the settings are read has
	// switched the server by then, so the status never shows the old
	// server without the move.
	c.analytics.mu.Lock()
	if m := c.analytics.moving; m != nil {
		out.MovingTo = m.to.Name
	}
	c.analytics.mu.Unlock()
	st, err := c.store.GetAnalyticsSettings(ctx)
	if store.IsNotFound(err) {
		out.Server = c.localName()
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = st.Enabled
	serverID := c.localID
	if st.ServerID != nil {
		serverID = *st.ServerID
	}
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return out, err
	}
	out.Server = srv.Name
	out.ServerOnline = c.online(serverID)
	cl, _ := c.analyticsClient()
	if cl != nil && out.ServerOnline {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if h, err := cl.Health(hctx); err == nil {
			out.StoreRunning, out.StoreVersion, out.Events, out.StoreBytes, out.GeoIP = true, h.Version, h.Events, h.FileBytes, h.GeoIP
		} else if c.retryStart() {
			c.ensureAnalyticsStore(serverID)
		}
	}
	c.analytics.mu.Lock()
	out.Starting, out.Error = c.analytics.starting, c.analytics.storeErr
	c.analytics.mu.Unlock()
	if out.MovingTo == out.Server {
		out.MovingTo = "" // it ended meanwhile
	}
	return out, nil
}

// retryStart reports whether the status may start the store again: its
// last start failed, a while ago. A store that started and doesn't answer
// yet (opening its database) or that Docker restarts is left alone.
func (c *Core) retryStart() bool {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	return c.analytics.storeErr != "" && now().Sub(c.analytics.startedAt) >= storeRetryAfter
}

// SetAnalyticsServer picks the server the store runs on (an empty name
// or the control plane's own: the control plane's server) and turns
// analytics on. A store that runs elsewhere moves there with its data, in
// the background (see the top of this file); the answer shows the move
// running, and analytics.moved or analytics.move_failed is audited at the
// end. When the old server is offline its data can't be copied: the move
// is refused unless req.StartEmpty, which starts the store empty and
// removes the old one when that server is back.
func (c *Core) SetAnalyticsServer(ctx context.Context, a Actor, req apitypes.SetAnalyticsServerRequest) (apitypes.AnalyticsStatus, error) {
	if err := a.requirePlatform(ServersManage, "choosing the analytics server"); err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	var target store.Server
	var err error
	if req.Server == "" {
		target, err = c.store.GetServer(ctx, c.localID)
	} else if target, err = c.store.GetServerByName(ctx, req.Server); store.IsNotFound(err) {
		return apitypes.AnalyticsStatus{}, notFound("server %q not found; see `dock server list`", req.Server)
	}
	if err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	var id *int64
	if !target.Local {
		id = &target.ID
	}
	// Whether analytics runs, and the server it starts on when it doesn't,
	// are decided under the switch turning it off holds, so a shutdown in
	// flight is either done (off, its data kept on its server) or hasn't
	// begun.
	c.analytics.switching.Lock()
	cl, old := c.analyticsClient()
	if cl == nil {
		err := c.startAnalyticsOn(ctx, a, target, id)
		c.analytics.switching.Unlock()
		if err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
		c.audit(ctx, a, "analytics.server", nil, nil, target.Name, "analytics store on "+target.Name)
		return c.AnalyticsStatus(ctx, a)
	}
	c.analytics.switching.Unlock()
	if old != target.ID {
		from, err := c.store.GetServer(ctx, old)
		if err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
		if err := c.startStoreMove(ctx, a, cl, from, target, req.StartEmpty); err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
	}
	return c.AnalyticsStatus(ctx, a)
}

// startAnalyticsOn turns analytics on with its store on target (id: nil
// for the control plane's server), for SetAnalyticsServer while it doesn't
// run. Turned off, its data may still be on its server, and a store
// started elsewhere would have it removed as a stray: only its own server
// is taken. The first time, the store goes straight there; tracing may
// have made the settings with another server, and the trace store
// follows. The caller holds analytics.switching.
func (c *Core) startAnalyticsOn(ctx context.Context, a Actor, target store.Server, id *int64) error {
	prev, err := c.store.GetAnalyticsSettings(ctx)
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	switch {
	case c.analyticsOff() && c.settingsServer(prev) != target.ID:
		return conflict("analytics is turned off and its data stays on its server; turn it on there first (`dock analytics on`), then move it to %s", target.Name)
	case err == nil && c.settingsServer(prev) != target.ID:
		if err := c.store.SetAnalyticsServer(ctx, store.SetAnalyticsServerParams{ServerID: id, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
			return err
		}
		c.moveTraceStore(c.settingsServer(prev), target.ID)
	}
	return c.enableAnalyticsLocked(ctx, a.Name, id, false)
}

// settingsServer is the analytics server st names: the control plane's
// own when it names none.
func (c *Core) settingsServer(st store.AnalyticsSetting) int64 {
	if st.ServerID != nil {
		return *st.ServerID
	}
	return c.localID
}

// startStoreMove checks a move can run and starts it in the background:
// still from the store whose client cl the caller saw, which turning
// analytics off takes away under the same lock.
func (c *Core) startStoreMove(ctx context.Context, a Actor, cl *analytics.Client, from, to store.Server, startEmpty bool) error {
	if !c.online(to.ID) {
		return errf(apitypes.CodeServerOffline, "server %s is offline; move the analytics store once it is back", to.Name)
	}
	if !to.Local {
		if h := c.nodes.State(to.ID).Hello; h.Protocol < remote.AnalyticsBinaryProtocol {
			return invalid("server %s runs dock %s, which can't run the analytics store; update it with `dock update` first", to.Name, h.Version)
		}
	}
	copyData := c.online(from.ID)
	if !copyData && !startEmpty {
		return errf(apitypes.CodeServerOffline, "server %s holds the analytics data and is offline, so it can't be copied; move once it is back, or start empty on %s with `dock analytics server %s --start-empty` (the old data is removed when %s is back)", from.Name, to.Name, to.Name, from.Name)
	}
	c.analytics.mu.Lock()
	switch {
	case c.analytics.client != cl:
		// Turned off (or moved) since the caller looked: turning off
		// takes the client away under this lock, after checking no move
		// is reserved, so one of the two goes through.
		c.analytics.mu.Unlock()
		return conflict("analytics was turned off or moved meanwhile; see `dock analytics`")
	case c.analytics.moving != nil:
		m := c.analytics.moving
		c.analytics.mu.Unlock()
		return conflict("the analytics store is moving to %s already; wait for that to finish", m.to.Name)
	case c.analytics.starting:
		c.analytics.mu.Unlock()
		return conflict("the analytics store is starting on %s; move it once it runs", from.Name)
	}
	m := &analyticsMove{from: from, to: to}
	c.analytics.moving = m
	c.analytics.mu.Unlock()
	how := "copying its data"
	if !copyData {
		how = "starting empty: " + from.Name + " is offline"
	}
	c.audit(ctx, a, "analytics.move", nil, nil, to.Name, fmt.Sprintf("started moving the analytics store from %s to %s, %s", from.Name, to.Name, how))
	c.goCounted("moving the analytics store", func() { c.runStoreMove(a, m, copyData) })
	return nil
}

// runStoreMove runs a move startStoreMove started and records how it went.
func (c *Core) runStoreMove(a Actor, m *analyticsMove, copyData bool) {
	ctx, cancel := context.WithTimeout(c.ctx, storeMoveTimeout)
	defer cancel()
	// Through caught: a move that panics is a failed move, not one that
	// holds the forwarders off for good.
	moved, err := caught(c.log, "moving the analytics store", func() (string, error) { return c.moveStore(ctx, a, m, copyData) })
	c.analytics.mu.Lock()
	c.analytics.moving = nil
	c.analytics.mu.Unlock()
	if c.ctx.Err() != nil {
		return // the daemon stops: the next start finds the old store in place
	}
	if err != nil {
		err = fmt.Errorf("moving the analytics store to %s failed, it stays on %s: %w", m.to.Name, m.from.Name, err)
		c.setStoreErr(err)
		c.log.Error("analytics: move the store", "err", err)
		c.audit(context.WithoutCancel(ctx), a.platform(), "analytics.move_failed", nil, nil, m.to.Name, err.Error())
	} else {
		c.setStoreErr(nil)
		c.audit(context.WithoutCancel(ctx), a.platform(), "analytics.moved", nil, nil, m.to.Name, moved)
		if !copyData && c.online(m.from.ID) {
			// Back while the move ran, when its store couldn't go yet.
			c.removeStrayStore(m.from.ID, false)
		}
	}
	c.publishAnalytics()
	c.wakeForwarders()
}

// moveStore does the move's work, with the forwarders held off. It returns
// the audit summary. A failure before the switch leaves the store where
// it was and removes the partial copy.
func (c *Core) moveStore(ctx context.Context, a Actor, m *analyticsMove, copyData bool) (string, error) {
	st, err := c.store.GetAnalyticsSettings(ctx)
	if err != nil {
		return "", err
	}
	token, err := c.box.Open(st.Token, analyticsTokenAAD)
	if err != nil {
		return "", fmt.Errorf("open the analytics token: %w", err)
	}
	// Forwarders write nothing until the move ends: their tails end, and
	// the lock waits for those writing to finish.
	c.analytics.mu.Lock()
	c.analytics.runCancel()
	c.analytics.runCtx, c.analytics.runCancel = context.WithCancel(c.ctx)
	c.analytics.mu.Unlock()
	c.analytics.writing.Lock()
	defer c.analytics.writing.Unlock()

	var events, bytes int64
	if copyData {
		if events, bytes, err = c.copyStore(ctx, m, string(token)); err != nil {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), storeStartTimeout)
			defer rcancel()
			if rerr := c.runtime(m.to.ID).RemoveAnalyticsStore(rctx, node.StoreAnalytics); rerr != nil {
				c.log.Warn("analytics: remove the partial copy; it goes when that server next connects", "server", m.to.Name, "err", rerr)
			}
			return "", err
		}
	} else {
		// Empty means empty: not a store an earlier move left there.
		if err := c.runtime(m.to.ID).RemoveAnalyticsStore(ctx, node.StoreAnalytics); err != nil {
			return "", err
		}
		if err := c.startStore(ctx, m.to.ID, string(token)); err != nil {
			return "", err
		}
	}

	// The switch: from here on the new store is the analytics store, and
	// nothing below fails the move.
	var id *int64
	if !m.to.Local {
		id = &m.to.ID
	}
	if err := c.store.SetAnalyticsServer(ctx, store.SetAnalyticsServerParams{ServerID: id, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
		return "", err
	}
	st.ServerID = id
	c.moveTraceStore(m.from.ID, m.to.ID)
	if err := c.runAnalytics(ctx, st); err != nil {
		// The next daemon start picks it up from the settings.
		c.log.Error("analytics: forward to the moved store", "err", err)
	}
	if !copyData {
		return fmt.Sprintf("moved the analytics store from %s to %s, starting empty; the old store is removed when %s is back", m.from.Name, m.to.Name, m.from.Name), nil
	}
	summary := fmt.Sprintf("moved the analytics store from %s to %s with %d events (%s)", m.from.Name, m.to.Name, events, humanBytes(bytes))
	if err := c.runtime(m.from.ID).RemoveAnalyticsStore(ctx, node.StoreAnalytics); err != nil {
		c.log.Warn("analytics: remove the old store; it goes when that server next connects", "server", m.from.Name, "err", err)
		return summary + "; removing it from " + m.from.Name + " failed: " + err.Error(), nil
	}
	return summary + "; removed it from " + m.from.Name, nil
}

// copyStore starts an empty store on the move's new server and fills it
// with the old one's database, streamed through the control plane and
// checked by its SHA-256 and its events. It returns the events and bytes
// copied.
func (c *Core) copyStore(ctx context.Context, m *analyticsMove, token string) (int64, int64, error) {
	from, to := c.storeClient(m.from.ID, token), c.storeClient(m.to.ID, token)
	if _, err := c.readyStore(ctx, from, 0); err != nil {
		return 0, 0, fmt.Errorf("the store on %s doesn't answer, so its data can't be copied; wait for it to run (see `dock analytics server`): %w", m.from.Name, err)
	}
	// A store left there by a move that stopped part way goes first: the
	// copy fills an empty store only.
	if err := c.runtime(m.to.ID).RemoveAnalyticsStore(ctx, node.StoreAnalytics); err != nil {
		return 0, 0, err
	}
	if err := c.startStore(ctx, m.to.ID, token); err != nil {
		return 0, 0, err
	}
	if _, err := c.readyStore(ctx, to, 0); err != nil {
		return 0, 0, fmt.Errorf("the new store on %s: %w", m.to.Name, err)
	}
	snap, err := from.Snapshot(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read the store on %s: %w", m.from.Name, err)
	}
	defer func() { _ = snap.Close() }()
	sum := sha256.New()
	got, err := to.Restore(ctx, io.TeeReader(snap, sum), snap.Size)
	if err != nil {
		return 0, 0, fmt.Errorf("copy to %s: %w", m.to.Name, err)
	}
	if want := hex.EncodeToString(sum.Sum(nil)); got.Bytes != snap.Size || got.SHA256 != want {
		return 0, 0, fmt.Errorf("the copy on %s doesn't match: %d of %d bytes arrived", m.to.Name, got.Bytes, snap.Size)
	}
	// It restarts on the copy and must count what the snapshot held.
	after, err := c.readyStore(ctx, to, snap.Events)
	if err != nil {
		return 0, 0, fmt.Errorf("the new store on %s after the copy: %w", m.to.Name, err)
	}
	if after.Events != snap.Events {
		return 0, 0, fmt.Errorf("the new store on %s has %d events, the copy %d", m.to.Name, after.Events, snap.Events)
	}
	return snap.Events, snap.Size, nil
}

// errAnalyticsOff is analytics before it is turned on, for the backup.
var errAnalyticsOff = errors.New("analytics is off")

// analyticsSnapshot opens the store's snapshot for the control plane's
// backup (cpbackup.Source.Analytics): its database and session replays.
// The store holds its writes while it streams; the forwarders wait.
func (c *Core) analyticsSnapshot(ctx context.Context) (io.ReadCloser, int64, int64, error) {
	cl, _ := c.analyticsClient()
	if cl == nil {
		return nil, 0, 0, errAnalyticsOff
	}
	snap, err := cl.Snapshot(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	return snap, snap.Size, snap.Events, nil
}

// restoreStoreBackup fills an empty store from the analytics snapshot a
// restored control plane backup left in the data dir, then deletes it.
// Forwarders hold off meanwhile, as for a move: the store must be empty
// to take it. A store with events already keeps them, and the snapshot
// goes.
func (c *Core) restoreStoreBackup(ctx context.Context, cl *analytics.Client) error {
	path := filepath.Join(c.cfg.DataDir, cpbackup.AnalyticsFile)
	infoPath := filepath.Join(c.cfg.DataDir, cpbackup.AnalyticsInfoFile)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var info cpbackup.AnalyticsInfo
	b, err := os.ReadFile(infoPath)
	if err == nil {
		err = json.Unmarshal(b, &info)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", infoPath, err)
	}
	// As a move does: hold the forwarders off (a forwarder that takes the
	// new run before the lock below would tail it, holding its read lock
	// for good) and end their tails, whose read locks last as long as a
	// tail, then wait for any write in flight. The caller wakes them.
	c.analytics.mu.Lock()
	c.analytics.restoring = true
	c.analytics.runCancel()
	c.analytics.runCtx, c.analytics.runCancel = context.WithCancel(c.ctx)
	c.analytics.mu.Unlock()
	defer func() {
		c.analytics.mu.Lock()
		c.analytics.restoring = false
		c.analytics.mu.Unlock()
	}()
	c.analytics.writing.Lock()
	defer c.analytics.writing.Unlock()
	h, err := c.readyStore(ctx, cl, 0)
	if err != nil {
		return err
	}
	if h.Events == 0 {
		got, err := cl.Restore(ctx, f, info.Bytes)
		if err != nil {
			return fmt.Errorf("the analytics store didn't take the backup's data; it stays in %s: %w", path, err)
		}
		if got.Bytes != info.Bytes {
			return fmt.Errorf("the analytics store took %d of the backup's %d bytes; they stay in %s", got.Bytes, info.Bytes, path)
		}
		// It restarts on the copy and must count what the backup held.
		if _, err := c.readyStore(ctx, cl, info.Events); err != nil {
			return err
		}
		c.log.Info("analytics: the store has the backup's data", "events", info.Events, "bytes", info.Bytes)
	} else {
		c.log.Warn("analytics: the store has events already, so the backup's data isn't put in it", "events", h.Events)
	}
	_ = os.Remove(infoPath)
	return os.Remove(path)
}
