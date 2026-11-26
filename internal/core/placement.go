package core

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Placement (ADR 0015): every environment has a kind, servers accept kinds,
// and a project keeps all its environments of one kind on one server. The
// first environment of a kind picks that server; later ones follow it.

// defaultKind is the kind of an environment created without one.
func defaultKind(name string) apitypes.EnvironmentKind {
	if name == "production" {
		return apitypes.KindProduction
	}
	return apitypes.KindNonProduction
}

func validKind(k apitypes.EnvironmentKind) bool {
	return k == apitypes.KindProduction || k == apitypes.KindNonProduction
}

// kindWords is how errors name a kind.
func kindWords(k apitypes.EnvironmentKind) string {
	return strings.ReplaceAll(string(k), "_", "-")
}

func acceptsKind(s store.Server, k apitypes.EnvironmentKind) bool {
	if k == apitypes.KindProduction {
		return s.AcceptsProduction
	}
	return s.AcceptsNonProduction
}

// serverLoad is what placement weighs about one server.
type serverLoad struct {
	server   store.Server
	online   bool
	reserved int64 // bytes of memory limits placed here
	projects int
}

// free is the memory not yet reserved. A server that hasn't reported its
// memory has none, so it only wins when nothing else is eligible.
func (l serverLoad) free() int64 { return l.server.MemoryBytes - l.reserved }

// pickServer chooses where a project's first environment of kind goes:
// among online servers accepting kind and not disabled for placement, the
// one with the most free memory, then the one with fewer projects, then
// the oldest. It is O(servers) over loads from one query.
func pickServer(kind apitypes.EnvironmentKind, loads []serverLoad) (serverLoad, error) {
	var best *serverLoad
	for i := range loads {
		l := &loads[i]
		if !l.online || l.server.DisabledForPlacement || !acceptsKind(l.server, kind) {
			continue
		}
		if best == nil || better(*l, *best) {
			best = l
		}
	}
	if best != nil {
		return *best, nil
	}
	return serverLoad{}, noServerError(kind, loads)
}

// better reports whether a should win over b.
func better(a, b serverLoad) bool {
	if c := cmp.Compare(a.free(), b.free()); c != 0 {
		return c > 0
	}
	if a.projects != b.projects {
		return a.projects < b.projects
	}
	return a.server.ID < b.server.ID
}

// noServerError explains why no server can take an environment of kind:
// how many servers that accept it are offline or disabled for placement.
// Org members get it, so it names no servers (seesServers).
func noServerError(kind apitypes.EnvironmentKind, loads []serverLoad) error {
	disabled, offline := 0, 0
	for _, l := range loads {
		if !acceptsKind(l.server, kind) {
			continue
		}
		switch {
		case l.server.DisabledForPlacement:
			disabled++
		case !l.online:
			offline++
		}
	}
	if disabled+offline == 0 {
		return conflict("no server accepts %s environments; a platform operator has to allow them on one", kindWords(kind))
	}
	var why []string
	if offline > 0 {
		why = append(why, count(offline, "server")+" offline")
	}
	if disabled > 0 {
		why = append(why, count(disabled, "server")+" disabled for placement")
	}
	msg := fmt.Sprintf("no server can take a %s environment now (%s); try again later, or ask a platform operator", kindWords(kind), strings.Join(why, ", "))
	if offline > 0 {
		return errf(apitypes.CodeServerOffline, "%s", msg)
	}
	return conflict("%s", msg)
}

// serverLoads loads every server with what placement weighs, in one query.
func (c *Core) serverLoads(ctx context.Context, q *store.Queries) ([]serverLoad, error) {
	rows, err := q.ListServerLoads(ctx, int64(c.cfg.Defaults.MemoryMB))
	if err != nil {
		return nil, err
	}
	out := make([]serverLoad, len(rows))
	for i, r := range rows {
		out[i] = serverLoad{server: r.Server, online: c.online(r.Server.ID), reserved: r.ReservedMb << 20, projects: int(r.Projects)}
	}
	return out, nil
}

