package apitypes

// AnalyticsStatus is Dock Analytics' state (ADR 0037), for operators:
// which server runs the store and whether it answers.
type AnalyticsStatus struct {
	// Enabled is set once analytics was turned on: the store runs and
	// every server's events are forwarded to it.
	Enabled bool `json:"enabled"`
	// Server runs the store: the control plane's own server by default.
	Server       string `json:"server"`
	ServerOnline bool   `json:"server_online"`
	// StoreRunning says the store answered just now; StoreVersion,
	// Events and StoreBytes are what it said.
	StoreRunning bool   `json:"store_running"`
	StoreVersion string `json:"store_version,omitempty"`
	Events       int64  `json:"events"`
	StoreBytes   int64  `json:"store_bytes"`
}

// SetAnalyticsServerRequest picks the analytics server.
type SetAnalyticsServerRequest struct {
	// Server is a server's name; empty is the control plane's server.
	Server string `json:"server" doc:"The server the analytics store runs on; empty for the control plane's own"`
}
