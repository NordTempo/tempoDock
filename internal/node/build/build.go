// Package build implements node.Builder with a capped BuildKit container.
//
// BuildKit runs as one long-lived container (ContainerName) with hard memory
// and CPU limits and a low CPU weight, so a build can never starve the apps on
// the same server. A build that needs more memory than the cap fails; apps are
// unaffected. The container is created on demand before the first build.
// Its name is fixed, so there is one dock runtime per Docker host: a
// BuildKit container another runtime created (Builder.Owner) is refused,
// never recreated.
//
// Built images carry node.LabelImage, so disk pruning can tell them from
// other images on the host once their tag is gone.
//
// The container runs the rootful BuildKit image privileged, the same way
// `docker buildx create --driver docker-container` does. Rootless BuildKit
// needs seccomp/AppArmor unconfined plus extra kernel settings and is slower,
// and the daemon already holds root-equivalent Docker API access, so privileged
// doesn't widen who can do what. Build steps still run in BuildKit's own
// sandboxes, not with the container's privileges.
//
// The daemon reaches BuildKit through `buildctl dial-stdio` over the Docker
// exec API: no extra socket permissions or ports to manage.
//
// Sources with a Dockerfile use BuildKit's built-in Dockerfile frontend;
// `# syntax=` directives are ignored, since a custom frontend could bypass
// the cache namespacing below. Everything else gets a Railpack build: the
// plan is generated with the Railpack library in a capped child process
// (PlanMain) and built with the matching Railpack BuildKit frontend, which
// is what Railpack recommends for platforms; its images are pinned by
// digest (railpackImages). New layers are compressed with zstd.
//
// # Push
//
// With BuildRequest.Push the built image goes to the platform registry
// (ADR 0014 section 4), which other servers pull it from by digest.
// BuildKit pushes it itself, through the relay (relay.go), and the
// builder pulls it back into Docker: only the layers that changed move,
// where a Docker export sends the whole image every build (ADR 0089
// amendment). Without the relay, the image is loaded into Docker and
// Docker pushes it (dockerPush). The control plane passes a short-lived
// token scoped to the one repository; nothing on the builder stores
// registry credentials. Without a push, results are loaded straight
// into the local Docker Engine.
//
// # Variables
//
// Every service variable is available to build steps as a BuildKit secret
// with the variable's name as ID. Secret mounts don't end up in the image
// or its history (unless a step copies them into a layer). In a Dockerfile:
//
//	RUN --mount=type=secret,id=NPM_TOKEN,env=NPM_TOKEN npm ci
//
// Dockerfile builds also get the variables the Dockerfile declares with ARG
// as build args, since many Dockerfiles rely on ARG for settings like
// NODE_ENV or public URLs. ARG values are visible in the image history
// (`docker history`) of every RUN step that sees them, so secrets should be
// consumed with secret mounts, never declared as ARG.
//
// # Cache isolation
//
// Builds of every project and org share the builder's cache, and
// BuildRequest.CacheScope keeps them apart where content alone doesn't.
// Cache mounts (RUN --mount=type=cache, Railpack's caches) are mutable
// state: each build's cache IDs are prefixed with the scope, so a build
// in one project and environment can't read or poison another's caches.
// Layers are content-addressed, but BuildKit leaves secret values out of
// a step's cache key: two builds with the same base, files and a RUN line
// that mounts a secret would share the layer whatever their secrets. So
// those steps are keyed by a hash of the scope and the secrets' values
// (secretsKey): a Dockerfile gets it as an added ARG (keySecretSteps),
// Railpack as its secrets-hash. Changing a secret rebuilds the steps
// that mount it (in Railpack, any variable rebuilds every step).
//
// # Network
//
// Build steps share the BuildKit container's network namespace (BuildKit's
// "host" network mode inside the container), so they reach exactly what the
// container does: the default bridge, or Builder.Network when set. No
// entitlements are granted, so `RUN --network=host` and
// `RUN --security=insecure` are refused.
package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc/codes"

	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Builder builds images in the capped BuildKit container, one at a time.
