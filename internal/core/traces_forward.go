package core

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Spans and app metrics reach the trace store as events reach the
// analytics store: every server's trace ingest appends them to its spans
// and metrics spools, and while tracing is on the control plane tails
// each server's spools, writes each batch to the trace store with its
// position (written once however often it is sent) under the caps, and
// acknowledges it. Nothing polls: a forwarder waits on its tail, or for a
// server's state to change.
//
// A server may only send spans and points of the environments placed on
// it: a server's trace key would let it make tokens for any service, so
// the forwarder drops those of other environments.

// A release's token sends for as long as the release is its service's,
// and for traceTokenGrace after: a token is in every container of its
// release, so one taken from an old release would otherwise send as the
// service for good (ADR 0075).

// traceTokenGrace is how long after a release ended (it was replaced, or
// failed) its spans and points are still taken: time for its last
// container to drain and send what it had. An hour is far more than
// that, and far less than a token found later is old.
const traceTokenGrace = time.Hour

// spoolClock is the time a spool's lines were written by, as far as its
// forwarder can tell: what a release must not have ended traceTokenGrace
// before for its lines to be taken. Lines carry no time a server vouches
// for, and the time they are forwarded at is wrong after an outage: a
// backlog from hours ago would lose every span of a release replaced
// since. So the forwarder goes by when it last had nothing left to
// forward: lines it reads while it keeps up were written just now, and a
// backlog was written no earlier than when it last kept up.
type spoolClock struct {
	// asOf is when the forwarder last kept up: it had forwarded every
	// line there was. Zero until it first does (after a daemon start),
	// and until then no line is dropped for its release's age.
	asOf time.Time
	// flowing is set while the forwarder keeps up with its tail: the
	// last batch wasn't a full one, so nothing was waiting behind it.
	flowing bool
}

// at is the time the batch now arriving is judged at: now while the
// forwarder keeps up, else when it last did.
func (k *spoolClock) at(now time.Time) time.Time {
	if k.flowing {
		return now
	}
	return k.asOf
}

// forwarded records a batch that arrived at t and reached the trace
// store. A batch short of full was all there was: the forwarder keeps
// up. A full one may have more behind it.
func (k *spoolClock) forwarded(t time.Time, full bool) {
	if k.flowing || !full {
		k.asOf = t
	}
	k.flowing = !full
}

// stopped records that the tail ended at t: what is spooled from here on
// waits for the next tail, as a backlog.
func (k *spoolClock) stopped(t time.Time) {
	if k.flowing {
		k.asOf = t
	}
	k.flowing = false
}

// errTracingOff ends a trace spool's forwarder: tracing was turned off.
var errTracingOff = errors.New("tracing is off")

// traceSpools are the spools of every server the trace store takes.
var traceSpools = []node.Spool{node.SpoolSpans, node.SpoolMetrics}

// traceSource names a server's spool in the trace store's cursors:
// "spans:server:2", "metrics:server:2".
func traceSource(sp spoolOf) string {
	return string(sp.spool) + ":server:" + strconv.FormatInt(sp.server, 10)
}

// tracesRun is the trace store's client and server, and the context that
// ends when either changes; nil while tracing is off.
func (c *Core) tracesRun() (*analytics.Client, int64, context.Context) {
	c.traces.mu.Lock()
	defer c.traces.mu.Unlock()
	return c.traces.client, c.traces.serverID, c.traces.runCtx
}

// forwardTracesFrom starts serverID's trace spools' forwarders unless
// they run.
func (c *Core) forwardTracesFrom(serverID int64) {
	c.traces.mu.Lock()
	defer c.traces.mu.Unlock()
	if c.ctx.Err() != nil || c.traces.client == nil {
		return
	}
	if c.traces.forwarding == nil {
		c.traces.forwarding = map[spoolOf]bool{}
	}
	for _, which := range traceSpools {
		sp := spoolOf{serverID, which}
		if !c.traces.forwarding[sp] {
			c.traces.forwarding[sp] = true
			c.goCounted("forwarding a trace spool", func() { c.forwardTraceSpool(sp) })
		}
	}
}

