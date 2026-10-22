package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// gitReplayWindow is how long delivery IDs are remembered: a delivery
// seen again within it is answered without effect.
const gitReplayWindow = 7 * 24 * time.Hour

// gitMaxPushCommits is the most commits GitHub lists in a push payload; a
// push with that many may have more, so its changed paths are unknown.
const gitMaxPushCommits = 2048

// GitDelivery is one webhook request from GitHub, as the hooks listener
// received it.
type GitDelivery struct {
	HookID string // the last path segment of the webhook URL
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
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	HeadCommit *pushCommit  `json:"head_commit"`
	Commits    []pushCommit `json:"commits"`
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

// GitWebhook handles a delivery: it checks the signature, ignores what
// the connection doesn't deploy and queues the rest. It returns a short
// message for GitHub's delivery log. Errors: not_found (no such webhook),
// rate_limited, unauthorized (bad or missing signature), validation.
//
// The webhook's own secret is its authority: the release it starts acts
// as "github:<pusher>" with deploy scope on that one service.
func (c *Core) GitWebhook(ctx context.Context, d GitDelivery) (string, error) {
	conn, err := c.store.GetGitConnectionByHook(ctx, d.HookID)
	if store.IsNotFound(err) {
		return "", notFound("no such webhook")
	}
	if err != nil {
		return "", err
	}
	// Hook IDs are unguessable, so only someone who has the URL can spend
	// this; the bound keeps a leaked URL from costing more.
	if !c.git.limit.allow(conn.HookID, time.Now()) {
		return "", errf(apitypes.CodeRateLimited, "too many deliveries for this webhook; try again in a minute")
	}
	secret, err := c.box.Open(conn.Secret)
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
	case "push":
	default:
		return fmt.Sprintf("ignored: %q events don't deploy; send only pushes", d.Event), nil
	}
	if !deliveryRe.MatchString(d.ID) {
		return "", invalid("X-GitHub-Delivery header missing or malformed")
	}
	t := now()
	if err := c.store.AddGitDelivery(ctx, store.AddGitDeliveryParams{DeliveryID: d.ID, CreatedAt: t}); store.IsUnique(err) {
		return "ignored: delivery " + d.ID + " was already received", nil
	} else if err != nil {
		return "", err
	}
	if err := c.store.PruneGitDeliveries(ctx, t.Add(-gitReplayWindow)); err != nil {
		c.log.Warn("prune webhook deliveries", "err", err)
	}
	var p pushPayload
	if err := json.Unmarshal(d.Body, &p); err != nil {
		return "", invalid("push payload is not JSON: %v; set the webhook's content type to application/json", err)
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
// another repository or branch, a tag, a deleted branch, or (with a
// directory) no changed file inside it.
func pushDecision(conn store.GitConnection, p pushPayload) (apitypes.Commit, string) {
	if !strings.EqualFold(p.Repository.FullName, conn.Repo) {
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
