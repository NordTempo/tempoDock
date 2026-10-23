// Package cli implements every `dock` command, including `serve`.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Execute runs the CLI and returns the process exit code (see
// output.ExitCode).
func Execute() int {
	if code, ok := helperMain(os.Args); ok {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], output.Std())
}

// globals is state shared by all commands.
type globals struct {
	io   *output.IO
	json bool
	// org is --org: the organization to act in, over DOCK_ORG, the link's
	// and the context's.
	org string
	// started is set once flags and arguments were accepted, so errors
	// before that point are usage errors.
	started bool
}

// run executes one command line against io and returns the exit code.
func run(ctx context.Context, args []string, io *output.IO) int {
	g := &globals{io: io}
	root := &cobra.Command{
		Use:           "dock",
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
	root.PersistentFlags().StringVar(&g.org, "org", "", "organization to act in (default: the linked one, then `dock org use`'s)")
	root.SetArgs(args)
	root.SetIn(io.In)
	root.SetOut(io.Out)
	root.SetErr(io.Err)
	addGroup(root, "start", "Getting started:",
		newLoginCmd(g), newLinkCmd(g), newUnlinkCmd(g), newWhoamiCmd(g), newMeCmd(g), newContextCmd(g), newOrgCmd(g), newLogoutCmd(g), newVersionCmd(g),
		newMCPCmd(g))
	addGroup(root, "ship", "Deploy and operate:",
		newDeployCmd(g), newPromoteCmd(g), newStatusCmd(g), newLogsCmd(g), newReleaseListCmd(g, "releases"), newReleaseCmd(g),
		newRollbackCmd(g), newRestartCmd(g), newDBCmd(g), newConnectCmd(g), newGitCmd(g), newBackupCmd(g))
	addGroup(root, "config", "Configure (changes are staged until applied):",
		newEnvCmd(g), newChangesCmd(g), newApplyCmd(g), newDiscardCmd(g), newDomainCmd(g), newVolumeCmd(g),
		newServiceCmd(g), newEnvironmentCmd(g), newProjectCmd(g))
	addGroup(root, "admin", "Administer:",
		newTokenCmd(g), newUserCmd(g), newAuditCmd(g), newServerCmd(g), newRegistryCmd(g), newAlertsCmd(g))
	if cmds := serverCommands(); len(cmds) > 0 {
		addGroup(root, "server", "Run a server:", cmds...)
	}

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

// addGroup adds cmds to root under a help heading.
func addGroup(root *cobra.Command, id, title string, cmds ...*cobra.Command) {
	root.AddGroup(&cobra.Group{ID: id, Title: title})
	for _, c := range cmds {
		c.GroupID = id
		root.AddCommand(c)
	}
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
	_, link := c.currentLink()
	return c.resolveFor(link)
}

// currentLink returns the link that applies to the working directory and
// the directory it is stored under, or nil.
func (c userConfig) currentLink() (string, *clientconfig.Link) {
	wd, err := os.Getwd()
	if err != nil {
		return "", nil
	}
	dir, l, ok := c.FindLink(wd)
	if !ok {
		return "", nil
	}
	return dir, &l
}

// resolveFor is resolve with a given link (which may be nil).
func (c userConfig) resolveFor(link *clientconfig.Link) (clientconfig.Resolved, error) {
	r, err := c.Resolve(link)
	switch {
	case errors.Is(err, clientconfig.ErrNoContext):
		return r, &output.Error{
			Exit:    output.ExitAuth,
			Message: "no server configured",
			Hint: fmt.Sprintf("Log in with `dock login <server-url>`, or set %s and %s.",
				clientconfig.EnvURL, clientconfig.EnvToken),
		}
	case errors.Is(err, clientconfig.ErrContextNotFound):
		return r, &output.Error{Exit: output.ExitNotFound, Err: err, Hint: "Run `dock context list` to see contexts."}
	case errors.Is(err, clientconfig.ErrTokenWithoutURL):
		return r, &output.Error{
			Exit:    output.ExitUsage,
			Err:     err,
			Message: fmt.Sprintf("%s is set but %s is not; the token is only sent to the server %s names", clientconfig.EnvToken, clientconfig.EnvURL, clientconfig.EnvURL),
			Hint:    fmt.Sprintf("Set %s to the token's server, or unset %s to use the context's own token.", clientconfig.EnvURL, clientconfig.EnvToken),
		}
	}
	return r, err
}
