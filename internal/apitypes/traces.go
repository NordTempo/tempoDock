package apitypes

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
