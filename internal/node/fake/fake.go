// Package fake is an in-memory node.Runtime for tests and for running the
// daemon without Docker (PLATFORM_FAKE_RUNTIME=1).
//
// Containers "run" instantly and pass health checks unless a failure is
// configured with the Fail* methods. Ops records every mutating call in order,
// so tests can assert sequencing (e.g. stateful recreate stops before start).
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/platform/internal/node"
)

// Runtime is a fake node.Runtime. The zero value is not usable; use New.
type Runtime struct {
	mu         sync.Mutex
	containers map[string]*node.Container
	specs      map[string]node.ContainerSpec
	images     map[string]string
	networks   map[string]bool
	volumes    map[string]bool
	routes     []node.Route
	ops        []string
	logs       map[string][]node.LogLine
	subs       []chan node.ContainerEvent
	nextID     atomic.Int64
	nextPort   int

	buildErr    error
	pullErr     error
	startErr    error
	healthErr   func(node.ContainerSpec) error
	exitOnStart bool
	routesErr   error
	healthGate  chan struct{}
	domainCheck func(hostname string) node.DomainCheck
}

var _ node.Runtime = (*Runtime)(nil)

// New returns an empty fake runtime.
func New() *Runtime {
	return &Runtime{
		containers: map[string]*node.Container{},
		specs:      map[string]node.ContainerSpec{},
		images:     map[string]string{},
		networks:   map[string]bool{},
		volumes:    map[string]bool{},
		logs:       map[string][]node.LogLine{},
		nextPort:   40000,
	}
}

// FailBuild makes every Build return err (nil to succeed again).
func (r *Runtime) FailBuild(err error) { r.mu.Lock(); r.buildErr = err; r.mu.Unlock() }

// FailPull makes every Pull return err.
func (r *Runtime) FailPull(err error) { r.mu.Lock(); r.pullErr = err; r.mu.Unlock() }

// FailStart makes every Start return err.
func (r *Runtime) FailStart(err error) { r.mu.Lock(); r.startErr = err; r.mu.Unlock() }

// FailHealthCheck makes health checks fail for containers where fn returns
// an error. Nil makes every check pass.
func (r *Runtime) FailHealthCheck(fn func(node.ContainerSpec) error) {
	r.mu.Lock()
	r.healthErr = fn
	r.mu.Unlock()
}

// ExitOnStart makes new containers exit immediately (code 1), so their
// health checks fail and a die event is emitted.
func (r *Runtime) ExitOnStart(exit bool) { r.mu.Lock(); r.exitOnStart = exit; r.mu.Unlock() }

// FailApplyRoutes makes ApplyRoutes return err, keeping the previous routes.
func (r *Runtime) FailApplyRoutes(err error) { r.mu.Lock(); r.routesErr = err; r.mu.Unlock() }

// HoldHealthChecks blocks health checks until the returned func is called,
// for tests that need a deploy to stay in flight.
func (r *Runtime) HoldHealthChecks() (release func()) {
	gate := make(chan struct{})
	r.mu.Lock()
	r.healthGate = gate
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.healthGate = nil
			r.mu.Unlock()
			close(gate)
		})
	}
}

// SetDomainCheck sets how CheckDomain answers. By default DNS and
// certificates are OK for every hostname.
func (r *Runtime) SetDomainCheck(fn func(hostname string) node.DomainCheck) {
	r.mu.Lock()
	r.domainCheck = fn
	r.mu.Unlock()
}

// Routes returns the last applied routing table.
func (r *Runtime) Routes() []node.Route {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.routes)
}

// Ops returns every mutating call so far, like "start web-1" or "stop web-1".
func (r *Runtime) Ops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ops)
}

// Running returns the names of running containers, sorted.
func (r *Runtime) Running() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, c := range r.containers {
		if c.State == node.StateRunning {
			names = append(names, c.Name)
		}
	}
	slices.Sort(names)
	return names
}

