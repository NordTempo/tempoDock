package mcp

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
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
	Image                     *string  `json:"image,omitempty" doc:"Image the service runs and image deploys default to, e.g. ghcr.io/acme/web:3f2a1c. Empty: built from source with dock deploy."`
	Port                      *int     `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Port the app listens on in the container; traffic and the health check go there. 0 means 8080. Dock also sets PORT to it."`
	HealthCheckPath           *string  `json:"health_check_path,omitempty" doc:"HTTP path that must answer 2xx or 3xx before a release gets traffic, e.g. /healthz. Empty: a TCP check on the port."`
	HealthCheckTimeoutSeconds *int     `json:"health_check_timeout_seconds,omitempty" minimum:"0" doc:"How long the health check may take to pass. 0: the server default (60)."`
	StartCommand              *string  `json:"start_command,omitempty" doc:"Command run with sh -c instead of the image's own. Empty: the image's."`
	DockerfilePath            *string  `json:"dockerfile_path,omitempty" doc:"Dockerfile for source deploys, relative to the repository root."`
	BuildCommand              *string  `json:"build_command,omitempty" doc:"Build command for source deploys without a Dockerfile."`
	MemoryMB                  *int     `json:"memory_mb,omitempty" minimum:"0" doc:"Hard memory limit in MB; an app over it is killed (OOM). 0: the server default (512)."`
	CPUs                      *float64 `json:"cpus,omitempty" minimum:"0" doc:"CPU limit in cores, e.g. 0.5. 0: the server default (1)."`
}

// addServiceTools registers the service, volume, domain removal, stats
// and alert tools. Stopping and starting a service are with the release
// tools (tools.go), snapshots in snapshots.go.
func (h *handler) addServiceTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "create_service", "Create a service",
		"Create a service in an environment. Its settings are live at once and nothing is deployed: set its variables, then deploy_image (or `dock deploy` for source code). "+
			"With template (postgres or redis) it is a database with generated credentials, a volume and its own settings, and it deploys right away. Needs deploy.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			EnvInput
			Name     string            `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"Service name, DNS-safe, e.g. web. Also its hostname on the environment's private network."`
			Template apitypes.Template `json:"template,omitempty" enum:"postgres,redis" doc:"Create a database from a template; the settings below are then ignored."`
			SettingsInput
		}) (mcpkit.Reply, error) {
			ref, err := envOf(c, in.EnvInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			set := in.SettingsInput
			svc, err := h.core.CreateService(ctx, c.Actor, ref, apitypes.CreateServiceRequest{
				Name: in.Name, Template: in.Template, Image: deref(set.Image), Port: deref(set.Port),
				HealthCheckPath: deref(set.HealthCheckPath), HealthCheckTimeoutSeconds: deref(set.HealthCheckTimeoutSeconds),
				StartCommand: deref(set.StartCommand), DockerfilePath: deref(set.DockerfilePath), BuildCommand: deref(set.BuildCommand),
				MemoryMB: deref(set.MemoryMB), CPUs: deref(set.CPUs),
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			summary := fmt.Sprintf("Created %s/%s/%s.", ref.Project, ref.Environment, svc.Name)
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
		"Stage changes to a service's settings: port, health check path and timeout, image, start command, memory and CPU limits, build settings. Only the ones passed change. "+
			"This is how to fix a deploy whose health check failed on a wrong port or path. Nothing goes live until apply_changes or the service's next deploy; review with show_changes. "+
			"A dock.toml in deployed source overrides these settings for source deploys.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			SettingsInput
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
				MemoryMB: set.MemoryMB, CPUs: set.CPUs,
			})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Staged settings of %s. %d changes staged in %s/%s; review with show_changes, then apply_changes takes them live.",
				ref, res.Pending.Count, ref.Project, ref.Environment), Data: res.Pending}, nil
		})

	mcpkit.AddTool(s, "list_volumes", "List volumes",
		"A service's volumes: name, mount path and whether it is still staged (mounted once applied). Snapshots of them: list_snapshots.",
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
		"Stage a persistent volume for a service, mounted at an absolute path. Once applied the service is stateful: it keeps its data across releases and deploys with a short downtime (stop, then start). "+
			"Nothing goes live until apply_changes or the service's next deploy; review with show_changes.",
		mcpkit.Mutating, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Name      string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"Volume name, e.g. data."`
			MountPath string `json:"mount_path" pattern:"^/" doc:"Absolute path in the container, e.g. /var/lib/app."`
		}) (mcpkit.Reply, error) {
			ref, err := serviceOf(c, in.ServiceInput)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			res, err := h.core.AddVolume(ctx, c.Actor, ref, apitypes.AddVolumeRequest{Name: in.Name, MountPath: in.MountPath})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			return mcpkit.Reply{Summary: fmt.Sprintf("Staged volume %s at %s for %s. %d changes staged in %s/%s; review with show_changes, then apply_changes takes them live.",
				in.Name, in.MountPath, ref, res.Pending.Count, ref.Project, ref.Environment), Data: res.Pending}, nil
		})

	mcpkit.AddTool(s, "remove_domain", "Remove a domain",
		"Remove a hostname from a service: its route goes at once (the hostname stops serving) and so does the DNS record Dock made for it, if any. Only when asked.",
		mcpkit.Destructive, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Hostname string `json:"hostname" doc:"The hostname to remove, e.g. shop.example.com (list_domains shows them)."`
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
		"A service's CPU, memory and disk I/O: the latest sample of its active container, and averages and peaks over a range, across releases, with the range in at most 24 points. "+
			"For a slow or crashing (OOM) service.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			ServiceInput
			Range apitypes.StatsRange `json:"range,omitempty" enum:"1h,24h,7d" doc:"How far back. Default 1h."`
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
		"The platform's alerts: full disks, offline servers, crash loops, failed deploys and backups, domain errors, analytics anomalies. Open ones by default, critical first; all includes resolved ones, newest first. "+
			"Platform operators only: other tokens are refused.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in struct {
			All    bool  `json:"all,omitempty" doc:"Also resolved alerts, newest first."`
			Limit  int   `json:"limit,omitempty" doc:"With all: how many. Default 50, at most 500."`
			Before int64 `json:"before,omitempty" doc:"With all: only alerts with a smaller id, to page back."`
		}) (mcpkit.Reply, error) {
			n, err := mcpkit.Limit("limit", in.Limit, 50, 500)
			if err != nil {
				return mcpkit.Reply{}, err
			}
			as, err := h.core.ListAlerts(ctx, c.Actor, in.All, n, in.Before)
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
