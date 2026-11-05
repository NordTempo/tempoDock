package apitypes

import "time"

// Two-factor sign-in (ADR 0057): authenticator apps as second factors,
// recovery codes, step-up re-authentication.

// LoginResult answers a password check: signed in (Me), or a second factor
// is needed (SecondFactor), finished at POST /auth/login/verify.
type LoginResult struct {
	Me *Me `json:"me,omitempty"`
	// SecondFactor lists the factors the user can finish with: totp,
	// recovery. Set when the password was right and one is needed.
	SecondFactor []string `json:"second_factor,omitempty" enum:"totp,recovery"`
}

// Verification proves who the caller is: the second factor of a sign-in,
// or a step-up before a sensitive action. Exactly one field is set.
// Password is for step-ups by users without a second factor.
type Verification struct {
	Password string `json:"password,omitempty"`
	// Code is a 6-digit code from an authenticator app.
	Code string `json:"code,omitempty"`
	// RecoveryCode is a one-time recovery code (sign-in only).
	RecoveryCode string `json:"recovery_code,omitempty"`
}

// Factor is one of a user's second factors.
type Factor struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind" enum:"totp"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// TwoFactor is the caller's two-factor setup.
type TwoFactor struct {
	Factors []Factor `json:"factors"`
	// RecoveryCodes counts the unused recovery codes.
	RecoveryCodes int `json:"recovery_codes"`
	// ReauthUntil is when the session's last verification stops covering
	// sensitive actions; absent when it already doesn't.
	ReauthUntil *time.Time `json:"reauth_until,omitempty"`
}

// TOTPSetup is an authenticator app key waiting for its first code.
type TOTPSetup struct {
	Ceremony string `json:"ceremony"`
	// Secret is the key in base32, for typing in; URI is the otpauth://
	// link a QR code carries.
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// AddTOTPRequest confirms an authenticator app with a code from it.
type AddTOTPRequest struct {
	Ceremony string `json:"ceremony" minLength:"1"`
	Code     string `json:"code" minLength:"6" maxLength:"8"`
}

// FactorAdded is a new second factor. RecoveryCodes are set, once, when it
// is the user's first: store them somewhere safe.
type FactorAdded struct {
	Factor        Factor   `json:"factor"`
	RecoveryCodes []string `json:"recovery_codes,omitempty"`
}

// RecoveryCodes are new one-time recovery codes, shown once; the old
// ones stop working.
type RecoveryCodes struct {
	Codes []string `json:"codes"`
}

// RequireTwoFactorRequest turns an org's (or the platform's operators')
// two-factor requirement on or off.
type RequireTwoFactorRequest struct {
	Require bool `json:"require"`
}

// PlatformSecurity is the platform's sign-in policy.
type PlatformSecurity struct {
	// OperatorsTwoFactor: operators run the platform only after signing
	// in with a second factor.
	OperatorsTwoFactor bool `json:"operators_two_factor"`
}
