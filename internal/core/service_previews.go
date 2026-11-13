package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Previews belong to services (ADR 0077). A service with a GitHub source
// opts in (build): pull requests into its branch get a preview cloned
// from its environment, the base. Each service also says which of its own
// variables' literal values a preview copies and whether its volumes
// start from its newest snapshot. The base environment holds the numbers
// about a preview as a whole: how many may be open and when they sleep,
// and the shared variables they copy.

// variableKeyRe matches a variable key in a copy list.
var variableKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// previewSetup is what previews cloned from one base environment are made
// with: the environment's numbers and its services' settings.
type previewSetup struct {
	maxPreviews int64
	// sleepAfter is in minutes; 0 never.
	sleepAfter int64
	// copyVariables are the variables whose literal values a preview
	// copies, as service.KEY and shared.KEY.
	copyVariables []string
	// seed are the services whose volumes start from their newest
	// snapshot.
	seed []string
}

// previewSetupOf reads a base environment's preview setup: two queries.
func (c *Core) previewSetupOf(ctx context.Context, envID int64) (previewSetup, error) {
	out := previewSetup{maxPreviews: defaultMaxPreviews, sleepAfter: apitypes.SleepAfterDefault, copyVariables: []string{}, seed: []string{}}
	ep, err := c.store.GetEnvironmentPreviews(ctx, envID)
	switch {
	case err == nil:
		out.maxPreviews, out.sleepAfter = ep.MaxPreviews, ep.SleepAfterMinutes
		for _, k := range copyList(ep.CopyShared) {
			out.copyVariables = append(out.copyVariables, sharedScope+"."+k)
		}
	case !store.IsNotFound(err):
		return out, err
	}
	rows, err := c.store.ListEnvServicePreviews(ctx, envID)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		for _, k := range copyList(r.CopyVariables) {
			out.copyVariables = append(out.copyVariables, r.Name+"."+k)
		}
		if r.Seed {
			out.seed = append(out.seed, r.Name)
		}
	}
	slices.Sort(out.copyVariables)
	return out, nil
}

// previewTouches reports whether a pull request changes something in the
// directory of one of the services it would build, as pushes are judged
// (touches): one compare call to GitHub. When GitHub can't say (the diff
// is too big, or the call fails), it counts as touched. Otherwise it also
// returns the directories it left alone, for the delivery's answer.
func (c *Core) previewTouches(ctx context.Context, base envScope, matched []store.GitConnection, ev pullRequestPayload) (string, bool) {
	var dirs []string
	for _, conn := range matched {
		if conn.Dir == "" {
			return "", true
		}
		dirs = append(dirs, conn.Dir+"/")
	}
	svc, err := c.store.GetService(ctx, matched[0].ServiceID)
	if err != nil {
		return "", true
	}
	cred, err := c.gitCred(ctx, svcScope{base, svc}, matched[0])
	if err != nil {
		return "", true
	}
	files, ok := c.git.github.changedFiles(ctx, matched[0].Repo, ev.PullRequest.Base.Ref, ev.PullRequest.Head.SHA, cred)
	if !ok {
		return "", true
	}
	commits := []pushCommit{{Modified: files}}
	for _, conn := range matched {
		if touches(commits, conn.Dir) {
			return "", true
		}
	}
	return strings.Join(dirs, " or "), false
}

// maxCompareFiles is the most files GitHub lists in a comparison.
const maxCompareFiles = 300

// changedFiles lists the files a pull request changes, from the merge
// base of baseRef to head; ok is false when GitHub didn't list them all
// or couldn't be asked.
func (g githubClient) changedFiles(ctx context.Context, repo, baseRef, head string, cred githubCred) ([]string, bool) {
	var out struct {
		Files *[]struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		} `json:"files"`
	}
	if err := g.call(ctx, http.MethodGet, "/repos/"+repo+"/compare/"+escapePath(baseRef)+"..."+head, cred, "the changes of "+shortSHA(head)+" in "+repo, &out); err != nil {
		return nil, false
	}
	if out.Files == nil || len(*out.Files) >= maxCompareFiles {
		return nil, false
	}
	var files []string
	for _, f := range *out.Files {
		files = append(files, f.Filename)
		if f.PreviousFilename != "" {
			files = append(files, f.PreviousFilename)
		}
	}
	return files, true
}

