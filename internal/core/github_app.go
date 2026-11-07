package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The install's GitHub App (ADR 0031). One App per Dock install, created
// through GitHub's manifest flow: an operator opens a page Dock serves
// (GitHubManifest), which posts the App's manifest to github.com; GitHub
// redirects back with a code (CompleteGitHubApp), exchanged for the App's
// ID, private key and webhook secret. Installing the App on a GitHub
// account gives it read access to that account's repositories; each
// installation is assigned to the org that may deploy from it. The App
// is the platform's: operators create and remove it, and assign or move
// installations; an org's admins install it for their own org and
// unassign their own installations (ADR 0062). Services
// then connect a repository with no webhook or token of their own, and
// the App's one webhook (gitAppWebhook) routes pushes to them.
//
// The links into GitHub carry a state: 256 random bits, issued to an
// operator (or an org's admin, for that org's install), single use,
// valid for githubFlowTTL. The state is the authority for GitHub's
// redirect back, which therefore needs no session and works when the
// flow was started from the CLI.

const (
	// githubWeb is GitHub's web origin, where Apps are registered and
	// installed.
	githubWeb = "https://github.com"
	// githubFlowTTL is how long a manifest or install link stays usable;
	// GitHub's manifest codes also expire after an hour.
	githubFlowTTL = time.Hour
	// maxGitHubFlows bounds the links waiting for GitHub's redirect.
	maxGitHubFlows = 16
	// tokenRenewal is how early an installation token is renewed before
	// it expires (they last an hour).
	tokenRenewal = 5 * time.Minute
	// maxGitHubRepos caps the repositories listed for an org.
	maxGitHubRepos = 1000
)

// Paths of GitHub's redirects back to Dock, served by the API without a
// session (the state is their authority).
const (
	GitHubAppCallbackPath     = "/api/v1/github/app/callback"
	GitHubInstallCallbackPath = "/api/v1/github/install/callback"
)

// githubAppState is the App's in-memory state: links waiting for GitHub
// and installation tokens. Both are bounded (maxGitHubFlows, one token per
// installation) and expire on use, with no timer.
type githubAppState struct {
	mu     sync.Mutex
	flows  map[string]githubFlow
	tokens map[int64]installToken
}

// githubFlow is a link into GitHub waiting for its redirect back.
type githubFlow struct {
	install bool  // an install link; else the App's manifest
	actor   Actor // the operator or org admin who started it
	// owner is the GitHub org to register the App under and hookID its
	// webhook's ID, for a manifest.
	owner   string
	hookID  string
	org     store.Org // install: the org it is for
	expires time.Time
}

type installToken struct {
	token   string
	expires time.Time
}

func newGitHubAppState() githubAppState {
	return githubAppState{flows: map[string]githubFlow{}, tokens: map[int64]installToken{}}
}

// startFlow stores f under a new state and returns the state. Expired
// links are dropped first, then the oldest if there are too many.
func (s *githubAppState) startFlow(f githubFlow) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := time.Now()
	for k, v := range s.flows {
		if t.After(v.expires) {
			delete(s.flows, k)
		}
	}
	if len(s.flows) >= maxGitHubFlows {
		oldest := ""
		for k, v := range s.flows {
			if oldest == "" || v.expires.Before(s.flows[oldest].expires) {
				oldest = k
			}
		}
		delete(s.flows, oldest)
	}
	state := randomHex(32)
	f.expires = t.Add(githubFlowTTL)
	s.flows[state] = f
	return state
}

// flow returns the link state started, if it is still usable and of the
// kind wanted; take also uses it up.
func (s *githubAppState) flow(state string, install, take bool) (githubFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[state]
	if !ok || f.install != install || time.Now().After(f.expires) {
		again := "`dock github app create`"
		if install {
			again = "`dock github install <org>`"
		}
		return githubFlow{}, errf(apitypes.CodeUnauthorized, "this link expired or was already used; start again with %s", again)
	}
	if take {
		delete(s.flows, state)
	}
	return f, nil
}

// forgetTokens drops cached installation tokens: all of them (id 0) when
// the App goes, or one installation's.
func (s *githubAppState) forgetTokens(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 {
		clear(s.tokens)
		return
	}
	delete(s.tokens, id)
}

