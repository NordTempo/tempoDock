// Package build implements node.Builder with a capped BuildKit container.
//
// BuildKit runs as one long-lived container (ContainerName) with hard memory
// and CPU limits and a low CPU weight, so a build can never starve the apps on
// the same server. A build that needs more memory than the cap fails; apps are
// unaffected. The container is created on demand before the first build.
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
// is what Railpack recommends for platforms. Results are exported straight
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
// consumed with secret mounts, never declared as ARG. Changing a secret
// doesn't invalidate cached Dockerfile layers that read it; Railpack builds
// rebuild when any variable changes.
//
// # Cache isolation
//
// Cache mounts (RUN --mount=type=cache, Railpack's caches) are mutable
// state shared across builds on the builder. Each build's cache IDs are
// prefixed with BuildRequest.CacheScope, so a build in one project and
// environment can't read or poison another's caches. Layer cache is
// content-addressed and needs no namespacing.
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
	"time"

	bkclient "github.com/moby/buildkit/client"
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

	docker  *client.Client
	cfg     config.BuildKit
	workDir string
	sem     chan struct{}
}

var _ node.Builder = (*Builder)(nil)

// New returns a Builder using docker for the BuildKit container and image
// loading. Build contexts are unpacked under workDir, which should be on
// disk (not a tmpfs /tmp) since contexts can be large.
func New(docker *client.Client, cfg config.BuildKit, workDir string) *Builder {
	return &Builder{docker: docker, cfg: cfg, workDir: workDir, sem: make(chan struct{}, 1)}
}

// Build implements node.Builder.
func (b *Builder) Build(ctx context.Context, req node.BuildRequest) (string, error) {
	logs := req.Logs
	if logs == nil {
		logs = io.Discard
	}
	if req.Tag == "" {
		return "", errors.New("build: no tag")
	}

	if err := os.MkdirAll(b.workDir, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(b.workDir, "build-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		return "", err
	}
	if err := extract(req.Source, src, defaultLimits); err != nil {
		return "", fmt.Errorf("unpack source: %w", err)
	}

	select {
	case b.sem <- struct{}{}:
	default:
		_, _ = fmt.Fprintln(logs, "waiting for another build to finish")
		select {
		case b.sem <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	defer func() {
		<-b.sem
		if len(b.sem) == 0 {
			releaseMemory()
		}
	}()

	opt, err := solveOpt(ctx, dir, src, req, logs)
	if err != nil {
		return "", err
	}

	bk, err := b.ensureDaemon(ctx, logs)
	if err != nil {
		return "", err
	}
	defer func() { _ = bk.Close() }()

	started := time.Now()
	if err := b.solve(ctx, bk, opt, req.Tag, logs); err != nil {
		return "", b.explain(ctx, err, started)
	}

	img, err := b.docker.ImageInspect(ctx, req.Tag)
	if err != nil {
		return "", fmt.Errorf("inspect built image: %w", err)
	}
	return img.ID, nil
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
	if c, ierr := b.docker.ContainerInspect(ctx, ContainerName, client.ContainerInspectOptions{}); ierr == nil {
		if t, perr := time.Parse(time.RFC3339Nano, c.Container.State.StartedAt); perr == nil && t.After(started) {
			return fmt.Errorf("BuildKit restarted during the build, most likely out of memory (%s): %w", hint, err)
		}
	}
	return err
}

// solveOpt picks the frontend: a Dockerfile build when one is given or
// present at the root, otherwise Railpack.
func solveOpt(ctx context.Context, dir, src string, req node.BuildRequest, logs io.Writer) (bkclient.SolveOpt, error) {
	ns, err := cacheNamespace(req.CacheScope)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	dockerfile := req.Dockerfile
	if dockerfile == "" {
		if _, err := os.Stat(filepath.Join(src, "Dockerfile")); err == nil {
			dockerfile = "Dockerfile"
		}
	}
	if dockerfile != "" {
		return dockerfileOpt(src, dockerfile, req.BuildArgs, ns, logs)
	}
	return railpackOpt(ctx, dir, src, req, ns, logs)
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

// solve runs the build, streaming plain progress to logs and the resulting
// image tarball into Docker's image load API.
func (b *Builder) solve(ctx context.Context, bk *bkclient.Client, opt bkclient.SolveOpt, tag string, logs io.Writer) error {
	pr, pw := io.Pipe()
	opt.Exports = []bkclient.ExportEntry{{
		Type:   bkclient.ExporterDocker,
		Attrs:  map[string]string{"name": tag},
		Output: func(map[string]string) (io.WriteCloser, error) { return pw, nil },
	}}

	loaded := make(chan error, 1)
	go func() {
		err := b.load(ctx, pr)
		_ = pr.CloseWithError(err)
		loaded <- err
	}()

	display, err := progressui.NewDisplay(logs, progressui.PlainMode)
	if err != nil {
		return err
	}
	status := make(chan *bkclient.SolveStatus)
	shown := make(chan struct{})
	go func() {
		// Drain until Solve closes status, even after ctx ends.
		_, _ = display.UpdateFrom(context.WithoutCancel(ctx), status)
		close(shown)
	}()

	_, err = bk.Solve(ctx, nil, opt, status)
	_ = pw.CloseWithError(err) // unblocks load if the export never started
	<-shown
	loadErr := <-loaded
	if err != nil {
		return err
	}
	return loadErr
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
