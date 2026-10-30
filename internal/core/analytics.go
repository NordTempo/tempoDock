package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Dock Analytics in the control plane (ADR 0037). Every server's ingest
// appends events to its own spool. Once analytics is enabled the control
// plane:
//
//   - keeps the store's container running on the analytics server
//     (EnsureAnalyticsStore, on start, on a change of server and whenever
//     that server reconnects);
//   - makes a salt per UTC day and hands today's and tomorrow's to every
//     server (rotateSalts on the statistics tick, and on each connect);
//   - forwards every server's spool to the store: one goroutine per server
//     tails it over the node link, writes each batch to the store with its
//     position (so a batch written twice counts once) and acknowledges it.
//
// Nothing polls: tails are streams the node pushes into as events arrive,
// and a forwarder that can't run waits for a server to change state. Only
// a store that doesn't answer is retried, with backoff, while forwarding
// fails.
type analyticsState struct {
	mu sync.Mutex
	// saltDay is the UTC day salts were last made and handed out for.
	saltDay int64
	// forwarding holds the servers with a forwarder running.
	forwarding map[int64]bool
	// wake is closed and replaced when a server's state changes, waking
	// forwarders that wait for theirs or the analytics server's.
	wake chan struct{}
	// client reaches the store; nil until analytics is enabled. serverID
	// is the server it runs on. runCtx ends when either changes, ending
	// the forwarders' tails so they pick up the new store.
	client    *analytics.Client
	serverID  int64
	runCtx    context.Context
	runCancel context.CancelFunc
}

// analyticsMinMemory is the least the store's container may get.
const analyticsMinMemory = 256 << 20

// defaultAnalyticsImage is the store's image released with this version
// of Dock.
func defaultAnalyticsImage() string {
	return "ghcr.io/nordtempo/dock-analytics:" + version.Version
}

// analyticsSource names a server's spool in the store's cursors.
func analyticsSource(serverID int64) string { return "server:" + strconv.FormatInt(serverID, 10) }

// startAnalytics resumes analytics after a restart: salts for every
// server, and the store and forwarders if analytics was enabled.
func (c *Core) startAnalytics(ctx context.Context) error {
	c.analytics.wake = make(chan struct{})
	c.analytics.forwarding = map[int64]bool{}
	c.rotateSalts(ctx, now())
	st, err := c.store.GetAnalyticsSettings(ctx)
	if store.IsNotFound(err) || (err == nil && !st.Enabled) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.runAnalytics(ctx, st)
}

// runAnalytics connects to the store, starts it on its server (in the
// background: an image pull takes a while) and starts the forwarders of
// the servers that are online.
func (c *Core) runAnalytics(ctx context.Context, st store.AnalyticsSetting) error {
	token, err := c.box.Open(st.Token)
	if err != nil {
		return fmt.Errorf("open the analytics token: %w", err)
	}
	serverID := c.local.ID
	if st.ServerID != nil {
		serverID = *st.ServerID
	}
	c.analytics.mu.Lock()
	if c.analytics.runCancel != nil {
		c.analytics.runCancel()
	}
	c.analytics.runCtx, c.analytics.runCancel = context.WithCancel(c.ctx)
	c.analytics.serverID = serverID
	c.analytics.client = &analytics.Client{
		BaseURL: "http://analytics",
		Token:   string(token),
		HTTP: &http.Client{Transport: &http.Transport{
			// The store's port on its server, over the node link for a
			// remote one; one connection is plenty.
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.runtime(serverID).DialAnalyticsStore(ctx)
			},
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
	c.analytics.mu.Unlock()
	c.ensureAnalyticsStore(serverID)
	c.forwardFrom(c.local.ID)
	if c.nodes != nil {
		servers, err := c.store.ListServers(ctx)
		if err != nil {
			return err
		}
		for _, s := range servers {
			if !s.Local && c.online(s.ID) {
				c.forwardFrom(s.ID)
			}
		}
	}
	return nil
}

// analyticsClient is the store's client and server, or nil before
// analytics is enabled.
func (c *Core) analyticsClient() (*analytics.Client, int64) {
	cl, id, _ := c.analyticsRun()
	return cl, id
}

// analyticsRun is analyticsClient with the context that ends when the
// store's client or server changes.
func (c *Core) analyticsRun() (*analytics.Client, int64, context.Context) {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	return c.analytics.client, c.analytics.serverID, c.analytics.runCtx
}

// ensureAnalyticsStore starts (or updates) the store's container on
// serverID, in the background.
func (c *Core) ensureAnalyticsStore(serverID int64) {
	cl, _ := c.analyticsClient()
	if cl == nil || !c.online(serverID) {
		return
	}
	image := c.cfg.Analytics.StoreImage
	if image == "" {
		image = defaultAnalyticsImage()
	}
	spec := &node.AnalyticsStoreSpec{Image: image, Token: cl.Token, MemoryBytes: max(int64(c.cfg.Analytics.StoreMemoryMB)<<20, analyticsMinMemory)}
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Minute)
		defer cancel()
		if err := c.runtime(serverID).EnsureAnalyticsStore(ctx, spec); err != nil && c.ctx.Err() == nil {
			c.log.Error("start the analytics store", "server", serverID, "err", err)
			return
		}
		c.wakeForwarders()
	})
}

