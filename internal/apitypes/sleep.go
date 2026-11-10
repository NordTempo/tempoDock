package apitypes

import "time"

// Sleeping environments (ADR 0070): an environment without
// requests for a while stops its services' containers and wakes on the
// next request to its domains.

// SleepState is where an environment is between running and asleep.
type SleepState string

// Sleep states.
const (
	SleepAwake SleepState = "awake"
	// SleepGoing is set while its containers stop.
	SleepGoing  SleepState = "sleeping"
	SleepAsleep SleepState = "asleep"
	// SleepWaking is set while its containers start and pass their
	// health checks again.
	SleepWaking SleepState = "waking"
)

// EnvironmentSleep is an environment's sleep: its setting and state. It
// is set on environments that may sleep on their own or are asleep.
type EnvironmentSleep struct {
	// AfterMinutes is how long it may go without requests before it
	// sleeps; 0 when it only sleeps when asked.
	AfterMinutes int        `json:"after_minutes" doc:"Minutes without requests before it sleeps; 0 when it sleeps only when asked"`
	State        SleepState `json:"state" enum:"awake,sleeping,asleep,waking"`
	// Since is when it last went to sleep (asleep) or woke (awake).
	Since *time.Time `json:"since,omitempty"`
	// LastRequestAt is the last minute its domains had requests, as the
	// daemon saw since it started.
	LastRequestAt *time.Time `json:"last_request_at,omitempty"`
	// SleepsAt is when it goes to sleep if nothing happens before.
	SleepsAt   *time.Time `json:"sleeps_at,omitempty"`
	FreedBytes int64      `json:"freed_bytes,omitempty" doc:"Memory its containers used when it went to sleep"`
	// SleptBy is who put it to sleep; empty when it went to sleep idle.
	SleptBy string `json:"slept_by,omitempty"`
	// WokeBy is who woke it: a person, "deploy", or the request that
	// did ("GET /checkout/:id").
	WokeBy string `json:"woke_by,omitempty"`
	// Blocked says why it can't sleep on its own, when it can't.
	Blocked string `json:"blocked,omitempty"`
}

// SetSleepRequest changes how long an environment may go without requests
// before it sleeps.
type SetSleepRequest struct {
	AfterMinutes int `json:"after_minutes" minimum:"0" maximum:"1440" doc:"Minutes without requests before it sleeps, 5 to 1440; 0 turns sleeping off"`
}

// SleepAfterDefault is how long a preview may go without requests before
// it sleeps, unless its project says otherwise.
const SleepAfterDefault = 30

// Bounds of a sleep setting, in minutes.
const (
	SleepAfterMin = 5
	SleepAfterMax = 1440
)
