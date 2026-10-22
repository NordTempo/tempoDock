package apitypes

// CloneEnvironmentRequest copies an environment into a new one of the same
// project (`dock environment clone staging staging-2`, ADR 0022).
type CloneEnvironmentRequest struct {
	Name string          `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"The new environment's DNS-safe name"`
	Kind EnvironmentKind `json:"kind,omitempty" enum:"production,non_production" doc:"Default: production for an environment named production, otherwise non_production. Never changes."`
	// Deploy also deploys the images running in the source, by digest,
	// through promote's path. Without it nothing is deployed.
	Deploy bool `json:"deploy,omitempty" doc:"Also deploy the images serving in the source, by digest, like promote"`
	// CopyValues copies literal values into an environment of the other
	// kind (production to non-production or the reverse), which a clone
	// otherwise never does. Admin only; same-kind clones ignore it.
	CopyValues bool `json:"copy_values,omitempty" doc:"Admin only: also copy literal values when the clone's kind differs from the source's (e.g. production secrets into non-production)"`
}

// CloneResult says what a clone created, what it set anew and what it
// left out.
type CloneResult struct {
	From        string      `json:"from"`
	Environment Environment `json:"environment"`
	Services    []string    `json:"services" doc:"Cloned services, by name"`
	Variables   int         `json:"variables" doc:"Variables created with the source's value"`
	// Generated are template variables whose values weren't copied, set
	// anew: a new password and the template's defaults.
	Generated []VariableRef `json:"generated" doc:"Template variables set anew (new password, template defaults) because their values weren't copied"`
	// Skipped are variables left out because their literal values weren't
	// copied: the caller isn't an admin, or the clone crosses kinds
	// without copy_values.
	Skipped []VariableRef `json:"skipped" doc:"Variables not created because their values weren't copied (needs admin, and copy_values across kinds); set them in the new environment"`
	// CopiedAcrossKinds are literal values copied from an environment of
	// the other kind, with copy_values; SourceKind says which kind.
	CopiedAcrossKinds []VariableRef   `json:"copied_across_kinds" doc:"Literal values copied from an environment of the other kind (copy_values)"`
	SourceKind        EnvironmentKind `json:"source_kind" enum:"production,non_production"`
	Volumes           int             `json:"volumes" doc:"New, empty volumes, staged until each service's first deploy"`
	// NotCopied names what a clone never copies, with counts, e.g.
	// "2 domains".
	NotCopied []string `json:"not_copied"`
	// Deploy is the promotion that deploys the source's images (with
	// deploy only); DeployError says why it didn't start.
	Deploy      *PromoteResult `json:"deploy,omitempty"`
	DeployError string         `json:"deploy_error,omitempty"`
}

// String is the variable as a reference names it: web.KEY or shared.KEY.
func (v VariableRef) String() string {
	if v.Service == "" {
		return "shared." + v.Key
	}
	return v.Service + "." + v.Key
}
