package core

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Followed Compose files (ADR 0108). An environment can follow a Docker
// Compose file in a GitHub repository, read through the organization's
// GitHub App. A push to its branch that changes the file (or a file it
// read) queues a sync, as a push to a connected service queues a deploy;
// the sync converts the file at that commit, plans it against the
// environment and stages the result, like `dock import compose --stage`.
// Nothing goes live until someone applies the staged changes, except a
// sync that auto_apply may apply on its own (composeAutoApply).
//
// A sync acts as the source: the system, named after whoever pushed or
// asked, with deploy in that one environment, or admin when the source
// stages archives (prune), which only an admin can turn on. Each source
// syncs one commit at a time (holdComposeSync); pushes that come meanwhile
// replace the queued commit, latest wins.

// composeDefaultPaths are the Compose file names tried, in order, when a
// source names no path (Docker Compose's own order, as the CLI's).
var composeDefaultPaths = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yaml", "compose.yml"}

// maxComposeReads caps the other files one sync fetches (.env and the
// env_file entries).
const maxComposeReads = 16

// ComposeSource is the Compose file an environment follows, if any.
// Scope: read.
func (c *Core) ComposeSource(ctx context.Context, a Actor, ref EnvRef) (apitypes.ComposeSourceStatus, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "reading an environment")
	if err != nil {
		return apitypes.ComposeSourceStatus{}, err
	}
	row, err := c.store.GetComposeSource(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return apitypes.ComposeSourceStatus{}, nil
	}
	if err != nil {
		return apitypes.ComposeSourceStatus{}, err
	}
	src := toComposeSource(es, row)
	return apitypes.ComposeSourceStatus{Source: &src}, nil
}

// FollowCompose makes an environment follow a Compose file in a GitHub
// repository, or changes what it follows, and syncs the branch's head at
// once. Needs deploy in the environment; staging archives (prune) needs
// admin. The repository is read through the org's GitHub App: the branch
// and the file must exist.
func (c *Core) FollowCompose(ctx context.Context, a Actor, ref EnvRef, req apitypes.ComposeSourceRequest) (apitypes.ComposeSyncResult, error) {
	const action = "following a Compose file"
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, action)
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	if req.Prune {
		if err := a.checkEnvAccess(apitypes.ScopeAdmin, "staging archives for services a followed Compose file drops", es.project, es.env); err != nil {
			return apitypes.ComposeSyncResult{}, err
		}
	}
	if es.env.Preview {
		return apitypes.ComposeSyncResult{}, conflict("%s is a preview: it follows its pull request, not a Compose file; follow one in the environment it was made from", es)
	}
	// An empty branch is the repository's default, which the App says.
	repo, branch, _, err := validGitTarget(req.Repo, cmp.Or(req.Branch, "main"), "")
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	if req.Branch == "" {
		branch = ""
	}
	p, err := validComposePath(req.Path)
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	target, err := c.resolveAppTarget(ctx, es.project.OrgID, repo, branch, "")
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	cred, err := c.installationCred(ctx, es.project.OrgID, target.installationID, target.repo.FullName)
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	if p, err = c.findComposeFile(ctx, cred, target.repo.FullName, target.branch, p); err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	row, err := c.store.UpsertComposeSource(ctx, store.UpsertComposeSourceParams{
		EnvironmentID: es.env.ID, InstallationID: target.installationID, RepoID: target.repo.ID, Repo: target.repo.FullName,
		Branch: target.branch, Path: p, Prune: req.Prune, AutoApply: req.AutoApply, CreatedBy: a.Name,
	})
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	c.audit(ctx, a, "compose.follow", &es.project, &es.env, es.String(), fmt.Sprintf("follows %s on %s of %s%s", p, row.Branch, row.Repo, sourceFlags(row)))
	return c.syncComposeNow(ctx, a, es, row)
}

// UnfollowCompose stops an environment following its Compose file, once
// a sync running now has finished, so nothing stages after it returns.
// What syncs staged stays staged. Needs deploy.
func (c *Core) UnfollowCompose(ctx context.Context, a Actor, ref EnvRef) error {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "following a Compose file")
	if err != nil {
		return err
	}
	row, err := c.store.GetComposeSource(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return notFound("%s doesn't follow a Compose file", es)
	}
	if err != nil {
		return err
	}
	release, ok := c.holdComposeSync(ctx, row.ID)
	if !ok {
		return ctx.Err()
	}
	defer release()
	n, err := c.store.DeleteComposeSource(ctx, store.DeleteComposeSourceParams{ID: row.ID, EnvironmentID: es.env.ID})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("%s doesn't follow a Compose file", es)
	}
	c.audit(ctx, a, "compose.unfollow", &es.project, &es.env, es.String(), "stopped following its Compose file")
	return nil
}

