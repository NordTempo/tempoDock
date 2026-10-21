// Package cli implements every `platform` command, including `serve`.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/NordTempo/platform/internal/cli/output"
	"github.com/NordTempo/platform/internal/clientconfig"
	"github.com/NordTempo/platform/internal/version"
)

// Execute runs the CLI and returns the process exit code (see
// output.ExitCode).
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], output.Std())
}

// globals is state shared by all commands.
type globals struct {
	io   *output.IO
	json bool
	// started is set once flags and arguments were accepted, so errors
	// before that point are usage errors.
	started bool
}

// run executes one command line against io and returns the exit code.
func run(ctx context.Context, args []string, io *output.IO) int {
	g := &globals{io: io}
	root := &cobra.Command{
		Use:           "platform",
		Short:         "Deploy Docker apps to your own servers",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			g.started = true
			io.JSON = g.json
		},
	}
	root.PersistentFlags().BoolVar(&g.json, "json", false, "print stable, machine-readable JSON (see docs/cli.md)")
	root.SetArgs(args)
	root.SetIn(io.In)
	root.SetOut(io.Out)
	root.SetErr(io.Err)
	root.AddCommand(
		newServeCmd(),
		newAdminCmd(),
		newOpenAPICmd(),
		newContextCmd(g),
		newLogoutCmd(g),
		newUnlinkCmd(g),
	)

	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	io.JSON = g.json
	if _, ok := errors.AsType[*output.Error](err); !ok && !g.started {
		err = &output.Error{Exit: output.ExitUsage, Err: err, Hint: fmt.Sprintf("Run `%s --help` for usage.", cmd.CommandPath())}
	}
	io.PrintError(err)
	return int(output.ExitCodeOf(err))
}

// group makes cmd a parent of subcommands that shows help when run alone
// and fails with a usage error on an unknown subcommand.
func group(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return &output.Error{
				Exit:    output.ExitUsage,
				Message: fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath()),
				Hint:    fmt.Sprintf("Run `%s --help` for usage.", cmd.CommandPath()),
			}
		}
		return cmd.Help()
	}
	return cmd
}

// userConfig is the loaded CLI config and where to save it.
type userConfig struct {
	*clientconfig.Config
	path string
}

func loadConfig() (userConfig, error) {
	path, err := clientconfig.DefaultPath()
	if err != nil {
		return userConfig{}, err
	}
	cfg, err := clientconfig.Load(path)
	if err != nil {
		return userConfig{}, &output.Error{Exit: output.ExitError, Err: err, Hint: "Fix or delete " + path + "."}
	}
	return userConfig{cfg, path}, nil
}

func (c userConfig) save() error { return c.Save(c.path) }

// resolve picks the context for the current directory (see
// clientconfig.Config.Resolve) and turns failures into exit-coded errors.
func (c userConfig) resolve() (clientconfig.Resolved, error) {
	var link *clientconfig.Link
	if wd, err := os.Getwd(); err == nil {
		if _, l, ok := c.FindLink(wd); ok {
			link = &l
		}
	}
	r, err := c.Resolve(link)
	switch {
	case errors.Is(err, clientconfig.ErrNoContext):
		return r, &output.Error{
			Exit:    output.ExitAuth,
			Message: "no server configured",
			Hint: fmt.Sprintf("Add one with `platform context add <name> --server <url> --token <token>`, or set %s and %s.",
				clientconfig.EnvURL, clientconfig.EnvToken),
		}
	case errors.Is(err, clientconfig.ErrContextNotFound):
		return r, &output.Error{Exit: output.ExitNotFound, Err: err, Hint: "Run `platform context list` to see contexts."}
	}
	return r, err
}
