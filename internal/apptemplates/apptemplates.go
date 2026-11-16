// Package apptemplates is the catalog of app templates (ADR 0083): each
// app's services as a project file fragment (ADR 0049) with the inputs
// its form asks for and the secrets Dock generates. Expand turns one into
// the environment a project file would describe, which core plans and
// creates with the code `dock apply` uses. The catalog is embedded and
// parsed on first use.
package apptemplates

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Template is an app template as its file in catalog/ writes it.
type Template struct {
	Name     string `toml:"name"`
	Title    string `toml:"title"`
	Summary  string `toml:"summary"`
	Category string `toml:"category"`
	Website  string `toml:"website"`
	License  string `toml:"license"`
	// Revision goes up with every change to the file, so services know
	// which version made them.
	Revision int `toml:"revision"`
	// Main is the service the user names and the domain goes on.
	Main string `toml:"main"`
	// Domain gives the main service a domain: the one given, or a
	// generated one.
	Domain   bool                     `toml:"domain"`
	Notes    string                   `toml:"notes"`
	Inputs   []apitypes.TemplateInput `toml:"inputs"`
	Services []Service                `toml:"services"`
	// Mark is the SVG path of the app's mark (catalog/marks/NAME.svg).
	Mark string `toml:"-"`
}

// Service is one service of a template: the project file's service, plus
// what Dock generates for it and how its image is bumped.
type Service struct {
	apitypes.ServiceExport
	// Generate are variables Dock fills once, by kind: password, hex:N,
	// base64:N (N random bytes) or uuid.
	Generate map[string]string `toml:"generate"`
	// Track is the tag pattern scripts/templates bumps the image along.
	Track string `toml:"track"`
}

//go:embed catalog/*.toml catalog/marks/*.svg
var files embed.FS

// Catalog returns the built-in templates by name, parsed once.
var Catalog = sync.OnceValues(func() (map[string]Template, error) {
	entries, err := files.ReadDir("catalog")
	if err != nil {
		return nil, err
	}
	out := map[string]Template{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".toml")
		if !ok {
			continue
		}
		b, err := files.ReadFile("catalog/" + e.Name())
		if err != nil {
			return nil, err
		}
		t, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		if t.Name != name {
			return nil, fmt.Errorf("template %s: name is %q; it must match the file name", name, t.Name)
		}
		t.Mark = Mark(name)
		out[name] = t
	}
	return out, nil
})

// Mark is the SVG path data of the mark called name ("" for none): an
// app's, or a database template's (postgres, redis).
func Mark(name string) string {
	svg, err := files.ReadFile("catalog/marks/" + name + ".svg")
	if err != nil {
		return ""
	}
	return markPath(svg)
}

// pathRe finds the path data of a Simple Icons SVG.
var pathRe = regexp.MustCompile(`<path[^>]*\sd="([^"]+)"`)

// markPath is the path data of a mark's SVG ("" when it has none).
func markPath(svg []byte) string {
	if m := pathRe.FindSubmatch(svg); m != nil {
		return string(m[1])
	}
	return ""
}

// MaxSize caps a template file (org templates are uploaded).
const MaxSize = 64 << 10

// Parse reads a template file strictly (an unknown key is an error) and
// checks it.
func Parse(b []byte) (Template, error) {
	var t Template
	if len(b) > MaxSize {
		return t, fmt.Errorf("the template is larger than %d KB", MaxSize>>10)
	}
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			var keys []string
			for _, e := range strict.Errors {
				keys = append(keys, strings.Join(e.Key(), "."))
			}
			return t, fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
		}
		return t, fmt.Errorf("isn't valid TOML: %w", err)
	}
	return t, t.check()
}

