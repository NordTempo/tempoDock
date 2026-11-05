package compose

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The YAML model: the parts of a Compose file the converter reads. Leaves
// are scalars kept as written (yaml.v2 would read `restart: no` as false
// and `1.10` as 1.1), interpolated when the converter uses them. Types that
// Compose writes in more than one shape decode every shape themselves.
// Structs that may hold keys Dock doesn't map record their keys, so every
// key gets a warning or a mapping.

// scalar is a YAML scalar as written. set is false for null and for a key
// that isn't there: yaml.v2 doesn't call UnmarshalYAML for null.
type scalar struct {
	text string
	set  bool
}

// UnmarshalYAML keeps the scalar's text and refuses lists and maps.
func (s *scalar) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch v.(type) {
	case []any, map[any]any:
		return fmt.Errorf("expected a single value, not a list or map")
	}
	s.set = true
	return unmarshal(&s.text)
}

// keysOf decodes a YAML map into out and returns its keys, sorted. Merge
// keys (`<<`) are resolved by yaml.v2 before either decode sees them.
func keysOf(unmarshal func(any) error, out any) ([]string, error) {
	var m map[string]any
	if err := unmarshal(&m); err != nil {
		return nil, err
	}
	if err := unmarshal(out); err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(m)), nil
}

// file is a Compose file.
type file struct {
	keys     []string
	Name     scalar                    `yaml:"name"`
	Services map[string]*service       `yaml:"services"`
	Volumes  map[string]map[string]any `yaml:"volumes"`
	Networks map[string]map[string]any `yaml:"networks"`
	// Secrets and Configs are the top-level definitions, by name.
	Secrets map[string]map[string]any `yaml:"secrets"`
	Configs map[string]map[string]any `yaml:"configs"`
}

// UnmarshalYAML records the top-level keys.
func (f *file) UnmarshalYAML(unmarshal func(any) error) error {
	type plain file
	keys, err := keysOf(unmarshal, (*plain)(f))
	f.keys = keys
	return err
}

// service is one service of a Compose file.
type service struct {
	keys        []string
	Image       scalar       `yaml:"image"`
	Build       *build       `yaml:"build"`
	Environment environment  `yaml:"environment"`
	EnvFile     envFiles     `yaml:"env_file"`
	Ports       []port       `yaml:"ports"`
	Expose      []scalar     `yaml:"expose"`
	Volumes     []mount      `yaml:"volumes"`
	DependsOn   dependsOn    `yaml:"depends_on"`
	Healthcheck *healthcheck `yaml:"healthcheck"`
	Command     command      `yaml:"command"`
	Entrypoint  command      `yaml:"entrypoint"`
	MemLimit    scalar       `yaml:"mem_limit"`
	CPUs        scalar       `yaml:"cpus"`
	Deploy      *deploy      `yaml:"deploy"`
	Restart     scalar       `yaml:"restart"`
	NetworkMode scalar       `yaml:"network_mode"`
	// Labels is a list of KEY=VALUE or a map; labelsOf reads both.
	Labels any `yaml:"labels"`
	// Secrets and Configs are names, or maps with a source.
	Secrets  []any    `yaml:"secrets"`
	Configs  []any    `yaml:"configs"`
	Profiles []string `yaml:"profiles"`
	Extends  any      `yaml:"extends"`
}

// UnmarshalYAML records the service's keys.
func (s *service) UnmarshalYAML(unmarshal func(any) error) error {
	type plain service
	keys, err := keysOf(unmarshal, (*plain)(s))
	s.keys = keys
	return err
}

// build is a service's build: a context path, or a map.
type build struct {
	keys       []string
	Context    scalar `yaml:"context"`
	Dockerfile scalar `yaml:"dockerfile"`
}

// UnmarshalYAML reads `build: ./dir` and `build: {context: ...}`.
func (b *build) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.(map[any]any); !ok {
		return unmarshal(&b.Context)
	}
	type plain build
	keys, err := keysOf(unmarshal, (*plain)(b))
	b.keys = keys
	return err
}

