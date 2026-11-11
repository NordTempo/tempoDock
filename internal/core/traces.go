package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The trace store: spans for tracing over OTLP, kept by the
// dock-analytics binary in a container of its own (`dock-analytics
// traces`) beside the analytics store, on the analytics server, with the
// install's analytics token. Apart from the analytics store so a backlog
// of spans can't take its memory.
//
// Operators turn tracing on (EnableTraces), which doesn't need analytics
// on. The control plane then keeps the trace store running as it does the
// analytics store: on start, whenever its server reconnects, and when the
// status finds it down. Spans are diagnostics kept for days, so they don't
// move: when the analytics server changes, the trace store starts empty
// there and the old one goes. Once a day, with the analytics salts, days
// past [traces] retention_days are dropped.

// tracesState is the trace store as the control plane runs it.
type tracesState struct {
	mu sync.Mutex
	// client reaches the trace store, nil while tracing is off; serverID
	// is the server it runs on.
	client   *analytics.Client
	serverID int64
	// runCtx ends when the client or its server changes, or tracing is
	// turned off, ending the spans forwarders' tails.
	runCtx    context.Context
	runCancel context.CancelFunc
	// forwarding holds the servers with a spans forwarder running.
	forwarding map[int64]bool
	// starting is set while a start runs; storeErr is why the last one
	// failed, "" once one worked.
	starting  bool
	startedAt time.Time
	storeErr  string
}

// traceStoreTimeout bounds a start of the trace store (it may download
// the binary) and its removal.
const traceStoreTimeout = storeStartTimeout

// runTraces connects to the trace store on the analytics server of st,
// starts it there in the background, hands every server the trace key and
// starts the spans forwarders.
func (c *Core) runTraces(st store.AnalyticsSetting) error {
	token, err := c.box.Open(st.Token, analyticsTokenAAD)
	if err != nil {
		return fmt.Errorf("open the analytics token: %w", err)
	}
	serverID := c.localID
	if st.ServerID != nil {
		serverID = *st.ServerID
	}
	c.traces.mu.Lock()
	c.traces.serverID = serverID
	c.traces.client = c.traceClient(serverID, string(token))
	c.newTracesRun()
	c.traces.mu.Unlock()
	c.ensureTraceStore(serverID)
	// The servers' ingests take spans once they have the key.
	c.pushAnalyticsToAll(c.ctx)
	c.forwardSpansFrom(c.localID)
	if c.nodes != nil {
		servers, err := c.store.ListServers(c.ctx)
		if err != nil {
			return err
		}
		for _, s := range servers {
			if !s.Local && c.online(s.ID) {
				c.forwardSpansFrom(s.ID)
			}
		}
	}
	return nil
}

// newTracesRun ends the forwarders' tails of the old trace store and
// starts a new run; mu is held. A nil client (tracing off) ends them.
func (c *Core) newTracesRun() {
	if c.traces.runCancel != nil {
		c.traces.runCancel()
	}
	c.traces.runCtx, c.traces.runCancel = context.WithCancel(c.ctx)
}

// traceClient reaches the trace store on serverID, over the node link for
// a remote one.
func (c *Core) traceClient(serverID int64, token string) *analytics.Client {
	return &analytics.Client{
		BaseURL: "http://traces",
		Token:   token,
		HTTP: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.runtime(serverID).DialAnalyticsStore(ctx, node.StoreTraces)
			},
			IdleConnTimeout: 90 * time.Second,
		}},
	}
}

// tracesClient is the trace store's client and server, nil while tracing
// is off.
func (c *Core) tracesClient() (*analytics.Client, int64) {
	c.traces.mu.Lock()
	defer c.traces.mu.Unlock()
	return c.traces.client, c.traces.serverID
}

// ensureTraceStore starts (or updates) the trace store on serverID, in
// the background, unless a start runs.
func (c *Core) ensureTraceStore(serverID int64) {
	cl, _ := c.tracesClient()
	if cl == nil || !c.online(serverID) {
		return
	}
	c.traces.mu.Lock()
	if c.traces.starting {
		c.traces.mu.Unlock()
		return
	}
	c.traces.starting, c.traces.startedAt = true, now()
	c.traces.mu.Unlock()
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, traceStoreTimeout)
		defer cancel()
		err := c.startTraceStore(ctx, serverID, cl.Token)
		c.traces.mu.Lock()
		c.traces.starting, c.traces.storeErr = false, ""
		if err != nil {
			c.traces.storeErr = err.Error()
		}
		c.traces.mu.Unlock()
		if c.ctx.Err() != nil {
			return
		}
		c.publishAnalytics()
		if err != nil {
			c.log.Error("traces: start the trace store", "server", serverID, "err", err)
		}
	})
}

