// Package fake is an in-memory node.Runtime for tests and for running the
// daemon without Docker (DOCK_FAKE_RUNTIME=1).
//
// Containers "run" instantly and pass health checks unless a failure is
// configured with the Fail* methods. Ops records every mutating call in order,
// so tests can assert sequencing (e.g. stateful recreate stops before start).
package fake

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Runtime is a fake node.Runtime. The zero value is not usable; use New.
type Runtime struct {
	mu         sync.Mutex
	containers map[string]*node.Container
	specs      map[string]node.ContainerSpec
	images     map[string]string
	networks   map[string]node.Labels
	volumes    map[string]node.Labels
	// data is what volumes hold, for jobs that archive or unpack one
	// (SetVolumeData).
	data   map[string][]byte
	routes []node.Route
	certs  []node.Certificate
	ops    []string
	logs   map[string][]node.LogLine
	// changed is closed (and cleared) when logs or a container's state
	// change, waking Logs followers.
	changed  chan struct{}
	subs     []chan node.ContainerEvent
	nextID   atomic.Int64
	nextPort int

	buildErr   error
	buildBase  node.Built
	pullErr    error
	startErr   error
	stopErr    error
	restartErr error
	resumeErr  error
	// openRequests is what OpenRequests answers (SetOpenRequests).
	openRequests int
	healthErr    func(node.ContainerSpec) error
	exitOnStart  bool
	routesErr    error
	routesHold   *routesHold
	healthGate   chan struct{}
	// passHealth, when set (PassHealthAfter), turns a started container's
	// health from starting to healthy that long after it starts.
	passHealth  time.Duration
	stopGate    chan struct{}
	domainCheck func(hostname string) node.DomainCheck
	dialAddr    string
	// forwards are Forward's listeners by the port asked for; each
	// listens on a free loopback port (ForwardAddr).
	forwards map[int]net.Listener
	onJob    func(context.Context, node.JobSpec) (node.JobResult, error)
	jobs     []node.JobSpec
	// sidecars are EnsureSidecar's specs by name; sidecarErr fails it.
	sidecars   map[string]node.SidecarSpec
	sidecarErr error

	// imageVolumes are the VOLUMEs images declare (SetImageVolumes);
	// keptAnon names the containers Remove kept the anonymous volumes of.
	imageVolumes map[string][]string
	keptAnon     []string

	// tunnel is what EnsureTunnel last ran; nil when cloudflared isn't.
	tunnel *node.TunnelSpec

	traffic    []node.HostTraffic
	disk       node.DiskUsage
	diskSet    bool
	host       node.HostStats
	hostSet    bool
	ctrStats   map[string]node.ContainerStats
	statsCalls atomic.Int64

	analytics fakeAnalytics

	// archives are the log archives by service and release (logarchive.go).
	archives   map[string]map[string]*fakeArchive
	archiveErr error

	// probes are scripted uptime probe results (probe.go).
	probes fakeProbes
	// hostChecks are scripted host check results (hostcheck.go).
	hostChecks fakeHostChecks

	// scans are scripted image scans (scan.go).
	scans fakeScans
}

var _ node.Runtime = (*Runtime)(nil)

// New returns an empty fake runtime.
func New() *Runtime {
	return &Runtime{
		containers: map[string]*node.Container{},
		specs:      map[string]node.ContainerSpec{},
		images:     map[string]string{},
		networks:   map[string]node.Labels{},
		volumes:    map[string]node.Labels{},
		data:       map[string][]byte{},
		logs:       map[string][]node.LogLine{},
		ctrStats:   map[string]node.ContainerStats{},
		archives:   map[string]map[string]*fakeArchive{},
		nextPort:   40000,
		forwards:   map[int]net.Listener{},
	}
}

// FailBuild makes every Build return err (nil to succeed again).
func (r *Runtime) FailBuild(err error) { r.mu.Lock(); r.buildErr = err; r.mu.Unlock() }

// FailPull makes every Pull return err.
func (r *Runtime) FailPull(err error) { r.mu.Lock(); r.pullErr = err; r.mu.Unlock() }

// FailStart makes every Start return err.
func (r *Runtime) FailStart(err error) { r.mu.Lock(); r.startErr = err; r.mu.Unlock() }

// FailStop makes every Stop return err, leaving the container running.
func (r *Runtime) FailStop(err error) { r.mu.Lock(); r.stopErr = err; r.mu.Unlock() }

// FailRestart makes every Restart return err, leaving the container
// running as it was.
func (r *Runtime) FailRestart(err error) { r.mu.Lock(); r.restartErr = err; r.mu.Unlock() }

// FailResume makes every Resume return err, leaving the container
// stopped.
func (r *Runtime) FailResume(err error) { r.mu.Lock(); r.resumeErr = err; r.mu.Unlock() }

// SetOpenRequests makes OpenRequests answer n, as if Caddy proxied n
// requests (a websocket, say) to the upstreams asked about.
func (r *Runtime) SetOpenRequests(n int) { r.mu.Lock(); r.openRequests = n; r.mu.Unlock() }

// OpenRequests implements node.Proxy (SetOpenRequests).
func (r *Runtime) OpenRequests(context.Context, []string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openRequests, nil
}

// SetHealth changes the health check state of container id, as Docker's
// probe would: Health and, when unhealthy, HealthOutput (otherwise
// cleared). Like Docker, it emits a health_status event only when the
// state changes.
func (r *Runtime) SetHealth(id, health, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return
	}
	if health != node.HealthUnhealthy {
		output = ""
	}
	changed := c.Health != health
	c.Health, c.HealthOutput = health, output
	if changed {
		r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "health_status", Health: health})
	}
}

// PassHealthAfter makes containers with a health check report healthy d
// after they start or restart, with a health_status event, as Docker's
// probe would; until then they are starting. A `make dev` daemon sets it
// (about a second); tests leave it off and drive health with SetHealth.
// A failed (FailHealthCheck) or held (HoldHealthChecks) check stays
// starting.
func (r *Runtime) PassHealthAfter(d time.Duration) { r.mu.Lock(); r.passHealth = d; r.mu.Unlock() }

