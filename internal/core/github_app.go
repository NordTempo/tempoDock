package core

import (
	"cmp"
	"context"
	"crypto/subtle"
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

// GitHub Apps (ADR 0031, 0105), created through GitHub's manifest flow:
// a person opens a page Dock serves (GitHubManifest), which posts the
// App's manifest to github.com; GitHub redirects back with a code
// (CompleteGitHubApp), exchanged for the App's ID, private key and webhook
// secret. Installing an App on a GitHub account gives it read access to
// that account's repositories; each installation belongs to the org that
// may deploy from it. Services then connect a repository with no webhook
// or token of their own, and each App's webhook (gitAppWebhook) routes
// pushes to them.
//
// An App is the platform's or one org's. The platform's are made and
// removed by operators, who assign or move their installations; an org's
// admins install them for their own org and unassign their own
// installations (ADR 0062). An org's own App is private and made by its
// unlimited admins: its installations belong to that org, and only one on
// the App's owner account counts (recordOrgInstallation), so nothing has
// to be assigned or checked.
//
// The links into GitHub carry a state: 256 random bits, issued to an
// operator or an org's admin, single use, valid for GitHubFlowTTL. For
// the manifest the state is the authority for GitHub's redirect back. An
// install link is a person's: it opens Dock's start page, which needs
// that person's session and binds the flow to the browser with a cookie
// (BindGitHubInstall), so a link sent to someone else installs nothing
// for the org. Every later step checks the cookie. The state vouches for
// the org and the App, never for the installation ID next to it: a
// platform App with its OAuth client sends the browser on to GitHub's
// authorize page and claims the installation only if the person's token
// can access it (ADR 0066, github_app_verify.go), after the person
// confirms the org on Dock's page. An org's own App asks GitHub, as the
// App, which account the installation is on.

const (
	// githubWeb is GitHub's web origin, where Apps are registered and
	// installed.
	githubWeb = "https://github.com"
	// GitHubFlowTTL is how long a manifest or install link stays usable,
	// and the API's install binding cookie lasts; GitHub's manifest codes
	// also expire after an hour.
	GitHubFlowTTL = time.Hour
	// maxGitHubFlows bounds the links waiting for GitHub's redirect.
	maxGitHubFlows = 16
	// tokenRenewal is how early an installation token is renewed before
	// it expires (they last an hour).
	tokenRenewal = 5 * time.Minute
	// maxGitHubRepos caps the repositories listed for an org.
	maxGitHubRepos = 1000
	// maxGitHubApps caps the Apps of one owner (the platform, or an org):
	// connecting a repository may ask each of them in turn.
	maxGitHubApps = 10
)

// Paths of the pages GitHub's flows pass through, served by the API
// outside Huma: GitHub's redirects back to Dock, and the install's start
// and confirm pages.
const (
	GitHubAppCallbackPath     = "/api/v1/github/app/callback"
	GitHubInstallCallbackPath = "/api/v1/github/install/callback"
	// GitHubUserCallbackPath is the App's OAuth callback, where the
	// installer comes back from GitHub's authorize page (ADR 0066).
	GitHubUserCallbackPath = "/api/v1/github/user/callback"
	// GitHubInstallStartPath is where an install link opens: it binds
	// the flow to the browser and goes on to GitHub.
	GitHubInstallStartPath = "/api/v1/github/install/start"
	// GitHubInstallConfirmPath takes the confirm page's form: the person
	// assigns the installation to the org.
	GitHubInstallConfirmPath = "/api/v1/github/install/confirm"
)

// githubAppState is the Apps' in-memory state: links waiting for GitHub
// and installation tokens (installation IDs are unique across Apps). Both are bounded (maxGitHubFlows, one token per
// installation) and expire on use, with no timer.
type githubAppState struct {
	mu     sync.Mutex
	flows  map[string]githubFlow
	tokens map[int64]installToken
}

// githubFlowKind is what a link into GitHub is for.
type githubFlowKind int

const (
	flowManifest githubFlowKind = iota // registering an App
	flowInstall                        // installing one for an org
	flowVerify                         // checking who installed it (ADR 0066)
	flowConfirm                        // waiting for the person to confirm the org
)

// githubFlow is a link into GitHub waiting for its redirect back.
type githubFlow struct {
	kind  githubFlowKind
	actor Actor // the operator or org admin who started it
	// owner is the GitHub org to register the App under, hookID its
	// webhook's ID and public whether any account may install it, for a
	// manifest.
	owner  string
	hookID string
	public bool
	// org is the org the App is for (ID 0: the platform's), for a
	// manifest; the org the installation is for, for the other kinds.
	org store.Org
	// app is the App (its Dock ID) being installed: install, verify and
	// confirm.
	app int64
	// binding is the SHA-256 of the install's browser binding cookie,
	// set when the start page opens (BindGitHubInstall); "" until then.
	binding string
	// installation, verifier and redirect are a verify link's: the
	// installation to claim, its PKCE verifier and the redirect URL the
	// code is bound to. account is the installation's GitHub account, for
	// the confirm page.
	installation int64
	verifier     string
	redirect     string
	account      string
	expires      time.Time
}

// boundTo reports whether binding is the browser binding of f.
func (f githubFlow) boundTo(binding string) bool {
	return f.binding != "" && subtle.ConstantTimeCompare([]byte(hashSecret(binding)), []byte(f.binding)) == 1
}

// errGitHubUnbound refuses a step of an install from a browser other
// than the one that opened the install link.
func errGitHubUnbound() error {
	return errf(apitypes.CodeUnauthorized, "this install was started in another browser, or its cookie was lost; start again from Dock with `dock github install`, in the browser you are signed in to Dock with")
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
	f.expires = t.Add(GitHubFlowTTL)
	s.flows[state] = f
	return state
}

// flow returns the link state started, if it is still usable and of the
// kind wanted; take also uses it up.
func (s *githubAppState) flow(state string, kind githubFlowKind, take bool) (githubFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[state]
	if !ok || f.kind != kind || time.Now().After(f.expires) {
		again := "`dock github app create`"
		if kind != flowManifest {
			again = "`dock github install <org>`"
		}
		return githubFlow{}, errf(apitypes.CodeUnauthorized, "this link expired or was already used; start again with %s", again)
	}
	if take {
		delete(s.flows, state)
	}
	return f, nil
}

// bind binds the install link state to a browser for user userID, the
// person who started it, and returns the binding for its cookie and the
// App the link installs. Opening the link again (the same person) binds
// the newer browser instead.
func (s *githubAppState) bind(state string, userID int64) (binding string, app int64, err error) {
	if _, err := s.flow(state, flowInstall, false); err != nil {
		return "", 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[state]
	if !ok {
		return "", 0, errf(apitypes.CodeUnauthorized, "this link was just used; start again with `dock github install <org>`")
	}
	if userID == 0 || f.actor.UserID != userID {
		return "", 0, errf(apitypes.CodeForbidden, "this install link is someone else's: only the person who asked Dock for it can use it. Sign in to Dock as yourself and run `dock github install`, or install the App on GitHub and ask an operator to assign it (`dock github assign`)")
	}
	binding, hash := newSecret("")
	f.binding = hash
	s.flows[state] = f
	return binding, f.app, nil
}

// forgetTokens drops cached installation tokens: all of them (id 0) when
// an App goes, or one installation's.
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

// noGitHubApp is the error for an operation that needs an App while there
// is none.
func noGitHubApp() error {
	return notFound("Dock has no GitHub App; an organization's admin creates one with `dock github app create`, an operator one for every organization with `dock github app create --platform`")
}

// appOrg is the org an App belongs to, 0 for the platform's.
func appOrg(app store.GithubApp) int64 {
	if app.OrgID == nil {
		return 0
	}
	return *app.OrgID
}

// githubApp loads an App by its Dock ID, with its private key.
func (c *Core) githubApp(ctx context.Context, id int64) (store.GithubApp, error) {
	app, err := c.store.GetGitHubApp(ctx, id)
	if store.IsNotFound(err) {
		return app, noGitHubApp()
	}
	return app, err
}

// appJWT is a fresh JWT for app.
func (c *Core) appJWT(app store.GithubApp) (string, error) {
	pemBytes, err := c.box.Open(app.PrivateKey, appKeyAAD(app.HookID))
	if err != nil {
		return "", fmt.Errorf("open the private key of the GitHub App %s: %w", app.Slug, err)
	}
	key, err := parseAppKey(pemBytes)
	if err != nil {
		return "", err
	}
	return appJWT(app.AppID, key, time.Now())
}

// installationToken returns a token for installation id of the App with
// Dock ID appID, from the cache while it has more than tokenRenewal left;
// the App is only read when the cache misses.
func (c *Core) installationToken(ctx context.Context, appID, id int64) (string, error) {
	s := &c.git.app
	s.mu.Lock()
	t, ok := s.tokens[id]
	s.mu.Unlock()
	if ok && time.Until(t.expires) > tokenRenewal {
		return t.token, nil
	}
	app, err := c.githubApp(ctx, appID)
	if err != nil {
		return "", err
	}
	jwt, err := c.appJWT(app)
	if err != nil {
		c.githubAppFailed(ctx, app, err)
		return "", err
	}
	token, expires, err := c.git.github.accessToken(ctx, jwt, id)
	if err != nil {
		// A removed installation (404) is the org's, not the App failing.
		if CodeOf(err) != apitypes.CodeNotFound {
			c.githubAppFailed(ctx, app, err)
		}
		return "", err
	}
	c.connectionWorks(ctx, githubAppAlertKey(app.ID))
	s.mu.Lock()
	s.tokens[id] = installToken{token: token, expires: expires}
	s.mu.Unlock()
	return token, nil
}

// githubAppFailed raises an App's alert, the platform's or its org's:
// Dock couldn't sign in as the App or get an installation token, so
// pushes don't deploy and statuses aren't posted until it can.
func (c *Core) githubAppFailed(ctx context.Context, app store.GithubApp, err error) {
	c.connectionFailedIn(ctx, appOrg(app), githubAppAlertKey(app.ID), "github:"+app.Slug, "The GitHub App "+app.Slug+" is failing",
		fmt.Sprintf("Dock couldn't get a GitHub token for the App: %v. Deploys from GitHub and commit statuses wait until it can.", err), "dock github status")
}

// toGitHubApp describes an App for the API; org is its org's slug, empty
// for the platform's.
func toGitHubApp(app store.GithubApp, org string) apitypes.GitHubApp {
	return apitypes.GitHubApp{
		ID: app.AppID, Slug: app.Slug, Name: app.Name, Owner: app.Owner, URL: app.HtmlUrl, Org: org,
		VerifiesInstalls: app.ClientSecret != nil, CreatedBy: app.CreatedBy, CreatedAt: app.CreatedAt,
	}
}

// assignedInstallation is an installation Dock has given to an org, for
// appStatus.
type assignedInstallation struct {
	org, account string
}

// appStatus describes an App with its installations: GitHub's list merged
// with assigned, the App's installations Dock has given to orgs. With
// all, every installation GitHub lists is included (an unassigned one has
// no org); without, only the assigned ones, and GitHub is asked only when
// there are some.
func (c *Core) appStatus(ctx context.Context, app store.GithubApp, org string, assigned map[int64]assignedInstallation, conns map[int64]int, all bool) apitypes.GitHubAppStatus {
	out := apitypes.GitHubAppStatus{App: toGitHubApp(app, org), WebhookURL: c.hookURL(app.HookID), Installations: []apitypes.GitHubInstallation{}}
	if !all && len(assigned) == 0 {
		return out
	}
	var listed []githubInstallation
	jwt, err := c.appJWT(app)
	if err == nil {
		listed, err = c.git.github.installations(ctx, jwt)
	}
	if err != nil {
		out.InstallationsError = c.errorLine(err)
	}
	seen := map[int64]bool{}
	for _, i := range listed {
		as, ok := assigned[i.ID]
		if !ok && !all {
			continue
		}
		seen[i.ID] = true
		out.Installations = append(out.Installations, apitypes.GitHubInstallation{
			ID: i.ID, Account: i.Account.Login, AccountType: i.Account.Type, Repositories: i.RepositorySelection,
			Org: as.org, Connections: conns[i.ID], Suspended: i.SuspendedAt != nil, URL: i.HTMLURL,
		})
	}
	for id, as := range assigned {
		if !seen[id] {
			out.Installations = append(out.Installations, apitypes.GitHubInstallation{
				ID: id, Account: as.account, Org: as.org, Connections: conns[id], Missing: err == nil,
			})
		}
	}
	slices.SortFunc(out.Installations, func(x, y apitypes.GitHubInstallation) int { return cmp.Compare(x.ID, y.ID) })
	return out
}

// installationConnections counts the services connected through each
// installation.
func (c *Core) installationConnections(ctx context.Context) (map[int64]int, error) {
	counts, err := c.store.CountInstallationConnections(ctx)
	conns := make(map[int64]int, len(counts))
	for _, n := range counts {
		conns[n.InstallationID] = int(n.Connections)
	}
	return conns, err
}

// GitHubStatus describes every App, where each receives pushes, and its
// installations: GitHub's list merged with Dock's assignments. Operators
// only.
func (c *Core) GitHubStatus(ctx context.Context, a Actor) (apitypes.GitHubStatus, error) {
	if err := a.requirePlatform(PlatformView, "reading the GitHub App"); err != nil {
		return apitypes.GitHubStatus{}, err
	}
	out := apitypes.GitHubStatus{
		HooksHint: c.hooksHint(), CallbackURL: c.dockURL() + GitHubUserCallbackPath,
		Apps: []apitypes.GitHubAppStatus{}, Installations: []apitypes.GitHubInstallation{},
	}
	orgs, err := c.store.ListOrgSlugs(ctx)
	if err != nil {
		return out, err
	}
	out.Orgs = orgs
	apps, err := c.store.ListGitHubApps(ctx)
	if err != nil || len(apps) == 0 {
		return out, err
	}
	rows, err := c.store.ListGitHubInstallations(ctx)
	if err != nil {
		return out, err
	}
	conns, err := c.installationConnections(ctx)
	if err != nil {
		return out, err
	}
	byApp := map[int64]map[int64]assignedInstallation{}
	for _, r := range rows {
		if byApp[r.AppID] == nil {
			byApp[r.AppID] = map[int64]assignedInstallation{}
		}
		byApp[r.AppID][r.InstallationID] = assignedInstallation{org: r.OrgSlug, account: r.Account}
	}
	for _, r := range apps {
		out.Apps = append(out.Apps, c.appStatus(ctx, r.GithubApp, r.OrgSlug, byApp[r.GithubApp.ID], conns, true))
	}
	if first := out.Apps[0]; first.App.Org == "" {
		out.App, out.WebhookURL, out.Installations, out.InstallationsError = &first.App, first.WebhookURL, first.Installations, first.InstallationsError
	}
	return out, nil
}

// githubLoginRe matches GitHub account names.
var githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](-?[A-Za-z0-9]){0,38}$`)

// StartGitHubApp begins registering an App: the platform's (platform, for
// operators) or the actor's org's own (for its unlimited admins, always
// private). It returns the state for the page that posts the manifest to
// GitHub (GitHubManifest). Needs a public address, since GitHub delivers
// pushes there.
func (c *Core) StartGitHubApp(ctx context.Context, a Actor, platform bool, req apitypes.GitHubAppRequest) (string, error) {
	owner, err := connectionsIn(a, platform, "creating a GitHub App")
	if err != nil {
		return "", err
	}
	var org store.Org
	if owner != 0 {
		if req.Public {
			return "", invalid("an organization's own GitHub App is private: only its GitHub account installs it. An operator makes a public App for every organization with `dock github app create --platform --public`")
		}
		if org, err = c.store.GetOrg(ctx, owner); err != nil {
			return "", err
		}
	}
	login := strings.TrimSpace(req.Owner)
	if login != "" && !githubLoginRe.MatchString(login) {
		return "", invalid("%q is not a GitHub organization name", login)
	}
	if c.hooksHost() == "" {
		return "", invalid("GitHub needs a public address to send pushes to; an operator sets one with `dock address set <hostname>`")
	}
	apps, err := c.store.ListGitHubApps(ctx)
	if err != nil {
		return "", err
	}
	if n := len(slices.DeleteFunc(apps, func(r store.ListGitHubAppsRow) bool { return appOrg(r.GithubApp) != owner })); n >= maxGitHubApps {
		return "", conflict("there are already %d GitHub Apps here, the most one owner has; remove one first with `dock github app remove <slug>`", n)
	}
	return c.git.app.startFlow(githubFlow{kind: flowManifest, actor: a, org: org, owner: login, hookID: randomHex(16), public: req.Public}), nil
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
	CallbackURLs       []string          `json:"callback_urls,omitempty"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
}

