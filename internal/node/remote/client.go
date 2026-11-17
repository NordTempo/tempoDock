package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// ActionResync is the Action of a synthetic node.ContainerEvent that
// Client.Events sends when events may have been missed: after the server
// reconnected, or the node's event stream restarted. Nothing else is set
// on it. Core reconciles the server when it sees one; events after it are
// from the new stream. Events with this action from the node itself are
// dropped.
const ActionResync = "dock.resync"

// Deadlines of calls on the control plane (ADR 0014 amendment, compromised
// node). Control calls get DefaultCallTimeout (HubOptions.CallTimeout);
// Stop and HealthCheck that plus their own timeout. Work that takes as
// long as the server needs gets a bound of its own: pulls, prunes, disk
// usage, tunnels, the analytics store and updates. Builds and jobs have
// none: a build is bounded by core, and a job (a backup, restore or
// backup move) runs for as long as its data takes, until its caller's ctx
// ends it. Logs and Events streams have none either: a quiet stream is
// normal, a dead link is caught by the keepalive, and their caller's ctx
// ends them.
const (
	DefaultCallTimeout = 30 * time.Second
	// pullTimeout bounds a pull. Its progress isn't byte level, so a big
	// layer can be silent for minutes; this only stops a node that never
	// finishes.
	pullTimeout = 30 * time.Minute
	// pruneTimeout bounds PruneImages and PruneBuildCache: removing many
	// images one by one, or trimming tens of gigabytes of BuildKit cache,
	// takes minutes on a slow disk.
	pruneTimeout = 10 * time.Minute
)

// Client is a remote server's node.Runtime on the control plane. Calls go
// over the server's current connection; while it has none they fail with
// ErrOffline (ErrIncompatible while its version is too old). Canceling a
// call's ctx cancels it on the node too.
type Client struct {
	hub *Hub
	id  int64
}

var _ node.Runtime = (*Client)(nil)

var errNoEnd = errors.New("response ended without an end frame")

// Online reports whether the server is connected and compatible right now.
func (c *Client) Online() bool {
	s := c.hub.session(c.id)
	return s != nil && s.status == StatusOnline
}

// session returns the server's session if calls can go to it.
func (c *Client) session(ctx context.Context) (*session, error) {
	s := c.hub.session(c.id)
	switch {
	case s == nil:
		return nil, offline(ctx, c.hub.name(c.id), nil)
	case s.status != StatusOnline:
		return nil, fmt.Errorf("server %s: %w", s.name(), ErrIncompatible)
	}
	return s, nil
}

// registryLoginProtocol is the first node protocol that takes a pull's
// username and password.
const registryLoginProtocol = 18

// Pull implements node.Containers. Progress arrives as log frames. An
// older node would drop a username and password and pull anonymously, so
// a pull with them is refused there, saying to upgrade it.
func (c *Client) Pull(ctx context.Context, ref string, logs io.Writer, opts ...node.PullOptions) (string, error) {
	req := pullRequest{Ref: ref}
	if len(opts) > 0 {
		o := opts[0]
		req.Token, req.Username, req.Password = o.Token, o.Username, o.Password
	}
	if req.Username != "" {
		if err := c.needProtocol(ctx, registryLoginProtocol, "pull with registry credentials"); err != nil {
			return "", err
		}
	}
	r, err := callWithLogs[imageIDResult](ctx, c, opPull, pullTimeout, req, nil, logs)
	return r.ImageID, err
}

// HasImage implements node.Containers.
func (c *Client) HasImage(ctx context.Context, ref string) (bool, error) {
	r, err := unaryCall[hasImageResult](ctx, c, opHasImage, imageRequest{Ref: ref})
	return r.Present, err
}

// RemoveImage implements node.Containers.
func (c *Client) RemoveImage(ctx context.Context, ref string) error {
	_, err := unaryCall[empty](ctx, c, opRemoveImage, imageRequest{Ref: ref})
	return err
}

// ownImagesProtocol is the first node protocol whose prune-images only
// removes the platform's own images, and that has remove-network.
const ownImagesProtocol = 3

// needProtocol returns an error saying what an older node can't do
// (what) when the server's protocol is below min.
func (c *Client) needProtocol(ctx context.Context, min int, what string) error {
	s, err := c.session(ctx)
	if err != nil {
		return err
	}
	if s.hello.Protocol < min {
		return fmt.Errorf("server %s runs dock %s, which can't %s; upgrade dock on it", s.name(), s.hello.Version, what)
	}
	return nil
}

// PruneImages implements node.Containers. Older nodes prune every
// dangling image on their host, other work's too, so they are not asked:
// they answer with an error and are simply not pruned.
func (c *Client) PruneImages(ctx context.Context, req node.PruneImagesRequest) (node.PruneResult, error) {
	if err := c.needProtocol(ctx, ownImagesProtocol, "prune only its own images"); err != nil {
		return node.PruneResult{}, err
	}
	r, err := callWithLogs[pruneResult](ctx, c, opPruneImages, pruneTimeout, pruneImagesRequest{Keep: req.Keep, Repos: req.Repos}, nil, nil)
	return node.PruneResult(r), err
}

