package apitypes

import "time"

// CLILoginStartRequest starts a browser login for the CLI (`dock login`).
// The fields only describe the client to the user who approves it.
type CLILoginStartRequest struct {
	ClientName string `json:"client_name" minLength:"1" maxLength:"64" example:"dock CLI"`
	Hostname   string `json:"hostname" maxLength:"255" example:"ada-laptop"`
	OS         string `json:"os" maxLength:"64" example:"darwin/arm64"`
}

// CLILoginStart is a pending browser login. The user opens VerificationURL
// and approves UserCode; the CLI collects its token with DeviceCode.
type CLILoginStart struct {
	UserCode        string    `json:"user_code" example:"BCDF-GHJK"`
	VerificationURL string    `json:"verification_url" example:"https://dock.example.com/cli-login?code=BCDF-GHJK"`
	DeviceCode      string    `json:"device_code" doc:"Secret: only the CLI that started the login may know it"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// CLILoginWaitRequest collects a browser login's token once approved.
type CLILoginWaitRequest struct {
	DeviceCode string `json:"device_code" minLength:"1" maxLength:"128"`
}

// CLILoginWait answers a wait: still pending (HTTP 202, ask again), or
// approved with the new token (HTTP 200, returned exactly once).
type CLILoginWait struct {
	Status string `json:"status" enum:"pending,approved"`
	Token  string `json:"token,omitempty" doc:"The new API token's secret; only when approved"`
	User   string `json:"user,omitempty" doc:"Email of the user who approved"`
	Scope  Scope  `json:"scope,omitempty"`
}

// CLILogin is a browser login as the approval page shows it.
type CLILogin struct {
	UserCode   string    `json:"user_code" example:"BCDF-GHJK"`
	ClientName string    `json:"client_name"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	IP         string    `json:"ip" doc:"Address the CLI started the login from"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Status     string    `json:"status" enum:"pending,approved,denied"`
	// Scope is what the CLI gets when the caller approves: their own role.
	Scope     Scope  `json:"scope" enum:"read,deploy,admin"`
	TokenName string `json:"token_name" example:"cli: ada-laptop"`
	TokenDays int    `json:"token_days" doc:"Days until the token expires"`
}

// CLILoginDecision approves or denies a browser login by its user code.
type CLILoginDecision struct {
	UserCode string `json:"user_code" minLength:"1" maxLength:"16" example:"BCDF-GHJK"`
}
