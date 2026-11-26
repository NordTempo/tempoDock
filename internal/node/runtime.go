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
	"path"
	"slices"
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
	Prober
	Scanner
}

// Prober runs uptime checks (ADR 0055): the control plane asks a server
// other than the one serving a site to fetch it.
type Prober interface {
	// Probe sends one GET to req.URL and says whether the answer was the
	// expected one in time. Redirects aren't followed (req.Cookie relies
	// on it), nothing is kept
	// alive, and addresses of the server itself, link-local, multicast and
	// cloud metadata addresses are refused after DNS. A site that is down
	// is a result (Up false, Error set), not an error; the error is for
	// a request that can't be made (a malformed URL, an older node).
	Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)
}

// ProbeRequest is one uptime probe.
type ProbeRequest struct {
	// URL is http:// or https://.
	URL string
	// ExpectStatus is the status that means up: an exact code (100-599),
	// or a class: ExpectClass2xx or ExpectClass3xx. 0 means 200.
	ExpectStatus int
	// Timeout bounds the whole request, connecting included.
	Timeout time.Duration
	// Cookie, when set, is the request's Cookie header: the session that
	// lets a probe through a Dock login (access.ProbeCookie). It goes to
	// URL's host only, as redirects aren't followed, and never over the
	// node link: such a probe runs on the control plane.
	Cookie string
}

// Status classes for ProbeRequest.ExpectStatus: any 2xx or any 3xx code.
const (
	ExpectClass2xx = 2
	ExpectClass3xx = 3
)

// StatusMatches reports whether status is what expect asks for (see
// ProbeRequest.ExpectStatus).
func StatusMatches(expect, status int) bool {
	switch expect {
	case 0:
		return status == 200
	case ExpectClass2xx, ExpectClass3xx:
		return status/100 == expect
	}
	return status == expect
}