// envEntry is one `environment` entry. A value that isn't set (`KEY` in
// the list form, `KEY:` in the map form) comes from the shell.
type envEntry struct {
	key   string
	value scalar
}

// environment is a service's `environment`, in the map or the list form.
type environment []envEntry

// UnmarshalYAML reads both forms. Map entries come out sorted by key.
func (e *environment) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.([]any); ok {
		var list []scalar
		if err := unmarshal(&list); err != nil {
			return err
		}
		for _, item := range list {
			key, value, ok := strings.Cut(item.text, "=")
			*e = append(*e, envEntry{key: key, value: scalar{text: value, set: ok}})
		}
		return nil
	}
	var m map[string]scalar
	if err := unmarshal(&m); err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		*e = append(*e, envEntry{key: k, value: m[k]})
	}
	return nil
}

// envFileRef is one `env_file` entry.
type envFileRef struct {
	path     scalar
	optional bool
}

// envFiles is a service's `env_file`: a path, or a list of paths and
// {path, required} maps.
type envFiles []envFileRef

// UnmarshalYAML reads every form.
func (e *envFiles) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.([]any); !ok {
		var s scalar
		if err := unmarshal(&s); err != nil {
			return err
		}
		*e = envFiles{{path: s}}
		return nil
	}
	var list []envFileItem
	if err := unmarshal(&list); err != nil {
		return err
	}
	for _, item := range list {
		*e = append(*e, envFileRef(item))
	}
	return nil
}

// envFileItem is one entry of env_file's list form.
type envFileItem envFileRef

// UnmarshalYAML reads a path or a {path, required} map.
func (e *envFileItem) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.(map[any]any); !ok {
		return unmarshal(&e.path)
	}
	var long struct {
		Path     scalar `yaml:"path"`
		Required scalar `yaml:"required"`
	}
	if err := unmarshal(&long); err != nil {
		return err
	}
	e.path = long.Path
	e.optional = long.Required.set && isFalse(long.Required.text)
	return nil
}

// port is one `ports` entry: the short form ("8080:80/tcp") or the long
// one ({target, published, protocol, host_ip}).
type port struct {
	short scalar
	long  *portLong
}

// portLong is the long form of a port.
type portLong struct {
	keys      []string
	Target    scalar `yaml:"target"`
	Published scalar `yaml:"published"`
	Protocol  scalar `yaml:"protocol"`
	HostIP    scalar `yaml:"host_ip"`
}

// UnmarshalYAML reads both forms.
func (p *port) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.(map[any]any); !ok {
		return unmarshal(&p.short)
	}
	p.long = &portLong{}
	type plain portLong
	keys, err := keysOf(unmarshal, (*plain)(p.long))
	p.long.keys = keys
	return err
}

// mount is one service `volumes` entry: "source:target[:mode]" or the long
// form.
type mount struct {
	short scalar
	long  *mountLong
}

// mountLong is the long form of a mount.
type mountLong struct {
	keys     []string
	Type     scalar `yaml:"type"`
	Source   scalar `yaml:"source"`
	Target   scalar `yaml:"target"`
	ReadOnly scalar `yaml:"read_only"`
}

// UnmarshalYAML reads both forms.
func (m *mount) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.(map[any]any); !ok {
		return unmarshal(&m.short)
	}
	m.long = &mountLong{}
	type plain mountLong
	keys, err := keysOf(unmarshal, (*plain)(m.long))
	m.long.keys = keys
	return err
}

// dependency is one `depends_on` entry.
type dependency struct {
	service   string
	condition string
}

// dependsOn is a service's `depends_on`: a list of names, or a map of
// names to {condition, required, restart}.
type dependsOn []dependency

// UnmarshalYAML reads both forms. Map entries come out sorted by name.
func (d *dependsOn) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	if _, ok := v.([]any); ok {
		var list []scalar
		if err := unmarshal(&list); err != nil {
			return err
		}
		for _, s := range list {
			*d = append(*d, dependency{service: s.text})
		}
		return nil
	}
	var m map[string]*struct {
		Condition scalar `yaml:"condition"`
	}
	if err := unmarshal(&m); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		dep := dependency{service: name}
		if m[name] != nil {
			dep.condition = m[name].Condition.text
		}
		*d = append(*d, dep)
	}
	return nil
}