// EnsureNetwork implements node.Containers.
func (c *Client) EnsureNetwork(ctx context.Context, name string, labels node.Labels) error {
	_, err := unaryCall[empty](ctx, c, opEnsureNetwork, resourceRequest{Name: name, Labels: toWireLabels(labels)})
	return err
}

// RemoveNetwork implements node.Containers. An older node doesn't have
// the operation: its networks stay.
func (c *Client) RemoveNetwork(ctx context.Context, name string, labels node.Labels) error {
	if err := c.needProtocol(ctx, ownImagesProtocol, "remove networks"); err != nil {
		return err
	}
	_, err := unaryCall[empty](ctx, c, opRemoveNetwork, resourceRequest{Name: name, Labels: toWireLabels(labels)})
	return err
}

// EnsureVolume implements node.Containers.
func (c *Client) EnsureVolume(ctx context.Context, name string, labels node.Labels) error {
	_, err := unaryCall[empty](ctx, c, opEnsureVolume, resourceRequest{Name: name, Labels: toWireLabels(labels)})
	return err
}

// removeVolumeProtocol is the first node protocol that has remove-volume
// (ADR 0056).
const removeVolumeProtocol = 20

// RemoveVolume implements node.Containers. An older node doesn't have the
// operation: its volumes stay.
func (c *Client) RemoveVolume(ctx context.Context, name string, labels node.Labels) error {
	if err := c.needProtocol(ctx, removeVolumeProtocol, "remove volumes"); err != nil {
		return err
	}
	_, err := unaryCall[empty](ctx, c, opRemoveVolume, resourceRequest{Name: name, Labels: toWireLabels(labels)})
	return err
}

// SidecarProtocol is the first node protocol that runs sidecars: a
// Postgres database's WAL archiver (ADR 0093).
const SidecarProtocol = 33

// EnsureSidecar implements node.Containers.
func (c *Client) EnsureSidecar(ctx context.Context, spec node.SidecarSpec) error {
	if err := c.needProtocol(ctx, SidecarProtocol, "archive a database's WAL for point-in-time restore"); err != nil {
		return err
	}
	_, err := unaryCall[empty](ctx, c, opEnsureSidecar, toSidecarRequest(spec))
	return err
}

// RemoveSidecar implements node.Containers. An older node has none to
// remove.
func (c *Client) RemoveSidecar(ctx context.Context, name string, labels node.Labels) error {
	if c.needProtocol(ctx, SidecarProtocol, "remove sidecars") != nil {
		return nil
	}
	_, err := unaryCall[empty](ctx, c, opRemoveSidecar, resourceRequest{Name: name, Labels: toWireLabels(labels)})
	return err
}

// ExecFormProtocol is the first node protocol that starts a container
// with args instead of a shell command (ADR 0083).
const ExecFormProtocol = 29

// Start implements node.Containers. An older node would run the image's
// own command instead of args, so a spec with args needs a newer one.
func (c *Client) Start(ctx context.Context, spec node.ContainerSpec) (node.Container, error) {
	if len(spec.Args) > 0 {
		if err := c.needProtocol(ctx, ExecFormProtocol, "run a start command in exec form"); err != nil {
			return node.Container{}, err
		}
	}
	r, err := unaryCall[containerResult](ctx, c, opStart, startRequest{Spec: toWireSpec(spec)})
	return r.Container.node(), err
}

// Stop implements node.Containers.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	_, err := callWithLogs[empty](ctx, c, opStop, timeout+c.hub.callTimeout(), stopRequest{ID: id, TimeoutMS: timeout.Milliseconds()}, nil, nil)
	return err
}

// healthProtocol is the first protocol with restart and the ongoing
// health check (ADR 0043).
const healthProtocol = 13

// Restart implements node.Containers. Older nodes can't restart a
// container in place.
func (c *Client) Restart(ctx context.Context, id string, timeout time.Duration) error {
	if err := c.needProtocol(ctx, healthProtocol, "restart a container"); err != nil {
		return err
	}
	_, err := callWithLogs[empty](ctx, c, opRestart, timeout+c.hub.callTimeout(), stopRequest{ID: id, TimeoutMS: timeout.Milliseconds()}, nil, nil)
	return err
}

// SleepProtocol is the first node protocol whose environments can sleep:
// resume, open-requests, the routes' wake check and wake events (ADR
// 0070).
const SleepProtocol = 22

// Resume implements node.Containers.
func (c *Client) Resume(ctx context.Context, id string) error {
	if err := c.needProtocol(ctx, SleepProtocol, "wake a sleeping environment"); err != nil {
		return err
	}
	_, err := unaryCall[empty](ctx, c, opResume, containerRequest{ID: id})
	return err
}

