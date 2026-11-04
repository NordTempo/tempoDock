package cli

import (
	"cmp"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

func newServiceCmd(g *globals) *cobra.Command {
	cmd := group(&cobra.Command{Use: "service", Short: "Manage services"})
	cmd.AddCommand(
		newServiceListCmd(g),
		newServiceCreateCmd(g),
		newServiceShowCmd(g),
		newServiceUpdateCmd(g),
		newServiceStopCmd(g),
		newServiceStartCmd(g),
		newServiceArchiveCmd(g),
	)
	return cmd
}

func newServiceListCmd(g *globals) *cobra.Command {
	var f targetFlags
	var all bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List an environment's services",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			svcs, err := get[[]apitypes.Service](cmd.Context(), s, t.envPath()+"/services")
			if err != nil {
				return err
			}
			if !all {
				svcs = live(svcs)
			}
			return g.io.Print(svcs, func() error {
				tb := g.io.Table("name", "state", "release", "image", "pending")
				for _, svc := range svcs {
					tb.Row(svc.Name, serviceState(svc), releaseNumber(svc.ActiveRelease), dash(svc.Image), strconv.Itoa(svc.PendingChanges))
				}
				return tb.Flush()
			})
		},
	}
	f.add(cmd, needEnvironment)
	cmd.Flags().BoolVar(&all, "all", false, "include archived services")
	return cmd
}

func releaseNumber(n *int64) string {
	if n == nil {
		return "-"
	}
	return itoa(*n)
}

// settingsFlags are the service settings shared by `service create` and
// `service update`.
type settingsFlags struct {
	image, healthCheckPath, startCommand, dockerfile, buildCommand string
	port, memoryMB, healthCheckRetries                             int
	healthCheckTimeout, probeTimeout, startPeriod                  time.Duration
	interval                                                       intervalFlag
	preDeployCommand                                               string
	preDeployTimeout                                               time.Duration
	cpus                                                           float64
}

// intervalFlag is --health-check-interval in whole seconds: a duration,
// 0 for the default, or "off" (-1, the API's value for no ongoing check).
type intervalFlag int

func (v *intervalFlag) Set(s string) error {
	if s == "off" {
		*v = -1
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return fmt.Errorf("%q is not a duration; use e.g. 30s, 0 for the default, or off", s)
	}
	*v = intervalFlag(d / time.Second)
	return nil
}

func (v *intervalFlag) String() string {
	switch *v {
	case 0:
		return ""
	case -1:
		return "off"
	}
	return (time.Duration(*v) * time.Second).String()
}

func (v *intervalFlag) Type() string { return "duration|off" }

func (f *settingsFlags) add(fs *pflag.FlagSet) {
	fs.StringVar(&f.image, "image", "", "image to deploy, e.g. postgres:17 (image deploys keep it up to date)")
	fs.IntVar(&f.port, "port", 0, "container port that receives traffic (0: 8080)")
	fs.StringVar(&f.healthCheckPath, "health-check-path", "", "HTTP path that must answer 2xx/3xx (empty: TCP check)")
	fs.DurationVar(&f.healthCheckTimeout, "health-check-timeout", 0, "how long a new release may take to pass, e.g. 60s")
	fs.Var(&f.interval, "health-check-interval", "time between health probes while the service runs, or off (0: 30s)")
	fs.DurationVar(&f.probeTimeout, "health-check-probe-timeout", 0, "how long one ongoing health probe may take (0: 5s)")
	fs.IntVar(&f.healthCheckRetries, "health-check-retries", 0, "failed probes in a row before the service is restarted (0: 3)")
	fs.DurationVar(&f.startPeriod, "health-check-start-period", 0, "time after a (re)start in which failed probes don't count (0: the health check timeout)")
	fs.StringVar(&f.startCommand, "start-command", "", "command that overrides the image's")
	fs.StringVar(&f.dockerfile, "dockerfile", "", "Dockerfile path relative to the source root")
	fs.StringVar(&f.buildCommand, "build-command", "", "Railpack build command (builds without a Dockerfile)")
	fs.IntVar(&f.memoryMB, "memory", 0, "memory limit in MB")
	fs.Float64Var(&f.cpus, "cpus", 0, "CPU limit, e.g. 0.5")
	fs.StringVar(&f.preDeployCommand, "pre-deploy-command", "", "runs with each new image before it gets traffic, e.g. migrations (empty: none)")
	fs.DurationVar(&f.preDeployTimeout, "pre-deploy-timeout", 0, "how long the pre-deploy command may run, e.g. 15m (0: 10m)")
}