// hooksHost is the public hostname webhooks are delivered to: [hooks]
// public_host, else Dock's public address (which serves /hooks/github/
// too); "" without either.
func (c *Core) hooksHost() string {
	return cmp.Or(c.cfg.Hooks.PublicHost, c.publicHost())
}

// noGitHubApp is the error for an operation that needs the App.
func noGitHubApp() error {
	return notFound("Dock has no GitHub App; an operator creates one with `dock github app create`")
}

// githubApp loads the App with its private key.
func (c *Core) githubApp(ctx context.Context) (store.GithubApp, error) {
	app, err := c.store.GetGitHubApp(ctx)
	if store.IsNotFound(err) {
		return app, noGitHubApp()
	}
	return app, err
}

// appJWT is a fresh JWT for the App.
func (c *Core) appJWT(ctx context.Context) (string, error) {
	app, err := c.githubApp(ctx)
	if err != nil {
		return "", err
	}
	pemBytes, err := c.box.Open(app.PrivateKey, appKeyAAD)
	if err != nil {
		return "", fmt.Errorf("open the GitHub App's private key: %w", err)
	}
	key, err := parseAppKey(pemBytes)
	if err != nil {
		return "", err
	}
	return appJWT(app.AppID, key, time.Now())
}

// installationToken returns a token for an installation, from the cache
// while it has more than tokenRenewal left.
func (c *Core) installationToken(ctx context.Context, id int64) (string, error) {
	s := &c.git.app
	s.mu.Lock()
	t, ok := s.tokens[id]
	s.mu.Unlock()
	if ok && time.Until(t.expires) > tokenRenewal {
		return t.token, nil
	}
	jwt, err := c.appJWT(ctx)
	if err != nil {
		if CodeOf(err) != apitypes.CodeNotFound { // no App at all is not a failing one
			c.githubAppFailed(ctx, err)
		}
		return "", err
	}
	token, expires, err := c.git.github.accessToken(ctx, jwt, id)
	if err != nil {
		// A removed installation (404) is the org's, not the App failing.
		if CodeOf(err) != apitypes.CodeNotFound {
			c.githubAppFailed(ctx, err)
		}
		return "", err
	}
	c.connectionWorks(ctx, githubAppAlertKey)
	s.mu.Lock()
	s.tokens[id] = installToken{token: token, expires: expires}
	s.mu.Unlock()
	return token, nil
}

// githubAppFailed raises the GitHub App's platform alert: Dock couldn't
// sign in as the App or get an installation token, so pushes don't
// deploy and statuses aren't posted until it can.
func (c *Core) githubAppFailed(ctx context.Context, err error) {
	c.connectionFailed(ctx, githubAppAlertKey, githubAppAlertKey, "The GitHub App is failing",
		fmt.Sprintf("Dock couldn't get a GitHub token for the App: %v. Deploys from GitHub and commit statuses wait until it can.", err), "dock github status")
}

// toGitHubApp describes the App for the API.
func toGitHubApp(app store.GithubApp) *apitypes.GitHubApp {
	return &apitypes.GitHubApp{
		ID: app.AppID, Slug: app.Slug, Name: app.Name, Owner: app.Owner, URL: app.HtmlUrl,
		CreatedBy: app.CreatedBy, CreatedAt: app.CreatedAt,
	}
}

