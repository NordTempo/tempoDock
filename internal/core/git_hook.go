package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// gitReplayWindow is how long deliveries (their IDs and body hashes) are
// remembered: a delivery seen again within it is answered without effect.
const gitReplayWindow = 7 * 24 * time.Hour

// gitMaxPushCommits is the most commits GitHub lists in a push payload; a
// push with that many may have more, so its changed paths are unknown.
const gitMaxPushCommits = 2048

// GitDelivery is one webhook request from GitHub, as the hooks listener
// received it.
type GitDelivery struct {
	// HookID is the last path segment of the webhook URL: a connection's
	// hook ID or the GitHub App's.
	HookID string
	// Event, ID and Signature are the X-GitHub-Event, X-GitHub-Delivery
	// and X-Hub-Signature-256 headers.
	Event, ID, Signature string
	Body                 []byte
}

// pushPayload is the part of GitHub's push event push-to-deploy reads.
type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		// PushedAt is when the push happened: a Unix time in push events
		// (GitHub sends a timestamp string in other events).
		PushedAt json.RawMessage `json:"pushed_at"`
	} `json:"repository"`
	// Installation is set on deliveries to the GitHub App.
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	HeadCommit *pushCommit  `json:"head_commit"`
	Commits    []pushCommit `json:"commits"`
}

// pushedAt is when the push happened, false when the payload doesn't say.
func (p pushPayload) pushedAt() (time.Time, bool) {
	raw := p.Repository.PushedAt
	if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type pushCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Author  struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"author"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
}

// hookIDRe matches the hook IDs GitConnect makes (randomHex(16)).
var hookIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// GitHookExists reports not_found unless hookID is a webhook connection's
// hook or the GitHub App's. The hooks listener asks before it reads a
// body, so nobody without a webhook URL can hold its few read slots.
func (c *Core) GitHookExists(ctx context.Context, hookID string) error {
	_, _, err := c.hookTarget(ctx, hookID)
	return err
}

// hookTarget is what a webhook URL delivers to: a webhook connection, or
// the GitHub App (app is set). Errors: not_found.
func (c *Core) hookTarget(ctx context.Context, hookID string) (store.GitConnection, *store.GithubApp, error) {
	if !hookIDRe.MatchString(hookID) {
		return store.GitConnection{}, nil, notFound("no such webhook")
	}
	conn, err := c.webhookConnection(ctx, hookID)
	if err == nil || !store.IsNotFound(err) && CodeOf(err) != apitypes.CodeNotFound {
		return conn, nil, err
	}
	app, err := c.store.GetGitHubApp(ctx)
	switch {
	case err == nil && app.HookID == hookID:
		return conn, &app, nil
	case err == nil || store.IsNotFound(err):
		return conn, nil, notFound("no such webhook")
	}
	return conn, nil, err
}

// webhookConnection is the webhook connection with hookID. App
// connections keep a hook ID, unused, which is not found here.
func (c *Core) webhookConnection(ctx context.Context, hookID string) (store.GitConnection, error) {
	conn, err := c.store.GetGitConnectionByHook(ctx, hookID)
	if err == nil && conn.InstallationID != 0 {
		return conn, notFound("no such webhook")
	}
	return conn, err
}

// GitWebhook handles a delivery: it checks the signature, ignores what
// the connection doesn't deploy and queues the rest. It returns a short
// message for GitHub's delivery log. Errors: not_found (no such webhook),
// rate_limited, unauthorized (bad or missing signature), validation.
//
// The webhook's own secret is its authority: the release it starts acts
// as "github:<pusher>" with deploy scope on that one service. The App's
// deliveries go to gitAppWebhook.
func (c *Core) GitWebhook(ctx context.Context, d GitDelivery) (string, error) {
	conn, app, err := c.hookTarget(ctx, d.HookID)
	if err != nil {
		return "", err
	}
	if app != nil {
		return c.gitAppWebhook(ctx, *app, d)
	}
	// Hook IDs are unguessable, so only someone who has the URL can spend
	// this; the bound keeps a leaked URL from costing more.
	if !c.git.limit.Allow(conn.HookID, time.Now()) {
		return "", errf(apitypes.CodeRateLimited, "too many deliveries for this webhook; try again in a minute")
	}
	secret, err := c.box.Open(conn.Secret, gitSecretAAD(conn.ServiceID))
	if err != nil {
		return "", fmt.Errorf("open webhook secret: %w", err)
	}
	if !validSignature(secret, d.Body, d.Signature) {
		return "", errf(apitypes.CodeUnauthorized, "signature missing or wrong; set the webhook's secret to the one `dock git connect` printed (or rotate it with --rotate-secret)")
	}
	switch d.Event {
	case "ping":
		c.gitEvent(conn.ID, "ping from GitHub: webhook works")
		return "pong", nil
	case "push", "pull_request":
	default:
		return fmt.Sprintf("ignored: %q events don't deploy; send pushes (and pull requests, for previews)", d.Event), nil
	}
	if seen, err := c.gitDelivery(ctx, d); seen != "" || err != nil {
		return seen, err
	}
	if d.Event == "pull_request" {
		return c.gitHookPullRequest(ctx, conn, d.Body)
	}
	var p pushPayload
	if err := json.Unmarshal(d.Body, &p); err != nil {
		return "", invalid("push payload is not JSON: %v; set the webhook's content type to application/json", err)
	}
	return c.queuePush(ctx, conn, p)
}