// GetPreviews returns a project's previews: the services with preview
// settings, the numbers of each base, and its open previews (those of
// environments the actor can see). A fixed number of queries whatever
// the number of services and previews. Scope: read.
func (c *Core) GetPreviews(ctx context.Context, a Actor, project string) (apitypes.PreviewSettings, error) {
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeRead, "reading previews")
	if err != nil {
		return apitypes.PreviewSettings{}, err
	}
	out := apitypes.PreviewSettings{
		Services: []apitypes.PreviewService{}, Environments: []apitypes.PreviewEnvironment{}, Previews: []apitypes.Preview{},
		MaxPreviews: defaultMaxPreviews, CopyVariables: []string{}, SeedFromSnapshot: []string{}, SleepAfterMinutes: apitypes.SleepAfterDefault,
	}
	rows, err := c.store.ListProjectServicePreviews(ctx, p.ID)
	if err != nil {
		return out, err
	}
	var bases []int64
	for _, r := range rows {
		if !a.canSee(p.OrgID, p.ID, r.EnvironmentID) {
			continue
		}
		out.Services = append(out.Services, apitypes.PreviewService{
			Service: r.Service, Environment: r.Environment, Repo: r.Repo, Branch: r.Branch, Dir: r.Dir,
			ServicePreviews: servicePreviewsOf(store.ServicePreview{ServiceID: r.ServiceID, Build: r.Build, AllowForks: r.AllowForks, CopyVariables: r.CopyVariables, Seed: r.Seed, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}),
		})
		if r.Build && !slices.Contains(bases, r.EnvironmentID) {
			bases = append(bases, r.EnvironmentID)
			ep, err := c.environmentPreviews(ctx, r.EnvironmentID)
			if err != nil {
				return out, err
			}
			out.Environments = append(out.Environments, apitypes.PreviewEnvironment{Environment: r.Environment, EnvironmentPreviews: ep})
		}
	}
	if len(out.Environments) > 0 {
		// The single-base fields, from the first base.
		first := out.Environments[0]
		out.Enabled, out.Base = true, first.Environment
		out.MaxPreviews, out.SleepAfterMinutes, out.UpdatedBy, out.UpdatedAt = first.MaxPreviews, first.SleepAfterMinutes, first.UpdatedBy, first.UpdatedAt
		for _, k := range first.CopyShared {
			out.CopyVariables = append(out.CopyVariables, sharedScope+"."+k)
		}
		for _, s := range out.Services {
			if s.Environment != first.Environment {
				continue
			}
			for _, k := range s.CopyVariables {
				out.CopyVariables = append(out.CopyVariables, s.Service+"."+k)
			}
			if s.Seed {
				out.SeedFromSnapshot = append(out.SeedFromSnapshot, s.Service)
			}
			out.AllowForks = out.AllowForks || s.AllowForks
		}
		slices.Sort(out.CopyVariables)
	}
	all, err := c.previewsOf(ctx, p, 0)
	if err != nil {
		return out, err
	}
	for _, pv := range all {
		if a.canSee(p.OrgID, p.ID, pv.envID) {
			out.Previews = append(out.Previews, pv.Preview)
		}
	}
	return out, nil
}

// servicePreviewsOf is a service_previews row as the API has it.
func servicePreviewsOf(r store.ServicePreview) apitypes.ServicePreviews {
	t := r.UpdatedAt
	return apitypes.ServicePreviews{
		Build: r.Build, AllowForks: r.AllowForks, CopyVariables: copyList(r.CopyVariables), Seed: r.Seed,
		UpdatedBy: r.UpdatedBy, UpdatedAt: &t,
	}
}

// environmentPreviews is an environment's preview numbers, the defaults
// when it has none.
func (c *Core) environmentPreviews(ctx context.Context, envID int64) (apitypes.EnvironmentPreviews, error) {
	out := apitypes.EnvironmentPreviews{MaxPreviews: defaultMaxPreviews, SleepAfterMinutes: apitypes.SleepAfterDefault, CopyShared: []string{}}
	ep, err := c.store.GetEnvironmentPreviews(ctx, envID)
	switch {
	case err == nil:
		t := ep.UpdatedAt
		out.MaxPreviews, out.SleepAfterMinutes, out.CopyShared = int(ep.MaxPreviews), int(ep.SleepAfterMinutes), copyList(ep.CopyShared)
		out.UpdatedBy, out.UpdatedAt = ep.UpdatedBy, &t
	case !store.IsNotFound(err):
		return out, err
	}
	return out, nil
}

