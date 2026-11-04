package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Importing other formats into a project (ADR 0049). The daemon converts,
// so the CLI stays free of YAML and Compose code: it sends the files and
// prints what comes back.

// maxImportFile caps each file the CLI sends with an import.
const maxImportFile = 1 << 20

func newImportCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a project from another format",
	}
	cmd.AddCommand(newImportComposeCmd(g))
	return cmd
}

// newImportComposeCmd is `dock import compose`: convert a Compose file
// into a project file, show the plan, and with --apply stage and apply it.
func newImportComposeCmd(g *globals) *cobra.Command {
	var project, outPath string
	var apply, yes bool
	cmd := &cobra.Command{
		Use:   "compose [FILE] [--project NAME] [-o OUT] [--apply]",
		Short: "Convert a docker-compose.yml into a project, and plan or create it",
		Long: `Convert a Docker Compose file (default: docker-compose.yml, or compose.yaml)
into a Dock project file and show what applying it would do. Nothing
changes without --apply.

postgres and redis images become Dock's database templates, with backups.
Everything Dock can't run as written (bind mounts, host networking,
privileged mode, extra ports, ...) is listed as a warning: nothing is
dropped silently. Secrets (passwords, tokens, keys) are kept out of the
file; their values come from the Compose file or are asked for.

The .env next to the Compose file and the env_file files it names are sent
with it. --project names the project (default: the directory's name).

  -o OUT     also write the project file, for ` + "`dock apply OUT`" + ` later
  --apply    stage and apply it (asks for each secret's value on a terminal;
             Enter keeps the Compose file's), after confirming, or --yes`,
		Example: `  dock import compose                         # plan ./docker-compose.yml
  dock import compose stack.yml -p shop -o dock.project.toml
  dock import compose --apply`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := composePath(args)
			if err != nil {
				return err
			}
			if project == "" {
				project = projectFromDir(path)
			}
			ctx := cmd.Context()
			s, err := g.connect()
			if err != nil {
				return err
			}
			req, err := composeRequest(path)
			if err != nil {
				return err
			}
			endpoint := apiPrefix + "/projects/" + project + "/import/compose"
			res, err := send[apitypes.ComposeImport](ctx, s, http.MethodPost, endpoint, req)
			if err != nil {
				return err
			}
			if len(res.MissingFiles) > 0 && addEnvFiles(&req, filepath.Dir(path), res.MissingFiles) {
				if res, err = send[apitypes.ComposeImport](ctx, s, http.MethodPost, endpoint, req); err != nil {
					return err
				}
			}
			if outPath != "" {
				if err := writeNew(outPath, res.File); err != nil {
					return err
				}
			}
			if !apply {
				return g.io.Print(res, func() error {
					printImport(g, res, outPath)
					g.io.Progressf("Create it with `dock import compose %s -p %s --apply`%s.", path, project, applyLater(outPath))
					return nil
				})
			}
			if !g.io.JSON {
				printImport(g, res, outPath)
			}
			if req.Values, err = askSecrets(g, res.Secrets, yes); err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Stage and apply this plan in project %s?", project), "yes"); err != nil {
					return err
				}
			}
			req.Apply = true
			res, err = send[apitypes.ComposeImport](ctx, s, http.MethodPost, endpoint, req)
			if err != nil {
				return err
			}
			return g.io.Print(res, func() error {
				printImported(g, project, res)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&project, "project", "p", "", "project name (default: the Compose file's directory)")
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "also write the project file here")
	cmd.Flags().BoolVar(&apply, "apply", false, "stage and apply it")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask: keep the Compose file's secret values and apply")
	return cmd
}

// composePath is the Compose file to read: the argument, or the first of
// the default names that exists.
func composePath(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yaml", "compose.yml"} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return "", usageError("no docker-compose.yml here", "Pass the Compose file: `dock import compose path/to/compose.yml`.")
}

var notName = regexp.MustCompile(`[^a-z0-9-]+`)

// projectFromDir is a project name from the Compose file's directory.
func projectFromDir(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "app"
	}
	name := strings.Trim(notName.ReplaceAllString(strings.ToLower(filepath.Base(filepath.Dir(abs))), "-"), "-")
	if len(name) > 32 {
		name = strings.Trim(name[:32], "-")
	}
	if name == "" {
		return "app"
	}
	return name
}

