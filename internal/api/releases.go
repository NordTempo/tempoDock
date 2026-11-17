package api

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

const (
	releasesPath = servicePathT + "/releases"
	releasePathT = releasesPath + "/{number}"
)

// ReleasePath addresses a release by number.
type ReleasePath struct {
	ServicePath
	Number int64 `path:"number" minimum:"1" doc:"Release number"`
}

// sourceInput keeps a reference to the request body so the handler can
// stream it to disk after the permission check. Huma doesn't read bodies
// for inputs without a Body field.
type sourceInput struct {
	ServicePath
	body io.Reader
}

// Resolve captures the raw request body, read with uploadIdle between
// its bytes at most.
func (in *sourceInput) Resolve(ctx huma.Context) []error {
	_, w := humago.Unwrap(ctx)
	in.body = &idleBody{r: ctx.BodyReader(), rc: http.NewResponseController(w)}
	return nil
}

// uploadIdle is how long a source upload may send nothing before it is
// cut. An upload holds one of the server's few upload places for as long
// as its client keeps the connection open, and nothing else bounds a
// body: without this a connection that sends a byte and waits holds its
// place for good. A variable so tests can shorten it.
var uploadIdle = time.Minute

// idleBody reads a request's body with a deadline uploadIdle past each
// read: the connection's own read deadline, moved along as bytes come,
// so a slow upload goes on and a silent one ends. The deadline is the
// connection's (no timer of Dock's), and the server clears it before the
// connection's next request.
type idleBody struct {
	r  io.Reader
	rc *http.ResponseController
}

func (b *idleBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(uploadIdle))
	return b.r.Read(p)
}

func registerReleases(s *Server, a huma.API) {
	tags := []string{"releases"}
	authed(s, a, huma.Operation{
		OperationID: "list-releases", Method: http.MethodGet, Path: releasesPath,
		Summary: "List releases", Description: "Newest first.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Limit int `query:"limit" default:"20" minimum:"1" maximum:"100"`
	}) (*out[[]apitypes.Release], error) {
		return reply(s.core.ListReleases(ctx, actor, in.ref(), in.Limit))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-release", Method: http.MethodGet, Path: releasePathT,
		Summary: "Get a release", Description: "Includes its progress events.", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ReleasePath) (*out[apitypes.Release], error) {
		return reply(s.core.GetRelease(ctx, actor, in.ref(), in.Number))
	})
	authed(s, a, huma.Operation{
		OperationID: "deploy-image", Method: http.MethodPost, Path: releasesPath,
		Summary: "Deploy an image", Description: "Queues a release from a prebuilt image, taking the service's staged changes with it. Returns immediately; follow `/events` or poll the release. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.DeployImageRequest
	}) (*out[apitypes.Release], error) {
		return reply(s.core.DeployImage(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "deploy-source", Method: http.MethodPost, Path: releasesPath + "/source",
		// The upload streams; it can't run again from JSON.
		Metadata: map[string]any{"replay": false},
		Summary:  "Deploy source", Description: "Upload a build context as a tar (optionally gzipped) stream. It is streamed to disk, built on this server, and deployed. A `dock.toml` at its root overrides build and deploy settings. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusAccepted,
		Errors: []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge},
		RequestBody: &huma.RequestBody{
			Required: true,
			Content: map[string]*huma.MediaType{
				"application/x-tar": {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}},
				"application/gzip":  {Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}},
			},
		},
	}, func(ctx context.Context, actor core.Actor, in *sourceInput) (*out[apitypes.Release], error) {
		return reply(s.core.DeploySource(ctx, actor, in.ref(), in.body, s.opts.MaxUploadBytes))
	})
	authed(s, a, huma.Operation{
		OperationID: "rollback", Method: http.MethodPost, Path: releasePathT + "/rollback",
		Summary: "Roll back to a release", Description: "Creates a new release with that release's image and resolved config, through the same health-checked path. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, actor core.Actor, in *ReleasePath) (*out[apitypes.Release], error) {
		return reply(s.core.Rollback(ctx, actor, in.ref(), in.Number))
	})
	authed(s, a, huma.Operation{
		OperationID: "cancel-release", Method: http.MethodPost, Path: releasePathT + "/cancel",
		Summary: "Cancel a deploy", Description: "Stops a release that is deploying, at any step before traffic moves to it (ADR 0086). What it started is removed, it reads as canceled, the release that served keeps serving, and the changes it carried are staged again. Answers once it is canceled, with the release. 409 if it already finished or is switching traffic. Scope: deploy.",
		Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *ReleasePath) (*out[apitypes.Release], error) {
		return reply(s.core.CancelRelease(ctx, actor, in.ref(), in.Number))
	})
	authed(s, a, huma.Operation{
		OperationID: "restart", Method: http.MethodPost, Path: servicePathT + "/restart",
		Summary: "Restart a service", Description: "Creates a new release identical to the active one. Scope: deploy.",
		Tags: tags, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.Release], error) {
		return reply(s.core.Restart(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-build-log", Method: http.MethodGet, Path: releasePathT + "/build-log",
		Summary: "Build log", Description: "Build (or image pull) output as plain text. Follow it live with `/build-log/stream`.",
		Tags: tags,
		Responses: map[string]*huma.Response{"200": {
			Description: "Build log",
			Content:     map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: huma.TypeString}}},
		}},
	}, func(ctx context.Context, actor core.Actor, in *ReleasePath) (*huma.StreamResponse, error) {
		f, err := s.core.BuildLog(ctx, actor, in.ref(), in.Number)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			defer func() { _ = f.Close() }()
			hctx.SetHeader("Content-Type", "text/plain; charset=utf-8")
			hctx.SetStatus(http.StatusOK)
			_, _ = io.Copy(hctx.BodyWriter(), f)
		}}, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "promote", Method: http.MethodPost, Path: projectPathT + "/promote",
		Summary: "Promote between environments", Tags: tags, DefaultStatus: http.StatusAccepted, Errors: withOffline,
		Description: "Queues a release in `to` for each of `from`'s active releases (all services, or the listed ones), with the same image by digest and `to`'s own variables and settings. They deploy one at a time and stop at the first failure. Services missing in `to` are skipped and listed. Returns immediately; follow `/events` or poll the releases. Scope: deploy on `to`.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ProjectPath
		Body apitypes.PromoteRequest
	}) (*out[apitypes.PromoteResult], error) {
		return reply(s.core.Promote(ctx, actor, in.Project, in.Body))
	})
}