// forwardTraceSpool forwards a server's spool until the daemon stops,
// tracing is turned off or the server is removed. Between tails it waits
// for a state change, or (when the trace store failed) a backoff timer,
// the one timer it has.
func (c *Core) forwardTraceSpool(sp spoolOf) {
	defer func() {
		c.traces.mu.Lock()
		delete(c.traces.forwarding, sp)
		c.traces.mu.Unlock()
	}()
	retry := forwardRetryMin
	var clock spoolClock
	for c.ctx.Err() == nil {
		wake := c.waitWake()
		err := c.forwardTraceSpoolOnce(sp, &clock)
		var timer <-chan time.Time
		switch {
		case err == nil, errors.Is(err, errNotReady):
			retry = forwardRetryMin
		case errors.Is(err, errTracingOff), store.IsNotFound(err):
			return
		default:
			c.log.Warn("traces: forward a trace spool", "server", sp.server, "spool", sp.spool, "err", err, "retry", retry)
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

// forwardTraceSpoolOnce tails a server's spool from the trace store's
// cursor until the tail ends, a write fails or the trace store changes.
// clock is the spool's, kept by its forwarder from one tail to the next.
func (c *Core) forwardTraceSpoolOnce(sp spoolOf, clock *spoolClock) error {
	defer func() { clock.stopped(now()) }()
	serverID := sp.server
	need := remote.TraceIngestProtocol
	if sp.spool == node.SpoolMetrics {
		need = remote.MetricsProtocol
	}
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
	case serverID != c.localID && c.nodes.State(serverID).Hello.Protocol < need:
		return errNotReady // no such spool until it is updated
	}
	source := traceSource(sp)
	ctx, cancel := context.WithCancel(run)
	defer cancel()
	cur, err := cl.SpansCursor(ctx, source)
	if err != nil {
		return err
	}
	rt := c.runtime(serverID)
	tail, err := rt.TailSpool(ctx, sp.spool, cur)
	if err != nil {
		return err
	}
	maxBytes := int64(c.cfg.Traces.MaxGB) << 30
	write := func(lines []byte, pos uint64) error {
		_, err := cl.WriteSpanLines(ctx, source, pos, lines, analytics.TraceLimits{
			ServiceDay: c.cfg.Traces.ServiceDaySpans, OrgDay: c.cfg.Traces.OrgDaySpans,
			ErrorDay: c.cfg.Traces.ErrorDaySpans, MaxBytes: maxBytes,
		})
		return err
	}
	if sp.spool == node.SpoolMetrics {
		write = func(lines []byte, pos uint64) error {
			_, err := cl.WriteMetricLines(ctx, source, pos, lines, analytics.MetricLimits{
				Series: c.cfg.Traces.MetricSeries, Names: c.cfg.Traces.MetricNames, MaxBytes: maxBytes,
			})
			return err
		}
	}
	for b := range tail {
		if b.Skipped {
			c.log.Warn("traces: a server's spool was full and dropped lines before they reached the trace store", "server", serverID, "spool", sp.spool)
		}
		arrived := now()
		lines, dropped, ended, err := c.boundLines(ctx, serverID, b.Events, clock.at(arrived))
		if err != nil {
			return err
		}
		if dropped > 0 {
			c.log.Warn("traces: dropped lines that don't name a service of the server that sent them", "server", serverID, "spool", sp.spool, "lines", dropped)
		}
		if ended > 0 {
			c.log.Warn("traces: dropped lines sent with the token of a release that was replaced or failed over an hour before", "server", serverID, "spool", sp.spool, "lines", ended)
		}
		if err := write(lines, b.Position); err != nil {
			return err
		}
		clock.forwarded(arrived, b.Count >= node.SpoolBatchEvents)
		if err := rt.AckSpool(ctx, sp.spool, b.Position, b.Count); err != nil {
			return err // the trace store has them; the next tail starts from its cursor
		}
	}
	if ctx.Err() != nil || !c.online(serverID) {
		return errNotReady
	}
	return errTailEnded
}

// boundLines keeps the spans or metric points (JSON lines) a server may
// send: each must name a live service in an environment on that server,
// with that service's environment, project and org, and one of its
// releases (or none). The ingest takes them from the app's token, but a
// server's spool is only as trustworthy as the server: this keeps one
// from writing spans or points for another org's or another server's
// services. Two queries a batch. Lines that name no service are dropped
// and counted too.
//
// A line whose release ended more than traceTokenGrace before asOf (the
// spool's clock; zero for no such check) is dropped and counted apart,
// as ended. A release ends when it fails or is canceled, and when the
// release that replaces it goes live. The active release never ends
// while it is active: asleep, moving to another server, or live again
// after a failed deploy; nor does one that is still deploying. A
// rollback is a new release with a token of its own.
func (c *Core) boundLines(ctx context.Context, serverID int64, lines []byte, asOf time.Time) (out []byte, dropped, ended int, err error) {
	type claim struct {
		Org     int64 `json:"org"`
		Project int64 `json:"project"`
		Env     int64 `json:"env"`
		Service int64 `json:"service"`
		Release int64 `json:"release"`
	}
	var claims []claim
	var raw [][]byte
	var rels []int64
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var cl claim
		if json.Unmarshal(line, &cl) != nil || cl.Service == 0 {
			dropped++
			continue
		}
		claims, raw = append(claims, cl), append(raw, line)
		if cl.Release != 0 {
			rels = append(rels, cl.Release)
		}
	}
	if len(claims) == 0 {
		return nil, dropped, 0, nil
	}
	scopes, err := c.store.ListServerServiceScopes(ctx, serverID)
	if err != nil {
		return nil, 0, 0, err
	}
	of := make(map[int64]store.ListServerServiceScopesRow, len(scopes))
	for _, sc := range scopes {
		of[sc.ID] = sc
	}
	relService, over := map[int64]int64{}, map[int64]bool{}
	if len(rels) > 0 {
		rows, err := c.store.ListReleaseServices(ctx, uniq(rels))
		if err != nil {
			return nil, 0, 0, err
		}
		for _, r := range rows {
			relService[r.ID] = r.ServiceID
			if at := releaseEnded(r); at != nil && !asOf.IsZero() && at.Add(traceTokenGrace).Before(asOf) {
				over[r.ID] = true
			}
		}
	}
	out = make([]byte, 0, len(lines))
	for i, cl := range claims {
		sc, ok := of[cl.Service]
		switch {
		case !ok || sc.EnvironmentID != cl.Env || sc.ProjectID != cl.Project || sc.OrgID != cl.Org ||
			(cl.Release != 0 && relService[cl.Release] != cl.Service):
			dropped++
		case over[cl.Release]:
			ended++
		default:
			out = append(append(out, raw[i]...), '\n')
		}
	}
	return out, dropped, ended, nil
}

// releaseEnded is when a release stopped being its service's, or nil
// while it still is (active, or deploying): a failed or canceled one
// when it finished, a superseded one when the release that replaced it
// went live. A superseded release nothing replaced (its service was
// archived, which supersedes it) has only its own finish time left to
// go by.
func releaseEnded(r store.ListReleaseServicesRow) *time.Time {
	switch apitypes.ReleaseStatus(r.Status) {
	case apitypes.ReleaseFailed:
		return r.FinishedAt
	case apitypes.ReleaseSuperseded:
		if r.ReplacedAt != nil {
			return r.ReplacedAt
		}
		return r.FinishedAt
	}
	return nil
}