// GetServicePreviews returns what a service does in previews. Scope:
// read.
func (c *Core) GetServicePreviews(ctx context.Context, a Actor, ref ServiceRef) (apitypes.ServicePreviews, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading preview settings")
	if err != nil {
		return apitypes.ServicePreviews{}, err
	}
	r, err := c.store.GetServicePreviews(ctx, ss.svc.ID)
	if store.IsNotFound(err) {
		return apitypes.ServicePreviews{CopyVariables: []string{}}, nil
	}
	if err != nil {
		return apitypes.ServicePreviews{}, err
	}
	return servicePreviewsOf(r), nil
}

// SetServicePreviews sets what a service does in previews. Previews run
// whatever code a pull request holds with the copied values, so this is
// for admins of the environment, and not in a preview itself. Building
// pull requests needs a GitHub source, and one repository and branch
// previews from one environment of a project. Scope: admin.
func (c *Core) SetServicePreviews(ctx context.Context, a Actor, ref ServiceRef, req apitypes.SetServicePreviewsRequest) (apitypes.ServicePreviews, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeAdmin, "setting up previews")
	if err != nil {
		return apitypes.ServicePreviews{}, err
	}
	if ss.svc.ArchivedAt != nil {
		return apitypes.ServicePreviews{}, invalid("service %s is archived", ss)
	}
	if err := c.notPreviewEnv(ctx, ss.envScope); err != nil {
		return apitypes.ServicePreviews{}, err
	}
	keys, err := copyKeys(req.CopyVariables)
	if err != nil {
		return apitypes.ServicePreviews{}, err
	}
	conn, err := c.store.GetGitConnectionByService(ctx, ss.svc.ID)
	connected := err == nil
	if err != nil && !store.IsNotFound(err) {
		return apitypes.ServicePreviews{}, err
	}
	if (req.Build || req.AllowForks) && !connected {
		return apitypes.ServicePreviews{}, invalid("%s has no GitHub source, so no pull request can build it; connect one first (`dock git connect owner/repo -s %s`)", ss, ss.svc.Name)
	}
	if req.AllowForks && !req.Build {
		return apitypes.ServicePreviews{}, invalid("forks are previewed only when pull requests build %s; turn that on too", ss.svc.Name)
	}
	if req.Seed {
		if _, _, err := c.previewSeeds(ctx, ss.envScope, []string{ss.svc.Name}); err != nil {
			return apitypes.ServicePreviews{}, err
		}
	}
	list, _ := json.Marshal(keys)
	c.previews.mu.Lock()
	defer c.previews.mu.Unlock()
	if req.Build {
		if err := c.oneBasePerBranch(ctx, ss, conn); err != nil {
			return apitypes.ServicePreviews{}, err
		}
	}
	if err := c.store.UpsertServicePreviews(ctx, store.UpsertServicePreviewsParams{
		ServiceID: ss.svc.ID, Build: req.Build, AllowForks: req.AllowForks, CopyVariables: string(list), Seed: req.Seed,
		UpdatedBy: a.Name, UpdatedAt: now(),
	}); err != nil {
		return apitypes.ServicePreviews{}, err
	}
	c.audit(ctx, a, "service.previews", &ss.project, &ss.env, ss.String(), describeServicePreviews(conn, req, keys))
	return c.GetServicePreviews(ctx, a, ref)
}

// describeServicePreviews is the audit summary of a service's preview
// settings.
func describeServicePreviews(conn store.GitConnection, req apitypes.SetServicePreviewsRequest, keys []string) string {
	var parts []string
	switch {
	case req.Build && req.AllowForks:
		parts = append(parts, fmt.Sprintf("pull requests into %s build it in a preview, forks too", conn.Branch))
	case req.Build:
		parts = append(parts, fmt.Sprintf("pull requests into %s build it in a preview", conn.Branch))
	default:
		parts = append(parts, "pull requests don't build it")
	}
	if len(keys) > 0 {
		parts = append(parts, "values copied: "+strings.Join(keys, ", "))
	}
	if req.Seed {
		parts = append(parts, "previews start from its newest snapshot")
	}
	return "previews: " + strings.Join(parts, "; ")
}