// SyncCompose syncs an environment's followed Compose file at its
// branch's head now, whatever the last sync was, and returns the outcome
// with the plan. Needs deploy; the sync itself acts as the source.
func (c *Core) SyncCompose(ctx context.Context, a Actor, ref EnvRef) (apitypes.ComposeSyncResult, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeDeploy, "following a Compose file")
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	row, err := c.store.GetComposeSource(ctx, es.env.ID)
	if store.IsNotFound(err) {
		return apitypes.ComposeSyncResult{}, notFound("%s doesn't follow a Compose file; follow one with `dock compose follow OWNER/REPO -p %s -e %s`", es, es.project.Name, es.env.Name)
	}
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	return c.syncComposeNow(ctx, a, es, row)
}

// syncComposeNow syncs row at its branch's head for a, who asked.
func (c *Core) syncComposeNow(ctx context.Context, a Actor, es envScope, row store.ComposeSource) (apitypes.ComposeSyncResult, error) {
	release, ok := c.holdComposeSync(ctx, row.ID)
	if !ok {
		return apitypes.ComposeSyncResult{}, ctx.Err()
	}
	defer release()
	// Read again under the hold: a push's sync may have just changed it.
	row, err := c.store.GetComposeSourceByID(ctx, row.ID)
	if store.IsNotFound(err) {
		return apitypes.ComposeSyncResult{}, notFound("%s doesn't follow a Compose file", es)
	}
	if err != nil {
		return apitypes.ComposeSyncResult{}, err
	}
	return c.syncCompose(ctx, es, row, row.Branch, a.Name), nil
}

// syncCompose syncs row at ref (a commit or its branch) as by and records
// the outcome: converts the file, plans it, stages it, and applies it
// when auto_apply allows. A failure is the sync's outcome, not an error:
// it is recorded and audited like the others. Callers hold the source
// (holdComposeSync).
func (c *Core) syncCompose(ctx context.Context, es envScope, row store.ComposeSource, ref, by string) apitypes.ComposeSyncResult {
	out := apitypes.ComposeSyncResult{Warnings: []apitypes.ComposeWarning{}, Plan: newPlan(es.project.Name)}
	commit, reads, outcome, summary := c.runComposeSync(ctx, es, row, ref, by, &out)
	if outcome == apitypes.ComposeFailed && commit.SHA == "" {
		commit.SHA = row.PendingSha // what the push named, if a push
	}
	at := now()
	stored, err := c.store.FinishComposeSync(ctx, store.FinishComposeSyncParams{
		LastSha: commit.SHA, LastMessage: commit.Message, LastAuthor: commit.Author, LastAt: &at,
		Outcome: string(outcome), Summary: summary, Reads: strings.Join(reads, "\n"), Synced: row.PendingSha, ID: row.ID,
	})
	if err != nil {
		// Unrecorded: the queued commit stays queued, for the next push,
		// sync or start; staging it again is a no-op.
		c.log.Error("record a Compose sync", "env", es.String(), "err", err)
		stored = row
		stored.LastSha, stored.LastMessage, stored.LastAuthor, stored.LastAt, stored.Outcome, stored.Summary = commit.SHA, commit.Message, commit.Author, &at, string(outcome), summary
	}
	out.Source = toComposeSource(es, stored)
	c.audit(ctx, c.composeActor(es, row, by), "compose.sync", &es.project, &es.env, es.String(),
		fmt.Sprintf("Compose file at %s: %s", shortSHA(commit.SHA), summary))
	return out
}