// Remove implements node.Containers.
func (c *Client) Remove(ctx context.Context, id string, opts node.RemoveOptions) error {
	_, err := unaryCall[empty](ctx, c, opRemove, removeRequest{ID: id, DropAnonymousVolumes: !opts.KeepAnonymousVolumes})
	return err
}

// Inspect implements node.Containers.
func (c *Client) Inspect(ctx context.Context, id string) (node.Container, error) {
	r, err := unaryCall[containerResult](ctx, c, opInspect, containerRequest{ID: id})
	return r.Container.node(), err
}

// List implements node.Containers.
func (c *Client) List(ctx context.Context) ([]node.Container, error) {
	r, err := unaryCall[containersResult](ctx, c, opList, empty{})
	if err != nil {
		return nil, err
	}
	return fromWireContainers(r.Containers), nil
}

// ReleaseContainers implements node.Containers.
func (c *Client) ReleaseContainers(ctx context.Context, want node.Labels) ([]string, error) {
	id, err := strconv.ParseInt(want.Release, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("release containers: invalid release %q", want.Release)
	}
	w := toWireLabels(want)
	r, err := unaryCall[idsResult](ctx, c, opReleaseCtrs, releaseRequest{ReleaseID: id, Labels: &w})
	return r.IDs, err
}

// jobProtocol is the first node protocol that serves run-job.
const jobProtocol = 2

// MoveSyncProtocol is the first protocol whose nodes mount dock-sync in
// jobs (JobSpec.Sync), hold routes and forward connections: what a move
// without downtime needs of both servers (ADR 0063).
const MoveSyncProtocol = 21

// SnapshotProtocol is the first protocol whose nodes pause containers
// while a job runs (JobSpec.Pause), as snapshots need (ADR 0047).
const SnapshotProtocol = 15

// RunJob implements node.Containers. It blocks on the node until the job
// ends, with no deadline of its own: a backup takes as long as its data
// does, and ending the call force-removes the job's container. A node too
// old to run jobs is an error saying so.
// A job with a Stream is its own full-duplex call (streamJob).
func (c *Client) RunJob(ctx context.Context, spec node.JobSpec) (node.JobResult, error) {
	if spec.Stream != nil {
		return c.streamJob(ctx, spec)
	}
	if spec.Stdin != nil {
		return node.JobResult{}, errors.New("jobs with input run on the control plane's own server only")
	}
	if err := c.needProtocol(ctx, jobProtocol, "run backup jobs"); err != nil {
		return node.JobResult{}, err
	}
	if spec.Dock {
		if err := c.needProtocol(ctx, SidecarProtocol, "back up and restore a database for point-in-time restore"); err != nil {
			return node.JobResult{}, err
		}
	}
	if spec.Sync {
		if err := c.needProtocol(ctx, MoveSyncProtocol, "copy volumes while their service runs"); err != nil {
			return node.JobResult{}, err
		}
	}
	// An older node would ignore pause and copy volumes in use.
	if len(spec.Pause) > 0 {
		if err := c.needProtocol(ctx, SnapshotProtocol, "pause services for a snapshot"); err != nil {
			return node.JobResult{}, err
		}
	}
	r, err := callWithLogs[jobResult](ctx, c, opRunJob, 0, toJobRequest(spec), nil, nil)
	return node.JobResult{ExitCode: r.ExitCode, Output: r.Output}, err
}

// HealthCheck implements node.Containers. It blocks on the node, where the
// container's port is reachable.
func (c *Client) HealthCheck(ctx context.Context, ct node.Container, hc node.HealthCheck) error {
	_, err := callWithLogs[empty](ctx, c, opHealthCheck, hc.Timeout+hc.Grace+c.hub.callTimeout(),
		healthRequest{Container: toWireContainer(ct), Check: toWireHealthCheck(hc)}, nil, nil)
	return err
}

// Logs implements node.Containers. The channel also closes when the
// connection drops; the caller can't tell that from the end of the logs.
func (c *Client) Logs(ctx context.Context, id string, opts node.LogOptions) (<-chan node.LogLine, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	req := logsRequest{ID: id, Follow: opts.Follow, Tail: opts.Tail, Since: opts.Since}
	return streamOn(ctx, s, opLogs, req, wireLogLine.node)
}

// LogsProtocol is the first protocol whose nodes keep a removed
// container's output: archive-logs, archived-logs and prune-logs (ADR
// 0046).
const LogsProtocol = 14

// archiveLogsTimeout bounds ArchiveLogs: a container's log is at most
// Docker's 30 MB, which takes well under a second to compress.
const archiveLogsTimeout = time.Minute

