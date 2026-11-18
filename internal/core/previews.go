package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Preview environments per pull request (ADR 0056). A service opts in
// (ADR 0077: service_previews.build); its environment is the base. A
// pull_request event for a repository such a service deploys, into the
// branch it deploys, that changes something in its directory, clones the
// base into pr-<number> (non-production, placed by ADR 0015),
// connects the matching services to the pull request's head commit,
// promotes the base's images for the rest and gives every service that
// has a domain in the base a generated one. New commits redeploy it; the
// pull request closing archives it and frees its containers, network,
// volumes and hostnames. One comment on the pull request, edited in place,
// and a dock/preview commit status say where it is and how it is doing
// (preview_github.go).

const (
	// defaultMaxPreviews is a project's cap on open previews unless set.
	defaultMaxPreviews = 3
	// maxMaxPreviews is the highest cap a project may set.
	maxMaxPreviews = 20
)

// previewState is the in-memory state of previews.
type previewState struct {
	// mu serializes changes to previews (pull request events, closing,
	// settings), so two deliveries of one pull request never race to
	// create its environment.
	mu sync.Mutex
	// refreshMu guards refreshing: per preview ID, while a refresh of its
	// comment and status runs; again asks it to run once more.
	refreshMu  sync.Mutex
	refreshing map[int64]*previewRefresh
	// posted is the last commit status posted per preview ("sha state"),
	// so an unchanged state isn't posted again.
	posted map[int64]string
}

// previewRefresh is a running refresh of one preview's comment.
type previewRefresh struct{ again bool }

func newPreviewState() previewState {
	return previewState{refreshing: map[int64]*previewRefresh{}, posted: map[int64]string{}}
}

// copyList reads a JSON list column (copy_variables, copy_shared).
func copyList(s string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// GetPreview returns a preview environment's pull request and status.
// Scope: read.
func (c *Core) GetPreview(ctx context.Context, a Actor, ref EnvRef) (apitypes.Preview, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "reading a preview")
	if err != nil {
		return apitypes.Preview{}, err
	}
	pv, err := c.store.GetPreviewByEnvironment(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return apitypes.Preview{}, notFound("%s is not a preview environment", es)
	}
	if err != nil {
		return apitypes.Preview{}, err
	}
	return c.toPreview(ctx, es, pv)
}

// ClosePreview closes a preview before its pull request does, as its
// closing would: the environment is archived and its resources freed. A
// later event of the pull request makes a new one. Scope: deploy.
func (c *Core) ClosePreview(ctx context.Context, a Actor, ref EnvRef) (apitypes.Preview, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "closing a preview")
	if err != nil {
		return apitypes.Preview{}, err
	}
	if err := a.checkProject(apitypes.ScopeDeploy, "closing a preview", es.project); err != nil {
		return apitypes.Preview{}, err
	}
	c.previews.mu.Lock()
	defer c.previews.mu.Unlock()
	pv, err := c.store.GetPreviewByEnvironment(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return apitypes.Preview{}, notFound("%s is not a preview environment", es)
	}
	if err != nil {
		return apitypes.Preview{}, err
	}
	if pv.ClosedAt != nil {
		return apitypes.Preview{}, invalid("the preview of %s#%d is closed already", pv.Repo, pv.Number)
	}
	if _, err := c.closePreview(ctx, a, es, pv, pv.PrUpdatedAt, "closed by "+a.Name); err != nil {
		return apitypes.Preview{}, err
	}
	if pv, err = c.store.GetPreview(ctx, pv.ID); err != nil {
		return apitypes.Preview{}, err
	}
	return c.toPreview(ctx, es, pv)
}

// previewOf is a preview as the API shows it, with its environment's ID.
type previewOf struct {
	apitypes.Preview
	envID int64
}

// previewsOf builds a project's open previews (or the one in environment
// envID) in a fixed number of queries: previews, their services' newest releases,
// their domains and the servers.
func (c *Core) previewsOf(ctx context.Context, p store.Project, envID int64) ([]previewOf, error) {
	rows, err := c.store.ListOpenPreviews(ctx, p.ID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	states, err := c.store.ListPreviewServiceStates(ctx, store.ListPreviewServiceStatesParams{ProjectID: p.ID, EnvironmentID: envID})
	if err != nil {
		return nil, err
	}
	domains, err := c.store.ListPreviewDomains(ctx, store.ListPreviewDomainsParams{ProjectID: p.ID, EnvironmentID: envID})
	if err != nil {
		return nil, err
	}
	servers, err := c.serverRows(ctx)
	if err != nil {
		return nil, err
	}
	envs, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	data, err := c.store.ListProjectPreviewData(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]previewOf, 0, len(rows))
	for _, r := range rows {
		if envID != 0 && r.EnvironmentID != envID {
			continue
		}
		i := slices.IndexFunc(envs, func(e store.Environment) bool { return e.ID == r.EnvironmentID })
		if i < 0 {
			continue
		}
		pv := basePreview(envs[i].Name, r)
		var st []string
		for _, s := range states {
			if s.EnvironmentID == r.EnvironmentID {
				st = append(st, s.Status)
			}
		}
		pv.Status = previewStatus(st)
		plain := c.ingressOf(servers[envs[i].ServerID]).plainHTTP
		for _, d := range domains {
			if d.EnvironmentID != r.EnvironmentID || slices.ContainsFunc(pv.URLs, func(u apitypes.PreviewURL) bool { return u.Service == d.Service }) {
				continue
			}
			pv.URLs = append(pv.URLs, apitypes.PreviewURL{Service: d.Service, URL: hostURL(d.Hostname, plain)})
		}
		for _, d := range data {
			if d.PreviewID == r.ID {
				pv.Databases = append(pv.Databases, previewDatabase(d))
			}
		}
		out = append(out, previewOf{pv, r.EnvironmentID})
	}
	return out, nil
}