// nameRe is a template's and its services' names: Dock's name rules.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// check checks a template's structure: names, the main service, inputs
// naming real services, references to its own services, and no keys a
// template can't set (domains, git, secrets by name).
func (t Template) check() error {
	if !nameRe.MatchString(t.Name) {
		return fmt.Errorf("name %q must be lower case letters, digits and dashes", t.Name)
	}
	if t.Title == "" || t.Summary == "" || t.Category == "" || t.License == "" {
		return errors.New("title, summary, category and license are required")
	}
	if t.Revision < 1 {
		return errors.New("revision must be 1 or more")
	}
	if len(t.Services) == 0 {
		return errors.New("a template needs at least one service")
	}
	local := map[string]bool{}
	for _, s := range t.Services {
		if !nameRe.MatchString(s.Name) {
			return fmt.Errorf("service name %q must be lower case letters, digits and dashes", s.Name)
		}
		if local[s.Name] {
			return fmt.Errorf("service %s is in the template twice", s.Name)
		}
		local[s.Name] = true
	}
	if !local[t.Main] {
		return fmt.Errorf("main is %q, which isn't one of the template's services", t.Main)
	}
	for _, s := range t.Services {
		switch {
		case s.Image == "" && s.Template == apitypes.TemplateNone:
			return fmt.Errorf("service %s needs an image or a database template", s.Name)
		case len(s.Domains) > 0:
			return fmt.Errorf("service %s: domains come from the template's domain setting", s.Name)
		case s.Git != nil || s.Stopped:
			return fmt.Errorf("service %s: a template can't set git or stopped", s.Name)
		case len(s.Variables) > 0:
			return fmt.Errorf("service %s: secrets are inputs (kind secret) or generated, not variables", s.Name)
		}
		for _, d := range s.DependsOn {
			if !local[d] || d == s.Name {
				return fmt.Errorf("service %s depends_on %q, which isn't another service of the template", s.Name, d)
			}
		}
		for k, v := range s.Values {
			for _, m := range refRe.FindAllStringSubmatch(v, -1) {
				if m[1] != "shared" && m[1] != "org" && !local[m[1]] {
					return fmt.Errorf("service %s: %s references %s, which isn't a service of the template", s.Name, k, m[0])
				}
			}
		}
		for k, kind := range s.Generate {
			if _, err := generate(kind); err != nil {
				return fmt.Errorf("service %s: generate %s: %w", s.Name, k, err)
			}
			if _, ok := s.Values[k]; ok {
				return fmt.Errorf("service %s: %s is both generated and in values", s.Name, k)
			}
		}
	}
	keys := map[string]bool{}
	for _, in := range t.Inputs {
		svc := cmp.Or(in.Service, t.Main)
		i := slices.IndexFunc(t.Services, func(s Service) bool { return s.Name == svc })
		switch {
		case in.Key == "" || in.Label == "":
			return errors.New("every input needs a key and a label")
		case keys[in.Key]:
			return fmt.Errorf("input %s is in the template twice", in.Key)
		case i < 0:
			return fmt.Errorf("input %s is for service %q, which isn't in the template", in.Key, svc)
		case !slices.Contains([]apitypes.TemplateInputKind{apitypes.InputText, apitypes.InputEmail, apitypes.InputSecret, apitypes.InputChoice}, in.Kind):
			return fmt.Errorf("input %s: kind must be text, email, secret or choice", in.Key)
		case in.Kind == apitypes.InputChoice && !slices.Contains(in.Choices, in.Default):
			return fmt.Errorf("input %s: a choice needs choices and a default among them", in.Key)
		}
		s := t.Services[i]
		if _, ok := s.Values[in.Key]; ok {
			return fmt.Errorf("input %s is also in the values of %s", in.Key, svc)
		}
		if _, ok := s.Generate[in.Key]; ok {
			return fmt.Errorf("input %s is also generated for %s", in.Key, svc)
		}
		keys[in.Key] = true
	}
	return nil
}

