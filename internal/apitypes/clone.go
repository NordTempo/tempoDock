package apitypes

// CloneEnvironmentRequest copies an environment into a new one of the same
// project (`dock environment clone staging staging-2`, ADR 0022).
type CloneEnvironmentRequest struct {
	Name string          `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"The new environment's DNS-safe name"`
	Kind EnvironmentKind `json:"kind,omitempty" enum:"production,non_production" doc:"Default: production for an environment named production, otherwise non_production. Never changes."`
	// Deploy also deploys the images running in the source, by digest,
	// through promote's path. Without it nothing is deployed.
	Deploy bool `json:"deploy,omitempty" doc:"Also deploy the images serving in the source, by digest, like promote"`
}

// CloneResult says what a clone created, what it set anew and what it
// left out.
type CloneResult struct {
	From        string      `json:"from"`
	Environment Environment `json:"environment"`
	Services    []string    `json:"services" doc:"Cloned services, by name"`
	Variables   int         `json:"variables" doc:"Variables created with the source's value"`
	// Generated are template variables the caller can't read, set anew: a
	// new password and the template's defaults.
	Generated []VariableRef `json:"generated" doc:"Template variables set anew (new password, template defaults) because copying needs admin"`
	// Skipped are variables left out: copying their value needs admin.
	Skipped []VariableRef `json:"skipped" doc:"Variables not created because copying their value needs admin; set them in the new environment"`
	Volumes int           `json:"volumes" doc:"New, empty volumes, staged until each service's first deploy"`
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
