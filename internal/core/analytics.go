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
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Dock Analytics in the control plane (ADR 0037). Every server's ingest
// appends events to its own spool. Once analytics is enabled the control
// plane:
//
//   - keeps the store's container running on the analytics server, from
//     the release's binary (analytics_store.go: on start, on a move,
//     whenever that server reconnects, and when the status page finds it
//     down);
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
	// forwarding holds the servers' spools with a forwarder running.
	forwarding map[spoolOf]bool
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
	// writing is read locked by each forwarder while it writes to the
	// store, and locked by a move while it copies the store
	// (analytics_store.go).
	writing sync.RWMutex
	// starting is set while a start of the store runs, so the status
	// page asking again doesn't start another. storeErr is why the last
	// start or move failed, "" once one worked.
	starting  bool
	startedAt time.Time
	storeErr  string
	// moving is the move running, nil otherwise. restoring is set while
	// the store takes a restored control plane backup's data. Both hold
	// the forwarders off.
	moving    *analyticsMove
	restoring bool
	// binMu makes downloads of the store's binary one at a time.
	binMu sync.Mutex
	// live are the sites someone watches live (analytics_live.go).
	live liveSites
	// alerts are the analytics alerts' state (analytics_alerts.go).
	alerts analyticsAlerts
	// public are the public dashboards' hostnames (analytics_public.go).
	public publicIndex
	// switching is held through turning analytics on or off, so a site's
	// first domain can't turn it on again while an operator turns it off
	// (enableAnalytics, SetAnalytics). Taken before mu, never under it.
	switching sync.Mutex
	// off: an operator turned analytics off (ADR 0101). The ingests get no
	// salts, so they keep nothing, and a site's first domain doesn't turn
	// it on again.
	off bool
	// pushing holds a lock per server, taken by each push of its salts and
	// rules from reading them to handing them over (pushSalts). Guarded by
	// mu; one entry per server.
	pushing map[int64]*sync.Mutex
}

// analyticsMinMemory is the least the store's container may get.
const analyticsMinMemory = 256 << 20

// spoolOf names one of a server's spools.
type spoolOf struct {
	server int64
	spool  node.Spool
}

// analyticsSource names a server's spool in the store's cursors:
// "server:1" and "replay:server:1".
func analyticsSource(sp spoolOf) string {
	src := "server:" + strconv.FormatInt(sp.server, 10)
	if sp.spool != node.SpoolEvents {
		src = string(sp.spool) + ":" + src
	}
	return src
}

// startAnalytics resumes analytics after a restart: salts for every
// server, and the store and forwarders if analytics was enabled.
func (c *Core) startAnalytics(ctx context.Context) error {
	c.analytics.wake = make(chan struct{})
	c.analytics.forwarding = map[spoolOf]bool{}
	c.analyticsServersChanged()
	if err := c.assignPublicSlugs(ctx); err != nil {
		c.log.Error("analytics: give sites their public dashboard addresses", "err", err)
	}
	if err := c.renameSitesAfterGoneHostnames(ctx); err != nil {
		c.log.Error("analytics: rename sites named after removed hostnames", "err", err)
	}
	if hosts, err := c.loadPublicHosts(ctx); err == nil {
		c.analytics.public.hosts.Store(&hosts)
	} else {
		c.log.Error("analytics: read the public dashboards; they aren't served until a site changes", "err", err)
	}
	// Whether an operator turned analytics off comes first: the salts
	// handed out next are withheld then (analyticsConfig).
	st, err := c.store.GetAnalyticsSettings(ctx)
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	c.analytics.mu.Lock()
	c.analytics.off = err == nil && st.TurnedOff
	c.analytics.mu.Unlock()
	c.rotateSalts(ctx, now())
	if store.IsNotFound(err) {
		return nil
	}
	if st.TracesEnabled {
		if err := c.runTraces(st); err != nil {
			return err
		}
	}
	if !st.Enabled {
		return nil
	}
	return c.runAnalytics(ctx, st)
}

