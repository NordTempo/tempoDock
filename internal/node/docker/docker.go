// Package docker implements node.Containers on top of the Docker Engine API.
//
// It only ever sees and touches resources labelled node.LabelManaged=true.
// Anything else on the host is reported as not found, and volumes are never
// removed.
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"golang.org/x/sync/errgroup"

	"github.com/NordTempo/tempoDock/internal/node"
)

// Runtime is a node.Containers backed by the local Docker Engine.
type Runtime struct {
	cli *client.Client
	// pool is where network subnets come from (config [network] pool).
	pool netip.Prefix
	// netMu serialises picking a free subnet and creating the network.
	netMu sync.Mutex
	// DataDir is dock's data dir, which DiskUsage measures and log
	// archives live in (ADR 0046); "" skips both.
	DataDir string
	// Probe is the host path of the installed dock-probe (probe.Install),
	// which Start bind-mounts into containers with a spec.Health; ""
	// starts them without an ongoing health check.
	Probe string
	// Sync is the host path of the installed dock-sync
	// (probe.InstallSync), which RunJob bind-mounts for a spec.Sync; ""
	// fails those jobs with node.ErrNoSync.
	Sync string
}

var _ node.Containers = (*Runtime)(nil)

// New connects to the Docker Engine configured by the environment
// (DOCKER_HOST etc., defaulting to the local socket). Networks it creates get
// a /24 from pool.
func New(pool netip.Prefix) (*Runtime, error) {
	if !pool.Addr().Is4() || pool.Bits() > 24 {
		return nil, fmt.Errorf("network pool %s must be an IPv4 CIDR of /24 or larger", pool)
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Runtime{cli: cli, pool: pool.Masked()}, nil
}

// Close releases the underlying Docker client.
func (r *Runtime) Close() error { return r.cli.Close() }

// managedFilter matches only platform-managed resources.
func managedFilter() client.Filters {
	return client.Filters{}.Add("label", node.LabelManaged+"=true")
}

func isManaged(labels map[string]string) bool { return labels[node.LabelManaged] == "true" }

// isApp reports whether a container is a managed app container rather than
// platform infrastructure such as BuildKit.
func isApp(labels map[string]string) bool {
	return isManaged(labels) && labels[node.LabelRole] == ""
}

// notFound is returned for missing and for unmanaged resources alike, so
// callers can't tell (or care) that something unmanaged exists.
// The error matches both node.ErrNotFound and containerd's not-found class.
func notFound(kind, name string) error {
	return notFoundError(fmt.Sprintf("%s %q not found", kind, name))
}

type notFoundError string

func (e notFoundError) Error() string { return string(e) }

func (e notFoundError) Is(target error) bool {
	return target == node.ErrNotFound || cerrdefs.IsNotFound(target)
}

// Pull pulls ref, writing one readable line per status change to logs.
// Errors are Docker's own, without a "pull <ref>" prefix: callers say what
// they were pulling. A digest-pinned ref (name@sha256:...) that is already
// on the server is immutable, so it is not pulled again: that saves the
// registry round trips (1.5-2 s) of every redeploy. Tags are always pulled,
// since they may have moved. A token in opts authenticates the pull (see
// RegistryAuth).
//
// Servers are shared by orgs, so an image on the server may be another
// org's private one: only a platform registry pull (a Token, which core
// minted after checking the caller may pull, pullAllowed) reuses it as it
// is. Any other digest ref is first looked up at its registry with the
// caller's own credentials (one manifest request), so the registry decides
// whether the caller may have it.
func (r *Runtime) Pull(ctx context.Context, ref string, logs io.Writer, opts ...node.PullOptions) (string, error) {
	if logs == nil {
		logs = io.Discard
	}
	var pull client.ImagePullOptions
	platform := false
	if len(opts) > 0 {
		switch o := opts[0]; {
		case o.Token != "":
			pull.RegistryAuth, platform = RegistryAuth(o.Token), true
		case o.Username != "":
			pull.RegistryAuth = PasswordAuth(ref, o.Username, o.Password)
		}
	}
	if strings.Contains(ref, "@sha256:") {
		if img, err := r.cli.ImageInspect(ctx, ref); err == nil && (platform || r.mayPull(ctx, ref, pull.RegistryAuth)) {
			_, _ = fmt.Fprintln(logs, "Image is already on the server")
			return img.ID, nil
		}
	}
	resp, err := r.cli.ImagePull(ctx, ref, pull)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Close() }()

	seen := map[string]string{}
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return "", err
		}
		if msg.Error != nil {
			return "", errors.New(msg.Error.Message)
		}
		if line, ok := pullLine(msg, seen); ok {
			_, _ = fmt.Fprintln(logs, line)
		}
	}

	img, err := r.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect pulled image %s: %w", ref, err)
	}
	return img.ID, nil
}