// passHealthLater arms passHealth's one-shot timer for container id,
// started at started; a restart since (a new StartedAt) or a health
// state other than starting leaves it alone. Called with r.mu held.
func (r *Runtime) passHealthLater(id string, started time.Time) {
	if r.passHealth <= 0 {
		return
	}
	time.AfterFunc(r.passHealth, func() {
		r.mu.Lock()
		c, ok := r.containers[id]
		pass := ok && c.State == node.StateRunning && c.StartedAt.Equal(started) && c.Health == node.HealthStarting && r.healthGate == nil &&
			(r.healthErr == nil || r.healthErr(r.specs[id]) == nil)
		r.mu.Unlock()
		if pass {
			r.SetHealth(id, node.HealthHealthy, "")
		}
	})
}

// demoCrashLoop is a `make dev` demo of a crash loop's diagnosis (ADR
// 0071), for a service with the variable DEMO_CRASH_LOOP=<exit code>:
// 8 s after it starts (past a deploy's grace), its container says a
// variable is missing and exits with code, and Docker restarts it, five
// times a second apart.
func (r *Runtime) demoCrashLoop(id string, code int) {
	for i := range 5 {
		time.AfterFunc(time.Duration(8+i)*time.Second, func() {
			r.mu.Lock()
			c, ok := r.containers[id]
			name := ""
			if ok {
				name = c.Name
			}
			r.mu.Unlock()
			if !ok {
				return
			}
			r.addLogs(name, "stderr", []string{"Error: DATABASE_URL is not set"})
			r.Emit(node.ContainerEvent{ContainerID: id, Action: "die", ExitCode: code, Time: time.Now()})
			r.Emit(node.ContainerEvent{ContainerID: id, Action: "restart", Time: time.Now()})
		})
	}
}

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

// routesHold blocks the ApplyRoutes calls match picks until release.
type routesHold struct {
	match   func([]node.Route) bool
	held    chan struct{} // closed when the first matching call waits
	release chan struct{}
	once    sync.Once
}

// HoldApplyRoutes blocks ApplyRoutes calls whose routes match picks until
// the returned release is called, like a proxy that takes its time; held
// is closed once one waits. Tests use it to stop a deploy's cutover after
// it rendered routes and before its release reads as active.
func (r *Runtime) HoldApplyRoutes(match func([]node.Route) bool) (held <-chan struct{}, release func()) {
	h := &routesHold{match: match, held: make(chan struct{}), release: make(chan struct{})}
	r.mu.Lock()
	r.routesHold = h
	r.mu.Unlock()
	var once sync.Once
	return h.held, func() {
		once.Do(func() {
			r.mu.Lock()
			r.routesHold = nil
			r.mu.Unlock()
			close(h.release)
		})
	}
}

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

// HoldStops blocks Stop until the returned func is called, like a container
// that takes its whole stop timeout to exit.
func (r *Runtime) HoldStops() (release func()) {
	gate := make(chan struct{})
	r.mu.Lock()
	r.stopGate = gate
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.stopGate = nil
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

// Certificates returns the certificates of the last applied routing table.
func (r *Runtime) Certificates() []node.Certificate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.certs)
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

// AddLogs appends stdout lines to a container, by name.
func (r *Runtime) AddLogs(name string, lines ...string) { r.addLogs(name, "stdout", lines) }

// AddStderr appends stderr lines to a container, by name.
func (r *Runtime) AddStderr(name string, lines ...string) { r.addLogs(name, "stderr", lines) }

// addLogs appends lines written to stream to the container named name.
func (r *Runtime) addLogs(name, stream string, lines []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if c.Name == name {
			for _, l := range lines {
				r.logs[id] = append(r.logs[id], node.LogLine{Time: time.Now(), Stream: stream, Text: l})
			}
		}
	}
	r.notifyLocked()
}

// notifyLocked wakes Logs followers (r.mu held).
func (r *Runtime) notifyLocked() {
	if r.changed != nil {
		close(r.changed)
		r.changed = nil
	}
}

// changedLocked returns a channel closed at the next change (r.mu held).
func (r *Runtime) changedLocked() <-chan struct{} {
	if r.changed == nil {
		r.changed = make(chan struct{})
	}
	return r.changed
}

// AddContainer injects a container the platform didn't start (e.g. left over
// from elsewhere), for reconcile tests.
func (r *Runtime) AddContainer(c node.Container) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.containers[c.ID] = &c
}

// Subscribers is how many Events streams are open: a test that emits an
// event waits for its follower first, since Emit reaches only those
// already subscribed (as a real node's stream does).
func (r *Runtime) Subscribers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}

// Emit sends a container event to all Events subscribers and applies it to
// the container's state (die → exited, start → running, oom → OOMKilled,
// health_status → Health).
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
		case "health_status":
			c.Health = ev.Health
		}
	}
	r.notifyLocked()
	r.sendLocked(ev)
	r.mu.Unlock()
}