type Builder struct {
	// Network is the Docker network the BuildKit container (and so every
	// build step) runs on. Empty means Docker's default bridge. The network
	// must exist; changing it recreates the container on the next build.
	// Set it before the first build.
	Network string
	// Owner identifies the dock runtime this builder belongs to (its data
	// dir), recorded on the BuildKit container (node.LabelOwner). A
	// container another runtime on the same Docker host created is never
	// used, recreated or pruned: builds fail saying so instead. Set it
	// before the first build.
	Owner string
	// Probe is the host path of dock-probe (probe.Install) and RelayDir a
	// host directory for the relay's socket: with both, BuildKit pushes
	// to the registry itself through the relay (relay.go), and the node
	// pulls what it built. Without, the image is loaded into Docker and
	// Docker pushes it. The BuildKit container mounts both directories.
	// Set them before the first build.
	Probe, RelayDir string

	docker  *client.Client
	cfg     config.BuildKit
	workDir string
	sem     chan struct{}
	warmMu  sync.Mutex // one WarmBuilder at a time
	// name and cacheVolume are the BuildKit container and its cache volume:
	// ContainerName and CacheVolume, except in tests.
	name, cacheVolume string
}

var _ node.Builder = (*Builder)(nil)

// New returns a Builder using docker for the BuildKit container and image
// loading. Build contexts are unpacked under workDir, which should be on
// disk (not a tmpfs /tmp) since contexts can be large.
func New(docker *client.Client, cfg config.BuildKit, workDir string) *Builder {
	return &Builder{
		docker: docker, cfg: cfg, workDir: workDir, sem: make(chan struct{}, 1),
		name: ContainerName, cacheVolume: CacheVolume,
	}
}