// mayPull reports whether the registry gives ref's manifest to auth.
func (r *Runtime) mayPull(ctx context.Context, ref, auth string) bool {
	_, err := r.cli.DistributionInspect(ctx, ref, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
	return err == nil
}

// pullLine renders a pull progress message as a log line. Byte-level
// progress updates and repeated statuses per layer are dropped; seen tracks
// the last status printed per layer ID.
func pullLine(msg jsonstream.Message, seen map[string]string) (string, bool) {
	if msg.Status == "" || msg.Progress != nil && msg.Progress.Total > 0 {
		return "", false
	}
	if msg.ID == "" {
		return msg.Status, true
	}
	if seen[msg.ID] == msg.Status {
		return "", false
	}
	seen[msg.ID] = msg.Status
	return msg.ID + ": " + msg.Status, true
}

// HasImage reports whether ref is present locally.
func (r *Runtime) HasImage(ctx context.Context, ref string) (bool, error) {
	_, err := r.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return true, nil
}

// RemoveImage implements node.Containers. Without force, Docker refuses
// to remove an image a container uses, and a tag shared with another
// reference only drops that reference.
func (r *Runtime) RemoveImage(ctx context.Context, ref string) error {
	_, err := r.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{PruneChildren: true})
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("image %s: %w", ref, node.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("remove image %s: %w", ref, err)
	}
	return nil
}

// PruneImages implements node.Containers. Docker's own `image prune`
// can't be told what to keep or whose images are whose, so this lists
// dangling images and removes the platform's (ownedImage) one by one,
// without force: one a container uses is refused and skipped.
func (r *Runtime) PruneImages(ctx context.Context, req node.PruneImagesRequest) (node.PruneResult, error) {
	res, err := r.cli.ImageList(ctx, client.ImageListOptions{Filters: client.Filters{}.Add("dangling", "true")})
	if err != nil {
		return node.PruneResult{}, fmt.Errorf("list dangling images: %w", err)
	}
	var out node.PruneResult
	for _, img := range res.Items {
		if slices.Contains(req.Keep, img.ID) || !ownedImage(img.Labels, img.RepoDigests, req.Repos) {
			continue
		}
		_, err := r.cli.ImageRemove(ctx, img.ID, client.ImageRemoveOptions{PruneChildren: true})
		switch {
		case err == nil:
			out.Removed++
			out.ReclaimedBytes += img.Size
		case cerrdefs.IsConflict(err) || cerrdefs.IsNotFound(err):
		case ctx.Err() != nil:
			return out, ctx.Err()
		default:
			return out, fmt.Errorf("remove image %s: %w", img.ID, err)
		}
	}
	return out, nil
}

// ownedImage reports whether the platform made an image with these labels
// and digest references (repo@sha256:...): it built it (node.LabelImage),
// or pulled it from a repository under one of repos (the platform
// registry's). An image it can't tell is never its own.
func ownedImage(labels map[string]string, repoDigests, repos []string) bool {
	if labels[node.LabelImage] == "true" {
		return true
	}
	for _, d := range repoDigests {
		repo, _, ok := strings.Cut(d, "@")
		if ok && slices.ContainsFunc(repos, func(p string) bool { return p != "" && strings.HasPrefix(repo, p) }) {
			return true
		}
	}
	return false
}

