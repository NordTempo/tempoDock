package apitypes

import "time"

// TracingStatus is tracing's state, for operators: whether it is on and
// whether the trace store answers. The trace store keeps spans in a
// container of its own beside the analytics store, on the analytics
// server.
type TracingStatus struct {
	// Enabled is set while tracing is on: the trace store runs.
	Enabled bool `json:"enabled"`
	// Server is the analytics server, where the trace store runs.
	Server       string `json:"server"`
	ServerOnline bool   `json:"server_online"`
	// StoreRunning says the trace store answered just now; StoreVersion
	// and StoreBytes (its database file) are what it said.
	StoreRunning bool   `json:"store_running"`
	StoreVersion string `json:"store_version,omitempty"`
	StoreBytes   int64  `json:"store_bytes"`
	// Starting says the trace store is being started.
	Starting bool `json:"starting,omitempty"`
	// Error is why it last failed to start, and what to do; empty once
	// it starts.
	Error string `json:"error,omitempty"`
	// RetentionDays and MemoryMB are [traces] retention_days and
	// store_memory_mb.
	RetentionDays int `json:"retention_days"`
	MemoryMB      int `json:"memory_mb"`
}

// TracingSettings turns tracing on or off. Off removes the trace store
// and every span it holds.
type TracingSettings struct {
	Enabled bool `json:"enabled"`
}

// TraceRange is how far back a trace list goes, ending now.
type TraceRange string

// Trace list ranges.
const (
	TraceRange1h  TraceRange = "1h"
	TraceRange24h TraceRange = "24h"
	TraceRange7d  TraceRange = "7d"
)

// TraceList is a service's traces over a range: the spans where they
// entered the service, newest or slowest first.
type TraceList struct {
	Range  TraceRange     `json:"range" enum:"1h,24h,7d"`
	Since  time.Time      `json:"since"`
	Traces []TraceSummary `json:"traces"`
	// More says the range has more matching traces than the list holds.
	More bool `json:"more,omitempty"`
}

// TraceSummary is one trace in a list: the span where it entered the
// service. Error says that span failed; Kept why the trace was kept.
type TraceSummary struct {
	Trace      string    `json:"trace" doc:"Trace ID, 32 hex digits"`
	Span       string    `json:"span" doc:"The entry span's ID"`
	Release    int64     `json:"release" doc:"Release number of the service that served it; 0 when unknown"`
	Start      time.Time `json:"start"`
	DurationMS float64   `json:"duration_ms"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind" enum:"unspecified,internal,server,client,producer,consumer,edge"`
	Method     string    `json:"method,omitempty"`
	Route      string    `json:"route,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Error      bool      `json:"error"`
	Kept       string    `json:"kept" enum:"sampled,error,slow,forced"`
}

// Trace is a trace's spans by start time, as far as the caller may see
// them: spans of services outside its token's project or environment are
// hidden (no name, attributes or events).
type Trace struct {
	Trace      string      `json:"trace"`
	Start      time.Time   `json:"start"`
	DurationMS float64     `json:"duration_ms"`
	Spans      []TraceSpan `json:"spans"`
	// Truncated says the trace has more spans than Dock returns (2,000).
	Truncated bool `json:"truncated,omitempty"`
}

// TraceSpan is one span. OffsetMS is its start after the trace's.
// Attribute values and event messages are masked as logs are: sensitive
// names when they were taken in, and the release's secret values for
// readers who can't reveal them.
type TraceSpan struct {
	Span        string         `json:"span"`
	Parent      string         `json:"parent,omitempty"`
	Project     string         `json:"project,omitempty"`
	Environment string         `json:"environment,omitempty"`
	Service     string         `json:"service,omitempty"`
	Release     int64          `json:"release,omitempty"`
	Hidden      bool           `json:"hidden,omitempty" doc:"A span of a service the caller can't see"`
	OffsetMS    float64        `json:"offset_ms"`
	DurationMS  float64        `json:"duration_ms"`
	Name        string         `json:"name,omitempty"`
	Kind        string         `json:"kind" enum:"unspecified,internal,server,client,producer,consumer,edge"`
	Status      string         `json:"status" enum:"unset,ok,error"`
	Method      string         `json:"method,omitempty"`
	Route       string         `json:"route,omitempty"`
	HTTPStatus  int            `json:"http_status,omitempty"`
	Kept        string         `json:"kept" enum:"sampled,error,slow,forced"`
	Attrs       map[string]any `json:"attrs,omitempty"`
	Events      []TraceEvent   `json:"events,omitempty"`
}

// TraceEvent is an event of a span: an exception, or an annotation.
type TraceEvent struct {
	Name  string         `json:"name"`
	AtMS  float64        `json:"at_ms" doc:"After the span's start"`
	Attrs map[string]any `json:"attrs,omitempty"`
}
