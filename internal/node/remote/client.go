package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// PruneImages implements node.Containers. A node older than the
// operation answers with an error, so it is simply not pruned.
func (c *Client) PruneImages(ctx context.Context, keep []string) (node.PruneResult, error) {
	r, err := unaryCall[pruneResult](ctx, c, opPruneImages, pruneImagesRequest{Keep: keep})
	return node.PruneResult(r), err
}

// EnsureNetwork implements node.Containers.
func (c *Client) EnsureNetwork(ctx context.Context, name string, labels node.Labels) error {
	_, err := unaryCall[empty](ctx, c, opEnsureNetwork, resourceRequest{Name: name, Labels: toWireLabels(labels)})
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
	s, err := c.session(ctx)
	if err != nil {
		return node.JobResult{}, err
	}
	if s.hello.Protocol < jobProtocol {
		return node.JobResult{}, fmt.Errorf("server %s runs dock %s, which can't run backup jobs; upgrade dock on it", s.name, s.hello.Version)
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

// ApplyRoutes implements node.Proxy.
func (c *Client) ApplyRoutes(ctx context.Context, routes []node.Route) error {
	_, err := unaryCall[empty](ctx, c, opApplyRoutes, routesRequest{Routes: toWireRoutes(routes)})
	return err
}

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
