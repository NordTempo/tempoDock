package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// maxRequestWait caps one wait for an access request, under the server's
// 40 s write timeout; `dock request wait` asks again.
const maxRequestWait = 30

// RequestPath addresses an access request.
type RequestPath struct {
	ID int64 `path:"id" minimum:"1" doc:"Access request number"`
}

// registerRequests registers access requests (ADR 0044): the inbox, one
// request, waiting for one, and deciding.
func registerRequests(s *Server, a huma.API) {
	tags := []string{"requests"}
	base := Prefix + "/requests"
	authed(s, a, huma.Operation{
		OperationID: "list-requests", Method: http.MethodGet, Path: base,
		Summary:     "List access requests",
		Description: "Newest first. Admins see their organization's; a token sees the ones it filed.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Status apitypes.AccessRequestStatus `query:"status" enum:"pending,approved,rejected,expired,done,failed" doc:"Only requests with this status"`
		Limit  int                          `query:"limit" default:"50" minimum:"1" maximum:"200"`
	}) (*out[[]apitypes.AccessRequest], error) {
		return reply(s.core.ListRequests(ctx, actor, core.RequestFilter{Status: in.Status, Limit: in.Limit}))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-request", Method: http.MethodGet, Path: base + "/{id}",
		Summary:     "Get an access request",
		Description: "With `wait` (seconds, at most 30), answers once the request is decided and has run, or when the wait is over. Admins get its arguments and how many changes its environment had since.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		RequestPath
		Wait int `query:"wait" minimum:"0" maximum:"30" doc:"Seconds to wait for a decision"`
	}) (*out[apitypes.AccessRequest], error) {
		if in.Wait > 0 {
			return reply(s.core.WaitRequest(ctx, actor, in.ID, time.Duration(min(in.Wait, maxRequestWait))*time.Second))
		}
		return reply(s.core.GetRequest(ctx, actor, in.ID))
	})
	authed(s, a, huma.Operation{
		OperationID: "check-request", Method: http.MethodPost, Path: base + "/{id}/check",
		Summary:     "Check an access request",
		Description: "Whether approving would run now, against live state, without running it: the call goes through its own checks as the token that asked and stops at the environment's lock. `reason` says why it would fail, `service` names the service where that is fixed. Admins in a UI session only.", Tags: tags,
		// Checking is never itself a request.
		Metadata: map[string]any{"replay": false},
	}, func(ctx context.Context, actor core.Actor, in *RequestPath) (*out[apitypes.AccessRequestCheck], error) {
		return reply(s.core.CheckRequest(ctx, actor, in.ID))
	})
	for _, approve := range []bool{true, false} {
		id, summary, desc := "reject-request", "Reject an access request", "Nothing runs. Admins in a UI session only."
		if approve {
			id, summary, desc = "approve-request", "Approve an access request",
				"Runs the call as the token that asked, through every check it would pass now, with the environment's lock lifted for that call. "+
					"Answers within seconds with the outcome, or while it still runs. Admins in a UI session only."
		}
		authed(s, a, huma.Operation{
			OperationID: id, Method: http.MethodPost, Path: base + "/{id}/" + map[bool]string{true: "approve", false: "reject"}[approve],
			Summary: summary, Description: desc, Tags: tags,
			// Deciding is never itself a request.
			Metadata: map[string]any{"replay": false},
		}, func(ctx context.Context, actor core.Actor, in *struct {
			RequestPath
			Body apitypes.DecideAccessRequest
		}) (*out[apitypes.AccessRequest], error) {
			return reply(s.core.DecideRequest(ctx, actor, in.ID, approve, in.Body))
		})
	}
}