// runComposeSync does syncCompose's work and says how it went: the
// commit synced, the other files it read (from the repository's root),
// the outcome and its one-line summary. It fills out's warnings and plan.
func (c *Core) runComposeSync(ctx context.Context, es envScope, row store.ComposeSource, ref, by string, out *apitypes.ComposeSyncResult) (apitypes.Commit, []string, apitypes.ComposeOutcome, string) {
	reads := strings.Split(row.Reads, "\n")
	if row.Reads == "" {
		reads = nil
	}
	fail := func(commit apitypes.Commit, err error) (apitypes.Commit, []string, apitypes.ComposeOutcome, string) {
		return commit, reads, apitypes.ComposeFailed, cleanLine(c.errorLine(err), 300)
	}
	cred, err := c.installationCred(ctx, es.project.OrgID, row.InstallationID, row.Repo)
	if err != nil {
		return fail(apitypes.Commit{}, err)
	}
	commit, err := c.git.github.commit(ctx, row.Repo, ref, cred)
	if err != nil {
		return fail(apitypes.Commit{}, err)
	}
	b, err := c.git.github.file(ctx, row.Repo, commit.SHA, row.Path, cred, maxComposeSize)
	if CodeOf(err) == apitypes.CodeNotFound {
		return fail(commit, notFound("%s isn't in %s at %s; follow its new path with `dock compose follow %s --path PATH`", row.Path, row.Repo, shortSHA(commit.SHA), row.Repo))
	}
	if err != nil {
		return fail(commit, err)
	}
	// The .env next to the file, then the env_file entries the
	// conversion asks for, read relative to the file's directory.
	dir := path.Dir(row.Path)
	req := apitypes.ComposeImportRequest{Compose: string(b), EnvFiles: map[string]string{}, Environment: es.env.Name, Prune: row.Prune}
	reads = nil
	fetch := func(name string) error {
		full := path.Join(dir, name)
		if path.IsAbs(name) || full == ".." || strings.HasPrefix(full, "../") {
			return invalid("%s reads %s, outside the repository", row.Path, name)
		}
		if len(reads) >= maxComposeReads {
			return invalid("%s reads more than %d files", row.Path, maxComposeReads)
		}
		// Read or missing, it is one of the source's files: a push that
		// adds a missing one syncs too.
		reads = append(reads, full)
		f, err := c.git.github.file(ctx, row.Repo, commit.SHA, full, cred, maxComposeSize)
		if CodeOf(err) == apitypes.CodeNotFound {
			return nil // the conversion says what is missing
		}
		if err != nil {
			return err
		}
		req.EnvFiles[name] = string(f)
		return nil
	}
	if err := fetch(".env"); err != nil {
		return fail(commit, err)
	}
	conv, ar, err := convertCompose(es.project.Name, req)
	if err == nil && len(conv.MissingFiles) > 0 {
		for _, name := range conv.MissingFiles {
			if err := fetch(name); err != nil {
				return fail(commit, err)
			}
		}
		conv, ar, err = convertCompose(es.project.Name, req)
	}
	if err != nil {
		return fail(commit, err)
	}
	out.Warnings = conv.Warnings
	for _, name := range conv.MissingFiles {
		out.Warnings = append(out.Warnings, apitypes.ComposeWarning{Key: name, Message: fmt.Sprintf("%s isn't in the repository at %s; its variables are left out", path.Join(dir, name), shortSHA(commit.SHA))})
	}

	actor := c.composeActor(es, row, by)
	// A secret's value comes from the file only for a variable that
	// doesn't exist yet, live or staged, checked as it is staged: one set
	// in Dock since (a rotated password) keeps its value.
	pr := composePruning(row.Prune)
	pr.initValues = true
	staged, waiting, err := c.applyProject(ctx, actor, es.project.Name, ar, pr)
	if err != nil {
		return fail(commit, err)
	}
	out.Plan = staged.Plan
	summary := planSummary(staged.Plan, len(out.Warnings))
	if len(staged.Unchanged) > 0 {
		return commit, reads, apitypes.ComposeUnchanged, summary
	}
	why := composeAutoApply(row, es.env, staged.Plan, waiting[es.env.Name])
	if why == "" {
		// Nothing else may have been staged since this sync's own
		// transaction: applying takes every staged change along.
		if cur, err := c.pendingChanges(ctx, c.store.Queries, es, false); err != nil || len(staged.Staged) == 0 || cur.Count != staged.Staged[0].Count {
			why = "other changes were staged meanwhile"
		}
	}
	if why != "" {
		if row.AutoApply {
			summary += "; not applied: " + why
		}
		return commit, reads, apitypes.ComposeStaged, summary
	}
	applied, err := c.ApplyChanges(ctx, actor, EnvRef{es.project.Name, es.env.Name})
	if err != nil {
		return commit, reads, apitypes.ComposeStaged, summary + "; applying failed: " + cleanLine(c.errorLine(err), 200)
	}
	if len(applied.Failed) > 0 {
		summary += "; still staged: " + cleanLine(strings.Join(applied.Failed, "; "), 200)
	}
	if len(applied.Skipped) > 0 {
		summary += "; " + strings.Join(applied.Skipped, ", ") + " picks it up with its next release (one was deploying)"
	}
	return commit, reads, apitypes.ComposeApplied, summary
}

