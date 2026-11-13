package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Picking what a service deploys from (the "From GitHub" flow): a
// repository the org's GitHub App reads, a branch and a directory. Every
// answer is read from GitHub when asked; nothing is cached and nothing
// runs at idle.

const (
	// maxGitHubBranches is the most branches listed (one page).
	maxGitHubBranches = 100
	// maxTreeDirs caps the directories listed of one repository.
	maxTreeDirs = 5000
	// maxGitHubTree caps what is read of a tree listing. GitHub stops at
	// 100,000 entries (about 7 MB) itself.
	maxGitHubTree = 16 << 20
)

// appMarkers are the files that make a directory look like an app a
// builder starts from, in the order they are listed.
var appMarkers = []string{
	"Dockerfile", "dock.toml", "package.json", "go.mod", "pyproject.toml", "requirements.txt",
	"Cargo.toml", "Gemfile", "composer.json", "deno.json", "mix.exs", "pom.xml", "build.gradle",
}

// skippedDirs are never listed, nor anything under them: dependencies
// and build output, which hold other projects' package.json files.
var skippedDirs = []string{"node_modules", "vendor", "dist", "build", "target", "__pycache__"}

// skippedPath reports whether a tree path is in a skipped or hidden
// directory (or is one).
func skippedPath(p string) bool {
	for part := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(part, ".") || slices.Contains(skippedDirs, part) {
			return true
		}
	}
	return false
}

// GitHubBranches lists a repository's branches, the default first, for
// picking the one a service deploys. Scope: deploy.
func (c *Core) GitHubBranches(ctx context.Context, a Actor, repo string) (apitypes.GitHubBranches, error) {
	if err := a.require(apitypes.ScopeDeploy, "listing a repository's branches"); err != nil {
		return apitypes.GitHubBranches{}, err
	}
	r, token, err := c.orgAppRepo(ctx, a.OrgID, repo)
	if err != nil {
		return apitypes.GitHubBranches{}, err
	}
	names, err := c.git.github.branches(ctx, token, r.FullName)
	if err != nil {
		return apitypes.GitHubBranches{}, err
	}
	out := apitypes.GitHubBranches{Repo: r.FullName, DefaultBranch: r.DefaultBranch, Branches: []string{}, Truncated: len(names) >= maxGitHubBranches}
	slices.SortFunc(names, func(x, y string) int {
		switch {
		case x == r.DefaultBranch:
			return -1
		case y == r.DefaultBranch:
			return 1
		}
		return strings.Compare(x, y)
	})
	out.Branches = append(out.Branches, names...)
	return out, nil
}

// GitHubPaths lists a repository's directories at a branch (its default
// when branch is empty), with the ones that look like an app first, for
// picking the one a service builds. One tree listing from GitHub, read
// as a stream: only directory names and marker files are kept. Scope:
// deploy.
func (c *Core) GitHubPaths(ctx context.Context, a Actor, repo, branch string) (apitypes.GitHubPaths, error) {
	if err := a.require(apitypes.ScopeDeploy, "listing a repository's directories"); err != nil {
		return apitypes.GitHubPaths{}, err
	}
	r, token, err := c.orgAppRepo(ctx, a.OrgID, repo)
	if err != nil {
		return apitypes.GitHubPaths{}, err
	}
	if strings.TrimSpace(branch) == "" {
		branch = r.DefaultBranch
	}
	if _, branch, _, err = validGitTarget(r.FullName, branch, ""); err != nil {
		return apitypes.GitHubPaths{}, err
	}
	sha, err := c.git.github.branchHead(ctx, token, r.FullName, branch)
	if err != nil {
		return apitypes.GitHubPaths{}, err
	}
	markers := map[string][]string{}
	dirs := map[string]bool{}
	capped := false
	truncated, err := c.git.github.tree(ctx, token, r.FullName, sha, func(e treeEntry) bool {
		if skippedPath(e.Path) {
			return true
		}
		switch e.Type {
		case "tree":
			if len(dirs) >= maxTreeDirs {
				capped = true
				return false
			}
			dirs[e.Path] = true
		case "blob":
			dir, name := path.Split(e.Path)
			if slices.Contains(appMarkers, name) {
				d := strings.TrimSuffix(dir, "/")
				markers[d] = append(markers[d], name)
			}
		}
		return true
	})
	if err != nil {
		return apitypes.GitHubPaths{}, err
	}
	used, err := c.store.ListOrgGitServices(ctx, a.OrgID)
	if err != nil {
		return apitypes.GitHubPaths{}, err
	}
	out := apitypes.GitHubPaths{Repo: r.FullName, Branch: branch, Commit: sha, Roots: []apitypes.GitHubPath{}, Dirs: []string{}, Truncated: truncated || capped}
	for _, d := range slices.Sorted(func(yield func(string) bool) {
		for d := range markers {
			if !yield(d) {
				return
			}
		}
	}) {
		found := markers[d]
		slices.SortFunc(found, func(x, y string) int { return slices.Index(appMarkers, x) - slices.Index(appMarkers, y) })
		out.Roots = append(out.Roots, apitypes.GitHubPath{Path: d, Markers: found, UsedBy: usedBy(used, r, &d)})
	}
	for _, d := range slices.Sorted(func(yield func(string) bool) {
		for d := range dirs {
			if !yield(d) {
				return
			}
		}
	}) {
		if _, root := markers[d]; !root {
			out.Dirs = append(out.Dirs, d)
		}
	}
	return out, nil
}