// oneBasePerBranch refuses a second environment of a project previewing
// pull requests into the same repository and branch: a pull request has
// one preview, cloned from one base. The caller holds previews.mu.
func (c *Core) oneBasePerBranch(ctx context.Context, ss svcScope, conn store.GitConnection) error {
	rows, err := c.store.ListProjectServicePreviews(ctx, ss.project.ID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Build && r.EnvironmentID != ss.env.ID && r.Branch == conn.Branch &&
			((r.RepoID != 0 && r.RepoID == conn.RepoID) || strings.EqualFold(r.Repo, conn.Repo)) {
			return conflict("pull requests into %s of %s already preview from %s (%s); a pull request has one preview, so turn them off there first (`dock service previews off -s %s -e %s`)",
				conn.Branch, conn.Repo, r.Environment, r.Service, r.Service, r.Environment)
		}
	}
	return nil
}

// notPreviewEnv refuses settings in a preview environment: previews are
// cloned from long-lived ones.
func (c *Core) notPreviewEnv(ctx context.Context, es envScope) error {
	if _, err := c.store.GetPreviewByEnvironment(ctx, es.env.ID); err == nil {
		return invalid("%s is a preview environment; previews are cloned from a long-lived one, e.g. staging", es)
	} else if !store.IsNotFound(err) {
		return err
	}
	return nil
}

// copyKeys checks and sorts a copy list of variable keys.
func copyKeys(in []string) ([]string, error) {
	keys := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.TrimSpace(k)
		if !variableKeyRe.MatchString(k) {
			return nil, invalid("%q is not a variable key; name it like PUBLIC_URL", k)
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}

// GetEnvironmentPreviews returns the numbers of previews cloned from an
// environment. Scope: read.
func (c *Core) GetEnvironmentPreviews(ctx context.Context, a Actor, ref EnvRef) (apitypes.EnvironmentPreviews, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "reading preview settings")
	if err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	return c.environmentPreviews(ctx, es.env.ID)
}

// SetEnvironmentPreviews sets the numbers of previews cloned from an
// environment and the shared variables they copy; open previews take the
// new sleep setting. Scope: admin.
func (c *Core) SetEnvironmentPreviews(ctx context.Context, a Actor, ref EnvRef, req apitypes.SetEnvironmentPreviewsRequest) (apitypes.EnvironmentPreviews, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeAdmin, "setting up previews")
	if err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	if err := notArchived(es.project); err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	if err := c.notPreviewEnv(ctx, es); err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	max := req.MaxPreviews
	if max == 0 {
		max = defaultMaxPreviews
	}
	if max < 1 || max > maxMaxPreviews {
		return apitypes.EnvironmentPreviews{}, invalid("max previews must be 1-%d", maxMaxPreviews)
	}
	sleepAfter := req.SleepAfterMinutes
	switch sleepAfter {
	case 0:
		sleepAfter = apitypes.SleepAfterDefault
	case -1:
		sleepAfter = 0
	}
	if err := validSleepAfter(sleepAfter); err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	keys, err := copyKeys(req.CopyShared)
	if err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	list, _ := json.Marshal(keys)
	c.previews.mu.Lock()
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.UpsertEnvironmentPreviews(ctx, store.UpsertEnvironmentPreviewsParams{
			EnvironmentID: es.env.ID, MaxPreviews: int64(max), SleepAfterMinutes: int64(sleepAfter), CopyShared: string(list),
			UpdatedBy: a.Name, UpdatedAt: now(),
		}); err != nil {
			return err
		}
		// Open previews follow their base's setting.
		return q.SetPreviewsSleepAfter(ctx, store.SetPreviewsSleepAfterParams{SleepAfterMinutes: int64(sleepAfter), BaseEnvironmentID: es.env.ID})
	})
	c.previews.mu.Unlock()
	if err != nil {
		return apitypes.EnvironmentPreviews{}, err
	}
	c.reloadSleep(ctx)
	summary := fmt.Sprintf("previews cloned from it: at most %d open", max)
	if sleepAfter > 0 {
		summary += fmt.Sprintf(", they sleep after %d minutes without requests", sleepAfter)
	} else {
		summary += ", they never sleep"
	}
	if len(keys) > 0 {
		summary += "; shared values copied: " + strings.Join(keys, ", ")
	}
	c.audit(ctx, a, "environment.previews", &es.project, &es.env, es.String(), summary)
	return c.environmentPreviews(ctx, es.env.ID)
}
