package mcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Compact views of API types. Tool results go into a model's context, so
// they leave out what an agent doesn't act on (IDs, image IDs, resolved
// config) and keep what it does (status, errors, names).

// releaseView is a release as tools return it.
type releaseView struct {
	Service     string                 `json:"service"`
	Number      int64                  `json:"number"`
	Status      apitypes.ReleaseStatus `json:"status"`
	Source      apitypes.ReleaseSource `json:"source"`
	FromRelease int64                  `json:"from_release,omitempty"`
	Image       string                 `json:"image,omitempty"`
	Error       string                 `json:"error,omitempty"`
	ErrorCode   apitypes.ErrorCode     `json:"error_code,omitempty"`
	CreatedBy   string                 `json:"created_by"`
	CreatedAt   time.Time              `json:"created_at"`
	FinishedAt  *time.Time             `json:"finished_at,omitempty"`
	// Events are the deploy's progress lines (release_details and waits).
	Events []string `json:"events,omitempty"`
	// Logs explain a failed release (release_details and waits).
	Logs *failureLogs `json:"logs,omitempty"`
	// Diagnosis names a failed release's likely cause; the diagnose tool
	// has its evidence (ADR 0070).
	Diagnosis *apitypes.DiagnosisRef `json:"diagnosis,omitempty"`
}

// failureLogs are the last lines that explain a failed release.
type failureLogs struct {
	// Container is what the release's container wrote before it was
	// stopped (a crash or a failed health check).
	Container []string `json:"container,omitempty"`
	// Build is the end of the build or image pull output.
	Build []string `json:"build,omitempty"`
}

func viewRelease(r apitypes.Release) releaseView {
	v := releaseView{
		Service: r.Service, Number: r.Number, Status: r.Status, Source: r.Source, FromRelease: r.FromRelease,
		Image: r.ImageRef, Error: r.Error, ErrorCode: r.ErrorCode, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		FinishedAt: r.FinishedAt, Diagnosis: r.Diagnosis,
	}
	for _, e := range r.Events {
		v.Events = append(v.Events, e.Message)
	}
	return v
}

func viewReleases(rs []apitypes.Release) []releaseView {
	out := make([]releaseView, len(rs))
	for i, r := range rs {
		out[i] = viewRelease(r)
	}
	return out
}

// releaseSummary is a release's short form inside service views.
type releaseSummary struct {
	Number    int64                  `json:"number"`
	Status    apitypes.ReleaseStatus `json:"status"`
	Image     string                 `json:"image,omitempty"`
	Error     string                 `json:"error,omitempty"`
	ErrorCode apitypes.ErrorCode     `json:"error_code,omitempty"`
	CreatedBy string                 `json:"created_by"`
	CreatedAt time.Time              `json:"created_at"`
}

