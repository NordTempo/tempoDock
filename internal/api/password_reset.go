package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// registerPasswordReset registers password reset by email (ADR 0079):
// public, rate limited like logins.
func registerPasswordReset(s *Server, a huma.API) {
	tags := []string{"auth"}
	public(a, huma.Operation{
		OperationID: "ask-password-reset", Method: http.MethodPost, Path: Prefix + "/auth/password-reset",
		Summary: "Email a password reset link", Tags: tags,
		Description:   "Emails a link that sets a new password to the account with this email, if there is one. The answer is the same either way. 403 while passwords don't sign in or Dock can't email links (GET /auth/methods says `password_reset`). Rate limited like logins.",
		DefaultStatus: http.StatusAccepted,
		Errors:        []int{http.StatusForbidden, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *resetAskInput) (*struct{}, error) {
		if err := s.core.RequestPasswordReset(ctx, in.Body.Email, in.client.ip); err != nil {
			return nil, s.problem(err)
		}
		return nil, nil
	})
	public(a, huma.Operation{
		OperationID: "get-password-reset", Method: http.MethodGet, Path: Prefix + "/auth/password-reset/{code}",
		Summary: "Look at a password reset link", Tags: tags,
		Description: "Whose password a reset link sets, while it works. A link that was used, replaced or is past its hour is 404. Rate limited per client IP.",
		Errors:      []int{http.StatusNotFound, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *resetCodeInput) (*out[apitypes.PasswordResetInfo], error) {
		info, err := s.core.PasswordReset(ctx, in.Code, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return reply(info, nil)
	})
	public(a, huma.Operation{
		OperationID: "reset-password-with-link", Method: http.MethodPost, Path: Prefix + "/auth/password-reset/{code}",
		Summary: "Set a new password with a reset link", Tags: tags,
		Description:   "Sets the account's password (at least 12 characters), spends the link and ends every session and `dock login` token of the account; second factors stay, except on an account made from an invite whose email this link proves for the first time: what was set up on it before (second factors, recovery codes, tokens) is removed. It doesn't sign in. Rate limited per client IP.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *resetSetInput) (*struct{}, error) {
		if err := s.core.ResetPasswordWithLink(ctx, in.Code, in.Body, in.client.ip); err != nil {
			return nil, s.problem(err)
		}
		return nil, nil
	})
}

type resetAskInput struct {
	Body   apitypes.PasswordResetAsk
	client client
}

// Resolve records how the request arrived.
func (in *resetAskInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

type resetCodeInput struct {
	Code   string `path:"code" maxLength:"200"`
	client client
}

// Resolve records how the request arrived.
func (in *resetCodeInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

type resetSetInput struct {
	Code   string `path:"code" maxLength:"200"`
	Body   apitypes.PasswordResetRequest
	client client
}

// Resolve records how the request arrived.
func (in *resetSetInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}
