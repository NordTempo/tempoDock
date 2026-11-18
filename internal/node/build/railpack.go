package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
	"github.com/railwayapp/railpack/core/logger"

	"github.com/NordTempo/tempoDock/internal/node"
)

const planFile = "railpack-plan.json"

// railpackOpt generates a Railpack plan for src and returns options to build
// it with the Railpack frontend. Build args become Railpack variables: they
// configure the plan (RAILPACK_* settings) and are exposed to build steps as
// secrets, the way Railway does it.
func railpackOpt(ctx context.Context, dir, src string, req node.BuildRequest, ns string, logs io.Writer) (bkclient.SolveOpt, error) {
	// Railpack joins these onto src and reads them on the host.
	for _, k := range []string{"RAILPACK_CONFIG_FILE", "RAILPACK_SHELL_SCRIPT"} {
		if v := strings.TrimSpace(req.BuildArgs[k]); v != "" && !filepath.IsLocal(v) {
			return bkclient.SolveOpt{}, fmt.Errorf("%s must be a path inside the source", k)
		}
	}
	// The planner runs mise on this host; it has to be the checked one.
	if err := ensureMise(ctx, logs); err != nil {
		return bkclient.SolveOpt{}, err
	}
	res, err := runPlan(ctx, planInput{
		Dir:          src,
		Vars:         req.BuildArgs,
		BuildCommand: req.BuildCommand,
		StartCommand: req.StartCommand,
	})
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	for _, l := range res.Logs {
		_, _ = fmt.Fprintf(logs, "railpack %s: %s\n", l.Level, l.Msg)
	}
	if res.Error != "" {
		return bkclient.SolveOpt{}, errors.New(res.Error)
	}
	if !res.Success {
		return bkclient.SolveOpt{}, errors.New("railpack could not work out how to build this app; add a Dockerfile or see the log above")
	}

	planDir := filepath.Join(dir, "plan")
	if err := os.Mkdir(planDir, 0o700); err != nil {
		return bkclient.SolveOpt{}, err
	}
	if err := os.WriteFile(filepath.Join(planDir, planFile), res.Plan, 0o600); err != nil {
		return bkclient.SolveOpt{}, err
	}

	frontend := railpackFrontend()
	if d, ok := railpackImages[frontend]; ok {
		frontend += "@" + d
	}
	attrs := map[string]string{
		"source":   frontend,
		"filename": planFile,
		// Prefixes every cache mount ID in the plan ("<ns>-<cache name>").
		"build-arg:cache-key": ns,
	}
	if len(req.BuildArgs) > 0 {
		// Secrets don't invalidate cached layers on their own (see
		// Cache isolation in the package doc).
		attrs["build-arg:secrets-hash"] = secretsKey(ns, req.BuildArgs, slices.Collect(maps.Keys(req.BuildArgs)))
	}
	opt, err := localOpt("gateway.v0", attrs, src, planDir)
	opt.Session = []session.Attachable{secretStore(req.BuildArgs)}
	opt.SourcePolicy = railpackPolicy()
	return opt, err
}

// railpackFrontend is the Railpack frontend image for the plans this
// binary generates.
func railpackFrontend() string { return "ghcr.io/railwayapp/railpack-frontend:" + railpackVersion }

// railpackImages pins the images a Railpack build uses to their digests:
// the frontend, the builder and runtime images its plans name, and the
// alpine image it hashes secrets in (a helper; nothing of it reaches the
// image). Unpinned, BuildKit asks each image's registry what its tag
// points at on every build, about 15 s of a 35 s warm build on the dev
// VM (ADR 0089). The Railpack tags are versioned, so the pins change
// only with Railpack; TestRailpackImagesPinned fails when an upgrade
// names others. A digest is the index digest, e.g. the
// docker-content-digest of `curl -I` on the manifest with an OCI index
// Accept header.
var railpackImages = map[string]string{
	"ghcr.io/railwayapp/railpack-frontend:v0.43.0":        "sha256:355ef61fae6f2551103cd8b10c710cb07167dd353d561f15c69b8d7ac1c676cc",
	"ghcr.io/railwayapp/railpack-builder:mise-2026.10.14": "sha256:03df9e9aa1e786c2e0dd12c5409acad3bbd5a0fa8a75c1117c7ddab5c0c3fde4",
	"ghcr.io/railwayapp/railpack-runtime:mise-2026.10.14": "sha256:02820533713338569ae4d4f0df986c3e7cc6e93457fd06fbb0d2cfa86fba9cdb",
	"docker.io/library/alpine:latest":                     "sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf",
}

// railpackPolicy is a BuildKit source policy that swaps each pinned image
// (railpackImages) for its digest wherever the build names it.
func railpackPolicy() *spb.Policy {
	p := &spb.Policy{}
	for _, ref := range slices.Sorted(maps.Keys(railpackImages)) {
		p.Rules = append(p.Rules, &spb.Rule{
			Action:   spb.PolicyAction_CONVERT,
			Selector: &spb.Selector{Identifier: "docker-image://" + ref},
			Updates:  &spb.Update{Identifier: "docker-image://" + ref + "@" + railpackImages[ref]},
		})
	}
	return p
}

// PlanCommand is the hidden first argument that makes the dock binary run
// PlanMain instead of the CLI.
const PlanCommand = "__railpack-plan"

