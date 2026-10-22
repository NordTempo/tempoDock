package tbhttp

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// ConnPath names a connection in a path. Path types are exported: Huma
// only sees the fields of exported embedded types.
type ConnPath struct {
	Connection string `path:"connection" doc:"The connection's name"`
}

type updateConnInput struct {
	ConnPath
	Body tbtypes.UpdateConnectionRequest
}

type setPolicyInput struct {
	Body apitypes.UpdateDataPolicyRequest
}

type setConnPolicyInput struct {
	ConnPath
	Body apitypes.UpdateDataPolicyRequest
}

type auditInput struct {
	Connection string `query:"connection" doc:"Only events about this connection"`
	Action     string `query:"action" doc:"Only this action, e.g. data.query"`
	Before     int64  `query:"before" minimum:"0" doc:"Events with IDs below this, to page back"`
	Limit      int    `query:"limit" minimum:"0" maximum:"500" doc:"Default 100"`
}

func registerConnections(s *server, a huma.API) {
	tags := []string{"connections"}
	authed(s, a, huma.Operation{
		OperationID: "list-connections", Method: http.MethodGet, Path: Prefix + "/connections",
		Summary:     "List connections",
		Description: "The ones you may see, with your access on each; admins also get their open changesets. Doesn't touch the databases.",
		Tags:        tags,
	}, func(ctx context.Context, act tempobase.Actor, _ *struct{}) (*out[[]tbtypes.Connection], error) {
		return reply(s.app.ListConnections(ctx, act))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-connection", Method: http.MethodPost, Path: Prefix + "/connections",
		Summary:     "Add a connection",
		Description: "Admin only. The URL is sealed at rest and its password never comes back. The only URL parameter is sslmode.",
		Tags:        tags,
	}, func(ctx context.Context, act tempobase.Actor, in *struct {
		Body tbtypes.CreateConnectionRequest
	}) (*out[tbtypes.Connection], error) {
		return reply(s.app.CreateConnection(ctx, act, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "test-connection", Method: http.MethodPost, Path: Prefix + "/connections/test",
		Summary:     "Test a URL without saving it",
		Description: "Admin only. Logs in as the URL's user without changing anything: the server version and whether setup can create tempoBase's roles, or what failed.",
		Tags:        tags,
	}, func(ctx context.Context, act tempobase.Actor, in *struct{ Body tbtypes.TestConnectionRequest }) (*out[tbtypes.TestConnectionResult], error) {
		return reply(s.app.TestConnection(ctx, act, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-connection", Method: http.MethodGet, Path: Prefix + "/connections/{connection}",
		Summary: "Get a connection", Tags: tags,
	}, func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[tbtypes.Connection], error) {
		return reply(s.app.GetConnection(ctx, act, in.Connection))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-connection", Method: http.MethodPatch, Path: Prefix + "/connections/{connection}",
		Summary:     "Change a connection",
		Description: "Admin only. A new URL or production flag applies at once; if the URL names another database, set up data access there.",
		Tags:        tags,
	}, func(ctx context.Context, act tempobase.Actor, in *updateConnInput) (*out[tbtypes.Connection], error) {
		return reply(s.app.UpdateConnection(ctx, act, in.Connection, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "delete-connection", Method: http.MethodDelete, Path: Prefix + "/connections/{connection}",
		Summary:     "Remove a connection",
		Description: "Admin only. Deletes what tempoBase keeps about it (setup, sensitive columns, saved queries, changesets, policy, token limits) but its audit events. tempoBase's roles stay in the database.",
		Tags:        tags, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*empty, error) {
		return nil, s.app.DeleteConnection(ctx, act, in.Connection)
	})
}

func registerPolicy(s *server, a huma.API) {
	tags := []string{"policy"}
	authed(s, a, huma.Operation{
		OperationID: "get-data-policy", Method: http.MethodGet, Path: Prefix + "/data-policy",
		Summary:     "Get the default data access policy",
		Description: "Subjects role:read and role:deploy are viewers and editors. Effective shows what applies per subject and kind.",
		Tags:        tags,
	}, func(ctx context.Context, act tempobase.Actor, _ *struct{}) (*out[apitypes.DataPolicy], error) {
		return reply(s.app.GetDataPolicy(ctx, act, ""))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-data-policy", Method: http.MethodPut, Path: Prefix + "/data-policy",
		Summary: "Replace the default data access policy", Description: "Admin only.", Tags: tags,
	}, func(ctx context.Context, act tempobase.Actor, in *setPolicyInput) (*out[apitypes.DataPolicy], error) {
		return reply(s.app.SetDataPolicy(ctx, act, "", in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-connection-data-policy", Method: http.MethodGet, Path: Prefix + "/connections/{connection}/data-policy",
		Summary: "Get a connection's data access policy", Tags: tags,
	}, func(ctx context.Context, act tempobase.Actor, in *ConnPath) (*out[apitypes.DataPolicy], error) {
		return reply(s.app.GetDataPolicy(ctx, act, in.Connection))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-connection-data-policy", Method: http.MethodPut, Path: Prefix + "/connections/{connection}/data-policy",
		Summary:     "Replace a connection's data access policy",
		Description: "Admin only. Rules may only name the connection's kind (production or non_production).",
		Tags:        tags,
	}, func(ctx context.Context, act tempobase.Actor, in *setConnPolicyInput) (*out[apitypes.DataPolicy], error) {
		return reply(s.app.SetDataPolicy(ctx, act, in.Connection, in.Body))
	})
}

func registerAudit(s *server, a huma.API) {
	tags := []string{"audit"}
	authed(s, a, huma.Operation{
		OperationID: "list-audit", Method: http.MethodGet, Path: Prefix + "/audit",
		Summary: "Read the audit log", Description: "Admin only. Newest first.", Tags: tags,
	}, func(ctx context.Context, act tempobase.Actor, in *auditInput) (*out[[]tbtypes.AuditEvent], error) {
		return reply(s.app.ListAudit(ctx, act, tempobase.AuditQuery{Connection: in.Connection, Action: in.Action, Before: in.Before, Limit: in.Limit}))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-audit-detail", Method: http.MethodGet, Path: Prefix + "/audit/{id}/detail",
		Summary: "The SQL of a data.query or data.commit event", Description: "Admin only.", Tags: tags,
	}, func(ctx context.Context, act tempobase.Actor, in *struct {
		ID int64 `path:"id" doc:"The event's detail_id"`
	}) (*out[apitypes.AuditDetail], error) {
		return reply(s.app.GetAuditDetail(ctx, act, in.ID))
	})
}
