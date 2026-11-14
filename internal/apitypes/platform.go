package apitypes

// The platform apart (ADR 0080): running Dock itself is a platform role,
// never an org's permission, with its own credentials.

// PlatformRole is a user's or a platform token's role on the platform.
type PlatformRole string

// Platform roles. Built in; the platform has no custom roles.
const (
	// PlatformOperator runs the platform: servers, accounts, updates,
	// backups, the registry, the platform's settings, alerts and orgs.
	PlatformOperator PlatformRole = "operator"
	// PlatformViewer reads the console and changes nothing.
	PlatformViewer PlatformRole = "viewer"
)

// SetPlatformRoleRequest gives a user a platform role, or takes it away
// (none).
type SetPlatformRoleRequest struct {
	Role string `json:"role" enum:"operator,viewer,none"`
}

// CreatePlatformTokenRequest makes a platform token: it runs the platform
// with Role and never acts in an organization.
type CreatePlatformTokenRequest struct {
	Name          string       `json:"name" minLength:"1" maxLength:"100"`
	Role          PlatformRole `json:"role" enum:"operator,viewer"`
	ExpiresInDays int          `json:"expires_in_days,omitempty" minimum:"0" doc:"0 means never"`
}

// SplitTokenResult is a legacy token split in two: Token stays the org's
// (no longer the platform's), Platform is the new platform token with its
// secret, shown once.
type SplitTokenResult struct {
	Token    Token        `json:"token"`
	Platform CreatedToken `json:"platform"`
}
