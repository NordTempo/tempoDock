package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Deploys into a locked environment wait for approval (ADR 0044). They
// can't be stored as calls: a source upload streams, and a GitHub push
// isn't a token's call at all. A token's `dock deploy` keeps its checked
// archive in the requests directory; a push becomes a request naming its
// commit. A newer upload or push of the service replaces the one
// waiting. Approving deploys it like any other.

// heldDeploy is a held deploy's request arguments: where, and what.
type heldDeploy struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	// Upload is the archive in the requests directory, Bytes its size and
	// DockToml whether it has one (source deploys).
	Upload   string `json:"upload,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	DockToml bool   `json:"dock_toml,omitempty"`
	// The commit, its repository and branch (pushes).
	SHA     string `json:"sha,omitempty"`
	Message string `json:"message,omitempty"`
	Author  string `json:"author,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

func (h heldDeploy) ref() ServiceRef { return ServiceRef{h.Project, h.Environment, h.Service} }

// requestsDir holds the archives of held source deploys, in the data
// directory next to uploads (the same filesystem, so holding is a rename).
func (c *Core) requestsDir() string { return filepath.Join(c.cfg.DataDir, "requests") }

// dropUpload removes a held archive; "" is none.
func (c *Core) dropUpload(name string) {
	if name == "" {
		return
	}
	if err := os.Remove(filepath.Join(c.requestsDir(), filepath.Base(name))); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.log.Warn("remove held upload", "upload", name, "err", err)
	}
}

// sweepUploads removes archives no pending or running request holds (a
// crash between saving one and filing its request); Start calls it.
func (c *Core) sweepUploads(ctx context.Context) error {
	entries, err := os.ReadDir(c.requestsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	held, err := c.store.ListHeldUploads(ctx)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, h := range held {
		keep[h] = true
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			c.dropUpload(e.Name())
		}
	}
	return nil
}

// holdSource keeps a token's source upload into a locked environment as
// an access request: the archive is received and checked as for a
// deploy, then kept until the request ends. le is the lock's refusal.
func (c *Core) holdSource(ctx context.Context, a Actor, le *LockedError, ref ServiceRef, body io.Reader, maxBytes int64) error {
	ss, err := c.loadService(ctx, a.OrgID, ref)
	if err != nil {
		return err
	}
	if ss.svc.ArchivedAt != nil {
		return invalid("service %s is archived", ss)
	}
	done, err := c.beginUpload(ss)
	if err != nil {
		return err
	}
	defer done()
	path, repo, err := c.saveUpload(body, maxBytes)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := os.MkdirAll(c.requestsDir(), 0o700); err != nil {
		_ = os.Remove(path)
		return err
	}
	name := filepath.Base(path)
	if err := os.Rename(path, filepath.Join(c.requestsDir(), name)); err != nil {
		_ = os.Remove(path)
		return err
	}
	args, err := json.Marshal(heldDeploy{Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		Upload: name, Bytes: st.Size(), DockToml: repo != nil})
	if err != nil {
		c.dropUpload(name)
		return err
	}
	r, err := c.fileRequest(ctx, a, le, Call{Via: "source", Op: "deploy-source", Title: "Deploy source", Args: args,
		Reason: reasonOf(ctx), Key: "service " + strconv.FormatInt(ss.svc.ID, 10), Upload: name})
	if err != nil {
		c.dropUpload(name)
		return err
	}
	return le.withRequest(r.ID)
}

