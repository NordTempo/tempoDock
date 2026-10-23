package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

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

// hookReadTimeout bounds reading a delivery's body. GitHub gives up on a
// delivery after 10 seconds.
const hookReadTimeout = 15 * time.Second

// maxHookReads bounds deliveries being read at once, so concurrent large
// bodies can't add up to more than maxHookReads × maxHookBody of memory.
const maxHookReads = 2

// hooksPath is the prefix of every webhook URL.
const hooksPath = "/hooks/github/"

// Hooks returns the webhook handler: for [hooks] listen, the loopback
// listener the control plane's Caddy proxies https://<public_host>/hooks/github/
// to (ADR 0018), and for the same paths on Dock's public address (Public).
// Build it once and share it, so both share the read slots. It serves only:
//
//	POST /hooks/github/{id}  a delivery to a connection's webhook, or to the
//	                         GitHub App's (ADR 0030) (core.GitWebhook)
//
// Answers are plain text, which GitHub shows in the delivery log. The
// webhook must exist, and the body fit, before a read slot is taken: the
// listener is public, and slots held by strangers would stall deliveries.
// A slot is held for at most hookReadTimeout, also on the public listener,
// which has no read timeout of its own.
func Hooks(c *core.Core, log *slog.Logger) http.Handler {
	sem := make(chan struct{}, maxHookReads)
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		var ce *core.Error
		if !errors.As(err, &ce) {
			log.Error("github webhook", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Error(w, ce.Msg, statusForCode(ce.Code))
	}
	mux.HandleFunc("POST "+hooksPath+"{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := c.GitHookExists(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		if r.ContentLength > maxHookBody {
			http.Error(w, "payload larger than 4 MB", http.StatusRequestEntityTooLarge)
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(hookReadTimeout))
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
			fail(w, err)
			return
		}
		_, _ = io.WriteString(w, msg+"\n")
	})
	return mux
}
