package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// What a preview tells its pull request (ADR 0056): one comment, edited
// in place as the preview changes, and a dock/preview commit status on
// the head commit linking to its address. Both go through the base
// connection's credentials (the App's installation token, or a webhook
// connection's own token); without any, nothing is posted. Posting never
// blocks or fails a preview: failures are recorded as its last event.

// previewStatusContext is the commit status context previews post.
const previewStatusContext = "dock/preview"

// githubComment is an issue comment as GitHub lists it.
type githubComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// sendJSON sends a request with a JSON body and decodes the answer into v
// (nil: discarded).
func (g githubClient) sendJSON(ctx context.Context, method, path string, cred githubCred, what string, body, v any) error {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	resp, err := g.do(ctx, method, path, cred, what, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if v == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxGitHubJSON))
		return err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGitHubJSON)).Decode(v); err != nil {
		return fmt.Errorf("read GitHub's answer for %s: %w", what, err)
	}
	return nil
}

// comments lists a pull request's first 100 comments, where Dock's own
// is looked for when it lost track of it.
func (g githubClient) comments(ctx context.Context, repo string, number int64, cred githubCred) ([]githubComment, error) {
	var out []githubComment
	err := g.sendJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, number), cred,
		fmt.Sprintf("pull request #%d in %s", number, repo), nil, &out)
	return out, err
}

// createComment comments on a pull request and returns the comment's ID.
func (g githubClient) createComment(ctx context.Context, repo string, number int64, cred githubCred, body string) (int64, error) {
	var out githubComment
	err := g.sendJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), cred,
		fmt.Sprintf("pull request #%d in %s", number, repo), map[string]string{"body": body}, &out)
	return out.ID, err
}

// editComment replaces a comment's body.
func (g githubClient) editComment(ctx context.Context, repo string, id int64, cred githubCred, body string) error {
	return g.sendJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, id), cred,
		fmt.Sprintf("comment %d in %s", id, repo), map[string]string{"body": body}, nil)
}

// refreshPreview makes sure a preview's comment and status get brought up
// to date, in the background: one refresh per preview at a time, and
// requests while one runs make it run once more, so a burst of releases
// edits the comment a few times, not once per release.
func (c *Core) refreshPreview(id int64) {
	c.previews.refreshMu.Lock()
	defer c.previews.refreshMu.Unlock()
	if c.ctx.Err() != nil {
		return
	}
	if r := c.previews.refreshing[id]; r != nil {
		r.again = true
		return
	}
	r := &previewRefresh{}
	c.previews.refreshing[id] = r
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			c.previews.refreshMu.Lock()
			r.again = false
			c.previews.refreshMu.Unlock()
			c.syncPreview(id)
			c.previews.refreshMu.Lock()
			if r.again && c.ctx.Err() == nil {
				c.previews.refreshMu.Unlock()
				continue
			}
			delete(c.previews.refreshing, id)
			c.previews.refreshMu.Unlock()
			return
		}
	}()
}

// syncPreview posts a preview's commit status, if it changed, and writes
// its comment.
func (c *Core) syncPreview(id int64) {
	ctx := c.ctx
	pv, err := c.store.GetPreview(ctx, id)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("refresh preview", "preview", id, "err", err)
		}
		return
	}
	e, err := c.store.GetEnvironment(ctx, pv.EnvironmentID)
	if err != nil {
		c.log.Error("refresh preview", "preview", id, "err", err)
		return
	}
	p, err := c.store.GetProject(ctx, e.ProjectID)
	if err != nil {
		c.log.Error("refresh preview", "preview", id, "err", err)
		return
	}
	es := envScope{p, e}
	out, err := c.toPreview(ctx, es, pv)
	if err != nil {
		c.log.Error("refresh preview", "preview", id, "err", err)
		return
	}
	if pv.ClosedAt != nil {
		// The archive renamed it; the pull request knew it as pr-N.
		out.Environment, _, _ = strings.Cut(e.Name, "~")
	}
	conn, ok, err := c.previewConnectionOf(ctx, pv)
	if err != nil || !ok {
		if err != nil {
			c.log.Error("refresh preview", "preview", id, "err", err)
		}
		return
	}
	cred, err := c.gitCred(ctx, svcScope{envScope: es}, conn)
	if err != nil {
		c.setPreviewEvent(id, "nothing posted to the pull request: "+errorLine(err))
		return
	}
	link := c.previewLink(ctx, p, out.Environment)
	if pv.ClosedAt == nil {
		state, desc := "pending", "Deploying preview "+out.Environment
		switch out.Status {
		case apitypes.PreviewLive:
			state, desc = "success", "Preview "+out.Environment+" is live"
		case apitypes.PreviewFailed:
			state, desc = "failure", "Preview "+out.Environment+" failed to deploy"
		}
		target := link
		if len(out.URLs) > 0 {
			target = out.URLs[0].URL
		}
		key := pv.HeadSha + " " + state + " " + target
		c.previews.refreshMu.Lock()
		changed := c.previews.posted[id] != key
		c.previews.refreshMu.Unlock()
		if changed {
			st := githubStatus{State: state, Description: desc, Context: previewStatusContext, TargetURL: target}
			if err := c.git.github.status(ctx, conn.Repo, pv.HeadSha, cred, st); err != nil {
				c.log.Warn("post preview status", "preview", out.Environment, "err", err)
			} else {
				c.previews.refreshMu.Lock()
				c.previews.posted[id] = key
				c.previews.refreshMu.Unlock()
			}
		}
	}
	body := c.previewComment(p, out, link, int(e.SleepAfterMinutes))
	if err := c.writePreviewComment(ctx, pv, conn.Repo, cred, body); err != nil {
		c.log.Warn("comment on pull request", "preview", out.Environment, "err", err)
		c.setPreviewEvent(id, "couldn't comment on the pull request: "+errorLine(err)+"; the GitHub App (or token) needs Pull requests write access")
	}
}

