package cli

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Sleeping environments: `dock env sleep|wake|sleep-after`, also under
// `dock environment`.

// newEnvironmentSleepCmd is `dock env sleep [<environment>]`.
func newEnvironmentSleepCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "sleep [<environment>]",
		Short: "Put an environment to sleep now; the next request wakes it",
		Long: `Stop every service of a non-production environment, keeping their
containers, so they free their memory. The next request to one of its
domains wakes it (the request waits while it does), as do a deploy, a
` + "`dock run`" + `, a job's run now and the Data panel. Services stopped with
` + "`dock service stop`" + ` stay stopped. Production environments never sleep.
Needs deploy.`,
		Example:           `  dock env sleep pr-12`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeEnvironmentArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, s, err := lockTarget(g, f, args)
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.envPath()+"/sleep", nil)
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				g.io.Successf("%s/%s is %s", e.Project, e.Name, sleepWords(e.Sleep))
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

// newEnvironmentWakeCmd is `dock env wake [<environment>]`.
func newEnvironmentWakeCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "wake [<environment>]",
		Short: "Wake an asleep environment and wait until it is awake",
		Long: `Start an asleep environment's services again, databases first, and wait
until each passed its health check. Opening one of its domains does the
same. Needs read.`,
		Example:           `  dock env wake pr-12`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeEnvironmentArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, s, err := lockTarget(g, f, args)
			if err != nil {
				return err
			}
			start := time.Now()
			g.io.Progressf("Waking %s/%s", t.Project, t.Environment)
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPost, t.envPath()+"/wake", nil)
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				g.io.Successf("%s/%s is awake (%.1fs)", e.Project, e.Name, time.Since(start).Seconds())
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

// newEnvironmentSleepAfterCmd is `dock env sleep-after [<environment>]
// <duration>|off`.
func newEnvironmentSleepAfterCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "sleep-after [<environment>] <duration>|off",
		Short: "Make a non-production environment sleep after a while without requests",
		Long: `Make a non-production environment sleep on its own after <duration>
without requests to its domains (5m to 24h, whole minutes); off turns it
off. Open websockets and streams, deploys and runs keep it awake.
Previews take their project's setting
(` + "`dock env previews enable <base> --sleep-after 30m`" + `). Needs admin.`,
		Example: `  dock env sleep-after staging 2h
  dock env sleep-after staging off`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mins, err := parseSleepAfter(args[len(args)-1])
			if err != nil {
				return err
			}
			if mins < 0 {
				mins = 0
			}
			t, s, err := lockTarget(g, f, args[:len(args)-1])
			if err != nil {
				return err
			}
			e, err := send[apitypes.Environment](cmd.Context(), s, http.MethodPut, t.envPath()+"/sleep", apitypes.SetSleepRequest{AfterMinutes: mins})
			if err != nil {
				return err
			}
			return g.io.Print(e, func() error {
				if mins == 0 {
					g.io.Successf("%s/%s no longer sleeps on its own", e.Project, e.Name)
				} else {
					g.io.Successf("%s/%s sleeps after %s without requests", e.Project, e.Name, minutesText(mins))
				}
				if e.Sleep != nil && e.Sleep.Blocked != "" {
					g.io.Progressf("It can't sleep on its own yet: %s.", e.Sleep.Blocked)
				}
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	return cmd
}

// parseSleepAfter reads a sleep setting: a duration in whole minutes, or
// "off" (-1).
func parseSleepAfter(s string) (int, error) {
	if strings.EqualFold(s, "off") {
		return -1, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d%time.Minute != 0 || d < apitypes.SleepAfterMin*time.Minute || d > apitypes.SleepAfterMax*time.Minute {
		return 0, &output.Error{Exit: output.ExitUsage, Message: fmt.Sprintf("%q is not a sleep setting; use whole minutes from 5m to 24h, e.g. 30m or 2h, or off", s)}
	}
	return int(d / time.Minute), nil
}

// sleepFlag is the --sleep-after of `dock env previews enable`.
type sleepFlag struct{ minutes *int }

func (f sleepFlag) String() string { return "" }
func (f sleepFlag) Type() string   { return "duration|off" }
func (f sleepFlag) Set(s string) error {
	m, err := parseSleepAfter(s)
	if err == nil {
		*f.minutes = m
	}
	return err
}

// minutesText says a number of minutes the way people say it: 30 min, 2h.
func minutesText(m int) string {
	if m%60 == 0 {
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%d min", m)
}

// sleepWords describes an environment's sleep for a table or status
// line; "" for one that never sleeps.
func sleepWords(sl *apitypes.EnvironmentSleep) string {
	if sl == nil {
		return ""
	}
	switch sl.State {
	case apitypes.SleepAsleep:
		s := "asleep"
		if sl.Since != nil {
			s += " " + strings.TrimSuffix(ago(*sl.Since), " ago")
		}
		if sl.FreedBytes > 0 {
			s += fmt.Sprintf(" (%s freed)", bytesize(sl.FreedBytes))
		}
		return s
	case apitypes.SleepWaking:
		return "waking"
	case apitypes.SleepGoing:
		return "going to sleep"
	}
	switch {
	case sl.Blocked != "":
		return "awake, can't sleep: " + sl.Blocked
	case sl.SleepsAt != nil:
		if d := time.Until(*sl.SleepsAt); d > 0 {
			return "awake, sleeps in " + minutesText(max(1, int(d.Round(time.Minute)/time.Minute)))
		}
		return "awake, sleeps soon"
	}
	return "awake"
}
