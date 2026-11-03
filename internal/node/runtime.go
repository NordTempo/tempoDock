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
	Traffic
	Tunnel
	Analytics
}

// Tunnel runs a server's Cloudflare Tunnel connector, cloudflared, when
// the control plane manages its tunnel (ADR 0036).
type Tunnel interface {
	// EnsureTunnel runs cloudflared with spec's token, or stops and
	// removes it when spec is nil. It does nothing when cloudflared
	// already runs as spec says, so it is cheap enough for every
	// reconnect.
	EnsureTunnel(ctx context.Context, spec *TunnelSpec) error
}

// TunnelSpec is the tunnel a server's cloudflared runs.
type TunnelSpec struct {
	// Token is the tunnel's token from Cloudflare: a secret, which only
	// runs this tunnel. Never log it.
	Token string
	// Hostname is the tunnel's CNAME target, <id>.cfargotunnel.com, for
	// domain check messages.
	Hostname string
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
	// PruneImages removes dangling images (no tag left) that the platform
	// made, no container uses and whose ID isn't in req.Keep. Made by the
	// platform means built (LabelImage) or pulled from the platform
	// registry (req.Repos); no other image on the host is ever touched,
	// so a server shared with other work keeps its own. Images in use are
	// skipped, never forced.
	PruneImages(ctx context.Context, req PruneImagesRequest) (PruneResult, error)
	// DiskUsage says where Docker's data root and dock's data dir go on
	// this server (ADR 0035). Images under req's prune filter count as
	// reclaimable, as PruneImages would remove them. It asks Docker to
	// size every image and volume, which takes seconds: call it when
	// someone asks, never on a timer.
	DiskUsage(ctx context.Context, req PruneImagesRequest) (DiskUsage, error)

	// EnsureNetwork creates the bridge network, labelled with labels'
	// project and environment, if it doesn't exist. An existing network
	// labelled for a different project or environment is an error, so one
	// environment can never join another's network.
	EnsureNetwork(ctx context.Context, name string, labels Labels) error
	// RemoveNetwork removes an environment's network, so its subnet goes
	// back to the pool. Only a managed network whose labels match labels
	// (Labels.Matches) is removed; any other, or none, is ErrNotFound
	// (wrapped). Docker refuses while a container uses it (an error).
	RemoveNetwork(ctx context.Context, name string, labels Labels) error
	// EnsureVolume creates the named volume if it doesn't exist. An existing
	// volume labelled for a different service is an error. Volumes are never
	// removed by the runtime.
	EnsureVolume(ctx context.Context, name string, labels Labels) error

	// Start creates and starts a container. Restart policy is always
	// unless-stopped, logs use the local driver with rotation. With
	// spec.Health set the container gets Docker's own healthcheck, run
	// by the server's dock-probe (ADR 0043).
	Start(ctx context.Context, spec ContainerSpec) (Container, error)
	// Stop stops a container, waiting up to timeout for a graceful exit.
	Stop(ctx context.Context, id string, timeout time.Duration) error
	// Restart stops a running container (waiting up to timeout for a
	// graceful exit) and starts the same container again: same ID,
	// port and volumes. A container that isn't running is left alone
	// (ErrNotRunning, wrapped), so a stopped service is never started
	// by it. Core calls it for a container whose health check keeps
	// failing (ADR 0043).
	Restart(ctx context.Context, id string, timeout time.Duration) error
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

	// RunJob runs a one-off container to completion and removes it (ADR
	// 0019: backups and restores). It carries LabelRole RoleJob, so it is
	// never listed or reported as an app. A non-zero exit is not an error:
	// the result says how it ended. Cancelling ctx kills and removes it.
	RunJob(ctx context.Context, spec JobSpec) (JobResult, error)
}

// RoleJob is the LabelRole value of RunJob's containers.
const RoleJob = "job"

