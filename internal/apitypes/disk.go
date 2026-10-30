package apitypes

import "time"

// DiskGroup sorts what uses a server's disk, for the console (ADR 0035).
type DiskGroup string

const (
	// DiskApps is services' data: their volumes and containers' layers.
	DiskApps DiskGroup = "apps"
	// DiskImages are images kept: running, for rollbacks, shared layers,
	// and images Dock didn't make.
	DiskImages DiskGroup = "images"
	// DiskReclaimable are images a prune removes.
	DiskReclaimable DiskGroup = "reclaimable"
	// DiskBuild is BuildKit's cache on a builder.
	DiskBuild DiskGroup = "build"
	// DiskDock is Dock's own: its data dir, local backups, the registry.
	DiskDock DiskGroup = "dock"
	// DiskOther is the rest of the filesystem: volumes Dock didn't make,
	// logs, the system.
	DiskOther DiskGroup = "other"
)

// ServerDisk is where a server's disk goes, read when asked (Docker sizes
// every image and volume, which takes seconds).
type ServerDisk struct {
	Server string `json:"server"`
	// UsedBytes and TotalBytes are the filesystem of Docker's data root.
	UsedBytes  int64 `json:"used_bytes"`
	TotalBytes int64 `json:"total_bytes"`
	// Parts add up to UsedBytes, biggest first within each group.
	Parts []DiskPart `json:"parts"`
	// ReclaimableBytes is what `dock server prune` frees: images no
	// release needs, and build cache above what is kept.
	ReclaimableBytes int64     `json:"reclaimable_bytes"`
	ReadAt           time.Time `json:"read_at"`
	// Refreshing: this is the last reading, and a new one is being read;
	// it arrives as a disk event.
	Refreshing bool `json:"refreshing,omitempty"`
	// Error says why reading it again failed; the reading is the last
	// one that worked.
	Error string `json:"error,omitempty"`
}

// DiskPart is one user of a server's disk.
type DiskPart struct {
	Group DiskGroup `json:"group" enum:"apps,images,reclaimable,build,dock,other"`
	// Name is a service (project/environment/service) or what the part is.
	Name string `json:"name" example:"shop/production/db"`
	// Detail says more: a volume's name, how many images.
	Detail string `json:"detail,omitempty"`
	Bytes  int64  `json:"bytes"`
	// Reclaimable is what a prune frees of it.
	Reclaimable int64 `json:"reclaimable"`
}

// ServerPruneResult is what `dock server prune` removed.
type ServerPruneResult struct {
	Server        string `json:"server"`
	ImagesRemoved int    `json:"images_removed"`
	ImageBytes    int64  `json:"image_bytes"`
	CacheBytes    int64  `json:"cache_bytes"`
	// Error says why part of the prune failed; what was freed is above.
	Error string `json:"error,omitempty"`
}
