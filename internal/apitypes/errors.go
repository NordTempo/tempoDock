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
	CodeInternal          ErrorCode = "internal"
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