// orgAppRepo resolves a repository the org's GitHub App reads, with an
// installation token that reads it.
func (c *Core) orgAppRepo(ctx context.Context, orgID int64, repo string) (githubRepo, string, error) {
	name, _, _, err := validGitTarget(repo, "main", "")
	if err != nil {
		return githubRepo{}, "", err
	}
	org, err := c.store.GetOrg(ctx, orgID)
	if err != nil {
		return githubRepo{}, "", err
	}
	inst, r, err := c.appRepo(ctx, org, name)
	if err != nil {
		return githubRepo{}, "", err
	}
	token, err := c.installationToken(ctx, inst)
	return r, token, err
}

// checkAppTarget checks, through the App, that a connection's branch
// exists and its directory is a directory on it, so a typo fails when
// connecting instead of at the first push.
func (c *Core) checkAppTarget(ctx context.Context, token, repo, branch, dir string) error {
	if _, err := c.git.github.branchHead(ctx, token, repo, branch); CodeOf(err) == apitypes.CodeNotFound {
		return notFound("%s has no branch %s; see its branches with `dock github branches %s`", repo, branch, repo)
	} else if err != nil {
		return err
	}
	if dir == "" {
		return nil
	}
	ok, err := c.git.github.isDir(ctx, token, repo, branch, dir)
	if err != nil {
		return err
	}
	if !ok {
		return notFound("%s isn't a directory on %s of %s; see its directories with `dock github paths %s --branch %s`", dir, branch, repo, repo, branch)
	}
	return nil
}

// usedBy names the org's services that deploy from repository r (and,
// with dir, build that directory), as project/environment/service with
// ":dir" when they build a subdirectory.
func usedBy(rows []store.ListOrgGitServicesRow, r githubRepo, dir *string) []string {
	out := []string{}
	for _, row := range rows {
		same := (row.RepoID != 0 && row.RepoID == r.ID) || (row.RepoID == 0 && strings.EqualFold(row.Repo, r.FullName))
		if !same || (dir != nil && row.Dir != *dir) {
			continue
		}
		name := row.Project + "/" + row.Environment + "/" + row.Service
		if dir == nil && row.Dir != "" {
			name += ":" + row.Dir
		}
		out = append(out, name)
	}
	return out
}

// escapePath escapes each segment of a slash-separated path for a URL.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// branches lists a repository's branch names, at most one page.
func (g githubClient) branches(ctx context.Context, token, repo string) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
	}
	if err := g.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/branches?per_page=%d", repo, maxGitHubBranches),
		githubCred{token, credInstall}, "the branches of "+repo, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for _, b := range out {
		names = append(names, b.Name)
	}
	return names, nil
}

// branchHead is a branch's head commit; a missing branch is not found.
func (g githubClient) branchHead(ctx context.Context, token, repo, branch string) (string, error) {
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.call(ctx, http.MethodGet, "/repos/"+repo+"/branches/"+escapePath(branch),
		githubCred{token, credInstall}, "branch "+branch+" of "+repo, &out); err != nil {
		return "", err
	}
	if !shaRe.MatchString(out.Commit.SHA) {
		return "", fmt.Errorf("GitHub described branch %s of %s without a commit", branch, repo)
	}
	return out.Commit.SHA, nil
}

// isDir reports whether dir is a directory at branch: GitHub's contents
// answer for one is a list. Only its first byte is read.
func (g githubClient) isDir(ctx context.Context, token, repo, branch, dir string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	resp, err := g.do(ctx, http.MethodGet, "/repos/"+repo+"/contents/"+escapePath(dir)+"?ref="+url.QueryEscape(branch),
		githubCred{token, credInstall}, dir+" in "+repo, nil)
	if CodeOf(err) == apitypes.CodeNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return false, fmt.Errorf("read GitHub's answer for %s in %s: %w", dir, repo, err)
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return b == '[', nil
		}
	}
}

// treeEntry is one entry of a recursive tree listing: a file ("blob"),
// a directory ("tree") or a submodule ("commit").
type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

// tree walks the recursive tree of commit sha, one entry at a time, until
// visit returns false. It reports whether GitHub cut the listing short.
// The listing is decoded as a stream: only one entry is held at a time.
func (g githubClient) tree(ctx context.Context, token, repo, sha string, visit func(treeEntry) bool) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	what := "the files of " + repo + " at " + shortSHA(sha)
	resp, err := g.do(ctx, http.MethodGet, "/repos/"+repo+"/git/trees/"+sha+"?recursive=1", githubCred{token, credInstall}, what, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxGitHubTree))
	bad := func(err error) (bool, error) { return false, fmt.Errorf("read GitHub's answer for %s: %w", what, err) }
	if err := expectDelim(dec, '{'); err != nil {
		return bad(err)
	}
	truncated := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return bad(err)
		}
		switch key {
		case "tree":
			if err := expectDelim(dec, '['); err != nil {
				return bad(err)
			}
			for dec.More() {
				var e treeEntry
				if err := dec.Decode(&e); err != nil {
					return bad(err)
				}
				if !visit(e) {
					// The rest isn't needed; closing the body ends it.
					return truncated, nil
				}
			}
			if err := expectDelim(dec, ']'); err != nil {
				return bad(err)
			}
		case "truncated":
			if err := dec.Decode(&truncated); err != nil {
				return bad(err)
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return bad(err)
			}
		}
	}
	return truncated, nil
}

// expectDelim reads the next JSON token and checks it is delimiter d.
func expectDelim(dec *json.Decoder, d json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t != d {
		return fmt.Errorf("expected %v, got %v", d, t)
	}
	return nil
}
