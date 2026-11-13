package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerEmail registers Dock's own email (ADR 0079): the platform's
// SMTP server, a test mail and the outbox's latest mail. Operators only.
func registerEmail(s *Server, a huma.API) {
	tags := []string{"email"}
	authed(s, a, huma.Operation{
		OperationID: "get-email", Method: http.MethodGet, Path: Prefix + "/platform/email",
		Summary: "Show Dock's email settings", Tags: tags,
		Description: "The SMTP server Dock sends invites and other mail through, without its password, and how sending goes: the latest attempt's outcome and the outbox. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.EmailSettings], error) {
		return reply(s.core.EmailSettings(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-email", Method: http.MethodPut, Path: Prefix + "/platform/email",
		Summary: "Set Dock's SMTP server", Tags: tags,
		Description: "Sets the SMTP server every mail Dock sends goes through. The password is stored encrypted and never returned; leave it out to keep the stored one. Mail waiting for a retry is tried again at once. Operators only, recently confirmed.",
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.SetEmailRequest }) (*out[apitypes.EmailSettings], error) {
		return reply(s.core.SetEmail(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-email", Method: http.MethodDelete, Path: Prefix + "/platform/email",
		Summary: "Turn Dock's email off", Tags: tags,
		Description: "Removes the SMTP server; mail still queued is given up on, and invites show their link to copy instead. Operators only, recently confirmed.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.EmailSettings], error) {
		return reply(s.core.RemoveEmail(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "test-email", Method: http.MethodPost, Path: Prefix + "/platform/email/test",
		Summary: "Send a test email", Tags: tags,
		Description: "Sends a test mail now, to `to` or your own address, and says how it went. Operators only.",
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body *apitypes.TestEmailRequest }) (*out[apitypes.EmailTest], error) {
		var req apitypes.TestEmailRequest
		if in.Body != nil {
			req = *in.Body
		}
		return reply(s.core.TestEmail(ctx, actor, req))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-email", Method: http.MethodGet, Path: Prefix + "/platform/email/outbox",
		Summary: "The mail Dock sent", Tags: tags,
		Description: "The latest mails, newest first, without their bodies: what, to whom, and how it went. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Limit int `query:"limit" minimum:"0" maximum:"200" doc:"Default 50"`
	}) (*out[apitypes.EmailLog], error) {
		return reply(s.core.EmailLog(ctx, actor, in.Limit))
	})
}
