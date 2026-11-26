package apitypes

// FeatureKey names an optional part of Dock that an operator turns on and
// off on the Features page (ADR 0101).
type FeatureKey string

const (
	// FeatureScanning is image vulnerability scanning (ADR 0074).
	FeatureScanning FeatureKey = "scanning"
	// FeatureAnalytics is Dock Analytics (ADR 0037).
	FeatureAnalytics FeatureKey = "analytics"
	// FeatureTracing is tracing over OTLP (ADR 0075).
	FeatureTracing FeatureKey = "tracing"
)

// Features are the optional features with what each costs now.
type Features struct {
	Items []Feature `json:"items"`
}

// Feature is one optional feature: whether it is on, where it runs and
// what it takes. Memory and disk come from the last samples Dock took
// (memory each minute, a server's disk when someone last read it); nil
// when there is none.
type Feature struct {
	Key     FeatureKey `json:"key" enum:"scanning,analytics,tracing"`
	Enabled bool       `json:"enabled"`
	// Servers are the servers it runs on (the builders for scanning, the
	// analytics server for the stores).
	Servers []string `json:"servers"`
	// MemoryBytes is what its containers use now; MemoryLimitBytes the
	// most they may take (scanning only runs jobs, up to this).
	MemoryBytes      *int64 `json:"memory_bytes,omitempty"`
	MemoryLimitBytes int64  `json:"memory_limit_bytes"`
	// DiskBytes is its data on disk; it stays when turned off unless
	// removed with it.
	DiskBytes *int64 `json:"disk_bytes,omitempty"`
}

// AnalyticsSwitch turns Dock Analytics on or off (ADR 0101).
type AnalyticsSwitch struct {
	Enabled bool `json:"enabled"`
	// RemoveData, turning it off, also removes the store's data: every
	// site's events, replays and errors. Kept, they are there again when
	// it is turned on.
	RemoveData bool `json:"remove_data,omitempty"`
}