// GitHubStatus describes the App, where it receives pushes, and its
// installations: GitHub's list merged with Dock's assignments. Operators
// only.
func (c *Core) GitHubStatus(ctx context.Context, a Actor) (apitypes.GitHubStatus, error) {
	if err := a.requireOperator("reading the GitHub App"); err != nil {
		return apitypes.GitHubStatus{}, err
	}
	out := apitypes.GitHubStatus{HooksHint: c.hooksHint(), Installations: []apitypes.GitHubInstallation{}}
	orgs, err := c.store.ListOrgSlugs(ctx)
	if err != nil {
		return out, err
	}
	out.Orgs = orgs
	app, err := c.store.GetGitHubApp(ctx)
	if store.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.App, out.WebhookURL = toGitHubApp(app), c.hookURL(app.HookID)
	assigned, err := c.store.ListGitHubInstallations(ctx)
	if err != nil {
		return out, err
	}
	counts, err := c.store.CountInstallationConnections(ctx)
	if err != nil {
		return out, err
	}
	conns := map[int64]int{}
	for _, n := range counts {
		conns[n.InstallationID] = int(n.Connections)
	}
	orgOf := map[int64]string{}
	for _, i := range assigned {
		orgOf[i.InstallationID] = i.OrgSlug
	}
	var listed []githubInstallation
	jwt, listErr := c.appJWT(ctx)
	if listErr == nil {
		listed, listErr = c.git.github.installations(ctx, jwt)
	}
	if listErr != nil {
		out.InstallationsError = errorLine(listErr)
	}
	seen := map[int64]bool{}
	for _, i := range listed {
		seen[i.ID] = true
		out.Installations = append(out.Installations, apitypes.GitHubInstallation{
			ID: i.ID, Account: i.Account.Login, AccountType: i.Account.Type, Repositories: i.RepositorySelection,
			Org: orgOf[i.ID], Connections: conns[i.ID], Suspended: i.SuspendedAt != nil, URL: i.HTMLURL,
		})
	}
	for _, i := range assigned {
		if !seen[i.InstallationID] {
			out.Installations = append(out.Installations, apitypes.GitHubInstallation{
				ID: i.InstallationID, Account: i.Account, Org: i.OrgSlug, Connections: conns[i.InstallationID], Missing: listErr == nil,
			})
		}
	}
	return out, nil
}

