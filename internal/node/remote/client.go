package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Client is a remote server's node.Runtime on the control plane. Calls go
// over the server's current connection; while it has none they fail with
// ErrOffline. Canceling a call's ctx cancels it on the node too.
type Client struct {
	hub    *Hub
	server string
}

var _ node.Runtime = (*Client)(nil)

var errNoEnd = errors.New("response ended without an end frame")

// Online reports whether the server is connected right now.
func (c *Client) Online() bool { return c.hub.session(c.server) != nil }

func (c *Client) session(ctx context.Context) (*session, error) {
	if s := c.hub.session(c.server); s != nil {
		return s, nil
	}
	return nil, offline(ctx, c.server, nil)
}

// Pull implements node.Containers. Progress arrives as log frames.
func (c *Client) Pull(ctx context.Context, ref string, logs io.Writer) (string, error) {
	return withLogsCall[string](ctx, c, opPull, refArgs{ref}, nil, logs)
}

// HasImage implements node.Containers.
func (c *Client) HasImage(ctx context.Context, ref string) (bool, error) {
	return unaryCall[bool](ctx, c, opHasImage, refArgs{ref})
}

// EnsureNetwork implements node.Containers.
func (c *Client) EnsureNetwork(ctx context.Context, name string, labels node.Labels) error {
	_, err := unaryCall[struct{}](ctx, c, opEnsureNetwork, labeledArgs{name, labels})
	return err
}

// EnsureVolume implements node.Containers.
func (c *Client) EnsureVolume(ctx context.Context, name string, labels node.Labels) error {
	_, err := unaryCall[struct{}](ctx, c, opEnsureVolume, labeledArgs{name, labels})
	return err
}

// Start implements node.Containers.
func (c *Client) Start(ctx context.Context, spec node.ContainerSpec) (node.Container, error) {
	return unaryCall[node.Container](ctx, c, opStart, spec)
}

// Stop implements node.Containers.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	_, err := unaryCall[struct{}](ctx, c, opStop, stopArgs{id, timeout})
	return err
}

// Remove implements node.Containers.
func (c *Client) Remove(ctx context.Context, id string) error {
	_, err := unaryCall[struct{}](ctx, c, opRemove, idArgs{id})
	return err
}

// Inspect implements node.Containers.
func (c *Client) Inspect(ctx context.Context, id string) (node.Container, error) {
	return unaryCall[node.Container](ctx, c, opInspect, idArgs{id})
}

// List implements node.Containers.
func (c *Client) List(ctx context.Context) ([]node.Container, error) {
	return unaryCall[[]node.Container](ctx, c, opList, noArgs{})
}

// HealthCheck implements node.Containers. It blocks on the node, where the
// container's port is reachable.
func (c *Client) HealthCheck(ctx context.Context, ct node.Container, hc node.HealthCheck) error {
	_, err := unaryCall[struct{}](ctx, c, opHealthCheck, healthArgs{ct, hc})
	return err
}

// Logs implements node.Containers. The channel also closes when the
// connection drops; the caller can't tell that from the end of the logs.
func (c *Client) Logs(ctx context.Context, id string, opts node.LogOptions) (<-chan node.LogLine, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	return streamOn[node.LogLine](ctx, s, opLogs, logsArgs{id, opts})
}

// Events implements node.Containers. It follows the server across
// reconnects, so the channel only closes when ctx ends. Events that happen
// while the server is offline are lost; reconcile on reconnect instead.
func (c *Client) Events(ctx context.Context) (<-chan node.ContainerEvent, error) {
	out := make(chan node.ContainerEvent, 64)
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			s, err := c.hub.wait(ctx, c.server)
			if err != nil {
				return
			}
			if ch, err := streamOn[node.ContainerEvent](ctx, s, opEvents, noArgs{}); err == nil {
				for ev := range ch {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
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

// Build implements node.Builder. The build context streams up while build
// output streams back, on the same call.
func (c *Client) Build(ctx context.Context, req node.BuildRequest) (string, error) {
	args := buildArgs{
		Dockerfile: req.Dockerfile, BuildCommand: req.BuildCommand, StartCommand: req.StartCommand,
		BuildArgs: req.BuildArgs, Tag: req.Tag,
	}
	return withLogsCall[string](ctx, c, opBuild, args, req.Source, req.Logs)
}

// ApplyRoutes implements node.Proxy.
func (c *Client) ApplyRoutes(ctx context.Context, routes []node.Route) error {
	_, err := unaryCall[struct{}](ctx, c, opApplyRoutes, routesArgs{routes})
	return err
}

// CheckDomain implements node.Proxy.
func (c *Client) CheckDomain(ctx context.Context, hostname string) (node.DomainCheck, error) {
	return unaryCall[node.DomainCheck](ctx, c, opCheckDomain, nameArgs{hostname})
}

// HostStats implements node.Stats.
func (c *Client) HostStats(ctx context.Context) (node.HostStats, error) {
	return unaryCall[node.HostStats](ctx, c, opHostStats, noArgs{})
}

// ContainerStats implements node.Stats. One call for all containers.
func (c *Client) ContainerStats(ctx context.Context, ids []string) ([]node.ContainerStats, error) {
	return unaryCall[[]node.ContainerStats](ctx, c, opCtrStats, idsArgs{ids})
}

func unaryCall[R any](ctx context.Context, c *Client, op string, args any) (R, error) {
	return withLogsCall[R](ctx, c, op, args, nil, nil)
}

func withLogsCall[R any](ctx context.Context, c *Client, op string, args any, body io.Reader, logs io.Writer) (R, error) {
	var r R
	s, err := c.session(ctx)
	if err != nil {
		return r, err
	}
	var onLog func(string)
	if logs != nil {
		onLog = func(l string) { _, _ = io.WriteString(logs, l) }
	}
	err = call(ctx, s, op, args, body, onLog, nil, &r)
	return r, err
}

// streamOn opens a stream and forwards its items until it ends, the
// connection drops or ctx ends.
func streamOn[T any](ctx context.Context, s *session, op string, args any) (<-chan T, error) {
	body, err := open(ctx, s, op, args, nil)
	if err != nil {
		return nil, err
	}
	ch := make(chan T)
	go func() {
		defer close(ch)
		_ = read(ctx, s, body, nil, func(raw json.RawMessage) error {
			var v T
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			select {
			case ch <- v:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
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
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://node/v1/"+op, r)
	if err != nil {
		return nil, err
	}
	resp, err := s.cc.RoundTrip(req)
	if err != nil {
		return nil, offline(ctx, s.server, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		var e Error
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			return nil, offline(ctx, s.server, err)
		}
		return nil, &e
	}
	return resp.Body, nil
}

// read consumes response frames up to and including the end frame.
func read(ctx context.Context, s *session, frames io.ReadCloser, onLog func(string), onItem func(json.RawMessage) error, result any) error {
	defer func() { _ = frames.Close() }()
	dec := json.NewDecoder(frames)
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			if errors.Is(err, io.EOF) {
				err = errNoEnd
			}
			return offline(ctx, s.server, err)
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