// composeRequest reads the Compose file and the .env next to it.
func composeRequest(path string) (apitypes.ComposeImportRequest, error) {
	var req apitypes.ComposeImportRequest
	b, err := readLimited(path)
	if err != nil {
		return req, &output.Error{Exit: output.ExitUsage, Message: err.Error()}
	}
	req.Compose = string(b)
	req.EnvFiles = map[string]string{}
	if env, err := readLimited(filepath.Join(filepath.Dir(path), ".env")); err == nil {
		req.EnvFiles[".env"] = string(env)
	}
	return req, nil
}

// addEnvFiles adds the env files the daemon asked for, read relative to
// the Compose file's directory, and reports whether it found any.
func addEnvFiles(req *apitypes.ComposeImportRequest, dir string, names []string) bool {
	found := false
	for _, name := range names {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, name)
		}
		if b, err := readLimited(p); err == nil {
			req.EnvFiles[name] = string(b)
			found = true
		}
	}
	return found
}

// readLimited reads a file the CLI sends, refusing one over maxImportFile.
func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxImportFile+1))
	if err == nil && len(b) > maxImportFile {
		err = fmt.Errorf("%s is larger than %d KB", path, maxImportFile>>10)
	}
	return b, err
}

// writeNew writes the project file, never over an existing file.
func writeNew(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return usageError(path+" exists", "Pass another -o path, or remove it first.")
	}
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func applyLater(outPath string) string {
	if outPath == "" {
		return ""
	}
	return fmt.Sprintf(", or apply %s with `dock apply %s`", outPath, outPath)
}

// askSecrets asks for each secret's value on a terminal: Enter keeps the
// Compose file's. Without a terminal (or with --yes) the Compose file's
// values are used, and a secret without one stays to be set.
func askSecrets(g *globals, secrets []apitypes.ComposeSecret, yes bool) ([]apitypes.VariableValue, error) {
	var out []apitypes.VariableValue
	if yes || !g.io.Interactive || g.io.JSON {
		return out, nil
	}
	for _, sec := range secrets {
		label := fmt.Sprintf("Value for %s.%s", sec.Service, sec.Key)
		if sec.HasValue {
			label += " (Enter keeps the Compose file's)"
		} else {
			label += " (Enter leaves it to set later)"
		}
		v, err := g.io.PromptSecret(label, "yes")
		if err != nil {
			if e, ok := errors.AsType[*output.Error](err); ok && e.Exit == output.ExitUsage {
				continue // empty: keep
			}
			return nil, err
		}
		out = append(out, apitypes.VariableValue{Environment: sec.Environment, Service: sec.Service, Key: sec.Key, Value: v})
	}
	return out, nil
}

// printImport prints a conversion: warnings, secrets, builds and the plan.
func printImport(g *globals, res apitypes.ComposeImport, outPath string) {
	for _, w := range res.Warnings {
		where := w.Service
		if where == "" {
			where = "compose"
		}
		g.io.Warnf("%s: %s", where, w.Message)
	}
	for _, sec := range res.Secrets {
		state := "value from the Compose file, never printed"
		if !sec.HasValue {
			state = fmt.Sprintf("needs a value: `dock env set -s %s %s=...`", sec.Service, sec.Key)
		}
		g.io.Progressf("Secret %s.%s: %s", sec.Service, sec.Key, state)
	}
	printPlan(g, res.Plan)
	if outPath != "" {
		g.io.Successf("Wrote %s", outPath)
	}
}

// printImported reports what --apply did, and how to deploy services
// built from source.
func printImported(g *globals, project string, res apitypes.ComposeImport) {
	if res.Result == nil {
		return
	}
	for _, a := range res.Result.Applied {
		g.io.Successf("Applied %s/%s: %s started", project, a.Environment, plural(len(a.Releases), "release"))
		for _, f := range a.Failed {
			g.io.Warnf("Still staged: %s", f)
		}
	}
	for _, b := range res.Builds {
		g.io.Progressf("%s builds from source: deploy it with `dock deploy %s -p %s -e production -s %s`.", b.Service, b.Context, project, b.Service)
	}
	g.io.Progressf("Follow the releases with `dock status -p %s -e production`.", project)
}
