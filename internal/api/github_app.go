package api

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// githubAppNewPath is the page that posts the App's manifest to GitHub.
const githubAppNewPath = Prefix + "/github/app/new"

// registerGitHub registers the install's GitHub App (ADR 0031): operators
// create it, install it and assign installations to orgs; an org's admins
// see and install it for their own org and unassign their own
// installations (ADR 0062); members list the repositories their org can
// connect. GitHub's redirects back land on
// plain pages (githubPages), outside Huma.
func registerGitHub(s *Server, a huma.API, mux *http.ServeMux) {
	tags := []string{"github"}
	authed(s, a, huma.Operation{
		OperationID: "get-github", Method: http.MethodGet, Path: Prefix + "/github",
		Summary: "Show the GitHub App", Tags: tags,
		Description: "The install's GitHub App, its webhook URL and its installations (asked of GitHub now) with the org each is assigned to. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.GitHubStatus], error) {
		return reply(s.core.GitHubStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-github-app", Method: http.MethodPost, Path: Prefix + "/github/app",
		Summary: "Start creating the GitHub App", Tags: tags,
		Description: "Returns a page on this server (a path) to open in a browser, valid for an hour: it sends the App's manifest to GitHub, which creates the App and sends the browser back to Dock. Needs Dock's public address. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.GitHubAppRequest }) (*out[apitypes.GitHubLink], error) {
		state, err := s.core.StartGitHubApp(ctx, actor, in.Body)
		if err != nil {
			return nil, err
		}
		return reply(apitypes.GitHubLink{URL: githubAppNewPath + "?state=" + url.QueryEscape(state)}, nil)
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-github-app", Method: http.MethodDelete, Path: Prefix + "/github/app",
		Summary: "Remove the GitHub App", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Forgets the App's key and installations. Services connected through it can't fetch until reconnected. Delete the App on GitHub too. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*struct{}, error) {
		return nil, s.core.RemoveGitHubApp(ctx, actor)
	})
	authed(s, a, huma.Operation{
		OperationID: "set-github-app-client-secret", Method: http.MethodPut, Path: Prefix + "/github/app/client-secret",
		Summary: "Set the GitHub App's client secret", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "For an App made before Dock kept its OAuth client: generate a client secret in the App's settings on GitHub and add the callback URL `GET /github` shows. Dock checks the secret with GitHub, then checks who installs the App. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.GitHubClientSecretRequest
	}) (*struct{}, error) {
		return nil, s.core.SetGitHubAppClientSecret(ctx, actor, in.Body)
	})
	authed(s, a, huma.Operation{
		OperationID: "get-github-org", Method: http.MethodGet, Path: Prefix + "/github/org",
		Summary: "Show the GitHub App for the organization", Tags: tags,
		Description: "The platform's GitHub App, if it has one, and the organization's own installations (asked of GitHub now) with the services connected through each. Organization admins.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.GitHubOrg], error) {
		return reply(s.core.GitHubOrgStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "install-github-app", Method: http.MethodPost, Path: Prefix + "/github/installations",
		Summary: "Get the GitHub App's install link for an org", Tags: tags,
		Description: "Returns the App's install page on GitHub, valid for an hour. Once it is installed, GitHub sends the browser back to Dock, which checks with GitHub that the person can access the installation and assigns it to the org, unless another org has it. " +
			"Organization admins for their own organization (org empty or theirs); operators for any.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.GitHubOrgRequest }) (*out[apitypes.GitHubLink], error) {
		return reply(s.core.StartGitHubInstall(ctx, actor, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "assign-github-installation", Method: http.MethodPut, Path: Prefix + "/github/installations/{id}",
		Summary: "Assign a GitHub App installation to an org", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "The org may then connect the installation's repositories. Moves it from another org, whose services stop fetching from it. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID   int64 `path:"id"`
		Body apitypes.GitHubOrgRequest
	}) (*struct{}, error) {
		return nil, s.core.AssignGitHubInstallation(ctx, actor, in.ID, in.Body)
	})
	authed(s, a, huma.Operation{
		OperationID: "unassign-github-installation", Method: http.MethodDelete, Path: Prefix + "/github/installations/{id}",
		Summary: "Unassign a GitHub App installation", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Its org's services stop fetching from it. Organization admins for their own organization's installations; operators for any.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		return nil, s.core.UnassignGitHubInstallation(ctx, actor, in.ID)
	})
	authed(s, a, huma.Operation{
		OperationID: "list-github-repos", Method: http.MethodGet, Path: Prefix + "/github/repos",
		Summary: "List the GitHub repositories the org can connect", Tags: tags,
		Description: "The repositories the GitHub App can read through the org's installations, by name, at most 1000. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.GitHubRepos], error) {
		return reply(s.core.GitHubRepos(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-github-branches", Method: http.MethodGet, Path: Prefix + "/github/repos/{owner}/{name}/branches",
		Summary: "List a repository's branches", Tags: tags,
		Description: "The branches of a repository the org's GitHub App reads, the default first, at most 100. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Owner string `path:"owner" maxLength:"39"`
		Name  string `path:"name" maxLength:"100"`
	}) (*out[apitypes.GitHubBranches], error) {
		return reply(s.core.GitHubBranches(ctx, actor, in.Owner+"/"+in.Name))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-github-paths", Method: http.MethodGet, Path: Prefix + "/github/repos/{owner}/{name}/paths",
		Summary: "List a repository's directories", Tags: tags,
		Description: "The directories of a repository the org's GitHub App reads, at a branch: the ones with a Dockerfile, package.json, go.mod and the like first (roots), then the rest. Dependency and hidden directories are left out. Scope: deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Owner  string `path:"owner" maxLength:"39"`
		Name   string `path:"name" maxLength:"100"`
		Branch string `query:"branch" maxLength:"255" doc:"The repository's default branch when omitted"`
	}) (*out[apitypes.GitHubPaths], error) {
		return reply(s.core.GitHubPaths(ctx, actor, in.Owner+"/"+in.Name, in.Branch))
	})
	githubPages(s.core, s.opts.Logger, mux)
}

// githubPageCSP lets the manifest page post its form to GitHub, and
// nothing else: no scripts, no loads.
const githubPageCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action https://github.com; frame-ancestors 'none'; base-uri 'none'"

// githubPage is the HTML of the pages GitHub's flow passes through: a
// title, a message, and either the manifest form or a link onward.
var githubPage = template.Must(template.New("github").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Tempo Dock</title>
<style>body{margin:0;background:#000;color:#fff;font:14px/1.5 system-ui,sans-serif}main{max-width:560px;margin:15vh auto;padding:0 24px}
h1{font-size:18px;margin:0 0 8px}p{color:#ddd}code{font-family:ui-monospace,Menlo,monospace;font-size:13px}a{color:#cfe0ff}
button{background:#fff;color:#000;border:0;padding:4px 12px;font:inherit;cursor:pointer}</style></head>
<body><main><h1>{{.Title}}</h1><p>{{.Message}}</p>
{{if .Action}}<form method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button type="submit">Continue to GitHub</button></form>{{end}}
{{if .Link}}<p><a href="{{.Link}}">{{.LinkText}}</a></p>{{end}}
</main></body></html>`))

// githubPageData fills githubPage.
type githubPageData struct {
	Title, Message   string
	Action, Manifest string
	Link, LinkText   string
}

// githubPages serves the plain pages of the GitHub App's flows. They need
// no session: each carries the state Dock issued to an operator, which is
// single use (core.CompleteGitHubApp).
//
//	GET /api/v1/github/app/new?state=          posts the manifest to GitHub
//	GET /api/v1/github/app/callback?code&state  GitHub's redirect: store the App
//	GET /api/v1/github/install/callback?…       GitHub's redirect after install
//	GET /api/v1/github/user/callback?code&state GitHub's redirect after authorizing: check and assign
func githubPages(c *core.Core, log *slog.Logger, mux *http.ServeMux) {
	render := func(w http.ResponseWriter, status int, d githubPageData) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", githubPageCSP)
		w.WriteHeader(status)
		if err := githubPage.Execute(w, d); err != nil {
			log.Warn("render GitHub page", "err", err)
		}
	}
	fail := func(w http.ResponseWriter, err error) {
		status, msg := http.StatusInternalServerError, "Internal error; see the daemon log."
		if ce, ok := errors.AsType[*core.Error](err); ok {
			status, msg = statusForCode(ce.Code), ce.Msg
		} else {
			log.Error("GitHub App", "err", err)
		}
		render(w, status, githubPageData{Title: "GitHub App", Message: msg})
	}
	mux.HandleFunc("GET "+githubAppNewPath, func(w http.ResponseWriter, r *http.Request) {
		action, manifest, err := c.GitHubManifest(r.URL.Query().Get("state"), requestOrigin(r))
		if err != nil {
			fail(w, err)
			return
		}
		render(w, http.StatusOK, githubPageData{
			Title:   "Create Dock's GitHub App",
			Message: "GitHub asks you to confirm the App's name, then sends you back here. The App can read code and post commit statuses, and receives pushes.",
			Action:  action, Manifest: string(manifest),
		})
	})
	mux.HandleFunc("GET "+core.GitHubAppCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		app, err := c.CompleteGitHubApp(r.Context(), q.Get("code"), q.Get("state"))
		if err != nil {
			fail(w, err)
			return
		}
		render(w, http.StatusOK, githubPageData{
			Title:   "GitHub App created",
			Message: "Dock now has the GitHub App " + app.Name + ". Next, install it on the GitHub account whose repositories should deploy, for an org: dock github install <org>.",
			Link:    "/console", LinkText: "Back to Dock",
		})
	})
	mux.HandleFunc("GET "+core.GitHubInstallCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
		if q.Get("state") == "" {
			// GitHub also comes here after repositories were changed on
			// its own pages, with no state of Dock's.
			render(w, http.StatusOK, githubPageData{
				Title:   "GitHub App saved",
				Message: "GitHub saved the installation. If it is new, an operator assigns it to an org: dock github assign " + strconv.FormatInt(id, 10) + " <org>.",
				Link:    "/console", LinkText: "Back to Dock",
			})
			return
		}
		step, err := c.CompleteGitHubInstall(r.Context(), id, q.Get("setup_action"), q.Get("state"), requestOrigin(r))
		if err != nil {
			fail(w, err)
			return
		}
		if step.Redirect != "" {
			http.Redirect(w, r, step.Redirect, http.StatusFound)
			return
		}
		render(w, http.StatusOK, githubPageData{Title: "GitHub App installed", Message: step.Message, Link: "/", LinkText: "Back to Dock"})
	})
	mux.HandleFunc("GET "+core.GitHubUserCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		msg, err := c.CompleteGitHubVerify(r.Context(), q.Get("code"), q.Get("state"))
		if err != nil {
			fail(w, err)
			return
		}
		render(w, http.StatusOK, githubPageData{Title: "GitHub App installed", Message: msg, Link: "/", LinkText: "Back to Dock"})
	})
}

// requestOrigin is the scheme and host the browser used to reach Dock:
// https when Caddy forwarded it (Dock's public address), else plain.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
