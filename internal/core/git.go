package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/httpkit"
	"github.com/NordTempo/tempoDock/internal/store"
)

// GitHub push-to-deploy (ADR 0018). A service connects a repository,
// branch and optional subdirectory. GitHub posts every push to the
// connection's webhook (git_hook.go); a push to the branch is queued on
// the connection (pending_*), replacing any older queued push, and the
// service's git worker turns it into a source release: it fetches the
// commit's tarball from GitHub (git_github.go), re-roots it at the
// subdirectory (git_tar.go) and hands it to the same pipeline as `dock
// deploy`. A running deploy is never cancelled; the push waits for it.
//
// A connection either has its own webhook and optional token (mode
// webhook), or goes through the install's GitHub App (mode app, ADR 0031,
// github_app.go): the App's one webhook delivers every push and its
// installation tokens read the repository.

// gitHookPath is the webhook path prefix, on [hooks] public_host and on
// Dock's public address.
const gitHookPath = "/hooks/github/"

// gitRetryDelay is how long a worker waits when the service's upload slot
// is busy (a `dock deploy` upload is streaming, or two uploads are), before
// it looks again. It runs only while a push is queued and blocked.
const gitRetryDelay = 10 * time.Second

// gitState is push-to-deploy's in-memory state.
type gitState struct {
	// mu guards workers; Close holds it while cancelling, so no worker
	// starts after shutdown began.
	mu      sync.Mutex
	workers map[int64]*gitWorker // by service ID, while one runs
	// eventMu orders gitEventIn's write and publish, so two deliveries
	// for one connection reach the bus in the order the store took them
	// and a page never ends on the older last event. Only gitEventIn
	// takes it.
	eventMu sync.Mutex
	// limit throttles webhook deliveries per hook; appLimit the App's,
	// which carry every App connection's pushes. Both count only
	// deliveries whose signature checked out, so ones with a wrong
	// signature never use up the sender's allowance. badLimit counts
	// those, per hook and client (badSignature).
	limit    *httpkit.Limiter
	appLimit *httpkit.Limiter
	badLimit *httpkit.Limiter
	github   githubClient
	app      githubAppState
	// statusMu guards statusTail: per service, the last commit status
	// post queued. Each post waits for the one before it, so GitHub sees
	// pending before success (it shows the newest status per context).
	statusMu   sync.Mutex
	statusTail map[int64]chan struct{}
}

// gitWorker is the state of a service's running worker.
type gitWorker struct {
	// again is set by a kick while the worker runs: it looks once more
	// before exiting.
	again bool
	// sha is the commit being fetched and cancel stops that fetch, when a
	// newer push replaces it.
	sha    string
	cancel context.CancelFunc
}

func newGitState(api string) gitState {
	return gitState{
		workers:    map[int64]*gitWorker{},
		statusTail: map[int64]chan struct{}{},
		// GitHub sends one delivery per push; bursts beyond this are
		// either a flood or a stolen URL.
		limit:    httpkit.NewLimiter(30, 2*time.Second),
		appLimit: httpkit.NewLimiter(120, 500*time.Millisecond),
		badLimit: httpkit.NewLimiter(10, 6*time.Second),
		github:   newGitHubClient(api),
		app:      newGitHubAppState(),
	}
}

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
	dirRe    = regexp.MustCompile(`^[A-Za-z0-9._ @+-]+(/[A-Za-z0-9._ @+-]+)*$`)
	shaRe    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// validGitTarget checks and normalizes a connection's repo, branch and dir.
func validGitTarget(repo, branch, dir string) (string, string, string, error) {
	repo = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(repo), "https://github.com/"), ".git")
	if !repoRe.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") {
		return "", "", "", invalid("%q is not a GitHub repository; use owner/name, e.g. acme/shop", repo)
	}
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if !branchRe.MatchString(branch) || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".lock") {
		return "", "", "", invalid("%q is not a valid branch name", branch)
	}
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	if dir == "." {
		dir = ""
	}
	if dir != "" {
		if !dirRe.MatchString(dir) {
			return "", "", "", invalid("%q is not a valid subdirectory; use a relative path like apps/web", dir)
		}
		for part := range strings.SplitSeq(dir, "/") {
			if part == "." || part == ".." {
				return "", "", "", invalid("subdirectory %q may not contain . or ..", dir)
			}
		}
	}
	return repo, branch, dir, nil
}