// sendLocked delivers ev to subscribers without blocking (r.mu held), like
// the events Docker emits for the runtime's own Stop and Remove.
func (r *Runtime) sendLocked(ev node.ContainerEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	for _, ch := range r.subs {
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

// Pull implements node.Containers. A pull with a token is recorded as
// "pull <ref> token=<token>", one with a username and password as
// "pull <ref> user=<username> password=<password>".
func (r *Runtime) Pull(_ context.Context, ref string, logs io.Writer, opts ...node.PullOptions) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var o node.PullOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	switch {
	case o.Token != "":
		r.op("pull %s token=%s", ref, o.Token)
	case o.Username != "":
		r.op("pull %s user=%s password=%s", ref, o.Username, o.Password)
	default:
		r.op("pull %s", ref)
	}
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

// RemoveImage implements node.Containers, recorded as "rmi <ref>". An
// image a container uses is refused, like Docker does.
func (r *Runtime) RemoveImage(_ context.Context, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("rmi %s", ref)
	id, ok := r.images[ref]
	if !ok {
		return fmt.Errorf("image %s: %w", ref, node.ErrNotFound)
	}
	for _, c := range r.containers {
		if c.Image == ref || c.Image == id {
			return fmt.Errorf("image %s is used by container %s", ref, c.Name)
		}
	}
	delete(r.images, ref)
	return nil
}

// SetDiskUsage sets what DiskUsage reports.
func (r *Runtime) SetDiskUsage(d node.DiskUsage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disk, r.diskSet = d, true
}

// DiskUsage implements node.Containers, recorded as "disk usage
// keep=<ids> repos=<prefixes>": what SetDiskUsage set, or until then a
// made-up breakdown for `make dev` (300 MiB per volume, 180 MiB of
// images per container, a BuildKit cache).
func (r *Runtime) DiskUsage(_ context.Context, req node.PruneImagesRequest) (node.DiskUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("disk usage %s", pruneOp(req))
	if !r.diskSet {
		d := node.DiskUsage{
			ImagesRunning: int64(len(r.containers)) * 180 << 20, ImagesKept: 1400 << 20, ImagesReclaimable: 620 << 20,
			ImagesRollbacks: 380 << 20, ImagesShared: 900 << 20, Containers: 64 << 20, DataDir: 96 << 20, ScanLeftovers: 1200 << 20,
			Volumes: []node.VolumeUsage{
				{Name: node.BuildCacheVolume, Bytes: 2900 << 20},
				{Name: "dock_scan_db_fake", Role: node.VolumeScanDB, Bytes: 4600 << 20},
			},
		}
		for _, name := range slices.Sorted(maps.Keys(r.volumes)) {
			d.Volumes = append(d.Volumes, node.VolumeUsage{Name: name, Service: r.volumes[name].Service, Bytes: 300 << 20})
		}
		return d, nil
	}
	d := r.disk
	d.Volumes = slices.Clone(d.Volumes)
	return d, nil
}

// PruneImages implements node.Containers, recorded as "prune images
// keep=<ids> repos=<prefixes>" and the request's other lists when set
// (pruneOp). The fake removes nothing.
func (r *Runtime) PruneImages(_ context.Context, req node.PruneImagesRequest) (node.PruneResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("prune images %s", pruneOp(req))
	return node.PruneResult{}, nil
}

// pruneOp is how a prune request is recorded: keep and repos always, the
// other lists and flags when set.
func pruneOp(req node.PruneImagesRequest) string {
	s := "keep=" + strings.Join(req.Keep, ",") + " repos=" + strings.Join(req.Repos, ",")
	for _, l := range []struct {
		name string
		ids  []string
	}{{"unneeded", req.Unneeded}, {"rollbacks", req.Rollbacks}, {"pinned", req.Pinned}, {"protect", req.Protect}} {
		if len(l.ids) > 0 {
			s += " " + l.name + "=" + strings.Join(l.ids, ",")
		}
	}
	if req.OnlyRollbacks {
		s += " only-rollbacks"
	}
	if req.Tool != "" {
		s += " tool=" + req.Tool
	}
	return s
}

// HasImageID reports whether an image with this ID is present under any
// reference.
func (r *Runtime) HasImageID(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, have := range r.images {
		if have == id {
			return true
		}
	}
	return false
}

// EnsureNetwork implements node.Containers.
func (r *Runtime) EnsureNetwork(_ context.Context, name string, labels node.Labels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	labels = node.Labels{Project: labels.Project, Environment: labels.Environment}
	if have, ok := r.networks[name]; ok {
		if have.Conflicts(labels) {
			return fmt.Errorf("network %q belongs to another environment", name)
		}
		return nil
	}
	r.networks[name] = labels
	return nil
}

// RemoveNetwork implements node.Containers, recorded as "remove network
// <name>".
func (r *Runtime) RemoveNetwork(_ context.Context, name string, labels node.Labels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	have, ok := r.networks[name]
	if !ok || !have.Matches(labels) {
		return fmt.Errorf("network %s: %w", name, node.ErrNotFound)
	}
	r.op("remove network %s", name)
	delete(r.networks, name)
	return nil
}

// EnsureVolume implements node.Containers.
func (r *Runtime) EnsureVolume(_ context.Context, name string, labels node.Labels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if have, ok := r.volumes[name]; ok {
		if have.Conflicts(labels) {
			return fmt.Errorf("volume %q belongs to another service", name)
		}
		return nil
	}
	r.volumes[name] = labels
	return nil
}

// RemoveVolume implements node.Containers, recorded as "remove volume
// <name>". Like Docker, it refuses while any container, running or not,
// mounts the volume.
func (r *Runtime) RemoveVolume(_ context.Context, name string, labels node.Labels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	have, ok := r.volumes[name]
	if !ok || !have.Matches(labels) {
		return fmt.Errorf("volume %s: %w", name, node.ErrNotFound)
	}
	for id, spec := range r.specs {
		if slices.ContainsFunc(spec.Mounts, func(m node.Mount) bool { return m.Volume == name }) {
			return fmt.Errorf("volume %s is in use by container %s", name, r.containers[id].Name)
		}
	}
	r.op("remove volume %s", name)
	delete(r.volumes, name)
	return nil
}

// Volumes returns the names of the volumes there are, sorted.
func (r *Runtime) Volumes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(maps.Keys(r.volumes))
}

// Networks returns the names of networks created so far, sorted.
func (r *Runtime) Networks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(maps.Keys(r.networks))
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
	if spec.Health != nil {
		c.Health = node.HealthStarting
	}
	c.AnonymousVolumes = node.AnonymousPaths(r.imageVolumes[spec.Image], spec.Mounts)
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
	r.logs[id] = append(r.logs[id], demoLogs()...)
	if n, err := strconv.Atoi(spec.Env["DEMO_CRASH_LOOP"]); err == nil && !exit {
		r.demoCrashLoop(id, n)
	}
	out := *c
	if !exit {
		r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "start"})
		if c.Health != "" {
			r.passHealthLater(id, c.StartedAt)
		}
	}
	r.mu.Unlock()
	if exit {
		r.Emit(node.ContainerEvent{ContainerID: id, Action: "die", ExitCode: 1})
	}
	return out, nil
}

// Stop implements node.Containers. Like Docker, it emits a die event for a
// running container (and Remove a destroy event).
func (r *Runtime) Stop(ctx context.Context, id string, _ time.Duration) error {
	r.mu.Lock()
	gate := r.stopGate
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
	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	r.op("stop %s", c.Name)
	if r.stopErr != nil {
		return r.stopErr
	}
	if c.State == node.StateRunning {
		c.State, c.ExitCode = node.StateExited, 0
		r.notifyLocked()
		r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "die"})
	}
	return nil
}

