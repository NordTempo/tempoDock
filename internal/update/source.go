package update

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// Release is a published release.
type Release struct {
	Version string
	// Name is the release's title; often just the version.
	Name string
	// Notes are the release notes, Markdown.
	Notes string
	// URL is the release's page; empty for a directory source.
	URL         string
	PublishedAt time.Time
	Prerelease  bool
}

// Source is where releases come from: GitHub, or a directory.
type Source interface {
	// Releases lists the published releases, newest first.
	Releases(ctx context.Context) ([]Release, error)
	// Open streams one asset of a release.
	Open(ctx context.Context, version, asset string) (io.ReadCloser, error)
	// String names the source for messages: "github.com/OWNER/REPO" or
	// the directory.
	String() string
}

// NewSource returns the source cfgSource names ([updates] source, already
// validated): a directory when it is an absolute path, else a GitHub
// repository, read with the token in tokenFile when that exists.
func NewSource(cfgSource, tokenFile string) Source {
	if filepath.IsAbs(cfgSource) {
		return DirSource{Path: cfgSource}
	}
	return &GitHub{Repo: strings.TrimPrefix(cfgSource, "github.com/"), TokenFile: tokenFile}
}

// Limits on what a source may send.
const (
	// maxReleaseList caps the release list's JSON (30 releases with notes).
	maxReleaseList = 8 << 20
	// maxNotes caps one directory release's notes.md.
	maxNotes = 256 << 10
	// MaxAsset caps a downloaded asset; the binary is ~35 MB.
	MaxAsset = 512 << 20
	// maxSums caps SHA256SUMS.
	maxSums = 64 << 10
)

// GitHub reads a repository's releases through GitHub's REST API, which
// (unlike release download links) takes a token for private repositories.
type GitHub struct {
	// Repo is OWNER/REPO.
	Repo string
	// TokenFile holds a token that can read the repository's releases;
	// read on every check when it exists, so adding one needs no restart.
	TokenFile string
	// API is the REST API's base URL; empty is https://api.github.com.
	// Tests point it at a fake.
	API string
	// Client defaults to a client without an overall timeout (assets are
	// large); every call is bounded by its ctx.
	Client *http.Client

	// mu guards assets: the asset URLs of the releases last listed, so a
	// download right after a check needs no lookup.
	mu     sync.Mutex
	assets map[string]map[string]string
}

func (g *GitHub) String() string { return "github.com/" + g.Repo }

// ghRelease is the part of GitHub's release object we use.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []struct {
		Name string `json:"name"`
		// URL is the asset's API URL, which serves its bytes with
		// Accept: application/octet-stream.
		URL string `json:"url"`
	} `json:"assets"`
}

// Releases lists the newest 30 releases, without drafts and tags that
// aren't release versions.
func (g *GitHub) Releases(ctx context.Context) ([]Release, error) {
	var list []ghRelease
	if err := g.getJSON(ctx, "/repos/"+g.Repo+"/releases?per_page=30", &list); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(list))
	assets := make(map[string]map[string]string, len(list))
	for _, r := range list {
		if r.Draft || !IsRelease(r.TagName) {
			continue
		}
		out = append(out, Release{
			Version: r.TagName, Name: r.Name, Notes: r.Body, URL: r.HTMLURL,
			PublishedAt: r.PublishedAt, Prerelease: r.Prerelease,
		})
		assets[r.TagName] = r.assetURLs()
	}
	g.mu.Lock()
	g.assets = assets
	g.mu.Unlock()
	sortNewestFirst(out)
	return out, nil
}

func (r ghRelease) assetURLs() map[string]string {
	m := make(map[string]string, len(r.Assets))
	for _, a := range r.Assets {
		m[a.Name] = a.URL
	}
	return m
}

// Open streams an asset of version through the API.
func (g *GitHub) Open(ctx context.Context, version, asset string) (io.ReadCloser, error) {
	g.mu.Lock()
	urls := g.assets[version]
	g.mu.Unlock()
	if urls == nil {
		var r ghRelease
		if err := g.getJSON(ctx, "/repos/"+g.Repo+"/releases/tags/"+version, &r); err != nil {
			return nil, err
		}
		urls = r.assetURLs()
	}
	u, ok := urls[asset]
	if !ok {
		return nil, fmt.Errorf("release %s of %s has no %s", version, g, asset)
	}
	resp, err := g.do(ctx, u, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// getJSON GETs an API path into v.
func (g *GitHub) getJSON(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := g.do(ctx, cmp.Or(g.API, "https://api.github.com")+path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseList)).Decode(v); err != nil {
		return fmt.Errorf("read %s's releases: %w", g, err)
	}
	return nil
}

// do sends a GET with the token, if there is one, and turns error statuses
// into errors that say what to do. Go drops the Authorization header when
// GitHub redirects an asset download to its storage host.
func (g *GitHub) do(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	token, err := g.token()
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach %s: %w", g, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound && token == "":
		return nil, fmt.Errorf("%s answered 404: the repository is private (or gone); put a token that can read its releases in %s", g, g.TokenFile)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s answered 404: the release or repository doesn't exist, or the token in %s can't read it", g, g.TokenFile)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%s refused the token in %s (401); replace it", g, g.TokenFile)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%s answered %d: probably GitHub's rate limit; try again later, or put a token in %s", g, resp.StatusCode, g.TokenFile)
	}
	return nil, fmt.Errorf("%s answered %s", g, resp.Status)
}

// token reads TokenFile; a missing file is no token.
func (g *GitHub) token() (string, error) {
	if g.TokenFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(g.TokenFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the GitHub token: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// DirSource is a directory of releases, one subdirectory per version holding
// the release's assets and, optionally, notes.md. A release's date is
// its directory's modification time.
type DirSource struct{ Path string }

func (d DirSource) String() string { return d.Path }

// Releases lists the version directories, newest first.
func (d DirSource) Releases(context.Context) ([]Release, error) {
	entries, err := os.ReadDir(d.Path)
	if err != nil {
		return nil, fmt.Errorf("read releases in %s: %w", d.Path, err)
	}
	var out []Release
	for _, e := range entries {
		if !e.IsDir() || !IsRelease(e.Name()) {
			continue
		}
		r := Release{Version: e.Name(), Name: e.Name(), Prerelease: semver.Prerelease(e.Name()) != ""}
		if info, err := e.Info(); err == nil {
			r.PublishedAt = info.ModTime().UTC()
		}
		if f, err := os.Open(filepath.Join(d.Path, e.Name(), "notes.md")); err == nil {
			b, _ := io.ReadAll(io.LimitReader(f, maxNotes))
			_ = f.Close()
			r.Notes = string(b)
		}
		out = append(out, r)
	}
	sortNewestFirst(out)
	return out, nil
}

// Open opens an asset file.
func (d DirSource) Open(_ context.Context, version, asset string) (io.ReadCloser, error) {
	if !IsRelease(version) || !filepath.IsLocal(asset) || strings.ContainsRune(asset, '/') {
		return nil, fmt.Errorf("no asset %q in release %q", asset, version)
	}
	f, err := os.Open(filepath.Join(d.Path, version, asset))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("release %s in %s has no %s", version, d.Path, asset)
	}
	return f, err
}

func sortNewestFirst(rs []Release) {
	slices.SortFunc(rs, func(a, b Release) int { return semver.Compare(b.Version, a.Version) })
}