// randomHex returns n random bytes, hex encoded.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GitStatus returns a service's GitHub source, if any. Scope: read.
func (c *Core) GitStatus(ctx context.Context, a Actor, ref ServiceRef) (apitypes.GitSource, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading the GitHub source")
	if err != nil {
		return apitypes.GitSource{}, err
	}
	out := apitypes.GitSource{HooksReady: c.hooksHost() != "", SameRepo: []apitypes.GitSibling{}}
	insts, err := c.store.ListOrgGitHubInstallations(ctx, ss.project.OrgID)
	if err != nil {
		return out, err
	}
	out.AppReady = len(insts) > 0
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	gc := c.toGitConnection(conn)
	out.Connection = &gc
	// Only a connected service waits on pushes, so only it gets the hint.
	out.HooksHint = c.hooksHint()
	conns, err := c.store.ListEnvGitConnections(ctx, ss.env.ID)
	if err != nil {
		return out, err
	}
	svcs, err := c.store.ListServices(ctx, ss.env.ID)
	if err != nil {
		return out, err
	}
	for _, other := range conns {
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.ID == other.ServiceID })
		same := (conn.RepoID != 0 && other.RepoID == conn.RepoID) || strings.EqualFold(other.Repo, conn.Repo)
		if other.ServiceID != conn.ServiceID && same && i >= 0 && svcs[i].ArchivedAt == nil {
			out.SameRepo = append(out.SameRepo, apitypes.GitSibling{Service: svcs[i].Name, Branch: other.Branch, Dir: other.Dir})
		}
	}
	return out, nil
}

// hooksHint says how to give webhooks a public address, while there is
// none.
func (c *Core) hooksHint() string {
	if c.hooksHost() != "" {
		return ""
	}
	return "No public address for GitHub to deliver pushes to: set Dock's public address with `dock address set <hostname>` (or [hooks] public_host in config.toml)."
}

func (c *Core) toGitConnection(conn store.GitConnection) apitypes.GitConnection {
	out := apitypes.GitConnection{
		Mode: apitypes.GitModeWebhook, Repo: conn.Repo, Branch: conn.Branch, Dir: conn.Dir, TokenSet: len(conn.Token) > 0, Status: conn.PostStatus,
		WebhookURL: c.hookURL(conn.HookID), LastEvent: conn.LastEvent, LastEventAt: conn.LastEventAt,
		CreatedBy: conn.CreatedBy, CreatedAt: conn.CreatedAt,
	}
	if conn.InstallationID != 0 {
		out.Mode, out.WebhookURL = apitypes.GitModeApp, ""
	}
	if conn.PendingSha != "" {
		out.Pending = &apitypes.Commit{SHA: conn.PendingSha, Message: conn.PendingMessage, Author: conn.PendingAuthor}
	}
	return out
}

// hookURL is the webhook's public URL, or "" without a public host
// (hooksHost). It is served by the control plane's own Caddy
// (withControlRoutes).
func (c *Core) hookURL(hookID string) string {
	host := c.hooksHost()
	if host == "" {
		return ""
	}
	return c.controlURL(host) + gitHookPath + hookID
}

// GitConnect connects a service to a GitHub repository and branch, or
// changes its connection. Through the GitHub App, the repository must be
// readable by an installation assigned to the service's org. With a
// webhook, a new connection (or RotateSecret, or a switch from the App)
// gets a new webhook URL and secret, returned once. Scope: deploy.
func (c *Core) GitConnect(ctx context.Context, a Actor, ref ServiceRef, req apitypes.GitConnectRequest) (apitypes.GitConnectResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "connecting a GitHub repository")
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
	return c.connectGit(ctx, a, ss, req, nil)
}

// appRepoTarget is a repository already resolved and checked through the
// App: the installation that reads it and the repository.
type appRepoTarget struct {
	installationID int64
	repo           githubRepo
	// branch is the branch checked: the one asked for, or the
	// repository's default.
	branch string
}