// GitHubManifest returns where to post the App's manifest and the
// manifest itself, for a state from StartGitHubApp. origin is the Dock
// URL the browser has open; GitHub sends it back there unless Dock has a
// public address. Only the platform's Apps get an OAuth callback: an
// org's own doesn't check who installs it.
func (c *Core) GitHubManifest(state, origin string) (string, []byte, error) {
	f, err := c.git.app.flow(state, flowManifest, false)
	if err != nil {
		return "", nil, err
	}
	hook := c.hookURL(f.hookID)
	if hook == "" {
		return "", nil, invalid("GitHub needs a public address to send pushes to; an operator sets one with `dock address set <hostname>`")
	}
	base := cmp.Or(c.publicURL(), origin)
	m := githubManifest{
		Name:        appName(cmp.Or(f.org.Slug, c.hooksHost())),
		URL:         base,
		Description: "Deploys pushes and previews pull requests with Tempo Dock.",
		RedirectURL: base + GitHubAppCallbackPath,
		SetupURL:    base + GitHubInstallCallbackPath, SetupOnUpdate: true,
		Public: f.public,
		// Pull requests: write lets previews comment (ADR 0056).
		DefaultPermissions: map[string]string{"contents": "read", "metadata": "read", "statuses": "write", "pull_requests": "write"},
		DefaultEvents:      []string{"push", "pull_request"},
	}
	if f.org.ID == 0 {
		m.CallbackURLs = []string{base + GitHubUserCallbackPath}
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

// appName suggests the App's name from the org's slug or, for the
// platform's, Dock's hostname: GitHub wants it unique and at most 34
// characters; the person can change it on GitHub's form.
func appName(of string) string {
	name := "Tempo Dock " + strings.ReplaceAll(of, ".", "-")
	return strings.TrimSpace(name[:min(34, len(name))])
}

// CompleteGitHubApp finishes registering an App: it exchanges the code
// GitHub redirected with for the App's credentials and stores them,
// sealed. The state is the authority (see the top of this file).
func (c *Core) CompleteGitHubApp(ctx context.Context, code, state string) (apitypes.GitHubApp, error) {
	f, err := c.git.app.flow(state, flowManifest, true)
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	if !manifestCodeRe.MatchString(code) {
		return apitypes.GitHubApp{}, invalid("GitHub's redirect has no valid code; start again with `dock github app create`")
	}
	created, err := c.git.github.convertManifest(ctx, code)
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	if _, err := parseAppKey([]byte(created.PEM)); err != nil {
		return apitypes.GitHubApp{}, err
	}
	key, err := c.box.Seal([]byte(created.PEM), appKeyAAD(f.hookID))
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	secret, err := c.box.Seal([]byte(created.WebhookSecret), appWebhookSecretAAD(f.hookID))
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	var clientSecret []byte
	if f.org.ID == 0 && created.ClientID != "" && created.ClientSecret != "" {
		if clientSecret, err = c.box.Seal([]byte(created.ClientSecret), appClientSecretAAD(f.hookID)); err != nil {
			return apitypes.GitHubApp{}, err
		}
	}
	var orgID *int64
	if f.org.ID != 0 {
		orgID = &f.org.ID
	}
	row, err := c.store.CreateGitHubApp(ctx, store.CreateGitHubAppParams{
		OrgID: orgID, AppID: created.ID, HookID: f.hookID, Slug: cleanLine(created.Slug, 100), Name: cleanLine(created.Name, 100),
		Owner: cleanLine(created.Owner.Login, 39), OwnerID: created.Owner.ID, HtmlUrl: cleanLine(created.HTMLURL, 200),
		PrivateKey: key, WebhookSecret: secret, ClientID: cleanLine(created.ClientID, 100), ClientSecret: clientSecret,
		CreatedBy: f.actor.Name, CreatedAt: now(),
	})
	if store.IsUnique(err) {
		return apitypes.GitHubApp{}, conflict("Dock already has the GitHub App %s", created.Slug)
	}
	if err != nil {
		return apitypes.GitHubApp{}, err
	}
	in := f.actor
	in.OrgID, in.OrgSlug = f.org.ID, f.org.Slug
	c.audit(ctx, cfAudit(in, f.org.ID), "github.app_create", nil, nil, row.Slug, fmt.Sprintf("created the GitHub App %s on %s", row.Slug, row.Owner))
	return toGitHubApp(row, f.org.Slug), nil
}

// manifestCodeRe matches the codes GitHub's manifest flow redirected with.
var manifestCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// manageApp loads the App with slug for a change by a: a platform App for
// operators, an org's own for its unlimited admins (another org's is not
// found for them) or operators.
func (c *Core) manageApp(ctx context.Context, a Actor, slug, action string) (store.GithubApp, error) {
	app, err := c.store.GetGitHubAppBySlug(ctx, strings.TrimSpace(slug))
	if err != nil && !store.IsNotFound(err) {
		return app, err
	}
	if err == nil && (app.OrgID == nil || a.Operator) {
		return app, a.requirePlatform(PlatformSettings, action)
	}
	if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
		return app, err
	}
	if err != nil || appOrg(app) != a.OrgID {
		return app, notFound("no GitHub App %q; `dock github installations` lists the organization's", slug)
	}
	return app, nil
}

// RemoveGitHubApp forgets an App and its installations' assignments.
// Services connected through it stay connected but can't fetch until they
// reconnect, or another App's installation assigned to their org delivers
// a push or pull request for their repository (adoptAppConnections). The
// App itself stays on GitHub; delete it there.
func (c *Core) RemoveGitHubApp(ctx context.Context, a Actor, slug string) error {
	app, err := c.manageApp(ctx, a, slug, "removing a GitHub App")
	if err != nil {
		return err
	}
	if err := c.store.DeleteGitHubApp(ctx, app.ID); err != nil {
		return err
	}
	c.git.app.forgetTokens(0)
	summary := "removed the GitHub App " + app.Slug
	switch org := appOrg(app); {
	case org == 0:
		c.audit(ctx, a.platform(), "github.app_remove", nil, nil, app.Slug, summary)
	case a.Operator && a.OrgID != org:
		// An operator removing an org's own App: both logs (ADR 0062).
		o, err := c.store.GetOrg(ctx, org)
		if err != nil {
			return err
		}
		c.auditBoth(ctx, a, o, "github.app_remove", summary)
	default:
		c.audit(ctx, a, "github.app_remove", nil, nil, app.Slug, summary)
	}
	c.connectionWorks(ctx, githubAppAlertKey(app.ID))
	return nil
}

// StartGitHubInstall returns the install link of an App for an org:
// Dock's start page at origin (or Dock's public address), which only a's
// person may open (BindGitHubInstall) and which goes on to the App's
// install page on GitHub. GitHub's redirect back (CompleteGitHubInstall)
// then leads to the installation being the org's. An org's unlimited
// admins install for their own org (req.Org empty or theirs); operators
// for any. req.App picks one of the Apps the org may use, and may be left
// out while there is one. A person's credential only: a token of no
// one's has no browser to finish in.
func (c *Core) StartGitHubInstall(ctx context.Context, a Actor, req apitypes.GitHubInstallRequest, origin string) (apitypes.GitHubLink, error) {
	org, err := c.installOrg(ctx, a, strings.TrimSpace(req.Org))
	if err != nil {
		return apitypes.GitHubLink{}, err
	}
	if a.UserID == 0 {
		return apitypes.GitHubLink{}, invalid("a GitHub App is installed by a person, who finishes in the browser they are signed in to Dock with; run `dock login`, then `dock github install`")
	}
	app, err := c.installApp(ctx, org, strings.TrimSpace(req.App))
	if err != nil {
		return apitypes.GitHubLink{}, err
	}
	if app.OrgID == nil && app.ClientSecret == nil && !a.Operator {
		return apitypes.GitHubLink{}, errNoAppClient()
	}
	state := c.git.app.startFlow(githubFlow{kind: flowInstall, actor: a, org: org, app: app.ID})
	return apitypes.GitHubLink{URL: cmp.Or(c.publicURL(), origin) + GitHubInstallStartPath + "?state=" + url.QueryEscape(state)}, nil
}

// orgApps are the Apps org may deploy through: its own first, oldest
// first, then the platform's.
func (c *Core) orgApps(ctx context.Context, org int64) ([]store.GithubApp, error) {
	apps, err := c.store.ListOrgGitHubApps(ctx, &org)
	slices.SortFunc(apps, func(x, y store.GithubApp) int {
		return cmp.Or(cmp.Compare(appOrg(y), appOrg(x)), cmp.Compare(x.ID, y.ID))
	})
	return apps, err
}

// installApp is the App org installs: the one named slug among those it
// may use (its own and the platform's), or the only one.
func (c *Core) installApp(ctx context.Context, org store.Org, slug string) (store.GithubApp, error) {
	apps, err := c.orgApps(ctx, org.ID)
	if err != nil {
		return store.GithubApp{}, err
	}
	if slug != "" {
		for _, app := range apps {
			if app.Slug == slug {
				return app, nil
			}
		}
		return store.GithubApp{}, notFound("org %s has no GitHub App %q; `dock github installations` lists the ones it may install", org.Slug, slug)
	}
	switch len(apps) {
	case 0:
		return store.GithubApp{}, noGitHubApp()
	case 1:
		return apps[0], nil
	}
	slugs := make([]string, len(apps))
	for i, app := range apps {
		slugs[i] = app.Slug
	}
	return store.GithubApp{}, invalid("org %s may install several GitHub Apps (%s); name one with --app", org.Slug, strings.Join(slugs, ", "))
}

// BindGitHubInstall opens an install link for a, the session of the
// browser it was opened in: if a is the person who asked for the link,
// it binds the flow to that browser and returns the App's install page on
// GitHub to go on to, and the binding for the browser's cookie. Someone
// else's link is refused, so a link sent to a GitHub account's owner
// can't install their account for the sender's org.
func (c *Core) BindGitHubInstall(ctx context.Context, a Actor, state string) (redirect, binding string, err error) {
	binding, id, err := c.git.app.bind(state, a.UserID)
	if err != nil {
		return "", "", err
	}
	app, err := c.githubApp(ctx, id)
	if err != nil {
		return "", "", err
	}
	return app.HtmlUrl + "/installations/new?state=" + url.QueryEscape(state), binding, nil
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
		if to, _, err := c.movedSlug(ctx, slug); err != nil {
			return store.Org{}, err
		} else if to != a.OrgSlug {
			return store.Org{}, errf(apitypes.CodeForbidden, "an organization's admins install GitHub Apps for their own organization only; an operator installs one for org %s", slug)
		}
	}
	org, err := c.store.GetOrg(ctx, a.OrgID)
	if store.IsNotFound(err) {
		return org, notFound("no organization to install the GitHub App for; pass --org")
	}
	return org, err
}