// refRe matches variable references, as core resolves them.
var refRe = regexp.MustCompile(`\$\{\{\s*([a-z0-9-]+)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Info describes a template for the picker, the CLI and agents.
func (t Template) Info() apitypes.AppTemplate {
	out := apitypes.AppTemplate{
		Name: t.Name, Title: t.Title, Summary: t.Summary, Category: t.Category, Website: t.Website,
		License: t.License, Revision: t.Revision, Mark: t.Mark, Domain: t.Domain, Notes: t.Notes,
		Inputs: slices.Clone(t.Inputs), Services: []apitypes.AppTemplateService{},
	}
	if out.Inputs == nil {
		out.Inputs = []apitypes.TemplateInput{}
	}
	for _, s := range t.Services {
		svc := apitypes.AppTemplateService{
			Name: s.Name, Main: s.Name == t.Main, Image: s.Image, Template: s.Template, Port: s.Port, MemoryMB: s.MemoryMB,
			Generated: slices.Sorted(maps.Keys(s.Generate)),
		}
		for _, v := range s.Volumes {
			svc.Volumes = append(svc.Volumes, v.MountPath)
		}
		out.Services = append(out.Services, svc)
	}
	// Main first, then as the file lists them.
	slices.SortStableFunc(out.Services, func(x, y apitypes.AppTemplateService) int { return boolInt(y.Main) - boolInt(x.Main) })
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Use is what a template is used with: the main service's name and the
// inputs by key.
type Use struct {
	Name   string
	Domain string
	Inputs map[string]string
}

// Expanded is a template turned into an environment of a project file:
// its services under their real names, and the values sent with it
// (generated secrets and secret inputs), never written in the file.
type Expanded struct {
	Services []apitypes.ServiceExport
	Values   []apitypes.VariableValue
	// Main is the main service's name.
	Main string
}

// Expand names the template's services for u (the main one u.Name, the
// others NAME-<local>), rewrites references and depends_on to those
// names, fills inputs and generates secrets. env is the environment the
// values are for.
func (t Template) Expand(env string, u Use) (Expanded, error) {
	main := cmp.Or(u.Name, t.Name)
	if !nameRe.MatchString(main) {
		return Expanded{}, fmt.Errorf("name %q must be lower case letters, digits and dashes, at most 32", main)
	}
	names := map[string]string{}
	for _, s := range t.Services {
		names[s.Name] = main
		if s.Name != t.Main {
			names[s.Name] = main + "-" + s.Name
		}
		if len(names[s.Name]) > 32 {
			return Expanded{}, fmt.Errorf("name %q makes service %s longer than 32 characters; pick a shorter one", main, names[s.Name])
		}
	}
	rename := func(v string) string {
		return refRe.ReplaceAllStringFunc(v, func(m string) string {
			sub := refRe.FindStringSubmatch(m)
			if n, ok := names[sub[1]]; ok {
				return "${{" + n + "." + sub[2] + "}}"
			}
			return m
		})
	}
	for k := range u.Inputs {
		if !slices.ContainsFunc(t.Inputs, func(in apitypes.TemplateInput) bool { return in.Key == k }) {
			return Expanded{}, fmt.Errorf("%s has no input %s; its inputs are %s", t.Title, k, t.inputKeys())
		}
	}
	out := Expanded{Main: main, Services: make([]apitypes.ServiceExport, 0, len(t.Services))}
	byLocal := map[string]int{}
	for _, s := range t.Services {
		fs := s.ServiceExport
		fs.Name = names[s.Name]
		fs.Values = map[string]string{}
		for k, v := range s.Values {
			fs.Values[k] = rename(v)
		}
		fs.DependsOn = nil
		for _, d := range s.DependsOn {
			fs.DependsOn = append(fs.DependsOn, names[d])
		}
		for _, k := range slices.Sorted(maps.Keys(s.Generate)) {
			v, _ := generate(s.Generate[k]) // checked by Parse
			fs.Variables = append(fs.Variables, k)
			out.Values = append(out.Values, apitypes.VariableValue{Environment: env, Service: fs.Name, Key: k, Value: v})
		}
		if s.Name == t.Main && t.Domain {
			if u.Domain != "" {
				fs.Domains = []apitypes.DomainExport{{Hostname: u.Domain}}
			} else {
				fs.Domains = []apitypes.DomainExport{{Generated: true}}
			}
		}
		byLocal[s.Name] = len(out.Services)
		out.Services = append(out.Services, fs)
	}
	for _, in := range t.Inputs {
		fs := &out.Services[byLocal[cmp.Or(in.Service, t.Main)]]
		v, given := u.Inputs[in.Key]
		v = strings.TrimSpace(v)
		if !given || v == "" {
			v = in.Default
		}
		switch {
		case v == "" && in.Optional:
			continue
		case v == "":
			return Expanded{}, fmt.Errorf("%s needs %s (input %s)", t.Title, strings.ToLower(in.Label), in.Key)
		case in.Kind == apitypes.InputChoice && !slices.Contains(in.Choices, v):
			return Expanded{}, fmt.Errorf("input %s is one of %s", in.Key, strings.Join(in.Choices, ", "))
		case in.Kind == apitypes.InputEmail && !strings.Contains(v, "@"):
			return Expanded{}, fmt.Errorf("input %s needs an email address", in.Key)
		case in.Kind == apitypes.InputSecret:
			fs.Variables = append(fs.Variables, in.Key)
			out.Values = append(out.Values, apitypes.VariableValue{Environment: env, Service: fs.Name, Key: in.Key, Value: v})
		default:
			fs.Values[in.Key] = v
		}
	}
	// The main service first, so a plan reads from the app down.
	slices.SortStableFunc(out.Services, func(x, y apitypes.ServiceExport) int { return boolInt(y.Name == main) - boolInt(x.Name == main) })
	return out, nil
}

func (t Template) inputKeys() string {
	if len(t.Inputs) == 0 {
		return "none"
	}
	var keys []string
	for _, in := range t.Inputs {
		keys = append(keys, in.Key)
	}
	return strings.Join(keys, ", ")
}

// generate makes a value of a generate kind: password (32 base32
// characters, safe in URLs and shells), hex:N and base64:N (N random
// bytes) or uuid (version 4).
func generate(kind string) (string, error) {
	name, n, hasN := strings.Cut(kind, ":")
	size := 0
	if hasN {
		var err error
		if size, err = strconv.Atoi(n); err != nil || size < 8 || size > 128 {
			return "", fmt.Errorf("%q: the size is a number of bytes from 8 to 128", kind)
		}
	}
	switch {
	case name == "password" && !hasN:
		return rand.Text() + rand.Text()[:6], nil
	case name == "uuid" && !hasN:
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		h := hex.EncodeToString(b)
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
	case name == "hex" && hasN:
		b := make([]byte, size)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b), nil
	case name == "base64" && hasN:
		b := make([]byte, size)
		_, _ = rand.Read(b)
		return base64.StdEncoding.EncodeToString(b), nil
	}
	return "", fmt.Errorf("%q: the kinds are password, uuid, hex:N and base64:N", kind)
}
