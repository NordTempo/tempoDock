package analytics

// Goals and funnels (ADR 0037, phase A4). A goal is a page (a path, *
// matching any run of characters) or a custom event (its name); a funnel
// is two to MaxFunnelSteps goals that a session reaches in order. Dock
// keeps their definitions; the store counts them over a query's events.

// GoalKind is what a goal matches.
type GoalKind string

const (
	GoalPage  GoalKind = "page"
	GoalEvent GoalKind = "event"
)

// GoalSpec is what a goal or a funnel step matches.
type GoalSpec struct {
	Kind GoalKind `json:"kind"`
	// Match is a path pattern for pages, an event's name for events.
	Match string `json:"match"`
}

// Limits on goals and funnels.
const (
	MaxGoals       = 20
	MaxFunnelSteps = 8
)

// GoalsQuery counts each goal's conversions over a query's events.
type GoalsQuery struct {
	Query
	Goals []GoalSpec `json:"goals"`
}

// GoalResult is one goal's conversions: the visitors and sessions that
// reached it (HyperLogLog, about 2% off) and how often it happened.
type GoalResult struct {
	Visitors int64 `json:"visitors"`
	Sessions int64 `json:"sessions"`
	Count    int64 `json:"count"`
}

// FunnelQuery counts the sessions that reach a funnel's steps in order.
type FunnelQuery struct {
	Query
	Steps []GoalSpec `json:"steps"`
}

// FunnelStep is how many sessions, and of how many visitors, reached a
// step after the ones before it.
type FunnelStep struct {
	Sessions int64 `json:"sessions"`
	Visitors int64 `json:"visitors"`
}

// Goal paths of the store's API: POST with a GoalsQuery or FunnelQuery,
// answering []GoalResult or []FunnelStep.
const (
	GoalsPath  = "/v1/goals"
	FunnelPath = "/v1/funnel"
)
