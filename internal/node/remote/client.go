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
// Stop and HealthCheck that plus their own timeout. Logs and Events
// streams have none: a quiet stream is normal, a dead link is caught by
// the keepalive, and their caller's ctx ends them.
const (
	DefaultCallTimeout = 30 * time.Second
	// pullTimeout bounds a pull. Its progress isn't byte level, so a big
	// layer can be silent for minutes; this only stops a node that never
	// finishes.
	pullTimeout = 30 * time.Minute
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
		return nil, fmt.Errorf("server %s: %w", s.name, ErrIncompatible)
	}
	return s, nil
}

// Pull implements node.Containers. Progress arrives as log frames.
func (c *Client) Pull(ctx context.Context, ref string, logs io.Writer, opts ...node.PullOptions) (string, error) {
	req := pullRequest{Ref: ref}
	if len(opts) > 0 {
		req.Token = opts[0].Token
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
		return fmt.Errorf("server %s runs dock %s, which can't %s; upgrade dock on it", s.name, s.hello.Version, what)
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
	r, err := unaryCall[pruneResult](ctx, c, opPruneImages, pruneImagesRequest{Keep: req.Keep, Repos: req.Repos})
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

// Start implements node.Containers.
func (c *Client) Start(ctx context.Context, spec node.ContainerSpec) (node.Container, error) {
	r, err := unaryCall[containerResult](ctx, c, opStart, startRequest{Spec: toWireSpec(spec)})
	return r.Container.node(), err
}

// Stop implements node.Containers.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	_, err := callWithLogs[empty](ctx, c, opStop, timeout+c.hub.callTimeout(), stopRequest{ID: id, TimeoutMS: timeout.Milliseconds()}, nil, nil)
	return err
}

// Remove implements node.Containers.
func (c *Client) Remove(ctx context.Context, id string) error {
	_, err := unaryCall[empty](ctx, c, opRemove, containerRequest{ID: id})
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

// RunJob implements node.Containers. It blocks on the node until the job
// ends; a node too old to run jobs is an error saying so.
func (c *Client) RunJob(ctx context.Context, spec node.JobSpec) (node.JobResult, error) {
	if spec.Stdin != nil {
		return node.JobResult{}, errors.New("jobs with input run on the control plane's own server only")
	}
	if err := c.needProtocol(ctx, jobProtocol, "run backup jobs"); err != nil {
		return node.JobResult{}, err
	}
	r, err := unaryCall[jobResult](ctx, c, opRunJob, toJobRequest(spec))
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
func (c *Client) Build(ctx context.Context, req node.BuildRequest) (string, error) {
	args := buildRequest{
		Dockerfile: req.Dockerfile, BuildCommand: req.BuildCommand, StartCommand: req.StartCommand,
		BuildArgs: req.BuildArgs, CacheScope: req.CacheScope, Tag: req.Tag, Push: toWirePushTarget(req.Push),
	}
	// No deadline of its own: core bounds builds ([buildkit] timeout_minutes).
	r, err := callWithLogs[imageIDResult](ctx, c, opBuild, 0, args, req.Source, req.Logs)
	return r.ImageID, err
}

// AccessProtocol is the first protocol whose routes carry access
// protection (ADR 0030). An older node would serve protected routes to
// everyone, so it isn't sent them.
const AccessProtocol = 5

// ApplyRoutes implements node.Proxy.
func (c *Client) ApplyRoutes(ctx context.Context, routes []node.Route) error {
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
	_, err := unaryCall[empty](ctx, c, opApplyRoutes, routesRequest{Routes: toWireRoutes(routes)})
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
	r, err := unaryCall[pruneResult](ctx, c, opPruneCache, pruneCacheRequest{KeepBytes: keepBytes})
	return node.PruneResult(r), err
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
		err = fmt.Errorf("server %s didn't answer %s within %s: %w", s.name, op, timeout, context.DeadlineExceeded)
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
		return nil, offline(ctx, s.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage))
		if err != nil {
			return nil, offline(ctx, s.name, err)
		}
		var e Error
		if json.Unmarshal(raw, &e) != nil || e.Code == "" {
			// An op the node doesn't know (an older node) is a plain 404.
			return nil, fmt.Errorf("server %s: %s: status %d", s.name, op, resp.StatusCode)
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
			return fmt.Errorf("server %s: %w", s.name, err)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errNoEnd
			}
			return offline(ctx, s.name, err)
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			return fmt.Errorf("server %s: bad frame: %w", s.name, err)
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

// analyticsStoreTimeout bounds EnsureAnalyticsStore, which may pull the
// store's base image, and sending the binary (~70 MB).
const analyticsStoreTimeout = 10 * time.Minute

// SetAnalytics implements node.Analytics.
func (c *Client) SetAnalytics(ctx context.Context, cfg node.AnalyticsConfig) error {
	if err := c.needProtocol(ctx, AnalyticsProtocol, "collect analytics"); err != nil {
		return err
	}
	req := setAnalyticsRequest{Salts: cfg.Salts}
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
func (c *Client) TailSpool(ctx context.Context, after uint64) (<-chan node.SpoolBatch, error) {
	if err := c.needProtocol(ctx, AnalyticsProtocol, "collect analytics"); err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	return streamOn(ctx, s, opTailSpool, tailSpoolRequest{After: after}, wireSpoolBatch.node)
}

// AckSpool implements node.Analytics.
func (c *Client) AckSpool(ctx context.Context, position uint64, events int) error {
	if err := c.needProtocol(ctx, AnalyticsProtocol, "collect analytics"); err != nil {
		return err
	}
	_, err := unaryCall[empty](ctx, c, opAckSpool, ackSpoolRequest{Position: position, Events: events})
	return err
}

// EnsureAnalyticsStore implements node.Analytics. The node runs the
// binary it has with spec's hash; one it doesn't have is sent first, from
// spec.Binary on this server, once per release.
func (c *Client) EnsureAnalyticsStore(ctx context.Context, spec node.AnalyticsStoreSpec) error {
	if err := c.needProtocol(ctx, AnalyticsBinaryProtocol, "run the analytics store"); err != nil {
		return err
	}
	req := analyticsStoreRequest{Spec: wireAnalyticsStoreSpec{SHA256: spec.SHA256, Token: spec.Token, MemoryBytes: spec.MemoryBytes}}
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
// container, its data and the binaries it received.
func (c *Client) RemoveAnalyticsStore(ctx context.Context) error {
	if err := c.needProtocol(ctx, AnalyticsBinaryProtocol, "remove the analytics store"); err != nil {
		return err
	}
	_, err := callWithLogs[empty](ctx, c, opRemoveAnalytics, analyticsStoreTimeout, empty{}, nil, nil)
	return err
}

// DialAnalyticsStore implements node.Analytics: a raw connection to the
// store's port on the node, over the link like Dial.
func (c *Client) DialAnalyticsStore(ctx context.Context) (net.Conn, error) {
	if err := c.needProtocol(ctx, AnalyticsProtocol, "run the analytics store"); err != nil {
		return nil, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	if s.dials.Add(1) > maxDials {
		s.dials.Add(-1)
		return nil, fmt.Errorf("server %s: %w", s.name, errTooManyDials)
	}
	lc, err := openConn(ctx, s, opDialAnalytics, empty{}, s.name+"/analytics", func() { s.dials.Add(-1) })
	if err != nil {
		s.dials.Add(-1)
		return nil, err
	}
	return lc, nil
}
