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
	var people bool
	cmd := &cobra.Command{
		Use:   "lock [<environment>] [--reason <text>] [--people]",
		Short: "Lock an environment: tokens can't change anything in it",
		Long: `Lock an environment. Tokens (API tokens, ` + "`dock login`" + ` tokens and MCP, so
agents and this CLI) can still read everything, but every change they try
in it (deploys, rollbacks, restarts, variables, settings, domains,
volumes, archive) is refused with exit code 5 and becomes an access
request that an admin approves or rejects in the UI. Approved, it runs as
the token that asked. People in the web UI are not affected, unless
--people: then only those who may approve there (Approvers, Full access)
change it, and everyone else's change waits for them too.

Locking an environment that is open for a while closes it again. Needs
admin; letting people change it again, like unlocking or opening it,
needs an approver there.`,
		Example: `  dock environment lock production --reason "live, customers use it"
  dock environment lock production --people`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeEnvironmentArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, s, err := lockTarget(g, f, args)
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.envPath()+"/lock", apitypes.LockEnvironmentRequest{Reason: reason, People: people})
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				if people {
					g.io.Successf("Locked %s/%s for people too: only its approvers change it; what others try waits for them in the UI", e.Project, e.Name)
					return nil
				}
				g.io.Successf("Locked %s/%s: tokens can't change anything in it; what they try waits for approval in the UI", e.Project, e.Name)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().StringVar(&reason, "reason", "", "why it is locked, shown with the lock")
	cmd.Flags().BoolVar(&people, "people", false, "hold people's changes too, but its approvers'")
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
		Example:           `  dock environment unlock production --for 30m`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeEnvironmentArg,
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

// newOrgLocksCmd is `dock org locks`: the organization's lock settings.
func newOrgLocksCmd(g *globals) *cobra.Command {
	var production string
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   "locks [--lock-production on|off] [--request-ttl 24h]",
		Short: "Show or change how environments lock and how long access requests wait",
		Long: `Without flags, show the organization's lock settings. --lock-production on
makes new production environments start locked (existing ones keep their
lock); --request-ttl is how long an access request waits for a decision
before it expires (1h to 720h). Changing them needs an unlimited admin.`,
		Example: `  dock org locks --lock-production on --request-ttl 48h`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := g.connect()
			if err != nil {
				return err
			}
			path := apiPrefix + "/org/lock-settings"
			cur, err := get[apitypes.OrgLockSettings](cmd.Context(), s, path)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("lock-production") || cmd.Flags().Changed("request-ttl") {
				switch production {
				case "on":
					cur.LockProduction = true
				case "off":
					cur.LockProduction = false
				case "":
				default:
					return &output.Error{Exit: output.ExitUsage, Message: "--lock-production is on or off"}
				}
				if cmd.Flags().Changed("request-ttl") {
					if ttl < time.Hour || ttl > 720*time.Hour || ttl%time.Hour != 0 {
						return &output.Error{Exit: output.ExitUsage, Message: "--request-ttl is whole hours from 1h to 720h"}
					}
					cur.RequestTTLHours = int(ttl / time.Hour)
				}
				if cur, err = send[apitypes.OrgLockSettings](cmd.Context(), s, http.MethodPut, path, cur); err != nil {
					return err
				}
			}
			return g.io.Print(cur, func() error {
				state := "start unlocked"
				if cur.LockProduction {
					state = "start locked"
				}
				_, _ = fmt.Fprintf(g.io.Out, "New production environments %s; access requests wait %d hours.\n", state, cur.RequestTTLHours)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&production, "lock-production", "", "on: new production environments start locked; off: they don't")
	cmd.Flags().DurationVar(&ttl, "request-ttl", 0, "how long access requests wait for a decision, e.g. 24h")
	return cmd
}