// placeEnvironment returns the server for a new environment of kind in p:
// where the project's environments of that kind already are (or are
// pinned), otherwise a server picked now and recorded for the next ones.
func (c *Core) placeEnvironment(ctx context.Context, q *store.Queries, p store.Project, kind apitypes.EnvironmentKind) (int64, error) {
	pl, err := q.GetPlacement(ctx, store.GetPlacementParams{ProjectID: p.ID, Kind: string(kind)})
	if err == nil {
		return pl.ServerID, nil
	}
	if !store.IsNotFound(err) {
		return 0, err
	}
	loads, err := c.serverLoads(ctx, q)
	if err != nil {
		return 0, err
	}
	best, err := pickServer(kind, loads)
	if err != nil {
		return 0, err
	}
	err = q.UpsertPlacement(ctx, store.UpsertPlacementParams{ProjectID: p.ID, Kind: string(kind), ServerID: best.server.ID})
	return best.server.ID, err
}

// ListPlacements returns which server a project's environments of each
// kind live on. Operators only, as servers are (seesServers).
func (c *Core) ListPlacements(ctx context.Context, a Actor, project string) ([]apitypes.Placement, error) {
	if err := a.requirePlatform(PlatformView, "reading placements"); err != nil {
		return nil, err
	}
	p, err := c.loadProject(ctx, a.OrgID, project)
	if err != nil {
		return nil, err
	}
	rows, err := c.store.ListPlacements(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	names, err := c.serverNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Placement, 0, len(rows))
	for _, r := range rows {
		out = append(out, apitypes.Placement{Kind: apitypes.EnvironmentKind(r.Kind), Server: names[r.ServerID], Pinned: r.Pinned})
	}
	return out, nil
}

// PinPlacement puts a project's environments of kind on a server chosen by
// an admin. Only before the project has any of that kind: moving placed
// environments (and their volumes) is a later feature. The server must
// accept the kind; being disabled for placement doesn't stop a pin.
func (c *Core) PinPlacement(ctx context.Context, a Actor, project string, kind apitypes.EnvironmentKind, server string) (apitypes.Placement, error) {
	if err := a.requirePlatform(ServersManage, "pinning a project to a server"); err != nil {
		return apitypes.Placement{}, err
	}
	if !validKind(kind) {
		return apitypes.Placement{}, invalid("kind must be production or non_production")
	}
	p, err := c.loadProject(ctx, a.OrgID, project)
	if err != nil {
		return apitypes.Placement{}, err
	}
	if err := notArchived(p); err != nil {
		return apitypes.Placement{}, err
	}
	srv, err := c.loadServer(ctx, server)
	if err != nil {
		return apitypes.Placement{}, err
	}
	if !acceptsKind(srv, kind) {
		return apitypes.Placement{}, invalid("server %s doesn't accept %s environments; allow them with `dock server update %s --%s`", srv.Name, kindWords(kind), srv.Name, kindWords(kind))
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		n, err := q.CountKindEnvironments(ctx, store.CountKindEnvironmentsParams{ProjectID: p.ID, Kind: string(kind)})
		if err != nil {
			return err
		}
		if n > 0 {
			pl, err := q.GetPlacement(ctx, store.GetPlacementParams{ProjectID: p.ID, Kind: string(kind)})
			if err != nil {
				return err
			}
			if pl.ServerID == srv.ID {
				return nil
			}
			return conflict("project %s already has %s environments on another server; moving them isn't supported yet", p.Name, kindWords(kind))
		}
		return q.UpsertPlacement(ctx, store.UpsertPlacementParams{ProjectID: p.ID, Kind: string(kind), ServerID: srv.ID, Pinned: true})
	})
	if err != nil {
		return apitypes.Placement{}, err
	}
	c.audit(ctx, a, "project.pin", &p, nil, p.Name, fmt.Sprintf("pinned %s environments to server %s", kindWords(kind), srv.Name))
	return apitypes.Placement{Kind: kind, Server: srv.Name, Pinned: true}, nil
}

func (c *Core) loadServer(ctx context.Context, name string) (store.Server, error) {
	s, err := c.store.GetServerByName(ctx, name)
	if store.IsNotFound(err) {
		return s, notFound("server %q not found", name)
	}
	return s, err
}

// online reports whether a server can be reached now. The local server
// always can; others when Options.Remote has a runtime for them.
func (c *Core) online(serverID int64) bool { return c.connected(serverID) != nil }

// connected returns the runtime of a server that can be reached now, or nil.
// A revoked server never can, even while the hub still has its link: no
// secrets or deploys go to a key the admin took back.
func (c *Core) connected(serverID int64) node.Runtime {
	if serverID == c.localID {
		return c.rt
	}
	if c.nodes != nil && !c.isRevoked(serverID) {
		if rt, ok := c.nodes.Runtime(serverID); ok {
			return rt
		}
	}
	return nil
}