// Build implements node.Builder.
func (b *Builder) Build(ctx context.Context, req node.BuildRequest) (node.Built, error) {
	logs := req.Logs
	if logs == nil {
		logs = io.Discard
	}
	if req.Tag == "" {
		return node.Built{}, errors.New("build: no tag")
	}
	if req.Push != nil {
		if err := checkPushTarget(req.Push); err != nil {
			return node.Built{}, err
		}
	}

	if err := os.MkdirAll(b.workDir, 0o700); err != nil {
		return node.Built{}, err
	}
	dir, err := os.MkdirTemp(b.workDir, "build-")
	if err != nil {
		return node.Built{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		return node.Built{}, err
	}
	if err := extract(req.Source, src, defaultLimits); err != nil {
		return node.Built{}, fmt.Errorf("unpack source: %w", err)
	}

	select {
	case b.sem <- struct{}{}:
	default:
		_, _ = fmt.Fprintln(logs, "waiting for another build to finish")
		select {
		case b.sem <- struct{}{}:
		case <-ctx.Done():
			return node.Built{}, ctx.Err()
		}
	}
	defer func() {
		<-b.sem
		if len(b.sem) == 0 {
			releaseMemory()
		}
	}()

	opt, base, err := solveOpt(ctx, dir, src, req, logs)
	if err != nil {
		return node.Built{}, err
	}

	bk, err := b.ensureDaemon(ctx, logs)
	if err != nil {
		return node.Built{}, err
	}
	defer func() { _ = bk.Close() }()

	started := time.Now()
	froms := map[string]string{} // image -> digest, from FROM steps
	var push *relayPush
	if req.Push != nil {
		if host := b.relayHost(req.Push); host != "" {
			push = &relayPush{PushTarget: req.Push, host: host}
		}
	}
	if err := b.solve(ctx, bk, opt, req.Tag, push, logs, froms); err != nil {
		return node.Built{}, b.explain(ctx, err, started)
	}
	switch {
	case push != nil:
		if err := b.pull(ctx, req.Tag, req.Push, logs); err != nil {
			return node.Built{}, err
		}
	case req.Push != nil:
		if err := b.dockerPush(ctx, req.Tag, req.Push, logs); err != nil {
			return node.Built{}, err
		}
	}
	img, err := b.docker.ImageInspect(ctx, req.Tag)
	if err != nil {
		return node.Built{}, fmt.Errorf("inspect built image: %w", err)
	}
	out := node.Built{ID: img.ID}
	if base != "" {
		out.Base, out.BaseDigest = familiar(base), froms[base]
	}
	return out, nil
}

// releaseMemory hands the heap a build churned through back to the OS.
//
// The built image streams through the daemon on its way into Docker,
// tunnelled twice in gRPC (BuildKit's session runs inside the control
// connection), so a build allocates about twice the image size in buffers
// of up to 1 MB. None of it stays live, but an idle daemon keeps it for
// minutes: nothing allocates, so no GC runs until the runtime's forced one
// every 2 minutes; gRPC's buffer pool is a sync.Pool, which survives one
// more; only then does the scavenger return the pages. The first GC moves
// the pool into its victim cache, FreeOSMemory's drops it and returns all
// free pages at once. Both are cheap: the live heap is a few MB.
func releaseMemory() {
	runtime.GC()
	debug.FreeOSMemory()
}

// explain makes failures caused by the memory cap say so. BuildKit reports
// a build step killed by the OOM killer as ResourceExhausted. If BuildKit
// itself was killed, the connection drops and the container restarts.
func (b *Builder) explain(ctx context.Context, err error, started time.Time) error {
	hint := fmt.Sprintf("the builder is capped at %d MB; raise [buildkit] memory_mb, "+
		"or build this app in CI and deploy the image", b.cfg.MemoryMB)
	if grpcerrors.Code(err) == codes.ResourceExhausted {
		return fmt.Errorf("build ran out of memory (%s): %w", hint, err)
	}
	if b.restartedSince(ctx, started) || grpcerrors.Code(err) == codes.Unavailable && b.diedSince(ctx, started) {
		return fmt.Errorf("BuildKit restarted during the build, most likely out of memory (%s): %w", hint, err)
	}
	return err
}

// restartedSince reports whether the BuildKit container started after t.
func (b *Builder) restartedSince(ctx context.Context, t time.Time) bool {
	c, err := b.docker.ContainerInspect(ctx, b.name, client.ContainerInspectOptions{})
	if err != nil {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, c.Container.State.StartedAt)
	return err == nil && at.After(t)
}

// dieWait bounds how long diedSince waits for Docker to see BuildKit exit.
const dieWait = 5 * time.Second

// diedSince reports whether the BuildKit container exited after t, for a
// build whose connection dropped. Docker may handle the exit a moment
// after the connection drops, before the restart restartedSince would
// see, so this reads the container's die events from t on (past ones are
// replayed) and waits up to dieWait for one.
func (b *Builder) diedSince(ctx context.Context, t time.Time) bool {
	ctx, cancel := context.WithTimeout(ctx, dieWait)
	defer cancel()
	sub := b.docker.Events(ctx, client.EventsListOptions{
		Since:   fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond()),
		Filters: client.Filters{}.Add("type", "container").Add("container", b.name).Add("event", "die"),
	})
	select {
	case <-sub.Messages:
		return true
	case err := <-sub.Err:
		_ = err // events unavailable: the build's own error stands
		return false
	case <-ctx.Done():
		return false
	}
}

// solveOpt picks the frontend: a Dockerfile build when one is given or
// present at the root, otherwise Railpack.
func solveOpt(ctx context.Context, dir, src string, req node.BuildRequest, logs io.Writer) (bkclient.SolveOpt, string, error) {
	ns, err := cacheNamespace(req.CacheScope)
	if err != nil {
		return bkclient.SolveOpt{}, "", err
	}
	dockerfile := req.Dockerfile
	if dockerfile == "" {
		if _, err := os.Stat(filepath.Join(src, "Dockerfile")); err == nil {
			dockerfile = "Dockerfile"
		}
	}
	if dockerfile != "" {
		return dockerfileOpt(dir, src, dockerfile, req.BuildArgs, ns, logs)
	}
	opt, err := railpackOpt(ctx, dir, src, req, ns, logs)
	return opt, "", err
}