// healthcheck is a service's `healthcheck`.
type healthcheck struct {
	keys        []string
	Test        command `yaml:"test"`
	Interval    scalar  `yaml:"interval"`
	Retries     scalar  `yaml:"retries"`
	StartPeriod scalar  `yaml:"start_period"`
	Disable     scalar  `yaml:"disable"`
}

// UnmarshalYAML records the health check's keys.
func (h *healthcheck) UnmarshalYAML(unmarshal func(any) error) error {
	type plain healthcheck
	keys, err := keysOf(unmarshal, (*plain)(h))
	h.keys = keys
	return err
}

// command is a `command`, `entrypoint` or health check `test`: a string
// (run by a shell for tests, split into words for the others) or a list
// of words.
type command struct {
	set    bool
	isList bool
	text   string
	list   []string
}

// UnmarshalYAML reads both forms.
func (c *command) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	c.set = true
	if _, ok := v.([]any); ok {
		var list []scalar
		if err := unmarshal(&list); err != nil {
			return err
		}
		c.isList = true
		for _, s := range list {
			c.list = append(c.list, s.text)
		}
		return nil
	}
	var s scalar
	if err := unmarshal(&s); err != nil {
		return err
	}
	c.text = s.text
	return nil
}

// deploy is a service's `deploy` section.
type deploy struct {
	keys      []string
	Replicas  scalar     `yaml:"replicas"`
	Mode      scalar     `yaml:"mode"`
	Resources *resources `yaml:"resources"`
}

// UnmarshalYAML records the section's keys.
func (d *deploy) UnmarshalYAML(unmarshal func(any) error) error {
	type plain deploy
	keys, err := keysOf(unmarshal, (*plain)(d))
	d.keys = keys
	return err
}

// resources is `deploy.resources`.
type resources struct {
	keys         []string
	Limits       *limits `yaml:"limits"`
	Reservations *limits `yaml:"reservations"`
}

// UnmarshalYAML records the section's keys.
func (r *resources) UnmarshalYAML(unmarshal func(any) error) error {
	type plain resources
	keys, err := keysOf(unmarshal, (*plain)(r))
	r.keys = keys
	return err
}

// limits is `deploy.resources.limits`.
// It is also `deploy.resources.reservations`, whose devices are GPUs.
type limits struct {
	keys    []string
	Memory  scalar `yaml:"memory"`
	CPUs    scalar `yaml:"cpus"`
	Devices []any  `yaml:"devices"`
}

// UnmarshalYAML records the section's keys.
func (l *limits) UnmarshalYAML(unmarshal func(any) error) error {
	type plain limits
	keys, err := keysOf(unmarshal, (*plain)(l))
	l.keys = keys
	return err
}

// isFalse reports whether a YAML scalar means false.
func isFalse(s string) bool {
	switch strings.ToLower(s) {
	case "false", "no", "off", "n":
		return true
	}
	return false
}

// isTrue reports whether a YAML scalar means true.
func isTrue(s string) bool {
	switch strings.ToLower(s) {
	case "true", "yes", "on", "y":
		return true
	}
	return false
}

// labelsOf reads labels in the list (KEY=VALUE) or the map form.
func labelsOf(v any) map[string]string {
	out := map[string]string{}
	switch l := v.(type) {
	case []any:
		for _, e := range l {
			k, val, _ := strings.Cut(fmt.Sprint(e), "=")
			out[k] = val
		}
	case map[any]any:
		for k, val := range l {
			out[fmt.Sprint(k)] = fmt.Sprint(val)
		}
	}
	return out
}

// sourceOf is the name a service's secrets or configs entry refers to:
// the entry itself in the short form, its source in the long one.
func sourceOf(v any) string {
	if m, ok := v.(map[any]any); ok {
		return fmt.Sprint(m["source"])
	}
	return fmt.Sprint(v)
}
