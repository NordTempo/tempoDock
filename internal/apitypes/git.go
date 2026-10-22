package apitypes

import "time"

// Commit is the git commit a release was built from (ADR 0018).
type Commit struct {
	SHA string `json:"sha" doc:"Full commit SHA"`
	// Message is the commit message's first line, at most 200 characters.
	Message string `json:"message"`
	Author  string `json:"author" doc:"GitHub login of the author, else their name"`
}

// GitSource is a service's GitHub push-to-deploy state.
type GitSource struct {
	// Connection is nil when the service has no GitHub source.
	Connection *GitConnection `json:"connection,omitempty"`
	// HooksReady is false while the server has no [hooks] public_host:
	// GitHub has nowhere to deliver pushes. HooksHint says how to fix it.
	HooksReady bool   `json:"hooks_ready"`
	HooksHint  string `json:"hooks_hint,omitempty"`
}

// GitConnection is a service's connected repository, branch and
// directory. The webhook secret and the token are never returned.
type GitConnection struct {
	Repo   string `json:"repo" doc:"owner/name"`
	Branch string `json:"branch"`
	Dir    string `json:"dir,omitempty" doc:"Subdirectory used as the build context; empty for the repository root"`
	// TokenSet says whether a GitHub token is stored (it never leaves the
	// server).
	TokenSet bool `json:"token_set"`
	// Status posts commit statuses (pending, success, failure) to GitHub;
	// needs a token.
	Status bool `json:"status"`
	// WebhookURL is the Payload URL to set on GitHub; empty while the
	// server has no [hooks] public_host.
	WebhookURL string `json:"webhook_url"`
	// Pending is a push waiting for the current deploy to finish.
	Pending *Commit `json:"pending,omitempty"`
	// LastEvent says what happened to the latest delivery or manual deploy,
	// e.g. "push abc1234: queued" or "push to refs/heads/dev: ignored".
	LastEvent   string     `json:"last_event,omitempty"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
}

// GitConnectRequest connects a service to a GitHub repository, or changes
// an existing connection.
type GitConnectRequest struct {
	Repo   string `json:"repo" minLength:"3" maxLength:"140" doc:"owner/name"`
	Branch string `json:"branch" minLength:"1" maxLength:"255"`
	Dir    string `json:"dir,omitempty" maxLength:"255" doc:"Subdirectory to build; empty for the repository root"`
	// Token sets the GitHub token: omitted keeps the stored one, "" removes
	// it. A fine-grained token with Contents read (and Commit statuses
	// write for Status).
	Token *string `json:"token,omitempty" maxLength:"512"`
	// Status turns commit statuses on or off; omitted keeps the setting
	// (off for a new connection).
	Status *bool `json:"status,omitempty"`
	// RotateSecret replaces the webhook URL and secret of an existing
	// connection. New connections always get new ones.
	RotateSecret bool `json:"rotate_secret,omitempty"`
}

// GitConnectResult is the connection and, when it was created or rotated,
// the webhook secret: shown once, then never again.
type GitConnectResult struct {
	Connection GitConnection `json:"connection"`
	Created    bool          `json:"created"`
	// Secret is the webhook secret, only when new.
	Secret string `json:"secret,omitempty"`
	// HooksHint says how to give the server a public webhook address, when
	// it has none yet.
	HooksHint string `json:"hooks_hint,omitempty"`
}

// GitDeployRequest deploys a commit of the connected branch now.
type GitDeployRequest struct {
	Ref string `json:"ref,omitempty" maxLength:"255" doc:"Commit SHA, branch or tag; default the connected branch's head"`
}

// GitDeployResult is the commit queued for deployment. Its release starts
// as soon as no other deploy of the service is running.
type GitDeployResult struct {
	Commit Commit `json:"commit"`
}