// recordOrgInstallation gives an installation of an org's own App to that
// org, if it is on the App's owner account: a private App installs only
// there, and an App made public on GitHub later doesn't hand the org
// another account's repositories. inst is GitHub's word, from the App's
// own API or its signed webhook, never from a redirect's URL. a is who
// installed it, for the org's audit log.
func (c *Core) recordOrgInstallation(ctx context.Context, a Actor, app store.GithubApp, inst githubInstallation) error {
	org := appOrg(app)
	if org == 0 {
		return fmt.Errorf("recordOrgInstallation: %s is the platform's App", app.Slug)
	}
	account := cleanLine(inst.Account.Login, 39)
	if inst.Account.ID != app.OwnerID {
		return errf(apitypes.CodeForbidden, "the GitHub App %s is installed on %s, which doesn't own it, so Dock doesn't deploy from it; install it on %s, the account that owns it", app.Slug, account, app.Owner)
	}
	if err := c.store.SetGitHubInstallation(ctx, store.SetGitHubInstallationParams{
		InstallationID: inst.ID, AppID: app.ID, OrgID: org, Account: account, AssignedBy: a.Name, AssignedAt: now(),
	}); err != nil {
		return err
	}
	in := a.InOrg(org)
	c.audit(ctx, in, "github.install_assign", nil, nil, account, fmt.Sprintf("the organization deploys from %s's repositories through its GitHub App %s (installation %d)", account, app.Slug, inst.ID))
	return nil
}

