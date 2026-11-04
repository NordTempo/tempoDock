package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

func newStatusCmd(g *globals) *cobra.Command {
	var f targetFlags
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show a service's status, or an environment overview",
		Long: `With a service (linked or --service): its state, active and in-flight
releases, domains and staged changes. Without one, or with --all: every
service in the environment.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			o, err := get[apitypes.ProjectOverview](cmd.Context(), s, t.projectPath()+"/overview")
			if err != nil {
				return err
			}
			var env *apitypes.EnvironmentOverview
			for i := range o.Environments {
				if o.Environments[i].Environment.Name == t.Environment {
					env = &o.Environments[i]
				}
			}
			if env == nil {
				return &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("environment %q not found in project %s", t.Environment, t.Project), Hint: "See `dock environment list`."}
			}
			if t.Service == "" || all {
				return g.io.Print(env, func() error { return printEnvironment(g, *env) })
			}
			for _, so := range env.Services {
				if so.Service.Name == t.Service {
					return g.io.Print(so, func() error {
						if l := lockWords(env.Environment.Lock); l != "" {
							_, _ = fmt.Fprintf(g.io.Out, "%s/%s is %s\n", t.Project, t.Environment, l)
						}
						return printServiceStatus(g, so)
					})
				}
			}
			return &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("service %q not found in %s/%s", t.Service, t.Project, t.Environment), Hint: "See `dock service list`."}
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&all, "all", "a", false, "show every service in the environment, even with a service linked")
	return cmd
}

// serviceState is the service's state for a table cell, or "archived". On
// a server that can't be reached it is the last known one, and says so.
func serviceState(svc apitypes.Service) string {
	switch {
	case svc.ArchivedAt != nil:
		return "archived"
	case svc.Status.ServerOffline:
		return runState(svc.Status) + " (last known)"
	}
	return runState(svc.Status)
}

// runState is a container's state with its ongoing health check (ADR
// 0043): a running container reads "unhealthy" while the check keeps
// failing and "starting" until it first passes, else "running".
func runState(s apitypes.ServiceStatus) string {
	if s.State == apitypes.ServiceRunning && (s.Health == apitypes.HealthUnhealthy || s.Health == apitypes.HealthStarting) {
		return string(s.Health)
	}
	return string(s.State)
}

// restartsNote counts a service's restarts, Docker's and the control
// plane's for a failing health check: " (2 restarts, 1 health restart)",
// or empty without any.
func restartsNote(s apitypes.ServiceStatus) string {
	var parts []string
	if s.RestartCount > 0 {
		parts = append(parts, plural(s.RestartCount, "restart"))
	}
	if s.HealthRestarts > 0 {
		parts = append(parts, plural(s.HealthRestarts, "health restart"))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// serverNote says a live service's server can't be reached, so its state
// is the last known: "server t3node is offline: last known running".
// Empty otherwise.
func serverNote(svc apitypes.Service) string {
	s := svc.Status
	if !s.ServerOffline || svc.ArchivedAt != nil {
		return ""
	}
	return fmt.Sprintf("server %s is %s: last known %s", s.Server, s.ServerStatus, runState(s))
}

// stoppedNote says who stopped a service and when (ADR 0050): "stopped
// 3m ago by alice". Empty for a service that isn't stopped.
func stoppedNote(svc apitypes.Service) string {
	if svc.StoppedAt == nil || svc.ArchivedAt != nil {
		return ""
	}
	return "stopped " + ago(*svc.StoppedAt) + " by " + cmp.Or(svc.StoppedBy, "unknown") + " (start it with `dock service start " + target{svc.Project, svc.Environment, svc.Name}.flags() + "`)"
}

// oomNote says a service's container was killed for running out of
// memory, or is empty.
func oomNote(s apitypes.ServiceStatus) string {
	switch {
	case !s.OOMKilled:
		return ""
	case s.MemoryLimitMB > 0:
		return fmt.Sprintf(", ran out of memory (limit %d MB)", s.MemoryLimitMB)
	default:
		return ", ran out of memory"
	}
}

func printEnvironment(g *globals, env apitypes.EnvironmentOverview) error {
	e := env.Environment
	header := fmt.Sprintf("%s/%s on %s", e.Project, e.Name, e.Server)
	if l := lockWords(e.Lock); l != "" {
		header += ", " + l
	}
	if env.PendingChanges > 0 {
		header += fmt.Sprintf(", %s (see `dock changes`)", plural(env.PendingChanges, "staged change"))
	}
	if i := slices.IndexFunc(env.Services, func(so apitypes.ServiceOverview) bool { return so.Service.Status.ServerOffline }); i >= 0 {
		s := env.Services[i].Service.Status
		header += fmt.Sprintf("; server %s is %s, states are the last known", s.Server, s.ServerStatus)
	}
	_, _ = fmt.Fprintln(g.io.Out, header)
	if len(env.Services) == 0 {
		_, _ = fmt.Fprintln(g.io.Out, "No services yet. Deploy one with `dock deploy -s <name>`.")
		return nil
	}
	t := g.io.Table("service", "state", "release", "deploying", "domain", "pending")
	for _, so := range env.Services {
		if so.Service.ArchivedAt != nil {
			continue
		}
		release, deploying, domain := "-", "-", "-"
		if so.ActiveRelease != nil {
			release = itoa(so.ActiveRelease.Number)
		}
		if so.Deploying != nil {
			deploying = fmt.Sprintf("%d %s", so.Deploying.Number, so.Deploying.Status)
		}
		if len(so.Domains) > 0 {
			domain = so.Domains[0].Hostname
			if len(so.Domains) > 1 {
				domain += fmt.Sprintf(" +%d", len(so.Domains)-1)
			}
		}
		t.Row(so.Service.Name, serviceState(so.Service), release, deploying, domain, strconv.Itoa(so.Service.PendingChanges))
	}
	return t.Flush()
}

func printServiceStatus(g *globals, so apitypes.ServiceOverview) error {
	svc := so.Service
	state := cmp.Or(stoppedNote(svc), serverNote(svc), serviceState(svc))
	state += restartsNote(svc.Status)
	state += oomNote(svc.Status)
	summary := func(r *apitypes.ReleaseSummary) string {
		if r == nil {
			return ""
		}
		return fmt.Sprintf("%d  %s  %s  by %s, %s", r.Number, r.Status, r.ImageRef, r.CreatedBy, ago(r.CreatedAt))
	}
	domains := make([]string, 0, len(so.Domains))
	for _, d := range so.Domains {
		s := d.Hostname + " (" + strings.ReplaceAll(string(d.Status), "_", " ") + ")"
		if d.RedirectTo != "" {
			s += " → " + d.RedirectTo
		}
		domains = append(domains, s)
	}
	pending := ""
	if svc.PendingChanges > 0 {
		pending = plural(svc.PendingChanges, "staged change") + " (see `dock changes`)"
	}
	active := summary(so.ActiveRelease)
	if active == "" {
		active = "none (deploy with `dock deploy`)"
	}
	return g.io.Fields(
		"service", svc.Project+"/"+svc.Environment+"/"+svc.Name,
		"state", state,
		"release", active,
		"deploying", summary(so.Deploying),
		"domains", strings.Join(domains, ", "),
		"pending", pending,
	)
}
