package apitypes

// ErrorCode is a machine-readable error category. API errors carry one in
// Error.Code; failed releases carry one in Release.ErrorCode. The CLI maps
// them to exit codes, so values never change meaning.
type ErrorCode string

// API error codes.
const (
	CodeValidation   ErrorCode = "validation"
	CodeUnauthorized ErrorCode = "unauthorized"
	CodeForbidden    ErrorCode = "forbidden"
	CodeNotFound     ErrorCode = "not_found"
	CodeConflict     ErrorCode = "conflict"
	// CodeDeployInProgress means another deploy of the service is running.
	CodeDeployInProgress ErrorCode = "deploy_in_progress"
	// CodeHostnameTaken means the hostname belongs to another service.
	CodeHostnameTaken ErrorCode = "hostname_taken"
	CodeTooLarge      ErrorCode = "too_large"
	// CodeRateLimited means too many attempts (e.g. logins); wait and retry.
	CodeRateLimited ErrorCode = "rate_limited"
	// CodeServerOffline means the server an operation needs isn't
	// connected (HTTP 503).
	CodeServerOffline ErrorCode = "server_offline"
	// CodeDenied means a user denied a browser login (`dock login`).
	CodeDenied ErrorCode = "denied"
	// CodeExpired means a browser login expired or was already used.
	CodeExpired ErrorCode = "expired"
	// CodeEnvironmentLocked means the environment is locked and the
	// caller is a token (ADR 0044); Error.Request names the access request
	// that waits for an admin's approval.
	CodeEnvironmentLocked ErrorCode = "environment_locked"
	// CodeReauthRequired means a sensitive action needs the person to
	// confirm who they are again (POST /me/reauth), then retry.
	CodeReauthRequired ErrorCode = "reauth_required"
	// CodeTwoFactorRequired means the org (or the platform, for operators)
	// requires a second factor this session or token didn't use.
	CodeTwoFactorRequired ErrorCode = "two_factor_required"
	// CodeSSORequired means a session that signed in through an identity
	// provider confirms who it is by signing in through it again (ADR
	// 0065).
	CodeSSORequired ErrorCode = "sso_required"
	CodeInternal    ErrorCode = "internal"
)

// Release failure codes (Release.ErrorCode). Release.Error has the details.
const (
	// CodeBuildFailed: the image could not be built or pulled.
	CodeBuildFailed ErrorCode = "build_failed"
	// CodeHealthCheckFailed: the new container never became healthy.
	CodeHealthCheckFailed ErrorCode = "health_check_failed"
	// CodeDeployFailed: anything else (container start, routing, the daemon
	// stopping mid-deploy).
	CodeDeployFailed ErrorCode = "deploy_failed"
)

// Error is an RFC 9457 (7807) problem response with a machine-readable code.
type Error struct {
	Type   string        `json:"type,omitempty" doc:"URI identifying the problem type"`
	Title  string        `json:"title" example:"Forbidden"`
	Status int           `json:"status" example:"403"`
	Detail string        `json:"detail" example:"this token has scope deploy; archiving a service requires admin"`
	Code   ErrorCode     `json:"code" example:"forbidden"`
	Errors []ErrorDetail `json:"errors,omitempty"`
	// Request is the access request an environment_locked error filed.
	Request int64 `json:"request,omitempty"`
	// Release is the release a deploy_in_progress error is about: the
	// one deploying, to wait for before retrying.
	Release int64 `json:"release,omitempty" doc:"With deploy_in_progress: the release that is deploying; wait for it, then retry"`
	// Refusal says why a forbidden call was refused and how it could be
	// allowed (ADR 0080).
	Refusal *Refusal `json:"refusal,omitempty" doc:"With forbidden: why the call was refused, and who or what can allow it"`
}

// RefusalCause is what refused a call: the thing to change to allow it.
type RefusalCause string

// Refusal causes. More come with the permission model of ADR 0080.
const (
	// RefusedRole: the person's role in the org is too low.
	RefusedRole RefusalCause = "role"
	// RefusedScope: the token's scope is too low; scopes don't change.
	RefusedScope RefusalCause = "scope"
	// RefusedLimit: the token is limited to another project or
	// environment, or to one where the call needs more.
	RefusedLimit RefusalCause = "limit"
	// RefusedPlatform: the call runs the platform, and the caller's
	// credential or platform role doesn't allow it.
	RefusedPlatform RefusalCause = "platform"
	// RefusedPerson: only a person in a signed-in browser session may do
	// it, never a token.
	RefusedPerson RefusalCause = "person"
)

// Refusal explains a forbidden error (ADR 0080): what was asked, what
// the caller has, what decided it, and what to do next. Reason and Fix
// are full sentences for people; the rest is for programs and agents.
type Refusal struct {
	DecidedBy RefusalCause `json:"decided_by" enum:"role,scope,limit,platform,person"`
	// Action is what was refused, as the check names it ("archiving a service").
	Action string `json:"action"`
	// Need is what the action needs: a scope (read, deploy, admin), a
	// platform permission (servers.manage) or "person".
	Need string `json:"need"`
	// Actor is who was refused: `user "ola@example.com"` or `token "ci"`.
	Actor string `json:"actor"`
	// Has is the caller's scope (a person's role) in the org.
	Has Scope `json:"has,omitempty"`
	// Limit is the project or environment a token is limited to.
	Limit string `json:"limit,omitempty" example:"shop/staging"`
	// Place is where the action was asked for, when it is in a project.
	Place string `json:"place,omitempty" example:"shop/production"`
	// Reason says why, Fix what would allow it, and how.
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
	// Ask names the people who can change it, for callers acting for a
	// person: the org's admins.
	Ask []string `json:"ask,omitempty"`
}

// ErrorDetail points at one invalid input.
type ErrorDetail struct {
	Message  string `json:"message"`
	Location string `json:"location,omitempty" example:"body.name"`
}

// Error implements error.
func (e *Error) Error() string { return e.Detail }

// GetStatus returns the HTTP status (used by the server).
func (e *Error) GetStatus() int { return e.Status }

// ContentType marks JSON errors as problem documents.
func (e *Error) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}
