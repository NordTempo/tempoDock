// Package compose converts a Docker Compose file into a Dock project file
// (ADR 0049). It is a pure conversion: no network, no database, no core.
// The daemon calls Convert for `dock import compose` and the UI's "From
// Compose", then plans the result like any project file.
//
// Only the subset Dock can run is mapped. Everything else, including keys
// it doesn't know, is reported as a Warning: nothing is dropped silently.
package compose

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Input is a Compose file and the files it reads.
type Input struct {
	Compose []byte
	// Project is the Dock project's name. Empty: the Compose file's
	// top-level `name`, else "app".
	Project string
	// EnvFiles are file contents by path as written in the Compose file
	// (env_file entries), plus ".env", used for ${VAR} interpolation.
	EnvFiles map[string]string
}

// Result is the converted project.
type Result struct {
	// File is the project file: version 1, one environment "production"
	// of kind production.
	File apitypes.ProjectExport
	// Secrets are the secret variables: their names are in File's
	// variables, their values only here.
	Secrets []Secret
	// Warnings are everything that isn't mapped exactly, sorted by service
	// (top-level ones first), then key.
	Warnings []Warning
	// Builds say where to deploy each source service from.
	Builds []Build
	// MissingFiles are env files the Compose file reads that weren't in
	// Input.EnvFiles, sorted: a client can send them and convert again.
	MissingFiles []string
}

// Secret is a secret variable's value. Value is "" when it couldn't be
// resolved: the user must set it.
type Secret struct {
	Service, Key, Value string
}

// Warning is something not mapped exactly. Service is the Dock service
// name, "" for the top level. Key is the Compose key it is about
// ("ports", "deploy.replicas"), or the variable's name. Message is a
// sentence saying what happens and, where possible, what to do.
type Warning struct {
	Service, Key, Message string
}

// Build is where a source service is deployed from: Context is the build
// context as written in the Compose file, Dockerfile the Dockerfile's path
// inside it ("Dockerfile" when the Compose file doesn't name one).
type Build struct {
	Service, Context, Dockerfile string
}

// environmentName is the one environment a converted project has.
const environmentName = "production"

// Convert converts a Compose file. It fails only when the file can't be
// read as Compose (bad YAML, a wrong shape, no services); anything it can
// read but not map is a warning.
func Convert(in Input) (Result, error) {
	var f file
	if err := yaml.Unmarshal(in.Compose, &f); err != nil {
		return Result{}, fmt.Errorf("the Compose file can't be read: %w", err)
	}
	if len(f.Services) == 0 {
		return Result{}, errors.New("the Compose file has no services; add at least one under `services:`")
	}
	c := newConverter(in)
	c.topLevel(&f)
	c.services = c.newServices(f.Services)
	for _, s := range c.services {
		c.convertService(s)
	}
	c.sharedVolumes()
	c.references()
	return c.result(), nil
}

// converter holds the state of one conversion.
type converter struct {
	in       Input
	project  string
	dotenv   map[string]string
	files    map[string]string // env files by cleaned path
	services []*svc            // sorted by Compose name
	byName   map[string]*svc   // by Compose name
	volumes  map[string]map[string]any
	warnings []Warning
	// missing are env files that weren't sent (Result.MissingFiles).
	missing []string
}

// svc is one service being converted.
type svc struct {
	compose string
	name    string
	def     *service
	out     apitypes.ServiceExport
	vars    map[string]*variable
	// mounts are the Compose volume names this service mounts.
	mounts []string
	// redisPassword is a Redis service's password from its command
	// (--requirepass) or environment, to recognise it in other services.
	redisPassword string
	// pg are a postgres template's credentials in the Compose file.
	pg *pgCreds
	// portVar is the PORT variable, which Dock sets itself.
	portVar *variable
	// build is set for a source service.
	build *Build
}

// newConverter reads the env files.
func newConverter(in Input) *converter {
	c := &converter{in: in, dotenv: map[string]string{}, files: map[string]string{}}
	for p, content := range in.EnvFiles {
		c.files[path.Clean(p)] = content
	}
	if content, ok := c.files[".env"]; ok {
		lines, bad := parseEnvFile(content, nil)
		for _, l := range lines {
			if !l.bare {
				c.dotenv[l.key] = l.value
			}
		}
		for _, n := range bad {
			c.warn(nil, ".env", "Line %d of .env isn't a KEY=VALUE variable, so it is skipped.", n)
		}
	}
	return c
}

// lookup finds a variable in .env for interpolation.
func (c *converter) lookup(name string) (string, bool) {
	v, ok := c.dotenv[name]
	return v, ok
}

// warn adds a warning about s (nil for the top level).
func (c *converter) warn(s *svc, key, format string, args ...any) {
	w := Warning{Key: key, Message: fmt.Sprintf(format, args...)}
	if s != nil {
		w.Service = s.name
	}
	c.warnings = append(c.warnings, w)
}

// str interpolates a scalar that isn't a variable value: an unset
// variable becomes "" with a warning.
func (c *converter) str(s *svc, key string, v scalar) string {
	out, missing := interpolate(v.text, c.lookup)
	for _, name := range missing {
		c.warn(s, key, "%s uses ${%s}, which isn't set in .env, so it is empty here. Add it to the .env file next to the Compose file and import again.", key, name)
	}
	return out
}

