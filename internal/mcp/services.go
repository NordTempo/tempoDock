package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Service tools: create a service, fix its settings (the usual cause of a
// failed health check is a wrong port or path), add volumes and remove
// domains, read its resource use and the platform's alerts. Settings and
// volumes are staged like variables; nothing here deploys, except a
// database template.

// SettingsInput are the service settings a tool may change. Only the ones
// passed change.
type SettingsInput struct {
	Image                     *string  `json:"image,omitempty"`
	Port                      *int     `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Container port. Default 8080. Sets PORT."`
	HealthCheckPath           *string  `json:"health_check_path,omitempty" doc:"e.g. /healthz. Empty: a TCP check."`
	HealthCheckTimeoutSeconds *int     `json:"health_check_timeout_seconds,omitempty" minimum:"0" doc:"Default 60."`
	StartCommand              *string  `json:"start_command,omitempty"`
	DockerfilePath            *string  `json:"dockerfile_path,omitempty"`
	BuildCommand              *string  `json:"build_command,omitempty"`
	MemoryMB                  *int     `json:"memory_mb,omitempty" minimum:"0" doc:"Default 512."`
	CPUs                      *float64 `json:"cpus,omitempty" minimum:"0" doc:"Cores. Default 1."`
	SecurityHeaders           *bool    `json:"security_headers,omitempty" doc:"HSTS, nosniff, referrer policy, SAMEORIGIN framing on responses lacking them. Default on; false if another site frames the app."`
}

// addServiceTools registers the service, volume, domain removal, stats
// and alert tools. Stopping and starting a service are with the release
// tools (tools.go), snapshots in snapshots.go.
func (h *handler) addServiceTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "create_service", "Create a service",
		"Create a service; deploy deploys its image at once and waits, otherwise nothing deploys until deploy_image. With template, a database (postgres, redis) that deploys at once. With repo, it builds that GitHub repository's dir through the org's GitHub App and deploys the branch's head at once.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			Name     string            `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
			Template apitypes.Template `json:"template,omitempty" enum:"postgres,redis" doc:"A database; other settings are ignored."`
			Deploy   bool              `json:"deploy,omitempty"`
			Repo     string            `json:"repo,omitempty" doc:"owner/name on GitHub."`
			Branch   string            `json:"branch,omitempty" doc:"With repo; its default branch if omitted."`
			Dir      string            `json:"dir,omitempty" doc:"With repo: the subdirectory to build, e.g. apps/web."`
			SettingsInput
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			set := in.SettingsInput
			req := apitypes.CreateServiceRequest{
				Name: in.Name, Template: in.Template, Image: deref(set.Image), Port: deref(set.Port),
				HealthCheckPath: deref(set.HealthCheckPath), HealthCheckTimeoutSeconds: deref(set.HealthCheckTimeoutSeconds),
				StartCommand: deref(set.StartCommand), DockerfilePath: deref(set.DockerfilePath), BuildCommand: deref(set.BuildCommand),
				MemoryMB: deref(set.MemoryMB), CPUs: deref(set.CPUs), SecurityHeaders: set.SecurityHeaders,
			}
			if in.Repo != "" {
				req.Git = &apitypes.ServiceGitRequest{Repo: in.Repo, Branch: in.Branch, Dir: in.Dir}
			} else if in.Branch != "" || in.Dir != "" {
				return mcpkit.Reply{}, mcpkit.Invalid("branch and dir need repo (owner/name)")
			}
			svc, err := h.core.CreateService(ctx, c.Actor, ref, req)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := fmt.Sprintf("Created %s/%s/%s.", ref.Project, ref.Environment, svc.Name)
			if req.Git != nil {
				return mcpkit.Reply{Summary: summary + " Its first release builds the branch's head from " + in.Repo + ": follow it with list_releases; every push deploys again.", Data: svc}, nil
			}
			if in.Deploy && svc.Template == apitypes.TemplateNone && svc.Image != "" {
				sref := core.ServiceRef{Project: ref.Project, Environment: ref.Environment, Service: svc.Name}
				// The service exists either way: a deploy that didn't start
				// is said in a reply, not as the call's error, so the model
				// doesn't create it again.
				rel, err := h.core.DeployImage(ctx, c.Actor, sref, apitypes.DeployImageRequest{})
				if err == nil {
					var r mcpkit.Reply
					if r, err = h.waitOne(ctx, c, "Deploy", rel, defaultWait); err == nil {
						r.Summary = summary + " " + r.Summary
						return r, nil
					}
				}
				return mcpkit.Reply{Summary: fmt.Sprintf("%s Its deploy didn't go through: %s. Fix that, then deploy_image; don't create it again.", summary, errorText(err)), Data: svc}, nil
			}
			switch {
			case svc.Template != apitypes.TemplateNone:
				summary += " It is deploying from the " + string(svc.Template) + " template: call wait_for_release."
			case svc.Image != "":
				summary += " Nothing is deployed yet: set its variables, then deploy_image."
			default:
				summary += " Nothing is deployed yet: set its variables, then run `dock deploy` in the repository."
			}
			return mcpkit.Reply{Summary: summary, Data: svc}, nil
		})

	mcpkit.AddTool(s, "update_service", "Stage service settings",
		"Stage service settings, e.g. port or health check path to fix a failed health check. Only those passed change.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			SettingsInput
			ApplyInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			set := in.SettingsInput
			if set == (SettingsInput{}) {
				return mcpkit.Reply{}, mcpkit.Invalid("pass at least one setting to change, e.g. port or health_check_path")
			}
			res, err := h.core.UpdateService(ctx, c.Actor, ref, apitypes.UpdateServiceRequest{
				Image: set.Image, Port: set.Port, HealthCheckPath: set.HealthCheckPath, HealthCheckTimeoutSeconds: set.HealthCheckTimeoutSeconds,
				StartCommand: set.StartCommand, DockerfilePath: set.DockerfilePath, BuildCommand: set.BuildCommand,
				MemoryMB: set.MemoryMB, CPUs: set.CPUs, SecurityHeaders: set.SecurityHeaders, Apply: in.Apply,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.stagedReply(ctx, c, "settings of "+ref.String(), ref.Env(), res)
		})

	mcpkit.AddTool(s, "list_volumes", "List volumes",
		"A service's volumes.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in ServiceInput) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			vs, err := h.core.ListVolumes(ctx, c.Actor, ref)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			lines := make([]string, len(vs))
			for i, v := range vs {
				lines[i] = v.Name + " at " + v.MountPath
				if v.Pending {
					lines[i] += " (staged)"
				}
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d volumes of %s: %s.", len(vs), ref, list(lines)), Data: vs}, nil
		})

	mcpkit.AddTool(s, "add_volume", "Stage a volume",
		"Stage a persistent volume. The service then keeps data across deploys, with a short downtime per deploy. Nothing deletes a volume.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Name      string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
			MountPath string `json:"mount_path" pattern:"^/"`
			ApplyInput
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.AddVolume(ctx, c.Actor, ref, apitypes.AddVolumeRequest{Name: in.Name, MountPath: in.MountPath, Apply: in.Apply})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return h.stagedReply(ctx, c, fmt.Sprintf("volume %s at %s for %s", in.Name, in.MountPath, ref), ref.Env(), res)
		})

	mcpkit.AddTool(s, "remove_domain", "Remove a domain",
		"Remove a hostname from a service; it stops serving at once. Only when asked.",
		mcpkit.Destructive, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname string `json:"hostname"`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			if err := h.core.RemoveDomain(ctx, c.Actor, ref, in.Hostname); err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Removed %s from %s.", in.Hostname, ref)}, nil
		})

	mcpkit.AddTool(s, "service_stats", "Service resource use",
		"A service's CPU, memory and disk I/O, now and over a range.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Range apitypes.StatsRange `json:"range,omitempty" enum:"1h,24h,7d" doc:"Default 1h."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			st, err := h.core.ServiceStats(ctx, c.Actor, ref, in.Range)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			v := viewStats(st)
			summary := fmt.Sprintf("%s over %s: CPU avg %.1f%%, peak %.1f%%; memory avg %d MB, peak %d MB", ref, v.Range, v.CPUAvg, v.CPUPeak, v.MemoryAvgMB, v.MemoryPeakMB)
			if len(st.Points) == 0 {
				summary = fmt.Sprintf("%s over %s: no samples (nothing ran)", ref, v.Range)
			}
			if u := st.Current; u != nil {
				summary += fmt.Sprintf("; now CPU %.1f%%, memory %d MB", u.CPUPercent, u.MemoryBytes>>20)
				if u.MemoryLimitBytes > 0 {
					summary += fmt.Sprintf(" of %d MB", u.MemoryLimitBytes>>20)
				}
			}
			return mcpkit.Reply{Summary: summary + ".", Data: v}, nil
		})

	mcpkit.AddTool(s, "list_alerts", "List alerts",
		"Open alerts: crash loops, failed deploys, unhealthy services, backups, uptime, access requests. platform: the platform's (operators).",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			Platform bool  `json:"platform,omitempty"`
			All      bool  `json:"all,omitempty" doc:"Also resolved ones."`
			Limit    int   `json:"limit,omitempty"`
			Before   int64 `json:"before,omitempty"`
		}) (mcpkit.Reply, error) {
			n, err := mcpkit.Limit("limit", in.Limit, 50, 500)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			as, err := h.core.ListAlerts(ctx, c.Actor, in.Platform, in.All, n, in.Before)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			titles := make([]string, len(as))
			for i, a := range as {
				titles[i] = fmt.Sprintf("%s (%s)", a.Title, a.Severity)
			}
			which := "open"
			if in.All {
				which = "recent"
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("%d %s alerts: %s.", len(as), which, list(titles)), Data: as}, nil
		})
}