// connectGit is GitConnect for a loaded service. pre, when set, is the
// App repository the caller resolved and checked already (creating a
// service from a repository does that before it creates anything).
func (c *Core) connectGit(ctx context.Context, a Actor, ss svcScope, req apitypes.GitConnectRequest, pre *appRepoTarget) (apitypes.GitConnectResult, error) {
	if ss.svc.ArchivedAt != nil {
		return apitypes.GitConnectResult{}, invalid("service %s is archived", ss)
	}
	repo, branch, dir, err := validGitTarget(req.Repo, req.Branch, req.Dir)
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
	old, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	exists := err == nil
	if err != nil && !store.IsNotFound(err) {
		return apitypes.GitConnectResult{}, err
	}
	mode, err := c.gitMode(ctx, ss, req.Mode, old, exists)
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
	wasApp := exists && old.InstallationID != 0
	t := gitTargetChange{token: old.Token, status: old.PostStatus, hookID: old.HookID, secret: old.Secret}
	if mode == apitypes.GitModeApp {
		switch {
		case req.Token != nil && *req.Token != "":
			err = invalid("the GitHub App reads the repository, so it needs no token; drop the token, or connect with --mode webhook")
		case pre == nil:
			pre, err = c.resolveAppTarget(ctx, ss.project.OrgID, repo, branch, dir)
		}
		if err == nil {
			err = c.appTarget(ss, req, *pre, &repo, &t, !wasApp)
		}
	} else {
		fresh := !exists || wasApp
		if !fresh && len(old.Token) > 0 && req.Token == nil && !strings.EqualFold(old.Repo, repo) {
			// The token was given for the repository it was stored
			// with. Whoever may change a service's repository hasn't
			// been given the token (the API never shows it), and it may
			// read more than this one: it doesn't follow the change.
			err = invalid("%s's GitHub token was given for %s, so it isn't used for %s; pass a token for the new repository (--token-stdin), or an empty one to connect without", ss, old.Repo, repo)
		} else {
			err = c.webhookTarget(ss.svc.ID, req, &t, fresh)
		}
	}
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
	var conn store.GitConnection
	if exists {
		conn, err = c.store.UpdateGitConnection(ctx, store.UpdateGitConnectionParams{
			Repo: repo, Branch: branch, Dir: dir, Token: t.token, PostStatus: t.status, HookID: t.hookID, Secret: t.secret,
			InstallationID: t.installationID, RepoID: t.repoID, ID: old.ID,
		})
	} else {
		conn, err = c.store.CreateGitConnection(ctx, store.CreateGitConnectionParams{
			ServiceID: ss.svc.ID, HookID: t.hookID, Repo: repo, Branch: branch, Dir: dir, Secret: t.secret, Token: t.token,
			PostStatus: t.status, InstallationID: t.installationID, RepoID: t.repoID, CreatedBy: a.Name,
		})
	}
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
	action, summary := "git.connect", fmt.Sprintf("connected %s", gitTarget(conn))
	if exists {
		action, summary = "git.update", fmt.Sprintf("GitHub source is now %s", gitTarget(conn))
	}
	summary += "; " + t.change
	if t.status {
		summary += "; commit statuses on"
	}
	c.audit(ctx, a, action, &ss.project, &ss.env, ss.String(), summary)
	res := apitypes.GitConnectResult{Connection: c.toGitConnection(conn), Created: !exists, Secret: t.newSecret}
	if mode == apitypes.GitModeWebhook {
		res.HooksHint = c.hooksHint()
	}
	return res, nil
}

// gitMode is the mode a connection gets: the one asked for; else an
// existing connection's; else the App when the org has an installation.
func (c *Core) gitMode(ctx context.Context, ss svcScope, asked apitypes.GitMode, old store.GitConnection, exists bool) (apitypes.GitMode, error) {
	switch {
	case asked == apitypes.GitModeApp || asked == apitypes.GitModeWebhook:
		return asked, nil
	case asked != "":
		return "", invalid("mode %q is not app or webhook", asked)
	case exists && old.InstallationID != 0:
		return apitypes.GitModeApp, nil
	case exists:
		return apitypes.GitModeWebhook, nil
	}
	insts, err := c.store.ListOrgGitHubInstallations(ctx, ss.project.OrgID)
	if err != nil {
		return "", err
	}
	if len(insts) > 0 {
		return apitypes.GitModeApp, nil
	}
	return apitypes.GitModeWebhook, nil
}

// gitTargetChange is how GitConnect changes a connection's credentials
// and webhook; it starts as the existing connection's.
type gitTargetChange struct {
	token          []byte
	status         bool
	hookID         string
	secret         []byte
	installationID int64
	repoID         int64
	// newSecret is a new webhook secret, returned once; change describes
	// the change for the audit log.
	newSecret, change string
}