// validScope matches cache scopes. Scopes exclude the separators BuildKit
// ("/") and Railpack ("-") put between namespace and cache ID, so two
// scopes can never produce the same cache ID.
var validScope = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// cacheNamespace returns the namespace for a build's cache mounts.
func cacheNamespace(scope string) (string, error) {
	if scope == "" {
		return "unscoped", nil
	}
	if !validScope.MatchString(scope) {
		return "", fmt.Errorf("build: cache scope %q may only contain letters, digits and _", scope)
	}
	return scope, nil
}

// secretStore exposes vars to build steps as BuildKit secrets, id = name.
func secretStore(vars map[string]string) session.Attachable {
	secrets := make(map[string][]byte, len(vars))
	for k, v := range vars {
		secrets[k] = []byte(v)
	}
	return secretsprovider.FromMap(secrets)
}

func localOpt(frontend string, attrs map[string]string, contextDir, dockerfileDir string) (bkclient.SolveOpt, error) {
	ctxFS, err := fsutil.NewFS(contextDir)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	dfFS, err := fsutil.NewFS(dockerfileDir)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	return bkclient.SolveOpt{
		Frontend:      frontend,
		FrontendAttrs: attrs,
		LocalMounts:   map[string]fsutil.FS{"context": ctxFS, "dockerfile": dfFS},
	}, nil
}

// relayPush is a push BuildKit makes itself, through the relay to
// host (relay.go).
type relayPush struct {
	*node.PushTarget
	host string
}

// solve runs the build, streaming plain progress to logs, and exports
// the image: pushed by BuildKit when push is set, otherwise as a tarball
// into Docker's image load API.
//
// The frontend runs from a build function rather than as the solve's own
// frontend, so the image config it returns is labelled node.LabelImage
// (labelImage) before the export: Railpack's frontend takes no label
// options, and disk pruning only removes images carrying the label.
//
// Layers are compressed with zstd, about three times faster than gzip (a
// cold Railpack export 15.6 s -> 5.2 s on two cores, ADR 0089), which
// Docker 23+ reads. Layers that already have a blob keep it, so
// unchanged layers aren't pushed again.
func (b *Builder) solve(ctx context.Context, bk *bkclient.Client, opt bkclient.SolveOpt, tag string, push *relayPush, logs io.Writer, froms map[string]string) error {
	display, err := progressui.NewDisplay(logs, progressui.PlainMode)
	if err != nil {
		return err
	}
	var loaded chan error
	var pw *io.PipeWriter
	if push != nil {
		// The pushed image only lives in the registry and, once pulled,
		// in Docker: store=false keeps no image record in BuildKit, which
		// would hold its layers past the build cache's pruning.
		opt.Exports = []bkclient.ExportEntry{{
			Type: bkclient.ExporterImage,
			Attrs: map[string]string{
				"name": push.Ref, "push": "true", "store": "false", "compression": "zstd",
			},
		}}
		opt.Session = append(opt.Session, newRegistryAuth(push.host, push.Token))
	} else {
		var pr *io.PipeReader
		pr, pw = io.Pipe()
		opt.Exports = []bkclient.ExportEntry{{
			Type:   bkclient.ExporterDocker,
			Attrs:  map[string]string{"name": tag, "compression": "zstd"},
			Output: func(map[string]string) (io.WriteCloser, error) { return pw, nil },
		}}
		loaded = make(chan error, 1)
		go func() {
			err := b.load(ctx, pr)
			_ = pr.CloseWithError(err)
			loaded <- err
		}()
	}

	status, shown := make(chan *bkclient.SolveStatus), make(chan struct{})
	tapped := make(chan *bkclient.SolveStatus)
	go func() {
		// Note the digest each FROM step resolved (the base image hint)
		// on the way to the display. Drains until Solve closes status.
		for st := range status {
			for _, v := range st.Vertexes {
				if m := fromVertex.FindStringSubmatch(v.Name); m != nil {
					froms[m[1]] = m[2]
				}
			}
			tapped <- st
		}
		close(tapped)
	}()
	go func() {
		// Drain until the tap closes, even after ctx ends.
		_, _ = display.UpdateFrom(context.WithoutCancel(ctx), tapped)
		close(shown)
	}()

	frontend, attrs := opt.Frontend, opt.FrontendAttrs
	var stopRelay func()
	var pushStarted time.Time
	resp, err := bk.Build(ctx, opt, "dock", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		// With a push, the build's steps all run here, before the relay
		// opens the registry to the container for the export.
		res, err := c.Solve(ctx, gateway.SolveRequest{Frontend: frontend, FrontendOpt: attrs, Evaluate: push != nil})
		if err != nil {
			return nil, err
		}
		if err := labelImage(res.Metadata); err != nil {
			return nil, err
		}
		if push != nil {
			if stopRelay, err = b.openRelay(ctx, push.host); err != nil {
				return nil, err
			}
			pushStarted = time.Now()
			_, _ = fmt.Fprintf(logs, "Pushing %s\n", push.Ref)
		}
		return res, nil
	}, status)
	if stopRelay != nil {
		stopRelay()
		if err != nil {
			// Every step passed: the export or its push failed, which
			// core's diagnosis tells from the build by this prefix.
			err = fmt.Errorf("push %s: %w", push.Ref, err)
		}
	}
	if pw != nil {
		_ = pw.CloseWithError(err) // unblocks load if the export never started
	}
	<-shown // the tap has closed: froms is complete
	if loaded != nil {
		if loadErr := <-loaded; err == nil {
			err = loadErr
		}
	}
	if err != nil || push == nil {
		return err
	}
	digest := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	if digest == "" {
		return fmt.Errorf("push %s: BuildKit reported no digest", push.Ref)
	}
	_, _ = fmt.Fprintf(logs, "Pushed %s (%s) in %.1fs\n", push.Ref, digest, time.Since(pushStarted).Seconds())
	return nil
}

