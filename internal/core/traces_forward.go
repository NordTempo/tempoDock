package core

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Spans reach the trace store as events reach the analytics store: every
// server's trace ingest appends them to its spans spool, and while
// tracing is on the control plane tails each server's spool, writes each
// batch to the trace store with its position (written once however often
// it is sent) under the daily caps, and acknowledges it. Nothing polls:
// a forwarder waits on its tail, or for a server's state to change.
//
// A server may only send spans of the environments placed on it: a
// server's trace key would let it make tokens for any service, so the
// forwarder drops spans of other environments.

// errTracingOff ends a spans forwarder: tracing was turned off.
var errTracingOff = errors.New("tracing is off")

// spansSource names a server's spans spool in the trace store's cursors.
func spansSource(serverID int64) string {
	return "spans:server:" + strconv.FormatInt(serverID, 10)
}

// tracesRun is the trace store's client and server, and the context that
// ends when either changes; nil while tracing is off.
func (c *Core) tracesRun() (*analytics.Client, int64, context.Context) {
	c.traces.mu.Lock()
	defer c.traces.mu.Unlock()
	return c.traces.client, c.traces.serverID, c.traces.runCtx
}

// forwardSpansFrom starts serverID's spans forwarder unless it runs.
func (c *Core) forwardSpansFrom(serverID int64) {
	c.traces.mu.Lock()
	defer c.traces.mu.Unlock()
	if c.ctx.Err() != nil || c.traces.client == nil || c.traces.forwarding[serverID] {
		return
	}
	if c.traces.forwarding == nil {
		c.traces.forwarding = map[int64]bool{}
	}
	c.traces.forwarding[serverID] = true
	c.wg.Go(func() { c.forwardSpans(serverID) })
}

// forwardSpans forwards a server's spans spool until the daemon stops,
// tracing is turned off or the server is removed. Between tails it waits
// for a state change, or (when the trace store failed) a backoff timer,
// the one timer it has.
func (c *Core) forwardSpans(serverID int64) {
	defer func() {
		c.traces.mu.Lock()
		delete(c.traces.forwarding, serverID)
		c.traces.mu.Unlock()
	}()
	retry := forwardRetryMin
	for c.ctx.Err() == nil {
		wake := c.waitWake()
		err := c.forwardSpansOnce(serverID)
		var timer <-chan time.Time
		switch {
		case err == nil, errors.Is(err, errNotReady):
			retry = forwardRetryMin
		case errors.Is(err, errTracingOff), store.IsNotFound(err):
			return
		default:
			c.log.Warn("traces: forward spans", "server", serverID, "err", err, "retry", retry)
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

// forwardSpansOnce tails a server's spans spool from the trace store's
// cursor until the tail ends, a write fails or the trace store changes.
func (c *Core) forwardSpansOnce(serverID int64) error {
	if serverID != c.localID {
		if _, err := c.store.GetServer(c.ctx, serverID); err != nil {
			return err
		}
	}
	cl, traceServer, run := c.tracesRun()
	switch {
	case cl == nil:
		return errTracingOff
	case !c.online(serverID) || !c.online(traceServer):
		return errNotReady
	case serverID != c.localID && c.nodes.State(serverID).Hello.Protocol < remote.TraceIngestProtocol:
		return errNotReady // no spans spool until it is updated
	}
	source := spansSource(serverID)
	ctx, cancel := context.WithCancel(run)
	defer cancel()
	cur, err := cl.SpansCursor(ctx, source)
	if err != nil {
		return err
	}
	rt := c.runtime(serverID)
	tail, err := rt.TailSpool(ctx, node.SpoolSpans, cur)
	if err != nil {
		return err
	}
	lim := analytics.TraceLimits{
		ServiceDay: c.cfg.Traces.ServiceDaySpans, OrgDay: c.cfg.Traces.OrgDaySpans,
		ErrorDay: c.cfg.Traces.ErrorDaySpans, MaxBytes: int64(c.cfg.Traces.MaxGB) << 30,
	}
	for b := range tail {
		if b.Skipped {
			c.log.Warn("traces: a server's spans spool was full and dropped spans before they reached the trace store", "server", serverID)
		}
		envs, err := c.store.ListServerEnvironmentIDs(ctx, serverID)
		if err != nil {
			return err
		}
		lines, dropped := placedSpans(b.Events, envs)
		if dropped > 0 {
			c.log.Warn("traces: dropped spans of environments that aren't on the server that sent them", "server", serverID, "spans", dropped)
		}
		if _, err := cl.WriteSpanLines(ctx, source, b.Position, lines, lim); err != nil {
			return err
		}
		if err := rt.AckSpool(ctx, node.SpoolSpans, b.Position, b.Count); err != nil {
			return err // the trace store has them; the next tail starts from its cursor
		}
	}
	if ctx.Err() != nil || !c.online(serverID) {
		return errNotReady
	}
	return errTailEnded
}

// placedSpans keeps the spans (JSON lines) of the environments in envs,
// and counts the others. A line that isn't a span goes on: the trace
// store skips and counts it.
func placedSpans(lines []byte, envs []int64) ([]byte, int) {
	on := make(map[int64]bool, len(envs))
	for _, id := range envs {
		on[id] = true
	}
	out := make([]byte, 0, len(lines))
	dropped := 0
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		var sp struct {
			Env int64 `json:"env"`
		}
		if json.Unmarshal(line, &sp) == nil && !on[sp.Env] {
			dropped++
			continue
		}
		out = append(append(out, line...), '\n')
	}
	return out, dropped
}