// writePreviewComment writes the preview's one comment: edits the one it
// made, else finds it by its marker (a crash lost its ID), else creates
// it, and records its ID.
func (c *Core) writePreviewComment(ctx context.Context, pv store.Preview, repo string, cred githubCred, body string) error {
	id := pv.CommentID
	if id != 0 {
		err := c.git.github.editComment(ctx, repo, id, cred, body)
		if CodeOf(err) != apitypes.CodeNotFound {
			return err
		}
		id = 0 // deleted on GitHub: make a new one
	}
	marker := c.previewMarker(pv.ProjectID)
	list, err := c.git.github.comments(ctx, repo, pv.Number, cred)
	if err != nil {
		return err
	}
	for _, cm := range list {
		if strings.Contains(cm.Body, marker) {
			id = cm.ID
			break
		}
	}
	if id != 0 {
		err = c.git.github.editComment(ctx, repo, id, cred, body)
	} else {
		id, err = c.git.github.createComment(ctx, repo, pv.Number, cred, body)
	}
	if err != nil {
		return err
	}
	if id != pv.CommentID {
		return c.store.SetPreviewComment(ctx, store.SetPreviewCommentParams{CommentID: id, ID: pv.ID})
	}
	return nil
}

// previewMarker is the hidden line that marks Dock's comment for a
// project, so two projects (or installs) previewing one repository keep
// a comment each.
func (c *Core) previewMarker(projectID int64) string {
	return fmt.Sprintf("<!-- tempo-dock-preview %s/%d -->", c.install, projectID)
}

// previewComment is the comment's Markdown. sleepAfter is the preview
// environment's idle minutes before it sleeps (ADR 0070), 0 for never:
// the comment says so, so a slow first visit isn't a surprise. It doesn't
// track sleeping and waking, which would edit the comment every half hour.
func (c *Core) previewComment(p store.Project, pv apitypes.Preview, link string, sleepAfter int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Tempo Dock preview** `%s` in %s: **%s**", pv.Environment, p.Name, pv.Status)
	if pv.Status == apitypes.PreviewClosed {
		b.WriteString("\n\nThe pull request closed, so the preview environment was archived: its containers, volumes, variables and addresses are gone.\n")
	} else {
		fmt.Fprintf(&b, " at `%s`\n\n", shortSHA(pv.Commit))
		if len(pv.URLs) == 0 {
			b.WriteString("No address yet: the base environment's services have no domains, or the server has no base domain.\n")
		} else {
			b.WriteString("| Service | Address |\n| --- | --- |\n")
			for _, u := range pv.URLs {
				fmt.Fprintf(&b, "| %s | %s |\n", u.Service, u.URL)
			}
		}
		if pv.Status == apitypes.PreviewFailed {
			// The comment is public: why it failed is in Dock.
			b.WriteString("\nA service failed to deploy; its release in Tempo Dock says why.\n")
		}
		if sleepAfter > 0 && len(pv.URLs) > 0 {
			idle := fmt.Sprintf("%d min", sleepAfter)
			if sleepAfter%60 == 0 {
				idle = fmt.Sprintf("%dh", sleepAfter/60)
			}
			fmt.Fprintf(&b, "\nIt sleeps after %s without traffic; the next visit wakes it in a few seconds.\n", idle)
		}
	}
	if link != "" && pv.Status != apitypes.PreviewClosed {
		fmt.Fprintf(&b, "\n[Open in Tempo Dock](%s)\n", link)
	}
	b.WriteString("\n" + c.previewMarker(p.ID) + "\n")
	return b.String()
}

