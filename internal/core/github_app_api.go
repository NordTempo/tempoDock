package core

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The GitHub App's side of GitHub's REST API (ADR 0031): the manifest
// conversion, the App's JWT and installation tokens, and the installation
// and repository lookups. github_app.go uses them.

// appJWT signs the App's JSON Web Token (RS256), which authenticates as
// the App itself: listing installations and getting installation tokens.
// GitHub accepts at most 10 minutes; iat is a minute early for clock
// drift.
func appJWT(appID int64, key *rsa.PrivateKey, t time.Time) (string, error) {
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{t.Add(-time.Minute).Unix(), t.Add(9 * time.Minute).Unix(), strconv.FormatInt(appID, 10)})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// parseAppKey reads the App's private key, a PEM block GitHub hands out
// in PKCS #1 (PKCS #8 is accepted too).
func parseAppKey(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("the GitHub App's private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse the GitHub App's private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the GitHub App's private key is not RSA")
	}
	return rk, nil
}

// call sends a request and decodes its JSON answer into v.
func (g githubClient) call(ctx context.Context, method, path string, cred githubCred, what string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	resp, err := g.do(ctx, method, path, cred, what, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGitHubJSON)).Decode(v); err != nil {
		return fmt.Errorf("read GitHub's answer for %s: %w", what, err)
	}
	return nil
}

// githubAppCreated is what GitHub returns for a manifest's code: the new
// App with its credentials, shown this once.
type githubAppCreated struct {
	ID            int64         `json:"id"`
	Slug          string        `json:"slug"`
	Name          string        `json:"name"`
	HTMLURL       string        `json:"html_url"`
	Owner         githubAccount `json:"owner"`
	WebhookSecret string        `json:"webhook_secret"`
	PEM           string        `json:"pem"`
	// The App's OAuth client, which checks who installs it (ADR 0066).
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// convertManifest exchanges the code GitHub's manifest flow redirected
// with for the App it created. The code is single use and expires after
// an hour; the call needs no authentication.
func (g githubClient) convertManifest(ctx context.Context, code string) (githubAppCreated, error) {
	var out githubAppCreated
	err := g.call(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", githubCred{}, "the GitHub App's manifest code", &out)
	return out, err
}

// githubAccount is a GitHub user or organization: an installation's,
// or an App's owner. The ID survives renames.
type githubAccount struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

// githubInstallation is one of the App's installations, as GitHub lists
// it.
type githubInstallation struct {
	ID                  int64         `json:"id"`
	Account             githubAccount `json:"account"`
	RepositorySelection string        `json:"repository_selection"`
	HTMLURL             string        `json:"html_url"`
	SuspendedAt         *time.Time    `json:"suspended_at"`
}

// installations lists the App's installations (the first 100: more is far
// beyond one install's use).
func (g githubClient) installations(ctx context.Context, jwt string) ([]githubInstallation, error) {
	var out []githubInstallation
	err := g.call(ctx, http.MethodGet, "/app/installations?per_page=100", githubCred{jwt, credApp}, "the GitHub App's installations", &out)
	return out, err
}

// installation looks up one of the App's installations.
func (g githubClient) installation(ctx context.Context, jwt string, id int64) (githubInstallation, error) {
	var out githubInstallation
	err := g.call(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(id, 10), githubCred{jwt, credApp}, fmt.Sprintf("installation %d of the GitHub App", id), &out)
	return out, err
}

// repoInstallation is the App's installation that can read repo.
func (g githubClient) repoInstallation(ctx context.Context, jwt, repo string) (githubInstallation, error) {
	var out githubInstallation
	err := g.call(ctx, http.MethodGet, "/repos/"+repo+"/installation", githubCred{jwt, credApp}, "GitHub App installation for "+repo, &out)
	return out, err
}

// accessToken gets an installation token: an hour's access to the
// installation's repositories, with the App's permissions.
func (g githubClient) accessToken(ctx context.Context, jwt string, id int64) (string, time.Time, error) {
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := g.call(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(id, 10)+"/access_tokens", githubCred{jwt, credApp},
		fmt.Sprintf("installation %d of the GitHub App", id), &out)
	if err == nil && out.Token == "" {
		err = fmt.Errorf("GitHub returned no token for installation %d", id)
	}
	return out.Token, out.ExpiresAt, err
}

// githubRepo is a repository as GitHub describes it.
type githubRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	// PushedAt is the newest push to any branch.
	PushedAt *time.Time `json:"pushed_at"`
}

// repo looks up a repository: its ID and canonical name.
func (g githubClient) repo(ctx context.Context, token, repo string) (githubRepo, error) {
	var out githubRepo
	err := g.call(ctx, http.MethodGet, "/repos/"+repo, githubCred{token, credInstall}, "repository "+repo, &out)
	return out, err
}

// installationRepos lists the repositories an installation token can
// read, at most limit of them, and whether there were more.
func (g githubClient) installationRepos(ctx context.Context, token string, limit int) ([]githubRepo, bool, error) {
	var all []githubRepo
	for page := 1; ; page++ {
		var out struct {
			TotalCount   int          `json:"total_count"`
			Repositories []githubRepo `json:"repositories"`
		}
		if err := g.call(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page),
			githubCred{token, credInstall}, "the installation's repositories", &out); err != nil {
			return nil, false, err
		}
		all = append(all, out.Repositories...)
		switch {
		case len(all) >= limit:
			return all[:limit], out.TotalCount > limit, nil
		case len(out.Repositories) < 100 || len(all) >= out.TotalCount:
			return all, false, nil
		}
	}
}