// topLevel handles the file's top-level keys and the project name.
func (c *converter) topLevel(f *file) {
	for _, k := range f.keys {
		switch {
		case k == "version", k == "name", k == "services", k == "volumes", strings.HasPrefix(k, "x-"):
		case k == "networks":
			c.warn(nil, k, "Dock gives every environment one network, and services reach each other by service name, so the networks section is ignored.")
		case k == "secrets":
			c.warn(nil, k, "Compose secrets aren't supported. Set each value as a secret variable with `dock env set` instead.")
		case k == "configs":
			c.warn(nil, k, "Compose configs aren't supported; build those files into the image instead.")
		case k == "include":
			c.warn(nil, k, "include isn't supported, so the Compose files it names aren't converted. Import each of them separately, or merge them into this file.")
		default:
			c.warn(nil, k, "%s isn't a Compose key Dock knows, so it is ignored. Check it for a typo.", k)
		}
	}
	c.project = c.in.Project
	if c.project == "" && f.Name.set {
		name := c.str(nil, "name", f.Name)
		c.project = normalizeName(name, "app")
		if c.project != name {
			c.warn(nil, "name", "The Compose project %q is named %q in Dock: project names are 1-32 lowercase letters, digits or dashes.", name, c.project)
		}
	}
	if c.project == "" {
		c.project = "app"
	}
	c.volumes = f.Volumes
	for _, name := range slices.Sorted(maps.Keys(f.Volumes)) {
		c.topVolume(name, f.Volumes[name])
	}
}

// topVolume warns about a top-level volume's settings Dock doesn't apply.
func (c *converter) topVolume(name string, def map[string]any) {
	key := "volumes." + name
	for _, k := range slices.Sorted(maps.Keys(def)) {
		v := fmt.Sprint(def[k])
		switch {
		case k == "name", strings.HasPrefix(k, "x-"):
		case k == "external":
			if isTrue(v) {
				c.warn(nil, key, "Volume %s is external in Compose (it already exists on that host). Dock creates a new, empty volume; copy the data over if you need it.", name)
			}
		case k == "driver":
			if v != "local" {
				c.warn(nil, key, "Volume %s uses the %s driver, which isn't supported; Dock creates a local volume on the service's server.", name, v)
			}
		case k == "driver_opts":
			c.warn(nil, key, "The driver options of volume %s aren't applied; Dock creates a plain local volume on the service's server.", name)
		case k == "labels":
			c.warn(nil, key, "The labels of volume %s are ignored.", name)
		default:
			c.warn(nil, key, "%s isn't a volume key Dock knows, so it is ignored.", k)
		}
	}
}

// newServices assigns Dock names and warns about the changed ones.
func (c *converter) newServices(defs map[string]*service) []*svc {
	names := slices.Sorted(maps.Keys(defs))
	dock := assignNames(names, "service", reservedNames)
	c.byName = make(map[string]*svc, len(names))
	out := make([]*svc, 0, len(names))
	for _, n := range names {
		def := defs[n]
		if def == nil {
			def = &service{}
		}
		s := &svc{compose: n, name: dock[n], def: def, vars: map[string]*variable{}}
		s.out.Name = s.name
		if s.name != n {
			why := "service names are 1-32 lowercase letters, digits or dashes"
			if slices.Contains(reservedNames, normalizeName(n, "")) {
				why = "shared and org are reserved for references"
			}
			c.warn(s, "", "Compose service %q is named %q in Dock: %s. Other services' variables that name it are updated.", n, s.name, why)
		}
		c.byName[n] = s
		out = append(out, s)
	}
	return out
}

// service finds a service by its Compose name.
func (c *converter) service(composeName string) *svc { return c.byName[composeName] }

// sharedVolumes warns when services share a named volume: in Dock each
// service has its own volumes.
func (c *converter) sharedVolumes() {
	users := map[string][]string{}
	for _, s := range c.services {
		for _, v := range s.mounts {
			users[v] = append(users[v], s.name)
		}
	}
	for _, s := range c.services {
		for _, v := range s.mounts {
			others := slices.DeleteFunc(slices.Clone(users[v]), func(n string) bool { return n == s.name })
			if len(others) > 0 {
				c.warn(s, "volumes", "Volume %s is also mounted by %s in Compose. In Dock every service has its own volumes, so they don't share these files.", v, strings.Join(others, ", "))
			}
		}
	}
}

// result assembles the project file, secrets, builds and warnings.
func (c *converter) result() Result {
	var r Result
	env := apitypes.EnvironmentExport{Name: environmentName, Kind: apitypes.KindProduction}
	for _, s := range c.services {
		for _, key := range slices.Sorted(maps.Keys(s.vars)) {
			v := s.vars[key]
			secret := !v.ref && (isSecretKey(key) || isSecretValue(v.value) || v.unresolved())
			if !secret {
				if s.out.Values == nil {
					s.out.Values = map[string]string{}
				}
				s.out.Values[key] = v.value
				continue
			}
			s.out.Variables = append(s.out.Variables, key)
			sec := Secret{Service: s.name, Key: key}
			if !v.unresolved() {
				sec.Value = v.value
			}
			r.Secrets = append(r.Secrets, sec)
		}
		env.Services = append(env.Services, s.out)
	}
	slices.SortFunc(env.Services, func(a, b apitypes.ServiceExport) int { return cmp.Compare(a.Name, b.Name) })
	r.File = apitypes.ProjectExport{Version: apitypes.ExportVersion, Project: c.project, Environments: []apitypes.EnvironmentExport{env}}
	slices.SortFunc(r.Secrets, func(a, b Secret) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Key, b.Key))
	})
	for _, s := range c.services {
		if s.build != nil {
			r.Builds = append(r.Builds, *s.build)
		}
	}
	slices.SortFunc(r.Builds, func(a, b Build) int { return cmp.Compare(a.Service, b.Service) })
	r.Warnings = c.warnings
	r.MissingFiles = slices.Sorted(slices.Values(c.missing))
	slices.SortStableFunc(r.Warnings, func(a, b Warning) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Key, b.Key))
	})
	return r
}
