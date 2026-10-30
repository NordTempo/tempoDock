package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	attrs := map[string]string{
		"source":   "ghcr.io/railwayapp/railpack-frontend:" + railpackVersion,
		"filename": planFile,
		// Prefixes every cache mount ID in the plan ("<ns>-<cache name>").
		"build-arg:cache-key": ns,
	}
	if len(req.BuildArgs) > 0 {
		h := sha256.New()
		for _, k := range slices.Sorted(maps.Keys(req.BuildArgs)) {
			_, _ = fmt.Fprintf(h, "%s=%s\n", k, req.BuildArgs[k])
		}
		// Secrets don't invalidate cached layers on their own.
		attrs["build-arg:secrets-hash"] = hex.EncodeToString(h.Sum(nil))
	}
	opt, err := localOpt("gateway.v0", attrs, src, planDir)
	opt.Session = []session.Attachable{secretStore(req.BuildArgs)}
	return opt, err
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
	case err != nil && outOfMemory(stderr.String()):
		return planResult{}, fmt.Errorf("generating the build plan ran out of memory (%d MB); the source may contain symlink loops", planMemory>>20)
	case err != nil:
		return planResult{}, fmt.Errorf("generate build plan: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var res planResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return planResult{}, fmt.Errorf("generate build plan: %w", err)
	}
	return res, nil
}

// outOfMemory reports whether a Go process's crash output says it hit its
// memory cap: "out of memory" when the heap can't grow, "cannot allocate
// memory" when the address-space limit (limitMemory) refuses a mapping
// first, which happens on some kernels and machines.
func outOfMemory(stderr string) bool {
	return strings.Contains(stderr, "out of memory") || strings.Contains(stderr, "cannot allocate memory")
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
