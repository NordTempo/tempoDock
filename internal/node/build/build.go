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
// Sources with a Dockerfile use BuildKit's dockerfile frontend. Everything
// else gets a Railpack build: the plan is generated in-process with the
// Railpack library and built with the matching Railpack BuildKit frontend,
// which is what Railpack recommends for platforms. Results are exported
// straight into the local Docker Engine.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc/codes"

	"github.com/NordTempo/platform/internal/config"
	"github.com/NordTempo/platform/internal/node"
)

// Builder builds images in the capped BuildKit container, one at a time.
type Builder struct {
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
	if err := extract(req.Source, src); err != nil {
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
	defer func() { <-b.sem }()

	opt, err := solveOpt(dir, src, req, logs)
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
func solveOpt(dir, src string, req node.BuildRequest, logs io.Writer) (bkclient.SolveOpt, error) {
	dockerfile := req.Dockerfile
	if dockerfile == "" {
		if _, err := os.Stat(filepath.Join(src, "Dockerfile")); err == nil {
			dockerfile = "Dockerfile"
		}
	}
	if dockerfile != "" {
		return dockerfileOpt(src, dockerfile, req.BuildArgs)
	}
	return railpackOpt(dir, src, req, logs)
}

func dockerfileOpt(src, dockerfile string, args map[string]string) (bkclient.SolveOpt, error) {
	if !filepath.IsLocal(dockerfile) {
		return bkclient.SolveOpt{}, fmt.Errorf("dockerfile path %q must be inside the source", dockerfile)
	}
	if _, err := os.Stat(filepath.Join(src, dockerfile)); err != nil {
		return bkclient.SolveOpt{}, fmt.Errorf("dockerfile %q not found in source", dockerfile)
	}
	attrs := map[string]string{"filename": filepath.Base(dockerfile)}
	for k, v := range args {
		attrs["build-arg:"+k] = v
	}
	return localOpt("dockerfile.v0", attrs, src, filepath.Join(src, filepath.Dir(dockerfile)))
}

const planFile = "railpack-plan.json"

// railpackOpt generates a Railpack plan for src and returns options to build
// it with the Railpack frontend. Build args become Railpack variables: they
// configure the plan (RAILPACK_* settings) and are exposed to build steps as
// secrets, the way Railway does it.
func railpackOpt(dir, src string, req node.BuildRequest, logs io.Writer) (bkclient.SolveOpt, error) {
	a, err := app.NewApp(src)
	if err != nil {
		return bkclient.SolveOpt{}, fmt.Errorf("railpack: %w", err)
	}
	vars := maps.Clone(req.BuildArgs)
	res, err := core.GenerateBuildPlan(a, app.NewEnvironment(&vars), &core.GenerateBuildPlanOptions{
		RailpackVersion:          railpackVersion,
		BuildCommand:             req.BuildCommand,
		StartCommand:             req.StartCommand,
		ErrorMissingStartCommand: true,
	})
	for _, l := range res.Logs {
		_, _ = fmt.Fprintf(logs, "railpack %s: %s\n", l.Level, l.Msg)
	}
	if err != nil {
		return bkclient.SolveOpt{}, fmt.Errorf("railpack (temporary failure, retry): %w", err)
	}
	if !res.Success {
		return bkclient.SolveOpt{}, errors.New("railpack could not work out how to build this app; add a Dockerfile or see the log above")
	}

	plan, err := json.Marshal(res.Plan)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	planDir := filepath.Join(dir, "plan")
	if err := os.Mkdir(planDir, 0o700); err != nil {
		return bkclient.SolveOpt{}, err
	}
	if err := os.WriteFile(filepath.Join(planDir, planFile), plan, 0o600); err != nil {
		return bkclient.SolveOpt{}, err
	}

	attrs := map[string]string{
		"source":   "ghcr.io/railwayapp/railpack-frontend:" + railpackVersion,
		"filename": planFile,
	}
	secrets := map[string][]byte{}
	if len(vars) > 0 {
		h := sha256.New()
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			secrets[k] = []byte(vars[k])
			_, _ = fmt.Fprintf(h, "%s=%s\n", k, vars[k])
		}
		// Secrets don't invalidate cached layers on their own.
		attrs["build-arg:secrets-hash"] = hex.EncodeToString(h.Sum(nil))
	}
	opt, err := localOpt("gateway.v0", attrs, src, planDir)
	opt.Session = []session.Attachable{secretsprovider.FromMap(secrets)}
	return opt, err
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

// railpackVersion is the Railpack library version compiled in. The frontend
// image must match the version that generated the plan.
var railpackVersion = func() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, m := range info.Deps {
			if m.Path == "github.com/railwayapp/railpack" {
				return m.Version
			}
		}
	}
	return "latest"
}()