// ArchiveLogs implements node.Containers. An older node can't: the error
// says so, and core keeps the release's last lines itself.
func (c *Client) ArchiveLogs(ctx context.Context, id string, limits node.LogLimits) (node.LogArchive, error) {
	if err := c.needProtocol(ctx, LogsProtocol, "keep the logs of removed containers"); err != nil {
		return node.LogArchive{}, err
	}
	req := archiveLogsRequest{ID: id, MaxBytes: limits.MaxBytes, MaxAgeMS: limits.MaxAge.Milliseconds(), MaxTotalBytes: limits.MaxTotalBytes}
	r, err := callWithLogs[archiveLogsResult](ctx, c, opArchiveLogs, archiveLogsTimeout, req, nil, nil)
	return node.LogArchive(r), err
}

// ArchivedLogs implements node.Containers, streamed like Logs.
func (c *Client) ArchivedLogs(ctx context.Context, q node.LogArchiveQuery) (<-chan node.LogLine, error) {
	if err := c.needProtocol(ctx, LogsProtocol, "keep the logs of removed containers"); err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	req := archivedLogsRequest{Service: q.Service, Release: q.Release, Tail: q.Tail, Since: q.Since}
	return streamOn(ctx, s, opArchivedLogs, req, wireLogLine.node)
}

// PruneLogs implements node.Containers.
func (c *Client) PruneLogs(ctx context.Context, p node.LogPrune) (node.PruneResult, error) {
	if err := c.needProtocol(ctx, LogsProtocol, "keep the logs of removed containers"); err != nil {
		return node.PruneResult{}, err
	}
	req := pruneLogsRequest{KeepServices: p.KeepServices, MaxBytes: p.Limits.MaxBytes, MaxAgeMS: p.Limits.MaxAge.Milliseconds(), MaxTotalBytes: p.Limits.MaxTotalBytes}
	r, err := unaryCall[pruneResult](ctx, c, opPruneLogs, req)
	return node.PruneResult(r), err
}