// Restart implements node.Containers. Like Docker, it emits die, start and
// restart events; the container keeps its ID and its health is reset to
// starting if it has a check. FailRestart's error fails it, and so does
// FailStop's (a restart stops first).
func (r *Runtime) Restart(_ context.Context, id string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	if c.State != node.StateRunning {
		return fmt.Errorf("container %s: %w", c.Name, node.ErrNotRunning)
	}
	r.op("restart %s", c.Name)
	if err := cmp.Or(r.restartErr, r.stopErr); err != nil {
		return err
	}
	c.StartedAt = time.Now()
	if c.Health != "" {
		c.Health, c.HealthOutput = node.HealthStarting, ""
		r.passHealthLater(id, c.StartedAt)
	}
	for _, action := range []string{"die", "start", "restart"} {
		ev := node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: action}
		if action == "die" {
			ev.ExitCode = 137
		}
		r.sendLocked(ev)
	}
	return nil
}

// Resume implements node.Containers. Like Docker, it emits a start event
// for a container it starts; its health starts over if it has a check.
// A running container is left alone.
func (r *Runtime) Resume(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	if c.State == node.StateRunning {
		return nil
	}
	r.op("resume %s", c.Name)
	if r.resumeErr != nil {
		return r.resumeErr
	}
	c.State, c.ExitCode, c.StartedAt = node.StateRunning, 0, time.Now()
	if c.Health != "" {
		c.Health, c.HealthOutput = node.HealthStarting, ""
		r.passHealthLater(id, c.StartedAt)
	}
	r.notifyLocked()
	r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "start"})
	return nil
}

// SetImageVolumes makes the image ref declare VOLUMEs at paths: Start, of
// ref or of the ID Pull gives it, reports the ones no mount covers as the
// container's AnonymousVolumes.
func (r *Runtime) SetImageVolumes(ref string, paths ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.imageVolumes == nil {
		r.imageVolumes = map[string][]string{}
	}
	r.imageVolumes[ref] = paths
	r.imageVolumes[imageID(ref)] = paths
}

// KeptAnonymousVolumes returns the names of the containers Remove kept the
// anonymous volumes of (RemoveOptions.KeepAnonymousVolumes), in order.
func (r *Runtime) KeptAnonymousVolumes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.keptAnon)
}

// Remove implements node.Containers.
func (r *Runtime) Remove(_ context.Context, id string, opts node.RemoveOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	if c.State == node.StateRunning {
		return fmt.Errorf("container %s is running; stop it first", c.Name)
	}
	r.op("remove %s", c.Name)
	if opts.KeepAnonymousVolumes {
		r.keptAnon = append(r.keptAnon, c.Name)
	}
	delete(r.containers, id)
	delete(r.specs, id)
	r.notifyLocked()
	r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "destroy"})
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

// ReleaseContainers implements node.Containers.
func (r *Runtime) ReleaseContainers(_ context.Context, want node.Labels) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, cid := range slices.Sorted(maps.Keys(r.containers)) {
		if l := r.containers[cid].Labels; l.Release == want.Release && l.Matches(want) {
			out = append(out, cid)
		}
	}
	return out, nil
}