// startTraceStore runs the trace store on serverID from the release's
// binary for its arch, the analytics store's.
func (c *Core) startTraceStore(ctx context.Context, serverID int64, token string) error {
	if serverID != c.localID && c.nodes.State(serverID).Hello.Protocol < remote.TraceStoreProtocol {
		return fmt.Errorf("the server runs dock %s, which can't run the trace store; update it with `dock update`", c.nodes.State(serverID).Hello.Version)
	}
	spec, err := c.storeSpec(ctx, serverID, token)
	if err != nil {
		return err
	}
	spec.Kind, spec.MemoryBytes = node.StoreTraces, int64(c.cfg.Traces.StoreMemoryMB)<<20
	return c.runtime(serverID).EnsureAnalyticsStore(ctx, spec)
}

// tracesServerChanged is called when a server comes online: the trace
// store starts if it runs there, and one left there by a change of the
// analytics server while it was offline goes.
func (c *Core) tracesServerChanged(serverID int64) {
	cl, traceServer := c.tracesClient()
	switch {
	case cl == nil:
	case serverID == traceServer:
		c.ensureTraceStore(serverID)
		c.forwardSpansFrom(serverID)
	default:
		c.removeTraceStore(serverID)
		c.forwardSpansFrom(serverID)
	}
}

// removeTraceStore removes the trace store and its spans from serverID, in
// the background. Servers from before the trace store have none.
func (c *Core) removeTraceStore(serverID int64) {
	if serverID != c.localID && c.nodes.State(serverID).Hello.Protocol < remote.TraceStoreProtocol {
		return
	}
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, traceStoreTimeout)
		defer cancel()
		if err := c.runtime(serverID).RemoveAnalyticsStore(ctx, node.StoreTraces); err != nil && c.ctx.Err() == nil {
			c.log.Warn("traces: remove a trace store from a server that isn't the analytics server", "server", serverID, "err", err)
		}
	})
}

// moveTraceStore follows the analytics server from one server to another:
// the trace store starts empty on the new one, and the old one and its
// spans go (when that server is back, if it is offline).
func (c *Core) moveTraceStore(from, to int64) {
	c.traces.mu.Lock()
	cl := c.traces.client
	if cl == nil || from == to {
		c.traces.mu.Unlock()
		return
	}
	c.traces.serverID = to
	c.traces.client = c.traceClient(to, cl.Token)
	c.newTracesRun()
	c.traces.mu.Unlock()
	c.ensureTraceStore(to)
	c.wakeForwarders()
	if c.online(from) {
		c.removeTraceStore(from)
	}
}

// pruneTraces drops the days of spans past [traces] retention_days, in
// the background, once a day with the analytics salts.
func (c *Core) pruneTraces() {
	cl, _ := c.tracesClient()
	if cl == nil {
		return
	}
	before := now().AddDate(0, 0, -c.cfg.Traces.RetentionDays)
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, 10*time.Minute)
		defer cancel()
		if _, err := cl.PruneTraces(ctx, analytics.TracePrune{Before: before.UnixMilli()}); err != nil && c.ctx.Err() == nil {
			c.log.Warn("traces: drop old spans; the next day's prune drops them", "err", err)
		}
	})
}

// TracingStatus is tracing's state, for operators. A trace store that
// should run but doesn't answer is started again, in the background, as
// the analytics store is.
func (c *Core) TracingStatus(ctx context.Context, a Actor) (apitypes.TracingStatus, error) {
	if err := a.requireOperator("reading the tracing status"); err != nil {
		return apitypes.TracingStatus{}, err
	}
	out := apitypes.TracingStatus{RetentionDays: c.cfg.Traces.RetentionDays, MemoryMB: c.cfg.Traces.StoreMemoryMB}
	st, err := c.store.GetAnalyticsSettings(ctx)
	if store.IsNotFound(err) {
		out.Server = c.localName()
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = st.TracesEnabled
	serverID := c.localID
	if st.ServerID != nil {
		serverID = *st.ServerID
	}
	srv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return out, err
	}
	out.Server, out.ServerOnline = srv.Name, c.online(serverID)
	if cl, _ := c.tracesClient(); cl != nil && out.ServerOnline {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if h, err := cl.Health(hctx); err == nil {
			out.StoreRunning, out.StoreVersion, out.StoreBytes = true, h.Version, h.FileBytes
		} else if c.retryTraceStart() {
			c.ensureTraceStore(serverID)
		}
	}
	c.traces.mu.Lock()
	out.Starting, out.Error = c.traces.starting, c.traces.storeErr
	c.traces.mu.Unlock()
	return out, nil
}

