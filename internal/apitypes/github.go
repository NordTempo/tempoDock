package apitypes

import "time"

// GitHubStatus is the install's GitHub App and its installations (ADR
// 0031), for operators.
type GitHubStatus struct {
	// App is nil until an operator creates one (`dock github app create`).
	App *GitHubApp `json:"app,omitempty"`
	// WebhookURL is where the App's deliveries go: [hooks] public_host or
	// Dock's public address. Empty while there is neither; HooksHint says
	// how to fix it.
	WebhookURL string `json:"webhook_url"`
	HooksHint  string `json:"hooks_hint,omitempty"`
	// Installations are the App's installations on GitHub, with the org
	// each is assigned to. Assigned ones GitHub no longer lists are
	// included with Missing set.
	Installations []GitHubInstallation `json:"installations"`
	// InstallationsError says why GitHub couldn't be asked; Installations
	// then holds only what Dock has assigned.
	InstallationsError string `json:"installations_error,omitempty"`
	// Orgs are every org's slug, to assign installations to.
	Orgs []string `json:"orgs"`
}

// GitHubApp is the install's GitHub App. Its private key and webhook
// secret never leave the server.
type GitHubApp struct {
	ID        int64     `json:"id" doc:"GitHub's App ID"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner" doc:"The GitHub account that owns the App"`
	URL       string    `json:"url" doc:"The App's page on GitHub"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// GitHubInstallation is one of the App's installations: a GitHub account
// whose repositories (all, or selected ones) the App can read.
type GitHubInstallation struct {
	ID           int64  `json:"id"`
	Account      string `json:"account"`
	AccountType  string `json:"account_type,omitempty" doc:"User or Organization"`
	Repositories string `json:"repositories,omitempty" enum:"all,selected" doc:"Whether the App can read all of the account's repositories or selected ones"`
	// Org is the slug of the org that deploys from it; empty while it is
	// unassigned.
	Org string `json:"org,omitempty"`
	// Connections counts the services connected through it.
	Connections int  `json:"connections"`
	Suspended   bool `json:"suspended,omitempty"`
	// Missing is set for an assigned installation GitHub no longer lists:
	// the App was uninstalled while Dock wasn't told.
	Missing bool `json:"missing,omitempty"`
	// URL is the installation's settings on GitHub, where its repositories
	// are chosen.
	URL string `json:"url,omitempty"`
}

// GitHubAppRequest starts registering the App.
type GitHubAppRequest struct {
	// Owner is the GitHub organization to register the App under; empty
	// registers it on the account of whoever opens the link.
	Owner string `json:"owner,omitempty" maxLength:"39"`
}

// GitHubLink is a page to open in a browser to continue on GitHub. URL is
// absolute, or a path on this Dock server when it starts with "/".
type GitHubLink struct {
	URL string `json:"url"`
}

// GitHubOrgRequest names the org an installation is for.
type GitHubOrgRequest struct {
	Org string `json:"org" minLength:"1" maxLength:"32" doc:"The org's slug"`
}

// GitHubRepos are the repositories the caller's org can connect through
// the App.
type GitHubRepos struct {
	Repos []GitHubRepo `json:"repos"`
	// Truncated is set when there were more than Dock lists.
	Truncated bool `json:"truncated,omitempty"`
}

// GitHubRepo is a repository the App can read.
type GitHubRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name" doc:"owner/name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}
