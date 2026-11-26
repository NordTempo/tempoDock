package apitypes

import "time"

// Preview environments per pull request (ADR 0056): pull requests into a
// branch a service with previews on deploys (ADR 0077) get an environment
// cloned from that service's environment, the base, as pr-<number>; it is
// archived when the pull request closes.

// PreviewSettings is a project's previews (`dock env previews`): which
// services build pull requests, the numbers of each base environment and
// the open previews. The single-base fields describe the first base, for
// clients that predate ADR 0077.
type PreviewSettings struct {
	// Enabled is set when some service builds pull requests.
	Enabled bool `json:"enabled"`
	// Services are the project's services with preview settings.
	Services []PreviewService `json:"services"`
	// Environments are the bases: environments with a service that
	// builds pull requests, with their numbers.
	Environments []PreviewEnvironment `json:"environments"`
	Previews     []Preview            `json:"previews" doc:"Open previews, newest first"`

	// Base is the first base environment ("" when off).
	Base        string `json:"base,omitempty" doc:"The first environment previews are cloned from"`
	MaxPreviews int    `json:"max_previews" doc:"The first base's most open previews at once"`
	// CopyVariables are the first base's copied variables, as web.KEY
	// or shared.KEY.
	CopyVariables     []string   `json:"copy_variables" doc:"The first base's variables whose literal values previews copy, as web.KEY or shared.KEY"`
	AllowForks        bool       `json:"allow_forks" doc:"Some service of the first base also builds pull requests from forks"`
	SeedFromSnapshot  []string   `json:"seed_from_snapshot" doc:"The first base's services whose volumes a new preview fills from their latest snapshot"`
	SleepAfterMinutes int        `json:"sleep_after_minutes" doc:"The first base's minutes without requests before a preview sleeps; 0 when previews never sleep"`
	UpdatedBy         string     `json:"updated_by,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

// PreviewService is one service's preview settings, with where it is and
// what it deploys from.
type PreviewService struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	// Repo, Branch and Dir are its GitHub source; empty without one.
	Repo   string `json:"repo,omitempty" doc:"owner/name"`
	Branch string `json:"branch,omitempty"`
	Dir    string `json:"dir,omitempty"`
	ServicePreviews
}

// PreviewEnvironment is a base environment's preview numbers.
type PreviewEnvironment struct {
	Environment string `json:"environment"`
	EnvironmentPreviews
}

// ServicePreviews is what a service does in previews (ADR 0077).
type ServicePreviews struct {
	// Build: pull requests into the service's branch build it in a
	// preview environment. Needs a GitHub source.
	Build      bool `json:"build" doc:"Pull requests into the service's branch get a preview that builds them"`
	AllowForks bool `json:"allow_forks" doc:"Also pull requests from forks (anyone on GitHub)"`
	// CopyVariables are the service's own variables whose literal values
	// previews copy; no other literal value is ever copied.
	CopyVariables []string `json:"copy_variables" doc:"The service's variables whose literal values previews copy, by key"`
	// Data is what the service's volumes start with in a new preview
	// (ADR 0092).
	Data PreviewData `json:"data" enum:"empty,snapshot,copy,masked" doc:"What the service's volumes start with in a new preview: empty, its newest snapshot, a copy of this database, or that copy with its sensitive columns masked"`
	// DataForks gives pull requests from forks the data too; without it
	// their previews start empty.
	DataForks bool `json:"data_forks" doc:"Pull requests from forks get the data too; otherwise their previews start empty"`
	// Masked counts the sensitive columns a masked copy replaces, as the
	// Data panel marks them; nil for a service that isn't a database.
	Masked *PreviewMasked `json:"masked,omitempty"`
	// CopyMaxMB is the largest database previews copy ([previews]
	// copy_max_mb).
	CopyMaxMB int        `json:"copy_max_mb,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// PreviewData is what a service's volumes start with in a new preview
// (ADR 0092).
type PreviewData string

const (
	// PreviewDataEmpty: new, empty volumes (the default).
	PreviewDataEmpty PreviewData = "empty"
	// PreviewDataSnapshot: the service's newest volume snapshot (ADR
	// 0047), any service with a volume.
	PreviewDataSnapshot PreviewData = "snapshot"
	// PreviewDataCopy: a copy of the base's live database, sensitive
	// columns and all. Template databases only.
	PreviewDataCopy PreviewData = "copy"
	// PreviewDataMasked: that copy with the Data panel's sensitive
	// columns replaced while it is read.
	PreviewDataMasked PreviewData = "masked"
)

// PreviewMasked counts a database's sensitive marks.
type PreviewMasked struct {
	Columns int `json:"columns" doc:"Sensitive columns marked in the Data panel"`
	Tables  int `json:"tables" doc:"Tables they are in"`
	// Keys are a key-value database's sensitive key patterns, which a
	// masked copy can't leave out yet: it starts empty while there are
	// any.
	Keys int `json:"keys,omitempty" doc:"Sensitive key patterns (Redis); a masked copy starts empty while there are any"`
}

// PreviewDataStatus is how a preview database's copy went.
type PreviewDataStatus string

const (
	PreviewDataCopying PreviewDataStatus = "copying"
	PreviewDataCopied  PreviewDataStatus = "copied"
	// PreviewDataStartedEmpty: no data was copied; Message says why.
	PreviewDataStartedEmpty PreviewDataStatus = "empty"
)

// PreviewDatabase is one preview database's copy of its base.
type PreviewDatabase struct {
	Service string `json:"service"`
	// From is the base service copied, as environment/service.
	From    string            `json:"from"`
	Mode    PreviewData       `json:"mode" enum:"empty,snapshot,copy,masked"`
	Status  PreviewDataStatus `json:"status" enum:"copying,copied,empty"`
	Message string            `json:"message,omitempty" doc:"The step a copy is at, or why it started empty"`
	Bytes   int64             `json:"bytes,omitempty" doc:"The base database's size when copied"`
	// MaskedTables and MaskedColumns count what a masked copy replaced.
	MaskedTables  int        `json:"masked_tables,omitempty"`
	MaskedColumns int        `json:"masked_columns,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// RefreshPreviewDataRequest refreshes an open preview's databases from
// their base.
type RefreshPreviewDataRequest struct {
	// Services limits it to these preview services; empty: every one
	// whose base service copies its data.
	Services []string `json:"services,omitempty" maxItems:"50" doc:"The preview's databases to refresh; empty: every one that copies its base"`
}

// SetServicePreviewsRequest sets what a service does in previews; every
// field is replaced.
type SetServicePreviewsRequest struct {
	Build         bool     `json:"build,omitempty" doc:"Pull requests into the service's branch get a preview that builds them"`
	AllowForks    bool     `json:"allow_forks,omitempty" doc:"Also pull requests from forks"`
	CopyVariables []string `json:"copy_variables,omitempty" maxItems:"100" doc:"The service's variables whose literal values previews copy, by key"`
	// Data is empty for empty.
	Data      PreviewData `json:"data,omitempty" enum:"empty,snapshot,copy,masked" doc:"What the service's volumes start with in a new preview (default empty); copy and masked are for template databases"`
	DataForks bool        `json:"data_forks,omitempty" doc:"Pull requests from forks get the data too"`
}

// EnvironmentPreviews are the numbers of previews cloned from an
// environment, and its shared variables they copy (ADR 0077).
type EnvironmentPreviews struct {
	MaxPreviews int `json:"max_previews" doc:"Most open previews at once; a pull request beyond it gets no preview until one closes"`
	// SleepAfterMinutes is how long a preview may go without requests
	// before it sleeps; 0 when previews never sleep.
	SleepAfterMinutes int        `json:"sleep_after_minutes" doc:"Minutes without requests before a preview sleeps; 0 when previews never sleep"`
	CopyShared        []string   `json:"copy_shared" doc:"Shared variables whose literal values previews copy, by key"`
	UpdatedBy         string     `json:"updated_by,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

// SetEnvironmentPreviewsRequest sets the numbers of previews cloned from
// an environment; every field is replaced.
type SetEnvironmentPreviewsRequest struct {
	MaxPreviews int `json:"max_previews,omitempty" minimum:"0" maximum:"20" doc:"Most open previews at once (default 3)"`
	// SleepAfterMinutes is 0 for the default (30), -1 for never.
	SleepAfterMinutes int      `json:"sleep_after_minutes,omitempty" minimum:"-1" maximum:"1440" doc:"Minutes without requests before a preview sleeps, 5 to 1440 (default 30); -1 never"`
	CopyShared        []string `json:"copy_shared,omitempty" maxItems:"100" doc:"Shared variables whose literal values previews copy, by key"`
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
	// Base is the environment it was cloned from.
	Base    string `json:"base" doc:"The environment the preview was cloned from"`
	Repo    string `json:"repo" doc:"owner/name"`
	Number  int64  `json:"number" doc:"Pull request number"`
	Title   string `json:"title"`
	Author  string `json:"author"`
	PullURL string `json:"pull_url" doc:"The pull request on GitHub: https://github.com/<repo>/pull/<number>"`
	Branch  string `json:"branch" doc:"The pull request's head branch"`
	Commit  string `json:"commit" doc:"The head commit previewed"`
	Fork    bool   `json:"fork"`
	// Status is closed once the pull request closed; otherwise it sums up
	// its services' newest releases: failed if any failed, deploying if
	// any is, live once all are, waiting before any deployed.
	Status PreviewStatus `json:"status" enum:"waiting,deploying,live,failed,closed"`
	// URLs are the preview's addresses, one per service with a domain.
	URLs []PreviewURL `json:"urls"`
	// Event says what happened last, e.g. why a comment couldn't be posted.
	Event string `json:"event,omitempty"`
	// Databases are its services that copied their base's data (ADR
	// 0092).
	Databases []PreviewDatabase `json:"databases"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	ClosedAt  *time.Time        `json:"closed_at,omitempty"`
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
	URL    string `json:"url" doc:"The pull request on GitHub: https://github.com/<repo>/pull/<number>"`
}