// ProbeResult is what one probe saw.
type ProbeResult struct {
	// Up: the expected status arrived within the timeout.
	Up bool
	// Status is the answer's status code; 0 when none came.
	Status int
	// Latency is until the answer's headers, or until the probe gave up.
	Latency time.Duration
	// Error says in plain words why the probe isn't up: a refused
	// address, DNS, a timeout, a TLS failure or an unexpected status.
	Error string
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
	// PruneImages removes the images no release needs (ADR 0035
	// amendment 2): with req.OnlyRollbacks just req.Rollbacks, otherwise
	// every image whose ID isn't in req.Keep that the platform made
	// (built, LabelImage, or pulled from its registry, req.Repos), that a
	// release used (req.Unneeded), or that is another version of one of
	// the platform's own images (req.Pinned), tagged or not. An image a
	// container uses (running or stopped) is never removed, nor one made
	// in the last hour (a build may not have recorded it yet), and no
	// other image on the host is ever touched, so a server shared with
	// other work keeps its own. Nothing is forced.
	PruneImages(ctx context.Context, req PruneImagesRequest) (PruneResult, error)
	// DiskUsage says where Docker's data root and dock's data dir go on
	// this server (ADR 0035). Images PruneImages would remove with req
	// count as reclaimable, req.Rollbacks on their own. With req.Tool it
	// also sizes what cut-off downloads left in the vulnerability
	// database's volume (a job in that image). It asks Docker to size
	// every image and volume, which takes seconds: call it when someone
	// asks, never on a timer.
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
	// removed by the runtime except through RemoveVolume, which core calls
	// only for an archived preview environment's volumes (ADR 0056).
	EnsureVolume(ctx context.Context, name string, labels Labels) error
	// RemoveVolume removes a managed volume whose labels match labels
	// (Labels.Matches, like RemoveNetwork), so a closed preview
	// environment's data goes with it (ADR 0056). Any other volume, or
	// none, is ErrNotFound (wrapped). Docker refuses while a container
	// uses it (an error); it is never forced.
	RemoveVolume(ctx context.Context, name string, labels Labels) error

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
	// Resume starts a stopped managed container again: same ID, port and
	// volumes. Core calls it to wake a sleeping environment, whose
	// containers it stopped with Stop. A running one is left alone.
	Resume(ctx context.Context, id string) error
	// Remove removes a stopped container. Its anonymous volumes (an
	// image's VOLUME no Dock volume covers) go with it, unless
	// opts.KeepAnonymousVolumes. Never removes a named volume.
	Remove(ctx context.Context, id string, opts RemoveOptions) error
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
	// ArchiveLogs copies a managed container's whole log into its
	// release's archive in this server's data dir (ADR 0046), as a new
	// gzip member of logs/<service>/<release>.jsonl.gz, then applies
	// limits to that service's archives (oldest files first). Core calls
	// it between Stop and Remove, so the archive holds everything the
	// container wrote. The service and release come from the container's
	// labels and are returned with the bytes written, and with an error
	// once they are known.
	ArchiveLogs(ctx context.Context, id string, limits LogLimits) (LogArchive, error)
	// ArchivedLogs streams a release's archived lines, oldest first; the
	// channel closes after the last one or when ctx ends. A release
	// without an archive is ErrNotFound (wrapped).
	ArchivedLogs(ctx context.Context, q LogArchiveQuery) (<-chan LogLine, error)
	// PruneLogs applies limits to every service's archives on this
	// server and removes the archives of services not in KeepServices
	// (the retention pass, hourly).
	PruneLogs(ctx context.Context, p LogPrune) (PruneResult, error)
	// Events streams lifecycle events for platform-managed containers until
	// ctx ends. Implementations reconnect on their own; the channel only
	// closes when ctx is done.
	Events(ctx context.Context) (<-chan ContainerEvent, error)

	// Dial opens a TCP connection to a port of a platform-managed service
	// container (ADR 0016), e.g. the database panel reaching Postgres. It
	// dials the container's IP on its environment network from the host
	// that runs it; on a remote server the bytes stream over the node link.
	// It is not an open proxy: the target must carry LabelManaged and a
	// service label (an app container, or a service's sidecar by its
	// name), and no other network or address is ever used. ctx only
	// bounds connecting; closing the returned conn ends it. A container
	// that doesn't exist or isn't a service is ErrNotFound (wrapped).
	Dial(ctx context.Context, req DialRequest) (net.Conn, error)

	// RunJob runs a one-off container to completion and removes it (ADR
	// 0019: backups and restores). It carries LabelRole RoleJob, so it is
	// never listed or reported as an app. A non-zero exit is not an error:
	// the result says how it ended. Cancelling ctx kills and removes it.
	// spec.Pause freezes service containers for exactly the job's run
	// (ADR 0047: snapshots).
	// With spec.Stream it is connected to its caller while it runs (`dock
	// run`, ADR 0048; on a remote server since node protocol 16).
	RunJob(ctx context.Context, spec JobSpec) (JobResult, error)

	// EnsureSidecar runs a container Dock owns next to a service (ADR
	// 0093: a Postgres database's WAL archiver): spec's image and
	// arguments, dock-probe (for spec.Health) and the server's own dock
	// binary bind-mounted read-only at ProbePath and DockPath, restarted
	// by Docker. A new dock version replaces it. It does
	// nothing when the container already runs as spec says, so it is
	// cheap enough for every release that goes active, and replaces it
	// otherwise. It carries LabelRole RoleSidecar: never listed or
	// returned as an app, but Events streams its health_status, die and
	// start events with Role set. On a remote server since node protocol
	// 33; an older node's error says to upgrade it.
	EnsureSidecar(ctx context.Context, spec SidecarSpec) error
	// RemoveSidecar stops and removes the sidecar named name. One that
	// isn't there, isn't a sidecar or carries labels other than labels
	// (another control plane's) is left alone without an error.
	RemoveSidecar(ctx context.Context, name string, labels Labels) error
}

// SidecarSpec is a sidecar container (EnsureSidecar). Env may hold
// secrets: Docker shows it only to those who can run Docker.
type SidecarSpec struct {
	Name   string
	Image  string
	Labels Labels
	Env    map[string]string
	// Args replace the image's command, without a shell.
	Args      []string
	Network   string
	Aliases   []string
	Mounts    []Mount
	Resources Resources
	Health    *LivenessCheck
}