// gitDelivery records a signature-checked delivery by its ID and by a
// hash of its hook and body; a non-empty answer means either was seen
// within the replay window and the delivery has no effect. The ID alone
// isn't enough: it isn't signed, so a captured body could be resent under
// a new one. The hook is in the hash because GitHub sends the same body
// to every webhook of a repository.
func (c *Core) gitDelivery(ctx context.Context, d GitDelivery) (string, error) {
	if !deliveryRe.MatchString(d.ID) {
		return "", invalid("X-GitHub-Delivery header missing or malformed")
	}
	h := sha256.New()
	h.Write([]byte(d.HookID + "\n"))
	h.Write(d.Body)
	t := now()
	if err := c.store.AddGitDelivery(ctx, store.AddGitDeliveryParams{DeliveryID: d.ID, BodyHash: h.Sum(nil), CreatedAt: t}); store.IsUnique(err) {
		return "ignored: delivery " + d.ID + " was already received", nil
	} else if err != nil {
		return "", err
	}
	if err := c.store.PruneGitDeliveries(ctx, t.Add(-gitReplayWindow)); err != nil {
		c.log.Warn("prune webhook deliveries", "err", err)
	}
	return "", nil
}

// gitAppWebhook handles a delivery to the GitHub App's webhook (ADR
// 0031); pull requests go to previews (ADR 0056). Pushes go to every App connection of the repository (by its
// GitHub ID, so renames don't matter), each deciding by its branch and
// directory; an uninstalled App's installation is unassigned. The App's
// webhook secret is the authority, as a connection's is for its own hook.
func (c *Core) gitAppWebhook(ctx context.Context, app store.GithubApp, d GitDelivery) (string, error) {
	if !c.git.appLimit.Allow(app.HookID, time.Now()) {
		return "", errf(apitypes.CodeRateLimited, "too many deliveries for the GitHub App; try again in a minute")
	}
	secret, err := c.box.Open(app.WebhookSecret, appWebhookSecretAAD)
	if err != nil {
		return "", fmt.Errorf("open the GitHub App's webhook secret: %w", err)
	}
	if !validSignature(secret, d.Body, d.Signature) {
		return "", errf(apitypes.CodeUnauthorized, "signature missing or wrong; the GitHub App's webhook secret must be the one Dock registered it with")
	}
	switch d.Event {
	case "ping":
		return "pong", nil
	case "installation", "push", "pull_request":
	default:
		return fmt.Sprintf("ignored: %q events don't deploy", d.Event), nil
	}
	if seen, err := c.gitDelivery(ctx, d); seen != "" || err != nil {
		return seen, err
	}
	switch d.Event {
	case "installation":
		return c.gitAppInstallation(ctx, d.Body)
	case "pull_request":
		return c.gitAppPullRequest(ctx, d.Body)
	}
	var p pushPayload
	if err := json.Unmarshal(d.Body, &p); err != nil {
		return "", invalid("push payload is not JSON: %v", err)
	}
	conns, err := c.store.GetAppGitConnections(ctx, store.GetAppGitConnectionsParams{RepoID: p.Repository.ID, InstallationID: p.Installation.ID})
	if err != nil {
		return "", err
	}
	if len(conns) == 0 {
		return "ignored: no service deploys " + cleanLine(p.Repository.FullName, 140), nil
	}
	msgs := make([]string, 0, len(conns))
	for _, conn := range conns {
		msg, err := c.queuePush(ctx, conn, p)
		if err != nil {
			return "", err
		}
		if ss, err := c.scopeOf(ctx, conn.ServiceID); err == nil {
			msg = ss.String() + ": " + msg
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "\n"), nil
}

// gitAppInstallation handles the App's installation events: an
// uninstalled App's installation is unassigned, so the console doesn't
// list it as assigned. Its connections then fail to fetch with a reason.
func (c *Core) gitAppInstallation(ctx context.Context, body []byte) (string, error) {
	var e struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return "", invalid("installation payload is not JSON: %v", err)
	}
	id := e.Installation.ID
	if e.Action != "deleted" {
		return fmt.Sprintf("installation %d: %s", id, cleanLine(e.Action, 40)), nil
	}
	if err := c.store.DeleteGitHubInstallation(ctx, id); err != nil {
		return "", err
	}
	c.git.app.forgetTokens(id)
	c.log.Info("GitHub App uninstalled", "installation", id)
	return fmt.Sprintf("installation %d removed", id), nil
}

