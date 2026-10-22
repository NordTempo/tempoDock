// Package node is the boundary between the control plane and a server.
//
// The control plane (internal/core) only ever talks to a server through the
// Runtime interface. On the control plane's own server that is Local, a plain
// in-process implementation. Remote servers (M3) get a client that sends the
// same calls over the node connection. Nothing outside this package tree
// imports Docker, BuildKit or Caddy.
package node

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// Runtime is everything the control plane needs from one server.
type Runtime interface {
	Containers
	Builder
	Proxy
	Stats
}

// Containers manages platform-owned containers, networks and volumes.
// Implementations must only ever see or touch resources carrying the
// LabelManaged label.
type Containers interface {
	// Pull makes an image available locally, writing progress to logs.
	// It returns the image's content-addressed ID (sha256:...). At most one
	// PullOptions is used; none pulls anonymously.
	Pull(ctx context.Context, ref string, logs io.Writer, opts ...PullOptions) (string, error)
	// HasImage reports whether ref is present locally.
	HasImage(ctx context.Context, ref string) (bool, error)
	// RemoveImage removes the local image reference ref (a tag or a
	// digest reference); the image itself goes with its last reference.
	// An image a container uses is never removed (an error). A ref that
	// isn't there is ErrNotFound (wrapped).
	RemoveImage(ctx context.Context, ref string) error

	// EnsureNetwork creates the bridge network, labelled with labels'
	// project and environment, if it doesn't exist. An existing network
	// labelled for a different project or environment is an error, so one
	// environment can never join another's network.
	EnsureNetwork(ctx context.Context, name string, labels Labels) error
	// EnsureVolume creates the named volume if it doesn't exist. An existing
	// volume labelled for a different service is an error. Volumes are never
	// removed by the runtime.
	EnsureVolume(ctx context.Context, name string, labels Labels) error

	// Start creates and starts a container. Restart policy is always
	// unless-stopped, logs use the local driver with rotation.
	Start(ctx context.Context, spec ContainerSpec) (Container, error)
	// Stop stops a container, waiting up to timeout for a graceful exit.
	Stop(ctx context.Context, id string, timeout time.Duration) error
	// Remove removes a stopped container. Never removes volumes.
	Remove(ctx context.Context, id string) error
	// Inspect returns the current state of one container.
	Inspect(ctx context.Context, id string) (Container, error)
	// List returns all platform-managed containers, running or not.
	List(ctx context.Context) ([]Container, error)
	// ReleaseContainers returns the IDs of the managed containers labelled
	// with want.Release whose labels match want (Labels.Matches), running
	// or not: one filtered list, without inspecting every container like
	// List.
	ReleaseContainers(ctx context.Context, want Labels) ([]string, error)

	// HealthCheck blocks until the container passes hc or hc.Timeout expires.
	// It runs on the server that hosts the container, since published ports
	// are bound to that server's loopback interface.
	HealthCheck(ctx context.Context, c Container, hc HealthCheck) error

	// Logs streams a container's logs. The channel closes when the stream ends
	// (immediately after the backlog when opts.Follow is false) or ctx ends.
	Logs(ctx context.Context, id string, opts LogOptions) (<-chan LogLine, error)
	// Events streams lifecycle events for platform-managed containers until
	// ctx ends. Implementations reconnect on their own; the channel only
	// closes when ctx is done.
	Events(ctx context.Context) (<-chan ContainerEvent, error)

	// Dial opens a TCP connection to a port of a platform-managed service
	// container (ADR 0016), e.g. the database panel reaching Postgres. It
	// dials the container's IP on its environment network from the host
	// that runs it; on a remote server the bytes stream over the node link.
	// It is not an open proxy: the target must carry LabelManaged and a
	// service label, and no other network or address is ever used. ctx only
	// bounds connecting; closing the returned conn ends it. A container
	// that doesn't exist or isn't a service is ErrNotFound (wrapped).
	Dial(ctx context.Context, req DialRequest) (net.Conn, error)
}

// PullOptions are the optional settings of Containers.Pull.
type PullOptions struct {
	// Token is a registry bearer token the control plane minted for this
	// pull (internal/registry, ADR 0014 section 4): scoped to the image's
	// repository, valid for minutes. Empty pulls anonymously.
	Token string
}

// DialRequest names what Containers.Dial connects to.
type DialRequest struct {
	// ContainerID is the full ID of a running service container.
	ContainerID string
	// Port is the container port (e.g. 5432), not a published host port.
	Port int
}

// Builder turns source code into an image available to this server.
type Builder interface {
	// Build builds req.Source and tags the result as req.Tag. Build output
	// is written to req.Logs. Returns the image ID (sha256:...).
	Build(ctx context.Context, req BuildRequest) (string, error)
}

// Proxy owns the server's public HTTP(S) routing.
type Proxy interface {
	// ApplyRoutes replaces the full routing table atomically. If the new
	// config is rejected, the previous one stays in effect.
	ApplyRoutes(ctx context.Context, routes []Route) error
	// CheckDomain reports whether hostname's DNS points at this server and
	// whether a valid certificate is being served for it.
	CheckDomain(ctx context.Context, hostname string) (DomainCheck, error)
}