// RoleJob is the LabelRole value of RunJob's containers.
const RoleJob = "job"

// RoleSidecar is the LabelRole value of EnsureSidecar's containers.
const RoleSidecar = "sidecar"

// ProbePath is where a sidecar (and an app container with a health
// check) finds dock-probe.
const ProbePath = "/.dock-probe"

// RoleBuildKit is the LabelRole value of the BuildKit container.
const RoleBuildKit = "buildkit"

// RoleCloudflared is the LabelRole value of the cloudflared container
// (Tunnel).
const RoleCloudflared = "cloudflared"

// RoleAnalytics is the LabelRole value of the analytics store's
// container (ADR 0037).
const RoleAnalytics = "analytics"

// RoleTraces is the LabelRole value of the trace store's container and
// volume (dock-analytics traces): spans for tracing over OTLP, in a
// container of their own beside the analytics store.
const RoleTraces = "traces"

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
	// Network is joined without aliases; empty means none at all: the
	// job has only loopback. A job that needs the internet names a
	// managed network, which the app firewall covers.
	Network   string
	Mounts    []Mount
	Resources Resources
	// Stdin, when set, is copied to the job's standard input, which closes
	// at its end. A read error from it stops the job (SIGINT) instead, so a
	// job never takes a cut-short input for the whole (the control plane
	// backup streams its archive into restic this way, ADR 0021). Local
	// runtimes only: the node link doesn't carry it.
	Stdin io.Reader
	// Pause holds full IDs of service containers to freeze (docker pause,
	// the cgroup freezer) while the job runs, so it can copy their volumes
	// as of one point in time without restarting them (ADR 0047:
	// snapshots). They are paused just before the job starts and always
	// unpaused when it ends: on success, a non-zero exit, an error or ctx
	// ending. Every ID must be an app container of this control plane
	// (LabelManaged, no LabelRole, a LabelControlPlane that matches
	// Labels.ControlPlane or none); one that isn't, or doesn't exist, is an
	// ErrNotFound (wrapped) before anything is paused. A container that
	// isn't running (exited) is skipped, as nothing writes to it, and one
	// already paused is left paused: only what RunJob paused is unpaused.
	// If pausing one fails, those already paused are unpaused and the job
	// doesn't run. A runtime that crashed while holding containers paused
	// unpauses them when it starts again (the Docker runtime's
	// UnpauseStranded).
	Pause []string
	// Stream, when set, connects the job to its caller while it runs
	// (`dock run`, ADR 0048): output as it is written, and optionally a
	// terminal and input. Not together with Stdin.
	Stream *JobStream
	// Sync bind-mounts the server's dock-sync read-only at SyncPath, for
	// the passes that copy a volume to another server (ADR 0063). A
	// server without one fails the job with ErrNoSync (wrapped).
	Sync bool
	// Dock bind-mounts the server's own dock binary read-only at DockPath,
	// for point-in-time restore's base backups and restores (ADR 0093).
	// A server without one fails the job with ErrNoDock (wrapped). On a
	// remote server since node protocol 33.
	Dock bool
}

// SyncPath is where JobSpec.Sync mounts dock-sync in a job.
const SyncPath = "/.dock-sync"

// DockPath is where JobSpec.Dock and sidecars mount the dock binary.
const DockPath = "/.dock"

// ErrNoDock means a server's runtime doesn't know where its dock binary
// is, so point-in-time restore can't run there.
var ErrNoDock = errors.New("this server's runtime doesn't know its dock binary, so point-in-time restore can't run there")

// ErrNoSync means a server's dock has no dock-sync to run (a build
// without `make build`).
var ErrNoSync = errors.New("this server's dock has no dock-sync (build it with `make build`)")