// RemoveNetwork implements node.Containers.
func (r *Runtime) RemoveNetwork(ctx context.Context, name string, labels node.Labels) error {
	r.netMu.Lock()
	defer r.netMu.Unlock()
	res, err := r.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return notFound("network", name)
	case err != nil:
		return fmt.Errorf("inspect network %s: %w", name, err)
	case res.Network.Name != name || !isManaged(res.Network.Labels) || !node.LabelsFrom(res.Network.Labels).Matches(labels):
		return notFound("network", name)
	}
	if _, err := r.cli.NetworkRemove(ctx, res.Network.ID, client.NetworkRemoveOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return notFound("network", name)
		}
		return fmt.Errorf("remove network %s: %w", name, err)
	}
	return nil
}

// BridgePrefix starts the host interface name of every network the runtime
// creates, so the firewall (`dock admin firewall`) can match app traffic by
// interface: "dk-+" in iptables.
const BridgePrefix = "dk-"

// BridgeNameOption is Docker's network option for the bridge interface name.
const BridgeNameOption = "com.docker.network.bridge.name"

// EnsureNetwork creates a managed bridge network called name if missing. It
// fails if an unmanaged network already uses the name, or a managed one
// labelled for another project or environment.
//
// New networks get the first free /24 of the runtime's pool and a bridge
// named BridgePrefix + a hash of the name. Networks created before the pool
// existed keep Docker's addresses; the firewall finds them by label.
func (r *Runtime) EnsureNetwork(ctx context.Context, name string, labels node.Labels) error {
	r.netMu.Lock()
	defer r.netMu.Unlock()
	res, err := r.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	switch {
	case err == nil && res.Network.Name == name:
		if !isManaged(res.Network.Labels) {
			return fmt.Errorf("network %q exists but is not managed by the platform", name)
		}
		if node.LabelsFrom(res.Network.Labels).Conflicts(labels) {
			return fmt.Errorf("network %q belongs to another environment", name)
		}
		return nil
	case err != nil && !cerrdefs.IsNotFound(err):
		return fmt.Errorf("inspect network %s: %w", name, err)
	}
	subnet, err := r.freeSubnet(ctx)
	if err != nil {
		return err
	}
	_, err = r.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver:  "bridge",
		IPAM:    &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet}}},
		Options: map[string]string{BridgeNameOption: bridgeName(name)},
		Labels:  node.Labels{ControlPlane: labels.ControlPlane, Project: labels.Project, Environment: labels.Environment}.Map(),
	})
	if err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// bridgeName is the host interface of network name: BridgePrefix plus a
// hash, as interface names are limited to 15 characters.
func bridgeName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return BridgePrefix + hex.EncodeToString(sum[:5])
}

// freeSubnet returns the first /24 of the pool that no Docker network,
// managed or not, overlaps.
func (r *Runtime) freeSubnet(ctx context.Context) (netip.Prefix, error) {
	res, err := r.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("list networks: %w", err)
	}
	var used []netip.Prefix
	for _, n := range res.Items {
		for _, c := range n.IPAM.Config {
			used = append(used, c.Subnet)
		}
	}
	return firstFree(r.pool, used)
}

// firstFree returns the first /24 in pool (an IPv4 prefix of /24 or larger)
// that overlaps nothing in used.
func firstFree(pool netip.Prefix, used []netip.Prefix) (netip.Prefix, error) {
	for p := netip.PrefixFrom(pool.Addr(), 24); pool.Contains(p.Addr()); {
		if !slices.ContainsFunc(used, p.Overlaps) {
			return p, nil
		}
		a := p.Addr().As4()
		next := binary.BigEndian.Uint32(a[:]) + 1<<8
		if next == 0 {
			break // wrapped past 255.255.255.0/24
		}
		binary.BigEndian.PutUint32(a[:], next)
		p = netip.PrefixFrom(netip.AddrFrom4(a), 24)
	}
	return netip.Prefix{}, fmt.Errorf("network pool %s is full; set a larger [network] pool", pool)
}