func (f *settingsFlags) create(name string) apitypes.CreateServiceRequest {
	return apitypes.CreateServiceRequest{
		Name: name, Image: f.image, Port: f.port, HealthCheckPath: f.healthCheckPath,
		HealthCheckTimeoutSeconds: int(f.healthCheckTimeout.Seconds()), StartCommand: f.startCommand,
		DockerfilePath: f.dockerfile, BuildCommand: f.buildCommand, MemoryMB: f.memoryMB, CPUs: f.cpus,
		HealthCheckIntervalSeconds:     int(f.interval),
		HealthCheckProbeTimeoutSeconds: int(f.probeTimeout.Seconds()),
		HealthCheckRetries:             f.healthCheckRetries,
		HealthCheckStartPeriodSeconds:  int(f.startPeriod.Seconds()),
		PreDeployCommand:               f.preDeployCommand, PreDeployTimeoutSeconds: int(f.preDeployTimeout.Seconds()),
	}
}

// update sets only the fields whose flags were given, so an empty value
// clears a setting and a missing flag leaves it alone. It also returns the
// names of those flags.
func (f *settingsFlags) update(fs *pflag.FlagSet) (apitypes.UpdateServiceRequest, []string) {
	var req apitypes.UpdateServiceRequest
	var changed []string
	set := func(flag string, apply func()) {
		if fs.Changed(flag) {
			apply()
			changed = append(changed, flag)
		}
	}
	timeout, preTimeout := int(f.healthCheckTimeout.Seconds()), int(f.preDeployTimeout.Seconds())
	interval, probeTimeout, startPeriod := int(f.interval), int(f.probeTimeout.Seconds()), int(f.startPeriod.Seconds())
	set("image", func() { req.Image = &f.image })
	set("port", func() { req.Port = &f.port })
	set("health-check-path", func() { req.HealthCheckPath = &f.healthCheckPath })
	set("health-check-timeout", func() { req.HealthCheckTimeoutSeconds = &timeout })
	set("start-command", func() { req.StartCommand = &f.startCommand })
	set("dockerfile", func() { req.DockerfilePath = &f.dockerfile })
	set("build-command", func() { req.BuildCommand = &f.buildCommand })
	set("memory", func() { req.MemoryMB = &f.memoryMB })
	set("cpus", func() { req.CPUs = &f.cpus })
	set("health-check-interval", func() { req.HealthCheckIntervalSeconds = &interval })
	set("health-check-probe-timeout", func() { req.HealthCheckProbeTimeoutSeconds = &probeTimeout })
	set("health-check-retries", func() { req.HealthCheckRetries = &f.healthCheckRetries })
	set("health-check-start-period", func() { req.HealthCheckStartPeriodSeconds = &startPeriod })
	set("pre-deploy-command", func() { req.PreDeployCommand = &f.preDeployCommand })
	set("pre-deploy-timeout", func() { req.PreDeployTimeoutSeconds = &preTimeout })
	return req, changed
}