// JobStream connects a running job to its caller. JobResult.Output still
// holds the tail of the output.
type JobStream struct {
	// Stdout gets the job's standard output as it is written; with TTY,
	// everything its terminal shows. Stderr gets its standard error
	// (without TTY); nil sends it to Stdout. Writes come from one
	// goroutine; a failing write stops the job.
	Stdout, Stderr io.Writer
	// Stdin, when set, is the job's standard input, passed on as it
	// arrives; its end (EOF or an error) closes the job's input. Unlike
	// JobSpec.Stdin the job may end before reading all of it, and RunJob
	// doesn't wait for it to end: the caller ends it after RunJob returns.
	Stdin io.Reader
	// TTY gives the job a terminal of Size, resized on every value from
	// Resize (which may be nil).
	TTY    bool
	Size   TermSize
	Resize <-chan TermSize
}

// TermSize is a terminal's size in characters.
type TermSize struct{ Cols, Rows uint16 }

// JobResult is how a job ended.
type JobResult struct {
	ExitCode int
	// Output is the tail (at most MaxJobOutput bytes) of stdout and stderr.
	Output string
}

// RemoveOptions are the settings of Containers.Remove.
type RemoveOptions struct {
	// KeepAnonymousVolumes keeps the container's anonymous volumes, for a
	// container that served (ADR 0052): a Postgres image without a Dock
	// volume keeps its data in one, so removing it would delete the data
	// for good. They stay as the service's one kept copy: the same call
	// removes the anonymous volumes an earlier one kept for the service,
	// once no container uses them. Anonymous volumes of containers created
	// before ADR 0052 carry no labels and are always left alone. Without
	// it (a release that never went live) they are removed.
	KeepAnonymousVolumes bool
}

