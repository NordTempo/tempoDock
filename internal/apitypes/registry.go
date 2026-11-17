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
	// Storage is where the registry keeps its images: "local" (the control
	// plane's disk) or a storage connection's name (ADR 0034).
	Storage string `json:"storage,omitempty" example:"local"`
	// StoredBytes is the size of the registry's blobs; 0 when it can't be
	// read.
	StoredBytes int64 `json:"stored_bytes"`
	// Migration is the latest move of the images to other storage since
	// the daemon started.
	Migration *RegistryMigration `json:"migration,omitempty"`
	// KeepReleases is the rollback window: each service's newest releases
	// whose images pruning keeps, with its active release.
	KeepReleases int        `json:"keep_releases"`
	Pruning      bool       `json:"pruning"`
	LastPruneAt  *time.Time `json:"last_prune_at,omitempty"`
	// LastPruned is how many images the last prune deleted.
	LastPruned int `json:"last_pruned"`
	// LastPruneError is why the last prune failed; LastPruneSkipped what
	// the last prune that ran through passed over because it couldn't
	// read or delete it (a repository and tag or digest, and why). The
	// next prune tries those again.
	LastPruneError   string   `json:"last_prune_error,omitempty"`
	LastPruneSkipped []string `json:"last_prune_skipped,omitempty"`
	// CollectPending: images were deleted whose layers aren't collected
	// yet; the next prune collects them.
	CollectPending bool `json:"collect_pending"`
	// Bucket is the registry's bucket as its storage gateway sees it
	// (ADR 0084); absent when the images are on the control plane's disk.
	Bucket   *RegistryBucket   `json:"bucket,omitempty"`
	Services []RegistryService `json:"services"`
}

// RegistryBucket is the health of the registry's bucket, from the calls
// the gateway makes to it anyway, and the spool on the control plane's
// disk where pushes land before they upload (ADR 0084).
type RegistryBucket struct {
	State string    `json:"state" enum:"healthy,degraded,failing"`
	Since time.Time `json:"since"`
	// Calls are the recent calls to the bucket (the last 64, up to 10
	// minutes back), Bad how many failed or took over 10 s, SlowestMS the
	// slowest, LastError the newest failure.
	Calls     int    `json:"calls"`
	Bad       int    `json:"bad"`
	SlowestMS int64  `json:"slowest_ms"`
	LastError string `json:"last_error,omitempty"`
	// Waiting objects are only on this server's disk until they upload.
	Waiting         int        `json:"waiting"`
	WaitingBytes    int64      `json:"waiting_bytes"`
	OldestWaitingAt *time.Time `json:"oldest_waiting_at,omitempty"`
	Uploading       int        `json:"uploading"`
	// SpoolBytes is what the spool holds, of SpoolMaxBytes; the disk it is
	// on has DiskFreeBytes of DiskBytes free, and writes stop when that
	// would fall under DiskMinFreePercent.
	SpoolBytes         int64 `json:"spool_bytes"`
	SpoolMaxBytes      int64 `json:"spool_max_bytes"`
	DiskFreeBytes      int64 `json:"disk_free_bytes"`
	DiskBytes          int64 `json:"disk_bytes"`
	DiskMinFreePercent int   `json:"disk_min_free_percent"`
	// Refusing is why pushes are refused: the spool is full, or the disk
	// is nearly full. Empty when they are accepted.
	Refusing      string     `json:"refusing,omitempty" enum:"spool,disk"`
	RefusingSince *time.Time `json:"refusing_since,omitempty"`
	// Stuck are blobs the bucket keeps timing out on; the next push of an
	// image with one uploads it again.
	Stuck []RegistryStuckBlob `json:"stuck"`
}

// RegistryStuckBlob is a blob the bucket keeps timing out on.
type RegistryStuckBlob struct {
	Digest string    `json:"digest" example:"sha256:e3a802d3..."`
	Since  time.Time `json:"since"`
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

// RegistryMigration is a move of the registry's images from one storage to
// another (`dock registry migrate`). The registry serves pulls read-only
// while it runs; builds wait for it.
type RegistryMigration struct {
	// From and To are "local" or storage connection names.
	From    string `json:"from"`
	To      string `json:"to"`
	Running bool   `json:"running"`
	// Objects and Bytes are what was copied, once it finished.
	Objects    int        `json:"objects"`
	Bytes      int64      `json:"bytes"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// MigrateRegistryRequest moves the registry's images.
type MigrateRegistryRequest struct {
	To string `json:"to" enum:"local,s3" doc:"local: the control plane's disk; s3: a storage connection"`
	// Storage names the storage connection for to s3; it may be left out
	// when there is only one.
	Storage string `json:"storage,omitempty"`
}

// RegistryCredential is how the organization signs in to a private image
// registry when a deploy pulls an image from it (ADR 0051). The password
// or token is never returned.
type RegistryCredential struct {
	// Host is the registry's host (and port), lower case; Docker Hub is
	// docker.io.
	Host      string    `json:"host" example:"ghcr.io"`
	Username  string    `json:"username" example:"acme-bot"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// SetRegistryCredentialRequest adds or replaces the credential for a
// registry host.
type SetRegistryCredentialRequest struct {
	Username string `json:"username" minLength:"1" maxLength:"256"`
	// Password is the password or access token. It is stored sealed and
	// never shown again.
	Password string `json:"password" minLength:"1" maxLength:"16384"`
}