// AssignGitHubInstallation lets an org deploy from an installation of a
// platform App (req.App, or whichever has it), moving it from another
// org if needed (that org's services stop fetching from it). Operators
// only.
func (c *Core) AssignGitHubInstallation(ctx context.Context, a Actor, id int64, req apitypes.GitHubInstallRequest) error {
	if err := a.requirePlatform(PlatformSettings, "assigning a GitHub App installation"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Org) == "" {
		return invalid("name the org to assign installation %d to", id)
	}
	org, err := c.orgNamed(ctx, req.Org)
	if err != nil {
		return err
	}
	app, inst, err := c.platformInstallation(ctx, id, strings.TrimSpace(req.App))
	if err != nil {
		return err
	}
	return c.assignInstallation(ctx, a, app, inst, org, true)
}

// platformInstallation finds installation id among the platform's Apps
// (only the one named slug, if given), as GitHub describes it.
func (c *Core) platformInstallation(ctx context.Context, id int64, slug string) (store.GithubApp, githubInstallation, error) {
	apps, err := c.orgApps(ctx, 0)
	if err != nil {
		return store.GithubApp{}, githubInstallation{}, err
	}
	for _, app := range apps {
		if slug != "" && app.Slug != slug {
			continue
		}
		jwt, err := c.appJWT(app)
		if err != nil {
			return app, githubInstallation{}, err
		}
		inst, err := c.git.github.installation(ctx, jwt, id)
		if CodeOf(err) == apitypes.CodeNotFound {
			continue
		}
		return app, inst, err
	}
	if slug != "" {
		return store.GithubApp{}, githubInstallation{}, notFound("the platform has no GitHub App %q with installation %d; see `dock github status`", slug, id)
	}
	return store.GithubApp{}, githubInstallation{}, notFound("no GitHub App of the platform has installation %d; see `dock github status`", id)
}

