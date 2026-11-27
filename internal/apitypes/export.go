package apitypes

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ExportVersion is the version of the export manifest's format.
const ExportVersion = 1

// ProjectExport is a project's configuration as a manifest (`dock project
// export`, ADR 0022): its environments, services and their non-secret
// settings, as they are live. Variables are listed by name only; values
// are never exported. Everything is sorted by name, and nothing in it
// changes unless the configuration does, so it diffs well in git.
type ProjectExport struct {
	Version int    `json:"version" toml:"version" doc:"Manifest format version"`
	Project string `json:"project" toml:"project"`
	// RegistryCredentials are the private registry hosts this project's
	// images pull from with an org credential (`dock registry login`), by
	// host only: the credential itself is never in a file (ADR 0049).
	RegistryCredentials []string            `json:"registry_credentials,omitempty" toml:"registry_credentials,omitempty"`
	Environments        []EnvironmentExport `json:"environments" toml:"environments"`
}

// EnvironmentExport is one environment of an export.
type EnvironmentExport struct {
	Name string          `json:"name" toml:"name"`
	Kind EnvironmentKind `json:"kind" toml:"kind"`
	// SharedVariables are the names of the environment's shared variables
	// whose values are set separately (secrets); a project file only
	// requires them to exist.
	SharedVariables []string `json:"shared_variables,omitempty" toml:"shared_variables,omitempty"`
	// SharedValues are shared variables with their value in the file:
	// literals written there on purpose, and references. The export writes
	// reference-only values here (ADR 0049), never a literal.
	SharedValues map[string]string `json:"shared_values,omitempty" toml:"shared_values,omitempty"`
	// LogDrains are where the environment's logs go, without the URL's
	// credentials, headers or token (`dock logs drain add`).
	LogDrains []LogDrainExport `json:"log_drains,omitempty" toml:"log_drains,omitempty,inline"`
	Services  []ServiceExport  `json:"services,omitempty" toml:"services,omitempty"`
}

// LogDrainExport is a log drain in an export: its name, kind and target
// (scheme and host), never its credentials.
type LogDrainExport struct {
	Name   string       `json:"name" toml:"name"`
	Kind   LogDrainKind `json:"kind" toml:"kind"`
	Target string       `json:"target" toml:"target"`
}

