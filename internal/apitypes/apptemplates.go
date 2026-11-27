package apitypes

// App templates (ADR 0083): an app's services as a project file fragment
// with the inputs its form asks for. Using one creates ordinary services.

// TemplateInputKind is what a template input asks for, and how the form
// shows it.
type TemplateInputKind string

const (
	InputText   TemplateInputKind = "text"
	InputEmail  TemplateInputKind = "email"
	InputSecret TemplateInputKind = "secret"
	InputChoice TemplateInputKind = "choice"
)

// TemplateInput is one input of an app template: a variable of one of its
// services, filled in when the template is used.
type TemplateInput struct {
	// Key is the variable the value is set as.
	Key string `json:"key" toml:"key"`
	// Service is the template's (local) name of the service that gets the
	// variable; empty for the main service.
	Service  string            `json:"service,omitempty" toml:"service,omitempty"`
	Label    string            `json:"label" toml:"label"`
	Kind     TemplateInputKind `json:"kind" toml:"kind" enum:"text,email,secret,choice"`
	Default  string            `json:"default,omitempty" toml:"default,omitempty"`
	Optional bool              `json:"optional,omitempty" toml:"optional,omitempty"`
	Choices  []string          `json:"choices,omitempty" toml:"choices,omitempty"`
	Help     string            `json:"help,omitempty" toml:"help,omitempty"`
}

// AppTemplate describes a template of the catalog for the picker, the CLI
// and agents.
type AppTemplate struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Category string `json:"category"`
	Website  string `json:"website,omitempty"`
	License  string `json:"license"`
	Revision int    `json:"revision"`
	// Mark is the SVG path data of the app's monochrome mark on a 24x24
	// view box; empty when it has none (the UI shows its initial).
	Mark string `json:"mark,omitempty"`
	// MemoryMB is what its services ask for together.
	MemoryMB int `json:"memory_mb"`
	// Domain is whether the main service is reachable from outside (it
	// gets a domain: one given, or a generated one).
	Domain   bool                 `json:"domain"`
	Services []AppTemplateService `json:"services"`
	Inputs   []TemplateInput      `json:"inputs"`
	// Notes say what to do once it runs (the first sign-in, say).
	Notes string `json:"notes,omitempty"`
	// Org is set for the organization's own templates, saved by its
	// admins; the others come with Dock.
	Org bool `json:"org,omitempty"`
}

// SaveTemplateRequest saves an organization template from its file, in
// the catalog's format (`dock template show NAME --toml` prints one).
type SaveTemplateRequest struct {
	File string `json:"file" doc:"The template file, TOML"`
}

// SaveTemplateFromRequest saves an environment's services as an
// organization template.
type SaveTemplateFromRequest struct {
	Name     string   `json:"name" doc:"The template's name: lower case letters, digits and dashes"`
	Title    string   `json:"title,omitempty" doc:"Default: the name"`
	Summary  string   `json:"summary,omitempty"`
	Services []string `json:"services,omitempty" doc:"The services it holds; default: every service that runs an image or a database template"`
	Main     string   `json:"main,omitempty" doc:"The service the user names; default: the first one that has a domain, else the first"`
}

// AppTemplateService is one service an app template creates.
type AppTemplateService struct {
	// Name is the template's name for it: the main service takes the name
	// given, the others NAME-<this>.
	Name     string   `json:"name"`
	Main     bool     `json:"main,omitempty"`
	Image    string   `json:"image,omitempty"`
	Template Template `json:"template,omitempty" enum:"postgres,redis,mysql,mariadb,valkey,pgvector,mongodb"`
	Port     int      `json:"port,omitempty"`
	MemoryMB int      `json:"memory_mb"`
	// Generated are the variables Dock generates for it (secrets).
	Generated []string `json:"generated,omitempty"`
	// Volumes are its volumes' mount paths.
	Volumes []string `json:"volumes,omitempty"`
}

// UseTemplateRequest uses an app template in an environment.
type UseTemplateRequest struct {
	Name   string `json:"name,omitempty" doc:"The main service's name; the others are NAME-<part>. Default: the template's name"`
	Domain string `json:"domain,omitempty" doc:"Hostname for the main service; empty for a generated one"`
	// Inputs are the template's inputs by key; secrets are sealed and
	// never shown back.
	Inputs map[string]string `json:"inputs,omitempty" doc:"Values for the template's inputs, by key"`
	Plan   bool              `json:"plan,omitempty" doc:"Only plan: say what it would create, change nothing"`
	// Detach returns once the services exist: their first deploys run in
	// order in the background, and the result lists no releases. The
	// console's dialog and `dock template use --detach` send it.
	Detach bool `json:"detach,omitempty" doc:"Return once the services exist; their first deploys run in the background and releases is empty"`
}

// UseTemplateResult is what using a template created, or would create.
type UseTemplateResult struct {
	Plan ApplyPlan `json:"plan"`
	// Services are the services created (or planned), main first.
	Services []string  `json:"services"`
	Releases []Release `json:"releases"`
	// File is the project file the template expands to, for a plan: edit
	// it and `dock apply` it for what the inputs don't cover. Generated
	// secrets are names in it, never values.
	File string `json:"file,omitempty"`
	// Failed lists what didn't work after the services were created (a
	// domain, a first deploy), each with what to do.
	Failed []string `json:"failed"`
}

// FromTemplate names the app template a service was created from.
type FromTemplate struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

// TemplateFile is a template's file, TOML.
type TemplateFile struct {
	File string `json:"file"`
}
