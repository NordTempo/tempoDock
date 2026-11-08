package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/version"
)

const (
	// githubAPI is GitHub's REST API.
	githubAPI = "https://api.github.com"
	// githubFetchTimeout bounds fetching one commit's tarball.
	githubFetchTimeout = 10 * time.Minute
	// githubCallTimeout bounds every other GitHub call.
	githubCallTimeout = 15 * time.Second
	// maxGitHubJSON caps what is read of a GitHub JSON response.
	maxGitHubJSON = 1 << 20
)

// githubClient is the little of GitHub's REST API push-to-deploy uses:
// a commit's metadata, its tarball, and commit statuses, plus the GitHub
// App's calls (github_app_api.go). Tokens go in the Authorization header
// only, and never into errors or logs.
type githubClient struct {
	api string
	// web is GitHub's web origin, for the App's OAuth pages (ADR 0066). A
	// GitHub other than github.com (tests' fake) serves both from api.
	web    string
	client *http.Client
}

func newGitHubClient(api string) githubClient {
	if api == "" {
		return githubClient{api: githubAPI, web: githubWeb, client: &http.Client{}}
	}
	api = strings.TrimSuffix(api, "/")
	return githubClient{api: api, web: api, client: &http.Client{}}
}

// githubCred is how a request authenticates, which decides what an error
// tells the user to fix.
type githubCred struct {
	token string
	kind  credKind
}

type credKind int

const (
	credToken   credKind = iota // a webhook connection's own token, or none
	credInstall                 // a GitHub App installation token (ADR 0031)
	credApp                     // the GitHub App's JWT
	credUser                    // a person's token for the App, checking an install (ADR 0066)
)

// do sends a request and returns the response if it is a 2xx; anything
// else becomes an error that says what to fix. what names the object for
// a 404 ("commit abc1234").
func (g githubClient) do(ctx context.Context, method, path string, cred githubCred, what string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.api+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "tempo-dock/"+version.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cred.token != "" {
		req.Header.Set("Authorization", "Bearer "+cred.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errf(apitypes.CodeConflict, "could not reach GitHub: %v; try again", redactURL(err))
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, githubError(resp, what, cred)
}

// githubError explains a non-2xx GitHub response, with a hint that fits
// how the request authenticated.
func githubError(resp *http.Response, what string, cred githubCred) error {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
	msg := cleanLine(e.Message, 200)
	switch {
	case resp.StatusCode == http.StatusUnauthorized && cred.kind == credApp:
		return invalid("GitHub rejected the GitHub App's key (401: %s); if the App was deleted on GitHub, remove it with `dock github app remove` and create a new one", msg)
	case resp.StatusCode == http.StatusUnauthorized && cred.kind == credUser:
		return invalid("GitHub rejected your sign-in for the GitHub App (401: %s); start again with `dock github install`", msg)
	case resp.StatusCode == http.StatusUnauthorized && cred.kind == credInstall:
		return invalid("GitHub rejected the GitHub App's installation token (401: %s); check the installation with `dock github status`", msg)
	case resp.StatusCode == http.StatusUnauthorized:
		return invalid("GitHub rejected the token (401: %s); set a new one with `dock git connect ... --token-stdin`", msg)
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		hint := "wait and try again"
		if cred.token == "" {
			hint = "add a token (`dock git connect ... --token-stdin`) for a higher limit, or wait"
		}
		return errf(apitypes.CodeRateLimited, "GitHub's API rate limit was reached; %s", hint)
	case resp.StatusCode == http.StatusForbidden && cred.kind != credToken:
		return invalid("GitHub refused the GitHub App access to %s (403: %s); accept the App's requested permissions on GitHub", what, msg)
	case resp.StatusCode == http.StatusForbidden:
		return invalid("GitHub refused access to %s (403: %s); the token needs Contents read access, and Commit statuses write for statuses", what, msg)
	case resp.StatusCode == http.StatusNotFound:
		hint := "check the repository and ref; a private repository needs a token with Contents read access (`dock git connect ... --token-stdin`)"
		switch {
		case cred.kind == credInstall:
			hint = "check the repository and ref, and that the GitHub App is installed on the repository"
		case cred.kind == credApp:
			hint = "check the name, and that the GitHub App is installed there"
		case cred.token != "":
			hint = "check the repository and ref, and that the token can read the repository"
		}
		return notFound("GitHub has no %s, or can't show it (404); %s", what, hint)
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return invalid("GitHub refused %s (422: %s)", what, msg)
	}
	return errf(apitypes.CodeConflict, "GitHub returned %d for %s (%s); try again", resp.StatusCode, what, msg)
}

// redactURL drops the query from the URL in an HTTP client error:
// GitHub's tarball redirect carries a short-lived token there.
func redactURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		u, _, _ := strings.Cut(ue.URL, "?")
		return fmt.Errorf("%s %s: %w", ue.Op, u, ue.Err)
	}
	return err
}

