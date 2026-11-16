// Package projectfile reads project files (`dock apply FILE`, ADR 0049):
// the format `dock project export` writes, as TOML or its JSON. Parsing is
// strict, so a typo is an error instead of a setting silently left out.
// It checks the file's structure; the daemon validates names, settings and
// hostnames with the same rules as every other way to change them.
package projectfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// MaxSize caps a project file, so a request can't make the daemon parse
// an arbitrarily large one.
const MaxSize = 1 << 20

// Parse reads a project file, TOML or JSON (a file starting with `{`), and
// checks its structure.
func Parse(b []byte) (apitypes.ProjectExport, error) {
	var f apitypes.ProjectExport
	if len(b) > MaxSize {
		return f, fmt.Errorf("the project file is larger than %d KB", MaxSize>>10)
	}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return f, errors.New("the project file is empty")
	}
	if trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return f, fmt.Errorf("the project file isn't valid JSON: %w", err)
		}
	} else {
		dec := toml.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return f, tomlError(err)
		}
	}
	return f, Check(f)
}

// tomlError says where a TOML error is, with the key for unknown ones.
func tomlError(err error) error {
	if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		var keys []string
		for _, e := range strict.Errors {
			row, _ := e.Position()
			keys = append(keys, fmt.Sprintf("%s (line %d)", strings.Join(e.Key(), "."), row))
		}
		return fmt.Errorf("unknown keys in the project file: %s", strings.Join(keys, ", "))
	}
	if de, ok := errors.AsType[*toml.DecodeError](err); ok {
		row, col := de.Position()
		return fmt.Errorf("the project file isn't valid TOML: line %d, column %d: %s", row, col, de.Error())
	}
	return fmt.Errorf("the project file isn't valid TOML: %w", err)
}

// Check checks a file's structure: its version, that names are unique and
// given, that a variable isn't both named and valued, and that depends_on
// names services of the same environment.
func Check(f apitypes.ProjectExport) error {
	if f.Version != apitypes.ExportVersion {
		return fmt.Errorf("version is %d; this Dock reads version %d project files (write `version = %d`)", f.Version, apitypes.ExportVersion, apitypes.ExportVersion)
	}
	envs := map[string]bool{}
	for _, e := range f.Environments {
		if e.Name == "" {
			return errors.New("an environment has no name")
		}
		if envs[e.Name] {
			return fmt.Errorf("environment %s is in the file twice", e.Name)
		}
		envs[e.Name] = true
		if err := checkVariables(e.Name, e.SharedVariables, e.SharedValues); err != nil {
			return err
		}
		svcs := map[string]bool{}
		for _, s := range e.Services {
			if s.Name == "" {
				return fmt.Errorf("a service in environment %s has no name", e.Name)
			}
			if svcs[s.Name] {
				return fmt.Errorf("service %s is in environment %s twice", s.Name, e.Name)
			}
			svcs[s.Name] = true
		}
		for _, s := range e.Services {
			where := e.Name + "/" + s.Name
			if err := checkVariables(where, s.Variables, s.Values); err != nil {
				return err
			}
			for _, d := range s.DependsOn {
				if d == s.Name || !svcs[d] {
					return fmt.Errorf("%s depends_on %q, which isn't another service of environment %s", where, d, e.Name)
				}
			}
			vols := map[string]bool{}
			for _, v := range s.Volumes {
				if v.Name == "" || v.MountPath == "" {
					return fmt.Errorf("a volume of %s needs a name and a mount_path", where)
				}
				if vols[v.Name] {
					return fmt.Errorf("volume %s of %s is in the file twice", v.Name, where)
				}
				vols[v.Name] = true
			}
			hosts := map[string]bool{}
			generated := 0
			for _, d := range s.Domains {
				switch {
				case d.Generated:
					generated++
				case d.Hostname == "":
					return fmt.Errorf("a domain of %s has no hostname; write generated = true for a generated one", where)
				case hosts[d.Hostname]:
					return fmt.Errorf("domain %s of %s is in the file twice", d.Hostname, where)
				}
				hosts[d.Hostname] = true
			}
			if generated > 1 {
				return fmt.Errorf("%s has %d generated domains; a service gets one", where, generated)
			}
			if err := checkJobs(where, s.Jobs); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkJobs refuses a job without a name, schedule or command, and a name
// listed twice. The daemon checks the schedule, timezone and limits.
func checkJobs(where string, jobs []apitypes.JobExport) error {
	if len(jobs) > apitypes.MaxJobsPerService {
		return fmt.Errorf("%s has %d jobs; a service has at most %d", where, len(jobs), apitypes.MaxJobsPerService)
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		switch {
		case j.Name == "":
			return fmt.Errorf("a job of %s has no name", where)
		case seen[j.Name]:
			return fmt.Errorf("job %s of %s is in the file twice", j.Name, where)
		case strings.TrimSpace(j.Schedule) == "" || strings.TrimSpace(j.Command) == "":
			return fmt.Errorf("job %s of %s needs a schedule and a command", j.Name, where)
		}
		seen[j.Name] = true
	}
	return nil
}

// checkVariables refuses a key that is both named (value set separately)
// and valued in the file, and duplicate names.
func checkVariables(where string, names []string, values map[string]string) error {
	seen := map[string]bool{}
	for _, k := range names {
		if seen[k] {
			return fmt.Errorf("variable %s of %s is listed twice", k, where)
		}
		seen[k] = true
		if _, ok := values[k]; ok {
			return fmt.Errorf("variable %s of %s is in both variables and values; a value set separately (a secret) goes in variables, one written in the file in values", k, where)
		}
	}
	return nil
}

// Environment returns the file's environment by name.
func Environment(f apitypes.ProjectExport, name string) (apitypes.EnvironmentExport, bool) {
	i := slices.IndexFunc(f.Environments, func(e apitypes.EnvironmentExport) bool { return e.Name == name })
	if i < 0 {
		return apitypes.EnvironmentExport{}, false
	}
	return f.Environments[i], true
}