// Stats reads resource usage straight from the kernel (/proc and cgroup
// files), never through Docker, so sampling stays a few small file reads.
// Counters are cumulative; callers turn two samples into rates.
type Stats interface {
	// HostStats samples the whole server.
	HostStats(ctx context.Context) (HostStats, error)
	// ContainerStats samples the given containers (full IDs). Containers
	// that don't exist (any more) are left out of the result.
	ContainerStats(ctx context.Context, ids []string) ([]ContainerStats, error)
}

// HostStats is one sample of a server's resources.
type HostStats struct {
	// CPUBusy and CPUTotal are cumulative CPU time across all cores, in
	// clock ticks; busy/total between two samples is the CPU use.
	CPUBusy  uint64
	CPUTotal uint64
	CPUs     int
	// MemoryTotal and MemoryAvailable are bytes (MemAvailable, as `free`).
	MemoryTotal     uint64
	MemoryAvailable uint64
	// Load1 is the one-minute load average.
	Load1 float64
	// Disks are the filesystems of Docker's data root (first) and dock's
	// data dir, once each when they are the same filesystem.
	Disks []Disk
}

// Disk is the usage of one filesystem.
type Disk struct {
	// Paths are the directories on it that dock uses.
	Paths []string
	Total uint64
	Used  uint64
}

// ContainerStats is one sample of a container's cgroup.
type ContainerStats struct {
	ID string
	// Time is when the node took the sample, for rates over CPUUsec and IO.
	Time time.Time
	// CPUUsec is cumulative CPU time in microseconds.
	CPUUsec uint64
	// Memory is the working set in bytes: usage minus inactive page
	// cache, the number `docker stats` shows.
	Memory uint64
	// MemoryLimit is the hard limit in bytes; 0 means none.
	MemoryLimit uint64
	// IORead and IOWrite are cumulative block IO bytes.
	IORead  uint64
	IOWrite uint64
}

// Docker label keys put on every platform-managed resource.
const (
	LabelManaged     = "dock.managed"
	LabelProject     = "dock.project"
	LabelEnvironment = "dock.env"
	LabelService     = "dock.service"
	LabelRelease     = "dock.release"
	// LabelControlPlane is the install ID of the control plane that created
	// the resource. Control planes ignore resources labelled for another
	// one, so several can share a Docker host.
	LabelControlPlane = "dock.control_plane"
	// LabelRole marks platform infrastructure (e.g. the BuildKit container).
	// Containers carrying it are not apps: Containers never lists, returns
	// or streams events for them.
	LabelRole = "dock.role"
)

// Labels identify what a container or volume belongs to. Values are IDs from
// the platform DB, so renames never orphan resources. ControlPlane names the
// control plane whose DB they are from; resources created before it existed
// have none.
type Labels struct {
	ControlPlane string
	Project      string
	Environment  string
	Service      string
	Release      string
}

