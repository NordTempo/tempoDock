package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The directory picker lists the directories that look like an app, with
// their marker files and the services that build them, and leaves out
// dependencies and hidden directories.
func TestGitHubPaths(t *testing.T) {
	h, gh, _ := appHarness(t)
	gh.mu.Lock()
	gh.files["package.json"] = "{}"
	gh.files["apps/web/package.json"] = "{}"
	gh.files["apps/api/go.mod"] = "module api"
	gh.files["apps/web/node_modules/left-pad/package.json"] = "{}"
	gh.files[".github/workflows/Dockerfile"] = "FROM x"
	gh.files["docs/guide.md"] = "# guide"
	gh.branches = []string{"dev"}
	gh.mu.Unlock()
	web := h.service("web", apitypes.CreateServiceRequest{Name: "web"})
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web"}); err != nil {
		t.Fatal(err)
	}

	p, err := h.c.GitHubPaths(h.ctx, testSystem, "acme/shop", "")
	if err != nil {
		t.Fatal(err)
	}
	type root struct{ path, markers, used string }
	var got []root
	for _, r := range p.Roots {
		got = append(got, root{r.Path, strings.Join(r.Markers, ","), strings.Join(r.UsedBy, ",")})
	}
	want := []root{{"", "package.json", ""}, {"apps/api", "go.mod", ""}, {"apps/web", "Dockerfile,dock.toml,package.json", "shop/production/web"}}
	if !slices.Equal(got, want) || p.Branch != "main" || p.Commit != shaC || p.Truncated {
		t.Fatalf("paths = %+v (roots %+v)", p, got)
	}
	if !slices.Equal(p.Dirs, []string{"apps", "docs"}) {
		t.Fatalf("other dirs = %v", p.Dirs)
	}
	if _, err := h.c.GitHubPaths(h.ctx, testSystem, "acme/shop", "nope"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("missing branch: %v", err)
	}

	b, err := h.c.GitHubBranches(h.ctx, testSystem, "acme/shop")
	if err != nil || !slices.Equal(b.Branches, []string{"main", "dev"}) || b.DefaultBranch != "main" {
		t.Fatalf("branches = %+v, %v", b, err)
	}
	repos, err := h.c.GitHubRepos(h.ctx, testSystem)
	if err != nil || len(repos.Repos) != 1 || !slices.Equal(repos.Repos[0].UsedBy, []string{"shop/production/web:apps/web"}) {
		t.Fatalf("repos = %+v, %v", repos, err)
	}

	reader := Actor{Kind: "user", Name: "ro", UserID: 5, OrgID: 1, Scope: apitypes.ScopeRead}
	if _, err := h.c.GitHubPaths(h.ctx, reader, "acme/shop", ""); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read scope: %v", err)
	}
}

// A service created from a repository is connected through the App and
// its branch's head deploys; a branch or directory that isn't there fails
// before anything is created, and so does connecting to one.
func TestCreateServiceFromRepo(t *testing.T) {
	h, _, _ := appHarness(t)
	h.service("api", apitypes.CreateServiceRequest{Name: "api"})
	env := EnvRef{"shop", "production"}
	for name, git := range map[string]apitypes.ServiceGitRequest{
		"missing dir":    {Repo: "acme/shop", Dir: "apps/wbe"},
		"missing branch": {Repo: "acme/shop", Branch: "nope", Dir: "apps/web"},
	} {
		_, err := h.c.CreateService(h.ctx, testSystem, env, apitypes.CreateServiceRequest{Name: "web", Git: &git})
		if CodeOf(err) != apitypes.CodeNotFound {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := h.c.GetService(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a failed create left a service: %v", err)
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, env, apitypes.CreateServiceRequest{Name: "web", Image: "x:1", Git: &apitypes.ServiceGitRequest{Repo: "acme/shop"}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("image and repo: %v", err)
	}
	api := ServiceRef{"shop", "production", "api"}
	if _, err := h.c.GitConnect(h.ctx, testSystem, api, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/missing"}); CodeOf(err) != apitypes.CodeNotFound ||
		!strings.Contains(err.Error(), "dock github paths acme/shop") {
		t.Fatalf("connect to a missing dir: %v", err)
	}

	if _, err := h.c.CreateService(h.ctx, testSystem, env, apitypes.CreateServiceRequest{Name: "web", Git: &apitypes.ServiceGitRequest{Repo: "acme/shop", Dir: "apps/web"}}); err != nil {
		t.Fatal(err)
	}
	web := ServiceRef{"shop", "production", "web"}
	src, err := h.c.GitStatus(h.ctx, testSystem, web)
	if err != nil || src.Connection == nil || src.Connection.Mode != apitypes.GitModeApp || src.Connection.Branch != "main" || src.Connection.Dir != "apps/web" {
		t.Fatalf("source = %+v, %v", src, err)
	}
	h.waitReleases(web, 1)
	if rel := h.wait(web, 1); rel.Commit == nil || rel.Commit.SHA != shaC {
		t.Fatalf("first release = %+v", rel)
	}
}