// githubLoginRe matches GitHub account names.
var githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](-?[A-Za-z0-9]){0,38}$`)

// StartGitHubApp begins registering the App: it returns the state for the
// page that posts the manifest to GitHub (GitHubManifest). Needs a public
// address, since GitHub delivers pushes there. Operators only.
func (c *Core) StartGitHubApp(ctx context.Context, a Actor, req apitypes.GitHubAppRequest) (string, error) {
	if err := a.requireOperator("creating the GitHub App"); err != nil {
		return "", err
	}
	owner := strings.TrimSpace(req.Owner)
	if owner != "" && !githubLoginRe.MatchString(owner) {
		return "", invalid("%q is not a GitHub organization name", owner)
	}
	if c.hooksHost() == "" {
		return "", invalid("GitHub needs a public address to send pushes to; set one first with `dock address set <hostname>`")
	}
	switch app, err := c.store.GetGitHubApp(ctx); {
	case err == nil:
		return "", conflict("Dock already has the GitHub App %s; remove it first with `dock github app remove`", app.Slug)
	case !store.IsNotFound(err):
		return "", err
	}
	return c.git.app.startFlow(githubFlow{actor: a, owner: owner, hookID: randomHex(16)}), nil
}

// githubManifest is the App GitHub is asked to register.
type githubManifest struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Description    string `json:"description"`
	HookAttributes struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	} `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	SetupURL           string            `json:"setup_url"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
}

// GitHubManifest returns where to post the App's manifest and the
// manifest itself, for a state from StartGitHubApp. origin is the Dock
// URL the browser has open; GitHub sends it back there unless Dock has a
// public address.
func (c *Core) GitHubManifest(state, origin string) (string, []byte, error) {
	f, err := c.git.app.flow(state, false, false)
	if err != nil {
		return "", nil, err
	}
	hook := c.hookURL(f.hookID)
	if hook == "" {
		return "", nil, invalid("GitHub needs a public address to send pushes to; set one first with `dock address set <hostname>`")
	}
	base := cmp.Or(c.publicURL(), origin)
	m := githubManifest{
		Name:        appName(c.hooksHost()),
		URL:         base,
		Description: "Deploys pushes and previews pull requests with Tempo Dock.",
		RedirectURL: base + GitHubAppCallbackPath,
		SetupURL:    base + GitHubInstallCallbackPath, SetupOnUpdate: true,
		// Pull requests: write lets previews comment (ADR 0056).
		DefaultPermissions: map[string]string{"contents": "read", "metadata": "read", "statuses": "write", "pull_requests": "write"},
		DefaultEvents:      []string{"push", "pull_request"},
	}
	m.HookAttributes.URL, m.HookAttributes.Active = hook, true
	b, err := json.Marshal(m)
	if err != nil {
		return "", nil, err
	}
	action := githubWeb + "/settings/apps/new"
	if f.owner != "" {
		action = githubWeb + "/organizations/" + f.owner + "/settings/apps/new"
	}
	return action + "?state=" + url.QueryEscape(state), b, nil
}

// appName suggests the App's name: GitHub wants it unique and at most 34
// characters; the operator can change it on GitHub's form.
func appName(host string) string {
	name := "Tempo Dock " + strings.ReplaceAll(host, ".", "-")
	return strings.TrimSpace(name[:min(34, len(name))])
}

// CompleteGitHubApp finishes registering the App: it exchanges the code
// GitHub redirected with for the App's credentials and stores them,
// sealed. The state is the authority (see the top of this file).
func (c *Core) CompleteGitHubApp(ctx context.Context, code, state string) (apitypes.GitHubApp, error) {
	f, err := c.git.app.flow(state, false, true)
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	if !manifestCodeRe.MatchString(code) {
		return apitypes.GitHubApp{}, invalid("GitHub's redirect has no valid code; start again with `dock github app create`")
	}
	if _, err := c.store.GetGitHubApp(ctx); err == nil {
		return apitypes.GitHubApp{}, conflict("Dock already has a GitHub App; remove it first with `dock github app remove`")
	}
	created, err := c.git.github.convertManifest(ctx, code)
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	if _, err := parseAppKey([]byte(created.PEM)); err != nil {
		return apitypes.GitHubApp{}, err
	}
	key, err := c.box.Seal([]byte(created.PEM), appKeyAAD)
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	secret, err := c.box.Seal([]byte(created.WebhookSecret), appWebhookSecretAAD)
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	row := store.GithubApp{
		AppID: created.ID, HookID: f.hookID, Slug: cleanLine(created.Slug, 100), Name: cleanLine(created.Name, 100), Owner: cleanLine(created.Owner.Login, 39),
		HtmlUrl: cleanLine(created.HTMLURL, 200), PrivateKey: key, WebhookSecret: secret, CreatedBy: f.actor.Name, CreatedAt: now(),
	}
	if err := c.store.Tx(ctx, func(q *store.Queries) error {
		// A new App has new installations; old assignments mean nothing.
		if err := q.DeleteGitHubInstallations(ctx); err != nil {
			return err
		}
		return q.SetGitHubApp(ctx, store.SetGitHubAppParams{
			AppID: row.AppID, HookID: row.HookID, Slug: row.Slug, Name: row.Name, Owner: row.Owner, HtmlUrl: row.HtmlUrl,
			PrivateKey: row.PrivateKey, WebhookSecret: row.WebhookSecret, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
		})
	}); err != nil {
		return apitypes.GitHubApp{}, err
	}
	c.git.app.forgetTokens(0)
	c.audit(ctx, f.actor.platform(), "github.app_create", nil, nil, row.Slug, fmt.Sprintf("created the GitHub App %s on %s", row.Slug, row.Owner))
	c.connectionWorks(ctx, githubAppAlertKey)
	return *toGitHubApp(row), nil
}

// manifestCodeRe matches the codes GitHub's manifest flow redirects with.
var manifestCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// RemoveGitHubApp forgets the App and its installations' assignments.
// Services connected through it stay connected but can't fetch until they
// reconnect. The App itself stays on GitHub; delete it there. Operators
// only.
func (c *Core) RemoveGitHubApp(ctx context.Context, a Actor) error {
	if err := a.requireOperator("removing the GitHub App"); err != nil {
		return err
	}
	app, err := c.githubApp(ctx)
	if err != nil {
		return err
	}
	if err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteGitHubInstallations(ctx); err != nil {
			return err
		}
		return q.DeleteGitHubApp(ctx)
	}); err != nil {
		return err
	}
	c.git.app.forgetTokens(0)
	c.audit(ctx, a.platform(), "github.app_remove", nil, nil, app.Slug, "removed the GitHub App "+app.Slug)
	c.connectionWorks(ctx, githubAppAlertKey)
	return nil
}

// StartGitHubInstall returns the App's install link on GitHub for an org:
// GitHub's redirect back (CompleteGitHubInstall) assigns the installation
// to it. An org's unlimited admins install it for their own org (req.Org
// empty or theirs); operators for any.
func (c *Core) StartGitHubInstall(ctx context.Context, a Actor, req apitypes.GitHubOrgRequest) (apitypes.GitHubLink, error) {
	org, err := c.installOrg(ctx, a, strings.TrimSpace(req.Org))
	if err != nil {
		return apitypes.GitHubLink{}, err
	}
	app, err := c.githubApp(ctx)
	if err != nil {
		return apitypes.GitHubLink{}, err
	}
	state := c.git.app.startFlow(githubFlow{install: true, actor: a, org: org})
	return apitypes.GitHubLink{URL: app.HtmlUrl + "/installations/new?state=" + url.QueryEscape(state)}, nil
}

// installOrg is the org an install link is for: the named one for an
// operator, else the actor's own, for its unlimited admins.
func (c *Core) installOrg(ctx context.Context, a Actor, slug string) (store.Org, error) {
	const action = "installing the GitHub App"
	if a.Operator && slug != "" {
		return c.orgNamed(ctx, slug)
	}
	if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
		return store.Org{}, err
	}
	if slug != "" && slug != a.OrgSlug {
		return store.Org{}, errf(apitypes.CodeForbidden, "an organization's admins install the GitHub App for their own organization only; an operator installs it for org %s", slug)
	}
	org, err := c.store.GetOrg(ctx, a.OrgID)
	if store.IsNotFound(err) {
		return org, notFound("no organization to install the GitHub App for; pass --org")
	}
	return org, err
}

// CompleteGitHubInstall handles GitHub's redirect after the App was
// installed or its repositories changed: it assigns the installation to
// the org the link was for, unless another org has it (conflict). It
// returns the org's slug and what happened, for the page the browser
// lands on. The state is the authority for the org, not the installation.
func (c *Core) CompleteGitHubInstall(ctx context.Context, installationID int64, setupAction, state string) (string, error) {
	f, err := c.git.app.flow(state, true, true)
	if err != nil {
		return "", err
	}
	if setupAction == "request" {
		return fmt.Sprintf("The installation was requested. Once an owner of the GitHub account approves it, an operator assigns it with `dock github assign <id> %s`.", f.org.Slug), nil
	}
	// The installation ID comes from the redirect, not the state: it may
	// only be claimed, never moved from another org.
	inst, err := c.assignInstallation(ctx, f.actor, installationID, f.org, false)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("The GitHub App can read %s's repositories, and org %s can now deploy from them: `dock git connect %s/<repo>`.", inst.Account.Login, f.org.Slug, inst.Account.Login), nil
}

// AssignGitHubInstallation lets an org deploy from an installation's
// repositories, moving it from another org if needed (that org's
// services stop fetching from it). Operators only.
func (c *Core) AssignGitHubInstallation(ctx context.Context, a Actor, id int64, req apitypes.GitHubOrgRequest) error {
	if err := a.requireOperator("assigning a GitHub App installation"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Org) == "" {
		return invalid("name the org to assign installation %d to", id)
	}
	org, err := c.orgNamed(ctx, req.Org)
	if err != nil {
		return err
	}
	_, err = c.assignInstallation(ctx, a, id, org, true)
	return err
}

// assignInstallation checks the installation is the App's and assigns it
// to org. Unless move is set, an installation another org has is refused
// (conflict) and stays with it.
func (c *Core) assignInstallation(ctx context.Context, a Actor, id int64, org store.Org, move bool) (githubInstallation, error) {
	jwt, err := c.appJWT(ctx)
	if err != nil {
		return githubInstallation{}, err
	}
	inst, err := c.git.github.installation(ctx, jwt, id)
	if err != nil {
		return githubInstallation{}, err
	}
	account := cleanLine(inst.Account.Login, 39)
	if move {
		err = c.store.SetGitHubInstallation(ctx, store.SetGitHubInstallationParams{
			InstallationID: id, OrgID: org.ID, Account: account, AssignedBy: a.Name, AssignedAt: now(),
		})
	} else {
		var n int64
		n, err = c.store.ClaimGitHubInstallation(ctx, store.ClaimGitHubInstallationParams{
			InstallationID: id, OrgID: org.ID, Account: account, AssignedBy: a.Name, AssignedAt: now(),
		})
		if err == nil && n == 0 {
			err = conflict("installation %d (%s) is assigned to another org; to move it to org %s, an operator runs `dock github assign %d %s`", id, account, org.Slug, id, org.Slug)
		}
	}
	if err != nil {
		return githubInstallation{}, err
	}
	in := a
	in.OrgID, in.OrgSlug = org.ID, org.Slug
	c.audit(ctx, in, "github.install_assign", nil, nil, account, fmt.Sprintf("org %s deploys from %s's repositories through the GitHub App (installation %d)", org.Slug, account, id))
	return inst, nil
}

// UnassignGitHubInstallation stops an org deploying from an installation.
// Operators unassign any; an org's unlimited admins their own org's
// (another org's is not found for them). Audited in the org's log.
func (c *Core) UnassignGitHubInstallation(ctx context.Context, a Actor, id int64) error {
	const action = "unassigning a GitHub App installation"
	if !a.Operator {
		if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
			return err
		}
	}
	inst, err := c.store.GetGitHubInstallation(ctx, id)
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	switch {
	case !a.Operator && (err != nil || inst.OrgID != a.OrgID):
		return notFound("installation %d is not assigned to this organization; `dock github installations` lists its own", id)
	case err != nil:
		return notFound("installation %d is not assigned to an org; see `dock github status`", id)
	}
	if err := c.store.DeleteGitHubInstallation(ctx, id); err != nil {
		return err
	}
	c.git.app.forgetTokens(id)
	in := a
	in.OrgID = inst.OrgID
	c.audit(ctx, in, "github.install_unassign", nil, nil, inst.Account, fmt.Sprintf("stopped deploying from %s's repositories through the GitHub App (installation %d)", inst.Account, id))
	return nil
}

// GitHubOrgStatus describes the platform's App and the actor's org's own
// installations, as GitHub lists them (asked only when the org has
// some), with the services connected through each. For the org's
// unlimited admins; operators see every installation in GitHubStatus.
func (c *Core) GitHubOrgStatus(ctx context.Context, a Actor) (apitypes.GitHubOrg, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "reading the organization's GitHub App installations"); err != nil {
		return apitypes.GitHubOrg{}, err
	}
	out := apitypes.GitHubOrg{Installations: []apitypes.GitHubInstallation{}}
	app, err := c.store.GetGitHubApp(ctx)
	if store.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.App = &apitypes.GitHubOrgApp{Slug: app.Slug, Name: app.Name, Owner: app.Owner, URL: app.HtmlUrl}
	own, err := c.store.ListOrgGitHubInstallations(ctx, a.OrgID)
	if err != nil || len(own) == 0 {
		return out, err
	}
	counts, err := c.store.CountInstallationConnections(ctx)
	if err != nil {
		return out, err
	}
	conns := map[int64]int{}
	for _, n := range counts {
		conns[n.InstallationID] = int(n.Connections)
	}
	listed := map[int64]githubInstallation{}
	jwt, listErr := c.appJWT(ctx)
	if listErr == nil {
		var all []githubInstallation
		all, listErr = c.git.github.installations(ctx, jwt)
		for _, i := range all {
			listed[i.ID] = i
		}
	}
	if listErr != nil {
		out.InstallationsError = errorLine(listErr)
	}
	for _, r := range own {
		inst := apitypes.GitHubInstallation{ID: r.InstallationID, Account: r.Account, Org: a.OrgSlug, Connections: conns[r.InstallationID]}
		if i, ok := listed[r.InstallationID]; ok {
			inst.AccountType, inst.Repositories, inst.Suspended, inst.URL = i.Account.Type, i.RepositorySelection, i.SuspendedAt != nil, i.HTMLURL
		} else {
			inst.Missing = listErr == nil
		}
		out.Installations = append(out.Installations, inst)
	}
	return out, nil
}

// orgNamed looks an org up by slug.
func (c *Core) orgNamed(ctx context.Context, slug string) (store.Org, error) {
	org, err := c.store.GetOrgBySlug(ctx, strings.TrimSpace(slug))
	if store.IsNotFound(err) {
		return org, notFound("no org %q; see `dock org list`", slug)
	}
	return org, err
}

// GitHubRepos lists the repositories the caller's org can connect through
// the App, by name. Scope: deploy.
func (c *Core) GitHubRepos(ctx context.Context, a Actor) (apitypes.GitHubRepos, error) {
	if err := a.require(apitypes.ScopeDeploy, "listing GitHub repositories"); err != nil {
		return apitypes.GitHubRepos{}, err
	}
	out := apitypes.GitHubRepos{Repos: []apitypes.GitHubRepo{}}
	insts, err := c.store.ListOrgGitHubInstallations(ctx, a.OrgID)
	if err != nil {
		return out, err
	}
	for _, inst := range insts {
		token, err := c.installationToken(ctx, inst.InstallationID)
		if err != nil {
			return out, err
		}
		repos, more, err := c.git.github.installationRepos(ctx, token, maxGitHubRepos-len(out.Repos))
		if err != nil {
			return out, err
		}
		for _, r := range repos {
			out.Repos = append(out.Repos, apitypes.GitHubRepo{ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private})
		}
		if more || len(out.Repos) >= maxGitHubRepos {
			out.Truncated = true
			break
		}
	}
	slices.SortFunc(out.Repos, func(x, y apitypes.GitHubRepo) int {
		return strings.Compare(strings.ToLower(x.FullName), strings.ToLower(y.FullName))
	})
	return out, nil
}

// appRepo resolves a repository for an App connection in org: the
// installation that reads it, which must be assigned to org, and the
// repository's ID and canonical name.
func (c *Core) appRepo(ctx context.Context, org store.Org, repo string) (int64, githubRepo, error) {
	jwt, err := c.appJWT(ctx)
	if err != nil {
		return 0, githubRepo{}, err
	}
	inst, err := c.git.github.repoInstallation(ctx, jwt, repo)
	if CodeOf(err) == apitypes.CodeNotFound {
		owner, _, _ := strings.Cut(repo, "/")
		return 0, githubRepo{}, notFound("the GitHub App is not installed on %s; an admin of org %s installs it on %s's account with `dock github install`, or connect with --mode webhook", repo, org.Slug, owner)
	}
	if err != nil {
		return 0, githubRepo{}, err
	}
	assigned, err := c.store.GetGitHubInstallation(ctx, inst.ID)
	if err != nil && !store.IsNotFound(err) {
		return 0, githubRepo{}, err
	}
	if err != nil || assigned.OrgID != org.ID {
		return 0, githubRepo{}, errf(apitypes.CodeForbidden, "the GitHub App reads %s through %s's installation, which org %s may not deploy from; an operator assigns it with `dock github assign %d %s`",
			repo, cleanLine(inst.Account.Login, 39), org.Slug, inst.ID, org.Slug)
	}
	token, err := c.installationToken(ctx, inst.ID)
	if err != nil {
		return 0, githubRepo{}, err
	}
	r, err := c.git.github.repo(ctx, token, repo)
	if err != nil {
		return 0, githubRepo{}, err
	}
	if r.ID == 0 || !repoRe.MatchString(r.FullName) {
		return 0, githubRepo{}, fmt.Errorf("GitHub described %s without an ID or name", repo)
	}
	return inst.ID, r, nil
}

// gitCred is how a connection authenticates to GitHub: its own token, or
// the App's installation token. An App connection only gets one while its
// installation is assigned to the service's org, so moving or unassigning
// an installation takes effect at once.
func (c *Core) gitCred(ctx context.Context, ss svcScope, conn store.GitConnection) (githubCred, error) {
	if conn.InstallationID == 0 {
		token, err := c.gitToken(conn)
		return githubCred{token: token}, err
	}
	inst, err := c.store.GetGitHubInstallation(ctx, conn.InstallationID)
	if err != nil && !store.IsNotFound(err) {
		return githubCred{}, err
	}
	if err != nil || inst.OrgID != ss.project.OrgID {
		return githubCred{}, errf(apitypes.CodeForbidden, "the GitHub App installation that reads %s is no longer assigned to this org; ask an operator (`dock github status`), or reconnect with `dock git connect %s --mode webhook`", conn.Repo, conn.Repo)
	}
	token, err := c.installationToken(ctx, conn.InstallationID)
	return githubCred{token: token, kind: credInstall}, err
}