// deref returns *p, or T's zero value for nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// statsView is a service's resource use for a model: aggregates over the
// range and at most maxStatsPoints averaged points, not the minute-by-
// minute history (1440 points for 24h).
type statsView struct {
	Range        apitypes.StatsRange    `json:"range"`
	Current      *apitypes.ServiceUsage `json:"current,omitempty"`
	CPUAvg       float64                `json:"cpu_avg_percent"`
	CPUPeak      float64                `json:"cpu_peak_percent"`
	MemoryAvgMB  int64                  `json:"memory_avg_mb"`
	MemoryPeakMB int64                  `json:"memory_peak_mb"`
	// PointSeconds is how long each point averages.
	PointSeconds int          `json:"point_seconds,omitempty"`
	Points       []statsPoint `json:"points"`
}

// statsPoint is the average of consecutive samples, from Time on.
type statsPoint struct {
	Time        time.Time `json:"time"`
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryMB    int64     `json:"memory_mb"`
	IOReadKBps  int64     `json:"io_read_kb_per_sec"`
	IOWriteKBps int64     `json:"io_write_kb_per_sec"`
}

// maxStatsPoints caps a statsView's points.
const maxStatsPoints = 24

// viewStats aggregates a service's statistics into a statsView.
func viewStats(st apitypes.ServiceStats) statsView {
	v := statsView{Range: st.Range, Current: st.Current, Points: []statsPoint{}}
	n := len(st.Points)
	if n == 0 {
		return v
	}
	per := (n + maxStatsPoints - 1) / maxStatsPoints
	v.PointSeconds = st.ResolutionSeconds * per
	var cpu float64
	var mem int64
	for chunk := range slices.Chunk(st.Points, per) {
		var sum apitypes.ServiceStatsPoint
		for _, p := range chunk {
			sum.CPUPercent += p.CPUPercent
			sum.MemoryBytes += p.MemoryBytes
			sum.IOReadBytesPerSec += p.IOReadBytesPerSec
			sum.IOWriteBytesPerSec += p.IOWriteBytesPerSec
			v.CPUPeak = max(v.CPUPeak, p.CPUPercent)
			v.MemoryPeakMB = max(v.MemoryPeakMB, p.MemoryBytes>>20)
		}
		cpu, mem = cpu+sum.CPUPercent, mem+sum.MemoryBytes
		k := int64(len(chunk))
		v.Points = append(v.Points, statsPoint{
			Time: chunk[0].Time, CPUPercent: round1(sum.CPUPercent / float64(k)), MemoryMB: sum.MemoryBytes / k >> 20,
			IOReadKBps: sum.IOReadBytesPerSec / k >> 10, IOWriteKBps: sum.IOWriteBytesPerSec / k >> 10,
		})
	}
	v.CPUAvg, v.CPUPeak = round1(cpu/float64(n)), round1(v.CPUPeak)
	v.MemoryAvgMB = mem / int64(n) >> 20
	return v
}

// round1 rounds to one decimal, for percentages in summaries and views.
func round1(f float64) float64 { return math.Round(f*10) / 10 }

// errorText is what an error tells the model in a reply rather than as
// the call's error: a Dock error's code and message, anything else as
// fail renders it.
func errorText(err error) string {
	if de, ok := errors.AsType[*datapanel.Error](err); ok {
		return string(de.Code) + ": " + de.Msg
	}
	return "internal error; see the daemon log"
}