// Events implements node.Containers. It follows the server across
// reconnects, so the channel only closes when ctx ends. Events that happen
// while the server is offline are lost: whenever that may have happened,
// the next subscription starts with an ActionResync event.
func (c *Client) Events(ctx context.Context) (<-chan node.ContainerEvent, error) {
	out := make(chan node.ContainerEvent, 64)
	gap := !c.Online()
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			s, err := c.hub.wait(ctx, c.id)
			if err != nil {
				return
			}
			if ch, err := streamOn(ctx, s, opEvents, empty{}, wireEvent.node); err == nil {
				// The stream is open on the node: everything from here on
				// arrives, so a reconcile now misses nothing.
				if gap && !send(ctx, out, node.ContainerEvent{Action: ActionResync, Time: time.Now()}) {
					return
				}
				for ev := range ch {
					if ev.Action == ActionResync {
						// Only this client says when events were missed; a
						// node sending it would make core re-read the
						// server on every event.
						continue
					}
					if !send(ctx, out, ev) {
						return
					}
				}
			}
			gap = true
			// Either the connection is going away (wait for the next one)
			// or the node's own event stream failed (retry shortly).
			select {
			case <-s.done:
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
}

// send delivers v unless ctx ends first.
func send[T any](ctx context.Context, ch chan<- T, v T) bool {
	select {
	case ch <- v:
		return true
	case <-ctx.Done():
		return false
	}
}

// Build implements node.Builder. The build context streams up while build
// output streams back, on the same call.
func (c *Client) Build(ctx context.Context, req node.BuildRequest) (node.Built, error) {
	args := buildRequest{
		Dockerfile: req.Dockerfile, BuildCommand: req.BuildCommand, StartCommand: req.StartCommand,
		BuildArgs: req.BuildArgs, CacheScope: req.CacheScope, Tag: req.Tag, Push: toWirePushTarget(req.Push),
	}
	// No deadline of its own: core bounds builds ([buildkit] timeout_minutes).
	r, err := callWithLogs[buildResult](ctx, c, opBuild, 0, args, req.Source, req.Logs)
	return node.Built{ID: r.ImageID, Base: r.Base, BaseDigest: r.BaseDigest}, err
}

// AccessProtocol is the first protocol whose routes carry access
// protection (ADR 0030). An older node would serve protected routes to
// everyone, so it isn't sent them.
const AccessProtocol = 5

// OpenRequests implements node.Proxy.
func (c *Client) OpenRequests(ctx context.Context, upstreams []string) (int, error) {
	if err := c.needProtocol(ctx, SleepProtocol, "put an environment to sleep"); err != nil {
		return 0, err
	}
	r, err := unaryCall[openRequestsResult](ctx, c, opOpenRequests, openRequestsRequest{Upstreams: upstreams})
	return r.Open, err
}

// WildcardProtocol is the first node protocol that loads the certificates
// apply-routes carries (ADR 0090).
const WildcardProtocol = 31

// ApplyRoutes implements node.Proxy. An older node gets no certificates:
// it keeps issuing on demand, and a key never goes where it isn't used.
func (c *Client) ApplyRoutes(ctx context.Context, routes []node.Route, certs []node.Certificate) error {
	if len(certs) > 0 {
		if s, err := c.session(ctx); err != nil || s.hello.Protocol < WildcardProtocol {
			certs = nil
		}
	}
	if slices.ContainsFunc(routes, func(r node.Route) bool { return r.Access != nil }) {
		if err := c.needProtocol(ctx, AccessProtocol, "protect a service's domains"); err != nil {
			return err
		}
	}
	if slices.ContainsFunc(routes, func(r node.Route) bool { return r.TunnelOnly }) {
		s, err := c.session(ctx)
		if err != nil {
			return err
		}
		if s.hello.Protocol < TunnelListenerProtocol {
			// It would serve them on its public IP: leave them out. It can't
			// run a tunnel for them anyway.
			routes = slices.DeleteFunc(slices.Clone(routes), func(r node.Route) bool { return r.TunnelOnly })
		}
	}
	if slices.ContainsFunc(routes, func(r node.Route) bool { return len(r.Analytics) > 0 }) {
		if s, err := c.session(ctx); err == nil && s.hello.Protocol < AnalyticsProtocol {
			// An older node would route events to an ingest it doesn't
			// have; its sites go uncounted until it is upgraded.
			routes = slices.Clone(routes)
			for i := range routes {
				routes[i].Analytics = nil
			}
		}
	}
	_, err := unaryCall[empty](ctx, c, opApplyRoutes, routesRequest{Routes: toWireRoutes(routes), Certificates: toWireCertificates(certs)})
	return err
}

// TunnelListenerProtocol is the first protocol whose direct nodes run a
// tunnel listener and know tunnel-only routes (ADR 0036).
const TunnelListenerProtocol = 9

// CheckDomain implements node.Proxy.
func (c *Client) CheckDomain(ctx context.Context, hostname string) (node.DomainCheck, error) {
	r, err := unaryCall[wireDomainCheck](ctx, c, opCheckDomain, domainRequest{Hostname: hostname})
	return r.node(), err
}

// PruneBuildCache implements node.Builder.
func (c *Client) PruneBuildCache(ctx context.Context, keepBytes int64) (node.PruneResult, error) {
	r, err := callWithLogs[pruneResult](ctx, c, opPruneCache, pruneTimeout, pruneCacheRequest{KeepBytes: keepBytes}, nil, nil)
	return node.PruneResult(r), err
}

// WarmProtocol is the first protocol with warm-builder.
const WarmProtocol = 32

// warmTimeout bounds WarmBuilder: Railpack's images are about 460 MB.
const warmTimeout = 30 * time.Minute

// WarmBuilder implements node.Builder. An older node isn't asked: its
// first Railpack build fetches the images, as before.
func (c *Client) WarmBuilder(ctx context.Context) error {
	if err := c.needProtocol(ctx, WarmProtocol, "fetch build images ahead"); err != nil {
		return nil
	}
	_, err := callWithLogs[empty](ctx, c, opWarmBuilder, warmTimeout, empty{}, nil, nil)
	return err
}

// HostStats implements node.Stats.
func (c *Client) HostStats(ctx context.Context) (node.HostStats, error) {
	r, err := unaryCall[wireHostStats](ctx, c, opHostStats, empty{})
	return r.node(), err
}

// ContainerStats implements node.Stats. One call for all containers.
func (c *Client) ContainerStats(ctx context.Context, ids []string) ([]node.ContainerStats, error) {
	r, err := unaryCall[containerStatsResult](ctx, c, opCtrStats, containerStatsRequest{IDs: ids})
	if err != nil {
		return nil, err
	}
	return fromWireContainerStats(r.Stats), nil
}

// TrafficProtocol is the first protocol whose nodes count traffic (ADR
// 0033).
const TrafficProtocol = 6

// DrainTraffic implements node.Traffic. An older node counts nothing, so
// it isn't asked: it has no traffic to report.
func (c *Client) DrainTraffic(ctx context.Context) ([]node.HostTraffic, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	if s.hello.Protocol < TrafficProtocol {
		return nil, nil
	}
	r, err := unaryCall[trafficResult](ctx, c, opDrainTraffic, empty{})
	if err != nil {
		return nil, err
	}
	return fromWireTraffic(r.Hosts), nil
}

// DiskProtocol is the first protocol whose nodes report disk usage (ADR
// 0035).
const DiskProtocol = 7

// diskUsageTimeout bounds DiskUsage: Docker sizes every image and volume,
// which takes seconds on a busy server, more than a control call may.
const diskUsageTimeout = 2 * time.Minute

// DiskUsage implements node.Containers.
func (c *Client) DiskUsage(ctx context.Context, req node.PruneImagesRequest) (node.DiskUsage, error) {
	if err := c.needProtocol(ctx, DiskProtocol, "report its disk usage"); err != nil {
		return node.DiskUsage{}, err
	}
	r, err := callWithLogs[wireDiskUsage](ctx, c, opDiskUsage, diskUsageTimeout, pruneImagesRequest{Keep: req.Keep, Repos: req.Repos}, nil, nil)
	return fromWireDiskUsage(r), err
}

// TunnelProtocol is the first protocol version with ensure-tunnel.
const TunnelProtocol = 8

// EnsureTunnel implements node.Tunnel. An older node can't run
// cloudflared for the control plane: that is an error saying to upgrade
// it.
func (c *Client) EnsureTunnel(ctx context.Context, spec *node.TunnelSpec) error {
	if err := c.needProtocol(ctx, TunnelProtocol, "run a Cloudflare tunnel for the control plane"); err != nil {
		return err
	}
	req := tunnelRequest{}
	if spec != nil {
		req.Spec = &wireTunnelSpec{Token: spec.Token, Hostname: spec.Hostname}
	}
	// Longer than a control call: the node may pull cloudflared first.
	_, err := callWithLogs[empty](ctx, c, opEnsureTunnel, ensureTunnelTimeout, req, nil, nil)
	return err
}

// ensureTunnelTimeout bounds ensure-tunnel, which may pull an image.
const ensureTunnelTimeout = 5 * time.Minute

// unaryCall makes a control call, bounded by the hub's call timeout.
func unaryCall[R any](ctx context.Context, c *Client, op string, args any) (R, error) {
	return callWithLogs[R](ctx, c, op, c.hub.callTimeout(), args, nil, nil)
}

// callWithLogs makes a call that may upload body and writes progress to
// logs before its result. A positive timeout bounds it (unless ctx ends
// sooner), so a node that accepts calls but never answers can't hold the
// caller (a global loop, a deploy) forever.
func callWithLogs[R any](ctx context.Context, c *Client, op string, timeout time.Duration, args any, body io.Reader, logs io.Writer) (R, error) {
	var r R
	s, err := c.session(ctx)
	if err != nil {
		return r, err
	}
	var onLog func(string)
	if logs != nil {
		onLog = func(l string) { _, _ = io.WriteString(logs, l) }
	}
	callCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	err = call(callCtx, s, op, args, body, onLog, nil, &r)
	if err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("server %s didn't answer %s within %s: %w", s.name(), op, timeout, context.DeadlineExceeded)
	}
	return r, err
}