func newServiceCreateCmd(g *globals) *cobra.Command {
	var f targetFlags
	var settings settingsFlags
	var template string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a service (without deploying it), or a database from a template",
		Long: `Create a service. Nothing is deployed until you deploy it.

With --template (postgres or redis) it is a database: a pinned image, a volume,
generated credentials and small-server defaults, deployed right away and
backed up daily. Other services reference it, e.g. ${{db.DATABASE_URL}}.`,
		Example: `  dock service create api --image ghcr.io/acme/api:1.4
  dock service create db --template postgres
  dock service create cache --template redis`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			req := settings.create(args[0])
			req.Template = apitypes.Template(template)
			svc, err := send[apitypes.Service](cmd.Context(), s, http.MethodPost, t.envPath()+"/services", req)
			if err != nil {
				return err
			}
			var exports []string
			if template != "" {
				tpls, err := get[[]apitypes.TemplateInfo](cmd.Context(), s, apiPrefix+"/templates")
				if err == nil {
					for _, tp := range tpls {
						if tp.Name == req.Template {
							exports = tp.Exports
						}
					}
				}
			}
			return g.io.Print(svc, func() error {
				if template == "" {
					g.io.Successf("Created service %s in %s/%s", svc.Name, svc.Project, svc.Environment)
					return nil
				}
				g.io.Successf("Created %s from the %s template in %s/%s; its first release is deploying", svc.Name, template, svc.Project, svc.Environment)
				for _, e := range exports {
					g.io.Detail(fmt.Sprintf("Other services use it with ${{%s.%s}}, e.g. `dock env set -s api %s='${{%s.%s}}'`", svc.Name, e, e, svc.Name, e))
				}
				created := target{svc.Project, svc.Environment, svc.Name}
				g.io.Detail(fmt.Sprintf("Follow the deploy with `dock release wait %s`; backups run daily (`dock backup list %s`).", created.flags(), created.flags()))
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	settings.add(cmd.Flags())
	cmd.Flags().StringVar(&template, "template", "", "create a database from a template: postgres or redis (other settings are ignored)")
	return cmd
}

// serviceArg lets commands take the service as an optional argument as
// well as --service.
func serviceArg(f *targetFlags, args []string) {
	if len(args) == 1 {
		f.service = args[0]
	}
}

func newServiceShowCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:               "show [name]",
		Short:             "Show a service's settings and state",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			svc, err := get[apitypes.Service](cmd.Context(), s, t.servicePath())
			if err != nil {
				return err
			}
			return g.io.Print(svc, func() error { return printService(g, svc) })
		},
	}
	f.add(cmd, needService)
	return cmd
}

// preDeployNote is a service's pre-deploy command with its timeout, if
// it has one.
func preDeployNote(s apitypes.ServiceSettings) string {
	if s.PreDeployCommand == "" {
		return ""
	}
	if s.PreDeployTimeoutSeconds == 0 {
		return s.PreDeployCommand
	}
	return fmt.Sprintf("%s (%s timeout)", s.PreDeployCommand, time.Duration(s.PreDeployTimeoutSeconds)*time.Second)
}

func printService(g *globals, svc apitypes.Service) error {
	state := cmp.Or(stoppedNote(svc), serverNote(svc), runState(svc.Status))
	if svc.ArchivedAt != nil {
		state = "archived " + ago(*svc.ArchivedAt)
	}
	state += restartsNote(svc.Status)
	state += oomNote(svc.Status)
	kind := ""
	if svc.Stateful {
		kind = "stateful (has volumes; deploys replace the container)"
	}
	port := "8080 (default)"
	if svc.Port != 0 {
		port = strconv.Itoa(svc.Port)
	}
	health := "TCP"
	if svc.HealthCheckPath != "" {
		health = "HTTP " + svc.HealthCheckPath
	}
	if svc.HealthCheckTimeoutSeconds != 0 {
		health += fmt.Sprintf(", %ds timeout", svc.HealthCheckTimeoutSeconds)
	}
	limits := ""
	if svc.MemoryMB != 0 {
		limits = fmt.Sprintf("%d MB", svc.MemoryMB)
	}
	if svc.CPUs != 0 {
		limits += fmt.Sprintf(" %g CPU", svc.CPUs)
	}
	pending := ""
	if svc.PendingChanges > 0 {
		pending = plural(svc.PendingChanges, "staged change") + " (see `dock changes`)"
	}
	return g.io.Fields(
		"service", svc.Project+"/"+svc.Environment+"/"+svc.Name,
		"state", state,
		"kind", kind,
		"release", releaseNumber(svc.ActiveRelease),
		"image", svc.Image,
		"port", port,
		"health check", health,
		"ongoing check", livenessNote(svc.ServiceSettings),
		"start command", svc.StartCommand,
		"dockerfile", svc.DockerfilePath,
		"build command", svc.BuildCommand,
		"pre-deploy", preDeployNote(svc.ServiceSettings),
		"limits", limits,
		"pending", pending,
	)
}

// livenessNote describes a service's ongoing health check settings, with
// the defaults the API's zeros stand for (ADR 0043).
func livenessNote(s apitypes.ServiceSettings) string {
	if s.HealthCheckIntervalSeconds == -1 {
		return "off"
	}
	start := "start period = health check timeout"
	if s.HealthCheckStartPeriodSeconds != 0 {
		start = fmt.Sprintf("%ds start period", s.HealthCheckStartPeriodSeconds)
	}
	return fmt.Sprintf("every %ds, %ds probe timeout, %d retries, %s",
		cmp.Or(s.HealthCheckIntervalSeconds, 30), cmp.Or(s.HealthCheckProbeTimeoutSeconds, 5), cmp.Or(s.HealthCheckRetries, 3), start)
}