// userInstallationRepoCount is how many of installation id's
// repositories a person's token has explicit access to (GET
// /user/installations/{id}/repositories, its total_count).
func (g githubClient) userInstallationRepoCount(ctx context.Context, token string, id int64) (int, error) {
	var out struct {
		TotalCount int `json:"total_count"`
	}
	err := g.call(ctx, http.MethodGet, fmt.Sprintf("/user/installations/%d/repositories?per_page=1", id),
		githubCred{token, credUser}, "your repositories in the installation", &out)
	return out.TotalCount, err
}

// installationRepoCount is how many repositories an installation token
// reads (GET /installation/repositories, its total_count).
func (g githubClient) installationRepoCount(ctx context.Context, token string) (int, error) {
	var out struct {
		TotalCount int `json:"total_count"`
	}
	err := g.call(ctx, http.MethodGet, "/installation/repositories?per_page=1", githubCred{token, credInstall}, "the installation's repositories", &out)
	return out.TotalCount, err
}

// appClientID reads the App's OAuth client ID (GET /app), for an App
// whose client Dock didn't keep when it was made.
func (g githubClient) appClientID(ctx context.Context, jwt string) (string, error) {
	var out struct {
		ClientID string `json:"client_id"`
	}
	err := g.call(ctx, http.MethodGet, "/app", githubCred{jwt, credApp}, "the GitHub App", &out)
	if err == nil && out.ClientID == "" {
		err = errors.New("GitHub returned no client ID for the GitHub App")
	}
	return out.ClientID, err
}

// checkClient checks an OAuth client ID and secret with GitHub: it asks
// about a made-up token (POST /applications/{client_id}/token), which
// GitHub answers 404 for a right pair and 401 for a wrong one.
func (g githubClient) checkClient(ctx context.Context, clientID, secret string) error {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	body := strings.NewReader(`{"access_token":"ghu_` + strings.Repeat("0", 36) + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.api+"/applications/"+url.PathEscape(clientID)+"/token", body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(clientID, secret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return errf(apitypes.CodeConflict, "could not reach GitHub: %v; try again", redactURL(err))
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return invalid("GitHub rejected the client secret for the GitHub App (client %s); generate a new one in the App's settings on GitHub and give it again", clientID)
	}
	return errf(apitypes.CodeConflict, "GitHub returned %d checking the GitHub App's client secret; try again", resp.StatusCode)
}

// userOAuth is the App's OAuth client: GitHub's authorize page, where an
// installer proves who they are (ADR 0066), and the code exchange.
func (g githubClient) userOAuth(clientID, secret, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: clientID, ClientSecret: secret, RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{AuthURL: g.web + "/login/oauth/authorize", TokenURL: g.web + "/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams},
	}
}

// maxUserInstallationPages bounds the pages of a person's installations
// read (100 each): far more installations of one App than anyone has.
const maxUserInstallationPages = 10

// userInstallations lists the IDs of the App's installations a person's
// token can access (GET /user/installations).
func (g githubClient) userInstallations(ctx context.Context, token string) ([]int64, error) {
	var ids []int64
	for page := 1; page <= maxUserInstallationPages; page++ {
		var out struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		if err := g.call(ctx, http.MethodGet, fmt.Sprintf("/user/installations?per_page=100&page=%d", page),
			githubCred{token, credUser}, "your installations of the GitHub App", &out); err != nil {
			return nil, err
		}
		for _, i := range out.Installations {
			ids = append(ids, i.ID)
		}
		if len(out.Installations) < 100 {
			break
		}
	}
	return ids, nil
}