// Caps for plan generation. Railpack follows directory symlinks, so a
// source with symlink loops could otherwise run forever or exhaust memory.
// planTimeout is a variable so tests can shorten it.
var planTimeout = 60 * time.Second

const planMemory uint64 = 512 << 20

// planInput is what the daemon sends PlanMain on stdin.
type planInput struct {
	Dir          string            `json:"dir"`
	Vars         map[string]string `json:"vars"`
	BuildCommand string            `json:"buildCommand"`
	StartCommand string            `json:"startCommand"`
}

// planResult is what PlanMain writes to stdout. Error is a user-facing
// message for a failure inside Railpack.
type planResult struct {
	Logs    []logger.Msg    `json:"logs,omitempty"`
	Plan    json.RawMessage `json:"plan,omitempty"`
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
}

// PlanMain generates a Railpack plan in a child process started by Build:
// it reads a planInput as JSON from stdin, writes a planResult to stdout
// and returns the exit code. The CLI must call it before parsing arguments
// when os.Args[1] is PlanCommand.
func PlanMain() int {
	// Hard cap (where supported) plus a soft one below it, so the GC works
	// harder before the hard cap kills the process.
	limitMemory(planMemory)
	debug.SetMemoryLimit(int64(planMemory / 4 * 3))

	// Nothing but the result may reach stdout.
	out := os.Stdout
	os.Stdout = os.Stderr

	var in planInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "read plan input:", err)
		return 1
	}
	if err := json.NewEncoder(out).Encode(generatePlan(in)); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "write plan:", err)
		return 1
	}
	return 0
}

// generatePlan runs Railpack's plan generation in this process.
func generatePlan(in planInput) planResult {
	a, err := app.NewApp(in.Dir)
	if err != nil {
		return planResult{Error: fmt.Sprintf("railpack: %v", err)}
	}
	vars := maps.Clone(in.Vars) // Railpack keeps the map
	res, err := core.GenerateBuildPlan(a, app.NewEnvironment(&vars), &core.GenerateBuildPlanOptions{
		RailpackVersion:          railpackVersion,
		BuildCommand:             in.BuildCommand,
		StartCommand:             in.StartCommand,
		ErrorMissingStartCommand: true,
	})
	out := planResult{Logs: res.Logs, Success: res.Success}
	if err != nil {
		out.Error = fmt.Sprintf("railpack (temporary failure, retry): %v", err)
		return out
	}
	if out.Plan, err = json.Marshal(res.Plan); err != nil {
		out.Error = fmt.Sprintf("railpack: %v", err)
	}
	return out
}

var errPlanTimeout = errors.New("plan timeout")

// runPlan runs PlanMain in a child copy of this binary, killing it after
// planTimeout. Variables go over stdin, not argv or the environment, so
// they don't show up in the process table.
func runPlan(ctx context.Context, in planInput) (planResult, error) {
	exe := "/proc/self/exe" // the running binary, even if replaced on disk since
	if runtime.GOOS != "linux" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return planResult{}, err
		}
	}
	input, err := json.Marshal(in)
	if err != nil {
		return planResult{}, err
	}

	ctx, cancel := context.WithTimeoutCause(ctx, planTimeout, errPlanTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, PlanCommand)
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	stderr := &headWriter{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	cmd.WaitDelay = 5 * time.Second // in case a grandchild holds the pipes open

	err = cmd.Run()
	switch {
	case errors.Is(context.Cause(ctx), errPlanTimeout):
		return planResult{}, fmt.Errorf("generating the build plan took longer than %s; the source may contain symlink loops", planTimeout)
	case ctx.Err() != nil:
		return planResult{}, context.Cause(ctx)
	case err != nil && runtimeCrash(stderr.String()):
		return planResult{}, fmt.Errorf("generating the build plan ran out of memory (%d MB) and crashed; the source may contain symlink loops", planMemory>>20)
	case err != nil:
		return planResult{}, fmt.Errorf("generate build plan: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var res planResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return planResult{}, fmt.Errorf("generate build plan: %w", err)
	}
	return res, nil
}

// runtimeCrash reports whether a Go process's crash output is the Go
// runtime dying rather than a planner error: what hitting the memory cap
// looks like. Go says "out of memory" when the heap can't grow, "cannot
// allocate memory" when the address-space limit (limitMemory) refuses a
// mapping, and on some machines its garbage collector faults outright
// (SIGSEGV) once a mapping fails. The planner itself never crashes the
// runtime on a valid source.
func runtimeCrash(stderr string) bool {
	return strings.Contains(stderr, "out of memory") || strings.Contains(stderr, "cannot allocate memory") ||
		strings.HasPrefix(strings.TrimSpace(stderr), "fatal error: runtime:") ||
		strings.HasPrefix(strings.TrimSpace(stderr), "SIGSEGV") || strings.HasPrefix(strings.TrimSpace(stderr), "unexpected fault address")
}

// headWriter keeps the first max bytes written to it and drops the rest.
// A Go crash prints the reason first, then every goroutine's stack.
type headWriter struct {
	buf bytes.Buffer
	max int
}

func (w *headWriter) Write(p []byte) (int, error) {
	w.buf.Write(p[:min(len(p), max(0, w.max-w.buf.Len()))])
	return len(p), nil
}

func (w *headWriter) String() string { return w.buf.String() }

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