// RoleBuildKit is the LabelRole value of the BuildKit container.
const RoleBuildKit = "buildkit"

// RoleCloudflared is the LabelRole value of the cloudflared container
// (Tunnel).
const RoleCloudflared = "cloudflared"

// RoleAnalytics is the LabelRole value of the analytics store's
// container (ADR 0037).
const RoleAnalytics = "analytics"

// RoleRegistryGC is the LabelRole value of the registry's garbage
// collection container.
const RoleRegistryGC = "registry-gc"

// MaxJobOutput caps JobResult.Output: the tail of the job's output is kept.
const MaxJobOutput = 256 << 10

// JobSpec describes a one-off container.
type JobSpec struct {
	Name   string
	Image  string
	Labels Labels
	Env    map[string]string
	// Script runs as `/bin/sh -c Script`, replacing the image's entrypoint.
	Script string
	// Network is joined without aliases; empty means none.
	Network   string
	Mounts    []Mount
	Resources Resources
	// Stdin, when set, is copied to the job's standard input, which closes
	// at its end. A read error from it stops the job (SIGINT) instead, so a
	// job never takes a cut-short input for the whole (the control plane
	// backup streams its archive into restic this way, ADR 0021). Local
	// runtimes only: the node link doesn't carry it.
	Stdin io.Reader
}

// JobResult is how a job ended.
type JobResult struct {
	ExitCode int
	// Output is the tail (at most MaxJobOutput bytes) of stdout and stderr.
	Output string
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
	// PruneBuildCache removes build cache, least recently used first, until
	// at most keepBytes are left. A server that doesn't build, or whose
	// BuildKit isn't running, has nothing to prune.
	PruneBuildCache(ctx context.Context, keepBytes int64) (PruneResult, error)
}

// PruneImagesRequest says which dangling images Containers.PruneImages may
// remove.
type PruneImagesRequest struct {
	// Keep are image IDs a rollback may still need.
	Keep []string
	// Repos are repository prefixes of the platform registry
	// ("127.0.0.1:5000/dock/p"): a dangling image with a digest reference
	// under one was pulled from there by the platform. Docker's classic
	// image store lists images pulled by digest as dangling.
	Repos []string
}

// BuildCacheVolume holds BuildKit's state and build cache on a builder
// (build.CacheVolume).
const BuildCacheVolume = "dock-buildkit-cache"

// DiskUsage is where a server's disk goes, as Containers.DiskUsage reads
// it. Image bytes are unique to each image (its size less the layers it
// shares with others); shared layers are counted once, in ImagesShared,
// so the image fields add up to what images take.
type DiskUsage struct {
	// ImagesRunning are images containers use; ImagesKept other images
	// the platform made; ImagesReclaimable those PruneImages would
	// remove; ImagesOther images the platform didn't make.
	ImagesRunning, ImagesKept, ImagesReclaimable, ImagesOther, ImagesShared int64
	// Volumes are the platform's volumes, BuildCacheVolume included.
	Volumes []VolumeUsage
	// OtherVolumes are volumes the platform didn't make.
	OtherVolumes int64
	// Containers are containers' writable layers.
	Containers int64
	// DataDir is dock's data dir: database, keys, build logs, tool caches.
	DataDir int64
}

// VolumeUsage is one platform volume's size.
type VolumeUsage struct {
	Name string
	// Service is the volume's LabelService (a service's ID), empty for
	// the platform's own volumes.
	Service string
	// ControlPlane is its LabelControlPlane: on a Docker host shared by
	// two installs, the other's volumes aren't this one's.
	ControlPlane string
	Bytes        int64
}