// resolveAppTarget resolves a connection's repository through the org's
// App and checks its branch (the default one when empty) and directory
// exist there.
func (c *Core) resolveAppTarget(ctx context.Context, orgID int64, repo, branch, dir string) (*appRepoTarget, error) {
	org, err := c.store.GetOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	inst, r, err := c.appRepo(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		if _, branch, _, err = validGitTarget(r.FullName, r.DefaultBranch, dir); err != nil {
			return nil, err
		}
	}
	token, err := c.installationToken(ctx, inst)
	if err != nil {
		return nil, err
	}
	if err := c.checkAppTarget(ctx, token, r.FullName, branch, dir); err != nil {
		return nil, err
	}
	return &appRepoTarget{installationID: inst, repo: r, branch: branch}, nil
}

// appTarget fills in an App connection from its resolved repository: its
// installation and repository ID, the canonical repository name, and no
// token (asking for one is refused before). fresh is a new App connection
// (new, or switched from a webhook): statuses default to on.
func (c *Core) appTarget(ss svcScope, req apitypes.GitConnectRequest, target appRepoTarget, repo *string, t *gitTargetChange, fresh bool) error {
	inst, r := target.installationID, target.repo
	var err error
	if t.hookID == "" {
		// The webhook columns stay filled, unused, so switching to a
		// webhook later is an update; the per-service hook refuses App
		// connections.
		if t.secret, err = c.box.Seal([]byte(randomHex(32)), gitSecretAAD(ss.svc.ID)); err != nil {
			return err
		}
		t.hookID = randomHex(16)
	}
	*repo = r.FullName
	t.token, t.installationID, t.repoID = nil, inst, r.ID
	switch {
	case req.Status != nil:
		t.status = *req.Status
	case fresh:
		t.status = true
	}
	t.change = "through the GitHub App"
	return nil
}

// webhookTarget fills in service serviceID's webhook connection: its
// token and, when fresh (new, or switched from the App) or rotated, a new
// webhook URL and secret. A fresh one starts with no token and statuses
// off.
func (c *Core) webhookTarget(serviceID int64, req apitypes.GitConnectRequest, t *gitTargetChange, fresh bool) error {
	t.installationID, t.repoID = 0, 0
	t.change = "token unchanged"
	if fresh {
		t.token, t.change = nil, "no token"
		if req.Status == nil {
			t.status = false
		}
	}
	if req.Token != nil {
		switch tok := strings.TrimSpace(*req.Token); {
		case tok == "":
			t.token, t.change = nil, "token removed"
		case len(tok) > 512 || strings.ContainsAny(tok, " \t\r\n"):
			return invalid("the GitHub token must be a single token with no spaces")
		default:
			sealed, err := c.box.Seal([]byte(tok), gitTokenAAD(serviceID))
			if err != nil {
				return err
			}
			t.token, t.change = sealed, "token set"
		}
	}
	if req.Status != nil {
		t.status = *req.Status
	}
	if t.status && len(t.token) == 0 {
		return invalid("commit statuses need a GitHub token with Commit statuses write access; pass one (--token-stdin) or turn statuses off")
	}
	if fresh || req.RotateSecret {
		t.hookID, t.newSecret = randomHex(16), randomHex(32)
		sealed, err := c.box.Seal([]byte(t.newSecret), gitSecretAAD(serviceID))
		if err != nil {
			return err
		}
		t.secret = sealed
		if !fresh {
			t.change += "; webhook secret rotated"
		}
	}
	return nil
}

// gitTarget is "owner/repo@branch" plus " in dir/" when set.
func gitTarget(conn store.GitConnection) string {
	s := conn.Repo + "@" + conn.Branch
	if conn.Dir != "" {
		s += " in " + conn.Dir + "/"
	}
	return s
}

// GitDisconnect removes a service's GitHub source, with its token and any
// queued push. A release already started keeps going. Scope: deploy.
func (c *Core) GitDisconnect(ctx context.Context, a Actor, ref ServiceRef) error {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "disconnecting a GitHub repository")
	if err != nil {
		return err
	}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return notFound("service %s has no GitHub source", ss)
	}
	if err != nil {
		return err
	}
	// Its pull request builds stop too, so connecting it again later
	// doesn't silently preview again (ADR 0077).
	if err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteGitConnection(ctx, ss.svc.ID); err != nil {
			return err
		}
		return q.StopServicePreviewBuilds(ctx, ss.svc.ID)
	}); err != nil {
		return err
	}
	c.kickGit(ss.svc.ID, true) // stops a fetch in progress
	c.audit(ctx, a, "git.disconnect", &ss.project, &ss.env, ss.String(), "disconnected "+gitTarget(conn))
	return nil
}