// ServiceExport is one service of an export: its settings (zero values
// left out), variables, volumes, domains and git connection. As a project
// file (`dock apply FILE`), a left-out setting means zero, or the
// template's value for a template service.
type ServiceExport struct {
	Name     string   `json:"name" toml:"name"`
	Template Template `json:"template,omitempty" toml:"template,omitempty"`
	// Stopped is set for a service stopped on purpose (`dock service stop`).
	Stopped                   bool    `json:"stopped,omitempty" toml:"stopped,omitempty"`
	Image                     string  `json:"image,omitempty" toml:"image,omitempty"`
	Port                      int     `json:"port,omitempty" toml:"port,omitempty"`
	HealthCheckPath           string  `json:"health_check_path,omitempty" toml:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds int     `json:"health_check_timeout_seconds,omitempty" toml:"health_check_timeout_seconds,omitempty"`
	StartCommand              string  `json:"start_command,omitempty" toml:"start_command,omitempty"`
	DockerfilePath            string  `json:"dockerfile_path,omitempty" toml:"dockerfile_path,omitempty"`
	BuildCommand              string  `json:"build_command,omitempty" toml:"build_command,omitempty"`
	MemoryMB                  int     `json:"memory_mb,omitempty" toml:"memory_mb,omitempty"`
	CPUs                      float64 `json:"cpus,omitempty" toml:"cpus,omitempty"`
	PreDeployCommand          string  `json:"pre_deploy_command,omitempty" toml:"pre_deploy_command,omitempty"`
	PreDeployTimeoutSeconds   int     `json:"pre_deploy_timeout_seconds,omitempty" toml:"pre_deploy_timeout_seconds,omitempty"`
	PreDeploySnapshot         bool    `json:"pre_deploy_snapshot,omitempty" toml:"pre_deploy_snapshot,omitempty"`
	// SecurityHeaders is only written when off. Left out, a new service
	// gets them and an existing one keeps its setting (ADR 0107).
	SecurityHeaders     *bool           `json:"security_headers,omitempty" toml:"security_headers,omitempty"`
	Database            DatabaseSetting `json:"database,omitempty" toml:"database,omitempty"`
	DatabaseURLVariable string          `json:"database_url_variable,omitempty" toml:"database_url_variable,omitempty"`
	// Variables are the names of the service's own variables whose values
	// are set separately (secrets); a project file only requires them.
	Variables []string `json:"variables,omitempty" toml:"variables,omitempty"`
	// Values are the service's variables with their value in the file
	// (literals and references), like SharedValues.
	Values map[string]string `json:"values,omitempty" toml:"values,omitempty"`
	// DependsOn names services in the same environment that start first
	// when an apply creates them (ADR 0049). An order hint for project
	// files: not stored, never exported.
	DependsOn []string       `json:"depends_on,omitempty" toml:"depends_on,omitempty"`
	Volumes   []VolumeExport `json:"volumes,omitempty" toml:"volumes,omitempty,inline"`
	Domains   []DomainExport `json:"domains,omitempty" toml:"domains,omitempty,inline"`
	Git       *GitExport     `json:"git,omitempty" toml:"git,omitempty,inline"`
	// Jobs are the service's scheduled jobs (ADR 0054), not those from
	// its dock.toml: a file's jobs are set at once when it's applied.
	Jobs []JobExport `json:"jobs,omitempty" toml:"jobs,omitempty"`

	// The ongoing health check (ADR 0043); an interval of -1 is off.
	HealthCheckIntervalSeconds     int `json:"health_check_interval_seconds,omitempty" toml:"health_check_interval_seconds,omitempty"`
	HealthCheckProbeTimeoutSeconds int `json:"health_check_probe_timeout_seconds,omitempty" toml:"health_check_probe_timeout_seconds,omitempty"`
	HealthCheckRetries             int `json:"health_check_retries,omitempty" toml:"health_check_retries,omitempty"`
	HealthCheckStartPeriodSeconds  int `json:"health_check_start_period_seconds,omitempty" toml:"health_check_start_period_seconds,omitempty"`
}

// VolumeExport is a volume of an exported service.
type VolumeExport struct {
	Name      string `json:"name" toml:"name"`
	MountPath string `json:"mount_path" toml:"mount_path"`
}

// DomainExport is a domain of an exported service.
type DomainExport struct {
	Hostname   string `json:"hostname" toml:"hostname"`
	RedirectTo string `json:"redirect_to,omitempty" toml:"redirect_to,omitempty"`
	Generated  bool   `json:"generated,omitempty" toml:"generated,omitempty"`
}

// JobExport is a scheduled job of an exported service, with the keys of
// SetJobRequest. Left-out keys take the same defaults.
type JobExport struct {
	Name           string     `json:"name" toml:"name"`
	Schedule       string     `json:"schedule" toml:"schedule"`
	Timezone       string     `json:"timezone,omitempty" toml:"timezone,omitempty"`
	Command        string     `json:"command" toml:"command"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty" toml:"timeout_seconds,omitempty"`
	Overlap        JobOverlap `json:"overlap,omitempty" toml:"overlap,omitempty" enum:"skip,allow"`
	// Concurrency is written only when it isn't overlap's default.
	Concurrency int `json:"concurrency,omitempty" toml:"concurrency,omitempty" minimum:"0" maximum:"16"`
	// Enabled is written only for a disabled job.
	Enabled *bool `json:"enabled,omitempty" toml:"enabled,omitempty"`
}

// GitExport is an exported service's git connection, without its webhook
// secret or token.
type GitExport struct {
	Repository string `json:"repository" toml:"repository"`
	Branch     string `json:"branch" toml:"branch"`
	Directory  string `json:"directory,omitempty" toml:"directory,omitempty"`
	Statuses   bool   `json:"statuses,omitempty" toml:"statuses,omitempty"`
}

// TOML renders the export as `dock project export` prints it and the UI
// downloads it: a header saying what it is, then the manifest.
func (ex ProjectExport) TOML() ([]byte, error) {
	return ex.TOMLWith(fmt.Sprintf("Tempo Dock project %s, exported with `dock project export`.\n", ex.Project) +
		"Settings as they are live. Secrets are names only (variables); values holds only references.\n" +
		"Apply it with `dock apply FILE --plan`, then without --plan to stage it.\n")
}

// TOMLWith renders the manifest after header, each of its lines a TOML
// comment.
func (ex ProjectExport) TOMLWith(header string) ([]byte, error) {
	var b bytes.Buffer
	for line := range strings.Lines(header) {
		b.WriteString("# " + line)
	}
	b.WriteString("\n")
	enc := toml.NewEncoder(&b)
	enc.SetIndentTables(true)
	if err := enc.Encode(ex); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