// assignInstallation assigns installation inst of the platform App app to
// org. Unless move is set, an installation another org has is refused
// (conflict) and stays with it.
func (c *Core) assignInstallation(ctx context.Context, a Actor, app store.GithubApp, inst githubInstallation, org store.Org, move bool) error {
	if app.OrgID != nil {
		return fmt.Errorf("assignInstallation: %s is an org's App", app.Slug)
	}
	account := cleanLine(inst.Account.Login, 39)
	var err error
	if move {
		err = c.store.SetGitHubInstallation(ctx, store.SetGitHubInstallationParams{
			InstallationID: inst.ID, AppID: app.ID, OrgID: org.ID, Account: account, AssignedBy: a.Name, AssignedAt: now(),
		})
	} else {
		var n int64
		n, err = c.store.ClaimGitHubInstallation(ctx, store.ClaimGitHubInstallationParams{
			InstallationID: inst.ID, AppID: app.ID, OrgID: org.ID, Account: account, AssignedBy: a.Name, AssignedAt: now(),
		})
		if err == nil && n == 0 {
			err = conflict("installation %d (%s) is assigned to another org; to move it to org %s, an operator runs `dock github assign %d %s`", inst.ID, account, org.Slug, inst.ID, org.Slug)
		}
	}
	if err != nil {
		return err
	}
	in := a
	in.OrgID, in.OrgSlug = org.ID, org.Slug
	c.audit(ctx, in, "github.install_assign", nil, nil, account, fmt.Sprintf("org %s deploys from %s's repositories through the GitHub App %s (installation %d)", org.Slug, account, app.Slug, inst.ID))
	return nil
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

// GitHubOrgStatus describes the Apps the actor's org may deploy through:
// its own, with every installation GitHub lists for each, and the
// platform's, with the org's installations of them (GitHub is asked only
// when the org has some). For the org's unlimited admins; operators see
// every installation in GitHubStatus.
func (c *Core) GitHubOrgStatus(ctx context.Context, a Actor) (apitypes.GitHubOrg, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "reading the organization's GitHub App installations"); err != nil {
		return apitypes.GitHubOrg{}, err
	}
	out := apitypes.GitHubOrg{Apps: []apitypes.GitHubAppStatus{}, Installations: []apitypes.GitHubInstallation{}}
	apps, err := c.orgApps(ctx, a.OrgID)
	if err != nil || len(apps) == 0 {
		return out, err
	}
	own, err := c.store.ListOrgGitHubInstallations(ctx, a.OrgID)
	if err != nil {
		return out, err
	}
	conns, err := c.installationConnections(ctx)
	if err != nil {
		return out, err
	}
	for _, app := range apps {
		assigned := map[int64]assignedInstallation{}
		for _, r := range own {
			if r.AppID == app.ID {
				assigned[r.InstallationID] = assignedInstallation{org: a.OrgSlug, account: r.Account}
			}
		}
		org := ""
		if app.OrgID != nil {
			org = a.OrgSlug
		}
		st := c.appStatus(ctx, app, org, assigned, conns, app.OrgID != nil)
		out.Apps = append(out.Apps, st)
		if app.OrgID == nil && out.App == nil {
			out.App = &apitypes.GitHubOrgApp{Slug: app.Slug, Name: app.Name, Owner: app.Owner, URL: app.HtmlUrl}
			out.Installations, out.InstallationsError = st.Installations, st.InstallationsError
		}
	}
	return out, nil
}