// GitDeploy queues a commit of the connected repository (default: the
// branch's head) as if it had been pushed, without the "already deployed"
// check: it is also how to retry a failed push. Scope: deploy.
func (c *Core) GitDeploy(ctx context.Context, a Actor, ref ServiceRef, req apitypes.GitDeployRequest) (apitypes.GitDeployResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "deploying")
	if err != nil {
		return apitypes.GitDeployResult{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.GitDeployResult{}, invalid("service %s is archived", ss)
	}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return apitypes.GitDeployResult{}, notFound("service %s has no GitHub source; connect one with `dock git connect owner/repo --branch main`", ss)
	}
	if err != nil {
		return apitypes.GitDeployResult{}, err
	}
	at := strings.TrimSpace(req.Ref)
	if at == "" {
		at = conn.Branch
	}
	if !branchRe.MatchString(at) || strings.Contains(at, "..") {
		return apitypes.GitDeployResult{}, invalid("%q is not a commit, branch or tag", at)
	}
	cred, err := c.gitCred(ctx, ss, conn)
	if err != nil {
		return apitypes.GitDeployResult{}, err
	}
	commit, err := c.git.github.commit(ctx, conn.Repo, at, cred)
	if err != nil {
		return apitypes.GitDeployResult{}, err
	}
	if err := c.store.SetGitPending(ctx, store.SetGitPendingParams{
		// Whoever got here may deploy it, also into a locked environment.
		PendingSha: commit.SHA, PendingMessage: commit.Message, PendingAuthor: commit.Author, PendingBy: a.Name, PendingForce: true, PendingApproved: 1, ID: conn.ID,
	}); err != nil {
		return apitypes.GitDeployResult{}, err
	}
	c.gitEvent(conn.ID, fmt.Sprintf("deploy %s by %s: queued", shortSHA(commit.SHA), a.Name))
	c.audit(ctx, a, "git.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("queued %s %s from %s", shortSHA(commit.SHA), commit.Message, conn.Repo))
	c.kickGit(ss.svc.ID, true)
	return apitypes.GitDeployResult{Commit: commit}, nil
}

// gitToken opens a connection's GitHub token ("" for none).
func (c *Core) gitToken(conn store.GitConnection) (string, error) {
	if len(conn.Token) == 0 {
		return "", nil
	}
	b, err := c.box.Open(conn.Token, gitTokenAAD(conn.ServiceID))
	if err != nil {
		return "", fmt.Errorf("open GitHub token: %w", err)
	}
	return string(b), nil
}

// gitEvent records what happened to a connection's latest push, without
// control characters: a failed push's reason can quote the repository
// (a dock.toml parse error), and the line is shown in terminals. The
// connection then goes out on the bus (EventGit), so an open Settings or
// History shows it: an ignored push changes nothing else. Failures are
// logged: the event is informational.
func (c *Core) gitEvent(connID int64, msg string) { c.gitEventIn(nil, connID, msg) }

// gitEventIn is gitEvent for a caller that has the connection's service
// scope already (an App delivery's loop over its connections); nil looks
// it up.
func (c *Core) gitEventIn(ss *svcScope, connID int64, msg string) {
	c.git.eventMu.Lock()
	defer c.git.eventMu.Unlock()
	t := now()
	conn, err := c.store.SetGitLastEvent(c.ctx, store.SetGitLastEventParams{LastEvent: cleanLine(msg, 500), LastEventAt: &t, ID: connID})
	if err != nil {
		c.log.Error("record git event", "err", err)
		return
	}
	if ss == nil {
		s, err := c.scopeOf(c.ctx, conn.ServiceID)
		if err != nil {
			c.log.Error("publish git event", "err", err)
			return
		}
		ss = &s
	}
	gc := c.toGitConnection(conn)
	c.publish(*ss, apitypes.Event{Type: apitypes.EventGit, Git: &gc})
}

// shortSHA is the 7-character form of a commit SHA.
func shortSHA(sha string) string { return sha[:min(7, len(sha))] }

// commitOf is a release's commit, or nil.
func commitOf(r store.Release) *apitypes.Commit {
	if r.CommitSha == "" {
		return nil
	}
	return &apitypes.Commit{SHA: r.CommitSha, Message: r.CommitMessage, Author: r.CommitAuthor}
}