// analyticsServerChanged is called when a server comes online or goes
// away: its forwarder starts (or wakes), the store starts if it is the
// analytics server, and it gets the salts.
func (c *Core) analyticsServerChanged(serverID int64, online bool) {
	if online {
		c.pushSalts(serverID)
		if cl, storeServer := c.analyticsClient(); cl != nil {
			if serverID == storeServer {
				c.ensureAnalyticsStore(serverID)
			}
			c.forwardFrom(serverID)
		}
	}
	c.wakeForwarders()
}

// wakeForwarders wakes every forwarder that waits for a state change.
func (c *Core) wakeForwarders() {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	if c.analytics.wake != nil {
		close(c.analytics.wake)
	}
	c.analytics.wake = make(chan struct{})
}

// waitWake returns the channel the next state change closes.
func (c *Core) waitWake() <-chan struct{} {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	return c.analytics.wake
}

// rotateSalts makes sure today's and tomorrow's salts exist and older ones
// are gone, and hands them to every server when the day changed. The
// statistics tick calls it every minute; on the same day it does nothing.
func (c *Core) rotateSalts(ctx context.Context, t time.Time) {
	today := ingest.Day(t)
	c.analytics.mu.Lock()
	done := c.analytics.saltDay >= today
	c.analytics.mu.Unlock()
	if done {
		// The same day, or a clock that went back: salts only move on.
		return
	}
	for _, d := range []int64{today, today + 1} {
		salt := make([]byte, ingest.SaltBytes)
		_, _ = rand.Read(salt)
		sealed, err := c.box.Seal(salt)
		if err == nil {
			err = c.store.AddAnalyticsSalt(ctx, store.AddAnalyticsSaltParams{Day: d, Salt: sealed})
		}
		if err != nil {
			c.log.Error("analytics: make a salt", "err", err)
			return
		}
	}
	if err := c.store.DeleteAnalyticsSaltsBefore(ctx, today); err != nil {
		c.log.Error("analytics: delete old salts", "err", err)
		return
	}
	c.analytics.mu.Lock()
	c.analytics.saltDay = today
	c.analytics.mu.Unlock()
	c.pushSalts(c.local.ID)
	if c.nodes == nil {
		return
	}
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return
	}
	for _, s := range servers {
		if !s.Local && c.online(s.ID) {
			c.pushSalts(s.ID)
		}
	}
}

// pushSalts hands a server today's and tomorrow's salts, in the
// background. A server that misses them (offline, or too old to collect)
// gets them on its next connect.
func (c *Core) pushSalts(serverID int64) {
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, time.Minute)
		defer cancel()
		rows, err := c.store.ListAnalyticsSalts(ctx, ingest.Day(now()))
		if err != nil {
			c.log.Error("analytics: read salts", "err", err)
			return
		}
		salts := make(map[int64][]byte, len(rows))
		for _, r := range rows {
			if salt, err := c.box.Open(r.Salt); err == nil {
				salts[r.Day] = salt
			}
		}
		if err := c.runtime(serverID).SetAnalytics(ctx, node.AnalyticsConfig{Salts: salts}); err != nil && c.ctx.Err() == nil {
			c.log.Warn("analytics: hand out salts; this server's sites go uncounted until it gets them", "server", serverID, "err", err)
		}
	})
}

