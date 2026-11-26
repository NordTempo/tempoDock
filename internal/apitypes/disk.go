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
	// ReclaimableBytes is what `dock server prune` frees by default: the
	// safe prunes (PruneTarget.Safe) together.
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
	// Prunes are what each prune would free, safe ones first.
	Prunes []DiskPrune `json:"prunes"`
	ReadAt time.Time   `json:"read_at"`
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

// PruneTarget is one thing a prune frees on a server (ADR 0035
// amendment 2). The first three are safe: nothing later costs more for
// them, and a prune without targets, or Dock's own on a full disk, runs
// them. The rest each cost something later.
type PruneTarget string

const (
	// PruneImages are images nothing needs: of archived services, out of
	// a rollback window, and other versions of Dock's own images.
	PruneImages PruneTarget = "images"
	// PruneScanner is what cut-off downloads left in the vulnerability
	// database's volume.
	PruneScanner PruneTarget = "scanner"
	// PruneCache is BuildKit's cache above [retention] build_cache_keep_mb.
	PruneCache PruneTarget = "cache"
	// PruneRollbacks are rollback images the registry has too, other
	// than the active release's and the one before: rolling back further
	// pulls them again.
	PruneRollbacks PruneTarget = "rollbacks"
	// PruneAllCache is all of BuildKit's cache: the next builds start cold.
	PruneAllCache PruneTarget = "all-cache"
	// PruneScanDB is the whole vulnerability database: the next scan
	// downloads it again (about 200 MB, 3.4 GB on disk), or with [scan]
	// offline an operator imports one (`dock scanning import`).
	PruneScanDB PruneTarget = "scan-db"
)

// PruneTargets are the targets in the order they are shown and run.
var PruneTargets = []PruneTarget{PruneImages, PruneScanner, PruneCache, PruneRollbacks, PruneAllCache, PruneScanDB}

// Safe reports whether a prune without targets runs t.
func (t PruneTarget) Safe() bool {
	return t == PruneImages || t == PruneScanner || t == PruneCache
}

// DiskPrune is what one prune would free on a server.
type DiskPrune struct {
	Target PruneTarget `json:"target" enum:"images,scanner,cache,rollbacks,all-cache,scan-db"`
	Bytes  int64       `json:"bytes"`
	// Safe: a prune without targets runs it (PruneTarget.Safe).
	Safe bool `json:"safe"`
}

// ServerPruneRequest says what `dock server prune` frees; no targets is
// the safe ones.
type ServerPruneRequest struct {
	Targets []PruneTarget `json:"targets,omitempty" enum:"images,scanner,cache,rollbacks,all-cache,scan-db"`
}

// ServerPruneResult is what `dock server prune` removed.
type ServerPruneResult struct {
	Server string `json:"server"`
	// Freed is what each target freed, in the order they ran.
	Freed []PruneFreed `json:"freed"`
	// ImagesRemoved and ImageBytes are images (images and rollbacks),
	// CacheBytes build cache (cache and all-cache), as before targets.
	ImagesRemoved int   `json:"images_removed"`
	ImageBytes    int64 `json:"image_bytes"`
	CacheBytes    int64 `json:"cache_bytes"`
	// Error says why part of the prune failed; what was freed is above.
	Error string `json:"error,omitempty"`
}

// PruneFreed is what one target of a prune freed.
type PruneFreed struct {
	Target  PruneTarget `json:"target" enum:"images,scanner,cache,rollbacks,all-cache,scan-db"`
	Removed int         `json:"removed"`
	Bytes   int64       `json:"bytes"`
}
