package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/oauth2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Checking who installed the GitHub App (ADR 0066). GitHub's redirect
// after an install names the installation in its URL, which anyone can
// edit. So when the App has its OAuth client, Dock doesn't claim the
// installation there: it sends the browser on to the App's authorize page
// on GitHub with a new single-use state, exchanges the code GitHub comes
// back with for the person's token, and claims the installation only if
// GET /user/installations lists it. The token answers that one question
// and is dropped.

// GitHubInstallStep is where GitHub's install redirect goes next: on to
// Redirect (GitHub's authorize page), or a page saying Message.
type GitHubInstallStep struct {
	Redirect string
	Message  string
}

// errNoAppClient refuses what needs the App's OAuth client while it has
// none (an App made before ADR 0066).
func errNoAppClient() error {
	return errf(apitypes.CodeConflict, "the GitHub App can't check who installs it yet; ask an operator to finish its setup with `dock github app secret`")
}

// CompleteGitHubInstall handles GitHub's redirect after the App was
// installed or its repositories changed, for a state from
// StartGitHubInstall. An App with its OAuth client goes on to GitHub's
// authorize page (CompleteGitHubVerify claims the installation); one
// without claims the installation for an operator's link and refuses an
// org admin's. origin is the Dock URL the browser has open, which the
// App's callback URL was registered under unless Dock has a public
// address.
func (c *Core) CompleteGitHubInstall(ctx context.Context, installationID int64, setupAction, state, origin string) (GitHubInstallStep, error) {
	f, err := c.git.app.flow(state, flowInstall, true)
	if err != nil {
		return GitHubInstallStep{}, err
	}
	if setupAction == "request" {
		return GitHubInstallStep{Message: fmt.Sprintf("The installation was requested. Once an owner of the GitHub account approves it, open the install link again from Dock, or an operator assigns it with `dock github assign <id> %s`.", f.org.Slug)}, nil
	}
	if installationID <= 0 {
		return GitHubInstallStep{}, invalid("GitHub's redirect names no installation; start again with `dock github install <org>`")
	}
	app, err := c.githubApp(ctx)
	if err != nil {
		return GitHubInstallStep{}, err
	}
	if app.ClientSecret == nil {
		if !f.actor.Operator {
			return GitHubInstallStep{}, errNoAppClient()
		}
		// Operators are trusted with the platform (ADR 0031): claim it.
		msg, err := c.claimInstallation(ctx, f, installationID)
		return GitHubInstallStep{Message: msg}, err
	}
	f.kind, f.installation = flowVerify, installationID
	f.verifier = oauth2.GenerateVerifier()
	f.redirect = cmp.Or(c.publicURL(), origin) + GitHubUserCallbackPath
	verify := c.git.app.startFlow(f)
	cfg := c.git.github.userOAuth(app.ClientID, "", f.redirect)
	return GitHubInstallStep{Redirect: cfg.AuthCodeURL(verify, oauth2.S256ChallengeOption(f.verifier))}, nil
}

// CompleteGitHubVerify handles GitHub's redirect back from the App's
// authorize page: it exchanges code for the person's token and claims the
// installation the verify state carries for its org, if the person can
// access it on GitHub. Returns what happened, for the page the browser
// lands on.
func (c *Core) CompleteGitHubVerify(ctx context.Context, code, state string) (string, error) {
	f, err := c.git.app.flow(state, flowVerify, true)
	if err != nil {
		return "", err
	}
	if code == "" {
		return "", errf(apitypes.CodeForbidden, "GitHub didn't confirm who you are, so Dock can't tell the installation is yours; start again with `dock github install` and authorize the App when GitHub asks")
	}
	app, err := c.githubApp(ctx)
	if err != nil {
		return "", err
	}
	if app.ClientSecret == nil {
		return "", errNoAppClient()
	}
	secret, err := c.box.Open(app.ClientSecret, appClientSecretAAD)
	if err != nil {
		return "", fmt.Errorf("open the GitHub App's client secret: %w", err)
	}
	cfg := c.git.github.userOAuth(app.ClientID, string(secret), f.redirect)
	tok, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, c.git.github.client), code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		c.log.Warn("github: user code exchange", "err", err)
		return "", invalid("GitHub didn't accept the App's sign-in code; start again with `dock github install`. If it keeps failing, an operator checks the App's client secret (`dock github app secret`) and that its callback URL is %s", f.redirect)
	}
	ids, err := c.git.github.userInstallations(ctx, tok.AccessToken)
	if err != nil {
		return "", err
	}
	if !slices.Contains(ids, f.installation) {
		return "", errf(apitypes.CodeForbidden, "your GitHub account can't access installation %d of the GitHub App, so Dock didn't give it to org %s; install the App from Dock with an account that can", f.installation, f.org.Slug)
	}
	return c.claimInstallation(ctx, f, f.installation)
}

// claimInstallation assigns an installation to the org of the install
// link f, unless another org has it (conflict), and says what happened.
func (c *Core) claimInstallation(ctx context.Context, f githubFlow, id int64) (string, error) {
	inst, err := c.assignInstallation(ctx, f.actor, id, f.org, false)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("The GitHub App can read %s's repositories, and org %s can now deploy from them: `dock git connect %s/<repo>`.", inst.Account.Login, f.org.Slug, inst.Account.Login), nil
}

// SetGitHubAppClientSecret gives an App made before ADR 0066 its OAuth
// client, so installs are checked: Dock reads the client ID from GitHub
// and checks the pair before storing the secret, sealed. Operators only.
func (c *Core) SetGitHubAppClientSecret(ctx context.Context, a Actor, req apitypes.GitHubClientSecretRequest) error {
	if err := a.requireOperator("setting the GitHub App's client secret"); err != nil {
		return err
	}
	secret := strings.TrimSpace(req.ClientSecret)
	if secret == "" {
		return invalid("give the client secret generated in the App's settings on GitHub (General > Client secrets)")
	}
	app, err := c.githubApp(ctx)
	if err != nil {
		return err
	}
	jwt, err := c.appJWT(ctx)
	if err != nil {
		return err
	}
	clientID, err := c.git.github.appClientID(ctx, jwt)
	if err != nil {
		return err
	}
	if err := c.git.github.checkClient(ctx, clientID, secret); err != nil {
		return err
	}
	sealed, err := c.box.Seal([]byte(secret), appClientSecretAAD)
	if err != nil {
		return err
	}
	n, err := c.store.SetGitHubAppClient(ctx, store.SetGitHubAppClientParams{ClientID: cleanLine(clientID, 100), ClientSecret: sealed})
	if err != nil {
		return err
	}
	if n == 0 {
		return noGitHubApp()
	}
	c.audit(ctx, a.platform(), "github.app_client", nil, nil, app.Slug, "set the GitHub App's client secret, so installs are checked")
	return nil
}