// streamOn opens a stream and forwards its items, converted by conv, until
// it ends, the connection drops or ctx ends.
func streamOn[W, T any](ctx context.Context, s *session, op string, args any, conv func(W) T) (<-chan T, error) {
	body, err := open(ctx, s, op, args, nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan T)
	go func() {
		defer close(ch)
		_ = read(ctx, s, body, nil, func(raw json.RawMessage) error {
			var w W
			if err := json.Unmarshal(raw, &w); err != nil {
				return err
			}
			if !send(ctx, ch, conv(w)) {
				return ctx.Err()
			}
			return nil
		}, nil)
	}()
	return ch, nil
}

// call makes one call on s and reads it to the end: log frames go to
// onLog, items to onItem and the result into result.
func call(ctx context.Context, s *session, op string, args any, body io.Reader, onLog func(string), onItem func(json.RawMessage) error, result any) error {
	if body != nil {
		var release func()
		body, release = cancelable(ctx, body)
		defer release()
	}
	frames, err := open(ctx, s, op, args, body)
	if err != nil {
		return err
	}
	return read(ctx, s, frames, onLog, onItem, result)
}

// open sends a call and returns the response's frames once the node
// accepted it. The request body is the JSON args line, then body if any.
func open(ctx context.Context, s *session, op string, args any, body io.Reader) (io.ReadCloser, error) {
	line, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var r io.Reader = bytes.NewReader(append(line, '\n'))
	if body != nil {
		r = io.MultiReader(r, body)
		if c, ok := body.(io.Closer); ok {
			// The transport closes the body when the call ends early (an
			// error status, a reset), which unblocks a pending Read. Closing
			// the response waits for that.
			r = struct {
				io.Reader
				io.Closer
			}{r, c}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://node/v1/"+op, r)
	if err != nil {
		return nil, err
	}
	resp, err := s.cc.RoundTrip(req)
	if err != nil {
		return nil, offline(ctx, s.name(), err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage))
		if err != nil {
			return nil, offline(ctx, s.name(), err)
		}
		var e Error
		if json.Unmarshal(raw, &e) != nil || e.Code == "" {
			// An op the node doesn't know (an older node) is a plain 404.
			return nil, fmt.Errorf("server %s: %s: status %d", s.name(), op, resp.StatusCode)
		}
		e.Message = truncate(e.Message, maxErrorMessage)
		return nil, &e
	}
	return resp.Body, nil
}

// read consumes response frames up to and including the end frame.
func read(ctx context.Context, s *session, frames io.ReadCloser, onLog func(string), onItem func(json.RawMessage) error, result any) error {
	defer func() { _ = frames.Close() }()
	lr := newLineReader(frames)
	for {
		line, err := lr.next()
		if errors.Is(err, errTooLarge) {
			// A hostile or broken node; closing the body resets the call.
			return fmt.Errorf("server %s: %w", s.name(), err)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errNoEnd
			}
			return offline(ctx, s.name(), err)
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			return fmt.Errorf("server %s: bad frame: %w", s.name(), err)
		}
		switch {
		case f.End && f.Error != nil:
			return f.Error
		case f.End:
			if result == nil {
				return nil
			}
			return json.Unmarshal(f.Result, result)
		case f.Item != nil && onItem != nil:
			if err := onItem(f.Item); err != nil {
				return err
			}
		case f.Log != "" && onLog != nil:
			onLog(f.Log)
		}
	}
}