// EnsureVolume creates a managed volume called name if missing. It fails if
// an unmanaged volume already uses the name, or a managed one labelled for
// another service. Existing labels are left as is.
func (r *Runtime) EnsureVolume(ctx context.Context, name string, labels node.Labels) error {
	res, err := r.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	switch {
	case err == nil:
		if !isManaged(res.Volume.Labels) {
			return fmt.Errorf("volume %q exists but is not managed by the platform", name)
		}
		if node.LabelsFrom(res.Volume.Labels).Conflicts(labels) {
			return fmt.Errorf("volume %q belongs to another service", name)
		}
		return nil
	case !cerrdefs.IsNotFound(err):
		return fmt.Errorf("inspect volume %s: %w", name, err)
	}
	_, err = r.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels.Map()})
	if err != nil {
		return fmt.Errorf("create volume %s: %w", name, err)
	}
	return nil
}

// RemoveVolume implements node.Containers. It never forces: a volume a
// container still uses stays, and Docker's refusal is returned.
func (r *Runtime) RemoveVolume(ctx context.Context, name string, labels node.Labels) error {
	res, err := r.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return notFound("volume", name)
	case err != nil:
		return fmt.Errorf("inspect volume %s: %w", name, err)
	case !isManaged(res.Volume.Labels) || !node.LabelsFrom(res.Volume.Labels).Matches(labels):
		return notFound("volume", name)
	}
	if _, err := r.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return notFound("volume", name)
		}
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

// Start creates and starts a container from spec. The network and volumes
// must already exist and be managed (see EnsureNetwork and EnsureVolume).
func (r *Runtime) Start(ctx context.Context, spec node.ContainerSpec) (node.Container, error) {
	if spec.Port < 0 || spec.Port > math.MaxUint16 {
		return node.Container{}, fmt.Errorf("invalid port %d", spec.Port)
	}
	if err := r.checkDeps(ctx, spec); err != nil {
		return node.Container{}, err
	}
	img, err := r.cli.ImageInspect(ctx, spec.Image)
	if err != nil {
		return node.Container{}, fmt.Errorf("inspect image %s: %w", spec.Image, err)
	}
	var declared []string
	if img.Config != nil {
		declared = slices.Collect(maps.Keys(img.Config.Volumes))
	}
	anon := node.AnonymousPaths(declared, spec.Mounts)
	opts := createOptions(spec, r.Probe, anon)
	created, err := r.cli.ContainerCreate(ctx, opts)
	if cerrdefs.IsConflict(err) {
		return node.Container{}, fmt.Errorf("container name %q is already in use", spec.Name)
	}
	if err != nil {
		return node.Container{}, fmt.Errorf("create container %s: %w", spec.Name, err)
	}
	// From here on, any failure (including ctx being cancelled) removes the
	// container: the caller gets no ID, so it could never clean it up, and a
	// stray container on a stateful service's volume must not keep running.
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = r.cli.ContainerRemove(cctx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	}
	if _, err := r.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanup()
		return node.Container{}, fmt.Errorf("start container %s: %w", spec.Name, err)
	}
	c, err := r.Inspect(ctx, created.ID)
	if err != nil {
		cleanup()
		return node.Container{}, err
	}
	c.AnonymousVolumes = anon
	return c, nil
}

// checkDeps refuses to attach a container to unmanaged networks or volumes.
// Docker would otherwise silently create missing volumes without labels.
func (r *Runtime) checkDeps(ctx context.Context, spec node.ContainerSpec) error {
	if spec.Network != "" {
		res, err := r.cli.NetworkInspect(ctx, spec.Network, client.NetworkInspectOptions{})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect network %s: %w", spec.Network, err)
		}
		if err != nil || res.Network.Name != spec.Network || !isManaged(res.Network.Labels) {
			return fmt.Errorf("network %q does not exist or is not managed by the platform", spec.Network)
		}
		if node.LabelsFrom(res.Network.Labels).Conflicts(spec.Labels) {
			return fmt.Errorf("network %q belongs to another environment", spec.Network)
		}
	}
	for _, m := range spec.Mounts {
		res, err := r.cli.VolumeInspect(ctx, m.Volume, client.VolumeInspectOptions{})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect volume %s: %w", m.Volume, err)
		}
		if err != nil || !isManaged(res.Volume.Labels) {
			return fmt.Errorf("volume %q does not exist or is not managed by the platform", m.Volume)
		}
		if node.LabelsFrom(res.Volume.Labels).Conflicts(spec.Labels) {
			return fmt.Errorf("volume %q belongs to another service", m.Volume)
		}
	}
	return nil
}