// fanOut runs fn(ctx, i) for i in [0, n) at the same time, each with its
// own timeout, and waits for all. Loops over servers use it so one server
// that doesn't answer costs at most timeout and holds up no other; a
// single call runs inline.
//
// A panic in one call ends that call only (recovered, logged to the
// default logger: fanOut has no Core). The other servers' calls finish,
// and the caller reads that server's slot as it was before the call: fn
// writes a slot only with a whole result, or the caller marks it failed
// first.
func fanOut(ctx context.Context, n int, timeout time.Duration, fn func(ctx context.Context, i int)) {
	run := func(i int) {
		defer recovered(slog.Default(), "a call to one server of several")
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		fn(ctx, i)
	}
	if n == 1 {
		run(0)
		return
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { run(i) }) // run recovers: no second closure per server
	}
	wg.Wait()
}

// runtimeFor returns the node runtime of a server, or a server_offline
// error that says why it can't be reached. Org members get these errors,
// so they don't name the server (seesServers); operators find it on the
// console. Remote servers come from the node hub (SetNodes).
func (c *Core) runtimeFor(serverID int64) (node.Runtime, error) {
	if rt := c.connected(serverID); rt != nil {
		return rt, nil
	}
	s, err := c.store.GetServer(context.Background(), serverID)
	switch {
	case err == nil && s.RevokedAt != nil:
		return nil, errf(apitypes.CodeServerOffline, "the server is revoked; a platform operator has to join it again")
	case c.nodes != nil && c.nodes.State(serverID).Status == remote.StatusIncompatible:
		return nil, errf(apitypes.CodeServerOffline, "the server runs a dock this control plane can't drive; a platform operator has to update it")
	}
	return nil, errf(apitypes.CodeServerOffline, "the server is offline; try again once it is back")
}

// runtime returns a server's runtime for call sites that handle runtime
// errors anyway: an offline server's calls all fail with server_offline.
func (c *Core) runtime(serverID int64) node.Runtime {
	rt, err := c.runtimeFor(serverID)
	if err != nil {
		return offlineRuntime{err}
	}
	return rt
}

// offlineRuntime is the runtime of a server that isn't connected.
type offlineRuntime struct{ err error }

var _ node.Runtime = offlineRuntime{}

func (o offlineRuntime) Pull(context.Context, string, io.Writer, ...node.PullOptions) (string, error) {
	return "", o.err
}
func (o offlineRuntime) HasImage(context.Context, string) (bool, error) { return false, o.err }
func (o offlineRuntime) RemoveImage(context.Context, string) error      { return o.err }
func (o offlineRuntime) PruneImages(context.Context, node.PruneImagesRequest) (node.PruneResult, error) {
	return node.PruneResult{}, o.err
}

