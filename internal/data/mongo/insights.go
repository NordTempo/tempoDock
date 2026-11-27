package mongo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

// Health for MongoDB (ADR 0110): the overview from serverStatus (one
// command) and the sessions from currentOp, as the monitor user
// (clusterMonitor). Dock leaves the profiler off, so there is no queries
// view; cancelling is killOp, as the owner.

// insightsTimeout bounds one insights read.
const insightsTimeout = 5 * time.Second

// acquireMonitor hands out a monitor connection; a refused login is
// ErrNotSetUp, so the panel creates the monitor and retries.
func (e *Engine) acquireMonitor(ctx context.Context, t data.Target) (*cached, error) {
	cn, err := e.acquire(ctx, t, data.RoleMonitor)
	if errors.Is(err, data.ErrNoPrivilege) {
		return nil, fmt.Errorf("%w: the monitor user can't log in", data.ErrNotSetUp)
	}
	return cn, err
}

// Pressure reads the connection counts.
func (e *Engine) Pressure(ctx context.Context, t data.Target) (apitypes.DataPressure, error) {
	mon, err := e.acquireMonitor(ctx, t)
	if err != nil {
		return apitypes.DataPressure{}, err
	}
	defer e.release(mon)
	status, err := run(ctx, mon, authDB, serverStatus)
	if err != nil {
		return apitypes.DataPressure{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	return pressure(status), nil
}

// serverStatus leaves out the big sections Dock doesn't read.
var serverStatus = wire.D{{Key: "serverStatus", Value: int32(1)}, {Key: "repl", Value: int32(0)}, {Key: "metrics", Value: int32(0)}, {Key: "locks", Value: int32(0)}, {Key: "wiredTiger", Value: int32(0)}, {Key: "tcmalloc", Value: int32(0)}}

func pressure(status wire.D) apitypes.DataPressure {
	conns, _ := status.Get("connections")
	cd := asDoc(conns)
	used := int(asInt(cd.Get("current")))
	return apitypes.DataPressure{
		Used: used, Max: used + int(asInt(cd.Get("available"))),
		Active: int(asInt(cd.Get("active"))), Idle: max(used-int(asInt(cd.Get("active"))), 0),
	}
}

// Insights reads the overview and the sessions.
func (e *Engine) Insights(ctx context.Context, t data.Target, c data.Call, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	ctx, cancel := context.WithTimeout(ctx, insightsTimeout)
	defer cancel()
	out := apitypes.DataInsights{Engine: apitypes.EngineMongoDB, ReadAt: time.Now(), Hints: []apitypes.DataHint{}, Unavailable: []apitypes.DataUnavailable{}}
	mon, err := e.acquireMonitor(ctx, t)
	if err != nil {
		return out, err
	}
	defer e.release(mon)
	for _, v := range req.Views {
		switch v {
		case apitypes.InsightOverview:
			status, err := run(ctx, mon, authDB, serverStatus)
			if err != nil {
				out.Unavailable = append(out.Unavailable, apitypes.DataUnavailable{View: v, Reason: err.Error()})
				continue
			}
			ov := &apitypes.DataOverview{Connections: pressure(status), QueryStats: apitypes.QueryStatsNotLoaded}
			if up := asInt(status.Get("uptime")); up > 0 {
				at := out.ReadAt.Add(-time.Duration(up) * time.Second).UTC()
				ov.StartedAt, out.Since, out.SinceReason = &at, &at, "start"
			}
			if dbs, err := run(ctx, mon, authDB, wire.D{{Key: "listDatabases", Value: int32(1)}}); err == nil {
				ov.Bytes = asInt(dbs.Get("totalSize"))
			}
			if ov.Connections.Max > 0 && ov.Connections.Used*10 >= ov.Connections.Max*8 {
				out.Hints = append(out.Hints, apitypes.DataHint{
					Kind: apitypes.HintConnectionsHigh, Level: "warn", View: v, Title: "Connections near the limit",
					Detail: fmt.Sprintf("%d of %d connections are open", ov.Connections.Used, ov.Connections.Max),
				})
			}
			out.Overview = ov
		case apitypes.InsightSessions:
			sessions, err := e.sessions(ctx, mon)
			if err != nil {
				out.Unavailable = append(out.Unavailable, apitypes.DataUnavailable{View: v, Reason: err.Error()})
				continue
			}
			out.Sessions = sessions
		default:
			out.Unavailable = append(out.Unavailable, apitypes.DataUnavailable{View: v, Reason: "MongoDB's panel shows the overview and sessions"})
		}
	}
	return out, nil
}

// sessions lists the client operations running now (currentOp), with
// their command as Extended JSON.
func (e *Engine) sessions(ctx context.Context, mon *cached) ([]apitypes.DataSession, error) {
	// secs_running is as of this command: each start counts back from it.
	now := time.Now()
	reply, err := run(ctx, mon, authDB, wire.D{{Key: "currentOp", Value: int32(1)}, {Key: "$all", Value: false}})
	if err != nil {
		return nil, err
	}
	ops, _ := reply.Get("inprog")
	out := []apitypes.DataSession{}
	for _, o := range asArray(ops) {
		od := asDoc(o)
		if docStr(od, "type") != "op" {
			continue
		}
		opid := wire.Compact(must(od.Get("opid")))
		s := apitypes.DataSession{
			ID: opid, Application: docStr(od, "appName"), Client: docStr(od, "client"),
			Kind: "client", State: "idle",
		}
		if users, ok := od.Get("effectiveUsers"); ok && len(asArray(users)) > 0 {
			s.User = docStr(asArray(users)[0], "user")
		}
		if strings.HasPrefix(s.Application, e.app) {
			s.Kind = "dock"
		}
		if active, _ := od.Get("active"); active == true {
			s.State = "active"
		}
		if secs := asInt(od.Get("secs_running")); secs > 0 || s.State == "active" {
			at := now.Add(-time.Duration(secs) * time.Second).UTC()
			s.QueryAt = &at
		}
		if cmd, ok := od.Get("command"); ok {
			text := wire.Compact(cmd)
			if len(text) > apitypes.DataInsightTextMaxBytes {
				text = text[:apitypes.DataInsightTextMaxBytes]
			}
			s.Text = text
		}
		out = append(out, s)
	}
	return out, nil
}

func must(v any, _ bool) any { return v }

// Cancel kills an operation (killOp) as the owner; MongoDB has one way,
// so terminate does the same. Found is false when it ended since.
func (e *Engine) Cancel(ctx context.Context, t data.Target, owner data.Credentials, session string, queryStartedAt *time.Time, _ bool) (data.CancelResult, error) {
	id, err := strconv.ParseInt(session, 10, 64)
	if err != nil {
		v, perr := wire.ParseJSON(session)
		n, ok := v.(int32)
		if perr != nil || !ok {
			return data.CancelResult{}, fmt.Errorf("%w: %q is not an operation ID from the sessions view", data.ErrInvalid, session)
		}
		id = int64(n)
	}
	cn, err := e.owner(ctx, t, owner)
	if err != nil {
		return data.CancelResult{}, err
	}
	defer func() { _ = cn.Conn.Close() }()
	reply, err := run(ctx, cn, authDB, wire.D{{Key: "currentOp", Value: int32(1)}, {Key: "opid", Value: int32(id)}})
	if err != nil {
		return data.CancelResult{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	ops, _ := reply.Get("inprog")
	if len(asArray(ops)) == 0 {
		return data.CancelResult{}, nil
	}
	od := asDoc(asArray(ops)[0])
	// The operation the caller saw, not a later one with its ID (IDs start
	// again with the server).
	if queryStartedAt != nil {
		running := time.Duration(asInt(od.Get("secs_running"))) * time.Second
		if started := time.Now().Add(-running); started.Sub(*queryStartedAt).Abs() > 2*time.Second {
			return data.CancelResult{}, nil
		}
	}
	res := data.CancelResult{Found: true, Application: docStr(od, "appName"), Running: time.Duration(asInt(od.Get("secs_running"))) * time.Second}
	if cmd, ok := od.Get("command"); ok {
		res.Text = wire.Compact(cmd)
	}
	if users, ok := od.Get("effectiveUsers"); ok && len(asArray(users)) > 0 {
		res.User = docStr(asArray(users)[0], "user")
	}
	if _, err := run(ctx, cn, authDB, wire.D{{Key: "killOp", Value: int32(1)}, {Key: "op", Value: int32(id)}}); err != nil {
		return res, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	return res, nil
}