// previewDatabase is a preview_data row as the API has it.
func previewDatabase(d store.ListProjectPreviewDataRow) apitypes.PreviewDatabase {
	return apitypes.PreviewDatabase{
		Service: d.Service, From: d.FromEnvironment + "/" + d.FromService, Mode: apitypes.PreviewData(d.Mode), Status: apitypes.PreviewDataStatus(d.Status),
		Message: d.Message, Bytes: d.Bytes, MaskedTables: int(d.MaskedTables), MaskedColumns: int(d.MaskedColumns), StartedAt: d.StartedAt, FinishedAt: d.FinishedAt,
	}
}

// toPreview is one preview, open or closed.
func (c *Core) toPreview(ctx context.Context, es envScope, r store.Preview) (apitypes.Preview, error) {
	if r.ClosedAt == nil {
		all, err := c.previewsOf(ctx, es.project, r.EnvironmentID)
		if err != nil {
			return apitypes.Preview{}, err
		}
		if len(all) == 1 {
			return all[0].Preview, nil
		}
	}
	pv := basePreview(es.env.Name, r)
	pv.Status = apitypes.PreviewClosed
	return pv, nil
}

// basePreview is a preview row's pull request, without status or URLs.
func basePreview(env string, r store.Preview) apitypes.Preview {
	return apitypes.Preview{
		Environment: env, Repo: r.Repo, Number: r.Number, Title: r.Title, Author: r.Author, PullURL: pullURL(r.Repo, r.Number),
		Branch: r.HeadBranch, Commit: r.HeadSha, Fork: r.Fork, URLs: []apitypes.PreviewURL{}, Databases: []apitypes.PreviewDatabase{}, Event: r.LastEvent,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt, Status: apitypes.PreviewClosed,
	}
}

// pullURL is a pull request's page on GitHub, built from the connected
// repository (owner/name, as validGitTarget leaves it) and the number:
// the address a delivery names for it is never stored or shown, so the
// link in the UI can only lead to that repository. Empty for a repo or
// number that isn't one.
func pullURL(repo string, number int64) string {
	if !repoRe.MatchString(repo) || number <= 0 {
		return ""
	}
	return "https://github.com/" + repo + "/pull/" + strconv.FormatInt(number, 10)
}

// hostURL is a hostname's address: https, or http on a server with TLS
// off.
func hostURL(host string, plain bool) string {
	if plain {
		return "http://" + host
	}
	return "https://" + host
}

// previewStatus sums up the newest release status of each of a
// preview's services: deploying while any is, then failed if any
// failed, live if any is live, else waiting.
func previewStatus(states []string) apitypes.PreviewStatus {
	out := apitypes.PreviewWaiting
	for _, s := range states {
		switch apitypes.ReleaseStatus(s) {
		case apitypes.ReleaseQueued, apitypes.ReleaseBuilding, apitypes.ReleaseStarting, apitypes.ReleaseHealthChecking:
			return apitypes.PreviewDeploying
		case apitypes.ReleaseFailed:
			out = apitypes.PreviewFailed
		case apitypes.ReleaseActive:
			if out == apitypes.PreviewWaiting {
				out = apitypes.PreviewLive
			}
		}
	}
	return out
}

