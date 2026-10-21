package cli

import (
	"fmt"
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
					return g.io.Print(so, func() error { return printServiceStatus(g, so) })
				}
			}
			return &output.Error{Exit: output.ExitNotFound, Message: fmt.Sprintf("service %q not found in %s/%s", t.Service, t.Project, t.Environment), Hint: "See `dock service list`."}
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&all, "all", "a", false, "show every service in the environment, even with a service linked")
	return cmd
}

// serviceState is the service's state, or "archived".
func serviceState(svc apitypes.Service) string {
	if svc.ArchivedAt != nil {
		return "archived"
	}
	return string(svc.Status.State)
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
	if env.PendingChanges > 0 {
		header += fmt.Sprintf(", %s (see `dock changes`)", plural(env.PendingChanges, "staged change"))
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
	state := serviceState(svc)
	if svc.Status.RestartCount > 0 {
		state += fmt.Sprintf(" (%d restarts)", svc.Status.RestartCount)
	}
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