// NoSwap returns the HostConfig.MemorySwap that caps a container at memory
// bytes with no swap, in a way that survives `systemctl daemon-reload`.
// A memory of 0 (no limit) stays 0.
//
// Docker's usual "no swap", MemorySwap == Memory, is lost on systemd hosts:
// with the systemd cgroup driver, runc writes memory.swap.max = 0 but never
// tells systemd (it skips a zero MemorySwapMax), so the container's scope
// unit keeps MemorySwapMax=infinity, and after any daemon-reload (package
// upgrades, the install script) systemd writes "max" back the next time it
// touches cgroups in the slice. One byte of swap is a value runc does pass
// to systemd, and the kernel rounds it down to zero pages: memory.swap.max
// reads 0 and systemd re-applies that on every reload. `docker update` with
// this value fixes the scope of an existing container too.
func NoSwap(memory int64) int64 {
	if memory <= 0 {
		return 0
	}
	return memory + 1
}

// createOptions translates spec into Docker's create request. probe is the
// host path of dock-probe (Runtime.Probe); without one, spec.Health is
// ignored. anon are the image's VOLUME paths no spec mount covers: each
// gets the anonymous volume Docker would make anyway, but labelled
// (anonymousMount), so Remove can find what it kept for the service.
func createOptions(spec node.ContainerSpec, probe string, anon []string) client.ContainerCreateOptions {
	cfg := &container.Config{
		Image:  spec.Image,
		Labels: spec.Labels.Map(),
		Env:    envList(spec.Env),
	}
	if spec.Cmd != "" {
		cfg.Cmd = []string{"sh", "-c", spec.Cmd}
	}

	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		LogConfig: container.LogConfig{
			Type:   "local",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		Resources: container.Resources{
			Memory: spec.Resources.MemoryBytes,
			// No swap (Docker's default is twice the limit), so the limit is
			// what the app gets and an app over it is killed instead of
			// thrashing the disk.
			MemorySwap: NoSwap(spec.Resources.MemoryBytes),
			NanoCPUs:   spec.Resources.NanoCPUs,
		},
		SecurityOpt: []string{"no-new-privileges"},
	}
	for _, m := range spec.Mounts {
		host.Mounts = append(host.Mounts, volumeMount(m))
	}
	for _, p := range anon {
		host.Mounts = append(host.Mounts, anonymousMount(p, spec.Labels))
	}
	if spec.Port > 0 {
		port, _ := network.PortFrom(uint16(spec.Port), network.TCP)
		cfg.ExposedPorts = network.PortSet{port: {}}
		binding := network.PortBinding{HostIP: netip.MustParseAddr("127.0.0.1")}
		if spec.HostPort > 0 {
			binding.HostPort = strconv.Itoa(spec.HostPort)
		}
		host.PortBindings = network.PortMap{port: {binding}}
	}
	if spec.Health != nil && probe != "" {
		cfg.Healthcheck = healthcheck(*spec.Health)
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeBind, Source: probe, Target: probePath, ReadOnly: true})
	} else {
		// The image's own HEALTHCHECK would run otherwise, and core acts
		// on what it reports: a service whose check is off, or that has
		// nothing to probe, gets none (ADR 0043).
		cfg.Healthcheck = noHealthcheck()
	}

	opts := client.ContainerCreateOptions{Name: spec.Name, Config: cfg, HostConfig: host}
	if spec.Network != "" {
		opts.NetworkingConfig = networking(spec.Network, spec.Aliases)
	}
	return opts
}

// noHealthcheck turns off the image's own HEALTHCHECK, for containers
// without an ongoing check of Dock's and for jobs.
func noHealthcheck() *container.HealthConfig {
	return &container.HealthConfig{Test: []string{"NONE"}}
}

// probePath is where dock-probe is bind-mounted in a container.
const probePath = "/.dock-probe"

// probeMargin is how much longer Docker waits for a probe than the probe
// waits for the app, so the probe prints why it timed out before Docker
// kills it (Docker would only log that the check timed out).
const probeMargin = time.Second