// HealthCheck implements node.Containers. A container with
// DEMO_HEALTH_HANG set never answers: its check waits out hc.Timeout, so
// `make dev` and the agent eval can cancel a deploy in flight (ADR 0086).
func (r *Runtime) HealthCheck(ctx context.Context, c node.Container, hc node.HealthCheck) error {
	r.mu.Lock()
	gate := r.healthGate
	hang := r.specs[c.ID].Env["DEMO_HEALTH_HANG"] != ""
	r.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if hang {
		t := time.NewTimer(hc.Timeout)
		defer t.Stop()
		select {
		case <-t.C:
			return fmt.Errorf("health check timed out after %s: the app never answered", hc.Timeout)
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
	if port := r.specs[c.ID].Env["DEMO_LISTEN_PORT"]; port != "" && port != r.specs[c.ID].Env["PORT"] {
		// A `make dev` demo of a deploy diagnosis (ADR 0071): the app
		// says it listens on another port than the one checked.
		r.logs[c.ID] = append(r.logs[c.ID], node.LogLine{Time: time.Now(), Stream: "stdout", Text: `{"level":30,"msg":"Server listening at http://0.0.0.0:` + port + `"}`})
		return fmt.Errorf("health check timed out after 1m0s: connect to 127.0.0.1:%d: connection closed, is the app listening on the container port?", cur.HostPort)
	}
	return nil
}

// Logs implements node.Containers. With opts.Follow it sends lines as
// AddLogs adds them, until the container stops or is removed or ctx ends,
// like Docker; without, the channel closes after the backlog.
func (r *Runtime) Logs(ctx context.Context, id string, opts node.LogOptions) (<-chan node.LogLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.containers[id]; !ok {
		return nil, fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	lines := r.logs[id]
	if opts.Follow {
		start := 0
		if opts.Tail > 0 && len(lines) > opts.Tail {
			start = len(lines) - opts.Tail
		}
		ch := make(chan node.LogLine)
		go r.follow(ctx, id, start, opts.Since, ch)
		return ch, nil
	}
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

// follow sends a container's lines from index pos on (skipping those
// before since), then new ones as they are added, until the container
// isn't running or ctx ends.
func (r *Runtime) follow(ctx context.Context, id string, pos int, since time.Time, ch chan<- node.LogLine) {
	defer close(ch)
	for {
		r.mu.Lock()
		lines := r.logs[id][pos:]
		pos += len(lines)
		c, ok := r.containers[id]
		running := ok && c.State == node.StateRunning
		changed := r.changedLocked()
		r.mu.Unlock()
		for _, l := range lines {
			if l.Time.Before(since) {
				continue
			}
			select {
			case ch <- l:
			case <-ctx.Done():
				return
			}
		}
		if !running {
			return
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return
		}
	}
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

// DialTo makes Dial connect to addr (host:port, e.g. a test Postgres)
// whatever container and port it is asked for.
func (r *Runtime) DialTo(addr string) { r.mu.Lock(); r.dialAddr = addr; r.mu.Unlock() }

// Dial implements node.Containers. Like the real runtime it only dials
// running service containers and sidecars (by name); the connection goes
// to the DialTo address.
func (r *Runtime) Dial(ctx context.Context, req node.DialRequest) (net.Conn, error) {
	// The container's fields are read under the lock: Stop changes them.
	r.mu.Lock()
	c, ok := r.containers[req.ContainerID]
	var service string
	var state node.ContainerState
	if ok {
		service, state = c.Labels.Service, c.State
	} else if s, found := r.sidecars[req.ContainerID]; found {
		ok, service, state = true, s.Labels.Service, node.StateRunning
	}
	addr := r.dialAddr
	r.mu.Unlock()
	switch {
	case !ok || service == "":
		return nil, fmt.Errorf("container %s: %w", req.ContainerID, node.ErrNotFound)
	case state != node.StateRunning:
		return nil, fmt.Errorf("container %s is %s", req.ContainerID, state)
	case addr == "":
		return nil, errors.New("fake: no dial target; call DialTo")
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// Forward implements node.Proxy, recorded as "forward <port>". It
// listens on a free loopback port instead of port, which ForwardAddr
// names; a port already forwarded is an error, as on a real server.
func (r *Runtime) Forward(_ context.Context, port int) (net.Listener, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ln, ok := r.forwards[port]; ok && !isClosed(ln) {
		return nil, fmt.Errorf("forward port %d: address already in use", port)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r.op("forward %d", port)
	ln = &fakeListener{Listener: ln}
	r.forwards[port] = ln
	return ln, nil
}

// ForwardAddr is the address Forward(port) listens on, "" when none
// listens.
func (r *Runtime) ForwardAddr(port int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ln, ok := r.forwards[port]; ok && !isClosed(ln) {
		return ln.Addr().String()
	}
	return ""
}

// fakeListener remembers being closed, so a port can be forwarded again.
type fakeListener struct {
	net.Listener
	closed atomic.Bool
}

func (l *fakeListener) Close() error { l.closed.Store(true); return l.Listener.Close() }

func isClosed(ln net.Listener) bool {
	fl, ok := ln.(*fakeListener)
	return ok && fl.closed.Load()
}

// syncJob plays dock-sync (ADR 0063) on a volume's data: `recv ROOT`
// writes the data it holds ("M", its length, the bytes), then reads "S"
// (unchanged) or "C" and new data; `send [--live|--check] ROOT` reads that
// list and answers with its own data where it differs, --check exiting 3
// instead. Both print dock-sync's report line on standard error. ROOT
// "/v" is the mounted volume's data; another root is a directory in it,
// kept as "<volume><root>".
func (r *Runtime) syncJob(spec node.JobSpec) (node.JobResult, error) {
	st := spec.Stream
	fields := strings.Fields(spec.Script)
	if len(spec.Mounts) != 1 || len(fields) < 3 {
		return node.JobResult{ExitCode: 2}, nil
	}
	root := fields[len(fields)-1]
	key := spec.Mounts[0].Volume
	if root != "/v" {
		key += root
	}
	stderr := st.Stderr
	if stderr == nil {
		stderr = st.Stdout
	}
	r.mu.Lock()
	data := slices.Clone(r.data[key])
	r.mu.Unlock()
	readBlob := func(in io.Reader) ([]byte, error) {
		var n [8]byte
		if _, err := io.ReadFull(in, n[:]); err != nil {
			return nil, err
		}
		b := make([]byte, binary.BigEndian.Uint64(n[:]))
		_, err := io.ReadFull(in, b)
		return b, err
	}
	writeBlob := func(w io.Writer, tag byte, b []byte) error {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		_, err := w.Write(append(append([]byte{tag}, n[:]...), b...))
		return err
	}
	tag := make([]byte, 1)
	switch {
	case slices.Contains(fields, "recv"):
		if err := writeBlob(st.Stdout, 'M', data); err != nil {
			return node.JobResult{ExitCode: 1}, err
		}
		if _, err := io.ReadFull(st.Stdin, tag); err != nil {
			_, _ = fmt.Fprintln(stderr, "dock-sync: the stream ended before the pass did: the source stopped sending")
			return node.JobResult{ExitCode: 1}, nil
		}
		changed := 0
		if tag[0] == 'C' {
			b, err := readBlob(st.Stdin)
			if err != nil {
				return node.JobResult{ExitCode: 1}, nil
			}
			r.mu.Lock()
			r.data[key] = b
			r.mu.Unlock()
			changed, data = 1, b
		}
		_, _ = fmt.Fprintf(stderr, "dock-sync: entries=1 changed=%d bytes=%d deleted=0 racy=0\n", changed, changed*len(data))
		return node.JobResult{}, nil
	case slices.Contains(fields, "send"):
		if _, err := io.ReadFull(st.Stdin, tag); err != nil || tag[0] != 'M' {
			return node.JobResult{ExitCode: 1}, nil
		}
		theirs, err := readBlob(st.Stdin)
		if err != nil {
			return node.JobResult{ExitCode: 1}, nil
		}
		same := bytes.Equal(theirs, data)
		changed := 0
		if !same {
			changed = 1
		}
		if slices.Contains(fields, "--check") {
			_, _ = st.Stdout.Write([]byte{'S'})
			_, _ = fmt.Fprintf(stderr, "dock-sync: entries=1 changed=%d bytes=0 deleted=0 racy=0\n", changed)
			if !same {
				return node.JobResult{ExitCode: 3, Output: "dock-sync: differs: /"}, nil
			}
			return node.JobResult{}, nil
		}
		if same {
			_, err = st.Stdout.Write([]byte{'S'})
		} else {
			err = writeBlob(st.Stdout, 'C', data)
		}
		if err != nil {
			return node.JobResult{ExitCode: 1}, err
		}
		_, _ = fmt.Fprintf(stderr, "dock-sync: entries=1 changed=%d bytes=%d deleted=0 racy=0\n", changed, changed*len(data))
		return node.JobResult{}, nil
	}
	return node.JobResult{ExitCode: 2}, nil
}

// ErrDefault, returned by OnJob's function, lets RunJob answer a job as
// it does without one.
var ErrDefault = errors.New("fake: answer as by default")

// tarVolume is the volume a job archives or unpacks, as the fake plays
// it: a script containing op ("tar -cf" or "tar -xf") with one mount.
func tarVolume(spec node.JobSpec, op string) (string, bool) {
	if !strings.Contains(spec.Script, op) || len(spec.Mounts) != 1 {
		return "", false
	}
	return spec.Mounts[0].Volume, true
}

// SetVolumeData sets what volume holds, for jobs that archive it: one
// with a Stream whose script contains "tar -cf" and mounts only that
// volume writes it to the Stream's Stdout, and one with "tar -xf" sets
// it from the Stream's Stdin. Enough for core's tests to follow a
// volume's copy between servers (ADR 0053).
func (r *Runtime) SetVolumeData(volume string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[volume] = slices.Clone(data)
}

// VolumeData returns what volume holds (SetVolumeData).
func (r *Runtime) VolumeData(volume string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.data[volume])
}

// OnJob makes RunJob answer with fn (nil: exit 0, no output).
func (r *Runtime) OnJob(fn func(node.JobSpec) (node.JobResult, error)) {
	if fn == nil {
		r.OnJobContext(nil)
		return
	}
	r.OnJobContext(func(_ context.Context, spec node.JobSpec) (node.JobResult, error) { return fn(spec) })
}

// OnJobContext is OnJob with the job's context, for jobs that run until
// it ends (a timeout, a canceled caller).
func (r *Runtime) OnJobContext(fn func(context.Context, node.JobSpec) (node.JobResult, error)) {
	r.mu.Lock()
	r.onJob = fn
	r.mu.Unlock()
}

// Jobs returns the specs of every job run so far.
func (r *Runtime) Jobs() []node.JobSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.jobs)
}

// RunJob implements node.Containers, recorded as "job <name>". The answer
// comes from OnJob's function, called without the lock held. Without one,
// a job's Stdin is read to the end, and a read error fails the job as
// Docker's runtime does. spec.Pause's running containers are paused around
// the answer ("pause <id>", "unpause <id>", with pause and unpause
// events), so OnJob's function sees them as node.StatePaused through
// Inspect; an unknown ID, or one of another control plane, is
// node.ErrNotFound before anything is paused. A Stream's Stdin is echoed
// to its Stdout. A move's size job and a snapshot answer with their
// volumes' size, and a Sync job plays dock-sync (syncJob). A job without
// a network has none, as on a real server, so one that runs restic (it
// has a repository in its environment) without one is refused: it could
// never reach its storage.
func (r *Runtime) RunJob(ctx context.Context, spec node.JobSpec) (node.JobResult, error) {
	if _, restic := spec.Env["RESTIC_REPOSITORY"]; restic && spec.Network == "" {
		return node.JobResult{}, fmt.Errorf("job %s runs restic without a network: a job without one has only loopback", spec.Name)
	}
	r.mu.Lock()
	r.op("job %s", spec.Name)
	r.jobs = append(r.jobs, spec)
	fn := r.onJob
	paused, err := r.pauseLocked(spec)
	r.mu.Unlock()
	if err != nil {
		return node.JobResult{}, err
	}
	defer r.unpause(paused)
	if err := ctx.Err(); err != nil {
		return node.JobResult{}, err
	}
	if fn != nil {
		if res, err := fn(ctx, spec); !errors.Is(err, ErrDefault) {
			return res, err
		}
	}
	if st := spec.Stream; st != nil && spec.Sync {
		return r.syncJob(spec)
	}
	if strings.Contains(spec.Script, "dock-move: kib") {
		// A move measuring the volumes it copies (ADR 0053).
		r.mu.Lock()
		var n int
		for _, m := range spec.Mounts {
			n += len(r.data[m.Volume])
		}
		r.mu.Unlock()
		return node.JobResult{Output: fmt.Sprintf("dock-move: kib %d\n", (n+1023)/1024)}, nil
	}
	if strings.Contains(spec.Script, "dock-snapshot: size") {
		// A volume snapshot (ADR 0047) reports its size like the real
		// script, so `make dev` and the agent eval can take snapshots:
		// du -sk counts each volume's directory too (4 KiB on ext4), so
		// a volume the fake holds no data for isn't reported as 0.
		r.mu.Lock()
		kib := 0
		for _, m := range spec.Mounts {
			kib += 4 + (len(r.data[m.Volume])+1023)/1024
		}
		r.mu.Unlock()
		return node.JobResult{Output: fmt.Sprintf("dock-snapshot: size %d\n", kib)}, nil
	}
	if st := spec.Stream; st != nil {
		if vol, ok := tarVolume(spec, "tar -cf"); ok && st.Stdout != nil {
			r.mu.Lock()
			out := slices.Clone(r.data[vol])
			r.mu.Unlock()
			if _, err := st.Stdout.Write(out); err != nil {
				return node.JobResult{ExitCode: 137}, fmt.Errorf("job output: %w", err)
			}
			return node.JobResult{}, nil
		}
		if vol, ok := tarVolume(spec, "tar -xf"); ok && st.Stdin != nil {
			in, err := io.ReadAll(st.Stdin)
			if err != nil {
				return node.JobResult{ExitCode: 1}, fmt.Errorf("job input: %w", err)
			}
			r.mu.Lock()
			r.data[vol] = in
			r.mu.Unlock()
			return node.JobResult{}, nil
		}
	}
	if spec.Stdin != nil {
		if _, err := io.Copy(io.Discard, spec.Stdin); err != nil {
			return node.JobResult{ExitCode: 130}, fmt.Errorf("job input: %w", err)
		}
	}
	if st := spec.Stream; st != nil && st.Stdin != nil {
		// Like `cat`: the input comes back as output, until its end.
		var out strings.Builder
		w := io.Writer(&out)
		if st.Stdout != nil {
			w = io.MultiWriter(st.Stdout, &out)
		}
		_, err := io.Copy(w, st.Stdin)
		return node.JobResult{Output: out.String()}, err
	}
	return node.JobResult{}, nil
}

// pauseLocked checks spec.Pause like Docker's runtime and pauses the
// running ones, returning their IDs (r.mu held).
func (r *Runtime) pauseLocked(spec node.JobSpec) ([]string, error) {
	want := node.Labels{ControlPlane: spec.Labels.ControlPlane}
	for _, id := range spec.Pause {
		if c, ok := r.containers[id]; !ok || !c.Labels.Matches(want) {
			return nil, fmt.Errorf("container %s: %w", id, node.ErrNotFound)
		}
	}
	var paused []string
	for _, id := range spec.Pause {
		if c := r.containers[id]; c.State == node.StateRunning {
			c.State = node.StatePaused
			r.op("pause %s", id)
			r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "pause"})
			paused = append(paused, id)
		}
	}
	return paused, nil
}

// unpause ends RunJob's pause of ids, those still there and paused.
func (r *Runtime) unpause(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if c, ok := r.containers[id]; ok && c.State == node.StatePaused {
			c.State = node.StateRunning
			r.op("unpause %s", id)
			r.sendLocked(node.ContainerEvent{ContainerID: id, Labels: c.Labels, Action: "unpause"})
		}
	}
}

// EnsureTunnel implements node.Tunnel: it records the spec. Only a
// change is recorded as an op ("tunnel <hostname>", "tunnel off").
func (r *Runtime) EnsureTunnel(ctx context.Context, spec *node.TunnelSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case spec == nil && r.tunnel != nil:
		r.op("tunnel off")
	case spec != nil && (r.tunnel == nil || *r.tunnel != *spec):
		r.op("tunnel %s", spec.Hostname)
	}
	if spec != nil {
		cp := *spec
		spec = &cp
	}
	r.tunnel = spec
	return nil
}

// Tunnel returns what EnsureTunnel last ran; nil when cloudflared isn't.
func (r *Runtime) Tunnel() *node.TunnelSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tunnel == nil {
		return nil
	}
	cp := *r.tunnel
	return &cp
}