// composeAutoApply says why a sync that staged plan isn't applied on its
// own, "" when it is: only with auto_apply, never for a sync that removes
// anything (archives need an admin's review), needs a person (a secret
// without a value, a manual step) or would apply changes someone else
// staged (others were waiting), and never in a fully frozen environment
// (ADR 0044).
func composeAutoApply(row store.ComposeSource, e store.Environment, plan apitypes.ApplyPlan, waiting int) string {
	switch {
	case !row.AutoApply:
		return "auto-apply is off"
	case slices.ContainsFunc(plan.Changes, func(ch apitypes.PlanChange) bool { return ch.Action == apitypes.ChangeRemove }):
		return "it removes something"
	case len(plan.Manual) > 0:
		return "it needs you (see the plan)"
	case waiting > 0:
		return "other changes were waiting in the environment"
	case lockHolds(e, now()) && e.LockPeople:
		return "the environment is frozen"
	}
	return ""
}

// planSummary is a plan in one line: "+1 service, −1 service, 3 other
// changes; 2 need you; 1 warning".
func planSummary(plan apitypes.ApplyPlan, warnings int) string {
	var added, removed, other int
	for _, ch := range plan.Changes {
		switch {
		case ch.Kind == apitypes.PlanService && ch.Action == apitypes.ChangeAdd:
			added++
		case ch.Kind == apitypes.PlanService && ch.Action == apitypes.ChangeRemove:
			removed++
		default:
			other++
		}
	}
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("+%s", count(added, "service")))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("−%s", count(removed, "service")))
	}
	if other > 0 {
		parts = append(parts, count(other, "other change"))
	}
	line := "no changes"
	if len(parts) > 0 {
		line = strings.Join(parts, ", ")
	}
	if n := len(plan.Manual); n > 0 {
		line += fmt.Sprintf("; %d need you", n)
	}
	if warnings > 0 {
		line += "; " + count(warnings, "warning")
	}
	return line
}

// composeActor is who a sync acts as: the system, named by (the pusher
// or the person who asked), limited to the source's environment, with
// deploy, or admin when the source stages archives.
func (c *Core) composeActor(es envScope, row store.ComposeSource, by string) Actor {
	scope := apitypes.ScopeDeploy
	if row.Prune {
		scope = apitypes.ScopeAdmin
	}
	return Actor{Kind: "system", Name: by, Scope: scope, ProjectID: es.project.ID, EnvironmentID: es.env.ID}.InOrg(es.project.OrgID)
}

// installationCred is a token of App installation id for reading repo,
// refused unless the installation is still assigned to org.
func (c *Core) installationCred(ctx context.Context, org, id int64, repo string) (githubCred, error) {
	inst, err := c.store.GetGitHubInstallation(ctx, id)
	if err != nil && !store.IsNotFound(err) {
		return githubCred{}, err
	}
	if err != nil || inst.OrgID != org {
		return githubCred{}, errf(apitypes.CodeForbidden, "the GitHub App installation that reads %s is no longer assigned to this org; see Settings > GitHub (`dock github installations`), then follow the file again", repo)
	}
	token, err := c.installationToken(ctx, inst.AppID, id)
	return githubCred{token: token, kind: credInstall}, err
}

// findComposeFile checks that p is a file on branch of repo, or with no
// p finds the first of the default names there.
func (c *Core) findComposeFile(ctx context.Context, cred githubCred, repo, branch, p string) (string, error) {
	try := []string{p}
	if p == "" {
		try = composeDefaultPaths
	}
	for _, name := range try {
		isDir, err := c.git.github.isDir(ctx, cred.token, repo, branch, name)
		if err != nil {
			return "", err
		}
		if isDir {
			if p == "" {
				continue // a default name taken by a directory; try the next
			}
			return "", invalid("%s is a directory on %s of %s; pass the Compose file's path, e.g. %s/compose.yaml", name, branch, repo, name)
		}
		_, err = c.git.github.file(ctx, repo, branch, name, cred, maxComposeSize)
		if err == nil {
			return name, nil
		}
		if CodeOf(err) != apitypes.CodeNotFound {
			return "", err
		}
	}
	if p != "" {
		return "", notFound("%s has no file %s on %s; see its files with `dock github paths %s --branch %s`", repo, p, branch, repo, branch)
	}
	return "", notFound("%s has no %s at its root on %s; pass the Compose file's path with --path", repo, strings.Join(composeDefaultPaths, " or "), branch)
}