// healthcheck is Docker's healthcheck running dock-probe for hc. A zero
// Timeout is probeTimeout; Docker's defaults apply to the other zero
// settings.
func healthcheck(hc node.LivenessCheck) *container.HealthConfig {
	timeout := hc.Timeout
	if timeout <= 0 {
		timeout = probeTimeout
	}
	test := []string{"CMD", probePath, "tcp", strconv.Itoa(hc.Port)}
	if hc.Path != "" {
		test = []string{"CMD", probePath, "http", strconv.Itoa(hc.Port), hc.Path}
	}
	test = append(test, strconv.FormatInt(timeout.Milliseconds(), 10))
	return &container.HealthConfig{
		Test:          test,
		Interval:      hc.Interval,
		Timeout:       timeout + probeMargin,
		Retries:       hc.Retries,
		StartPeriod:   hc.StartPeriod,
		StartInterval: hc.StartInterval,
	}
}

// volumeMount mounts a named volume.
func volumeMount(m node.Mount) mount.Mount {
	return mount.Mount{Type: mount.TypeVolume, Source: m.Volume, Target: m.Path, ReadOnly: m.ReadOnly}
}

// labelAnonymous ("true") marks the anonymous volumes Start makes for an
// image's VOLUMEs, next to the container's own labels (ADR 0052).
const labelAnonymous = "dock.anonymous"

// anonymousMount is an anonymous volume at path, labelled for the
// container's service and release. With no Source it is still anonymous
// to Docker: removing the container with RemoveVolumes removes it, and
// the image's files at path are copied in like for a VOLUME.
func anonymousMount(path string, labels node.Labels) mount.Mount {
	l := labels.Map()
	l[labelAnonymous] = "true"
	return mount.Mount{Type: mount.TypeVolume, Target: path, VolumeOptions: &mount.VolumeOptions{Labels: l}}
}

// networking joins one network with aliases.
func networking(name string, aliases []string) *network.NetworkingConfig {
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{name: {Aliases: aliases}},
	}
}

// envList renders env as sorted KEY=value pairs.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// Stop stops a managed container, killing it after timeout.
func (r *Runtime) Stop(ctx context.Context, id string, timeout time.Duration) error {
	if _, err := r.inspect(ctx, id); err != nil {
		return err
	}
	secs := int(math.Ceil(max(timeout, 0).Seconds()))
	if _, err := r.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs}); err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	return nil
}

// Restart restarts a running managed container in place, killing it after
// timeout. One that isn't running is node.ErrNotRunning: a stopped
// service is never started by a health restart.
func (r *Runtime) Restart(ctx context.Context, id string, timeout time.Duration) error {
	st, err := r.inspect(ctx, id)
	if err != nil {
		return err
	}
	if st.State == nil || !st.State.Running {
		return fmt.Errorf("restart container %s: %w", id, node.ErrNotRunning)
	}
	secs := int(math.Ceil(max(timeout, 0).Seconds()))
	if _, err := r.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &secs}); err != nil {
		return fmt.Errorf("restart container %s: %w", id, err)
	}
	return nil
}

// Resume starts a stopped managed container again, keeping its ID, port
// and volumes (a sleeping environment waking). A running one is left
// alone.
func (r *Runtime) Resume(ctx context.Context, id string) error {
	st, err := r.inspect(ctx, id)
	if err != nil {
		return err
	}
	if st.State != nil && st.State.Running {
		return nil
	}
	if _, err := r.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	return nil
}

// Remove removes a stopped managed container. Its anonymous volumes (the
// ones for an image's VOLUME that no Dock volume covers, a Postgres image
// without one, say) go with it, unless opts.KeepAnonymousVolumes: then
// they are the service's kept copy, and the copy kept before goes
// (removeKept). Named volumes, which are all of Dock's own, are never
// removed.
func (r *Runtime) Remove(ctx context.Context, id string, opts node.RemoveOptions) error {
	c, err := r.inspect(ctx, id)
	if err != nil {
		return err
	}
	if _, err := r.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{RemoveVolumes: !opts.KeepAnonymousVolumes}); err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	if opts.KeepAnonymousVolumes {
		var kept []string
		for _, m := range c.Mounts {
			if m.Type == mount.TypeVolume {
				kept = append(kept, m.Name)
			}
		}
		r.removeKept(ctx, node.LabelsFrom(c.Config.Labels), kept)
	}
	return nil
}

