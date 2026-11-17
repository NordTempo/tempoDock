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
// GET /user/installations lists it and the person can access every
// repository it covers (coversInstallation). The token answers those
// questions and is dropped. Every step checks the browser binding the start page
// set (BindGitHubInstall), and the claim waits for the person to confirm
// the org on Dock's page (ConfirmGitHubInstall).

// GitHubInstallStep is where an install goes next: on to Redirect
// (GitHub's authorize page), to Confirm (Dock's page asking to assign
// the installation), or a page saying Message.
type GitHubInstallStep struct {
	Redirect string
	Confirm  *GitHubInstallConfirm
	Message  string
}

// GitHubInstallConfirm is what the confirm page asks: may org Org deploy
// from GitHub account Account's repositories? State goes back with the
// answer.
type GitHubInstallConfirm struct {
	State, Org, Account string
}

// errNoAppClient refuses what needs the App's OAuth client while it has
// none (an App made before ADR 0066).
func errNoAppClient() error {
	return errf(apitypes.CodeConflict, "the GitHub App can't check who installs it yet; ask an operator to finish its setup with `dock github app secret`")
}

// CompleteGitHubInstall handles GitHub's redirect after the App was
// installed or its repositories changed, for a state from
// StartGitHubInstall, in the browser binding names. An App with its
// OAuth client goes on to GitHub's authorize page (CompleteGitHubVerify
// checks the installer); one without asks to confirm the installation
// for an operator's link and refuses an org admin's. origin is the Dock
// URL the browser has open, which the App's callback URL was registered
// under unless Dock has a public address.
func (c *Core) CompleteGitHubInstall(ctx context.Context, installationID int64, setupAction, state, binding, origin string) (GitHubInstallStep, error) {
	f, err := c.git.app.flow(state, flowInstall, true)
	if err != nil {
		return GitHubInstallStep{}, err
	}
	if !f.boundTo(binding) {
		return GitHubInstallStep{}, errGitHubUnbound()
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
		// Operators are trusted with the platform (ADR 0031): no check
		// of who installed it, but they confirm the org.
		return c.confirmStep(ctx, f, installationID)
	}
	f.kind, f.installation = flowVerify, installationID
	f.verifier = oauth2.GenerateVerifier()
	f.redirect = cmp.Or(c.publicURL(), origin) + GitHubUserCallbackPath
	verify := c.git.app.startFlow(f)
	cfg := c.git.github.userOAuth(app.ClientID, "", f.redirect)
	return GitHubInstallStep{Redirect: cfg.AuthCodeURL(verify, oauth2.S256ChallengeOption(f.verifier))}, nil
}

// CompleteGitHubVerify handles GitHub's redirect back from the App's
// authorize page, in the browser binding names: it exchanges code for
// the person's token and, if the person can access the installation the
// verify state carries on GitHub, asks them to confirm it for its org.
func (c *Core) CompleteGitHubVerify(ctx context.Context, code, state, binding string) (GitHubInstallStep, error) {
	f, err := c.verifyInstaller(ctx, code, state, binding)
	if err != nil {
		return GitHubInstallStep{}, err
	}
	return c.confirmStep(ctx, f, f.installation)
}

// verifyInstaller is CompleteGitHubVerify's check: the verify flow of
// state, once GitHub says its person can access the installation.
func (c *Core) verifyInstaller(ctx context.Context, code, state, binding string) (githubFlow, error) {
	f, err := c.git.app.flow(state, flowVerify, true)
	if err != nil {
		return f, err
	}
	if !f.boundTo(binding) {
		return f, errGitHubUnbound()
	}
	if code == "" {
		return f, errf(apitypes.CodeForbidden, "GitHub didn't confirm who you are, so Dock can't tell the installation is yours; start again with `dock github install` and authorize the App when GitHub asks")
	}
	app, err := c.githubApp(ctx)
	if err != nil {
		return f, err
	}
	if app.ClientSecret == nil {
		return f, errNoAppClient()
	}
	secret, err := c.box.Open(app.ClientSecret, appClientSecretAAD)
	if err != nil {
		return f, fmt.Errorf("open the GitHub App's client secret: %w", err)
	}
	cfg := c.git.github.userOAuth(app.ClientID, string(secret), f.redirect)
	tok, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, c.git.github.client), code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		c.log.Warn("github: user code exchange", "err", err)
		return f, invalid("GitHub didn't accept the App's sign-in code; start again with `dock github install`. If it keeps failing, an operator checks the App's client secret (`dock github app secret`) and that its callback URL is %s", f.redirect)
	}
	ids, err := c.git.github.userInstallations(ctx, tok.AccessToken)
	if err != nil {
		return f, err
	}
	if !slices.Contains(ids, f.installation) {
		return f, errf(apitypes.CodeForbidden, "your GitHub account can't access installation %d of the GitHub App, so Dock didn't give it to org %s; install the App from Dock with an account that can", f.installation, f.org.Slug)
	}
	return f, c.coversInstallation(ctx, f, tok.AccessToken)
}

// coversInstallation refuses an installer who can't access every
// repository installation f.installation covers. GitHub lists an
// installation to anyone with explicit access to one of its
// repositories, and Dock reads all of them: a member with read access to
// one repository mustn't hand an org the rest. The person's repositories
// in the installation are a subset of the installation's, so equal
// counts are equal sets.
func (c *Core) coversInstallation(ctx context.Context, f githubFlow, userToken string) error {
	mine, err := c.git.github.userInstallationRepoCount(ctx, userToken, f.installation)
	if err != nil {
		return err
	}
	token, err := c.installationToken(ctx, f.installation)
	if err != nil {
		return err
	}
	all, err := c.git.github.installationRepoCount(ctx, token)
	if err != nil {
		return err
	}
	if mine < all {
		return errf(apitypes.CodeForbidden, "installation %d of the GitHub App covers %d repositories and your GitHub account can access %d of them, so Dock didn't give it to org %s: Dock would read all of them. Ask an owner of the GitHub account to install it from Dock, or to limit the installation to repositories you can access", f.installation, all, mine, f.org.Slug)
	}
	return nil
}

// confirmStep asks the person of install flow f to confirm org f.org may
// deploy from installation id, naming its GitHub account (read with the
// App's own credentials, not from the redirect).
func (c *Core) confirmStep(ctx context.Context, f githubFlow, id int64) (GitHubInstallStep, error) {
	jwt, err := c.appJWT(ctx)
	if err != nil {
		return GitHubInstallStep{}, err
	}
	inst, err := c.git.github.installation(ctx, jwt, id)
	if err != nil {
		return GitHubInstallStep{}, err
	}
	f.kind, f.installation, f.account = flowConfirm, id, cleanLine(inst.Account.Login, 39)
	state := c.git.app.startFlow(f)
	return GitHubInstallStep{Confirm: &GitHubInstallConfirm{State: state, Org: f.org.Slug, Account: f.account}}, nil
}

// ConfirmGitHubInstall assigns the installation the confirm page asked
// about (state) to its org, from the browser binding names, unless
// another org has it (conflict). Returns what happened, for the page the
// browser lands on.
func (c *Core) ConfirmGitHubInstall(ctx context.Context, state, binding string) (string, error) {
	f, err := c.git.app.flow(state, flowConfirm, true)
	if err != nil {
		return "", err
	}
	if !f.boundTo(binding) {
		return "", errGitHubUnbound()
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
	if err := a.requirePlatform(PlatformSettings, "setting the GitHub App's client secret"); err != nil {
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