// Spec returns the spec a container was started with, by name.
func (r *Runtime) Spec(name string) (node.ContainerSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if c.Name == name {
			return r.specs[id], true
		}
	}
	return node.ContainerSpec{}, false
}

// AddLogs appends log lines to a container, by name.
func (r *Runtime) AddLogs(name string, lines ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if c.Name == name {
			for _, l := range lines {
				r.logs[id] = append(r.logs[id], node.LogLine{Time: time.Now(), Stream: "stdout", Text: l})
			}
		}
	}
}

// AddContainer injects a container the platform didn't start (e.g. left over
// from elsewhere), for reconcile tests.
func (r *Runtime) AddContainer(c node.Container) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.containers[c.ID] = &c
}

// Emit sends a container event to all Events subscribers and applies it to
// the container's state (die → exited, start → running, oom → OOMKilled).
func (r *Runtime) Emit(ev node.ContainerEvent) {
	r.mu.Lock()
	if c, ok := r.containers[ev.ContainerID]; ok {
		ev.Labels = c.Labels
		switch ev.Action {
		case "die":
			c.State, c.ExitCode = node.StateExited, ev.ExitCode
		case "start":
			c.State = node.StateRunning
		case "restart":
			c.State = node.StateRunning
			c.RestartCount++
		case "oom":
			c.OOMKilled = true
		}
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	subs := slices.Clone(r.subs)
	r.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (r *Runtime) op(format string, args ...any) {
	r.ops = append(r.ops, fmt.Sprintf(format, args...))
}

func imageID(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Pull implements node.Containers.
func (r *Runtime) Pull(_ context.Context, ref string, logs io.Writer) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("pull %s", ref)
	if r.pullErr != nil {
		return "", r.pullErr
	}
	_, _ = fmt.Fprintf(logs, "Pulled %s\n", ref)
	r.images[ref] = imageID(ref)
	return r.images[ref], nil
}

// HasImage implements node.Containers.
func (r *Runtime) HasImage(_ context.Context, ref string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.images[ref]
	return ok, nil
}

// EnsureNetwork implements node.Containers.
func (r *Runtime) EnsureNetwork(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.networks[name] = true
	return nil
}

// EnsureVolume implements node.Containers.
func (r *Runtime) EnsureVolume(_ context.Context, name string, _ node.Labels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.volumes[name] = true
	return nil
}

// Start implements node.Containers.
func (r *Runtime) Start(_ context.Context, spec node.ContainerSpec) (node.Container, error) {
	r.mu.Lock()
	r.op("start %s", spec.Name)
	if r.startErr != nil {
		defer r.mu.Unlock()
		return node.Container{}, r.startErr
	}
	for _, c := range r.containers {
		if c.Name == spec.Name {
			r.mu.Unlock()
			return node.Container{}, fmt.Errorf("container name %q already in use", spec.Name)
		}
	}
	id := fmt.Sprintf("c%04d", r.nextID.Add(1))
	c := &node.Container{
		ID:        id,
		Name:      spec.Name,
		Image:     spec.Image,
		Labels:    spec.Labels,
		State:     node.StateRunning,
		StartedAt: time.Now(),
	}
	if spec.Port != 0 {
		c.HostPort = spec.HostPort
		if c.HostPort == 0 {
			r.nextPort++
			c.HostPort = r.nextPort
		}
	}
	exit := r.exitOnStart
	if exit {
		c.State, c.ExitCode = node.StateExited, 1
	}
	r.containers[id] = c
	r.specs[id] = spec
	out := *c
	r.mu.Unlock()
	if exit {
		r.Emit(node.ContainerEvent{ContainerID: id, Action: "die", ExitCode: 1})
	}
	return out, nil
}

// Stop implements node.Containers.
func (r *Runtime) Stop(_ context.Context, id string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	r.op("stop %s", c.Name)
	c.State = node.StateExited
	return nil
}

// Remove implements node.Containers.
func (r *Runtime) Remove(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	r.op("remove %s", c.Name)
	delete(r.containers, id)
	delete(r.specs, id)
	return nil
}

// Inspect implements node.Containers.
func (r *Runtime) Inspect(_ context.Context, id string) (node.Container, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return node.Container{}, fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	return *c, nil
}

// List implements node.Containers.
func (r *Runtime) List(context.Context) ([]node.Container, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]node.Container, 0, len(r.containers))
	for _, id := range slices.Sorted(maps.Keys(r.containers)) {
		out = append(out, *r.containers[id])
	}
	return out, nil
}