// removeKept removes the anonymous volumes Start made for the service of
// labels that no container uses, except keep: what Remove kept of earlier
// containers. Docker refuses to remove a volume in use, so a running
// container's never goes. Failures are logged: the container is gone
// either way, and the next removal tries again.
func (r *Runtime) removeKept(ctx context.Context, labels node.Labels, keep []string) {
	if labels.ControlPlane == "" || labels.Service == "" {
		return // from before control plane labels: Start labelled nothing
	}
	f := client.Filters{}.Add("dangling", "true").Add("label",
		labelAnonymous+"=true",
		node.LabelControlPlane+"="+labels.ControlPlane,
		node.LabelProject+"="+labels.Project,
		node.LabelEnvironment+"="+labels.Environment,
		node.LabelService+"="+labels.Service,
	)
	res, err := r.cli.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		slog.Warn("list kept anonymous volumes", "service", labels.Service, "err", err)
		return
	}
	for _, v := range res.Items {
		if slices.Contains(keep, v.Name) {
			continue
		}
		if _, err := r.cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
			slog.Warn("remove kept anonymous volume", "volume", v.Name, "service", labels.Service, "err", err)
		}
	}
}

// Inspect returns the current state of a managed container.
func (r *Runtime) Inspect(ctx context.Context, id string) (node.Container, error) {
	c, err := r.inspect(ctx, id)
	if err != nil {
		return node.Container{}, err
	}
	return toContainer(c), nil
}

// MemoryOwner describes a container for the memory breakdown
// (stats.Options.Container). Containers Dock didn't create are counted as
// "other containers", without their names.
func (r *Runtime) MemoryOwner(ctx context.Context, id string) (node.MemoryPart, error) {
	res, err := r.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return node.MemoryPart{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	c := res.Container
	var labels map[string]string
	if c.Config != nil {
		labels = c.Config.Labels
	}
	name := strings.TrimPrefix(c.Name, "/")
	switch role := labels[node.LabelRole]; {
	case name == registryContainer, isManaged(labels) && role == node.RoleRegistryGC:
		return node.MemoryPart{Group: node.MemoryPlatform, Name: "registry"}, nil
	case !isManaged(labels):
		return node.MemoryPart{Group: node.MemorySystem, Name: "other containers"}, nil
	case role == "":
		return node.MemoryPart{Group: node.MemoryApps, Name: name, Service: labels[node.LabelService]}, nil
	case role == node.RoleBuildKit:
		return node.MemoryPart{Group: node.MemoryPlatform, Name: "BuildKit"}, nil
	default:
		return node.MemoryPart{Group: node.MemoryPlatform, Name: "jobs"}, nil
	}
}

// registryContainer is the platform registry deploy/install.sh runs
// (registry.ContainerName, which this package can't import).
const registryContainer = "dock-registry"

// UpgradeSwapLimits gives app containers created with Docker's plain "no
// swap" (MemorySwap == Memory) the NoSwap setting, which systemd keeps
// across a daemon-reload: one `docker update` per such container, a few at
// a time. Containers already set up right aren't touched. Run it once on
// startup; it returns how many containers it updated.
func (r *Runtime) UpgradeSwapLimits(ctx context.Context) (int, error) {
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: managedFilter()})
	if err != nil {
		return 0, fmt.Errorf("list containers: %w", err)
	}
	var updated atomic.Int32
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for _, s := range res.Items {
		g.Go(func() error {
			c, err := r.inspect(ctx, s.ID)
			if cerrdefs.IsNotFound(err) {
				return nil // removed since listing, or not an app
			}
			if err != nil {
				return err
			}
			h := c.HostConfig
			if h == nil || h.Memory <= 0 || h.MemorySwap != h.Memory {
				return nil
			}
			_, err = r.cli.ContainerUpdate(ctx, s.ID, client.ContainerUpdateOptions{
				Resources: &container.Resources{Memory: h.Memory, MemorySwap: NoSwap(h.Memory)},
			})
			switch {
			case cerrdefs.IsNotFound(err):
			case err != nil:
				return fmt.Errorf("update swap limit of %s: %w", c.Name, err)
			default:
				updated.Add(1)
			}
			return nil
		})
	}
	err = g.Wait()
	return int(updated.Load()), err
}