// retryTraceStart reports whether the status may start the trace store
// again: its last start failed, a while ago.
func (c *Core) retryTraceStart() bool {
	c.traces.mu.Lock()
	defer c.traces.mu.Unlock()
	return c.traces.storeErr != "" && now().Sub(c.traces.startedAt) >= storeRetryAfter
}

// EnableTraces turns tracing on: the trace store starts on the analytics
// server (the control plane's own if none was chosen). It doesn't turn
// analytics on. Turning it on again changes nothing.
func (c *Core) EnableTraces(ctx context.Context, a Actor) (apitypes.TracingStatus, error) {
	if err := a.requireOperator("turning tracing on"); err != nil {
		return apitypes.TracingStatus{}, err
	}
	if cl, _ := c.tracesClient(); cl == nil {
		sealed, err := c.box.Seal([]byte(newAnalyticsToken()), analyticsTokenAAD)
		if err != nil {
			return apitypes.TracingStatus{}, err
		}
		t := now()
		// The analytics settings row holds the token and the server; an
		// existing one keeps its own.
		if err := c.store.CreateAnalyticsSettings(ctx, store.CreateAnalyticsSettingsParams{Token: sealed, UpdatedBy: a.Name, UpdatedAt: t}); err != nil {
			return apitypes.TracingStatus{}, err
		}
		if err := c.store.SetTracesEnabled(ctx, store.SetTracesEnabledParams{TracesEnabled: true, UpdatedBy: a.Name, UpdatedAt: t}); err != nil {
			return apitypes.TracingStatus{}, err
		}
		st, err := c.store.GetAnalyticsSettings(ctx)
		if err != nil {
			return apitypes.TracingStatus{}, err
		}
		if err := c.runTraces(st); err != nil {
			return apitypes.TracingStatus{}, err
		}
		c.applyAllRoutes(ctx) // routes start traces at the edge
		c.audit(ctx, a.platform(), "tracing.enable", nil, nil, "tracing", "turned tracing on")
	}
	return c.TracingStatus(ctx, a)
}

// DisableTraces turns tracing off: the trace store and every span it
// holds are removed from the analytics server, which must be online.
func (c *Core) DisableTraces(ctx context.Context, a Actor) (apitypes.TracingStatus, error) {
	if err := a.requireOperator("turning tracing off"); err != nil {
		return apitypes.TracingStatus{}, err
	}
	if cl, serverID := c.tracesClient(); cl != nil {
		if !c.online(serverID) {
			return apitypes.TracingStatus{}, errf(apitypes.CodeServerOffline, "the analytics server is offline; turn tracing off once it is back, so its spans go with the trace store")
		}
		rctx, cancel := context.WithTimeout(ctx, traceStoreTimeout)
		defer cancel()
		if err := c.runtime(serverID).RemoveAnalyticsStore(rctx, node.StoreTraces); err != nil {
			return apitypes.TracingStatus{}, fmt.Errorf("remove the trace store: %w", err)
		}
		if err := c.store.SetTracesEnabled(ctx, store.SetTracesEnabledParams{TracesEnabled: false, UpdatedBy: a.Name, UpdatedAt: now()}); err != nil {
			return apitypes.TracingStatus{}, err
		}
		c.traces.mu.Lock()
		c.traces.client, c.traces.storeErr = nil, ""
		c.newTracesRun()
		c.traces.mu.Unlock()
		c.wakeForwarders()
		c.pushAnalyticsToAll(ctx) // the servers' ingests stop taking spans
		c.applyAllRoutes(ctx)     // and Caddy stops starting traces
		c.audit(ctx, a.platform(), "tracing.disable", nil, nil, "tracing", "turned tracing off and removed the trace store with its spans")
		c.publishAnalytics()
	}
	return c.TracingStatus(ctx, a)
}