// PruneResult is what a prune removed.
type PruneResult struct {
	Removed        int
	ReclaimedBytes int64
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

// Traffic counts the requests Caddy proxies to routes with Traffic set,
// from its access log (ADR 0033). Counts are kept in memory per hostname
// until the control plane drains them, once a minute.
type Traffic interface {
	// DrainTraffic returns what was counted per hostname since the last
	// call, and starts over. Hostnames without requests are left out.
	DrainTraffic(ctx context.Context) ([]HostTraffic, error)
}

// HostTraffic is what one hostname served between two drains.
type HostTraffic struct {
	Host     string
	Requests uint64
	// Status counts responses by class: 1xx and 2xx, 3xx, 4xx, 5xx.
	Status [4]uint64
	// Bytes is the response bytes sent.
	Bytes uint64
	// Duration is the time the requests took, summed.
	Duration time.Duration
	// Paths are the most requested method and path pairs, most first,
	// with IDs in paths replaced by ":id".
	Paths []PathCount
}

// PathCount is how often one method and path was requested.
type PathCount struct {
	Method   string
	Path     string
	Requests uint64
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
	// MemoryParts say where MemoryTotal-MemoryAvailable goes, the biggest
	// first. Empty when the server can't tell (no cgroup v2).
	MemoryParts []MemoryPart
}

// MemoryGroup sorts what uses a server's memory, for the console.
type MemoryGroup string

const (
	// MemoryApps are the containers of services.
	MemoryApps MemoryGroup = "apps"
	// MemoryPlatform is Dock's own infrastructure: the daemon, Caddy,
	// cloudflared, Docker itself, BuildKit, the registry and jobs.
	MemoryPlatform MemoryGroup = "platform"
	// MemoryKernel is kernel memory no process is charged for, and what
	// is left over.
	MemoryKernel MemoryGroup = "kernel"
	// MemorySystem is everything else on the server: other systemd units,
	// login sessions and containers Dock didn't create.
	MemorySystem MemoryGroup = "system"
)

// MemoryPart is one user of a server's memory. For processes it is what
// they hold that can't be reclaimed (anonymous memory and shmem/tmpfs);
// page cache is left out, as MemAvailable leaves it out of "used".
type MemoryPart struct {
	Group MemoryGroup
	Name  string
	// Service is the LabelService of an app's container: the service's
	// ID, which the control plane turns into its name.
	Service string
	Bytes   uint64
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
	// LabelImage ("true") marks images the platform built, so disk pruning
	// tells them from other images on a shared host once their tag is
	// gone. Docker copies image labels to containers made from the image;
	// nothing reads this one there.
	LabelImage = "dock.image"
	// LabelOwner is on the BuildKit container: the data dir of the dock
	// runtime that created it. Another runtime on the same Docker host
	// refuses to use or recreate it (one dock runtime per Docker host).
	LabelOwner = "dock.owner"
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
	// Health, when set, has Docker probe the running container (ADR
	// 0043). Nil means no ongoing check.
	Health *LivenessCheck
}

// LivenessCheck is the ongoing health check of a running container: Docker
// runs the server's dock-probe inside the container every Interval, so
// the daemon never polls (ADR 0043). The probe connects to Port on the
// container's own loopback:
//   - Path set: HTTP GET Path must answer 2xx or 3xx within Timeout.
//   - No Path: a TCP connect must succeed within Timeout.
//
// Retries failed probes in a row make the container unhealthy. Failures
// within StartPeriod after a start don't count; probes run every
// StartInterval then, so a healthy start is seen quickly.
type LivenessCheck struct {
	Port          int
	Path          string
	Interval      time.Duration
	Timeout       time.Duration
	Retries       int
	StartPeriod   time.Duration
	StartInterval time.Duration
}

// Health states of a container with a LivenessCheck, as Docker reports
// them. A container without one has none ("").
const (
	HealthStarting  = "starting"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
)

// ErrNotRunning is returned (wrapped) by Restart for a container that
// isn't running.
var ErrNotRunning = errors.New("container is not running")

// Mount attaches a named volume at Path.
type Mount struct {
	Volume   string
	Path     string
	ReadOnly bool
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
	// Health is the LivenessCheck's state (HealthStarting, HealthHealthy,
	// HealthUnhealthy), empty without one. HealthOutput is what the last
	// failed probe said, at most 256 bytes.
	Health       string
	HealthOutput string
}

// HealthCheck describes when a new container counts as healthy.
//   - Path set: HTTP GET http://127.0.0.1:HostPort{Path} returns 2xx/3xx.
//   - No Path, container publishes a port: TCP connect succeeds.
//   - Neither: the container stays running for Grace.
//
// Port (set without Path) instead probes that container port over TCP on
// the container's environment network, for services that publish nothing
// (database templates, ADR 0019).
//
// Interval is the steady probe interval; the first probes come sooner (the
// Docker runtime backs off from 50 ms).
type HealthCheck struct {
	Path     string
	Port     int
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
	// Action is Docker's action: start, die, stop, oom, restart, destroy,
	// ..., and "health_status" when a LivenessCheck's state changes.
	Action   string
	ExitCode int
	// Health is the new state of a "health_status" event: HealthHealthy
	// or HealthUnhealthy.
	Health string
	Time   time.Time
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
	// PathPrefix, when set, proxies only paths under it (e.g.
	// "/hooks/github/"); other paths on the hostnames get the 404 page.
	// The control plane's webhook route uses it (ADR 0018).
	PathPrefix string
	// ClientIPHeader, when set, is a request header Caddy sets to the
	// client's address as it determined it, replacing any the client
	// sent. Dock's public address uses it (ADR 0029). Local server only.
	ClientIPHeader string
	// Access, when set, keeps the route's hostnames from the public
	// (ADR 0030). Redirect routes ignore it.
	Access *Access
	// Traffic, when set, counts the route's requests for traffic
	// statistics (ADR 0033). Redirect routes ignore it.
	Traffic bool
	// TunnelOnly serves the route only to the server's Cloudflare tunnel,
	// on a direct server that runs one (ADR 0036): its hostnames resolve
	// to Cloudflare, so the public listeners neither serve them nor get
	// certificates for them.
	TunnelOnly bool
	// Analytics sends /_dock/a/* on some of the route's hostnames to the
	// server's analytics ingest (ADR 0037), each group tagged with the
	// IDs it stands for. Redirect routes ignore it.
	Analytics []AnalyticsHosts
}

// AnalyticsHosts are hostnames of a route that collect analytics, and the
// tag the ingest gets with their events: "site.service.release", the IDs
// of the site the hostnames belong to and the service and release that
// serve them.
type AnalyticsHosts struct {
	Hostnames []string
	Tag       string
}

// Access is how a route is protected: a password (PasswordHash set) or a
// Dock login (Key set), never both.
type Access struct {
	// Username and PasswordHash (bcrypt) ask visitors for HTTP basic auth.
	Username     string
	PasswordHash string
	// Key asks for a Dock login instead: Caddy has the server's access
	// check verify the visitor's session cookie, signed with Key
	// (internal/access), on every request, and visitors without one are
	// sent to LoginURL (Dock's /access page) to get one.
	Key      []byte
	LoginURL string
}

// Login reports whether the route needs a Dock login.
func (a *Access) Login() bool { return a != nil && len(a.Key) > 0 }

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

// Analytics is a server's part in Dock Analytics (ADR 0037). Every server
// has an ingest that appends events to its spool; the control plane reads
// spools (TailSpool), writes what it reads to the store and acknowledges
// it (AckSpool). The store runs on one server, the analytics server.
type Analytics interface {
	// SetAnalytics hands the server what its ingest needs: the salts of
	// today and tomorrow. A server without today's salt drops events.
	SetAnalytics(ctx context.Context, cfg AnalyticsConfig) error
	// TailSpool streams one of the spools' lines after position, in
	// batches of at most SpoolBatchEvents, oldest first: the backlog at
	// once, then new lines within a second of their arrival. The channel
	// closes when ctx ends or the stream fails.
	TailSpool(ctx context.Context, spool Spool, after uint64) (<-chan SpoolBatch, error)
	// AckSpool records that the store has everything of a spool up to
	// position, events in all; the spool deletes what it no longer needs.
	AckSpool(ctx context.Context, spool Spool, position uint64, events int) error
	// EnsureAnalyticsStore runs the store's container as spec says. It
	// returns once the container runs; the store may take a moment more
	// to answer.
	EnsureAnalyticsStore(ctx context.Context, spec AnalyticsStoreSpec) error
	// RemoveAnalyticsStore removes the store from this server: its
	// container, its data and its binary. Dock calls it once the store
	// moved to another server and the copy there was checked, and on a
	// server that holds a store it shouldn't (a move that stopped).
	RemoveAnalyticsStore(ctx context.Context) error
	// DialAnalyticsStore connects to the store's API on this server. On a
	// remote server the bytes go over the node link.
	DialAnalyticsStore(ctx context.Context) (net.Conn, error)
}

// AnalyticsBaseImage is what the store's container runs on: the C and
// C++ libraries DuckDB needs and a nonroot user, nothing else. Pinned by
// digest, a multi-platform index (linux/amd64 and linux/arm64).
const AnalyticsBaseImage = "gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f"

// Spool names one of a server's analytics spools.
type Spool string

const (
	// SpoolEvents holds events: page views, custom events, errors, Web
	// Vitals.
	SpoolEvents Spool = ""
	// SpoolReplay holds session replay chunks (phase A3), apart from the
	// events and capped on its own, so a full spool costs replays first.
	SpoolReplay Spool = "replay"
)

// SpoolBatchEvents caps a SpoolBatch.
const SpoolBatchEvents = 1000

// AnalyticsConfig is what SetAnalytics hands a server.
type AnalyticsConfig struct {
	// Salts are by UTC day number (days since 1970-01-01).
	Salts map[int64][]byte
	// Sites are the rules of every live site, by site ID, for the
	// ingest. A site missing here has none; nil keeps the rules the
	// server has (the control plane couldn't read them).
	Sites map[int64]SiteRules
}

// SiteRules are what a site's ingest leaves out: paths (* matches any
// run of characters) and client addresses or CIDR ranges; whether it
// collects errors and Web Vitals (on unless ErrorsOff); and the share of
// page loads it records for session replay (0: none) and whether
// replays mask all text.
type SiteRules struct {
	ExcludedPaths []string `json:",omitempty"`
	IgnoredIPs    []string `json:",omitempty"`
	ErrorsOff     bool     `json:",omitempty"`
	ReplayPercent int      `json:",omitempty"`
	MaskAllText   bool     `json:",omitempty"`
}

// SpoolBatch is events read from a spool.
type SpoolBatch struct {
	// Position is after the batch's last event; acknowledge it once the
	// store has them.
	Position uint64
	// Events are JSON lines of analytics.Event, newline-separated.
	Events []byte
	// Count is how many events Events holds.
	Count int
	// Skipped is set when events before the batch were dropped at the
	// spool's cap before they were read.
	Skipped bool
}

// AnalyticsStoreSpec is how the analytics store's container runs: the
// dock-analytics binary, mounted read-only into a container of
// AnalyticsBaseImage.
type AnalyticsStoreSpec struct {
	// Binary is the dock-analytics binary on this server: the release's
	// for its arch, which the control plane downloads when analytics is
	// turned on. Over the node link it names the control plane's copy,
	// sent to the server once (by SHA256).
	Binary string
	// SHA256 is the binary's hex SHA-256.
	SHA256 string
	// Token is the install's analytics token, which every call but its
	// health check needs.
	Token string
	// MemoryBytes is the container's memory limit; the store's DuckDB
	// limit and trim mark are derived from it.
	MemoryBytes int64
}