// pullRequestPayload is the part of GitHub's pull_request event previews
// read.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int64  `json:"number"`
	PullRequest struct {
		Title     string    `json:"title"`
		UpdatedAt time.Time `json:"updated_at"`
		Merged    bool      `json:"merged"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// fork reports whether the pull request comes from another repository
// (or one since deleted).
func (p pullRequestPayload) fork() bool {
	h := p.PullRequest.Head.Repo
	return h == nil || h.ID != p.PullRequest.Base.Repo.ID || !strings.EqualFold(h.FullName, p.PullRequest.Base.Repo.FullName)
}

// parsePullRequest reads and checks a pull_request delivery's body.
func parsePullRequest(body []byte) (pullRequestPayload, error) {
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return p, invalid("pull_request payload is not JSON: %v", err)
	}
	if p.Number <= 0 || p.PullRequest.UpdatedAt.IsZero() {
		return p, invalid("pull_request payload has no number or updated_at")
	}
	return p, nil
}

// previewActions are the pull_request actions previews act on; others
// (labels, reviews, ...) are answered without effect.
var previewActions = []string{"opened", "reopened", "synchronize", "closed", "edited"}

// gitAppPullRequest routes the App's pull_request delivery to every
// project whose base environment deploys the repository.
func (c *Core) gitAppPullRequest(ctx context.Context, body []byte) (string, error) {
	p, err := parsePullRequest(body)
	if err != nil {
		return "", err
	}
	if !slices.Contains(previewActions, p.Action) {
		return fmt.Sprintf("ignored: pull request %s", cleanLine(p.Action, 40)), nil
	}
	if err := c.adoptAppConnections(ctx, p.Repository.ID, p.Installation.ID); err != nil {
		return "", err
	}
	conns, err := c.store.ListAppPreviewConnections(ctx, store.ListAppPreviewConnectionsParams{RepoID: p.Repository.ID, InstallationID: p.Installation.ID})
	if err != nil {
		return "", err
	}
	return c.previewDeliveries(ctx, conns, p)
}

// gitHookPullRequest handles a pull_request delivery to a webhook
// connection: its service's project previews it if the service is in the
// base environment.
func (c *Core) gitHookPullRequest(ctx context.Context, conn store.GitConnection, body []byte) (string, error) {
	p, err := parsePullRequest(body)
	if err != nil {
		return "", err
	}
	if !slices.Contains(previewActions, p.Action) {
		return fmt.Sprintf("ignored: pull request %s", cleanLine(p.Action, 40)), nil
	}
	if !strings.EqualFold(p.Repository.FullName, conn.Repo) {
		return fmt.Sprintf("ignored: pull request in %s, connected to %s", cleanLine(p.Repository.FullName, 140), conn.Repo), nil
	}
	ok, err := c.previewHook(ctx, conn, p.Number)
	if err != nil || !ok {
		return "ignored: this webhook's service doesn't build pull requests (`dock service previews on`)", err
	}
	return c.previewDeliveries(ctx, []store.GitConnection{conn}, p)
}

// previewHook reports whether a webhook connection may speak for a pull
// request: only one of a service that builds pull requests, or of one in
// the open preview's base. Anyone who may connect a service anywhere
// gets its webhook secret, so a connection elsewhere (another
// environment, perhaps one a limited token controls) could otherwise sign
// a pull request and have it built with the base's credentials and copied
// values.
func (c *Core) previewHook(ctx context.Context, conn store.GitConnection, number int64) (bool, error) {
	ss, err := c.scopeOf(ctx, conn.ServiceID)
	if err != nil {
		return false, err
	}
	sp, err := c.store.GetServicePreviews(ctx, ss.svc.ID)
	switch {
	case err == nil && sp.Build:
		return true, nil
	case err != nil && !store.IsNotFound(err):
		return false, err
	}
	pv, err := c.store.GetOpenPreview(ctx, store.GetOpenPreviewParams{ProjectID: ss.project.ID, Repo: conn.Repo, Number: number})
	switch {
	case err == nil:
		return pv.BaseEnvironmentID == ss.env.ID, nil
	case store.IsNotFound(err):
		return false, nil
	}
	return false, err
}

// previewDeliveries handles a pull request event for the base
// environment connections it was routed to, project by project.
func (c *Core) previewDeliveries(ctx context.Context, conns []store.GitConnection, p pullRequestPayload) (string, error) {
	if len(conns) == 0 {
		return "ignored: no project previews pull requests of " + cleanLine(p.Repository.FullName, 140), nil
	}
	seen := map[int64]bool{}
	var msgs []string
	for _, conn := range conns {
		ss, err := c.scopeOf(ctx, conn.ServiceID)
		if err != nil {
			return "", err
		}
		if seen[ss.project.ID] {
			continue
		}
		seen[ss.project.ID] = true
		msg, err := c.previewEvent(ctx, ss.project, conn.Repo, p)
		if err != nil {
			return "", err
		}
		msgs = append(msgs, ss.project.Name+": "+msg)
	}
	return strings.Join(msgs, "\n"), nil
}

// previewEvent applies one pull request event to a project, and says what
// it did. It is idempotent: an event older than the newest one applied to
// the pull request (by its updated_at) changes nothing, and so does one
// whose state is already in place, so redeliveries and replays are safe.
func (c *Core) previewEvent(ctx context.Context, p store.Project, repo string, ev pullRequestPayload) (string, error) {
	c.previews.mu.Lock()
	defer c.previews.mu.Unlock()
	if p.ArchivedAt != nil {
		return "ignored: the project is archived", nil
	}
	pr := fmt.Sprintf("pull request #%d", ev.Number)
	last, err := c.store.GetLatestPreview(ctx, store.GetLatestPreviewParams{ProjectID: p.ID, Repo: repo, Number: ev.Number})
	found := err == nil
	if err != nil && !store.IsNotFound(err) {
		return "", err
	}
	if found && ev.PullRequest.UpdatedAt.Before(last.PrUpdatedAt) {
		return pr + ": ignored, older than the last event applied", nil
	}
	open := found && last.ClosedAt == nil
	actor := Actor{Kind: "system", Name: "github:" + loginOf(ev.Sender.Login), Scope: apitypes.ScopeDeploy}.InOrg(p.OrgID)

	if ev.Action == "closed" {
		if !open {
			return pr + ": ignored, no open preview", nil
		}
		e, err := c.store.GetEnvironment(ctx, last.EnvironmentID)
		if err != nil {
			return "", err
		}
		how := "closed"
		if ev.PullRequest.Merged {
			how = "merged"
		}
		archived, err := c.closePreview(ctx, actor, envScope{p, e}, last, ev.PullRequest.UpdatedAt, pr+" "+how)
		if err != nil {
			return "", err
		}
		if !archived {
			return pr + ": preview " + e.Name + " closed; archived once its running deploy finishes", nil
		}
		return pr + ": preview " + e.Name + " archived", nil
	}

	// The services this pull request builds: connected to its repository,
	// deploying the branch it merges into, with previews on. One
	// repository and branch previews from one environment (ADR 0077).
	rows, err := c.store.ListProjectServicePreviews(ctx, p.ID)
	if err != nil {
		return "", err
	}
	builds := map[int64]store.ListProjectServicePreviewsRow{}
	var branches []string
	var baseID int64
	for _, r := range rows {
		if !r.Build || r.Repo != repo {
			continue
		}
		branches = append(branches, r.Branch)
		if r.Branch == ev.PullRequest.Base.Ref && (baseID == 0 || baseID == r.EnvironmentID) {
			baseID = r.EnvironmentID
			builds[r.ServiceID] = r
		}
	}
	if len(branches) == 0 {
		return pr + ": ignored, previews are off for " + repo, nil
	}
	if baseID == 0 {
		return fmt.Sprintf("%s: ignored, it merges into %s and the services previewing %s deploy %s", pr, cleanLine(ev.PullRequest.Base.Ref, 100), repo,
			strings.Join(slices.Compact(slices.Sorted(slices.Values(branches))), ", ")), nil
	}
	base, err := c.store.GetEnvironment(ctx, baseID)
	if err != nil {
		return "", err
	}
	bes := envScope{p, base}
	conns, err := c.store.ListEnvGitConnections(ctx, base.ID)
	if err != nil {
		return "", err
	}
	svcs, err := c.store.ListServices(ctx, base.ID)
	if err != nil {
		return "", err
	}
	var matched []store.GitConnection
	for _, conn := range conns {
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.ID == conn.ServiceID })
		if _, ok := builds[conn.ServiceID]; ok && i >= 0 && svcs[i].ArchivedAt == nil {
			matched = append(matched, conn)
		}
	}
	if len(matched) == 0 {
		return pr + ": ignored, previews are off for " + repo, nil
	}
	sha := ev.PullRequest.Head.SHA
	if !shaRe.MatchString(sha) {
		return pr + ": ignored, no valid head commit", nil
	}
	head := previewHead{
		title: cleanLine(ev.PullRequest.Title, 200), author: loginOf(ev.PullRequest.User.Login), url: pullURL(repo, ev.Number),
		branch: cleanLine(ev.PullRequest.Head.Ref, 255), sha: sha, fork: ev.fork(), updated: ev.PullRequest.UpdatedAt,
	}
	if head.fork {
		head.branch = fmt.Sprintf("pull/%d/head", ev.Number)
	}

	if head.fork {
		// A fork's code is built only by services that allow forks, also
		// in a preview opened while they did: turning forks off stops it.
		matched = slices.DeleteFunc(matched, func(conn store.GitConnection) bool { return !builds[conn.ServiceID].AllowForks })
		if len(matched) == 0 {
			if ev.Action != "edited" { // a new title is no new commit to mark
				conn := conns[slices.IndexFunc(conns, func(c store.GitConnection) bool { _, ok := builds[c.ServiceID]; return ok })]
				c.postPreviewStatus(bes, conn, sha, "error", "No preview: pull requests from forks aren't previewed", "")
			}
			return pr + ": ignored, it comes from a fork and previews of forks are off", nil
		}
	}

	if open {
		e, err := c.store.GetEnvironment(ctx, last.EnvironmentID)
		if err != nil {
			return "", err
		}
		return c.updatePreview(ctx, actor, envScope{p, e}, last, head)
	}
	if ev.Action == "edited" {
		return pr + ": ignored, no open preview", nil
	}
	setup, err := c.previewSetupOf(ctx, base.ID)
	if err != nil {
		return "", err
	}
	n, err := c.store.CountOpenBasePreviews(ctx, base.ID)
	if err != nil {
		return "", err
	}
	if n >= setup.maxPreviews {
		c.postPreviewStatus(bes, matched[0], sha, "error", fmt.Sprintf("No preview: %d are open, the most allowed", n), "")
		return fmt.Sprintf("%s: no preview, %d are open, the most %s allows; close one or raise the limit (`dock env previews set %s --max N`)", pr, n, bes, base.Name), nil
	}
	if touched, ok := c.previewTouches(ctx, bes, matched, ev); !ok {
		return pr + ": ignored, nothing changed in " + touched, nil
	}
	return c.createPreview(ctx, actor, bes, setup, svcs, matched, repo, ev.Number, head)
}

// previewHead is a pull request's state as an event describes it.
type previewHead struct {
	title, author, url, branch, sha string
	fork                            bool
	updated                         time.Time
}

// createPreview clones the base into a new preview environment and
// deploys it: the matched services build the head commit, the rest run
// the base's images, and services with a domain in the base get a
// generated one. The caller holds previews.mu.
func (c *Core) createPreview(ctx context.Context, a Actor, base envScope, setup previewSetup, svcs []store.Service, matched []store.GitConnection, repo string, number int64, head previewHead) (string, error) {
	p := base.project
	pr := fmt.Sprintf("pull request #%d", number)
	name, err := c.previewName(ctx, p, number)
	if err != nil {
		return "", err
	}
	allowed := setup.copyVariables
	seeded := seedLiterals(svcs, snapshotSeeds(setup, head.fork))
	rules := cloneRules{literal: func(v apitypes.VariableRef) bool { return slices.Contains(allowed, v.String()) || seeded(v) }, orgRefsLiteral: true}
	clone := apitypes.CloneResult{Services: []string{}, Generated: []apitypes.VariableRef{}, Skipped: []apitypes.VariableRef{}, CopiedAcrossKinds: []apitypes.VariableRef{}}
	var dst envScope
	var pv store.Preview
	gitSvcs := map[string]bool{}
	var conns []store.GitConnection
	t := now()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		e, err := c.createEnv(ctx, q, p, name, apitypes.KindNonProduction)
		if err != nil {
			return err
		}
		if c.diskFull(e.ServerID) {
			srv, err := q.GetServer(ctx, e.ServerID)
			if err != nil {
				return err
			}
			return conflict("server %s's disk is past [retention] prune_disk_percent; free space on it (`dock server prune %s`) before previews go there", srv.Name, srv.Name)
		}
		if setup.sleepAfter > 0 {
			// It sleeps when nobody looks at it (sleep.go).
			if e, err = q.SetEnvironmentSleepAfter(ctx, store.SetEnvironmentSleepAfterParams{SleepAfterMinutes: setup.sleepAfter, ID: e.ID}); err != nil {
				return err
			}
		}
		dst = envScope{p, e}
		if _, err := c.cloneInto(ctx, q, base, dst, rules, base.env.Kind != string(apitypes.KindNonProduction), &clone); err != nil {
			return err
		}
		if pv, err = q.CreatePreview(ctx, store.CreatePreviewParams{
			ProjectID: p.ID, EnvironmentID: e.ID, BaseEnvironmentID: base.env.ID, Repo: repo, Number: number, Title: head.title, Author: head.author, HtmlUrl: head.url,
			HeadBranch: head.branch, HeadSha: head.sha, Fork: head.fork, PrUpdatedAt: head.updated,
			LastEvent: "created for " + shortSHA(head.sha), CreatedAt: t, UpdatedAt: t,
		}); err != nil {
			return err
		}
		// Grants for previews name their kind on the row (ADR 0080).
		if err := q.SetEnvironmentPreview(ctx, e.ID); err != nil {
			return err
		}
		e.Preview = true
		clones, err := q.ListServices(ctx, e.ID)
		if err != nil {
			return err
		}
		for _, bc := range matched {
			i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.ID == bc.ServiceID })
			j := slices.IndexFunc(clones, func(s store.Service) bool { return s.Name == svcs[i].Name })
			if j < 0 {
				continue
			}
			conn, err := c.previewConnection(ctx, q, bc, clones[j].ID, head, a.Name)
			if err != nil {
				return err
			}
			conns = append(conns, conn)
			gitSvcs[clones[j].Name] = true
		}
		return nil
	})
	if CodeOf(err) == apitypes.CodeConflict || CodeOf(err) == apitypes.CodeServerOffline {
		// The status is public: it says why without naming servers.
		c.postPreviewStatus(base, matched[0], head.sha, "error", "No preview: no server can take it now; see the delivery in Tempo Dock", "")
		return fmt.Sprintf("%s: no preview: %s", pr, c.errorLine(err)), nil
	}
	if err != nil {
		return "", err
	}
	c.reloadSleep(ctx)
	summary := fmt.Sprintf("preview of %s#%d (%s) at %s: %s", repo, number, head.title, shortSHA(head.sha), cloneSummary(base, apitypes.CloneResult{
		Environment: apitypes.Environment{Kind: apitypes.EnvironmentKind(dst.env.Kind)}, Services: clone.Services, Variables: clone.Variables, Volumes: clone.Volumes,
		Generated: clone.Generated, Skipped: clone.Skipped, SourceKind: apitypes.EnvironmentKind(base.env.Kind),
	}))
	c.audit(ctx, a, "preview.create", &p, &dst.env, dst.String(), summary)

	// Domains first, so the first releases route them.
	c.previewDomains(ctx, a, base, dst)
	var promoted []string
	for _, s := range svcs {
		if s.ArchivedAt == nil && !gitSvcs[s.Name] {
			promoted = append(promoted, s.Name)
		}
	}
	// Databases with data to copy go first, and the rest once they are
	// filled (ADR 0092).
	if plans := previewDataPlans(base, setup, svcs, promoted); len(plans) > 0 {
		rest := slices.DeleteFunc(promoted, func(name string) bool {
			return slices.ContainsFunc(plans, func(p previewDataPlan) bool { return p.from.svc.Name == name })
		})
		c.startPreviewData(a, base, dst, pv, plans, rest, conns)
		c.refreshPreview(pv.ID)
		return fmt.Sprintf("%s: preview %s created for %s", pr, name, shortSHA(head.sha)), nil
	}
	if len(promoted) > 0 {
		if _, err := c.promote(ctx, a, base, dst, promoted, true); err != nil {
			c.setPreviewEvent(pv.ID, "the base's images weren't deployed: "+c.errorLine(err))
		}
	}
	for _, conn := range conns {
		c.kickGit(conn.ServiceID, true)
	}
	c.refreshPreview(pv.ID)
	return fmt.Sprintf("%s: preview %s created for %s", pr, name, shortSHA(head.sha)), nil
}

// previewName is the name of a new preview: pr-<number>, or with -2, -3…
// when an environment has that name (another repository's pull request).
func (c *Core) previewName(ctx context.Context, p store.Project, number int64) (string, error) {
	for i := 1; ; i++ {
		name := "pr-" + strconv.FormatInt(number, 10)
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		if validName("environment", name) != nil {
			return "", invalid("pull request #%d makes no valid environment name", number)
		}
		_, err := c.store.GetEnvironmentByName(ctx, store.GetEnvironmentByNameParams{ProjectID: p.ID, Name: name})
		if store.IsNotFound(err) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
}

// previewConnection connects a preview service to the pull request: the
// base connection's repository, directory and credentials, the head
// branch for show, and the head commit queued. Pushes never reach it
// (GetAppGitConnections leaves previews out, and its own webhook URL is
// never registered); only the pull request's events deploy it. Commit
// statuses are off: the preview posts its own (dock/preview).
func (c *Core) previewConnection(ctx context.Context, q *store.Queries, base store.GitConnection, serviceID int64, head previewHead, by string) (store.GitConnection, error) {
	secret, err := c.box.Seal([]byte(randomHex(32)), gitSecretAAD(serviceID))
	if err != nil {
		return store.GitConnection{}, err
	}
	// The token is sealed again for the preview's row: sealed values are
	// bound to where they are stored (ADR 0041).
	var token []byte
	if len(base.Token) > 0 {
		plain, err := c.gitToken(base)
		if err != nil {
			return store.GitConnection{}, err
		}
		if token, err = c.box.Seal([]byte(plain), gitTokenAAD(serviceID)); err != nil {
			return store.GitConnection{}, err
		}
	}
	conn, err := q.CreateGitConnection(ctx, store.CreateGitConnectionParams{
		ServiceID: serviceID, HookID: randomHex(16), Repo: base.Repo, Branch: head.branch, Dir: base.Dir, Secret: secret,
		Token: token, InstallationID: base.InstallationID, RepoID: base.RepoID, CreatedBy: by,
	})
	if err != nil {
		return conn, err
	}
	err = q.SetGitPending(ctx, store.SetGitPendingParams{
		PendingSha: head.sha, PendingMessage: head.title, PendingAuthor: head.author, PendingBy: by, ID: conn.ID,
	})
	return conn, err
}

// previewDomains gives each preview service whose base service has a
// domain a generated one. Failures (no base domain on the server) are
// recorded on the preview, not fatal: the preview still deploys.
func (c *Core) previewDomains(ctx context.Context, a Actor, base, dst envScope) {
	domains, err := c.store.ListEnvironmentDomains(ctx, base.env.ID)
	if err != nil {
		c.log.Error("preview domains", "env", dst.String(), "err", err)
		return
	}
	svcs, err := c.store.ListServices(ctx, base.env.ID)
	if err != nil {
		c.log.Error("preview domains", "env", dst.String(), "err", err)
		return
	}
	done := map[string]bool{}
	for _, d := range domains {
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.ID == d.ServiceID })
		if i < 0 || svcs[i].ArchivedAt != nil || done[svcs[i].Name] {
			continue
		}
		done[svcs[i].Name] = true
		if _, err := c.GenerateDomain(ctx, a, ServiceRef{dst.project.Name, dst.env.Name, svcs[i].Name}); err != nil {
			if pv, perr := c.store.GetPreviewByEnvironment(ctx, dst.env.ID); perr == nil {
				c.setPreviewEvent(pv.ID, fmt.Sprintf("no address for %s: %s", svcs[i].Name, c.errorLine(err)))
			}
		}
	}
}

// updatePreview applies a newer event to an open preview: a new head
// commit is queued on its git services; a new title only recorded.
func (c *Core) updatePreview(ctx context.Context, a Actor, es envScope, pv store.Preview, head previewHead) (string, error) {
	pr := fmt.Sprintf("pull request #%d", pv.Number)
	if err := c.store.UpdatePreviewHead(ctx, store.UpdatePreviewHeadParams{
		Title: head.title, Author: head.author, HtmlUrl: head.url, HeadBranch: head.branch, HeadSha: head.sha,
		PrUpdatedAt: head.updated, UpdatedAt: now(), ID: pv.ID,
	}); err != nil {
		return "", err
	}
	if pv.HeadSha == head.sha {
		return fmt.Sprintf("%s: preview %s already has %s", pr, es.env.Name, shortSHA(head.sha)), nil
	}
	conns, err := c.store.ListEnvGitConnections(ctx, es.env.ID)
	if err != nil {
		return "", err
	}
	for _, conn := range conns {
		if err := c.store.SetGitPending(ctx, store.SetGitPendingParams{
			PendingSha: head.sha, PendingMessage: head.title, PendingAuthor: head.author, PendingBy: a.Name, ID: conn.ID,
		}); err != nil {
			return "", err
		}
		c.gitEvent(conn.ID, fmt.Sprintf("pull request #%d %s: queued", pv.Number, shortSHA(head.sha)))
		c.kickGit(conn.ServiceID, true)
	}
	c.setPreviewEvent(pv.ID, "updated to "+shortSHA(head.sha))
	c.audit(ctx, a, "preview.update", &es.project, &es.env, es.String(), fmt.Sprintf("preview of %s#%d now at %s", pv.Repo, pv.Number, shortSHA(head.sha)))
	c.refreshPreview(pv.ID)
	return fmt.Sprintf("%s: preview %s updated to %s", pr, es.env.Name, shortSHA(head.sha)), nil
}

// closePreview marks a preview closed, drops its queued commits and
// archives its environment (archivePreview); a deploy still running
// defers the archive to its end. It reports whether the environment is
// archived now. The caller holds previews.mu.
func (c *Core) closePreview(ctx context.Context, a Actor, es envScope, pv store.Preview, updated time.Time, why string) (bool, error) {
	t := now()
	n, err := c.store.ClosePreview(ctx, store.ClosePreviewParams{ClosedAt: &t, PrUpdatedAt: updated, LastEvent: why, UpdatedAt: t, ID: pv.ID})
	if err != nil || n == 0 {
		return false, err
	}
	conns, err := c.store.ListEnvGitConnections(ctx, es.env.ID)
	if err != nil {
		return false, err
	}
	for _, conn := range conns {
		if _, err := c.store.ClearGitPending(ctx, store.ClearGitPendingParams{ID: conn.ID, PendingSha: conn.PendingSha}); err != nil {
			return false, err
		}
		c.kickGit(conn.ServiceID, true) // stops a fetch in progress
	}
	c.audit(ctx, a, "preview.close", &es.project, &es.env, es.String(), fmt.Sprintf("preview of %s#%d closed: %s", pv.Repo, pv.Number, why))
	pv.ClosedAt = &t
	archived := c.archivePreview(ctx, es, pv)
	c.refreshPreview(pv.ID)
	return archived, nil
}

// archivePreview archives a closed preview's environment: every service
// archived (routes removed, containers stopped and removed), its domains
// deleted so their hostnames are free, its variables and git connections
// deleted (the values and token copy it was given go with it), the
// environment renamed and
// hidden; then, in the background once the containers are gone, its
// network and volumes are removed. While a release is in flight nothing
// is archived: the release's end calls it again (previewReleaseDone).
// It reports whether the environment is archived.
func (c *Core) archivePreview(ctx context.Context, es envScope, pv store.Preview) bool {
	var done []archivedService
	var domainIDs []int64
	var vols []store.Volume
	busy := false
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		e, err := q.GetEnvironment(ctx, es.env.ID)
		if err != nil || e.ArchivedAt != nil {
			return err
		}
		es.env = e
		svcs, err := q.ListServices(ctx, e.ID)
		if err != nil {
			return err
		}
		for _, s := range svcs {
			if _, err := q.GetInFlightRelease(ctx, s.ID); err == nil {
				busy = true
				return nil
			} else if !store.IsNotFound(err) {
				return err
			}
		}
		for _, s := range svcs {
			as, ok, err := archiveTx(ctx, q, svcScope{es, s})
			if err != nil {
				return err
			}
			if ok {
				done = append(done, as)
			}
			ids, err := q.DeleteServiceDomains(ctx, s.ID)
			if err != nil {
				return err
			}
			domainIDs = append(domainIDs, ids...)
			vs, err := q.ListVolumes(ctx, s.ID)
			if err != nil {
				return err
			}
			for _, v := range vs {
				if !v.Pending {
					vols = append(vols, v)
				}
			}
		}
		if err := q.DeleteEnvironmentVariables(ctx, e.ID); err != nil {
			return err
		}
		if err := q.DeleteEnvironmentGitConnections(ctx, e.ID); err != nil {
			return err
		}
		t := now()
		return q.ArchiveEnvironment(ctx, store.ArchiveEnvironmentParams{ArchivedAt: &t, Name: e.Name + "~" + strconv.FormatInt(e.ID, 10), ID: e.ID})
	})
	if err != nil {
		c.log.Error("archive a closed preview", "env", es.String(), "err", err)
		c.setPreviewEvent(pv.ID, "not archived yet: "+c.errorLine(err))
		return false
	}
	if busy {
		c.setPreviewEvent(pv.ID, "closed; archived once the running deploy finishes")
		return false
	}
	c.checksMu.Lock()
	for _, id := range domainIDs {
		delete(c.checks, id)
	}
	c.checksMu.Unlock()
	notes, stopped := c.finishArchive(ctx, done)
	notes = append(notes, c.removeNetworks(ctx, es.project, []store.Environment{es.env}, done, stopped)...)
	c.removePreviewVolumes(es, done, vols, stopped)
	c.maybePruneRegistry()
	msg := "archived; containers, network, volumes and addresses freed"
	if len(notes) > 0 {
		msg = archiveSummary("archived", notes)
	}
	c.setPreviewEvent(pv.ID, msg)
	return true
}

// removePreviewVolumes removes an archived preview's volumes in the
// background, once stopped is done (its containers are gone): a preview's
// data is thrown away with it, unlike any other volume (ADR 0056). Each
// volume's row goes once its Docker volume is gone; a row left (a
// restart cut the removal short, the server couldn't be reached, the
// removal failed) is tried again at the next start (resumePreviews).
func (c *Core) removePreviewVolumes(es envScope, done []archivedService, vols []store.Volume, stopped *sync.WaitGroup) {
	if len(vols) == 0 {
		return
	}
	svcs := map[int64]svcScope{}
	for _, as := range done {
		svcs[as.svc.ID] = as.svcScope
	}
	c.wg.Add(1)
	c.goCaught("removing a preview's volumes", func() {
		defer c.wg.Done()
		stopped.Wait()
		rt := c.connected(es.env.ServerID)
		for _, v := range vols {
			ss, ok := svcs[v.ServiceID]
			if !ok {
				ss = svcScope{es, store.Service{ID: v.ServiceID}}
			}
			if rt == nil {
				c.log.Warn("a closed preview's volume stays until the next start: its server is offline", "volume", v.DockerName, "server", es.env.ServerID)
				continue
			}
			if err := rt.RemoveVolume(c.ctx, v.DockerName, c.labels(ss, 0)); err != nil && !errors.Is(err, node.ErrNotFound) {
				if c.ctx.Err() == nil {
					c.log.Warn("remove a closed preview's volume; the next start tries again", "volume", v.DockerName, "err", err)
				}
				continue
			}
			if err := c.store.DeleteRemovedVolume(c.ctx, v.ID); err != nil && c.ctx.Err() == nil {
				c.log.Error("forget a closed preview's removed volume", "volume", v.DockerName, "err", err)
			}
		}
	})
}

// previewReleaseDone runs when a release of a service finishes: in a
// preview, its comment and status are refreshed, and a closed preview
// whose archive waited for the release is archived now.
func (c *Core) previewReleaseDone(ss svcScope) {
	pv, err := c.store.GetPreviewByEnvironment(c.ctx, ss.env.ID)
	if err != nil {
		if !store.IsNotFound(err) && c.ctx.Err() == nil {
			c.log.Error("preview after deploy", "service", ss.String(), "err", err)
		}
		return
	}
	if pv.ClosedAt != nil {
		c.previews.mu.Lock()
		c.archivePreview(c.ctx, ss.envScope, pv)
		c.previews.mu.Unlock()
		return
	}
	c.refreshPreview(pv.ID)
}

// resumePreviews finishes what a restart cut short: archived previews'
// volumes that weren't removed yet are removed (their containers went in
// recovery), and closed previews left unarchived are archived.
func (c *Core) resumePreviews(ctx context.Context) error {
	// Listed before the archives below start removing their own.
	kept, err := c.store.ListArchivedPreviewVolumes(ctx)
	if err != nil {
		return err
	}
	byEnv := map[int64][]store.Volume{}
	for _, r := range kept {
		byEnv[r.EnvironmentID] = append(byEnv[r.EnvironmentID], r.Volume)
	}
	for id, vols := range byEnv {
		e, err := c.store.GetEnvironment(ctx, id)
		if err != nil {
			return err
		}
		p, err := c.store.GetProject(ctx, e.ProjectID)
		if err != nil {
			return err
		}
		c.removePreviewVolumes(envScope{p, e}, nil, vols, &sync.WaitGroup{})
	}
	left, err := c.store.ListClosedPreviewsLeft(ctx)
	if err != nil {
		return err
	}
	c.previews.mu.Lock()
	defer c.previews.mu.Unlock()
	for _, pv := range left {
		e, err := c.store.GetEnvironment(ctx, pv.EnvironmentID)
		if err != nil {
			return err
		}
		p, err := c.store.GetProject(ctx, e.ProjectID)
		if err != nil {
			return err
		}
		c.archivePreview(ctx, envScope{p, e}, pv)
	}
	return nil
}

// setPreviewEvent records what happened to a preview last. Failures are
// logged: it is informational.
func (c *Core) setPreviewEvent(id int64, msg string) {
	if err := c.store.SetPreviewEvent(c.ctx, store.SetPreviewEventParams{LastEvent: cleanLine(msg, 500), UpdatedAt: now(), ID: id}); err != nil {
		c.log.Error("record preview event", "err", err)
	}
}

// previewEnvironments maps a project's preview environments to their
// pull requests, for annotating environments (one query).
func (c *Core) previewEnvironments(ctx context.Context, projectID int64) (map[int64]*apitypes.PullRequestRef, error) {
	rows, err := c.store.ListProjectPreviewEnvironments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*apitypes.PullRequestRef, len(rows))
	for _, r := range rows {
		out[r.EnvironmentID] = &apitypes.PullRequestRef{Repo: r.Repo, Number: r.Number, Title: r.Title, Author: r.Author, URL: pullURL(r.Repo, r.Number)}
	}
	return out, nil
}
