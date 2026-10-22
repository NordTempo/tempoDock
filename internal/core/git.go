package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
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

// gitHookPath is the webhook path prefix, on [hooks] public_host.
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
	// limit throttles webhook deliveries per hook.
	limit  *limiter
	github githubClient
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
		limit:  newLimiter(30, 2*time.Second),
		github: newGitHubClient(api),
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
	out := apitypes.GitSource{HooksReady: c.cfg.Hooks.PublicHost != "", HooksHint: c.hooksHint()}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	gc := c.toGitConnection(conn)
	out.Connection = &gc
	return out, nil
}

// hooksHint says how to give webhooks a public address, while there is
// none.
func (c *Core) hooksHint() string {
	if c.cfg.Hooks.PublicHost != "" {
		return ""
	}
	return "No public webhook address: set [hooks] public_host in config.toml to a hostname pointing at this server, then restart dock."
}

func (c *Core) toGitConnection(conn store.GitConnection) apitypes.GitConnection {
	out := apitypes.GitConnection{
		Repo: conn.Repo, Branch: conn.Branch, Dir: conn.Dir, TokenSet: len(conn.Token) > 0, Status: conn.PostStatus,
		WebhookURL: c.hookURL(conn.HookID), LastEvent: conn.LastEvent, LastEventAt: conn.LastEventAt,
		CreatedBy: conn.CreatedBy, CreatedAt: conn.CreatedAt,
	}
	if conn.PendingSha != "" {
		out.Pending = &apitypes.Commit{SHA: conn.PendingSha, Message: conn.PendingMessage, Author: conn.PendingAuthor}
	}
	return out
}