// HealthCheck implements node.Containers.
func (r *Runtime) HealthCheck(ctx context.Context, c node.Container, _ node.HealthCheck) error {
	r.mu.Lock()
	gate := r.healthGate
	r.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.containers[c.ID]
	if !ok {
		return fmt.Errorf("container %s: %w", c.ID, node.ErrNotFound)
	}
	if cur.State != node.StateRunning {
		return fmt.Errorf("container exited with code %d", cur.ExitCode)
	}
	if r.healthErr != nil {
		return r.healthErr(r.specs[c.ID])
	}
	return nil
}

// Logs implements node.Containers. Following is not simulated: the channel
// closes after the backlog.
func (r *Runtime) Logs(_ context.Context, id string, opts node.LogOptions) (<-chan node.LogLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.containers[id]; !ok {
		return nil, fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	lines := r.logs[id]
	if opts.Tail > 0 && len(lines) > opts.Tail {
		lines = lines[len(lines)-opts.Tail:]
	}
	ch := make(chan node.LogLine, len(lines))
	for _, l := range lines {
		if l.Time.Before(opts.Since) {
			continue
		}
		ch <- l
	}
	close(ch)
	return ch, nil
}

// Events implements node.Containers.
func (r *Runtime) Events(ctx context.Context) (<-chan node.ContainerEvent, error) {
	ch := make(chan node.ContainerEvent, 64)
	r.mu.Lock()
	r.subs = append(r.subs, ch)
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		r.mu.Lock()
		r.subs = slices.DeleteFunc(r.subs, func(c chan node.ContainerEvent) bool { return c == ch })
		r.mu.Unlock()
		close(ch)
	}()
	return ch, nil
}

// Build implements node.Builder. It reads the whole source stream.
func (r *Runtime) Build(_ context.Context, req node.BuildRequest) (string, error) {
	n, err := io.Copy(io.Discard, req.Source)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("build %s", req.Tag)
	_, _ = fmt.Fprintf(req.Logs, "Received %d bytes of build context\n", n)
	if r.buildErr != nil {
		_, _ = fmt.Fprintf(req.Logs, "ERROR: %v\n", r.buildErr)
		return "", r.buildErr
	}
	_, _ = fmt.Fprintf(req.Logs, "Built %s\n", req.Tag)
	r.images[req.Tag] = imageID(req.Tag)
	return r.images[req.Tag], nil
}

// ApplyRoutes implements node.Proxy.
func (r *Runtime) ApplyRoutes(_ context.Context, routes []node.Route) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routesErr != nil {
		return r.routesErr
	}
	parts := make([]string, 0, len(routes))
	for _, rt := range routes {
		parts = append(parts, strings.Join(rt.Hostnames, ",")+"->"+rt.Upstream+rt.RedirectTo)
	}
	r.op("routes %s", strings.Join(parts, " "))
	r.routes = slices.Clone(routes)
	return nil
}

// CheckDomain implements node.Proxy.
func (r *Runtime) CheckDomain(_ context.Context, hostname string) (node.DomainCheck, error) {
	r.mu.Lock()
	fn := r.domainCheck
	r.mu.Unlock()
	if fn != nil {
		return fn(hostname), nil
	}
	return node.DomainCheck{ExpectedIPs: []string{"127.0.0.1"}, ResolvedIPs: []string{"127.0.0.1"}, DNSOK: true, CertificateOK: true}, nil
}