// Map renders the labels as Docker labels, including LabelManaged.
func (l Labels) Map() map[string]string {
	m := map[string]string{LabelManaged: "true"}
	for k, v := range map[string]string{
		LabelControlPlane: l.ControlPlane,
		LabelProject:      l.Project,
		LabelEnvironment:  l.Environment,
		LabelService:      l.Service,
		LabelRelease:      l.Release,
	} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// Conflicts reports whether existing (read from a resource) belongs to
// something other than want: a label set on both sides that differs.
// Unset labels never conflict, so resources created before a label existed
// stay usable.
func (l Labels) Conflicts(want Labels) bool {
	differs := func(have, want string) bool { return have != "" && want != "" && have != want }
	return differs(l.ControlPlane, want.ControlPlane) || differs(l.Project, want.Project) ||
		differs(l.Environment, want.Environment) || differs(l.Service, want.Service) || differs(l.Release, want.Release)
}

// Matches reports whether a resource labelled l is one that want describes:
// every label set in want is the same on l. A resource without a control
// plane label (created before it existed) matches only if everything else
// does, so a control plane still finds its older containers but never one
// labelled for another control plane.
func (l Labels) Matches(want Labels) bool {
	same := func(have, want string) bool { return want == "" || have == want }
	return (l.ControlPlane == "" || same(l.ControlPlane, want.ControlPlane)) &&
		same(l.Project, want.Project) && same(l.Environment, want.Environment) &&
		same(l.Service, want.Service) && same(l.Release, want.Release)
}

// LabelsFrom reads platform labels back from a Docker label map.
func LabelsFrom(m map[string]string) Labels {
	return Labels{
		ControlPlane: m[LabelControlPlane],
		Project:      m[LabelProject],
		Environment:  m[LabelEnvironment],
		Service:      m[LabelService],
		Release:      m[LabelRelease],
	}
}

// ContainerSpec describes a container to start.
type ContainerSpec struct {
	Name   string
	Image  string
	Labels Labels
	Env    map[string]string
	// Cmd overrides the image's command when non-empty (run via `sh -c`).
	Cmd string
	// Network is joined with Aliases, so services reach each other by name.
	Network string
	Aliases []string
	// Port is the container port to publish on 127.0.0.1 with an ephemeral
	// host port. Zero means nothing is published.
	Port int
	// HostPort is the fixed 127.0.0.1 port Port is published on. Zero picks
	// an ephemeral port, which changes on every restart, so routed services
	// must always get a fixed one.
	HostPort  int
	Mounts    []Mount
	Resources Resources
}

// Mount attaches a named volume at Path.
type Mount struct {
	Volume string
	Path   string
}

// Resources are hard limits for a container. Zero means unlimited.
type Resources struct {
	MemoryBytes int64
	NanoCPUs    int64
}

// ContainerState mirrors Docker's container states.
type ContainerState string

const (
	StateCreated    ContainerState = "created"
	StateRunning    ContainerState = "running"
	StateRestarting ContainerState = "restarting"
	StateExited     ContainerState = "exited"
	StatePaused     ContainerState = "paused"
	StateDead       ContainerState = "dead"
)

// Container is the observed state of a platform-managed container.
type Container struct {
	ID     string
	Name   string
	Image  string
	Labels Labels
	State  ContainerState
	// HostPort is the 127.0.0.1 port Port is published on, or zero.
	HostPort     int
	StartedAt    time.Time
	ExitCode     int
	RestartCount int
	OOMKilled    bool
}

// HealthCheck describes when a new container counts as healthy.
//   - Path set: HTTP GET http://127.0.0.1:HostPort{Path} returns 2xx/3xx.
//   - No Path, container publishes a port: TCP connect succeeds.
//   - Neither: the container stays running for Grace.
//
// Interval is the steady probe interval; the first probes come sooner (the
// Docker runtime backs off from 50 ms).
type HealthCheck struct {
	Path     string
	Timeout  time.Duration
	Interval time.Duration
	// Grace is how long a portless container must stay up to count as healthy.
	Grace time.Duration
}

// LogOptions select which log lines to stream.
type LogOptions struct {
	Follow bool
	// Tail limits the backlog to the last N lines. Zero means all.
	Tail  int
	Since time.Time
}

// LogLine is one line of container output.
type LogLine struct {
	Time   time.Time
	Stream string // "stdout" or "stderr"
	Text   string
}

// ContainerEvent is a lifecycle change of a platform-managed container.
type ContainerEvent struct {
	ContainerID string
	Labels      Labels
	// Action is Docker's action: start, die, stop, oom, restart, destroy, ...
	Action   string
	ExitCode int
	Time     time.Time
}

// BuildRequest describes one image build.
type BuildRequest struct {
	// Source is a tar stream of the build context (uncompressed or gzip).
	Source io.Reader
	// Dockerfile is the Dockerfile path within the context. Empty means
	// "Dockerfile" if it exists, otherwise a Railpack zero-config build.
	Dockerfile string
	// BuildCommand and StartCommand are passed to Railpack builds.
	BuildCommand string
	StartCommand string
	// BuildArgs are the service's variables. Every build step can read them
	// as BuildKit secrets (id = name); Dockerfile builds also get the ones
	// the Dockerfile declares with ARG as build args, and Railpack reads its
	// RAILPACK_* settings from them.
	BuildArgs map[string]string
	// CacheScope namespaces BuildKit cache mounts, so one project's builds
	// can't read or poison another's. Use one scope per project and
	// environment; only letters, digits and _ are allowed. Empty shares a
	// single "unscoped" namespace.
	CacheScope string
	Tag        string
	Logs       io.Writer
	// Push, when set, also pushes the image to a registry. The image is
	// still loaded into the local Docker as Tag, so this server can start
	// it without pulling.
	Push *PushTarget
}

// PushTarget is where a build pushes its image (ADR 0014 section 4 and
// amendment). The pushed manifest's digest is in the build log and is read
// back from the registry by the control plane (registry.Client.Digest), so
// other servers and promotions pull it as <repo>@sha256:...
type PushTarget struct {
	// Ref is the full image reference to push, with a tag:
	// 127.0.0.1:5000/<project>/<service>:<env>-<release>.
	Ref string
	// Token is a bearer token with push access to Ref's repository,
	// minted by the control plane for this build (registry.PushTokenTTL).
	Token string
}

// Route sends traffic for Hostnames to Upstream (host:port).
type Route struct {
	Hostnames []string
	Upstream  string
	// RedirectTo, when set, permanently redirects to this hostname instead
	// of proxying.
	RedirectTo string
}

// DomainCheck is the observed state of one hostname on this server.
type DomainCheck struct {
	// ExpectedIPs are this server's public addresses that DNS should use.
	ExpectedIPs []string
	// ResolvedIPs are what the hostname currently resolves to.
	ResolvedIPs []string
	// DNSOK is true when ResolvedIPs includes an expected address.
	DNSOK bool
	// CertificateOK is true when a valid certificate is served for hostname
	// (always false when TLS is disabled).
	CertificateOK bool
	// Error explains a failed check in plain words.
	Error string
}

// ErrNotFound is returned (wrapped) when a container, image or other resource
// does not exist or is not platform-managed.
var ErrNotFound = errors.New("not found")