// hookURL is the webhook's public URL, or "" without [hooks] public_host.
// It is served by the control plane's own Caddy (withHookRoute).
func (c *Core) hookURL(hookID string) string {
	host := c.cfg.Hooks.PublicHost
	if host == "" {
		return ""
	}
	scheme, port := "https", c.cfg.Caddy.HTTPSPort
	switch {
	case c.cfg.Ingress.Mode == config.IngressCloudflareTunnel:
		port = 443
	case c.cfg.Caddy.DisableTLS:
		scheme, port = "http", c.cfg.Caddy.HTTPPort
	}
	if (scheme == "https" && port != 443) || (scheme == "http" && port != 80) {
		host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return scheme + "://" + host + gitHookPath + hookID
}

// withHookRoute adds the webhook route to the control plane's routes: on
// [hooks] public_host, only paths under gitHookPath, proxied to the
// loopback hooks listener, which serves nothing else. A domain with the
// same hostname (added before public_host was set) keeps it, and the
// route is left out.
func (c *Core) withHookRoute(serverID int64, routes []node.Route) []node.Route {
	host := c.cfg.Hooks.PublicHost
	if host == "" || serverID != c.local.ID {
		return routes
	}
	for _, r := range routes {
		for _, h := range r.Hostnames {
			if h == host {
				c.log.Warn("webhook hostname is also a domain; webhooks are not routed", "host", host)
				return routes
			}
		}
	}
	return append(routes, node.Route{Hostnames: []string{host}, Upstream: c.cfg.Hooks.Listen, PathPrefix: gitHookPath})
}

// GitConnect connects a service to a GitHub repository and branch, or
// changes its connection. A new connection (or RotateSecret) gets a new
// webhook URL and secret, returned once. Scope: deploy.
func (c *Core) GitConnect(ctx context.Context, a Actor, ref ServiceRef, req apitypes.GitConnectRequest) (apitypes.GitConnectResult, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "connecting a GitHub repository")
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
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
	token, tokenChange := old.Token, "token unchanged"
	if req.Token != nil {
		switch t := strings.TrimSpace(*req.Token); {
		case t == "":
			token, tokenChange = nil, "token removed"
		case len(t) > 512 || strings.ContainsAny(t, " \t\r\n"):
			return apitypes.GitConnectResult{}, invalid("the GitHub token must be a single token with no spaces")
		default:
			if token, err = c.box.Seal([]byte(t)); err != nil {
				return apitypes.GitConnectResult{}, err
			}
			tokenChange = "token set"
		}
	}
	if !exists && len(token) == 0 {
		tokenChange = "no token"
	}
	status := old.PostStatus
	if req.Status != nil {
		status = *req.Status
	}
	if status && len(token) == 0 {
		return apitypes.GitConnectResult{}, invalid("commit statuses need a GitHub token with Commit statuses write access; pass one (--token-stdin) or turn statuses off")
	}
	hookID, sealed, secret := old.HookID, old.Secret, ""
	if !exists || req.RotateSecret {
		hookID, secret = randomHex(16), randomHex(32)
		if sealed, err = c.box.Seal([]byte(secret)); err != nil {
			return apitypes.GitConnectResult{}, err
		}
	}
	var conn store.GitConnection
	if exists {
		conn, err = c.store.UpdateGitConnection(ctx, store.UpdateGitConnectionParams{
			Repo: repo, Branch: branch, Dir: dir, Token: token, PostStatus: status, HookID: hookID, Secret: sealed, ID: old.ID,
		})
	} else {
		conn, err = c.store.CreateGitConnection(ctx, store.CreateGitConnectionParams{
			ServiceID: ss.svc.ID, HookID: hookID, Repo: repo, Branch: branch, Dir: dir, Secret: sealed, Token: token,
			PostStatus: status, CreatedBy: a.Name,
		})
	}
	if err != nil {
		return apitypes.GitConnectResult{}, err
	}
	action, summary := "git.connect", fmt.Sprintf("connected %s", gitTarget(conn))
	if exists {
		action, summary = "git.update", fmt.Sprintf("GitHub source is now %s", gitTarget(conn))
	}
	summary += "; " + tokenChange
	if status {
		summary += "; commit statuses on"
	}
	if req.RotateSecret && exists {
		summary += "; webhook secret rotated"
	}
	c.audit(ctx, a, action, &ss.project, &ss.env, ss.String(), summary)
	return apitypes.GitConnectResult{Connection: c.toGitConnection(conn), Created: !exists, Secret: secret, HooksHint: c.hooksHint()}, nil
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
	if err := c.store.DeleteGitConnection(ctx, ss.svc.ID); err != nil {
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
	token, err := c.gitToken(conn)
	if err != nil {
		return apitypes.GitDeployResult{}, err
	}
	commit, err := c.git.github.commit(ctx, conn.Repo, at, token)
	if err != nil {
		return apitypes.GitDeployResult{}, err
	}
	if err := c.store.SetGitPending(ctx, store.SetGitPendingParams{
		PendingSha: commit.SHA, PendingMessage: commit.Message, PendingAuthor: commit.Author, PendingBy: a.Name, PendingForce: true, ID: conn.ID,
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
	b, err := c.box.Open(conn.Token)
	if err != nil {
		return "", fmt.Errorf("open GitHub token: %w", err)
	}
	return string(b), nil
}

// gitEvent records what happened to a connection's latest push, without
// control characters: a failed push's reason can quote the repository
// (a dock.toml parse error), and the line is shown in terminals. Failures
// are logged: the event is informational.
func (c *Core) gitEvent(connID int64, msg string) {
	t := now()
	if err := c.store.SetGitLastEvent(c.ctx, store.SetGitLastEventParams{LastEvent: cleanLine(msg, 500), LastEventAt: &t, ID: connID}); err != nil {
		c.log.Error("record git event", "err", err)
	}
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
	go c.gitWork(serviceID, w)
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
		step := c.gitStep(serviceID, w)
		c.git.mu.Lock()
		if step == gitAgain || w.again {
			c.git.mu.Unlock()
			continue
		}
		delete(c.git.workers, serviceID)
		c.git.mu.Unlock()
		if step == gitRetry {
			time.AfterFunc(gitRetryDelay, func() { c.kickGit(serviceID, false) })
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
		if err := c.store.ClearGitPending(ctx, store.ClearGitPendingParams{ID: conn.ID, PendingSha: sha}); err != nil {
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
	token, err := c.gitToken(conn)
	if err != nil {
		c.log.Error("git worker", "service", serviceID, "err", err)
		return gitDone
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
	actor := Actor{Kind: "system", Name: conn.PendingBy, Scope: apitypes.ScopeDeploy}
	open := func() (io.ReadCloser, error) { return c.git.github.tarball(fctx, conn.Repo, sha, token, conn.Dir) }
	rel, err := c.deploySource(fctx, actor, ss, open, int64(c.cfg.MaxUploadMB)<<20, commit)
	switch {
	case err == nil:
		c.audit(ctx, actor, "release.deploy", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("release %d from %s %s (%s@%s)", rel.Number, shortSHA(sha), commit.Message, conn.Repo, conn.Branch))
		if conn.PostStatus {
			c.postGitStatus(ss, conn, rel, "pending", fmt.Sprintf("Deploying release %d", rel.Number))
		}
		finish(fmt.Sprintf("push %s: release %d", shortSHA(sha), rel.Number))
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
		c.postGitStatus(ss, conn, store.Release{}, "error", errorLine(err))
	}
	return finish(fmt.Sprintf("push %s: not deployed: %s", shortSHA(sha), errorLine(err)))
}

// errorLine is an error's message for a one-line event: internal errors
// are logged and summarized.
func errorLine(err error) string {
	if CodeOf(err) == apitypes.CodeInternal {
		return "internal error; see the daemon log"
	}
	return err.Error()
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
		if deployErr != nil {
			state, desc = "failure", fmt.Sprintf("Release %d failed: %s", rel.Number, errorLine(deployErr))
		}
		c.postGitStatus(ss, conn, rel, state, desc)
	}
	if conn.PendingSha != "" {
		c.kickGit(ss.svc.ID, false)
	}
}

// postGitStatus posts a commit status in the background, after the
// service's earlier posts. Failures are logged; a deploy never waits for
// or fails on GitHub.
func (c *Core) postGitStatus(ss svcScope, conn store.GitConnection, rel store.Release, state, desc string) {
	token, err := c.gitToken(conn)
	if err != nil || token == "" {
		return
	}
	sha := rel.CommitSha
	if sha == "" {
		sha = conn.PendingSha
	}
	st := githubStatus{State: state, Description: desc, Context: "dock/" + ss.String()}
	if c.cfg.Hooks.UIURL != "" && rel.Number != 0 {
		st.TargetURL = fmt.Sprintf("%s/%s/releases/%d", c.cfg.Hooks.UIURL, ss.String(), rel.Number)
	}
	id, done := ss.svc.ID, make(chan struct{})
	c.git.statusMu.Lock()
	prev := c.git.statusTail[id]
	c.git.statusTail[id] = done
	c.git.statusMu.Unlock()
	c.wg.Add(1)
	go func() {
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
		if err := c.git.github.status(ctx, conn.Repo, sha, token, st); err != nil {
			c.log.Warn("post commit status", "service", ss.String(), "commit", shortSHA(sha), "err", err)
		}
	}()
}
