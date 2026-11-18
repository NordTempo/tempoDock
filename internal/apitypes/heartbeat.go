package apitypes

import "time"

// Heartbeat is the outside heartbeat (ADR 0097): the URL the control
// plane sends a GET to every few minutes, so a monitor outside Dock
// raises the alarm when the pings stop. The URL is a secret and never
// returned; Target is its scheme and host.
type Heartbeat struct {
	Target      string     `json:"target" doc:"The URL's scheme and host; the rest of it is a secret and never returned"`
	Minutes     int        `json:"minutes" doc:"How often a ping is sent, in minutes"`
	LastSentAt  *time.Time `json:"last_sent_at,omitempty" doc:"When a ping last went through, since the control plane started"`
	LastError   string     `json:"last_error,omitempty" doc:"Why the last ping failed; empty when it went through"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	Failures    int        `json:"failures" doc:"Pings that failed in a row; an alert opens at three"`
}

// SetHeartbeatRequest sets or replaces the heartbeat.
type SetHeartbeatRequest struct {
	URL     string `json:"url" maxLength:"2048" doc:"An http:// or https:// URL to send a GET to. Stored encrypted, never returned"`
	Minutes int    `json:"minutes,omitempty" minimum:"0" maximum:"60" doc:"How often, in minutes (1 to 60). Default 5"`
}

// HeartbeatTest is the outcome of sending one ping now.
type HeartbeatTest struct {
	Heartbeat Heartbeat `json:"heartbeat"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
}