func (o offlineRuntime) DiskUsage(context.Context, node.PruneImagesRequest) (node.DiskUsage, error) {
	return node.DiskUsage{}, o.err
}
func (o offlineRuntime) PruneBuildCache(context.Context, int64) (node.PruneResult, error) {
	return node.PruneResult{}, o.err
}
func (o offlineRuntime) WarmBuilder(context.Context) error                        { return o.err }
func (o offlineRuntime) EnsureNetwork(context.Context, string, node.Labels) error { return o.err }
func (o offlineRuntime) Resume(context.Context, string) error                     { return o.err }
func (o offlineRuntime) OpenRequests(context.Context, []string) (int, error)      { return 0, o.err }
func (o offlineRuntime) RemoveNetwork(context.Context, string, node.Labels) error { return o.err }
func (o offlineRuntime) EnsureVolume(context.Context, string, node.Labels) error  { return o.err }
func (o offlineRuntime) RemoveVolume(context.Context, string, node.Labels) error  { return o.err }
func (o offlineRuntime) Start(context.Context, node.ContainerSpec) (node.Container, error) {
	return node.Container{}, o.err
}
func (o offlineRuntime) Stop(context.Context, string, time.Duration) error        { return o.err }
func (o offlineRuntime) Restart(context.Context, string, time.Duration) error     { return o.err }
func (o offlineRuntime) Remove(context.Context, string, node.RemoveOptions) error { return o.err }
func (o offlineRuntime) Inspect(context.Context, string) (node.Container, error) {
	return node.Container{}, o.err
}
func (o offlineRuntime) List(context.Context) ([]node.Container, error) { return nil, o.err }
func (o offlineRuntime) ReleaseContainers(context.Context, node.Labels) ([]string, error) {
	return nil, o.err
}
func (o offlineRuntime) HealthCheck(context.Context, node.Container, node.HealthCheck) error {
	return o.err
}
func (o offlineRuntime) Logs(context.Context, string, node.LogOptions) (<-chan node.LogLine, error) {
	return nil, o.err
}
func (o offlineRuntime) Events(context.Context) (<-chan node.ContainerEvent, error) {
	return nil, o.err
}
func (o offlineRuntime) Dial(context.Context, node.DialRequest) (net.Conn, error) { return nil, o.err }
func (o offlineRuntime) RunJob(context.Context, node.JobSpec) (node.JobResult, error) {
	return node.JobResult{}, o.err
}
func (o offlineRuntime) EnsureSidecar(context.Context, node.SidecarSpec) error { return o.err }
func (o offlineRuntime) RemoveSidecar(context.Context, string, node.Labels) error {
	return o.err
}
func (o offlineRuntime) Build(context.Context, node.BuildRequest) (node.Built, error) {
	return node.Built{}, o.err
}
func (o offlineRuntime) ApplyRoutes(context.Context, []node.Route, []node.Certificate) error {
	return o.err
}
func (o offlineRuntime) CheckDomain(context.Context, string) (node.DomainCheck, error) {
	return node.DomainCheck{}, o.err
}
func (o offlineRuntime) Forward(context.Context, int) (net.Listener, error) { return nil, o.err }
func (o offlineRuntime) HostStats(context.Context) (node.HostStats, error) {
	return node.HostStats{}, o.err
}
func (o offlineRuntime) ContainerStats(context.Context, []string) ([]node.ContainerStats, error) {
	return nil, o.err
}
func (o offlineRuntime) DrainTraffic(context.Context) ([]node.HostTraffic, error) { return nil, o.err }
func (o offlineRuntime) EnsureTunnel(context.Context, *node.TunnelSpec) error     { return o.err }
func (o offlineRuntime) SetAnalytics(context.Context, node.AnalyticsConfig) error { return o.err }
func (o offlineRuntime) TailSpool(context.Context, node.Spool, uint64) (<-chan node.SpoolBatch, error) {
	return nil, o.err
}
func (o offlineRuntime) AckSpool(context.Context, node.Spool, uint64, int) error { return o.err }
func (o offlineRuntime) EnsureAnalyticsStore(context.Context, node.AnalyticsStoreSpec) error {
	return o.err
}
func (o offlineRuntime) RemoveAnalyticsStore(context.Context, node.StoreKind) error { return o.err }
func (o offlineRuntime) DialAnalyticsStore(context.Context, node.StoreKind) (net.Conn, error) {
	return nil, o.err
}
func (o offlineRuntime) ArchiveLogs(context.Context, string, node.LogLimits) (node.LogArchive, error) {
	return node.LogArchive{}, o.err
}
func (o offlineRuntime) ArchivedLogs(context.Context, node.LogArchiveQuery) (<-chan node.LogLine, error) {
	return nil, o.err
}
func (o offlineRuntime) PruneLogs(context.Context, node.LogPrune) (node.PruneResult, error) {
	return node.PruneResult{}, o.err
}
func (o offlineRuntime) Probe(context.Context, node.ProbeRequest) (node.ProbeResult, error) {
	return node.ProbeResult{}, o.err
}
func (o offlineRuntime) SBOM(context.Context, node.SBOMRequest, io.Writer) (node.SBOMResult, error) {
	return node.SBOMResult{}, o.err
}
func (o offlineRuntime) Match(context.Context, node.MatchRequest, io.Reader, func(node.Finding) error) (node.MatchResult, error) {
	return node.MatchResult{}, o.err
}
func (o offlineRuntime) ScanDatabase(context.Context, node.ScanDatabaseRequest) (node.ScanDatabase, error) {
	return node.ScanDatabase{}, o.err
}
func (o offlineRuntime) ImageDigest(context.Context, string) (string, error) { return "", o.err }
func (o offlineRuntime) RemoteDigest(context.Context, string, node.PullOptions) (string, error) {
	return "", o.err
}
func (o offlineRuntime) PruneScanDatabase(context.Context, node.ScanDatabasePrune) (node.PruneResult, error) {
	return node.PruneResult{}, o.err
}
func (o offlineRuntime) ImportScanDatabase(context.Context, node.ScanDatabaseRequest, io.Reader, int64) (node.ScanDatabase, error) {
	return node.ScanDatabase{}, o.err
}