// labelImage sets node.LabelImage in the image configs of a frontend's
// result metadata (one per platform), keeping every other field as is.
// A result without one has nothing to label: its image stays unlabelled,
// which pruning treats as not the platform's.
func labelImage(meta map[string][]byte) error {
	for k, dt := range meta {
		if k != exptypes.ExporterImageConfigKey && !strings.HasPrefix(k, exptypes.ExporterImageConfigKey+"/") {
			continue
		}
		var img, cfg map[string]json.RawMessage
		var labels map[string]string
		if err := json.Unmarshal(dt, &img); err != nil {
			return fmt.Errorf("read the image config: %w", err)
		}
		if raw, ok := img["config"]; ok {
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return fmt.Errorf("read the image config: %w", err)
			}
		}
		if raw, ok := cfg["Labels"]; ok {
			if err := json.Unmarshal(raw, &labels); err != nil {
				return fmt.Errorf("read the image labels: %w", err)
			}
		}
		if img == nil {
			img = map[string]json.RawMessage{}
		}
		if cfg == nil {
			cfg = map[string]json.RawMessage{}
		}
		if labels == nil {
			labels = map[string]string{}
		}
		labels[node.LabelImage] = "true"
		cfg["Labels"], _ = json.Marshal(labels)
		img["config"], _ = json.Marshal(cfg)
		meta[k], _ = json.Marshal(img)
	}
	return nil
}

// load feeds an image tarball to Docker and returns any error it reports.
func (b *Builder) load(ctx context.Context, r io.Reader) error {
	res, err := b.docker.ImageLoad(ctx, r, client.ImageLoadWithQuiet(true))
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	defer func() { _ = res.Close() }()
	dec := json.NewDecoder(res)
	for {
		var msg jsonstream.Message
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("load image: %w", err)
		}
		if msg.Error != nil {
			return fmt.Errorf("load image: %s", msg.Error.Message)
		}
	}
}
