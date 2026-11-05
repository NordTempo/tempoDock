package apitypes

import "time"

// Preview environments per pull request (ADR 0056): a project's base
// environment is cloned into pr-<number> for every pull request to a
// repository its services deploy, and archived when the pull request
// closes.

// PreviewSettings is a project's preview setup and its open previews
// (`dock env previews`).
type PreviewSettings struct {
	Enabled bool `json:"enabled"`
	// Base is the environment previews are cloned from ("" when off).
	Base        string `json:"base,omitempty" doc:"Environment previews are cloned from"`
	MaxPreviews int    `json:"max_previews" doc:"Most open previews at once; a pull request beyond it gets no preview until one closes"`
	// CopyVariables are the base's variables whose literal values
	// previews copy; no other literal value is ever copied.
	CopyVariables []string   `json:"copy_variables" doc:"Base variables whose literal values previews copy, as web.KEY or shared.KEY; no other literal value is copied"`
	AllowForks    bool       `json:"allow_forks" doc:"Also preview pull requests from forks (off by default)"`
	UpdatedBy     string     `json:"updated_by,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	Previews      []Preview  `json:"previews" doc:"Open previews, newest first"`
}

// SetPreviewsRequest turns previews on, or changes their settings.
type SetPreviewsRequest struct {
	Base          string   `json:"base" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"Environment to clone previews from, e.g. staging"`
	MaxPreviews   int      `json:"max_previews,omitempty" minimum:"0" maximum:"20" doc:"Most open previews at once (default 3)"`
	CopyVariables []string `json:"copy_variables,omitempty" maxItems:"100" doc:"Base variables whose literal values previews copy, as web.KEY or shared.KEY"`
	AllowForks    bool     `json:"allow_forks,omitempty" doc:"Also preview pull requests from forks"`
}

// PreviewStatus sums up a preview's services.
type PreviewStatus string

const (
	// PreviewWaiting: nothing deployed yet.
	PreviewWaiting   PreviewStatus = "waiting"
	PreviewDeploying PreviewStatus = "deploying"
	PreviewLive      PreviewStatus = "live"
	PreviewFailed    PreviewStatus = "failed"
	PreviewClosed    PreviewStatus = "closed"
)

// Preview is one pull request's preview environment.
type Preview struct {
	Environment string `json:"environment"`
	Repo        string `json:"repo" doc:"owner/name"`
	Number      int64  `json:"number" doc:"Pull request number"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	PullURL     string `json:"pull_url" doc:"The pull request on GitHub"`
	Branch      string `json:"branch" doc:"The pull request's head branch"`
	Commit      string `json:"commit" doc:"The head commit previewed"`
	Fork        bool   `json:"fork"`
	// Status is closed once the pull request closed; otherwise it sums up
	// its services' newest releases: failed if any failed, deploying if
	// any is, live once all are, waiting before any deployed.
	Status PreviewStatus `json:"status" enum:"waiting,deploying,live,failed,closed"`
	// URLs are the preview's addresses, one per service with a domain.
	URLs []PreviewURL `json:"urls"`
	// Event says what happened last, e.g. why a comment couldn't be posted.
	Event     string     `json:"event,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// PreviewURL is a preview service's address.
type PreviewURL struct {
	Service string `json:"service"`
	URL     string `json:"url"`
}

// PullRequestRef is the pull request a preview environment belongs to.
type PullRequestRef struct {
	Repo   string `json:"repo" doc:"owner/name"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url" doc:"The pull request on GitHub"`
}