// orgNamed looks an org up by slug.
func (c *Core) orgNamed(ctx context.Context, slug string) (store.Org, error) {
	org, err := c.orgBySlug(ctx, strings.TrimSpace(slug))
	if store.IsNotFound(err) {
		return org, notFound("no org %q; see `dock org list`", slug)
	}
	return org, err
}

// GitHubRepos lists the repositories the caller's org can connect
// through its Apps' installations, the newest push first, with the org's
// services that deploy from each. A repository two installations read is
// listed once. Scope: deploy.
func (c *Core) GitHubRepos(ctx context.Context, a Actor) (apitypes.GitHubRepos, error) {
	if err := a.require(apitypes.ScopeDeploy, "listing GitHub repositories"); err != nil {
		return apitypes.GitHubRepos{}, err
	}
	out := apitypes.GitHubRepos{Repos: []apitypes.GitHubRepo{}}
	insts, err := c.store.ListOrgGitHubInstallations(ctx, a.OrgID)
	if err != nil {
		return out, err
	}
	seen := map[int64]bool{}
	for _, inst := range insts {
		token, err := c.installationToken(ctx, inst.AppID, inst.InstallationID)
		if err != nil {
			return out, err
		}
		repos, more, err := c.git.github.installationRepos(ctx, token, maxGitHubRepos-len(out.Repos))
		if err != nil {
			return out, err
		}
		for _, r := range repos {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			out.Repos = append(out.Repos, apitypes.GitHubRepo{ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private, PushedAt: r.PushedAt})
		}
		if more || len(out.Repos) >= maxGitHubRepos {
			out.Truncated = true
			break
		}
	}
	used, err := c.store.ListOrgGitServices(ctx, a.OrgID)
	if err != nil {
		return out, err
	}
	for i, r := range out.Repos {
		out.Repos[i].UsedBy = usedBy(used, githubRepo{ID: r.ID, FullName: r.FullName}, nil)
	}
	slices.SortFunc(out.Repos, func(x, y apitypes.GitHubRepo) int {
		switch {
		case x.PushedAt != nil && y.PushedAt != nil && !x.PushedAt.Equal(*y.PushedAt):
			return y.PushedAt.Compare(*x.PushedAt)
		case (x.PushedAt == nil) != (y.PushedAt == nil):
			if x.PushedAt == nil {
				return 1
			}
			return -1
		}
		return strings.Compare(strings.ToLower(x.FullName), strings.ToLower(y.FullName))
	})
	return out, nil
}