// cancelable makes Read return when ctx ends even if src is blocked in Read
// (a stalled upload). net/http only checks the request's ctx between body
// reads, so without this a canceled Build could hang. release stops the
// copy when the call is over; the copying goroutine then lives until src
// returns from its current Read.
func cancelable(ctx context.Context, src io.Reader) (r io.Reader, release func()) {
	pr, pw := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = pr.CloseWithError(ctx.Err()) })
	go func() {
		_, err := io.Copy(pw, src)
		_ = pw.CloseWithError(err)
	}()
	return pr, func() {
		stop()
		_ = pr.Close()
	}
}

// AnalyticsProtocol is the first protocol whose nodes collect analytics
// (ADR 0037).
const AnalyticsProtocol = 10

// AnalyticsBinaryProtocol is the first protocol whose nodes run the
// store from the release's binary (ADR 0037); a node on 10 can't run it.
const AnalyticsBinaryProtocol = 11

// TraceStoreProtocol is the first protocol whose nodes run the trace
// store beside the analytics store: the store kind on its calls.
const TraceStoreProtocol = 24

// TraceIngestProtocol is the first protocol whose nodes take spans: the
// trace ingest, its key in set-analytics and the spans spool.
const TraceIngestProtocol = 25

// MetricsProtocol is the first protocol whose nodes take app metrics:
// the metrics spool (ADR 0075, phase 6).
const MetricsProtocol = 28

// TraceAppsProtocol is the first protocol whose nodes let apps reach the
// trace ingest: the container spec's extra hosts and Caddy's dock-otlp
// route. The control plane gives apps on older nodes no OTEL_* variables.
const TraceAppsProtocol = 26

// needStoreKind checks the node can act on the store of kind: any
// analytics node for the analytics store, TraceStoreProtocol for the
// trace store (an older node would take the call for the analytics
// store's).
func (c *Client) needStoreKind(ctx context.Context, kind node.StoreKind, min int, what string) error {
	if kind == node.StoreTraces {
		return c.needProtocol(ctx, TraceStoreProtocol, what+" (the trace store)")
	}
	return c.needProtocol(ctx, min, what)
}

// ReplayProtocol is the first protocol whose nodes record session
// replays: they have a replay spool (ADR 0037, A3).
const ReplayProtocol = 12

// spoolProtocol is the protocol a spool needs.
func spoolProtocol(spool node.Spool) (int, string) {
	switch spool {
	case node.SpoolReplay:
		return ReplayProtocol, "record session replays"
	case node.SpoolSpans:
		return TraceIngestProtocol, "take spans for tracing"
	case node.SpoolMetrics:
		return MetricsProtocol, "take app metrics"
	}
	return AnalyticsProtocol, "collect analytics"
}

// analyticsStoreTimeout bounds EnsureAnalyticsStore, which may pull the
// store's base image, and sending the binary (~70 MB).
const analyticsStoreTimeout = 10 * time.Minute

// SetAnalytics implements node.Analytics.
func (c *Client) SetAnalytics(ctx context.Context, cfg node.AnalyticsConfig) error {
	if err := c.needProtocol(ctx, AnalyticsProtocol, "collect analytics"); err != nil {
		return err
	}
	req := setAnalyticsRequest{Salts: cfg.Salts, TraceKey: cfg.TraceKey}
	if cfg.Sites != nil {
		req.Sites = make(map[int64]wireSiteRules, len(cfg.Sites))
		for id, r := range cfg.Sites {
			req.Sites[id] = wireSiteRules(r)
		}
	}
	_, err := unaryCall[empty](ctx, c, opSetAnalytics, req)
	return err
}

