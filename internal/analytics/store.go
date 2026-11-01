package analytics

import (
	"context"
	"io"
)

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
	Live(ctx context.Context, q LiveQuery) (Live, error)
	// Health says whether the store answers, and its size.
	Health(ctx context.Context) (Health, error)
	// Prune deletes a site's raw events before Before (Unix ms); its daily
	// rollups stay.
	Prune(ctx context.Context, q PruneQuery) (PruneResult, error)
	// UpdateGeoIP has the store fetch this month's location database
	// unless it has it, and answers the month it has.
	UpdateGeoIP(ctx context.Context) (GeoIP, error)
	// Errors, Error and Vitals read errors and Web Vitals (phase A2).
	Errors(ctx context.Context, q ErrorsQuery) ([]ErrorGroup, error)
	Error(ctx context.Context, q ErrorQuery) (ErrorDetail, error)
	Vitals(ctx context.Context, q VitalsQuery) (VitalsResult, error)
	// Traffic counts every site's page views in an hour and the same hour
	// of the days before (the traffic drop alert).
	Traffic(ctx context.Context, q TrafficQuery) ([]TrafficRow, error)
	// Replays, Replay and ReplayData read session replays (phase A3);
	// PruneReplays deletes old ones.
	Replays(ctx context.Context, q ReplaysQuery) ([]Replay, error)
	Replay(ctx context.Context, q ReplayQuery) (ReplayDetail, error)
	ReplayData(ctx context.Context, site int64, segments []string, gzipped bool) (io.ReadCloser, bool, error)
	PruneReplays(ctx context.Context, q ReplayPrune) (PruneResult, error)
}

// PruneQuery names the raw events Prune deletes: a site's events and
// Web Vitals before Before, and its errors before ErrorsBefore (none if
// 0). Error groups not seen since go with their errors.
type PruneQuery struct {
	Site         int64 `json:"site"`
	Before       int64 `json:"before"`
	ErrorsBefore int64 `json:"errors_before,omitempty"`
}

// PruneResult says how many events and errors went.
type PruneResult struct {
	Deleted       int64 `json:"deleted"`
	ErrorsDeleted int64 `json:"errors_deleted,omitempty"`
}

// GeoIP is the store's location database.
type GeoIP struct {
	// Month is the database's month, "2026-10"; empty for none yet.
	Month string `json:"month"`
}

// Health is the store's state.
type Health struct {
	Version   string `json:"version"`
	Events    int64  `json:"events"`
	FileBytes int64  `json:"file_bytes"`
	// GeoIP is the location database's month, "" for none yet.
	GeoIP string `json:"geoip,omitempty"`
}

// WriteResult answers a write.
type WriteResult struct {
	Written int `json:"written"`
	// Skipped counts lines that weren't events.
	Skipped int `json:"skipped,omitempty"`
	// NewErrors are the error groups the write saw for the first time:
	// what the "new error" alert is raised for.
	NewErrors []NewError `json:"new_errors,omitempty"`
}

// NewError is an error group's first error.
type NewError struct {
	Site    int64  `json:"site"`
	Group   string `json:"group"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    string `json:"path"`
	Release int64  `json:"release"`
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
	// LivePath takes POST with a LiveQuery and answers Live.
	LivePath = "/v1/live"
	// ErrorsPath, ErrorPath and VitalsPath take POST with an
	// ErrorsQuery, ErrorQuery or VitalsQuery and answer []ErrorGroup,
	// ErrorDetail or VitalsResult.
	ErrorsPath = "/v1/errors"
	ErrorPath  = "/v1/error"
	VitalsPath = "/v1/vitals"
	// TrafficPath takes POST with a TrafficQuery and answers
	// []TrafficRow.
	TrafficPath = "/v1/traffic"
	// PrunePath takes POST with a PruneQuery and answers PruneResult.
	PrunePath = "/v1/prune"
	// GeoIPPath takes POST and answers GeoIP once the store has this
	// month's location database (it may download it first).
	GeoIPPath = "/v1/geoip"
	// SnapshotPath answers GET with the store's database file, with its
	// Content-Length and SnapshotEventsHeader; writes wait until it is
	// sent. A move to another server copies it (ADR 0037).
	SnapshotPath = "/v1/snapshot"
	// SnapshotEventsHeader is the events the snapshot holds.
	SnapshotEventsHeader = "Dock-Analytics-Events"
	// RestorePath takes PUT with another store's snapshot and answers
	// Restored once it is on disk; the store then exits for its
	// container to restart on it. Only an empty store takes one.
	RestorePath = "/v1/restore"
)

// Restored is what a store received for RestorePath.
type Restored struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// MaxWriteBatch caps the events of one write, and MaxEventBytes one
// event's JSON line.
const (
	MaxWriteBatch = 10_000
	MaxEventBytes = 16 << 10
)