// forwardFrom starts serverID's forwarder unless it runs.
func (c *Core) forwardFrom(serverID int64) {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	if c.analytics.forwarding[serverID] || c.ctx.Err() != nil {
		return
	}
	c.analytics.forwarding[serverID] = true
	c.wg.Go(func() { c.forward(serverID) })
}

// errNotReady is a forwarder that can't run until a server's state
// changes: its server or the analytics server is offline.
var errNotReady = errors.New("not ready")

// Forwarder backoff, while the store doesn't answer or writes fail.
const (
	forwardRetryMin = time.Second
	forwardRetryMax = 30 * time.Second
)

// forward forwards serverID's spool to the store until the daemon stops
// or the server is removed. Between tails it waits for a state change,
// or (when the store failed) a backoff timer, the one timer it has.
func (c *Core) forward(serverID int64) {
	defer func() {
		c.analytics.mu.Lock()
		delete(c.analytics.forwarding, serverID)
		c.analytics.mu.Unlock()
	}()
	retry := forwardRetryMin
	for c.ctx.Err() == nil {
		wake := c.waitWake()
		err := c.forwardOnce(serverID)
		var timer <-chan time.Time
		switch {
		case err == nil:
			retry = forwardRetryMin
		case errors.Is(err, errNotReady):
			retry = forwardRetryMin
		case store.IsNotFound(err):
			return // the server was removed
		default:
			c.log.Warn("analytics: forward events", "server", serverID, "err", err, "retry", retry)
			timer = time.After(retry)
			retry = min(2*retry, forwardRetryMax)
		}
		select {
		case <-c.ctx.Done():
			return
		case <-wake:
		case <-timer:
		}
	}
}

// errTailEnded is a tail that stopped while its server is still online:
// the node failed to read its spool. Retried with backoff.
var errTailEnded = errors.New("the server's spool stopped streaming; see its daemon's log")

// forwardOnce tails serverID's spool from the store's cursor until the
// tail ends, a write fails, or the store changes. Batches go to the store
// as the JSON lines they are, with their position.
func (c *Core) forwardOnce(serverID int64) error {
	if serverID != c.local.ID {
		// Removed servers end their forwarder, offline or not.
		if _, err := c.store.GetServer(c.ctx, serverID); err != nil {
			return err
		}
	}
	cl, storeServer, run := c.analyticsRun()
	if cl == nil || !c.online(serverID) || !c.online(storeServer) {
		return errNotReady
	}
	source := analyticsSource(serverID)
	ctx, cancel := context.WithCancel(run)
	defer cancel()
	cur, err := cl.Cursor(ctx, source)
	if err != nil {
		return err
	}
	rt := c.runtime(serverID)
	tail, err := rt.TailSpool(ctx, cur)
	if err != nil {
		return err
	}
	for b := range tail {
		if b.Skipped {
			c.log.Warn("analytics: a server's spool was full and dropped events before they reached the store", "server", serverID)
		}
		if _, err := cl.WriteLines(ctx, source, b.Position, b.Events); err != nil {
			return err
		}
		if err := rt.AckSpool(ctx, b.Position, b.Count); err != nil {
			// The store has them; the next tail starts from its cursor.
			return err
		}
	}
	switch {
	case ctx.Err() != nil, !c.online(serverID):
		return errNotReady // the store changed, or the server went away
	}
	return errTailEnded
}

// newAnalyticsToken makes the install's store token.
func newAnalyticsToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// EnableAnalytics turns Dock Analytics on (ADR 0037): the store starts on
// the analytics server and every server's spool is forwarded to it. It is
// idempotent; sites call it when the first one is created.
func (c *Core) EnableAnalytics(ctx context.Context, a Actor) error {
	if err := a.requireOperator("turning analytics on"); err != nil {
		return err
	}
	return c.enableAnalytics(ctx, a.Name, nil)
}