// resumeGit restarts the workers of pushes queued before a restart.
func (c *Core) resumeGit(ctx context.Context) error {
	ids, err := c.store.ListGitPendingServices(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c.kickGit(id, false)
	}
	return nil
}

// kickGit makes sure the service's worker looks at its queued push. newer
// means the queued push changed: a fetch of an older one is cancelled.
func (c *Core) kickGit(serviceID int64, newer bool) {
	c.git.mu.Lock()
	defer c.git.mu.Unlock()
	if c.ctx.Err() != nil {
		return
	}
	if w := c.git.workers[serviceID]; w != nil {
		w.again = true
		if newer && w.cancel != nil {
			w.cancel()
		}
		return
	}
	w := &gitWorker{}
	c.git.workers[serviceID] = w
	c.wg.Add(1)
	c.goCaught("handling a push", func() { c.gitWork(serviceID, w) })
}

// gitStep is what a worker does after one look at the queue.
type gitStep int

const (
	gitDone  gitStep = iota // nothing to do, or a deploy runs: its end kicks again
	gitAgain                // look again now
	gitRetry                // blocked by an upload: look again after gitRetryDelay
)

// gitWork is a service's worker: it runs while the service has a queued
// push it can act on, one step at a time, then exits.
func (c *Core) gitWork(serviceID int64, w *gitWorker) {
	defer c.wg.Done()
	for {
		c.git.mu.Lock()
		w.again = false
		c.git.mu.Unlock()
		// A panic in a step ends the step: the push stays queued for the
		// next kick (gitDone), and the worker is free for it.
		step, _ := caught(c.log, "handling a push", func() (gitStep, error) { return c.gitStep(serviceID, w), nil })
		c.git.mu.Lock()
		if step == gitAgain || w.again {
			c.git.mu.Unlock()
			continue
		}
		delete(c.git.workers, serviceID)
		c.git.mu.Unlock()
		if step == gitRetry {
			c.afterCaught(gitRetryDelay, "retrying a push", func() { c.kickGit(serviceID, false) })
		}
		return
	}
}