// analyticsOff reports whether an operator turned analytics off.
func (c *Core) analyticsOff() bool {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	return c.analytics.off
}

// runAnalytics connects to the store, starts it on its server (in the
// background: an image pull takes a while) and starts the forwarders of
// the servers that are online.
func (c *Core) runAnalytics(ctx context.Context, st store.AnalyticsSetting) error {
	token, err := c.box.Open(st.Token, analyticsTokenAAD)
	if err != nil {
		return fmt.Errorf("open the analytics token: %w", err)
	}
	serverID := c.localID
	if st.ServerID != nil {
		serverID = *st.ServerID
	}
	c.analytics.mu.Lock()
	if c.analytics.runCancel != nil {
		c.analytics.runCancel()
	}
	c.analytics.runCtx, c.analytics.runCancel = context.WithCancel(c.ctx)
	c.analytics.serverID = serverID
	c.analytics.client = c.storeClient(serverID, string(token))
	c.analytics.mu.Unlock()
	c.ensureAnalyticsStore(serverID)
	c.forwardFrom(c.localID)
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

// storeClient reaches the store on serverID: its port on that server,
// over the node link for a remote one.
func (c *Core) storeClient(serverID int64, token string) *analytics.Client {
	return &analytics.Client{
		BaseURL: "http://analytics",
		Token:   token,
		Limits: analytics.StoreLimits{
			SiteDayEvents:      c.cfg.Analytics.SiteDayEvents,
			SiteDayReplayBytes: c.cfg.Analytics.SiteDayReplayMB << 20,
			MaxBytes:           c.cfg.Analytics.MaxGB << 30,
		},
		HTTP: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.runtime(serverID).DialAnalyticsStore(ctx, node.StoreAnalytics)
			},
			// A dashboard has dashboardParallel queries in flight.
			MaxIdleConnsPerHost: dashboardParallel,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
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

// pruneAnalytics deletes every site's raw events and Web Vitals past its
// event retention, its errors past its error retention and its replays
// past its replay retention, in the background, once a day with the
// salts. Daily totals stay.
func (c *Core) pruneAnalytics() {
	cl, _ := c.analyticsClient()
	if cl == nil {
		return
	}
	c.goCounted("pruning analytics data", func() {
		ctx, cancel := context.WithTimeout(c.ctx, time.Hour)
		defer cancel()
		sites, err := c.store.ListAllSites(ctx)
		if err != nil {
			return
		}
		defaults, err := c.siteDefaults(ctx)
		if err != nil {
			return
		}
		for _, s := range sites {
			set := mergeSettings(defaults, siteOverrides(store.Site{Settings: s.Settings}))
			t := now()
			q := analytics.PruneQuery{Site: s.ID, Before: t.AddDate(0, 0, -*set.EventRetentionDays).UnixMilli(),
				ErrorsBefore: t.AddDate(0, 0, -*set.ErrorRetentionDays).UnixMilli()}
			_, err := cl.Prune(ctx, q)
			if err == nil {
				_, err = cl.PruneReplays(ctx, analytics.ReplayPrune{Site: s.ID, Before: t.AddDate(0, 0, -*set.ReplayRetentionDays).UnixMilli()})
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				c.log.Warn("analytics: retention; trying again tomorrow", "site", s.ID, "err", err)
				// One site failing doesn't hold the others' retention up;
				// a store that is gone does.
				if errors.Is(err, analytics.ErrOffline) {
					return
				}
			}
		}
	})
}

// updateGeoIP has the store fetch this month's location database unless
// it has it, in the background: after the store starts, and once a day
// with the salts. The store answers at once when it is current.
func (c *Core) updateGeoIP() {
	cl, _ := c.analyticsClient()
	if cl == nil {
		return
	}
	c.goCounted("updating the location database", func() {
		// The store may still be opening its database after a start.
		for attempt := range 5 {
			ctx, cancel := context.WithTimeout(c.ctx, 15*time.Minute)
			_, err := cl.UpdateGeoIP(ctx)
			cancel()
			if err == nil || c.ctx.Err() != nil {
				return
			}
			if !errors.Is(err, analytics.ErrOffline) || attempt == 4 {
				c.log.Warn("analytics: the location database couldn't be updated; locations stay as they were", "err", err)
				return
			}
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
			}
		}
	})
}

// analyticsServerChanged is called when a server comes online or goes
// away: its forwarder starts (or wakes), the store starts if it is the
// analytics server (and a store left there goes if it isn't), and it
// gets the salts.
func (c *Core) analyticsServerChanged(serverID int64, online bool) {
	c.analyticsServersChanged()
	if online {
		c.tracesServerChanged(serverID)
		c.pushSalts(serverID)
		if cl, storeServer := c.analyticsClient(); cl != nil {
			if serverID == storeServer {
				c.ensureAnalyticsStore(serverID)
			} else {
				c.removeStrayStore(serverID, false)
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
		sealed, err := c.box.Seal(salt, saltAAD(d))
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
	c.updateGeoIP()
	c.pruneAnalytics()
	c.pruneTraces()
	c.pushSalts(c.localID)
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

// pushSalts hands a server what its ingest needs, in the background:
// today's and tomorrow's salts and every live site's rules. A server that
// misses them (offline, or too old to collect) gets them on its next
// connect. A server's pushes run one at a time, each reading what it hands
// over once the one before is done, so the last to land is never older
// than the one before it: without that, a push that read yesterday's
// salts could land after the one with today's.
func (c *Core) pushSalts(serverID int64) {
	c.goCounted("handing a server its analytics rules", func() {
		lock := c.pushLock(serverID)
		lock.Lock()
		defer lock.Unlock()
		ctx, cancel := context.WithTimeout(c.ctx, time.Minute)
		defer cancel()
		cfg, err := c.analyticsConfig(ctx)
		if err != nil {
			c.log.Error("analytics: read site rules; the servers keep the ones they have", "err", err)
		}
		if cfg.Salts == nil {
			return
		}
		if err := c.runtime(serverID).SetAnalytics(ctx, cfg); err != nil && c.ctx.Err() == nil {
			c.log.Warn("analytics: hand out salts; this server's sites go uncounted until it gets them", "server", serverID, "err", err)
		}
	})
}

// pushLock is the lock a server's pushes take (pushSalts).
func (c *Core) pushLock(serverID int64) *sync.Mutex {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	if c.analytics.pushing == nil {
		c.analytics.pushing = map[int64]*sync.Mutex{}
	}
	l := c.analytics.pushing[serverID]
	if l == nil {
		l = new(sync.Mutex)
		c.analytics.pushing[serverID] = l
	}
	return l
}

// analyticsConfig is what every server's ingest gets: the salts of today
// and later, and each live site's excluded paths, ignored addresses,
// whether it collects errors and Web Vitals, and how it records replays.
// When only the rules can't be read it returns the salts, Sites nil, and
// the error.
func (c *Core) analyticsConfig(ctx context.Context) (node.AnalyticsConfig, error) {
	rows, err := c.store.ListAnalyticsSalts(ctx, ingest.Day(now()))
	if err != nil {
		return node.AnalyticsConfig{}, err
	}
	cfg := node.AnalyticsConfig{Salts: make(map[int64][]byte, len(rows))}
	cfg.TraceKey = c.traceKey()
	if c.analyticsOff() {
		return cfg, nil // no salts: the ingests keep nothing (ADR 0101)
	}
	for _, r := range rows {
		if salt, err := c.box.Open(r.Salt, saltAAD(r.Day)); err == nil {
			cfg.Salts[r.Day] = salt
		}
	}
	sites, err := c.store.ListLiveSites(ctx)
	if err != nil {
		return cfg, err
	}
	defaults, err := c.siteDefaults(ctx)
	if err != nil {
		return cfg, err
	}
	cfg.Sites = map[int64]node.SiteRules{}
	for _, s := range sites {
		set := mergeSettings(defaults, siteOverrides(store.Site{Settings: s.Settings}))
		r := node.SiteRules{ExcludedPaths: *set.ExcludedPaths, IgnoredIPs: *set.IgnoredIPs, ErrorsOff: !*set.ErrorsAndVitals}
		if *set.Replay {
			r.ReplayPercent, r.MaskAllText = *set.ReplaySampleRate, *set.MaskAllText
		}
		// The consent mode matters to a site that records, or that holds
		// everything back; the rest send the same script either way.
		if *set.Consent == apitypes.ConsentAll || (*set.Consent == apitypes.ConsentReplay && r.ReplayPercent > 0) {
			r.Consent = *set.Consent
		}
		if len(r.ExcludedPaths) > 0 || len(r.IgnoredIPs) > 0 || r.ErrorsOff || r.ReplayPercent > 0 || r.Consent != "" {
			cfg.Sites[s.ID] = r
		}
	}
	return cfg, nil
}

// analyticsSitesChanged hands every server the sites' new rules, and
// routes the public dashboards anew.
func (c *Core) analyticsSitesChanged(ctx context.Context) {
	c.publicSitesChanged(ctx)
	c.pushAnalyticsToAll(ctx)
}

// pushAnalyticsToAll hands every online server what its ingests need
// (pushSalts): the salts, the sites' rules and the trace key.
func (c *Core) pushAnalyticsToAll(ctx context.Context) {
	c.pushSalts(c.localID)
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

// forwardFrom starts the forwarders of serverID's spools unless they run.
func (c *Core) forwardFrom(serverID int64) {
	c.analytics.mu.Lock()
	defer c.analytics.mu.Unlock()
	if c.ctx.Err() != nil {
		return
	}
	for _, which := range []node.Spool{node.SpoolEvents, node.SpoolReplay} {
		sp := spoolOf{serverID, which}
		if !c.analytics.forwarding[sp] {
			c.analytics.forwarding[sp] = true
			c.goCounted("forwarding a server's analytics", func() { c.forward(sp) })
		}
	}
}

// errNotReady is a forwarder that can't run until a server's state
// changes: its server or the analytics server is offline.
var errNotReady = errors.New("not ready")

// Forwarder backoff, while the store doesn't answer or writes fail.
const (
	forwardRetryMin = time.Second
	forwardRetryMax = 30 * time.Second
)

// forward forwards a server's spool to the store until the daemon stops
// or the server is removed. Between tails it waits for a state change,
// or (when the store failed) a backoff timer, the one timer it has.
func (c *Core) forward(sp spoolOf) {
	serverID := sp.server
	defer func() {
		c.analytics.mu.Lock()
		delete(c.analytics.forwarding, sp)
		c.analytics.mu.Unlock()
	}()
	retry := forwardRetryMin
	for c.ctx.Err() == nil {
		wake := c.waitWake()
		// Through caught: a tail that panics is a failed tail, tried
		// again after the backoff.
		err := caughtErr(c.log, "forwarding a server's analytics", func() error { return c.forwardOnce(sp) })
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

// forwardOnce tails a server's spool from the store's cursor until the
// tail ends, a write fails, or the store changes. Batches go to the store
// as the JSON lines they are, with their position. A server too old for
// the replay spool isn't ready for its forwarder until it is updated.
func (c *Core) forwardOnce(sp spoolOf) error {
	serverID := sp.server
	if serverID != c.localID {
		// Removed servers end their forwarder, offline or not.
		if _, err := c.store.GetServer(c.ctx, serverID); err != nil {
			return err
		}
	}
	// A move or a restore holds every forwarder off while it fills the
	// store. The run is read first: both mark themselves before they
	// replace the run, so a forwarder holding the new one sees the mark
	// and stops.
	cl, storeServer, run := c.analyticsRun()
	if !c.analytics.writing.TryRLock() {
		return errNotReady
	}
	defer c.analytics.writing.RUnlock()
	c.analytics.mu.Lock()
	held := c.analytics.moving != nil || c.analytics.restoring
	c.analytics.mu.Unlock()
	if held || cl == nil || !c.online(serverID) || !c.online(storeServer) {
		return errNotReady
	}
	if sp.spool == node.SpoolReplay && serverID != c.localID && c.nodes.State(serverID).Hello.Protocol < remote.ReplayProtocol {
		return errNotReady
	}
	source := analyticsSource(sp)
	ctx, cancel := context.WithCancel(run)
	defer cancel()
	cur, err := cl.Cursor(ctx, source)
	if err != nil {
		return err
	}
	rt := c.runtime(serverID)
	tail, err := rt.TailSpool(ctx, sp.spool, cur)
	if err != nil {
		return err
	}
	for b := range tail {
		if b.Skipped {
			c.log.Warn("analytics: a server's spool was full and dropped events before they reached the store", "server", serverID)
		}
		if sp.spool == node.SpoolReplay {
			res, err := cl.WriteReplayLines(ctx, source, b.Position, b.Events)
			if err != nil {
				return err
			}
			c.overBudget(res, "replay recordings", "site_day_replay_mb")
		} else {
			res, err := cl.WriteLines(ctx, source, b.Position, b.Events)
			if err != nil {
				return err
			}
			c.overBudget(res, "events", "site_day_events")
			c.observeLive(b.Events)
			c.analyticsWritten(ctx, res, b.Events)
			c.frontendErrors(serverID, b.Events)
		}
		if err := rt.AckSpool(ctx, sp.spool, b.Position, b.Count); err != nil {
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
	if err := a.requirePlatform(PlatformSettings, "turning analytics on"); err != nil {
		return err
	}
	return c.enableAnalytics(ctx, a.Name, nil, false)
}

// enableAnalytics is EnableAnalytics without the permission check, for
// operations that enable it on the way: the first site (bySite), which
// is refused while an operator has turned analytics off, and moving the
// store. A first enable puts the store on serverID (nil: the control
// plane's server); a later one changes nothing. Turned on again after it
// was turned off, the store runs on its server again, on the data it
// kept, and the ingests get their salts back, even when the start fails
// part way.
func (c *Core) enableAnalytics(ctx context.Context, by string, serverID *int64, bySite bool) error {
	c.analytics.switching.Lock()
	defer c.analytics.switching.Unlock()
	return c.enableAnalyticsLocked(ctx, by, serverID, bySite)
}

// enableAnalyticsLocked is enableAnalytics for a caller that holds
// analytics.switching.
func (c *Core) enableAnalyticsLocked(ctx context.Context, by string, serverID *int64, bySite bool) error {
	if cl, _ := c.analyticsClient(); cl != nil {
		return nil
	}
	wasOff := c.analyticsOff()
	if wasOff && bySite {
		return errAnalyticsTurnedOff
	}
	sealed, err := c.box.Seal([]byte(newAnalyticsToken()), analyticsTokenAAD)
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
	c.analytics.mu.Lock()
	c.analytics.off = false
	c.analytics.mu.Unlock()
	if wasOff {
		// The salts again, whatever happens to the start below.
		defer c.pushAnalyticsToAll(context.WithoutCancel(ctx))
	}
	return c.runAnalytics(ctx, st)
}

// errAnalyticsTurnedOff refuses turning analytics on by the way while an
// operator has turned it off.
var errAnalyticsTurnedOff = conflict("Dock Analytics is turned off on this platform; a platform operator turns it on in Console › Features (`dock analytics on`)")

// overBudget logs the sites a write took past their budget for the day
// (analytics.StoreLimits): the store names each once a day, so this is
// one line a site a day, however much arrives.
func (c *Core) overBudget(res analytics.WriteResult, what, setting string) {
	for _, site := range res.OverBudget {
		c.log.Warn("analytics: a site is past its "+what+" for today; the rest of today's are dropped. If that is real traffic, raise [analytics] "+setting, "site", site)
	}
}