// replaySource deploys a held archive: the source Replayer.
func (c *Core) replaySource(ctx context.Context, a Actor, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var h heldDeploy
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	ss, err := c.serviceFor(ctx, a, h.ref(), apitypes.ScopeDeploy, "deploying")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(c.requestsDir(), filepath.Base(h.Upload))
	open := func() (io.ReadCloser, error) { return os.Open(path) }
	rel, err := c.deploySource(ctx, a, ss, open, int64(c.cfg.MaxUploadMB)<<20, nil)
	if err != nil {
		return nil, err
	}
	c.audit(ctx, a, "release.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d from source upload", rel.Number))
	out, err := c.toRelease(ctx, ss, rel, false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// holdPush keeps a GitHub push into a locked environment as an access
// request instead of deploying it, and clears it from the queue; the
// worker calls it. It returns the request's number.
func (c *Core) holdPush(ctx context.Context, ss svcScope, conn store.GitConnection) (int64, error) {
	pusher := Actor{Kind: "system", Name: conn.PendingBy, OrgID: ss.project.OrgID}
	if pusher.Name == "" {
		pusher.Name = "github"
	}
	args, err := json.Marshal(heldDeploy{Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name,
		SHA: conn.PendingSha, Message: conn.PendingMessage, Author: conn.PendingAuthor, Repo: conn.Repo, Branch: conn.Branch})
	if err != nil {
		return 0, err
	}
	le := pusher.lockedErr(ss.project, ss.env, "deploying a push")
	r, err := c.fileRequest(ctx, pusher, le, Call{Via: "git", Op: "deploy-commit", Title: "Deploy a push", Args: args,
		Key: "service " + strconv.FormatInt(ss.svc.ID, 10)})
	return r.ID, err
}

// endHeldPush posts a failed commit status for a held push whose request
// ended without deploying it (rejected, expired or replaced), so GitHub
// doesn't show it pending forever. Other requests are left alone.
func (c *Core) endHeldPush(ctx context.Context, r store.AccessRequest, why string) {
	if r.Via != "git" {
		return
	}
	raw, err := c.box.Open(r.Args, sealedRequestArgs.AAD(r.Nonce))
	if err != nil {
		c.log.Error("held push status", "request", r.ID, "err", err)
		return
	}
	var h heldDeploy
	if err := json.Unmarshal(raw, &h); err != nil {
		c.log.Error("held push status", "request", r.ID, "err", err)
		return
	}
	ss, err := c.serviceFor(ctx, System.InOrg(r.OrgID), h.ref(), apitypes.ScopeDeploy, "deploying")
	if err != nil {
		return // the service is gone; nothing to post for
	}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if err != nil || !conn.PostStatus || conn.Repo != h.Repo {
		return
	}
	c.postGitStatus(ss, conn, store.Release{CommitSha: h.SHA}, "failure", "Not deployed: "+why)
}

// replayGit queues an approved push for the git worker, marked approved
// so it deploys into the locked environment: the git Replayer.
func (c *Core) replayGit(ctx context.Context, a Actor, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var h heldDeploy
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	ss, err := c.serviceFor(ctx, a, h.ref(), apitypes.ScopeDeploy, "deploying")
	if err != nil {
		return nil, err
	}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	if store.IsNotFound(err) || (err == nil && (conn.Repo != h.Repo || conn.Branch != h.Branch)) {
		return nil, conflict("%s's GitHub source changed since the push; deploy again from the service's page", ss)
	}
	if err != nil {
		return nil, err
	}
	if err := c.store.SetGitPending(ctx, store.SetGitPendingParams{
		PendingSha: h.SHA, PendingMessage: h.Message, PendingAuthor: h.Author, PendingBy: a.Name, PendingForce: true, PendingApproved: 1, ID: conn.ID,
	}); err != nil {
		return nil, err
	}
	c.gitEvent(conn.ID, fmt.Sprintf("push %s: approved, queued", shortSHA(h.SHA)))
	c.audit(ctx, a, "git.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("queued %s %s from %s", shortSHA(h.SHA), h.Message, h.Repo))
	c.kickGit(ss.svc.ID, true)
	return json.Marshal(map[string]string{"queued": h.SHA})
}

// reasonKey carries why a caller makes its changes (the API's Dock-Reason
// header) to the requests core files itself (held uploads).
type reasonKey struct{}

// WithReason returns ctx carrying the caller's reason for its changes.
func WithReason(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, reasonKey{}, reason)
}

func reasonOf(ctx context.Context) string {
	r, _ := ctx.Value(reasonKey{}).(string)
	return r
}
