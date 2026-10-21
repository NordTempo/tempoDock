package cli

import (
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
					state := string(svc.Status.State)
					if svc.ArchivedAt != nil {
						state = "archived"
					}
					tb.Row(svc.Name, state, releaseNumber(svc.ActiveRelease), dash(svc.Image), strconv.Itoa(svc.PendingChanges))
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
	port, memoryMB                                                 int
	healthCheckTimeout                                             time.Duration
	cpus                                                           float64
}

func (f *settingsFlags) add(fs *pflag.FlagSet) {
	fs.StringVar(&f.image, "image", "", "default image for image deploys, e.g. postgres:17")
	fs.IntVar(&f.port, "port", 0, "container port that receives traffic (0: 8080)")
	fs.StringVar(&f.healthCheckPath, "health-check-path", "", "HTTP path that must answer 2xx/3xx (empty: TCP check)")
	fs.DurationVar(&f.healthCheckTimeout, "health-check-timeout", 0, "how long a new release may take to pass, e.g. 60s")
	fs.StringVar(&f.startCommand, "start-command", "", "command that overrides the image's")
	fs.StringVar(&f.dockerfile, "dockerfile", "", "Dockerfile path relative to the source root")
	fs.StringVar(&f.buildCommand, "build-command", "", "Railpack build command (builds without a Dockerfile)")
	fs.IntVar(&f.memoryMB, "memory", 0, "memory limit in MB")
	fs.Float64Var(&f.cpus, "cpus", 0, "CPU limit, e.g. 0.5")
}

func (f *settingsFlags) create(name string) apitypes.CreateServiceRequest {
	return apitypes.CreateServiceRequest{
		Name: name, Image: f.image, Port: f.port, HealthCheckPath: f.healthCheckPath,
		HealthCheckTimeoutSeconds: int(f.healthCheckTimeout.Seconds()), StartCommand: f.startCommand,
		DockerfilePath: f.dockerfile, BuildCommand: f.buildCommand, MemoryMB: f.memoryMB, CPUs: f.cpus,
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
	timeout := int(f.healthCheckTimeout.Seconds())
	set("image", func() { req.Image = &f.image })
	set("port", func() { req.Port = &f.port })
	set("health-check-path", func() { req.HealthCheckPath = &f.healthCheckPath })
	set("health-check-timeout", func() { req.HealthCheckTimeoutSeconds = &timeout })
	set("start-command", func() { req.StartCommand = &f.startCommand })
	set("dockerfile", func() { req.DockerfilePath = &f.dockerfile })
	set("build-command", func() { req.BuildCommand = &f.buildCommand })
	set("memory", func() { req.MemoryMB = &f.memoryMB })
	set("cpus", func() { req.CPUs = &f.cpus })
	return req, changed
}

func newServiceCreateCmd(g *globals) *cobra.Command {
	var f targetFlags
	var settings settingsFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a service (without deploying it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, t, err := g.open(f, needEnvironment)
			if err != nil {
				return err
			}
			svc, err := send[apitypes.Service](cmd.Context(), s, http.MethodPost, t.envPath()+"/services", settings.create(args[0]))
			if err != nil {
				return err
			}
			return g.io.Print(svc, func() error {
				g.io.Successf("Created service %s in %s/%s", svc.Name, svc.Project, svc.Environment)
				return nil
			})
		},
	}
	f.add(cmd, needEnvironment)
	settings.add(cmd.Flags())
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
		Use:   "show [name]",
		Short: "Show a service's settings and state",
		Args:  cobra.MaximumNArgs(1),
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

func printService(g *globals, svc apitypes.Service) error {
	state := string(svc.Status.State)
	if svc.ArchivedAt != nil {
		state = "archived " + ago(*svc.ArchivedAt)
	}
	if svc.Status.RestartCount > 0 {
		state += fmt.Sprintf(" (%d restarts)", svc.Status.RestartCount)
	}
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
		"start command", svc.StartCommand,
		"dockerfile", svc.DockerfilePath,
		"build command", svc.BuildCommand,
		"limits", limits,
		"pending", pending,
	)
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
		Args: cobra.MaximumNArgs(1),
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

func newServiceArchiveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "archive [name]",
		Short: "Stop a service and remove its routes, keeping volumes and history",
		Args:  cobra.MaximumNArgs(1),
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
				g.io.Successf("Archived %s. Volumes and release history are kept.", t)
				return nil
			})
		},
	}
	f.add(cmd, needService)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