// TailSpool implements node.Analytics. The channel closes when the
// stream ends for any reason; the caller reads the store's cursor and
// tails again.
func (c *Client) TailSpool(ctx context.Context, spool node.Spool, after uint64) (<-chan node.SpoolBatch, error) {
	v, what := spoolProtocol(spool)
	if err := c.needProtocol(ctx, v, what); err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	return streamOn(ctx, s, opTailSpool, tailSpoolRequest{After: after, Spool: string(spool)}, wireSpoolBatch.node)
}

// AckSpool implements node.Analytics.
func (c *Client) AckSpool(ctx context.Context, spool node.Spool, position uint64, events int) error {
	v, what := spoolProtocol(spool)
	if err := c.needProtocol(ctx, v, what); err != nil {
		return err
	}
	_, err := unaryCall[empty](ctx, c, opAckSpool, ackSpoolRequest{Position: position, Events: events, Spool: string(spool)})
	return err
}

// EnsureAnalyticsStore implements node.Analytics. The node runs the
// binary it has with spec's hash; one it doesn't have is sent first, from
// spec.Binary on this server, once per release.
func (c *Client) EnsureAnalyticsStore(ctx context.Context, spec node.AnalyticsStoreSpec) error {
	if err := c.needStoreKind(ctx, spec.Kind, AnalyticsBinaryProtocol, "run the analytics store"); err != nil {
		return err
	}
	req := analyticsStoreRequest{Spec: wireAnalyticsStoreSpec{SHA256: spec.SHA256, Token: spec.Token, MemoryBytes: spec.MemoryBytes, Kind: string(spec.Kind)}}
	_, err := callWithLogs[empty](ctx, c, opRunAnalytics, analyticsStoreTimeout, req, nil, io.Discard)
	if !errors.Is(err, errNoAnalyticsBinary) {
		return err
	}
	if err := c.putAnalyticsBinary(ctx, spec.Binary, spec.SHA256); err != nil {
		return err
	}
	_, err = callWithLogs[empty](ctx, c, opRunAnalytics, analyticsStoreTimeout, req, nil, io.Discard)
	return err
}

// putAnalyticsBinary sends the store's binary at path to the node, which
// checks it against sum.
func (c *Client) putAnalyticsBinary(ctx context.Context, path, sum string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the analytics store's binary: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = callWithLogs[empty](ctx, c, opPutAnalyticsBinary, analyticsStoreTimeout, putAnalyticsBinaryRequest{SHA256: sum, Size: st.Size()}, f, nil)
	return err
}

// RemoveAnalyticsStore implements node.Analytics: the node removes the
// container of kind and its data, and for the analytics store the
// binaries it received.
func (c *Client) RemoveAnalyticsStore(ctx context.Context, kind node.StoreKind) error {
	if err := c.needStoreKind(ctx, kind, AnalyticsBinaryProtocol, "remove the analytics store"); err != nil {
		return err
	}
	_, err := callWithLogs[empty](ctx, c, opRemoveAnalytics, analyticsStoreTimeout, storeKindRequest{Kind: string(kind)}, nil, nil)
	return err
}

// DialAnalyticsStore implements node.Analytics: a raw connection to the
// store's port on the node, over the link like Dial.
func (c *Client) DialAnalyticsStore(ctx context.Context, kind node.StoreKind) (net.Conn, error) {
	if err := c.needStoreKind(ctx, kind, AnalyticsProtocol, "run the analytics store"); err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	if s.dials.Add(1) > maxDials {
		s.dials.Add(-1)
		return nil, fmt.Errorf("server %s: %w", s.name(), errTooManyDials)
	}
	lc, err := openConn(ctx, s, opDialAnalytics, storeKindRequest{Kind: string(kind)}, s.name()+"/analytics", func() { s.dials.Add(-1) })
	if err != nil {
		s.dials.Add(-1)
		return nil, err
	}
	return lc, nil
}

// ProbeProtocol is the first protocol whose nodes run uptime probes (ADR
// 0055). Core doesn't pick older nodes to probe.
const ProbeProtocol = 19

// Probe implements node.Prober. The call may take the probe's timeout,
// plus the link's own time. A probe with a session cookie is refused:
// those stay on the control plane (node.ProbeRequest.Cookie).
func (c *Client) Probe(ctx context.Context, req node.ProbeRequest) (node.ProbeResult, error) {
	if req.Cookie != "" {
		return node.ProbeResult{}, errors.New("a probe of a protected domain runs on the control plane only")
	}
	if err := c.needProtocol(ctx, ProbeProtocol, "run uptime checks"); err != nil {
		return node.ProbeResult{}, err
	}
	args := probeRequest{URL: req.URL, ExpectStatus: req.ExpectStatus, TimeoutMS: req.Timeout.Milliseconds()}
	r, err := callWithLogs[probeResult](ctx, c, opProbe, req.Timeout+c.hub.callTimeout(), args, nil, nil)
	return node.ProbeResult{Up: r.Up, Status: r.Status, Latency: time.Duration(r.LatencyMS) * time.Millisecond, Error: r.Error}, err
}
