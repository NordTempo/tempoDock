package api

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// githubAppNewPath is the page that posts the App's manifest to GitHub.
const githubAppNewPath = Prefix + "/github/app/new"

// installInput asks for an install link, which opens on the Dock origin
// the caller reached.
type installInput struct {
	Body   apitypes.GitHubInstallRequest
	origin string
}

// githubAppInput addresses a GitHub App by its slug.
type githubAppInput struct {
	Slug string `path:"slug" maxLength:"100"`
}

// Resolve records the origin the request came to.
func (in *installInput) Resolve(ctx huma.Context) []error {
	in.origin = clientOf(ctx).origin(ctx.Host())
	return nil
}

// registerGitHub registers the GitHub Apps (ADR 0031, 0105): operators
// create the platform's, install them and assign installations to orgs;
// an org's admins create the org's own, see and install the Apps for
// their own org and unassign their own installations (ADR 0062); members
// list the repositories their org can connect. GitHub's redirects back
// land on plain pages (githubPages), outside Huma.
func registerGitHub(s *Server, a huma.API, mux *http.ServeMux) {
	tags := []string{"github"}
	authed(s, a, huma.Operation{
		OperationID: "get-github", Method: http.MethodGet, Path: Prefix + "/github",
		Summary: "Show the GitHub Apps", Tags: tags,
		Description: "Every GitHub App, the platform's and each organization's own, with its webhook URL and its installations (asked of GitHub now) and the org each is assigned to. Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.GitHubStatus], error) {
		return reply(s.core.GitHubStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "create-github-app", Method: http.MethodPost, Path: Prefix + "/github/apps",
		Summary: "Start creating a GitHub App", Tags: tags,
		Description: "Returns a page on this server (a path) to open in a browser, valid for an hour: it sends the App's manifest to GitHub, which creates the App and sends the browser back to Dock. Needs Dock's public address. " +
			"The organization's own App (always private: only its GitHub account installs it, and its installations are the organization's), for its admins; with platform, an App for every organization, for operators.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Platform bool `query:"platform" doc:"An App of the platform's, which operators assign to organizations, instead of the organization's own. Operators only."`
		Body     apitypes.GitHubAppRequest
	}) (*out[apitypes.GitHubLink], error) {
		state, err := s.core.StartGitHubApp(ctx, actor, in.Platform, in.Body)
		if err != nil {
			return nil, err
		}
		return reply(apitypes.GitHubLink{URL: githubAppNewPath + "?state=" + url.QueryEscape(state)}, nil)
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-github-app", Method: http.MethodDelete, Path: Prefix + "/github/apps/{slug}",
		Summary: "Remove a GitHub App", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "Forgets the App's key and installations. Services connected through it can't fetch until reconnected. Delete the App on GitHub too. A platform App: operators; an organization's own: its admins.",
	}, func(ctx context.Context, actor core.Actor, in *githubAppInput) (*struct{}, error) {
		return nil, s.core.RemoveGitHubApp(ctx, actor, in.Slug)
	})
	authed(s, a, huma.Operation{
		OperationID: "set-github-app-client-secret", Method: http.MethodPut, Path: Prefix + "/github/apps/{slug}/client-secret",
		Summary: "Set a GitHub App's client secret", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "For a platform App made before Dock kept its OAuth client: generate a client secret in the App's settings on GitHub and add the callback URL `GET /github` shows. Dock checks the secret with GitHub, then checks who installs the App. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Slug string `path:"slug" maxLength:"100"`
		Body apitypes.GitHubClientSecretRequest
	}) (*struct{}, error) {
		return nil, s.core.SetGitHubAppClientSecret(ctx, actor, in.Slug, in.Body)
	})
	authed(s, a, huma.Operation{
		OperationID: "get-github-org", Method: http.MethodGet, Path: Prefix + "/github/org",
		Summary: "Show the organization's GitHub Apps", Tags: tags,
		Description: "The organization's own GitHub Apps with their installations, and the platform's with the organization's installations of them (asked of GitHub now) and the services connected through each. Organization admins.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.GitHubOrg], error) {
		return reply(s.core.GitHubOrgStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "install-github-app", Method: http.MethodPost, Path: Prefix + "/github/installations",
		Summary: "Get a GitHub App's install link for an org", Tags: tags,
		Description: "Returns the install link, valid for an hour: a page on Dock that only the caller, signed in to Dock in that browser, can open, and which goes on to the App's install page on GitHub. Once it is installed, GitHub sends the browser back to Dock. " +
			"For the organization's own App, the installation is the organization's if it is on the App's owner account. For a platform App, Dock checks with GitHub that the person can access the installation and, once they confirm, assigns it to the org, unless another org has it. " +
			"app names the App among those the org may use; it may be left out while there is one. Organization admins for their own organization (org empty or theirs); operators for any. A person's credential: a UI session or a `dock login` token.",
	}, func(ctx context.Context, actor core.Actor, in *installInput) (*out[apitypes.GitHubLink], error) {
		return reply(s.core.StartGitHubInstall(ctx, actor, in.Body, in.origin))
	})
	authed(s, a, huma.Operation{
		OperationID: "assign-github-installation", Method: http.MethodPut, Path: Prefix + "/github/installations/{id}",
		Summary: "Assign a GitHub App installation to an org", Tags: tags, DefaultStatus: http.StatusNoContent,
		Description: "An installation of a platform App (app, or whichever has it): the org may then connect its repositories. Moves it from another org, whose services stop fetching from it. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ID   int64 `path:"id"`
		Body apitypes.GitHubInstallRequest
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
		Description: "The repositories the org's GitHub App installations can read, the newest push first, at most 1000. Scope: deploy.",
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

// githubPageCSP lets the manifest page post its form to GitHub and the
// confirm page to Dock, and nothing else: no scripts, no loads.
const githubPageCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action https://github.com 'self'; frame-ancestors 'none'; base-uri 'none'"

// githubCookie binds a GitHub App install in flight to the browser of the
// person who opened its link (core.BindGitHubInstall). SameSite=Lax:
// GitHub sends the browser back with a cross-site top-level GET.
const githubCookie = "dock_github"

// githubCookie sets (value) or clears ("") the install binding cookie,
// sent only to the GitHub pages over plain HTTP.
func (c client) githubCookie(value string) http.Cookie {
	return c.cookie(githubCookie, value, Prefix+"/github", time.Now().Add(core.GitHubFlowTTL), http.SameSiteLaxMode)
}

// githubPage is the HTML of the pages GitHub's flow passes through: a
// title, a message, and either the manifest form or a link onward.
var githubPage = template.Must(template.New("github").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Tempo Dock</title>
<style>body{margin:0;background:#000;color:#fff;font:14px/1.5 system-ui,sans-serif}main{max-width:560px;margin:15vh auto;padding:0 24px}
h1{font-size:18px;margin:0 0 8px}p{color:#ddd}code{font-family:ui-monospace,Menlo,monospace;font-size:13px}a{color:#cfe0ff}
button{background:#fff;color:#000;border:0;padding:4px 12px;font:inherit;cursor:pointer}</style></head>
<body><main><h1>{{.Title}}</h1><p>{{.Message}}</p>
{{if .Action}}<form method="post" action="{{.Action}}">{{range $k, $v := .Fields}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}<button type="submit">{{.Button}}</button></form>{{end}}
{{if .Link}}<p><a href="{{.Link}}">{{.LinkText}}</a></p>{{end}}
</main></body></html>`))

// githubPageData fills githubPage: a form posts Fields to Action with a
// Button.
type githubPageData struct {
	Title, Message string
	Action, Button string
	Fields         map[string]string
	Link, LinkText string
}

// githubPages serves the plain pages of the GitHub App's flows. Each
// carries a single-use state Dock issued. The manifest's state is its
// authority; an install's start page needs the session of the person
// who asked for the link and binds the browser with githubCookie, which
// every later step checks (core.BindGitHubInstall).
//
//	GET  /api/v1/github/app/new?state=          posts the manifest to GitHub
//	GET  /api/v1/github/app/callback?code&state  GitHub's redirect: store the App
//	GET  /api/v1/github/install/start?state=     binds the browser, on to GitHub's install page
//	GET  /api/v1/github/install/callback?…       GitHub's redirect after install
//	GET  /api/v1/github/user/callback?code&state GitHub's redirect after authorizing: check, ask to confirm
//	POST /api/v1/github/install/confirm          the confirm form: assign
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
			Title:   "Create a GitHub App",
			Message: "GitHub asks you to confirm the App's name, then sends you back here. The App can read code and post commit statuses, and receives pushes.",
			Action:  action, Button: "Continue to GitHub", Fields: map[string]string{"manifest": string(manifest)},
		})
	})
	mux.HandleFunc("GET "+core.GitHubAppCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		app, err := c.CompleteGitHubApp(r.Context(), q.Get("code"), q.Get("state"))
		if err != nil {
			fail(w, err)
			return
		}
		if app.Org != "" {
			render(w, http.StatusOK, githubPageData{
				Title:   "GitHub App created",
				Message: "Org " + app.Org + " now has its own GitHub App " + app.Name + ". Next, install it on " + app.Owner + " from the organization's GitHub settings, or with dock github install --app " + app.Slug + ".",
				Link:    "/" + url.PathEscape(app.Org) + "/settings/github", LinkText: "Back to Dock",
			})
			return
		}
		render(w, http.StatusOK, githubPageData{
			Title:   "GitHub App created",
			Message: "Dock now has the GitHub App " + app.Name + ". Next, install it on the GitHub account whose repositories should deploy, for an org: dock github install <org> --app " + app.Slug + ".",
			Link:    "/console/github", LinkText: "Back to Dock",
		})
	})
	// step answers an install step: on to GitHub, the confirm page, or
	// what happened.
	step := func(w http.ResponseWriter, r *http.Request, st core.GitHubInstallStep) {
		switch {
		case st.Redirect != "":
			http.Redirect(w, r, st.Redirect, http.StatusFound)
		case st.Confirm != nil:
			render(w, http.StatusOK, githubPageData{
				Title:   "Assign to org " + st.Confirm.Org + "?",
				Message: "Org " + st.Confirm.Org + " will deploy from " + st.Confirm.Account + "'s repositories through the GitHub App, and its members can read them.",
				Action:  core.GitHubInstallConfirmPath, Button: "Assign to org " + st.Confirm.Org, Fields: map[string]string{"state": st.Confirm.State},
				Link: "/", LinkText: "Cancel",
			})
		default:
			render(w, http.StatusOK, githubPageData{Title: "GitHub App installed", Message: st.Message, Link: "/", LinkText: "Back to Dock"})
		}
	}
	mux.HandleFunc("GET "+core.GitHubInstallStartPath, func(w http.ResponseWriter, r *http.Request) {
		cl := requestClient(r)
		secret, legacy := cl.session(r.Cookie)
		if secret == "" {
			render(w, http.StatusUnauthorized, githubPageData{
				Title: "Sign in to Dock", Message: "This install link works in the browser you are signed in to Dock with. Sign in here, then open the link again.",
				Link: "/login", LinkText: "Sign in",
			})
			return
		}
		a, err := c.AuthenticateSession(r.Context(), secret, "", legacy)
		if err != nil {
			fail(w, err)
			return
		}
		to, binding, err := c.BindGitHubInstall(r.Context(), a, r.URL.Query().Get("state"))
		if err != nil {
			fail(w, err)
			return
		}
		ck := cl.githubCookie(binding)
		http.SetCookie(w, &ck)
		http.Redirect(w, r, to, http.StatusFound)
	})
	mux.HandleFunc("GET "+core.GitHubInstallCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
		if q.Get("state") == "" {
			// GitHub also comes here after repositories were changed on
			// its own pages, with no state of Dock's.
			render(w, http.StatusOK, githubPageData{
				Title:   "GitHub App saved",
				Message: "GitHub saved the installation. An organization's own App's installation on its owner account is the organization's once GitHub tells Dock. A platform App's new installation is assigned by an operator: dock github assign " + strconv.FormatInt(id, 10) + " <org>.",
				Link:    "/", LinkText: "Back to Dock",
			})
			return
		}
		cl := requestClient(r)
		st, err := c.CompleteGitHubInstall(r.Context(), id, q.Get("setup_action"), q.Get("state"), cl.read(r.Cookie, githubCookie), requestOrigin(r))
		if err != nil {
			fail(w, err)
			return
		}
		step(w, r, st)
	})
	mux.HandleFunc("GET "+core.GitHubUserCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		st, err := c.CompleteGitHubVerify(r.Context(), q.Get("code"), q.Get("state"), requestClient(r).read(r.Cookie, githubCookie))
		if err != nil {
			fail(w, err)
			return
		}
		step(w, r, st)
	})
	mux.HandleFunc("POST "+core.GitHubInstallConfirmPath, func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		cl := requestClient(r)
		msg, err := c.ConfirmGitHubInstall(r.Context(), r.PostFormValue("state"), cl.read(r.Cookie, githubCookie))
		if err != nil {
			fail(w, err)
			return
		}
		ck := cl.githubCookie("")
		http.SetCookie(w, &ck)
		render(w, http.StatusOK, githubPageData{Title: "GitHub App installed", Message: msg, Link: "/", LinkText: "Back to Dock"})
	})
}

// requestOrigin is the scheme and host the browser used to reach Dock:
// https when Caddy forwarded it (Dock's public address), else plain.
func requestOrigin(r *http.Request) string {
	return requestClient(r).origin(r.Host)
}
