package apitypes

import (
	"bytes"
	"fmt"

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
	Version      int                 `json:"version" toml:"version" doc:"Manifest format version"`
	Project      string              `json:"project" toml:"project"`
	Environments []EnvironmentExport `json:"environments" toml:"environments"`
}

// EnvironmentExport is one environment of an export.
type EnvironmentExport struct {
	Name string          `json:"name" toml:"name"`
	Kind EnvironmentKind `json:"kind" toml:"kind"`
	// SharedVariables are the names of the environment's shared variables.
	SharedVariables []string        `json:"shared_variables,omitempty" toml:"shared_variables,omitempty"`
	Services        []ServiceExport `json:"services,omitempty" toml:"services,omitempty"`
}

// ServiceExport is one service of an export: its settings (zero values
// left out), variable names, volumes, domains and git connection.
type ServiceExport struct {
	Name                      string          `json:"name" toml:"name"`
	Template                  Template        `json:"template,omitempty" toml:"template,omitempty"`
	Image                     string          `json:"image,omitempty" toml:"image,omitempty"`
	Port                      int             `json:"port,omitempty" toml:"port,omitempty"`
	HealthCheckPath           string          `json:"health_check_path,omitempty" toml:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds int             `json:"health_check_timeout_seconds,omitempty" toml:"health_check_timeout_seconds,omitempty"`
	StartCommand              string          `json:"start_command,omitempty" toml:"start_command,omitempty"`
	DockerfilePath            string          `json:"dockerfile_path,omitempty" toml:"dockerfile_path,omitempty"`
	BuildCommand              string          `json:"build_command,omitempty" toml:"build_command,omitempty"`
	MemoryMB                  int             `json:"memory_mb,omitempty" toml:"memory_mb,omitempty"`
	CPUs                      float64         `json:"cpus,omitempty" toml:"cpus,omitempty"`
	Database                  DatabaseSetting `json:"database,omitempty" toml:"database,omitempty"`
	DatabaseURLVariable       string          `json:"database_url_variable,omitempty" toml:"database_url_variable,omitempty"`
	// Variables are the names of the service's own variables.
	Variables []string       `json:"variables,omitempty" toml:"variables,omitempty"`
	Volumes   []VolumeExport `json:"volumes,omitempty" toml:"volumes,omitempty,inline"`
	Domains   []DomainExport `json:"domains,omitempty" toml:"domains,omitempty,inline"`
	Git       *GitExport     `json:"git,omitempty" toml:"git,omitempty,inline"`
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
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Tempo Dock project %s, exported with `dock project export`.\n", ex.Project)
	b.WriteString("# Settings as they are live. Variables are names only: values are never exported.\n\n")
	enc := toml.NewEncoder(&b)
	enc.SetIndentTables(true)
	if err := enc.Encode(ex); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
