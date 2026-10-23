package apitypes

import "time"

// Registry is the platform registry (ADR 0014 section 4) as the console
// shows it: where it is, what it stores, how pruning went, and which
// services keep images in it. Prune state is since the daemon started.
type Registry struct {
	// Enabled is false on installs without a registry, where images stay
	// on the server that built them; the rest is then empty.
	Enabled bool   `json:"enabled"`
	Host    string `json:"host,omitempty" example:"127.0.0.1:5000"`
	// StoredBytes is the size of the registry's blobs on disk; 0 when it
	// can't be read.
	StoredBytes int64 `json:"stored_bytes"`
	// KeepReleases is the rollback window: each service's newest releases
	// whose images pruning keeps, with its active release.
	KeepReleases int        `json:"keep_releases"`
	Pruning      bool       `json:"pruning"`
	LastPruneAt  *time.Time `json:"last_prune_at,omitempty"`
	// LastPruned is how many images the last prune deleted.
	LastPruned     int    `json:"last_pruned"`
	LastPruneError string `json:"last_prune_error,omitempty"`
	// CollectPending: images were deleted whose layers aren't collected
	// yet; the next prune collects them.
	CollectPending bool              `json:"collect_pending"`
	Services       []RegistryService `json:"services"`
}

// RegistryService is one service that pushed images to the registry.
type RegistryService struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	// Repository is the service's repository, without the registry host.
	Repository string `json:"repository" example:"dock/p1_e2_s3"`
	// Archived services keep no images; the next prune deletes them.
	Archived bool `json:"archived"`
	// Kept is how many distinct images pruning keeps for it.
	Kept int `json:"kept"`
	// Newest is its newest release with an image in the registry.
	Newest int64 `json:"newest"`
	// Active is its active release, 0 when none is.
	Active int64 `json:"active"`
}