// List returns all managed containers, running or not.
func (r *Runtime) List(ctx context.Context) ([]node.Container, error) {
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: managedFilter()})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]node.Container, 0, len(res.Items))
	for _, s := range res.Items {
		c, err := r.Inspect(ctx, s.ID)
		if cerrdefs.IsNotFound(err) {
			continue // removed since listing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ReleaseContainers returns the IDs of a release's containers that match
// want: one list call filtered by the release label, the rest compared
// here (Docker's filters can't match a missing label).
func (r *Runtime) ReleaseContainers(ctx context.Context, want node.Labels) ([]string, error) {
	f := managedFilter().Add("label", node.LabelRelease+"="+want.Release)
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	ids := make([]string, 0, len(res.Items))
	for _, s := range res.Items {
		if isApp(s.Labels) && node.LabelsFrom(s.Labels).Matches(want) {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// inspect fetches a container, treating unmanaged ones as not found.
func (r *Runtime) inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	res, err := r.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return container.InspectResponse{}, notFound("container", id)
	}
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	if res.Container.Config == nil || !isApp(res.Container.Config.Labels) {
		return container.InspectResponse{}, notFound("container", id)
	}
	return res.Container, nil
}

// toContainer maps Docker's inspect response to node.Container.
func toContainer(c container.InspectResponse) node.Container {
	out := node.Container{
		ID:           c.ID,
		Name:         strings.TrimPrefix(c.Name, "/"),
		RestartCount: c.RestartCount,
	}
	if c.Config != nil {
		out.Image = c.Config.Image
		out.Labels = node.LabelsFrom(c.Config.Labels)
	}
	if s := c.State; s != nil {
		out.State = node.ContainerState(s.Status)
		out.ExitCode = s.ExitCode
		out.OOMKilled = s.OOMKilled
		if t, err := time.Parse(time.RFC3339Nano, s.StartedAt); err == nil && t.Year() > 1 {
			out.StartedAt = t
		}
		if h := s.Health; h != nil {
			out.Health = string(h.Status)
			if h.Status == container.Unhealthy {
				out.HealthOutput = lastFailure(h.Log)
			}
		}
	}
	if c.NetworkSettings != nil {
		out.HostPort = hostPort(c.NetworkSettings.Ports)
	}
	return out
}

// maxHealthOutput caps node.Container.HealthOutput.
const maxHealthOutput = 256

// lastFailure is the trimmed output of the newest failed probe in log
// (oldest first), at most maxHealthOutput bytes.
func lastFailure(log []*container.HealthcheckResult) string {
	for _, r := range slices.Backward(log) {
		if r == nil || r.ExitCode == 0 {
			continue
		}
		out := strings.TrimSpace(r.Output)
		if len(out) > maxHealthOutput {
			out = strings.ToValidUTF8(out[:maxHealthOutput], "")
		}
		return out
	}
	return ""
}

// hostPort returns the loopback host port of the first published TCP port.
// Containers created by Start publish at most one.
func hostPort(ports network.PortMap) int {
	for p, bindings := range ports {
		if p.Proto() != network.TCP {
			continue
		}
		for _, b := range bindings {
			if n, err := strconv.Atoi(b.HostPort); err == nil && b.HostIP.IsLoopback() {
				return n
			}
		}
	}
	return 0
}

// errExited reports a container that died while being waited on. limit is
// its memory limit in bytes (0: none).
func errExited(code int, oom bool, limit int64) error {
	switch {
	case oom && limit > 0:
		return fmt.Errorf("container ran out of memory (limit %d MB) and was killed during health check", limit>>20)
	case oom:
		return errors.New("container ran out of memory and was killed during health check")
	case code == 137 && limit > 0:
		// SIGKILL. Some kernels don't report the OOM kill to Docker, so
		// name the likely cause without claiming it.
		return fmt.Errorf("container was killed (exit code 137) during health check, most likely out of memory (limit %d MB)", limit>>20)
	}
	return fmt.Errorf("container exited with code %d during health check", code)
}

var errRestarted = errors.New("container restarted during health check")