// gitStep acts on the service's queued push: skip it if it is already
// live, wait if a deploy is running, else fetch it and start its release.
func (c *Core) gitStep(serviceID int64, w *gitWorker) gitStep {
	ctx := c.ctx
	conn, err := c.store.GetGitConnectionByService(ctx, serviceID)
	if err != nil {
		if !store.IsNotFound(err) && ctx.Err() == nil {
			c.log.Error("git worker", "service", serviceID, "err", err)
		}
		return gitDone
	}
	if conn.PendingSha == "" {
		return gitDone
	}
	sha := conn.PendingSha
	commit := &apitypes.Commit{SHA: sha, Message: conn.PendingMessage, Author: conn.PendingAuthor}
	finish := func(msg string) gitStep {
		if _, err := c.store.ClearGitPending(ctx, store.ClearGitPendingParams{ID: conn.ID, PendingSha: sha}); err != nil {
			c.log.Error("clear queued push", "service", serviceID, "err", err)
			return gitDone
		}
		c.gitEvent(conn.ID, msg)
		return gitAgain
	}
	ss, err := c.scopeOf(ctx, serviceID)
	if err != nil {
		c.log.Error("git worker", "service", serviceID, "err", err)
		return gitDone
	}
	if ss.svc.ArchivedAt != nil {
		return finish(fmt.Sprintf("push %s: not deployed, the service is archived", shortSHA(sha)))
	}
	if _, err := c.store.GetInFlightRelease(ctx, serviceID); err == nil {
		return gitDone // the deploy's end kicks the worker again
	}
	if !conn.PendingForce {
		if active, err := c.store.GetActiveRelease(ctx, serviceID); err == nil && active.CommitSha == sha {
			return finish(fmt.Sprintf("push %s: already live in release %d", shortSHA(sha), active.Number))
		}
	}
	// A push waits for an approver only in an environment locked for
	// people too (a full freeze): a plain lock is for agents, and a push
	// is a person's merge (ADR 0044, amended 2026-11-26).
	if lockHolds(ss.env, now()) && ss.env.LockPeople && conn.PendingApproved == 0 {
		id, err := c.holdPush(ctx, ss, conn)
		if err != nil {
			c.log.Error("hold push", "service", serviceID, "err", err)
			return gitDone
		}
		if conn.PostStatus {
			c.postGitStatus(ss, conn, store.Release{}, "pending", fmt.Sprintf("Waiting for approval in Dock (access request #%d)", id))
		}
		return finish(fmt.Sprintf("push %s: %s is locked; waits for approval as access request #%d", shortSHA(sha), ss.envScope, id))
	}
	cred, err := c.gitCred(ctx, ss, conn)
	if err != nil {
		if ctx.Err() != nil {
			return gitDone // shutting down; the push stays queued
		}
		return finish(fmt.Sprintf("push %s: not deployed: %s", shortSHA(sha), c.errorLine(err)))
	}

	fctx, cancel := context.WithTimeout(ctx, githubFetchTimeout)
	defer cancel()
	c.git.mu.Lock()
	w.sha, w.cancel = sha, cancel
	c.git.mu.Unlock()
	defer func() {
		c.git.mu.Lock()
		w.sha, w.cancel = "", nil
		c.git.mu.Unlock()
	}()
	// A newer push that came before cancel was set couldn't cancel this
	// one; look again so it isn't deployed after being replaced. Later
	// pushes cancel fctx.
	cur, err := c.store.GetGitConnectionByService(ctx, serviceID)
	if err != nil {
		if !store.IsNotFound(err) && ctx.Err() == nil {
			c.log.Error("git worker", "service", serviceID, "err", err)
		}
		return gitDone
	}
	if cur.PendingSha != sha {
		return gitAgain
	}
	actor := Actor{Kind: "system", Name: conn.PendingBy, Scope: apitypes.ScopeDeploy}
	open := func() (io.ReadCloser, error) { return c.git.github.tarball(fctx, conn.Repo, sha, cred, conn.Dir) }
	// What the repository holds is read here, in a goroutine nothing
	// recovers: a panic on it is this push's failure (caught), and the
	// push is cleared below like any other that can't be deployed, so it
	// isn't tried again on every start.
	rel, err := caught(c.log, "deploying a push", func() (store.Release, error) {
		return c.deploySource(fctx, actor, ss, open, int64(c.cfg.MaxUploadMB)<<20, commit)
	})
	switch {
	case err == nil:
		// gitDeployStart cleared the push and recorded its release; this
		// clears it should that have failed, so it isn't deployed again.
		if _, err := c.store.ClearGitPending(ctx, store.ClearGitPendingParams{ID: conn.ID, PendingSha: sha}); err != nil {
			c.log.Error("clear queued push", "service", serviceID, "err", err)
		}
		c.audit(ctx, actor, "release.deploy", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("release %d from %s %s (%s@%s)", rel.Number, shortSHA(sha), commit.Message, conn.Repo, conn.Branch))
		return gitDone // the release is in flight; its end kicks again
	case ctx.Err() != nil:
		return gitDone // shutting down; the push stays queued
	case errors.Is(fctx.Err(), context.Canceled):
		return gitAgain // a newer push replaced this one
	case CodeOf(err) == apitypes.CodeDeployInProgress || CodeOf(err) == apitypes.CodeConflict:
		if _, err := c.store.GetInFlightRelease(ctx, serviceID); err == nil {
			return gitDone
		}
		return gitRetry // an upload holds the slot; no release will kick us
	}
	if conn.PostStatus {
		c.postGitStatus(ss, conn, store.Release{}, "error", c.errorLine(err))
	}
	return finish(fmt.Sprintf("push %s: not deployed: %s", shortSHA(sha), c.errorLine(err)))
}

// gitDeployStart runs as a release built from a push starts, before its
// deploy can finish: it queues the pending commit status, so success or
// failure (gitDeployDone) always reaches GitHub after it, and, for the
// queued push the release is, clears it and records its release, so
// nothing the deploy leads to (a later push found already live) is
// recorded before it.
func (c *Core) gitDeployStart(ss svcScope, rel store.Release) {
	if rel.Source != string(apitypes.SourceUpload) {
		return
	}
	conn, err := c.store.GetGitConnectionByService(c.ctx, ss.svc.ID)
	if err != nil {
		if !store.IsNotFound(err) {
			c.log.Error("git at deploy start", "service", ss.String(), "err", err)
		}
		return
	}
	if conn.PostStatus {
		c.postGitStatus(ss, conn, rel, "pending", fmt.Sprintf("Deploying release %d", rel.Number))
	}
	if conn.PendingSha != rel.CommitSha {
		return
	}
	// Only while the push is still queued: a newer one may have replaced it.
	n, err := c.store.ClearGitPending(c.ctx, store.ClearGitPendingParams{ID: conn.ID, PendingSha: rel.CommitSha})
	if err != nil {
		c.log.Error("clear queued push", "service", ss.String(), "err", err)
		return
	}
	if n == 1 {
		c.gitEvent(conn.ID, fmt.Sprintf("push %s: release %d", shortSHA(rel.CommitSha), rel.Number))
	}
}

