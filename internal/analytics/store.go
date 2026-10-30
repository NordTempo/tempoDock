package analytics

import "context"

// Store is the analytics store as Dock uses it: Client over HTTP to the
// dock-analytics container, or a fake in core's tests. Every method may
// fail with the store offline (its server down, or its container
// starting); callers say so instead of showing empty numbers.
type Store interface {
	// Write appends events from source (a spool) whose events up to
	// position are now all written. A batch at or before the source's
	// cursor was written before and is skipped; written says how many
	// events were new.
	Write(ctx context.Context, source string, position uint64, events []Event) (written int, err error)
	// Cursor is source's position: how far its events are written.
	Cursor(ctx context.Context, source string) (uint64, error)
	Overview(ctx context.Context, q Query) (Overview, error)
	Series(ctx context.Context, q SeriesQuery) ([]Point, error)
	Breakdown(ctx context.Context, q BreakdownQuery) ([]Row, error)
	// Health says whether the store answers, and its size.
	Health(ctx context.Context) (Health, error)
}

// Health is the store's state.
type Health struct {
	Version   string `json:"version"`
	Events    int64  `json:"events"`
	FileBytes int64  `json:"file_bytes"`
}

// WriteResult answers a write.
type WriteResult struct {
	Written int `json:"written"`
	// Skipped counts lines that weren't events.
	Skipped int `json:"skipped,omitempty"`
}

// CursorResult answers a cursor request.
type CursorResult struct {
	Position uint64 `json:"position"`
}

// HTTP API paths of the dock-analytics container. Every request but
// HealthPath carries Authorization: Bearer <the install's token>.
const (
	// HealthPath answers GET with Health.
	HealthPath = "/v1/health"
	// WritePath takes POST ?source=&position= with a body of events as
	// JSON lines, and answers WriteResult. Lines that aren't events are
	// skipped and counted, so one bad line can't hold a spool up.
	WritePath = "/v1/write"
	// CursorPath answers GET ?source= with CursorResult.
	CursorPath = "/v1/cursor"
	// OverviewPath, SeriesPath and BreakdownPath take POST with a
	// Query, SeriesQuery or BreakdownQuery and answer Overview, []Point
	// or []Row.
	OverviewPath  = "/v1/overview"
	SeriesPath    = "/v1/series"
	BreakdownPath = "/v1/breakdown"
)

// MaxWriteBatch caps the events of one write, and MaxEventBytes one
// event's JSON line.
const (
	MaxWriteBatch = 10_000
	MaxEventBytes = 16 << 10
)
