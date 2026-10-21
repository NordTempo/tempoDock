package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// Browser login for the CLI (see internal/core/cli_login.go). Start and
// wait are public: the CLI has no credentials yet, and its device code is
// what proves it started the login. Looking a login up and deciding it
// need a signed-in user; the CSRF guard covers approve and deny.

type cliStartInput struct {
	Body   apitypes.CLILoginStartRequest
	client client
	host   string
}

// Resolve records how the request arrived, for the rate limit and the
// verification URL.
func (in *cliStartInput) Resolve(ctx huma.Context) []error {
	in.client, in.host = clientOf(ctx), ctx.Host()
	return nil
}

type cliWaitOutput struct {
	Status int
	Body   apitypes.CLILoginWait
}

type cliLookupInput struct {
	Code   string `path:"code" maxLength:"16" example:"BCDF-GHJK"`
	client client
}

// Resolve records the client IP, for the wrong-code lockout.
func (in *cliLookupInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

type cliDecisionInput struct {
	Body   apitypes.CLILoginDecision
	client client
}

// Resolve records the client IP, for the wrong-code lockout.
func (in *cliDecisionInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

func registerCLILogin(s *Server, a huma.API) {
	tags := []string{"auth"}
	public(a, huma.Operation{
		OperationID: "start-cli-login", Method: http.MethodPost, Path: Prefix + "/auth/cli",
		Summary: "Start a browser login for the CLI",
		Description: "Returns a user code and the page where a signed-in user approves it, plus the device code the CLI " +
			"waits with. Expires in 10 minutes. Rate limited per client IP.",
		Tags: tags, DefaultStatus: http.StatusCreated, Errors: []int{http.StatusBadRequest, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *cliStartInput) (*out[apitypes.CLILoginStart], error) {
		res, err := s.core.StartCLILogin(ctx, in.Body, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		page := url.URL{Scheme: "http", Host: in.host, Path: "/cli-login", RawQuery: url.Values{"code": {res.UserCode}}.Encode()}
		if in.client.secure {
			page.Scheme = "https"
		}
		res.VerificationURL = page.String()
		return &out[apitypes.CLILoginStart]{Body: res}, nil
	})
	public(a, huma.Operation{
		OperationID: "wait-cli-login", Method: http.MethodPost, Path: Prefix + "/auth/cli/wait",
		Summary: "Wait for a browser login to be approved",
		Description: "Long poll: answers as soon as the login is decided, or after about 30 seconds with 202 and status " +
			"pending (ask again). Once approved, returns the new API token exactly once. 403 `denied` if denied, " +
			"410 `expired` if expired or already collected.",
		Tags: tags, Errors: []int{http.StatusBadRequest, http.StatusForbidden, http.StatusGone},
	}, func(ctx context.Context, in *struct{ Body apitypes.CLILoginWaitRequest }) (*cliWaitOutput, error) {
		res, err := s.core.WaitCLILogin(ctx, in.Body.DeviceCode)
		if err != nil {
			return nil, s.problem(err)
		}
		status := http.StatusOK
		if res.Status == "pending" {
			status = http.StatusAccepted
		}
		return &cliWaitOutput{Status: status, Body: res}, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "get-cli-login", Method: http.MethodGet, Path: Prefix + "/auth/cli/requests/{code}",
		Summary: "Show a browser login before approving it", Description: "Signed-in users only. Wrong codes lock the caller out for minutes after a few tries.",
		Tags: tags, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusTooManyRequests},
	}, func(ctx context.Context, actor core.Actor, in *cliLookupInput) (*out[apitypes.CLILogin], error) {
		return reply(s.core.CLILogin(ctx, actor, in.Code, in.client.ip))
	})
	decide := func(id, verb, summary, desc string, approve bool) {
		authed(s, a, huma.Operation{
			OperationID: id, Method: http.MethodPost, Path: Prefix + "/auth/cli/" + verb,
			Summary: summary, Description: desc, Tags: tags, DefaultStatus: http.StatusNoContent,
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusTooManyRequests},
		}, func(ctx context.Context, actor core.Actor, in *cliDecisionInput) (*struct{}, error) {
			return nil, s.core.DecideCLILogin(ctx, actor, in.Body.UserCode, in.client.ip, approve)
		})
	}
	decide("approve-cli-login", "approve", "Approve a browser login",
		"Signed-in users only. The CLI gets a new token of yours with your role as scope, expiring in 90 days.", true)
	decide("deny-cli-login", "deny", "Deny a browser login", "Signed-in users only.", false)
	authed(s, a, huma.Operation{
		OperationID: "revoke-own-token", Method: http.MethodDelete, Path: Prefix + "/me/token",
		Summary: "Revoke the token in use", Description: "Tokens only; `dock logout` uses it for tokens from `dock login`.",
		Tags: tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*struct{}, error) {
		return nil, s.core.RevokeOwnToken(ctx, actor)
	})
}
