package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

const gitPath = servicePathT + "/git"

// registerGit adds a service's GitHub push-to-deploy source (ADR 0018).
func registerGit(s *Server, a huma.API) {
	tags := []string{"git"}
	authed(s, a, huma.Operation{
		OperationID: "get-git-source", Method: http.MethodGet, Path: gitPath,
		Summary: "Get the GitHub source", Tags: tags,
		Description: "The connected repository, branch and directory, the webhook URL, a queued push and what the latest delivery did. The webhook secret and token are never returned (`token_set` only).",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.GitSource], error) {
		return reply(s.core.GitStatus(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "connect-git", Method: http.MethodPut, Path: gitPath,
		Summary: "Connect a GitHub repository", Tags: tags,
		Description: "Pushes to the branch then deploy the service from source, like `dock deploy` of that directory. Creates the connection or changes it. A new connection (or `rotate_secret`) returns the webhook secret once in `secret`. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.GitConnectRequest
	}) (*out[apitypes.GitConnectResult], error) {
		return reply(s.core.GitConnect(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "disconnect-git", Method: http.MethodDelete, Path: gitPath,
		Summary: "Disconnect the GitHub repository", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Removes the connection, its token and any queued push; the webhook stops working. Releases are kept. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*struct{}, error) {
		return nil, s.core.GitDisconnect(ctx, actor, in.ref())
	})
	authed(s, a, huma.Operation{
		OperationID: "deploy-git", Method: http.MethodPost, Path: gitPath + "/deploy",
		Summary: "Deploy a commit from GitHub", Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Queues a commit (default: the branch's head) as if it had been pushed, even if it is already live. Its release starts once no other deploy of the service runs; follow `/events`. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Body apitypes.GitDeployRequest
	}) (*out[apitypes.GitDeployResult], error) {
		return reply(s.core.GitDeploy(ctx, actor, in.ref(), in.Body))
	})
}

// maxHookBody caps a webhook delivery. GitHub's push payloads are a few
// KB; ones listing thousands of changed files can reach a few MB.
const maxHookBody = 4 << 20

// maxHookReads bounds deliveries being read at once, so concurrent large
// bodies can't add up to more than maxHookReads × maxHookBody of memory.
const maxHookReads = 2

// Hooks returns the handler for [hooks] listen, the loopback listener the
// control plane's Caddy proxies https://<public_host>/hooks/github/ to
// (ADR 0018). It serves only:
//
//	POST /hooks/github/{id}  a GitHub webhook delivery (core.GitWebhook)
//
// Answers are plain text, which GitHub shows in the delivery log.
func Hooks(c *core.Core, log *slog.Logger) http.Handler {
	sem := make(chan struct{}, maxHookReads)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/github/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-r.Context().Done():
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
		if err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				http.Error(w, "payload larger than 4 MB", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "could not read the payload", http.StatusBadRequest)
			return
		}
		msg, err := c.GitWebhook(r.Context(), core.GitDelivery{
			HookID: r.PathValue("id"), Event: r.Header.Get("X-GitHub-Event"), ID: r.Header.Get("X-GitHub-Delivery"),
			Signature: r.Header.Get("X-Hub-Signature-256"), Body: body,
		})
		if err != nil {
			var ce *core.Error
			if !errors.As(err, &ce) {
				log.Error("github webhook", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			http.Error(w, ce.Msg, statusForCode(ce.Code))
			return
		}
		_, _ = io.WriteString(w, msg+"\n")
	})
	return mux
}