// Build implements node.Builder. It reads the whole source stream. A push
// is recorded as "push <ref> token=<token>" after the build.
func (r *Runtime) Build(_ context.Context, req node.BuildRequest) (node.Built, error) {
	n, err := io.Copy(io.Discard, req.Source)
	if err != nil {
		return node.Built{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("build %s", req.Tag)
	_, _ = fmt.Fprintf(req.Logs, "Received %d bytes of build context\n", n)
	if r.buildErr != nil {
		_, _ = fmt.Fprintf(req.Logs, "ERROR: %v\n", r.buildErr)
		return node.Built{}, r.buildErr
	}
	_, _ = fmt.Fprintf(req.Logs, "Built %s\n", req.Tag)
	r.images[req.Tag] = imageID(req.Tag)
	if p := req.Push; p != nil {
		r.op("push %s token=%s", p.Ref, p.Token)
		_, _ = fmt.Fprintf(req.Logs, "Pushed %s\n", p.Ref)
	}
	return node.Built{ID: r.images[req.Tag], Base: r.buildBase.Base, BaseDigest: r.buildBase.BaseDigest}, nil
}

// SetBuildBase makes builds report base (and its digest) as the image
// they were built on.
func (r *Runtime) SetBuildBase(base, digest string) {
	r.mu.Lock()
	r.buildBase = node.Built{Base: base, BaseDigest: digest}
	r.mu.Unlock()
}

// PruneBuildCache implements node.Builder, recorded as "prune cache
// <keepBytes>".
func (r *Runtime) PruneBuildCache(_ context.Context, keepBytes int64) (node.PruneResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("prune cache %d", keepBytes)
	return node.PruneResult{}, nil
}

// WarmBuilder implements node.Builder, recorded as "warm builder".
func (r *Runtime) WarmBuilder(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("warm builder")
	return nil
}

// ApplyRoutes implements node.Proxy, recorded as "routes <host->upstream
// ...>", followed by " certs <names>" when there are certificates.
func (r *Runtime) ApplyRoutes(ctx context.Context, routes []node.Route, certs []node.Certificate) error {
	r.mu.Lock()
	if h := r.routesHold; h != nil && h.match(routes) {
		r.mu.Unlock()
		h.once.Do(func() { close(h.held) })
		select {
		case <-h.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if r.routesErr != nil {
		return r.routesErr
	}
	parts := make([]string, 0, len(routes))
	for _, rt := range routes {
		to := rt.Upstream + rt.RedirectTo
		if rt.Stopped != "" && rt.RedirectTo == "" {
			to = "stopped"
		}
		parts = append(parts, strings.Join(rt.Hostnames, ",")+"->"+to)
	}
	op := "routes " + strings.Join(parts, " ")
	for _, c := range certs {
		op += " certs " + strings.Join(c.Names, ",")
	}
	r.op("%s", op)
	r.routes = slices.Clone(routes)
	r.certs = slices.Clone(certs)
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
	if demoPendingDomain(hostname) {
		return node.DomainCheck{ExpectedIPs: []string{"127.0.0.1"}, Error: hostname + " doesn't resolve to this server yet"}, nil
	}
	return node.DomainCheck{ExpectedIPs: []string{"127.0.0.1"}, ResolvedIPs: []string{"127.0.0.1"}, DNSOK: true, CertificateOK: true}, nil
}

// demoPendingDomain reports whether DOCK_FAKE_PENDING_DOMAINS (hostnames,
// comma-separated) names hostname: a `make dev` daemon keeps those
// waiting for DNS, so the UI shows the record to create. Read per check,
// which runs every few seconds at most per pending domain.
func demoPendingDomain(hostname string) bool {
	for h := range strings.SplitSeq(os.Getenv("DOCK_FAKE_PENDING_DOMAINS"), ",") {
		if strings.EqualFold(strings.TrimSpace(h), hostname) {
			return true
		}
	}
	return false
}

// SetHostStats fixes what HostStats returns. Until it is called, every
// sample adds a quarter-busy second of CPU on a 4-CPU, 8 GiB host whose
// used memory is broken down as on a real server (fakeMemoryParts).
func (r *Runtime) SetHostStats(h node.HostStats) {
	r.mu.Lock()
	r.host, r.hostSet = h, true
	r.mu.Unlock()
}

// SetContainerStats fixes what ContainerStats returns for the container
// with this ID (Time included). Running containers without one use 64 MiB
// and a quarter of a core.
func (r *Runtime) SetContainerStats(s node.ContainerStats) {
	r.mu.Lock()
	r.ctrStats[s.ID] = s
	r.mu.Unlock()
}

// AddTraffic queues what the next DrainTraffic returns.
func (r *Runtime) AddTraffic(t ...node.HostTraffic) {
	r.mu.Lock()
	r.traffic = append(r.traffic, t...)
	r.mu.Unlock()
}

// DrainTraffic implements node.Traffic: what AddTraffic queued since the
// last call.
func (r *Runtime) DrainTraffic(context.Context) ([]node.HostTraffic, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.traffic
	r.traffic = nil
	return t, nil
}

// StatsCalls counts HostStats and ContainerStats calls, so tests can see
// sampling start and stop.
func (r *Runtime) StatsCalls() int64 { return r.statsCalls.Load() }

// HostStats implements node.Stats.
func (r *Runtime) HostStats(context.Context) (node.HostStats, error) {
	r.statsCalls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hostSet {
		r.host.CPUs, r.host.MemoryTotal = 4, 8<<30
		r.host.Disks = []node.Disk{{Paths: []string{"/var/lib/docker", "/var/lib/dock"}, Total: 40 << 30, Used: 11 << 30}}
		r.host.CPUBusy += 25
		r.host.CPUTotal += 100
		var used uint64
		r.host.MemoryParts, used = r.fakeMemoryParts()
		r.host.MemoryAvailable = r.host.MemoryTotal - used
	}
	return r.host, nil
}

// fakeMemoryParts makes up a breakdown of used memory and its total: 48
// MiB per running app container, typical platform and system units and
// kernel memory. r.mu is held.
func (r *Runtime) fakeMemoryParts() (parts []node.MemoryPart, used uint64) {
	parts = []node.MemoryPart{
		{Group: node.MemoryPlatform, Name: "BuildKit", Bytes: 412 << 20},
		{Group: node.MemoryPlatform, Name: "Docker engine", Bytes: 118 << 20},
		{Group: node.MemoryPlatform, Name: "Caddy", Bytes: 46 << 20},
		{Group: node.MemoryPlatform, Name: "dock", Bytes: 24 << 20},
		{Group: node.MemorySystem, Name: "snapd", Bytes: 61 << 20},
		{Group: node.MemorySystem, Name: "multipathd", Bytes: 27 << 20},
		{Group: node.MemorySystem, Name: "systemd-journald", Bytes: 24 << 20},
		{Group: node.MemorySystem, Name: "login sessions", Bytes: 9 << 20},
		{Group: node.MemoryKernel, Name: "slab", Bytes: 196 << 20},
		{Group: node.MemoryKernel, Name: "page tables", Bytes: 24 << 20},
		{Group: node.MemoryKernel, Name: "reserves and other", Bytes: 96 << 20},
	}
	for id, c := range r.containers {
		if c.State == node.StateRunning {
			parts = append(parts, node.MemoryPart{Group: node.MemoryApps, Name: id, Service: c.Labels.Service, Bytes: 48 << 20})
		}
	}
	for _, p := range parts {
		used += p.Bytes
	}
	slices.SortStableFunc(parts, func(a, b node.MemoryPart) int { return cmp.Compare(b.Bytes, a.Bytes) })
	return parts, used
}

// ContainerStats implements node.Stats.
func (r *Runtime) ContainerStats(_ context.Context, ids []string) ([]node.ContainerStats, error) {
	r.statsCalls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []node.ContainerStats
	now := time.Now()
	for _, id := range ids {
		c, ok := r.containers[id]
		if !ok {
			continue
		}
		if s, ok := r.ctrStats[id]; ok {
			out = append(out, s)
			continue
		}
		if c.State != node.StateRunning {
			continue
		}
		s := node.ContainerStats{
			ID: id, Time: now, CPUUsec: uint64(now.Sub(c.StartedAt).Microseconds() / 4), //nolint:gosec // started in the past
			Memory: 64 << 20, MemoryLimit: uint64(r.specs[id].Resources.MemoryBytes), //nolint:gosec // limits are positive
		}
		out = append(out, s)
	}
	return out, nil
}

// demoLogLines is DOCK_FAKE_LOGS's content, read once.
var demoLogLines = sync.OnceValue(func() []string {
	path := os.Getenv("DOCK_FAKE_LOGS")
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
})

// demoLogs is the output a `make dev` daemon gives every container it
// starts when DOCK_FAKE_LOGS names a file: its lines, a few seconds apart
// and ending now, on stdout, or stderr for lines that start with "E ".
// Without it containers start silent, as in tests.
func demoLogs() []node.LogLine {
	lines := demoLogLines()
	out := make([]node.LogLine, 0, len(lines))
	now := time.Now()
	for i, l := range lines {
		stream := "stdout"
		if rest, ok := strings.CutPrefix(l, "E "); ok {
			stream, l = "stderr", rest
		}
		at := now.Add(-time.Duration(len(lines)-i) * 7 * time.Second)
		out = append(out, node.LogLine{Time: at, Stream: stream, Text: l})
	}
	return out
}

// EnsureSidecar records spec ("ensure-sidecar <name>" when it changed).
func (r *Runtime) EnsureSidecar(_ context.Context, spec node.SidecarSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sidecarErr != nil {
		return r.sidecarErr
	}
	if r.sidecars == nil {
		r.sidecars = map[string]node.SidecarSpec{}
	}
	if old, ok := r.sidecars[spec.Name]; ok && reflect.DeepEqual(old, spec) {
		return nil
	}
	r.sidecars[spec.Name] = spec
	r.op("ensure-sidecar %s", spec.Name)
	return nil
}

// RemoveSidecar forgets a sidecar ("remove-sidecar <name>" if it ran).
func (r *Runtime) RemoveSidecar(_ context.Context, name string, labels node.Labels) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sidecars[name]; ok && !s.Labels.Conflicts(labels) {
		delete(r.sidecars, name)
		r.op("remove-sidecar %s", name)
	}
	return nil
}

// Sidecars returns the running sidecars by name.
func (r *Runtime) Sidecars() map[string]node.SidecarSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.sidecars)
}

// SetSidecarErr makes EnsureSidecar fail with err (nil: succeed).
func (r *Runtime) SetSidecarErr(err error) {
	r.mu.Lock()
	r.sidecarErr = err
	r.mu.Unlock()
}

// SidecarHealth emits a sidecar's health_status event, as Docker sends
// when its check changes state.
func (r *Runtime) SidecarHealth(name, health string) {
	r.mu.Lock()
	spec, ok := r.sidecars[name]
	r.mu.Unlock()
	if !ok {
		return
	}
	r.Emit(node.ContainerEvent{ContainerID: name, Labels: spec.Labels, Action: "health_status", Health: health, Role: node.RoleSidecar, Time: time.Now()})
}