// previewLink is the preview environment's page in Dock's UI, or "" with
// no UI address.
func (c *Core) previewLink(ctx context.Context, p store.Project, env string) string {
	ui := cmp.Or(c.cfg.Hooks.UIURL, c.publicURL())
	if ui == "" {
		return ""
	}
	org, err := c.store.GetOrg(ctx, p.OrgID)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s/%s", ui, org.Slug, p.Name, env)
}

// previewConnectionOf is the connection whose credentials a preview
// posts with: one of its own (copies of the base's), or once it is
// archived (its own are deleted) one of the base's. ok is false when
// neither has one with credentials.
func (c *Core) previewConnectionOf(ctx context.Context, pv store.Preview) (store.GitConnection, bool, error) {
	for _, env := range []int64{pv.EnvironmentID, pv.BaseEnvironmentID} {
		conns, err := c.store.ListEnvGitConnections(ctx, env)
		if err != nil {
			return store.GitConnection{}, false, err
		}
		for _, conn := range conns {
			if conn.Repo == pv.Repo && (conn.InstallationID != 0 || len(conn.Token) > 0) {
				return conn, true, nil
			}
		}
	}
	return store.GitConnection{}, false, nil
}

// postPreviewStatus posts a dock/preview status for a pull request that
// got no preview (from a fork, over the limit), in the background with
// the base connection's credentials.
func (c *Core) postPreviewStatus(base envScope, conn store.GitConnection, sha, state, desc, target string) {
	if conn.InstallationID == 0 && len(conn.Token) == 0 {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, githubCallTimeout)
		defer cancel()
		cred, err := c.gitCred(ctx, svcScope{envScope: base}, conn)
		if err == nil {
			err = c.git.github.status(ctx, conn.Repo, sha, cred, githubStatus{State: state, Description: desc, Context: previewStatusContext, TargetURL: target})
		}
		if err != nil {
			c.log.Warn("post preview status", "project", base.project.Name, "commit", shortSHA(sha), "err", err)
		}
	}()
}

// previewTell is an open preview of a project being deleted, with what
// its pull request is told through.
type previewTell struct {
	pv   store.Preview
	env  string
	repo string
	cred githubCred
}

// previewsToTell reads what DeleteProject's open previews post with,
// before their git connections go with the project. One without
// credentials is skipped: nothing was posted for it.
func (c *Core) previewsToTell(ctx context.Context, d deletion) []previewTell {
	rows, err := c.store.ListOpenPreviews(ctx, d.project.ID)
	if err != nil {
		c.log.Warn("list a deleted project's open previews", "project", d.project.Name, "err", err)
		return nil
	}
	var out []previewTell
	for _, pv := range rows {
		conn, ok, err := c.previewConnectionOf(ctx, pv)
		if err != nil || !ok {
			continue
		}
		cred, err := c.gitCred(ctx, svcScope{envScope: envScope{project: d.project}}, conn)
		if err != nil {
			continue
		}
		out = append(out, previewTell{pv: pv, env: d.env(pv.EnvironmentID).Name, repo: conn.Repo, cred: cred})
	}
	return out
}

// tellPreviewsDeleted tells open previews' pull requests, in the
// background, that the preview went with its project: the comment says
// so, and the dock/preview status stops linking to an address that is
// gone. The status stays a success, so a deleted project never blocks a
// merge. Failures are logged; there is no preview left to record them on.
func (c *Core) tellPreviewsDeleted(p store.Project, tells []previewTell) {
	if len(tells) == 0 {
		return
	}
	c.wg.Go(func() {
		ctx := c.ctx
		for _, t := range tells {
			st := githubStatus{State: "success", Context: previewStatusContext,
				Description: fmt.Sprintf("Preview %s was deleted with project %s", t.env, p.Name)}
			if err := c.git.github.status(ctx, t.repo, t.pv.HeadSha, t.cred, st); err != nil && ctx.Err() == nil {
				c.log.Warn("post a deleted preview's status", "preview", t.env, "err", err)
			}
			if t.pv.CommentID == 0 {
				continue
			}
			body := fmt.Sprintf("**Tempo Dock preview** `%s` in %s: **deleted**\n\nThe project was deleted in Tempo Dock: this preview environment and everything in it are gone.\n\n%s\n",
				t.env, p.Name, c.previewMarker(p.ID))
			if err := c.git.github.editComment(ctx, t.repo, t.pv.CommentID, t.cred, body); err != nil && ctx.Err() == nil {
				c.log.Warn("comment on a deleted preview's pull request", "preview", t.env, "err", err)
			}
		}
	})
}