func summarize(r *apitypes.ReleaseSummary) *releaseSummary {
	if r == nil {
		return nil
	}
	return &releaseSummary{Number: r.Number, Status: r.Status, Image: r.ImageRef, Error: r.Error, ErrorCode: r.ErrorCode, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
}

// overviewView is a project with its environments and services.
type overviewView struct {
	Project      string    `json:"project"`
	Environments []envView `json:"environments"`
}

type envView struct {
	Name string                   `json:"name"`
	Kind apitypes.EnvironmentKind `json:"kind"`
	// Server is set for platform operators only; ServerStatus says
	// whether deploys can reach it, for everyone.
	Server         string                `json:"server,omitempty"`
	ServerStatus   apitypes.ServerStatus `json:"server_status"`
	PendingChanges int                   `json:"pending_changes"`
	// Locked: tokens can't change anything here; what you try becomes an
	// access request a human approves (ADR 0044).
	Locked   bool          `json:"locked,omitempty"`
	Services []serviceView `json:"services"`
}

// serviceView is a service's live state.
type serviceView struct {
	Name     string                `json:"name"`
	Image    string                `json:"image,omitempty"`
	State    apitypes.ServiceState `json:"state"`
	Restarts int                   `json:"restarts,omitempty"`
	// Health is the ongoing health check's state while it runs (ADR
	// 0043): unhealthy means the process runs but doesn't answer.
	Health apitypes.HealthState `json:"health,omitempty"`
	// HealthRestarts are the control plane's restarts for a failing
	// health check; Restarts doesn't include them.
	HealthRestarts int `json:"health_restarts,omitempty"`
	// OOMKilled: the container hit its memory limit (MemoryMB).
	OOMKilled bool `json:"oom_killed,omitempty"`
	ExitCode  int  `json:"exit_code,omitempty"`
	MemoryMB  int  `json:"memory_mb"`
	// Stateful services have volumes and deploy with downtime (stop, then
	// start).
	Stateful       bool                `json:"stateful,omitempty"`
	Database       apitypes.DataEngine `json:"database,omitempty"`
	PendingChanges int                 `json:"pending_changes,omitempty"`
	Active         *releaseSummary     `json:"active_release,omitempty"`
	Deploying      *releaseSummary     `json:"deploying,omitempty"`
	Latest         *releaseSummary     `json:"latest_release,omitempty"`
	Domains        []string            `json:"domains,omitempty"`
	Archived       bool                `json:"archived,omitempty"`
	// PreDeployCommand runs before each new release gets traffic.
	PreDeployCommand string `json:"pre_deploy_command,omitempty"`
	// StoppedAt is set while the service is stopped on purpose (ADR
	// 0050): no container runs and its domains answer 503 until
	// start_service or a deploy runs it again.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	StoppedBy string     `json:"stopped_by,omitempty"`
	// PreDeploySnapshot snapshots stateful services before that command.
	PreDeploySnapshot bool `json:"pre_deploy_snapshot,omitempty"`
}

func viewService(s apitypes.Service) serviceView {
	return serviceView{
		Name: s.Name, Image: s.Image, State: s.Status.State, Restarts: s.Status.RestartCount, OOMKilled: s.Status.OOMKilled,
		Health: s.Status.Health, HealthRestarts: s.Status.HealthRestarts,
		ExitCode: s.Status.ExitCode, MemoryMB: s.EffectiveMemoryMB, Stateful: s.Stateful, Database: s.DataEngine,
		PendingChanges: s.PendingChanges, Archived: s.ArchivedAt != nil, PreDeployCommand: s.PreDeployCommand,
		StoppedAt: s.StoppedAt, StoppedBy: s.StoppedBy,
		PreDeploySnapshot: s.PreDeploySnapshot,
	}
}

func viewOverview(o apitypes.ProjectOverview) overviewView {
	out := overviewView{Project: o.Project.Name, Environments: make([]envView, 0, len(o.Environments))}
	for _, e := range o.Environments {
		ev := envView{Name: e.Environment.Name, Kind: e.Environment.Kind, Server: e.Environment.Server, ServerStatus: e.Environment.ServerStatus, PendingChanges: e.PendingChanges, Locked: e.Environment.Lock.Holds(time.Now()), Services: []serviceView{}}
		for _, s := range e.Services {
			sv := viewService(s.Service)
			sv.Active, sv.Deploying = summarize(s.ActiveRelease), summarize(s.Deploying)
			if s.LatestRelease != nil && (s.ActiveRelease == nil || s.LatestRelease.Number != s.ActiveRelease.Number) {
				sv.Latest = summarize(s.LatestRelease)
			}
			for _, d := range s.Domains {
				sv.Domains = append(sv.Domains, domainLine(d))
			}
			ev.Services = append(ev.Services, sv)
		}
		out.Environments = append(out.Environments, ev)
	}
	return out
}

// domainLine is a domain as one line: its URL, and its status unless it
// is active.
func domainLine(d apitypes.Domain) string {
	if d.Status == apitypes.DomainActive {
		return d.URL
	}
	s := d.URL + " (" + string(d.Status)
	if d.StatusReason != "" {
		s += ": " + d.StatusReason
	}
	return s + ")"
}

// logLine renders a log line as "time text", with stderr marked.
func logLine(l apitypes.LogLine) string {
	var b strings.Builder
	b.WriteString(l.Time.UTC().Format(time.RFC3339))
	if l.Stream == "stderr" {
		b.WriteString(" [stderr]")
	}
	b.WriteByte(' ')
	b.WriteString(l.Text)
	return b.String()
}

// serverView is a server as agents need it: where things run and whether
// it is up.
type serverView struct {
	Name                 string                `json:"name"`
	Status               apitypes.ServerStatus `json:"status"`
	Local                bool                  `json:"local,omitempty"`
	Version              string                `json:"version,omitempty"`
	AcceptsProduction    bool                  `json:"accepts_production"`
	AcceptsNonProduction bool                  `json:"accepts_non_production"`
	BaseDomain           string                `json:"base_domain,omitempty"`
	Environments         int                   `json:"environments"`
	MemoryMB             int64                 `json:"memory_mb,omitempty"`
	ReservedMemoryMB     int64                 `json:"reserved_memory_mb,omitempty"`
}

func viewServer(s apitypes.Server) serverView {
	return serverView{
		Name: s.Name, Status: s.Status, Local: s.Local, Version: s.Version, AcceptsProduction: s.AcceptsProduction,
		AcceptsNonProduction: s.AcceptsNonProduction, BaseDomain: s.BaseDomain, Environments: s.Environments,
		MemoryMB: s.MemoryBytes >> 20, ReservedMemoryMB: s.ReservedMemoryBytes >> 20,
	}
}

// describeRelease is a release in a summary line, e.g. "release 12 of
// web: active" or "release 12 of web: failed (health_check_failed: ...)".
func describeRelease(r apitypes.Release) string {
	s := fmt.Sprintf("release %d of %s: %s", r.Number, r.Service, r.Status)
	if r.Status == apitypes.ReleaseFailed && r.Error != "" {
		s += " (" + r.Error + ")"
	}
	return s
}

// snapshotLine is a snapshot in a summary line, e.g. "12 (manual):
// ready, 48.0 MB, paused 180ms" or "13 (before_deploy): failed (not
// enough disk ...)".
func snapshotLine(s apitypes.Snapshot) string {
	line := fmt.Sprintf("%d (%s): %s", s.ID, s.Trigger, s.Status)
	switch s.Status {
	case apitypes.SnapshotReady:
		line += fmt.Sprintf(", %.1f MB, paused %s", float64(s.SizeBytes)/(1<<20), time.Duration(s.PausedMS)*time.Millisecond)
	case apitypes.SnapshotFailed:
		line += " (" + s.Error + ")"
	}
	return line
}
