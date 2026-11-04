package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Two-factor sign-in, step-ups and managing second factors (ADR 0049).

// verifyInput finishes a pending sign-in, from its cookie.
type verifyInput struct {
	Pending string `cookie:"dock_login"`
	Body    apitypes.Verification
	client  client
}

// Resolve records how the request arrived.
func (in *verifyInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

type reauthInput struct {
	Body   apitypes.Verification
	client client
}

// Resolve records how the request arrived.
func (in *reauthInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

func registerTwoFactor(s *Server, a huma.API) {
	public(a, huma.Operation{
		OperationID: "login-verify", Method: http.MethodPost, Path: Prefix + "/auth/login/verify",
		Summary: "Finish signing in with a second factor",
		Description: "Finishes the sign-in POST /auth/login left pending (its cookie) with an authenticator app code or a recovery code, and sets the session cookie. " +
			"A few wrong factors end the pending sign-in.",
		Tags: []string{"auth"}, Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusGone, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *verifyInput) (*loginOutput, error) {
		res, err := s.core.LoginSecondFactor(ctx, in.Pending, in.Body, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return in.client.signedIn(res), nil
	})

	authed(s, a, huma.Operation{
		OperationID: "get-two-factor", Method: http.MethodGet, Path: Prefix + "/me/two-factor",
		Summary: "Your second factors", Tags: []string{"auth"},
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.TwoFactor], error) {
		return reply(s.core.TwoFactor(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "reauth", Method: http.MethodPost, Path: Prefix + "/me/reauth",
		Summary: "Confirm it's you",
		Description: "A step-up for sensitive actions (answers with code reauth_required): with an authenticator app code, " +
			"or the password for users without a second factor. Covers the session for 15 minutes.",
		Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, actor core.Actor, in *reauthInput) (*struct{}, error) {
		return nil, s.core.Reauth(ctx, actor, in.Body, in.client.ip)
	})
	authed(s, a, huma.Operation{
		OperationID: "start-totp", Method: http.MethodPost, Path: Prefix + "/me/totp",
		Summary: "Start adding an authenticator app", Description: "Returns a new key; POST /me/totp/confirm with a code from the app keeps it.", Tags: []string{"auth"},
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.TOTPSetup], error) {
		return reply(s.core.StartTOTP(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-totp", Method: http.MethodPost, Path: Prefix + "/me/totp/confirm",
		Summary: "Add an authenticator app", Description: "Your first second factor also returns recovery codes, once, and ends your other sessions.", Tags: []string{"auth"},
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.AddTOTPRequest }) (*out[apitypes.FactorAdded], error) {
		return reply(s.core.AddTOTP(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-factor", Method: http.MethodDelete, Path: Prefix + "/me/factors/{id}",
		Summary: "Remove a second factor", Description: "Removing the last turns two-factor sign-in off.", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveFactor(ctx, actor, in.ID)
	})
	authed(s, a, huma.Operation{
		OperationID: "new-recovery-codes", Method: http.MethodPost, Path: Prefix + "/me/recovery-codes",
		Summary: "Create new recovery codes", Description: "Shown once; the old ones stop working.", Tags: []string{"auth"},
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.RecoveryCodes], error) {
		return reply(s.core.NewRecoveryCodes(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-org-two-factor", Method: http.MethodPut, Path: Prefix + "/org/two-factor",
		Summary:     "Require two-factor sign-in of members",
		Description: "Members whose sign-in used no second factor can't act in the organization until they add one. Unlimited admins; turning it on needs your own sign-in to have used one.",
		Tags:        []string{"orgs"},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.RequireTwoFactorRequest
	}) (*out[apitypes.Org], error) {
		return reply(s.core.SetOrgRequireTwoFactor(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-platform-security", Method: http.MethodGet, Path: Prefix + "/platform/security",
		Summary: "The platform's sign-in policy", Description: "Operators only.", Tags: []string{"platform"},
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.PlatformSecurity], error) {
		return reply(s.core.PlatformSecurity(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-platform-security", Method: http.MethodPut, Path: Prefix + "/platform/security",
		Summary:     "Change the platform's sign-in policy",
		Description: "Operators only. Requiring two-factor sign-in of operators needs your own sign-in to have used one.", Tags: []string{"platform"},
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.PlatformSecurity }) (*out[apitypes.PlatformSecurity], error) {
		return reply(s.core.SetPlatformSecurity(ctx, actor, in.Body))
	})
}