// appInstall is an installation of an App: the App's Dock ID and
// GitHub's installation ID.
type appInstall struct {
	app, id int64
}

// appRepo resolves a repository for an App connection in org: the
// installation that reads it, which must be org's, and the repository's
// ID and canonical name. The org's own Apps are asked first, oldest
// first, then the platform's; the first whose installation reads the
// repository and is the org's wins.
func (c *Core) appRepo(ctx context.Context, org store.Org, repo string) (appInstall, githubRepo, error) {
	apps, err := c.orgApps(ctx, org.ID)
	if err != nil {
		return appInstall{}, githubRepo{}, err
	}
	owner, _, _ := strings.Cut(repo, "/")
	var refused error
	for _, app := range apps {
		jwt, err := c.appJWT(app)
		if err != nil {
			return appInstall{}, githubRepo{}, err
		}
		inst, err := c.git.github.repoInstallation(ctx, jwt, repo)
		if CodeOf(err) == apitypes.CodeNotFound {
			continue
		}
		if err != nil {
			return appInstall{}, githubRepo{}, err
		}
		assigned, err := c.store.GetGitHubInstallation(ctx, inst.ID)
		if err != nil && !store.IsNotFound(err) {
			return appInstall{}, githubRepo{}, err
		}
		if err != nil || assigned.OrgID != org.ID {
			if refused == nil {
				refused = c.notOrgsInstallation(app, inst, org, repo)
			}
			continue
		}
		at := appInstall{app: app.ID, id: inst.ID}
		token, err := c.installationToken(ctx, at.app, at.id)
		if err != nil {
			return appInstall{}, githubRepo{}, err
		}
		r, err := c.git.github.repo(ctx, token, repo)
		if err != nil {
			return appInstall{}, githubRepo{}, err
		}
		if r.ID == 0 || !repoRe.MatchString(r.FullName) {
			return appInstall{}, githubRepo{}, fmt.Errorf("GitHub described %s without an ID or name", repo)
		}
		return at, r, nil
	}
	if refused != nil {
		return appInstall{}, githubRepo{}, refused
	}
	return appInstall{}, githubRepo{}, notFound("no GitHub App org %s may use is installed on %s; an admin of org %s installs one on %s's account (Settings > GitHub, or `dock github install`), or connect with --mode webhook", org.Slug, repo, org.Slug, owner)
}

