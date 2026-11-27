package apitypes

import "time"

// DeployQueue is what deploys wait for and run (ADR 0086, amended
// 2026-11-27): each builder's build and the releases waiting behind it
// in order, then the deploys not at a builder. People see their own
// releases; another org's, or one they can't read, is Hidden, so its
// place still shows.
type DeployQueue struct {
	Builders []QueueBuilder `json:"builders"`
	// Deploying are the releases in flight that aren't at a builder:
	// queued behind a promotion step or a server move, pulling an image,
	// waiting for the registry, starting or health checking.
	Deploying []QueueItem `json:"deploying"`
}

// QueueBuilder is one builder's slot and line.
type QueueBuilder struct {
	// Label names it for everyone ("Builder 1", in the order servers
	// joined); Name is the server's, for operators only.
	Label string `json:"label"`
	Name  string `json:"name,omitempty" doc:"The server's name; operators only"`
	// Builds is which environments' images it builds.
	Builds   BuildKinds  `json:"builds" enum:"all,production,non_production,none"`
	Building *QueueItem  `json:"building,omitempty"`
	Waiting  []QueueItem `json:"waiting"`
}

// BuildKinds is which environment kinds a builder builds.
type BuildKinds string

// Build kinds of a builder.
const (
	BuildsAll           BuildKinds = "all"
	BuildsProduction    BuildKinds = "production"
	BuildsNonProduction BuildKinds = "non_production"
	BuildsNone          BuildKinds = "none"
)

// QueueItem is one release in the queue. A Hidden one has only its
// times.
type QueueItem struct {
	Hidden bool `json:"hidden,omitempty" doc:"Another org's release, or one you can't read: only its place and times"`
	// Org and ID, in the console's queue only: the release's org, and
	// the ID an operator cancels it by.
	Org         string        `json:"org,omitempty"`
	ID          int64         `json:"id,omitempty"`
	Project     string        `json:"project,omitempty"`
	Environment string        `json:"environment,omitempty"`
	Service     string        `json:"service,omitempty"`
	Release     int64         `json:"release,omitempty" doc:"The release's number in its service"`
	Status      ReleaseStatus `json:"status,omitempty" enum:"queued,building,starting,health_checking"`
	Source      ReleaseSource `json:"source,omitempty" enum:"upload,image,config,rollback,restart,promote"`
	// FromRelease is the release a rollback, restart or promotion copied.
	FromRelease int64   `json:"from_release,omitempty"`
	CreatedBy   string  `json:"created_by,omitempty" doc:"Who or what started it: a person, a token, git push, a preview"`
	Commit      *Commit `json:"commit,omitempty"`
	// Next is a pushed commit waiting for this deploy to end; a newer
	// push replaces it (ADR 0018).
	Next *Commit `json:"next,omitempty"`
	// QueuedAt is when the release was made; Since when it got where it
	// is: into the builder's line, into its slot.
	QueuedAt time.Time `json:"queued_at"`
	Since    time.Time `json:"since"`
}