func newServiceUpdateCmd(g *globals) *cobra.Command {
	var f targetFlags
	var settings settingsFlags
	var deploy, detach bool
	cmd := &cobra.Command{
		Use:   "update [name] --<setting> <value>...",
		Short: "Stage settings changes",
		Long: `Stage changes to a service's settings. Only the flags you pass change; pass
an empty value to clear a setting. Staged changes go live with the service's
next deploy, ` + "`dock apply`" + `, or right away with --deploy.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			req, changed := settings.update(cmd.Flags())
			if len(changed) == 0 {
				return usageError("nothing to change", "Pass at least one setting, e.g. --port 3000. See `dock service update --help`.")
			}
			req.Apply = deploy
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			o := stageOpts{what: "Changed " + strings.Join(changed, ", ") + " of " + t.Service, deploy: deploy, detach: detach}
			return g.stage(cmd.Context(), s, t, o, func() (apitypes.ChangeResult, error) {
				return send[apitypes.ChangeResult](cmd.Context(), s, http.MethodPatch, t.servicePath(), req)
			})
		},
	}
	f.add(cmd, needService)
	settings.add(cmd.Flags())
	addDeployFlags(cmd, &deploy, &detach)
	return cmd
}

// newServiceStopCmd is `dock service stop`: the service's container is
// removed and its domains answer 503, keeping everything else (ADR 0050).
func newServiceStopCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "stop [name]",
		Short: "Stop a service, keeping its release, volumes, domains and variables",
		Long: `Stop a service: its domains answer 503 and its container is removed, freeing
its memory. Its active release, port, volumes, domains and variables are kept.
Start it again with ` + "`dock service start`" + ` or any deploy.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Stop %s? Its container is removed and its domains answer 503 until it starts again.", t), "yes"); err != nil {
					return err
				}
			}
			svc, err := send[apitypes.Service](cmd.Context(), s, http.MethodPost, t.servicePath()+"/stop", nil)
			if err != nil {
				return deployInProgress(err, t)
			}
			return g.io.Print(svc, func() error {
				g.io.Successf("Stopped %s. Its domains answer 503; start it with `dock service start %s`.", t, t.flags())
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// newServiceStartCmd is `dock service start`: a restart-source release of
// a stopped service, followed like `dock restart`.
func newServiceStartCmd(g *globals) *cobra.Command {
	var f targetFlags
	var detach bool
	cmd := &cobra.Command{
		Use:   "start [name]",
		Short: "Start a stopped service with a copy of its active release",
		Long: `Start a stopped service: a new release copying its active one, through the
same health-checked path as a deploy. If that release fails, the service
stays stopped.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			return g.startAndFollow(ctx, s, t, detach, func() (apitypes.Release, error) {
				r, err := send[apitypes.Release](ctx, s, http.MethodPost, t.servicePath()+"/start", nil)
				if err == nil {
					g.io.Progressf("Starting %s: release %d copies release %d, %s", t, r.Number, r.FromRelease, describeCopy(r))
				}
				return r, err
			})
		},
	}
	f.add(cmd, needService)
	addDetachFlag(cmd, &detach)
	return cmd
}

func newServiceArchiveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:               "archive [name]",
		Short:             "Stop a service and remove its routes, keeping volumes and history",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServiceArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceArg(&f, args)
			s, t, err := g.open(f, needService)
			if err != nil {
				return err
			}
			if !yes {
				if err := g.io.Confirm(fmt.Sprintf("Archive %s? Its container stops and its domains stop routing.", t), "yes"); err != nil {
					return err
				}
			}
			svc, err := send[apitypes.Service](cmd.Context(), s, http.MethodPost, t.servicePath()+"/archive", nil)
			if err != nil {
				return err
			}
			return g.io.Print(svc, func() error {
				g.io.Successf("Archived %s. Its container is stopping; volumes and release history are kept.", t)
				for _, n := range svc.ArchiveNotes {
					g.io.Warnf("%s", n)
				}
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
