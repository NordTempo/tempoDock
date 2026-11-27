package apitypes

import "time"

// ComposeSourceRequest makes an environment follow a Docker Compose file
// in a GitHub repository (ADR 0110), read through the organization's
// GitHub App.
type ComposeSourceRequest struct {
	Repo string `json:"repo" doc:"owner/name; the organization's GitHub App must be installed on it"`
	// Branch is the branch followed, default the repository's default.
	Branch string `json:"branch,omitempty" doc:"Default: the repository's default branch"`
	// Path is the Compose file's path from the repository's root; empty
	// takes the first of the default names (docker-compose.yml, ...).
	Path string `json:"path,omitempty" doc:"The Compose file's path in the repository; default the first of docker-compose.yml, docker-compose.yaml, compose.yaml, compose.yml at its root"`
	// Prune stages archives for the services the file drops (admin).
	Prune bool `json:"prune,omitempty" doc:"Stage an archive for each service the file drops (admin); applying it still needs an admin"`
	// AutoApply applies a sync on its own when it only adds and changes,
	// nothing else is staged in the environment and no secret needs a
	// value; any other sync waits for review.
	AutoApply bool `json:"auto_apply,omitempty" doc:"Apply a sync by itself when it removes nothing, nothing else is staged here, and no secret needs a value"`
}

// ComposeOutcome is what a sync of a followed Compose file did.
type ComposeOutcome string

const (
	// ComposeStaged: its changes wait in the environment for review.
	ComposeStaged ComposeOutcome = "staged"
	// ComposeUnchanged: the environment matches the file, or its changes
	// were staged already.
	ComposeUnchanged ComposeOutcome = "unchanged"
	// ComposeApplied: staged and applied (auto_apply).
	ComposeApplied ComposeOutcome = "applied"
	// ComposeFailed: the file couldn't be read, converted or planned.
	ComposeFailed ComposeOutcome = "failed"
)

// ComposeSource is the Compose file an environment follows.
type ComposeSource struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Repo        string `json:"repo"`
	Branch      string `json:"branch"`
	Path        string `json:"path"`
	Prune       bool   `json:"prune"`
	AutoApply   bool   `json:"auto_apply"`
	// Pending is the commit of a push waiting to be synced.
	Pending string `json:"pending,omitempty"`
	// Last is the last sync, nil before the first.
	Last      *ComposeSync `json:"last,omitempty"`
	CreatedBy string       `json:"created_by"`
	CreatedAt time.Time    `json:"created_at"`
}

// ComposeSync is one sync of a followed Compose file: the commit, when,
// and what came of it.
type ComposeSync struct {
	Commit  Commit         `json:"commit"`
	At      time.Time      `json:"at"`
	Outcome ComposeOutcome `json:"outcome" enum:"staged,unchanged,applied,failed"`
	// Summary says what the sync staged in one line ("+1 service, ~2
	// changes, −1 service"), or why it failed.
	Summary string `json:"summary"`
}

// ComposeSourceStatus is an environment's followed Compose file, if any.
type ComposeSourceStatus struct {
	Source *ComposeSource `json:"source"`
}

// ComposeSyncResult is a sync run now: the source with its outcome, and
// the conversion's warnings and plan (empty when the sync failed).
type ComposeSyncResult struct {
	Source   ComposeSource    `json:"source"`
	Warnings []ComposeWarning `json:"warnings"`
	Plan     ApplyPlan        `json:"plan"`
}
