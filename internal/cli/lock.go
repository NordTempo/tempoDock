package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Locked environments (ADR 0044): `dock environment lock|unlock`, also
// registered under `dock env`.

// newEnvironmentLockCmd is `dock environment lock [<environment>]`.
func newEnvironmentLockCmd(g *globals) *cobra.Command {
	var f targetFlags
	var reason string
	cmd := &cobra.Command{
		Use:   "lock [<environment>] [--reason <text>]",
		Short: "Lock an environment: tokens can't change anything in it",
		Long: `Lock an environment. Tokens (API tokens, ` + "`dock login`" + ` tokens and MCP, so
agents and this CLI) can still read everything, but every change they try
in it (deploys, rollbacks, restarts, variables, settings, domains,
volumes, archive) is refused with exit code 5 and becomes an access
request that an admin approves or rejects in the UI. Approved, it runs as
the token that asked. People in the web UI are not affected.

Locking an environment that is open for a while closes it again. Needs
admin.`,
		Example: `  dock environment lock production --reason "live, customers use it"`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, s, err := lockTarget(g, f, args)
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.envPath()+"/lock", apitypes.LockEnvironmentRequest{Reason: reason})
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				g.io.Successf("Locked %s/%s: tokens can't change anything in it; what they try waits for approval in the UI", e.Project, e.Name)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&reason, "reason", "", "why it is locked, shown with the lock")
	return cmd
}

// newEnvironmentUnlockCmd is `dock environment unlock [<environment>]`.
func newEnvironmentUnlockCmd(g *globals) *cobra.Command {
	var f targetFlags
	var open time.Duration
	cmd := &cobra.Command{
		Use:   "unlock [<environment>] [--for <duration>]",
		Short: "Unlock an environment, or open it for a while",
		Long: `Unlock an environment, or with --for open it for that long (at most a
week), after which it locks again by itself.

The lock applies to this command too: run with a token (as the CLI always
is) in a locked environment, it files an access request that an admin
approves in the UI. Unlocking from the UI takes effect at once.`,
		Example: `  dock environment unlock production --for 30m`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if open < 0 || open > 7*24*time.Hour || open%time.Minute != 0 {
				return &output.Error{Exit: output.ExitUsage, Message: "--for must be whole minutes, at most a week (168h)"}
			}
			t, s, err := lockTarget(g, f, args)
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.envPath()+"/unlock", apitypes.UnlockEnvironmentRequest{Minutes: int(open / time.Minute)})
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				if e.Lock != nil && e.Lock.OpenUntil != nil {
					g.io.Successf("Opened %s/%s until %s; then it locks again", e.Project, e.Name, e.Lock.OpenUntil.Local().Format("15:04"))
				} else {
					g.io.Successf("Unlocked %s/%s", e.Project, e.Name)
				}
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().DurationVar(&open, "for", 0, "open the environment for this long, e.g. 30m or 2h, instead of unlocking it")
	return cmd
}

// lockTarget is the environment a lock command names: its argument, else
// the linked or --environment one.
func lockTarget(g *globals, f targetFlags, args []string) (target, *session, error) {
	need := needEnvironment
	if len(args) == 1 {
		need = needProject
	}
	s, t, err := g.open(f, need)
	if err != nil {
		return t, s, err
	}
	if len(args) == 1 {
		t.Environment = args[0]
	}
	return t, s, nil
}

// lockWords describes an environment's lock for a status line; "" when
// unlocked.
func lockWords(l *apitypes.EnvironmentLock) string {
	switch {
	case l == nil:
		return ""
	case !l.Holds(time.Now()):
		return fmt.Sprintf("open until %s, then locked", l.OpenUntil.Local().Format("15:04"))
	case l.Reason != "":
		return fmt.Sprintf("locked by %s (%s)", l.LockedBy, l.Reason)
	}
	return "locked by " + l.LockedBy
}