// commit looks up ref (a SHA, branch or tag) in repo.
func (g githubClient) commit(ctx context.Context, repo, ref string, cred githubCred) (apitypes.Commit, error) {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	// The list endpoint, unlike /commits/{ref}, leaves out the files and
	// their patches.
	resp, err := g.do(ctx, http.MethodGet, "/repos/"+repo+"/commits?per_page=1&sha="+url.QueryEscape(ref), cred, fmt.Sprintf("ref %s in %s", ref, repo), nil)
	if err != nil {
		return apitypes.Commit{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var list []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGitHubJSON)).Decode(&list); err != nil {
		return apitypes.Commit{}, fmt.Errorf("read GitHub commit: %w", err)
	}
	if len(list) == 0 || !shaRe.MatchString(list[0].SHA) {
		return apitypes.Commit{}, notFound("GitHub has no commit for %s in %s", ref, repo)
	}
	c := list[0]
	author := c.Commit.Author.Name
	if c.Author != nil && c.Author.Login != "" {
		author = c.Author.Login
	}
	return apitypes.Commit{SHA: c.SHA, Message: firstLine(c.Commit.Message), Author: cleanLine(author, 100)}, nil
}

// tarball fetches a commit's source and returns it as a plain tar with
// dir (or the repository root) at its root, streamed: nothing is held in
// memory or written to disk here.
func (g githubClient) tarball(ctx context.Context, repo, sha string, cred githubCred, dir string) (io.ReadCloser, error) {
	//nolint:bodyclose // closed by tarballReader.Close and by the goroutine below
	resp, err := g.do(ctx, http.MethodGet, "/repos/"+repo+"/tarball/"+sha, cred, "commit "+shortSHA(sha)+" in "+repo, nil)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GitHub's tarball of %s is not gzip: %w", shortSHA(sha), err)
	}
	pr, pw := io.Pipe()
	go func() {
		err := rerootTar(tar.NewReader(gz), pw, dir)
		_ = resp.Body.Close()
		_ = pw.CloseWithError(err)
	}()
	return &tarballReader{PipeReader: pr, body: resp.Body}, nil
}

// tarballReader is the re-rooted tar. Closing it also closes the download,
// which ends the goroutine that re-roots it.
type tarballReader struct {
	*io.PipeReader
	body io.Closer
}

func (t *tarballReader) Close() error {
	_ = t.body.Close()
	return t.PipeReader.Close()
}

// githubStatus is a commit status.
type githubStatus struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

// status posts a commit status. GitHub caps descriptions at 140
// characters.
func (g githubClient) status(ctx context.Context, repo, sha string, cred githubCred, st githubStatus) error {
	st.Description = cleanLine(st.Description, 140)
	resp, err := g.do(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, cred, "commit "+shortSHA(sha)+" in "+repo, st)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxGitHubJSON))
	return resp.Body.Close()
}

// firstLine is a commit message's subject: its first line, without
// control characters, at most 200 characters.
func firstLine(msg string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return cleanLine(line, 200)
}

// cleanLine drops control characters (a terminal would act on escape
// sequences in a commit message) and cuts s to max runes.
func cleanLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