// validComposePath checks a Compose file's path in a repository: relative,
// without . or .. parts. Empty stays empty (the default names).
func validComposePath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if len(p) > 255 || !dirRe.MatchString(p) {
		return "", invalid("%q is not a valid path; use one relative to the repository's root, like deploy/compose.yaml", p)
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "." || part == ".." {
			return "", invalid("path %q may not contain . or ..", p)
		}
	}
	return p, nil
}

// queueComposePushes queues an App push on each of the repository's
// sources that follow its branch and whose file (or a file it read) the
// push changes, and returns what happened to each, for GitHub's delivery
// log.
func (c *Core) queueComposePushes(ctx context.Context, p pushPayload) ([]string, error) {
	rows, err := c.store.ListRepoComposeSources(ctx, store.ListRepoComposeSourcesParams{RepoID: p.Repository.ID, InstallationID: p.Installation.ID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	// A push older than the replay window may have been forgotten by
	// gitDelivery, so a captured body could be replayed (as queuePush).
	if at, ok := p.pushedAt(); ok && now().Sub(at) > gitReplayWindow {
		return []string{fmt.Sprintf("(Compose) push %s: ignored, it was pushed %s, longer ago than deliveries are remembered; push again to sync it", shortSHA(p.After), at.UTC().Format(time.DateOnly))}, nil
	}
	var msgs []string
	for _, row := range rows {
		es, err := c.envByID(ctx, row.EnvironmentID)
		if err != nil {
			return msgs, err
		}
		msg := es.String() + " (Compose): "
		switch sha, ignored := composePushDecision(row, p); {
		case ignored != "":
			msg += ignored
		default:
			// Only if it still follows what the push was matched against.
			n, err := c.store.SetComposePending(ctx, store.SetComposePendingParams{
				PendingSha: sha, PendingBy: "github:" + loginOf(p.Pusher.Name),
				ID: row.ID, RepoID: row.RepoID, Branch: row.Branch, Path: row.Path,
			})
			if err != nil {
				return msgs, err
			}
			if n == 0 {
				msg += "push " + shortSHA(sha) + ": ignored, what it follows changed meanwhile"
				break
			}
			c.kickComposeSync(row.ID)
			msg += "push " + shortSHA(sha) + ": queued"
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// composePushDecision returns the commit a push syncs, or why it is
// ignored: another branch, a deleted branch, or none of the files the
// source reads changed.
func composePushDecision(row store.ComposeSource, p pushPayload) (string, string) {
	if p.Ref != "refs/heads/"+row.Branch {
		return "", fmt.Sprintf("ignored: push to %s, following %s", cleanLine(p.Ref, 255), row.Branch)
	}
	if p.Deleted || strings.Trim(p.After, "0") == "" {
		return "", "ignored: branch " + row.Branch + " was deleted"
	}
	if !shaRe.MatchString(p.After) {
		return "", "ignored: push has no valid commit SHA"
	}
	files := []string{row.Path, path.Join(path.Dir(row.Path), ".env")}
	if row.Reads != "" {
		files = append(files, strings.Split(row.Reads, "\n")...)
	}
	if !touchesFile(p.Commits, files) {
		return "", fmt.Sprintf("push %s: ignored, %s didn't change", shortSHA(p.After), row.Path)
	}
	return p.After, ""
}

// touchesFile reports whether the pushed commits change any of files.
// With no commits listed or the most GitHub lists, it can't tell, and
// says yes.
func touchesFile(commits []pushCommit, files []string) bool {
	if len(commits) == 0 || len(commits) >= gitMaxPushCommits {
		return true
	}
	for _, c := range commits {
		for _, changed := range [][]string{c.Added, c.Removed, c.Modified} {
			for _, f := range changed {
				if slices.Contains(files, f) {
					return true
				}
			}
		}
	}
	return false
}

// kickComposeSync makes sure a worker syncs source id's queued commit:
// one worker per source (composeWorkers); a push while it runs makes it
// look at the row once more when it is done, whatever came meanwhile.
func (c *Core) kickComposeSync(id int64) {
	c.composeMu.Lock()
	defer c.composeMu.Unlock()
	if c.ctx.Err() != nil {
		return
	}
	if again, running := c.composeWorkers[id]; running {
		*again = true
		return
	}
	c.composeWorkers[id] = new(false)
	c.goCounted("syncing a Compose file", func() { c.composeWork(id) })
}

// composeWork is source id's worker: it syncs the queued commit, and
// again while pushes came during a sync. It ends when nothing new came,
// also when a sync couldn't record its end (the commit stays queued for
// the next push, sync or start).
func (c *Core) composeWork(id int64) {
	exited := false
	defer func() {
		// A panic ends the worker (goCounted recovers it): free the source
		// for the next kick.
		if !exited {
			c.composeMu.Lock()
			delete(c.composeWorkers, id)
			c.composeMu.Unlock()
		}
	}()
	for {
		c.composeMu.Lock()
		*c.composeWorkers[id] = false
		c.composeMu.Unlock()
		c.composeOnce(id)
		// The check and the exit in one critical section: a kick after it
		// starts a new worker, one before it runs this one again.
		c.composeMu.Lock()
		if !*c.composeWorkers[id] || c.ctx.Err() != nil {
			delete(c.composeWorkers, id)
			c.composeMu.Unlock()
			exited = true
			return
		}
		c.composeMu.Unlock()
	}
}

// composeOnce syncs source id's queued commit, if any, under the
// source's hold (a sync asked for by hand may be running).
func (c *Core) composeOnce(id int64) {
	ctx := c.ctx
	release, ok := c.holdComposeSync(ctx, id)
	if !ok {
		return
	}
	defer release()
	row, err := c.store.GetComposeSourceByID(ctx, id)
	if err != nil {
		if !store.IsNotFound(err) && ctx.Err() == nil {
			c.log.Error("Compose sync", "source", id, "err", err)
		}
		return
	}
	if row.PendingSha == "" {
		return
	}
	es, err := c.envByID(ctx, row.EnvironmentID)
	if err != nil {
		c.log.Error("Compose sync", "source", id, "err", err)
		return
	}
	// The repository's file is read and converted here: a panic on it is
	// this sync's failure, recorded like any other.
	if _, err := caught(c.log, "syncing a Compose file", func() (apitypes.ComposeSyncResult, error) {
		return c.syncCompose(ctx, es, row, row.PendingSha, row.PendingBy), nil
	}); err != nil {
		if _, err := c.store.FinishComposeSync(ctx, store.FinishComposeSyncParams{
			LastSha: row.PendingSha, LastAt: new(now()), Outcome: string(apitypes.ComposeFailed),
			Summary: "the sync failed inside Dock; sync again with `dock compose sync`", Reads: row.Reads, Synced: row.PendingSha, ID: row.ID,
		}); err != nil {
			c.log.Error("record a Compose sync", "source", id, "err", err)
		}
	}
}

// resumeComposeSyncs starts the syncs of pushes queued before a restart.
func (c *Core) resumeComposeSyncs(ctx context.Context) error {
	ids, err := c.store.ListPendingComposeSources(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c.kickComposeSync(id)
	}
	return nil
}

// holdComposeSync waits until no other sync of source id runs and claims
// it; release ends the claim. False if ctx ends first.
func (c *Core) holdComposeSync(ctx context.Context, id int64) (release func(), ok bool) {
	for {
		c.composeMu.Lock()
		running, busy := c.composeSyncing[id]
		if !busy {
			done := make(chan struct{})
			c.composeSyncing[id] = done
			c.composeMu.Unlock()
			return func() {
				c.composeMu.Lock()
				delete(c.composeSyncing, id)
				c.composeMu.Unlock()
				close(done)
			}, true
		}
		c.composeMu.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// toComposeSource is a source as the API shows it.
func toComposeSource(es envScope, row store.ComposeSource) apitypes.ComposeSource {
	out := apitypes.ComposeSource{
		Project: es.project.Name, Environment: es.env.Name, Repo: row.Repo, Branch: row.Branch, Path: row.Path,
		Prune: row.Prune, AutoApply: row.AutoApply, Pending: row.PendingSha, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}
	if row.LastAt != nil {
		out.Last = &apitypes.ComposeSync{
			Commit:  apitypes.Commit{SHA: row.LastSha, Message: row.LastMessage, Author: row.LastAuthor},
			At:      *row.LastAt,
			Outcome: apitypes.ComposeOutcome(row.Outcome),
			Summary: row.Summary,
		}
	}
	return out
}

// sourceFlags is what a source does beyond staging, for its audit line.
func sourceFlags(row store.ComposeSource) string {
	var out []string
	if row.Prune {
		out = append(out, "stages archives")
	}
	if row.AutoApply {
		out = append(out, "auto-applies")
	}
	if len(out) == 0 {
		return ""
	}
	return " (" + strings.Join(out, ", ") + ")"
}
