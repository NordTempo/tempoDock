package apitypes

// ApplyRequest plans or stages a project file (`dock apply FILE`, ADR
// 0049). The project is the one in the path; the file's own `project` key
// is informational.
type ApplyRequest struct {
	// File is the project file: TOML as `dock project export` writes it,
	// or its JSON.
	File string `json:"file" doc:"The project file, TOML or JSON, as dock project export writes it"`
	// Environment limits the plan to one environment of the file.
	Environment string `json:"environment,omitempty" doc:"Only this environment of the file"`
	// Prune stages removals of what the file doesn't mention (variables,
	// domains, services). Never the default.
	Prune bool `json:"prune,omitempty" doc:"Also remove variables, domains and services the file doesn't mention"`
	// Values are variable values staged with the file (secrets the file
	// names without a value). Sealed, never echoed.
	Values []VariableValue `json:"values,omitempty" doc:"Values for variables the file names without one; staged, never shown back"`
	// Deploy applies each touched environment's staged changes afterwards,
	// as `dock changes apply` does. Only for apply.
	Deploy bool `json:"deploy,omitempty" doc:"Apply each touched environment's staged changes afterwards (apply only)"`
}

// VariableValue is one variable value sent with an apply.
type VariableValue struct {
	Environment string `json:"environment"`
	Service     string `json:"service,omitempty" doc:"Empty for a shared (environment-level) variable"`
	Key         string `json:"key"`
	Value       string `json:"value"`
}

// PlanKind is what a plan change is about.
type PlanKind string

const (
	PlanProject     PlanKind = "project"
	PlanEnvironment PlanKind = "environment"
	PlanService     PlanKind = "service"
	PlanSetting     PlanKind = "setting"
	PlanVariable    PlanKind = "variable"
	PlanVolume      PlanKind = "volume"
	PlanDomain      PlanKind = "domain"
	PlanGit         PlanKind = "git"
	PlanDatabase    PlanKind = "database"
)

// PlanChange is one line of a plan. Variable values are never in it.
type PlanChange struct {
	Environment string       `json:"environment,omitempty"`
	Service     string       `json:"service,omitempty"`
	Kind        PlanKind     `json:"kind" enum:"project,environment,service,setting,variable,volume,domain,git,database"`
	Action      ChangeAction `json:"action" enum:"add,update,remove"`
	// Name is the setting's field, the variable's key, the volume's name or
	// the domain's hostname.
	Name string `json:"name,omitempty"`
	// Old and New are settings, mount paths and the like as text.
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
	// Note explains a kept or manual item, or adds context.
	Note string `json:"note,omitempty"`
}

// ApplyPlan is the difference between a project file and the live state.
type ApplyPlan struct {
	Project string `json:"project"`
	// Changes is what applying stages (or creates: the project and empty
	// environments).
	Changes []PlanChange `json:"changes"`
	// Kept are live things the file doesn't mention. Prune removes the
	// removable ones (variables, domains, services); volumes and
	// environments are never removed.
	Kept []PlanChange `json:"kept"`
	// Manual is what applying can't do and a person must: values for
	// secrets the file names, git connections, data settings.
	Manual []PlanChange `json:"manual"`
}

// ApplyResult is what staging a project file did.
type ApplyResult struct {
	Plan ApplyPlan `json:"plan"`
	// Staged are the staged changes of each touched environment afterwards.
	Staged []PendingChanges `json:"staged"`
	// Applied, with deploy, is what applying each environment started.
	Applied []EnvironmentApplied `json:"applied,omitempty"`
}

// EnvironmentApplied is what applying one environment's changes started.
type EnvironmentApplied struct {
	Environment string `json:"environment"`
	AppliedChanges
}