// notOrgsInstallation refuses an installation of app that reads repo but
// isn't org's: an org's own App's installation Dock hasn't recorded (on
// another account, or its install event was missed), or a platform App's
// installation assigned elsewhere or to no one.
func (c *Core) notOrgsInstallation(app store.GithubApp, inst githubInstallation, org store.Org, repo string) error {
	account := cleanLine(inst.Account.Login, 39)
	if app.OrgID != nil {
		if inst.Account.ID != app.OwnerID {
			return errf(apitypes.CodeForbidden, "the GitHub App %s reads %s through an installation on %s, which doesn't own the App, so Dock doesn't deploy from it", app.Slug, repo, account)
		}
		return errf(apitypes.CodeForbidden, "Dock hasn't recorded the GitHub App %s's installation on %s yet; install it again from Dock (Settings > GitHub, or `dock github install --app %s`)", app.Slug, account, app.Slug)
	}
	return errf(apitypes.CodeForbidden, "the GitHub App %s reads %s through %s's installation, which org %s may not deploy from; an operator assigns it with `dock github assign %d %s`",
		app.Slug, repo, account, org.Slug, inst.ID, org.Slug)
}

// gitCred is how a connection authenticates to GitHub: its own token, or
// its App's installation token. An App connection only gets one while its
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
		return githubCred{}, errf(apitypes.CodeForbidden, "the GitHub App installation that reads %s is no longer assigned to this org; see Settings > GitHub (`dock github installations`), or reconnect with `dock git connect %s --mode webhook`", conn.Repo, conn.Repo)
	}
	token, err := c.installationToken(ctx, inst.AppID, conn.InstallationID)
	return githubCred{token: token, kind: credInstall}, err
}