// AnonymousPaths returns the paths of declared (an image's VOLUMEs) that
// no mount covers, sorted: Docker gives each container an anonymous
// volume at each. A mount covers only its own path, not paths below it.
func AnonymousPaths(declared []string, mounts []Mount) []string {
	var out []string
	for _, p := range declared {
		p = path.Clean(p)
		if !slices.ContainsFunc(mounts, func(m Mount) bool { return path.Clean(m.Path) == p }) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// PullOptions are the optional settings of Containers.Pull.
type PullOptions struct {
	// Token is a registry bearer token the control plane minted for this
	// pull (internal/registry, ADR 0014 section 4): scoped to the image's
	// repository, valid for minutes. Empty pulls anonymously.
	Token string
	// Username and Password sign in to any other registry: the org's
	// credential for the image's registry host (ADR 0051). Password is a
	// secret; never log it. Unused when Token is set.
	Username, Password string
}

// DialRequest names what Containers.Dial connects to.
type DialRequest struct {
	// ContainerID is the full ID of a running service container, or a
	// sidecar's name.
	ContainerID string
	// Port is the container port (e.g. 5432), not a published host port.
	Port int
	// Forwarded marks a connection a server forwards to a service that
	// moved away (ADR 0063): a remote server counts it against its
	// forwarded connections, not against the dials of the data panel.
	Forwarded bool
}

// Builder turns source code into an image available to this server.
type Builder interface {
	// Build builds req.Source and tags the result as req.Tag. Build output
	// is written to req.Logs. Returns the image ID (sha256:...) and, for a
	// Dockerfile build, the image it was built on.
	Build(ctx context.Context, req BuildRequest) (Built, error)
	// PruneBuildCache removes build cache, least recently used first, until
	// at most keepBytes are left. A server that doesn't build, or whose
	// BuildKit isn't running, has nothing to prune.
	PruneBuildCache(ctx context.Context, keepBytes int64) (PruneResult, error)
	// WarmBuilder fetches what builds on this server need before the
	// first one does: Railpack's images (ADR 0089 amendment), once per
	// set of them, so a call when they are fetched already returns at
	// once. Core calls it for builders only; builds work without it.
	WarmBuilder(ctx context.Context) error
}

// PruneImagesRequest says which images Containers.PruneImages may remove.
type PruneImagesRequest struct {
	// Keep are image IDs a rollback may still need.
	Keep []string
	// Repos are repository prefixes of the platform registry
	// ("127.0.0.1:5000/dock/p"): an image with a digest reference under
	// one was pulled from there by the platform.
	Repos []string
	// Unneeded are image IDs releases on this server used that no
	// rollback needs any more: out of their service's rollback window, or
	// of an archived service. Images from other registries are only
	// known this way.
	Unneeded []string
	// Protect are references ("alpine:latest") whose images are never
	// removed, whatever else says they may go ([retention] keep_images).
	Protect []string
	// Pinned are the images the platform runs its own work in, by
	// reference with a digest (BuildKit, the scanners, restic, ...):
	// other versions of their repositories, left by updates, go.
	Pinned []string
	// Rollbacks are image IDs of releases in a rollback window that the
	// platform registry has too, other than the active release and the
	// one before it: a rollback to one pulls it again. DiskUsage sizes
	// them; PruneImages removes them only with OnlyRollbacks, and then
	// nothing else.
	Rollbacks     []string
	OnlyRollbacks bool
	// Tool is a small image with a shell (busybox's sh, du and rm) for
	// DiskUsage to size the vulnerability database's leftovers with.
	Tool string
}

// The memory breakdown's names of the stores' containers (MemoryPart.Name).
const (
	MemoryAnalyticsStore = "analytics store"
	MemoryTraceStore     = "trace store"
)

// ScanDatabaseMemory caps the jobs that write a builder's vulnerability
// database (download, refresh, import): the most scanning takes at once.
// Unpacking Grype's archive (200 MB packed, 3.4 GB unpacked, 2026-11-11)
// goes through the page cache the job is charged for: it was killed at
// 1 GB and passed at 1.25 GB, so 2 GB leaves room for it to grow.
const ScanDatabaseMemory = 2 << 30

// BuildTagPrefix starts every build tag the platform gives what it builds
// ("dock/p3_e4_s5:12"): a name prune may remove from its own images.
const BuildTagPrefix = "dock/p"

// BuildCacheVolume holds BuildKit's state and build cache on a builder
// (build.CacheVolume).
const BuildCacheVolume = "dock-buildkit-cache"

// DiskUsage is where a server's disk goes, as Containers.DiskUsage reads
// it. Image bytes are unique to each image (its size less the layers it
// shares with others); shared layers are counted once, in ImagesShared,
// so the image fields add up to what images take.
type DiskUsage struct {
	// ImagesRunning are images containers use; ImagesKept other images a
	// release keeps or the platform made; ImagesReclaimable those
	// PruneImages would remove; ImagesRollbacks the request's Rollbacks;
	// ImagesOther images the platform didn't make.
	ImagesRunning, ImagesKept, ImagesReclaimable, ImagesRollbacks, ImagesOther, ImagesShared int64
	// ScanLeftovers is what downloads of the vulnerability database that
	// were cut off left in its volume (sized with the request's Tool).
	ScanLeftovers int64
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
	// Role names the runtime's own volumes: VolumeScanDB, VolumeAnalytics
	// or VolumeTraces; empty for the rest.
	Role  string
	Bytes int64
}

// The roles of the runtime's own volumes (VolumeUsage.Role).
const (
	VolumeScanDB    = "scan-db"
	VolumeAnalytics = "analytics"
	VolumeTraces    = "traces"
)

// PruneResult is what a prune removed.
type PruneResult struct {
	Removed        int
	ReclaimedBytes int64
}

// Proxy owns the server's public HTTP(S) routing.
type Proxy interface {
	// ApplyRoutes replaces the full routing table atomically, with the
	// certificates Caddy serves for the names they cover instead of
	// issuing its own (the base domain's wildcard, ADR 0090; nil for
	// none). If the new config is rejected, the previous one stays in
	// effect.
	ApplyRoutes(ctx context.Context, routes []Route, certs []Certificate) error
	// OpenRequests returns how many requests Caddy is proxying to
	// upstreams (host:port) right now, websockets and streams included:
	// core asks it before an environment sleeps, since the access log
	// only sees a request once it ends.
	OpenRequests(ctx context.Context, upstreams []string) (int, error)
	// CheckDomain reports whether hostname's DNS points at this server and
	// whether a valid certificate is being served for it.
	CheckDomain(ctx context.Context, hostname string) (DomainCheck, error)
	// Forward listens on 127.0.0.1:port, where this server's Caddy sends
	// the requests of an environment that moved away (ADR 0063): the
	// control plane accepts each connection and pipes it to the
	// service's container on the server it is on now. Closing the
	// listener stops it; on a remote server, so does the link dropping.
	Forward(ctx context.Context, port int) (net.Listener, error)
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

// PathCount is how often one method and path was requested, how many of
// those answered 5xx, and how long they took (ADR 0075): Latency counts
// the requests under each of LatencyBounds, the last one those past them.
type PathCount struct {
	Method   string
	Path     string
	Requests uint64
	Errors   uint64
	Latency  [len(LatencyBounds) + 1]uint64
}

// LatencyBounds are the upper bounds of PathCount.Latency's buckets.
var LatencyBounds = [...]time.Duration{
	5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond,
	250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2500 * time.Millisecond,
}

// LatencyBucket is the PathCount.Latency bucket d falls in.
func LatencyBucket(d time.Duration) int {
	for i, b := range LatencyBounds {
		if d < b {
			return i
		}
	}
	return len(LatencyBounds)
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
	// A job container that pauses services (JobSpec.Pause) carries it
	// too, so only its own runtime unpauses them after a crash.
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
	// Args, when set instead, override the image's command as they are,
	// without a shell: a start command in exec form (`["worker"]`), for
	// images whose entrypoint takes its mode as arguments.
	Args []string
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
	// ExtraHosts are "name:address" lines for the container's hosts file;
	// "host-gateway" is the server itself (TraceIngestHost). Older nodes
	// ignore them (protocol 26).
	ExtraHosts []string
}

// TraceIngestHost names the server in a traced app's hosts file: its
// Caddy, which forwards OTLP exports to the server's trace ingest.
const TraceIngestHost = "dock-otlp"

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
	Port int
	Path string
	// Kind, without Path, is what the probe asks the port (HealthKind);
	// "" is a TCP connect.
	Kind          HealthKind
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
	// AnonymousVolumes are the paths of the image's VOLUMEs no Mount
	// covers (AnonymousPaths), each an anonymous volume: data written
	// there is not in the next container. Only Start sets it.
	AnonymousVolumes []string
}

// HealthKind is what a health check asks a port beyond a TCP connect.
// The zero value asks nothing more. A server on an older dock doesn't
// know a newer kind and runs the TCP check.
type HealthKind string

// HealthPostgres checks that a PostgreSQL server accepts connections
// (internal/node/pgready): one that is starting up or recovering has its
// port open but refuses every connection with SQLSTATE 57P03.
const HealthPostgres HealthKind = "pg"

// HealthCheck describes when a new container counts as healthy.
//   - Path set: HTTP GET http://127.0.0.1:HostPort{Path} returns 2xx/3xx.
//   - No Path, container publishes a port: TCP connect succeeds.
//   - Neither: the container stays running for Grace.
//
// Port (set without Path) instead probes that container port over TCP on
// the container's environment network, for services that publish nothing
// (database templates, ADR 0019). Kind says what that probe asks the
// port: "" only connects, HealthPostgres also waits for PostgreSQL to
// accept connections.
//
// Interval is the steady probe interval; the first probes come sooner (the
// Docker runtime backs off from 50 ms).
type HealthCheck struct {
	Path string
	Port int
	// Kind applies to the Port probe only.
	Kind     HealthKind
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

// LogLimits bound a service's log archives on a server (ADR 0046,
// [logs] in the control plane's config). Zero means no limit.
type LogLimits struct {
	// MaxBytes caps the service's archives, compressed; the oldest files
	// go first.
	MaxBytes int64
	// MaxAge removes archives last written longer ago than this.
	MaxAge time.Duration
	// MaxTotalBytes caps every service's archives on the server together,
	// compressed; the oldest files go first, whatever their service.
	MaxTotalBytes int64
}

// LogArchive is what Containers.ArchiveLogs wrote: the container's
// service and release IDs (from its labels) and the compressed bytes
// added to the release's archive.
type LogArchive struct {
	Service, Release string
	Bytes            int64
}

// LogArchiveQuery selects a release's archived lines. Service and
// Release are IDs (decimal). Tail keeps the last N lines (0 for all);
// Since drops lines before it.
type LogArchiveQuery struct {
	Service, Release string
	Tail             int
	Since            time.Time
}

// LogPrune is one retention pass over a server's log archives: Limits
// for every service in KeepServices (IDs), and every other service's
// archives removed.
type LogPrune struct {
	KeepServices []string
	Limits       LogLimits
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
	// Request is set on an ActionWake event: what woke the environment.
	Request string
	// Role is RoleSidecar for a sidecar's event (EnsureSidecar), "" for
	// an app container's.
	Role string
}

// ActionWake is the Action of a ContainerEvent that is no container's: a
// node's wake check asking the control plane to wake the asleep
// environment Labels.Environment for Request (ADR 0070). Only nodes send
// it; the control plane checks the environment is on that server.
const ActionWake = "dock.wake"

// BuildRequest describes one image build.
// Built is a build's result.
type Built struct {
	// ID is the image's ID (sha256:...).
	ID string
	// Base is the image a Dockerfile build's final stage is built on, as
	// a reference (node:22-alpine), and BaseDigest the digest it resolved
	// to then (sha256:...): a newer image under the same tag is what a
	// rebuild picks up (ADR 0074). Empty for Railpack builds, scratch,
	// and a FROM whose variables don't resolve.
	Base, BaseDigest string
}

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
	// can't read or poison another's, and keys the layers of steps that
	// mount secrets. Use one scope per project and environment, with a
	// part nobody outside can guess; only letters, digits and _ are
	// allowed. Empty shares a single "unscoped" namespace.
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

// Certificate is a certificate the proxy loads instead of issuing its own
// for the names it covers: the wildcard of a server's base domain (ADR
// 0090), which keeps generated hostnames out of certificate-transparency
// logs. Caddy serves it for every hostname matching Names, routed or not,
// and issues no on-demand certificate for them.
type Certificate struct {
	// Names are the certificate's names: *.<base> and <base>.
	Names []string
	// ChainPEM is the leaf and its intermediates, KeyPEM its private key.
	ChainPEM, KeyPEM []byte
}

// Route sends traffic for Hostnames to Upstream (host:port).
type Route struct {
	Hostnames []string
	Upstream  string
	// Key, when set, names what the route serves across releases: a
	// service's routes have "s<service id>". A proxy that can keeps such
	// a route's Upstream, and the release of its Analytics tags, out of
	// its config, so a switch to another release or port changes no
	// config (ADR 0091). Routes with the same Key have the same Upstream.
	Key string
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
	// Stopped, when set, is the name of the stopped service the route
	// belongs to (ADR 0050): its hostnames answer 503 with a page saying
	// so instead of proxying, after its access check. Upstream stays set
	// for nodes that predate it. Redirect routes ignore it.
	Stopped string
	// Hold makes Caddy wait for the upstream, trying it again for up to
	// HoldFor, instead of answering 502 when it refuses connections: in
	// a move's hold (ADR 0063) a service's port is closed from its
	// container's stop until the control plane listens on it, which holds
	// the connections itself (a reload of Caddy ends its retries, not a
	// connection it made), and a forwarded route's listener only accepts
	// once the target serves.
	Hold bool
	// Wake, when set, is the key of the asleep environment the route
	// belongs to: before proxying, after its access check, Caddy asks
	// the server's wake check (Options.WakeURL), which wakes the
	// environment and holds the request until it is awake.
	Wake string
	// QuietProbes keeps Dock's uptime probes (User-Agent dock-uptime/*)
	// out of the access log, so they don't count as traffic and can't
	// keep an environment that sleeps awake.
	QuietProbes bool
	// Trace, when set, traces the route's requests from the edge (ADR
	// 0075): Caddy drops the visitor's trace headers, starts a trace and
	// passes its traceparent to the app, and the server's trace ingest
	// turns the request's access log line into the trace's edge span,
	// for the service and release this token names (a trace token,
	// internal/traces). Redirect and stopped routes ignore it.
	Trace string
}

// HoldFor is how long a Hold route waits for its upstream.
const HoldFor = 60 * time.Second

// AnalyticsHosts are hostnames of a route that collect analytics, and the
// tag the ingest gets with their events: "site.service.release", the IDs
// of the site the hostnames belong to and the service and release that
// serve them. Script has Caddy add the analytics script to their HTML
// pages (ADR 0105); without it a page loads the script itself. An older
// node ignores it.
type AnalyticsHosts struct {
	Hostnames []string
	Tag       string
	Script    bool
}

// Access is how a route is protected: a password (PasswordHash set) or a
// Dock login (Key set), never both.
type Access struct {
	// Username and PasswordHash (bcrypt) ask visitors for HTTP basic auth.
	Username     string
	PasswordHash string
	// ProbeKey, with a password, signs the session of Dock's uptime
	// probes (access.ProbeCookie), which the route lets through without
	// the password. A node that doesn't know it asks the probe for the
	// password too.
	ProbeKey []byte
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
	// EnsureAnalyticsStore runs the container of the store spec.Kind
	// names as spec says. It returns once the container runs; the store
	// may take a moment more to answer.
	EnsureAnalyticsStore(ctx context.Context, spec AnalyticsStoreSpec) error
	// RemoveAnalyticsStore removes the store of kind from this server:
	// its container and its data, and for the analytics store the binary
	// too. Dock calls it once the analytics store moved to another server
	// and the copy there was checked, on a server that holds a store it
	// shouldn't (a move that stopped), and for the trace store when
	// tracing is turned off or the analytics server changes.
	RemoveAnalyticsStore(ctx context.Context, kind StoreKind) error
	// StopAnalyticsStore stops and removes the container of the store of
	// kind and keeps its data, for analytics turned off with its data
	// kept (ADR 0101): EnsureAnalyticsStore runs it again on the same
	// data. A store that doesn't run is no error.
	StopAnalyticsStore(ctx context.Context, kind StoreKind) error
	// DialAnalyticsStore connects to the API of the store of kind on this
	// server. On a remote server the bytes go over the node link.
	DialAnalyticsStore(ctx context.Context, kind StoreKind) (net.Conn, error)
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
	// SpoolSpans holds spans from the trace ingest (analytics.Span JSON
	// lines), for the trace store, capped on its own.
	SpoolSpans Spool = "spans"
	// SpoolMetrics holds app metrics' points from the trace ingest
	// (analytics.MetricPoint JSON lines), for the trace store, capped on
	// its own: a cardinality bomb can't push out spans.
	SpoolMetrics Spool = "metrics"
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
	// TraceKey checks the service tokens of the trace ingest while
	// tracing is on; nil while it is off, and the ingest refuses spans.
	TraceKey []byte
}

// SiteRules are what a site's ingest leaves out: paths (* matches any
// run of characters) and client addresses or CIDR ranges; whether it
// collects errors and Web Vitals (on unless ErrorsOff); and the share of
// page loads it records for session replay (0: none) and whether
// replays mask all text; and what its script holds back until the page
// calls dock.consent(true): "replay", "all", or nothing ("").
type SiteRules struct {
	ExcludedPaths []string `json:",omitempty"`
	IgnoredIPs    []string `json:",omitempty"`
	ErrorsOff     bool     `json:",omitempty"`
	ReplayPercent int      `json:",omitempty"`
	MaskAllText   bool     `json:",omitempty"`
	Consent       string   `json:",omitempty"`
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

// StoreKind names one of the stores the dock-analytics binary runs, each
// in a container of its own on the analytics server.
type StoreKind string

const (
	// StoreAnalytics is Dock Analytics' store (ADR 0037).
	StoreAnalytics StoreKind = ""
	// StoreTraces is the trace store: spans for tracing over OTLP.
	StoreTraces StoreKind = "traces"
)

// AnalyticsStoreSpec is how a store's container runs: the dock-analytics
// binary, mounted read-only into a container of AnalyticsBaseImage.
type AnalyticsStoreSpec struct {
	// Kind is the store: StoreAnalytics or StoreTraces.
	Kind StoreKind
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