// gitDeployDone runs when any release of a service finishes: it posts the
// commit status of releases built from a push and wakes the worker if a
// push is queued.
func (c *Core) gitDeployDone(ss svcScope, rel store.Release, deployErr error) {
	if c.ctx.Err() != nil {
		return
	}
	conn, err := c.store.GetGitConnectionByService(c.ctx, ss.svc.ID)
	if err != nil {
		if !store.IsNotFound(err) {
			c.log.Error("git after deploy", "service", ss.String(), "err", err)
		}
		return
	}
	if conn.PostStatus && rel.CommitSha != "" && rel.Source == string(apitypes.SourceUpload) {
		state, desc := "success", fmt.Sprintf("Release %d is live", rel.Number)
		if _, canceled := errors.AsType[*canceledError](deployErr); canceled {
			// GitHub has no canceled state; error is the one that isn't
			// "the change failed" (ADR 0086).
			state, desc = "error", fmt.Sprintf("Release %d %s", rel.Number, deployErr)
		} else if deployErr != nil {
			state, desc = "failure", fmt.Sprintf("Release %d failed: %s", rel.Number, c.errorLine(deployErr))
		}
		c.postGitStatus(ss, conn, rel, state, desc)
	}
	if conn.PendingSha != "" {
		c.kickGit(ss.svc.ID, false)
	}
}

// gitDeployInterrupted posts the failure status of a release built from a
// push that a daemon restart cut short (recover fails it with msg).
// gitDeployDone never ran for it, so its pending status would otherwise
// stay pending.
func (c *Core) gitDeployInterrupted(ctx context.Context, ss svcScope, rel store.Release, msg string) {
	if rel.CommitSha == "" || rel.Source != string(apitypes.SourceUpload) {
		return
	}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if err != nil {
		if !store.IsNotFound(err) {
			c.log.Error("git after an interrupted deploy", "service", ss.String(), "err", err)
		}
		return
	}
	if conn.PostStatus {
		c.postGitStatus(ss, conn, rel, "failure", fmt.Sprintf("Release %d failed: %s", rel.Number, msg))
	}
}

// postGitStatus posts a commit status in the background, after the
// service's earlier posts. Failures are logged; a deploy never waits for
// or fails on GitHub.
func (c *Core) postGitStatus(ss svcScope, conn store.GitConnection, rel store.Release, state, desc string) {
	if conn.InstallationID == 0 && len(conn.Token) == 0 {
		return
	}
	sha := rel.CommitSha
	if sha == "" {
		sha = conn.PendingSha
	}
	st := githubStatus{State: state, Description: desc, Context: "dock/" + ss.String()}
	// The release's page: on [hooks] ui_url, else Dock's public address.
	if ui := c.uiURL(); ui != "" && rel.Number != 0 {
		if org, err := c.store.GetOrg(c.ctx, ss.project.OrgID); err == nil {
			st.TargetURL = fmt.Sprintf("%s/%s/%s/releases/%d", ui, org.Slug, ss.String(), rel.Number)
		}
	}
	id, done := ss.svc.ID, make(chan struct{})
	c.git.statusMu.Lock()
	prev := c.git.statusTail[id]
	c.git.statusTail[id] = done
	c.git.statusMu.Unlock()
	c.wg.Add(1)
	c.goCaught("posting a commit status", func() {
		defer c.wg.Done()
		defer func() {
			close(done)
			c.git.statusMu.Lock()
			if c.git.statusTail[id] == done {
				delete(c.git.statusTail, id)
			}
			c.git.statusMu.Unlock()
		}()
		if prev != nil {
			select {
			case <-prev:
			case <-c.ctx.Done():
				return
			}
		}
		ctx, cancel := context.WithTimeout(c.ctx, githubCallTimeout)
		defer cancel()
		cred, err := c.gitCred(ctx, ss, conn)
		if err == nil {
			err = c.git.github.status(ctx, conn.Repo, sha, cred, st)
		}
		if err != nil {
			c.log.Warn("post commit status", "service", ss.String(), "commit", shortSHA(sha), "err", err)
		}
	})
}
