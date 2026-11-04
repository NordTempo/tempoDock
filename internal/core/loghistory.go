package core

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Log history (ADR 0046). When dock removes a container, removeContainer
// has its server archive the container's output first (ArchiveLogs) and
// records the server on the release (logs_server_id); ServiceLogs reads
// a release whose container is gone from there. Limits come from [logs]
// and are applied by the server after each archive, and by the hourly
// retention pass (pruneLogs), which also drops the archives of services
// that no longer exist. Nothing runs while no container is removed.

const (
	// archiveTimeout bounds archiving one container's output. A
	// container's log is at most Docker's 30 MB, well under a second of
	// gzip; past this, removal goes on without it.
	archiveTimeout = time.Minute
	// pruneLogsTimeout bounds one server's part of a retention pass.
	pruneLogsTimeout = time.Minute
)

// keepsLogs reports whether a server archives removed containers' output:
// this server always, a remote one from node protocol 14. Older nodes'
// failed releases keep their last lines on the control plane instead
// (saveContainerLogs).
func (c *Core) keepsLogs(serverID int64) bool {
	return serverID == c.local.ID || (c.nodes != nil && c.nodes.State(serverID).Hello.Protocol >= remote.LogsProtocol)
}

// logLimits are [logs]'s limits for one service's archives.
func (c *Core) logLimits() node.LogLimits {
	return node.LogLimits{MaxBytes: int64(c.cfg.Logs.KeepMBPerService) << 20, MaxAge: days(c.cfg.Logs.KeepDays)}
}

// archiveLogs archives a stopped container's output on its server and
// records the server on the container's release, which is published so
// open release lists offer its logs. Best effort: a failure is a release
// event (when the release is known) and removal goes on, as a stuck
// archive must never keep an old release running.
func (c *Core) archiveLogs(ctx context.Context, serverID int64, rt node.Runtime, id string) {
	if !c.keepsLogs(serverID) {
		return
	}
	actx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	a, err := rt.ArchiveLogs(actx, id, c.logLimits())
	releaseID, perr := strconv.ParseInt(a.Release, 10, 64)
	if perr != nil {
		c.log.Warn("keep a removed container's logs", "server", serverID, "container", id, "err", cmp.Or(err, perr))
		return
	}
	if err == nil {
		err = c.store.SetReleaseLogsServer(ctx, store.SetReleaseLogsServerParams{LogsServerID: &serverID, ID: releaseID})
		if err != nil {
			c.log.Error("record a release's log archive", "release", releaseID, "err", err)
			return
		}
	}
	rel, gerr := c.store.GetRelease(ctx, releaseID)
	var ss svcScope
	if gerr == nil {
		ss, gerr = c.scopeOf(ctx, rel.ServiceID)
	}
	switch {
	case gerr != nil:
		c.log.Warn("keep a removed container's logs", "release", releaseID, "err", cmp.Or(err, gerr))
	case err != nil:
		c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Could not keep the logs of release %d: %v", rel.Number, c.nodeErr(serverID, err)))
	default:
		c.publishRelease(ctx, ss, rel)
	}
}

// pruneLogs holds every online server's log archives to [logs]'s limits
// and removes those of services that no longer exist (the retention pass,
// at most hourly). Servers too old to keep logs are skipped. One query
// lists the services; servers are pruned at the same time.
func (c *Core) pruneLogs(ctx context.Context, servers []store.Server) {
	ids, err := c.store.ListServiceIDs(ctx)
	if err != nil {
		c.log.Warn("retention: log archives", "err", err)
		return
	}
	keep := make([]string, len(ids))
	for i, id := range ids {
		keep[i] = strconv.FormatInt(id, 10)
	}
	var targets []store.Server
	for _, srv := range servers {
		if c.online(srv.ID) && c.keepsLogs(srv.ID) {
			targets = append(targets, srv)
		}
	}
	p := node.LogPrune{KeepServices: keep, Limits: c.logLimits()}
	fanOut(ctx, len(targets), pruneLogsTimeout, func(ctx context.Context, i int) {
		srv := targets[i]
		rt := c.connected(srv.ID)
		if rt == nil {
			return
		}
		r, err := rt.PruneLogs(ctx, p)
		if err != nil {
			c.log.Warn("retention: log archives", "server", srv.Name, "err", c.nodeErr(srv.ID, err))
		}
		if r.Removed > 0 {
			c.log.Info("retention: log archives", "server", srv.Name, "removed", r.Removed, "bytes", r.ReclaimedBytes)
		}
	})
}