// queuePush queues the push's commit on a connection, unless the
// connection doesn't deploy it or it is already live or deploying, and
// returns what happened.
func (c *Core) queuePush(ctx context.Context, conn store.GitConnection, p pushPayload) (string, error) {
	// A push older than the replay window may have been forgotten by
	// gitDelivery, so a captured body could be replayed: refuse it.
	if at, ok := p.pushedAt(); ok && now().Sub(at) > gitReplayWindow {
		msg := fmt.Sprintf("push %s: ignored, it was pushed %s, longer ago than deliveries are remembered; push again to deploy it",
			shortSHA(p.After), at.UTC().Format(time.DateOnly))
		c.gitEvent(conn.ID, msg)
		return msg, nil
	}
	commit, ignored := pushDecision(conn, p)
	if ignored != "" {
		c.gitEvent(conn.ID, ignored)
		return ignored, nil
	}
	if r, err := c.store.GetInFlightRelease(ctx, conn.ServiceID); err == nil && r.CommitSha == commit.SHA {
		msg := fmt.Sprintf("push %s: ignored, release %d is deploying it", shortSHA(commit.SHA), r.Number)
		c.gitEvent(conn.ID, msg)
		return msg, nil
	}
	if r, err := c.store.GetActiveRelease(ctx, conn.ServiceID); err == nil && r.CommitSha == commit.SHA {
		msg := fmt.Sprintf("push %s: ignored, already live in release %d", shortSHA(commit.SHA), r.Number)
		c.gitEvent(conn.ID, msg)
		return msg, nil
	}
	if err := c.store.SetGitPending(ctx, store.SetGitPendingParams{
		PendingSha: commit.SHA, PendingMessage: commit.Message, PendingAuthor: commit.Author,
		PendingBy: "github:" + loginOf(p.Pusher.Name), ID: conn.ID,
	}); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("push %s: queued", shortSHA(commit.SHA))
	if conn.PendingSha != "" && conn.PendingSha != commit.SHA {
		msg += fmt.Sprintf(", replacing %s", shortSHA(conn.PendingSha))
	}
	c.gitEvent(conn.ID, msg)
	c.kickGit(conn.ServiceID, true)
	return msg, nil
}

// deliveryRe matches GitHub's delivery IDs (GUIDs), loosely.
var deliveryRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,100}$`)

// validSignature checks X-Hub-Signature-256 ("sha256=<hex HMAC of body>")
// in constant time.
func validSignature(secret, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// pushDecision returns the commit a push deploys, or why it is ignored:
// another repository (for a webhook connection) or branch, a tag, a deleted branch, or (with a
// directory) no changed file inside it.
func pushDecision(conn store.GitConnection, p pushPayload) (apitypes.Commit, string) {
	// App connections were routed by repository ID, which survives renames.
	if conn.RepoID == 0 && !strings.EqualFold(p.Repository.FullName, conn.Repo) {
		return apitypes.Commit{}, fmt.Sprintf("ignored: push to %s, connected to %s", cleanLine(p.Repository.FullName, 140), conn.Repo)
	}
	if p.Ref != "refs/heads/"+conn.Branch {
		return apitypes.Commit{}, fmt.Sprintf("ignored: push to %s, deploying %s", cleanLine(p.Ref, 255), conn.Branch)
	}
	if p.Deleted || strings.Trim(p.After, "0") == "" {
		return apitypes.Commit{}, "ignored: branch " + conn.Branch + " was deleted"
	}
	if !shaRe.MatchString(p.After) {
		return apitypes.Commit{}, "ignored: push has no valid commit SHA"
	}
	if conn.Dir != "" && !touches(p.Commits, conn.Dir) {
		return apitypes.Commit{}, fmt.Sprintf("push %s: ignored, nothing changed in %s/", shortSHA(p.After), conn.Dir)
	}
	commit := apitypes.Commit{SHA: p.After}
	if h := p.HeadCommit; h != nil && h.ID == p.After {
		commit.Message = firstLine(h.Message)
		commit.Author = cleanLine(h.Author.Username, 100)
		if commit.Author == "" {
			commit.Author = cleanLine(h.Author.Name, 100)
		}
	}
	return commit, ""
}

// touches reports whether the pushed commits change anything in dir. With
// no commits listed (a force push to an older commit) or the most GitHub
// lists, it can't tell, and says yes.
func touches(commits []pushCommit, dir string) bool {
	if len(commits) == 0 || len(commits) >= gitMaxPushCommits {
		return true
	}
	for _, c := range commits {
		for _, files := range [][]string{c.Added, c.Removed, c.Modified} {
			for _, f := range files {
				if _, ok := within(f, dir); ok {
					return true
				}
			}
		}
	}
	return false
}

// loginOf keeps a GitHub login's characters (letters, digits, -), so an
// actor name can't carry anything else.
func loginOf(name string) string {
	login := strings.Map(func(r rune) rune {
		if r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, name)
	if login == "" {
		return "push"
	}
	return login[:min(39, len(login))]
}