// enableAnalytics is EnableAnalytics without the permission check, for
// operations that enable it on the way (the first site). A first enable
// puts the store on serverID (nil: the control plane's server); a later
// one changes nothing.
func (c *Core) enableAnalytics(ctx context.Context, by string, serverID *int64) error {
	if cl, _ := c.analyticsClient(); cl != nil {
		return nil
	}
	sealed, err := c.box.Seal([]byte(newAnalyticsToken()))
	if err != nil {
		return err
	}
	t := now()
	if err := c.store.CreateAnalyticsSettings(ctx, store.CreateAnalyticsSettingsParams{Token: sealed, ServerID: serverID, UpdatedBy: by, UpdatedAt: t}); err != nil {
		return err
	}
	if err := c.store.SetAnalyticsEnabled(ctx, store.SetAnalyticsEnabledParams{UpdatedBy: by, UpdatedAt: t}); err != nil {
		return err
	}
	st, err := c.store.GetAnalyticsSettings(ctx)
	if err != nil {
		return err
	}
	return c.runAnalytics(ctx, st)
}

// stopAnalyticsStore removes the store's container from a server it no
// longer runs on, in the background. The server runs ensures one at a
// time, so a start still pulling its image finishes first and is then
// removed. Its volume, with the data, stays.
func (c *Core) stopAnalyticsStore(serverID int64) {
	if !c.online(serverID) {
		c.log.Warn("analytics: the old analytics server is offline; remove its dock-analytics container there by hand", "server", serverID)
		return
	}
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Minute)
		defer cancel()
		if err := c.runtime(serverID).EnsureAnalyticsStore(ctx, nil); err != nil && c.ctx.Err() == nil {
			c.log.Warn("analytics: stop the store on the old server", "server", serverID, "err", err)
		}
	})
}

// AnalyticsStatus is Dock Analytics' state, for operators.
func (c *Core) AnalyticsStatus(ctx context.Context, a Actor) (apitypes.AnalyticsStatus, error) {
	if err := a.requireOperator("reading analytics status"); err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	out := apitypes.AnalyticsStatus{}
	st, err := c.store.GetAnalyticsSettings(ctx)
	if store.IsNotFound(err) {
		out.Server = c.local.Name
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = st.Enabled
	serverID := c.local.ID
	if st.ServerID != nil {
		serverID = *st.ServerID
	}
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return out, err
	}
	out.Server = srv.Name
	out.ServerOnline = c.online(serverID)
	if cl, _ := c.analyticsClient(); cl != nil && out.ServerOnline {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if h, err := cl.Health(hctx); err == nil {
			out.StoreRunning, out.StoreVersion, out.Events, out.StoreBytes = true, h.Version, h.Events, h.FileBytes
		}
	}
	return out, nil
}

// SetAnalyticsServer picks the server the store runs on ("" or the
// control plane's own name: the control plane's server) and turns
// analytics on. The store on the old server stops; its data stays there,
// on its volume, and the new store starts empty. Moving data is a later
// phase (ADR 0037).
func (c *Core) SetAnalyticsServer(ctx context.Context, a Actor, server string) (apitypes.AnalyticsStatus, error) {
	if err := a.requireOperator("choosing the analytics server"); err != nil {
		return apitypes.AnalyticsStatus{}, err
	}
	target := c.local
	if server != "" && server != c.local.Name {
		s, err := c.store.GetServerByName(ctx, server)
		if store.IsNotFound(err) {
			return apitypes.AnalyticsStatus{}, notFound("server %q not found; see `dock server list`", server)
		}
		if err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
		target = s
	}
	var id *int64
	if !target.Local {
		id = &target.ID
	}
	cl, old := c.analyticsClient()
	switch {
	case cl == nil:
		// The first time: straight onto the chosen server.
		if err := c.enableAnalytics(ctx, a.Name, id); err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
	case old != target.ID:
		if err := c.store.SetAnalyticsServer(ctx, store.SetAnalyticsServerParams{ServerID: id, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
		st, err := c.store.GetAnalyticsSettings(ctx)
		if err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
		if err := c.runAnalytics(ctx, st); err != nil {
			return apitypes.AnalyticsStatus{}, err
		}
		c.stopAnalyticsStore(old)
		c.wakeForwarders()
	}
	c.audit(ctx, a, "analytics.server", nil, nil, target.Name, "analytics store on "+target.Name)
	return c.AnalyticsStatus(ctx, a)
}
